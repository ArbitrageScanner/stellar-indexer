// Package queue carries raw ledgers between the fetcher and the writer over
// Kafka (Redpanda in the default deployment).
//
// The topic holds one record per ledger: key = 8-byte big-endian sequence,
// value = LedgerCloseMeta XDR (zstd-compressed by the producer). Raw XDR, not
// decoded rows, is the contract on the bus: it is the immutable source of
// truth, so a decoder fix can be replayed from the topic and other consumers
// can be added without touching the fetcher. The topic has a single
// partition: ledgers must be applied in order, and one ledger every ~5s is far
// below a partition's throughput. Delivery is at-least-once; the writer's
// sink is idempotent, which makes the pipeline effectively exactly-once.
package queue

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	// SchemaLCMv1 identifies the record value format.
	SchemaLCMv1 = "lcm-xdr-v1"
	// MaxRecordBytes bounds one ledger record; pubnet ledgers are 1-5 MB of XDR.
	MaxRecordBytes = 32 << 20
)

type Config struct {
	Brokers           []string
	Topic             string
	Group             string
	Retention         time.Duration // topic retention (replay window)
	RetentionBytes    int64         // 0 = unlimited
	ReplicationFactor int16
	// Network is the network passphrase; records are tagged with its hash so a
	// writer never ingests another network's ledgers by misconfiguration.
	Network string
}

// NetworkID is a short stable tag of a passphrase.
func NetworkID(passphrase string) string {
	s := sha256.Sum256([]byte(passphrase))
	return hex.EncodeToString(s[:8])
}

func key(seq uint32) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(seq))
	return b
}

// SeqOf decodes a record key.
func SeqOf(k []byte) (uint32, error) {
	if len(k) != 8 {
		return 0, fmt.Errorf("record key has %d bytes, want 8", len(k))
	}
	v := binary.BigEndian.Uint64(k)
	if v > uint64(^uint32(0)) {
		return 0, fmt.Errorf("record key %d is not a ledger sequence", v)
	}
	return uint32(v), nil
}

func header(r *kgo.Record, k string) string {
	for _, h := range r.Headers {
		if h.Key == k {
			return string(h.Value)
		}
	}
	return ""
}

// EnsureTopic creates the topic (one partition) if missing and checks that an
// existing one still has exactly one partition.
func EnsureTopic(ctx context.Context, cl *kgo.Client, cfg Config) error {
	adm := kadm.NewClient(cl)
	details, err := adm.ListTopics(ctx, cfg.Topic)
	if err != nil {
		return fmt.Errorf("list topics: %w", err)
	}
	if d, ok := details[cfg.Topic]; ok && d.Err == nil {
		if n := len(d.Partitions); n != 1 {
			return fmt.Errorf("topic %s has %d partitions; ledgers need a single ordered partition", cfg.Topic, n)
		}
		return nil
	}
	rf := cfg.ReplicationFactor
	if rf <= 0 {
		rf = 1
	}
	configs := map[string]*string{
		"max.message.bytes": ptr(strconv.Itoa(MaxRecordBytes)),
		"retention.ms":      ptr(strconv.FormatInt(cfg.Retention.Milliseconds(), 10)),
		"cleanup.policy":    ptr("delete"),
	}
	if cfg.RetentionBytes > 0 {
		configs["retention.bytes"] = ptr(strconv.FormatInt(cfg.RetentionBytes, 10))
	}
	resp, err := adm.CreateTopic(ctx, 1, rf, configs, cfg.Topic)
	if err != nil && !errors.Is(err, kerr.TopicAlreadyExists) {
		return fmt.Errorf("create topic %s: %w", cfg.Topic, err)
	}
	if resp.Err != nil && !errors.Is(resp.Err, kerr.TopicAlreadyExists) {
		return fmt.Errorf("create topic %s: %w", cfg.Topic, resp.Err)
	}
	// Wait until the new partition serves offset requests.
	for i := 0; i < 50; i++ {
		ends, err := adm.ListEndOffsets(ctx, cfg.Topic)
		if err == nil {
			if o, ok := ends.Lookup(cfg.Topic, 0); ok && o.Err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("topic %s not visible after creation", cfg.Topic)
}

func ptr(s string) *string { return &s }

// ---- producer ---------------------------------------------------------------

type Producer struct {
	cl  *kgo.Client
	cfg Config
	net string
}

func NewProducer(ctx context.Context, cfg Config) (*Producer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.DefaultProduceTopic(cfg.Topic),
		kgo.RequiredAcks(kgo.AllISRAcks()), // idempotent producer needs acks=all (franz-go default)
		kgo.ProducerBatchCompression(kgo.ZstdCompression(), kgo.SnappyCompression(), kgo.NoCompression()),
		kgo.ProducerBatchMaxBytes(MaxRecordBytes),
		kgo.MaxBufferedRecords(64),
		kgo.RecordRetries(10),
		kgo.ProduceRequestTimeout(30*time.Second),
	)
	if err != nil {
		return nil, err
	}
	if err := EnsureTopic(ctx, cl, cfg); err != nil {
		cl.Close()
		return nil, err
	}
	return &Producer{cl: cl, cfg: cfg, net: NetworkID(cfg.Network)}, nil
}

func (p *Producer) Close() { p.cl.Close() }

// Produce writes one ledger and waits for the broker acknowledgement, so the
// fetcher only advances past ledgers that are durably on the topic.
func (p *Producer) Produce(ctx context.Context, seq uint32, closedAt time.Time, xdr []byte) error {
	r := &kgo.Record{Key: key(seq), Value: xdr, Headers: []kgo.RecordHeader{
		{Key: "schema", Value: []byte(SchemaLCMv1)},
		{Key: "network", Value: []byte(p.net)},
		{Key: "closed_at", Value: []byte(closedAt.UTC().Format(time.RFC3339))},
	}}
	return p.cl.ProduceSync(ctx, r).FirstErr()
}

// LastSequence returns the ledger of the newest record on the topic (0 when
// empty), which is where a restarted fetcher resumes.
func (p *Producer) LastSequence(ctx context.Context) (uint32, error) {
	adm := kadm.NewClient(p.cl)
	ends, err := adm.ListEndOffsets(ctx, p.cfg.Topic)
	if err != nil {
		return 0, err
	}
	o, ok := ends.Lookup(p.cfg.Topic, 0)
	if !ok || o.Err != nil {
		if o.Err != nil {
			return 0, o.Err
		}
		return 0, fmt.Errorf("topic %s partition 0 not found", p.cfg.Topic)
	}
	if o.Offset == 0 {
		return 0, nil
	}
	starts, err := adm.ListStartOffsets(ctx, p.cfg.Topic)
	if err != nil {
		return 0, err
	}
	if s, ok := starts.Lookup(p.cfg.Topic, 0); ok && s.Offset >= o.Offset {
		return 0, nil // everything expired by retention
	}
	rd, err := kgo.NewClient(kgo.SeedBrokers(p.cfg.Brokers...),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{p.cfg.Topic: {0: kgo.NewOffset().At(o.Offset - 1)}}),
		kgo.FetchMaxBytes(MaxRecordBytes*2), kgo.FetchMaxPartitionBytes(MaxRecordBytes))
	if err != nil {
		return 0, err
	}
	defer rd.Close()
	for {
		f := rd.PollRecords(ctx, 1)
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if errs := f.Errors(); len(errs) > 0 {
			return 0, errs[0].Err
		}
		recs := f.Records()
		if len(recs) == 0 {
			continue
		}
		// Transaction/control records are not used, so the record at end-1 is ours.
		return SeqOf(recs[len(recs)-1].Key)
	}
}

// ---- consumer ----------------------------------------------------------------

// Message is one decoded record. Rejected is set (wrapping ErrForeignRecord)
// for records that are not ledgers of this network; they carry no payload but
// must still be committed past.
type Message struct {
	Seq      uint32
	XDR      []byte
	ClosedAt time.Time
	Rejected error
	rec      *kgo.Record
}

type Consumer struct {
	cl  *kgo.Client
	cfg Config
	net string
	lag func(int64)
}

// NewConsumer joins cfg.Group. With several writers in the group only one owns
// the partition; the others are hot standbys.
func NewConsumer(ctx context.Context, cfg Config, lag func(int64)) (*Consumer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.DisableAutoCommit(), // offsets are committed only after the DB commit
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchMaxBytes(MaxRecordBytes*2),
		kgo.FetchMaxPartitionBytes(MaxRecordBytes),
		kgo.FetchMaxWait(time.Second),
	)
	if err != nil {
		return nil, err
	}
	if err := EnsureTopic(ctx, cl, cfg); err != nil {
		cl.Close()
		return nil, err
	}
	return &Consumer{cl: cl, cfg: cfg, net: NetworkID(cfg.Network), lag: lag}, nil
}

func (c *Consumer) Close() { c.cl.Close() }

// ErrForeignRecord marks a record that does not belong to this pipeline.
var ErrForeignRecord = errors.New("foreign record")

// Poll returns up to max messages, blocking until at least one is available
// or ctx ends.
func (c *Consumer) Poll(ctx context.Context, max int) ([]Message, error) {
	f := c.cl.PollRecords(ctx, max)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, fe := range f.Errors() {
		if !errors.Is(fe.Err, context.Canceled) {
			return nil, fmt.Errorf("fetch %s/%d: %w", fe.Topic, fe.Partition, fe.Err)
		}
	}
	var msgs []Message
	var last *kgo.Record
	var hw int64
	f.EachPartition(func(p kgo.FetchTopicPartition) { hw = p.HighWatermark })
	for _, r := range f.Records() {
		last = r
		seq, kerr := SeqOf(r.Key)
		var rej error
		switch {
		case kerr != nil:
			rej = fmt.Errorf("%w: offset %d: %w", ErrForeignRecord, r.Offset, kerr)
		case header(r, "schema") != SchemaLCMv1:
			rej = fmt.Errorf("%w: offset %d: schema %q", ErrForeignRecord, r.Offset, header(r, "schema"))
		case header(r, "network") != c.net:
			rej = fmt.Errorf("%w: offset %d: ledger %d is from network %q, expected %q", ErrForeignRecord, r.Offset, seq, header(r, "network"), c.net)
		}
		if rej != nil {
			msgs = append(msgs, Message{Rejected: rej, rec: r})
			continue
		}
		t, _ := time.Parse(time.RFC3339, header(r, "closed_at"))
		msgs = append(msgs, Message{Seq: seq, XDR: r.Value, ClosedAt: t, rec: r})
	}
	if last != nil && c.lag != nil && hw > 0 {
		c.lag(hw - last.Offset - 1)
	}
	return msgs, nil
}

// Commit marks messages (and everything before them) as processed.
func (c *Consumer) Commit(ctx context.Context, msgs []Message) error {
	recs := make([]*kgo.Record, 0, len(msgs))
	for _, m := range msgs {
		if m.rec != nil {
			recs = append(recs, m.rec)
		}
	}
	if len(recs) == 0 {
		return nil
	}
	return c.cl.CommitRecords(ctx, recs...)
}
