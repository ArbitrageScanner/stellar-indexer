package ingest_test

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ArbitrageScanner/stellar-indexer/internal/ingest"
	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
	"github.com/ArbitrageScanner/stellar-indexer/internal/queue"
	"github.com/ArbitrageScanner/stellar-indexer/internal/store"
)

// End-to-end pipeline tests: fixture ledgers → fetcher → Kafka (in-memory
// kfake cluster) → writer → Postgres. Postgres via TEST_DATABASE_URL.

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func setupDB(t *testing.T) *store.Store {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("ptest_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.PartitionLedgers = 1000
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func fixture(t *testing.T, seq uint32) xdr.LedgerCloseMeta {
	f, err := os.Open(fmt.Sprintf("../processor/testdata/lcm_%d.b64.gz", seq))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, _ := gzip.NewReader(f)
	raw, _ := io.ReadAll(zr)
	bin, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var lcm xdr.LedgerCloseMeta
	if err := xdr.SafeUnmarshal(bin, &lcm); err != nil {
		t.Fatal(err)
	}
	return lcm
}

// fakeSource serves fixture ledgers; GetLedger blocks past the last one, like
// a live RPC tail.
type fakeSource struct {
	ledgers map[uint32]xdr.LedgerCloseMeta
	order   []uint32
	pos     int
}

func (s *fakeSource) Prepare(_ context.Context, from, _ uint32) error {
	s.pos = len(s.order)
	for i, q := range s.order {
		if q >= from {
			s.pos = i
			break
		}
	}
	return nil
}

func (s *fakeSource) GetLedger(ctx context.Context, _ uint32) (xdr.LedgerCloseMeta, error) {
	if s.pos >= len(s.order) {
		<-ctx.Done()
		return xdr.LedgerCloseMeta{}, ctx.Err()
	}
	l := s.ledgers[s.order[s.pos]]
	s.pos++
	return l, nil
}

func (s *fakeSource) Window(context.Context) (uint32, uint32, error) {
	return s.order[0], s.order[len(s.order)-1], nil
}
func (s *fakeSource) Close() error { return nil }

// newSource serves the given fixture ledgers (gaps between them are fine).
func newSource(t *testing.T, seqs ...uint32) *fakeSource {
	s := &fakeSource{ledgers: map[uint32]xdr.LedgerCloseMeta{}}
	for _, q := range seqs {
		s.ledgers[q] = fixture(t, q)
		s.order = append(s.order, q)
	}
	return s
}

func qcfg(brokers []string, group string) queue.Config {
	return queue.Config{Brokers: brokers, Topic: "stellar.ledgers", Group: group, Retention: time.Hour, Network: network.PublicNetworkPassphrase}
}

func newProc() *processor.Processor {
	reg := processor.NewRegistry(network.PublicNetworkPassphrase)
	return processor.New(network.PublicNetworkPassphrase, reg, processor.Options{Operations: true, State: true, Codes: true})
}

// runUntil runs fn in the background until cond holds (or times out), then
// cancels it and waits for it to return.
func runUntil(t *testing.T, fn func(ctx context.Context) error, cond func() bool) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var err error
	wg.Add(1)
	go func() { defer wg.Done(); err = fn(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	if !cond() {
		t.Fatal("condition not reached within 30s")
	}
	return err
}

func count(t *testing.T, db *store.Store, q string) int {
	var n int
	if err := db.Pool.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestQueuePipeline(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	brokers := cluster.ListenAddrs()

	prod, err := queue.NewProducer(ctx, qcfg(brokers, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	fetcher := &ingest.Fetcher{Src: newSource(t, 65311337, 65312802), Producer: prod, Log: quiet, StartLedger: "oldest", Backoff: 10 * time.Millisecond}
	_ = runUntil(t, fetcher.Run, func() bool { s, _ := prod.LastSequence(ctx); return s == 65312802 })

	// Restarting the fetcher resumes after the topic end: nothing is produced twice.
	_ = runUntil(t, fetcher.Run, func() bool { return true })
	if n := topicRecords(t, brokers); n != 2 {
		t.Fatalf("topic has %d records after fetcher restart, want 2", n)
	}

	newWriter := func() (*ingest.QueueWriter, *queue.Consumer) {
		cons, err := queue.NewConsumer(ctx, qcfg(brokers, "writer"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return &ingest.QueueWriter{Consumer: cons, Proc: newProc(), Store: db, Log: quiet, CursorName: "main", BatchSize: 10, Backoff: 10 * time.Millisecond}, cons
	}
	w, cons := newWriter()
	_ = runUntil(t, w.Run, func() bool { c, _ := db.Cursor(ctx, "main"); return c == 65312802 })
	cons.Close()
	ledgers := count(t, db, `SELECT count(*) FROM ledgers`)
	trades := count(t, db, `SELECT count(*) FROM trades`)
	candleTrades := count(t, db, `SELECT coalesce(sum(trades), 0) FROM candles_1m`)
	if ledgers != 2 || trades == 0 || candleTrades == 0 {
		t.Fatalf("after first write: ledgers %d trades %d candle trades %d", ledgers, trades, candleTrades)
	}
	if g := count(t, db, `SELECT count(*) FROM ledger_gaps WHERE from_ledger = 65311338 AND to_ledger = 65312801`); g != 1 {
		t.Fatalf("gap between the fixtures not recorded (%d)", g)
	}
	if p := count(t, db, `SELECT count(*) FROM pg_inherits WHERE inhparent = to_regclass('trades')`); p < 2 {
		t.Fatalf("expected trades partitions, got %d", p)
	}

	// Redelivery: a duplicate record plus a foreign-network record. The writer
	// skips the duplicate (no double-counted candles) and ignores the foreign one.
	lcm := fixture(t, 65312802)
	raw, _ := lcm.MarshalBinary()
	if err := prod.Produce(ctx, 65312802, lcm.ClosedAt(), raw); err != nil {
		t.Fatal(err)
	}
	foreign, err := queue.NewProducer(ctx, queue.Config{Brokers: brokers, Topic: "stellar.ledgers", Retention: time.Hour, Network: network.TestNetworkPassphrase})
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.Produce(ctx, 65312803, lcm.ClosedAt(), raw); err != nil {
		t.Fatal(err)
	}
	foreign.Close()
	w, cons = newWriter()
	err = runUntil(t, w.Run, func() bool { return groupLag(t, brokers) == 0 })
	cons.Close()
	if err != nil {
		t.Fatalf("writer stopped: %v", err)
	}
	if n := count(t, db, `SELECT count(*) FROM ledgers`); n != 2 {
		t.Fatalf("ledgers after redelivery: %d", n)
	}
	if n := count(t, db, `SELECT coalesce(sum(trades), 0) FROM candles_1m`); n != candleTrades {
		t.Fatalf("candle volume double-counted: %d -> %d", candleTrades, n)
	}
	if c, _ := db.Cursor(ctx, "main"); c != 65312802 {
		t.Fatalf("foreign record advanced the cursor to %d", c)
	}
}

func topicRecords(t *testing.T, brokers []string) int {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics("stellar.ledgers"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()), kgo.FetchMaxWait(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	n := 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		f := cl.PollFetches(ctx)
		n += len(f.Records())
	}
	return n
}

func groupLag(t *testing.T, brokers []string) int64 {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx := context.Background()
	end, err := endOffset(ctx, cl)
	if err != nil {
		return -1
	}
	committed, err := committedOffset(ctx, cl, "writer")
	if err != nil {
		return -1
	}
	return end - committed
}

func TestChainMismatchStopsWriter(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	b := &processor.Batch{Ledger: processor.Ledger{Sequence: 100, Hash: "aa", PrevHash: "99", ClosedAt: time.Now()}}
	if _, err := db.Write(ctx, "main", []*processor.Batch{b}); err != nil {
		t.Fatal(err)
	}
	bad := &processor.Batch{Ledger: processor.Ledger{Sequence: 101, Hash: "bb", PrevHash: "not-aa", ClosedAt: time.Now()}}
	_, err := db.Write(ctx, "main", []*processor.Batch{bad})
	if !errors.Is(err, store.ErrChainMismatch) {
		t.Fatalf("want ErrChainMismatch, got %v", err)
	}
	good := &processor.Batch{Ledger: processor.Ledger{Sequence: 101, Hash: "bb", PrevHash: "aa", ClosedAt: time.Now()}}
	if _, err := db.Write(ctx, "main", []*processor.Batch{good}); err != nil {
		t.Fatalf("valid successor rejected: %v", err)
	}
}

func TestPartitionRetention(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	for _, seq := range []uint32{100, 1100, 2100, 3100} {
		b := &processor.Batch{Ledger: processor.Ledger{Sequence: seq, Hash: fmt.Sprint(seq), ClosedAt: time.Now()}}
		if _, err := db.Write(ctx, "main", []*processor.Batch{b}); err != nil {
			t.Fatal(err)
		}
	}
	before := count(t, db, `SELECT count(*) FROM pg_inherits WHERE inhparent = to_regclass('transactions')`)
	out, err := db.Prune(ctx, "main", store.Retention{Raw: 1500})
	if err != nil {
		t.Fatal(err)
	}
	after := count(t, db, `SELECT count(*) FROM pg_inherits WHERE inhparent = to_regclass('transactions')`)
	// cutoff = 3100-1500 = 1600: partitions [0,1000) dropped, [1000,2000) kept.
	if out["transactions_partitions"] != 1 || after != before-1 {
		t.Fatalf("prune: %v, partitions %d -> %d", out, before, after)
	}
	// A late redelivery of a pruned ledger must not resurrect its partition.
	old := &processor.Batch{Ledger: processor.Ledger{Sequence: 150, Hash: "x", ClosedAt: time.Now()}}
	st, err := db.Write(ctx, "main", []*processor.Batch{old})
	if err != nil || st.Skipped != 1 {
		t.Fatalf("old ledger: %+v %v", st, err)
	}
	if n := count(t, db, `SELECT count(*) FROM pg_inherits WHERE inhparent = to_regclass('transactions')`); n != after {
		t.Fatalf("pruned partition recreated: %d -> %d", after, n)
	}
}

func endOffset(ctx context.Context, cl *kgo.Client) (int64, error) {
	o, err := kadm.NewClient(cl).ListEndOffsets(ctx, "stellar.ledgers")
	if err != nil {
		return 0, err
	}
	l, ok := o.Lookup("stellar.ledgers", 0)
	if !ok {
		return 0, errors.New("no partition")
	}
	return l.Offset, l.Err
}

func committedOffset(ctx context.Context, cl *kgo.Client, group string) (int64, error) {
	o, err := kadm.NewClient(cl).FetchOffsets(ctx, group)
	if err != nil {
		return 0, err
	}
	l, ok := o.Lookup("stellar.ledgers", 0)
	if !ok {
		return 0, nil
	}
	return l.At, l.Err
}

// The queue-less pipeline resumes from the DB cursor after a restart and
// writes each ledger once.
func TestDirectRunnerResume(t *testing.T) {
	db := setupDB(t)
	ctx := context.Background()
	run := func() error {
		r := &ingest.Runner{Src: newSource(t, 65311337, 65312802), Store: db, Proc: newProc(), Log: quiet, CursorName: "main",
			StartLedger: "oldest", CommitEvery: 1, Backoff: 10 * time.Millisecond} // fixtures are old: not "live"
		return runUntil(t, r.Run, func() bool { c, _ := db.Cursor(ctx, "main"); return c == 65312802 })
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	trades := count(t, db, `SELECT count(*) FROM trades`)
	if err := run(); err != nil { // restart: resumes after the cursor, re-reads nothing
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM trades`); n != trades || trades == 0 {
		t.Fatalf("trades after restart %d, before %d", n, trades)
	}
	if n := count(t, db, `SELECT count(*) FROM components WHERE role = 'writer'`); n != 1 {
		t.Fatalf("writer heartbeat rows: %d", n)
	}
}
