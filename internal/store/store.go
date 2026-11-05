// Package store persists processor batches to Postgres and serves queries.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
)

type Store struct {
	Pool *pgxpool.Pool
	// PartitionLedgers is the ledger-range size used when the partition layout
	// is first created (later runs reuse the stored value).
	PartitionLedgers int64

	parts partitions
}

func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &Store{Pool: pool, parts: partitions{have: map[int64]bool{}}}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// Cursor returns the last fully indexed ledger, or 0 when none.
// (A cursor row with ledger 0 exists while the first batch is in flight.)
func (s *Store) Cursor(ctx context.Context, name string) (uint32, error) {
	var l int64
	err := s.Pool.QueryRow(ctx, "SELECT ledger FROM cursors WHERE name=$1", name).Scan(&l)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return uint32(l), err
}

// LoadAssets feeds persisted SAC mappings into the registry.
func (s *Store) LoadAssets(ctx context.Context, reg *processor.Registry) (int, error) {
	rows, err := s.Pool.Query(ctx, "SELECT asset_key, coalesce(contract_id,'') FROM assets")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var k, c string
		if err := rows.Scan(&k, &c); err != nil {
			return n, err
		}
		reg.Preload(k, c)
		n++
	}
	return n, rows.Err()
}

// WriteStats reports what a write did.
type WriteStats struct {
	Ledgers, Skipped, Rows, Gaps int
}

// ErrChainMismatch means a ledger's prev_hash does not match the stored hash
// of its predecessor: the source served inconsistent data. Stellar has no
// reorgs, so this is never retried automatically.
var ErrChainMismatch = errors.New("ledger chain mismatch")

// Write stores batches (ascending ledgers) and advances the cursor in one
// transaction, so a crash at any point leaves either all or none of a batch.
// It is idempotent: ledgers already present are skipped, which keeps
// redelivered queue records and re-runs from double-counting candle volume.
// Each new ledger must chain onto its stored predecessor (ErrChainMismatch
// otherwise); ranges that were never received are recorded in ledger_gaps.
func (s *Store) Write(ctx context.Context, cursor string, batches []*processor.Batch) (WriteStats, error) {
	var st WriteStats
	if len(batches) == 0 {
		return st, nil
	}
	// Partition DDL runs outside the write transaction; ledgers at or below
	// the cursor are skipped below, so their (possibly dropped) ranges are not
	// recreated.
	done, err := s.Cursor(ctx, cursor)
	if err != nil {
		return st, err
	}
	for _, b := range []*processor.Batch{batches[0], batches[len(batches)-1]} {
		if b.Ledger.Sequence > done {
			if err := s.EnsurePartitions(ctx, max(b.Ledger.Sequence, done+1)); err != nil {
				return st, err
			}
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return st, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the cursor row: a second writer on the same cursor serialises here.
	var cur int64
	if _, err := tx.Exec(ctx, `INSERT INTO cursors (name, ledger) VALUES ($1, 0) ON CONFLICT (name) DO NOTHING`, cursor); err != nil {
		return st, err
	}
	if err := tx.QueryRow(ctx, `SELECT ledger FROM cursors WHERE name = $1 FOR UPDATE`, cursor).Scan(&cur); err != nil {
		return st, err
	}
	prevHash := map[uint32]string{}
	for _, b := range batches {
		seq := b.Ledger.Sequence
		// Everything at or below the cursor was committed earlier (or pruned
		// since): redelivery is a no-op.
		if int64(seq) <= cur {
			st.Skipped++
			continue
		}
		if err := checkChain(ctx, tx, b.Ledger, prevHash); err != nil {
			return st, err
		}
		if cur > 0 && int64(seq) > cur+1 {
			if _, err := tx.Exec(ctx, `INSERT INTO ledger_gaps (from_ledger, to_ledger, reason) VALUES ($1, $2, $3)
				ON CONFLICT (from_ledger) DO NOTHING`, cur+1, int64(seq)-1, "ledgers never received (source outage longer than RPC retention?)"); err != nil {
				return st, err
			}
			st.Gaps++
		}
		cur = int64(seq)
		prevHash[seq] = b.Ledger.Hash
		ok, err := insertLedger(ctx, tx, b.Ledger)
		if err != nil {
			return st, fmt.Errorf("ledger %d: %w", b.Ledger.Sequence, err)
		}
		if !ok {
			st.Skipped++
			continue
		}
		n, err := writeBatch(ctx, tx, b)
		if err != nil {
			return st, fmt.Errorf("ledger %d: %w", b.Ledger.Sequence, err)
		}
		st.Ledgers++
		st.Rows += n
	}
	last := batches[len(batches)-1].Ledger
	if _, err = tx.Exec(ctx, `UPDATE cursors SET ledger = GREATEST(ledger, $2), updated_at = now() WHERE name = $1`, cursor, int64(last.Sequence)); err != nil {
		return st, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO components (name, role, ledger, info, updated_at) VALUES ($1, 'writer', $2, $3, now())
		ON CONFLICT (name) DO UPDATE SET ledger = GREATEST(components.ledger, EXCLUDED.ledger), info = EXCLUDED.info, updated_at = now()`,
		"writer:"+cursor, int64(last.Sequence), jsonb(map[string]any{"closed_at": last.ClosedAt, "ledgers": st.Ledgers, "skipped": st.Skipped})); err != nil {
		return st, err
	}
	return st, tx.Commit(ctx)
}

// checkChain verifies l.PrevHash against the predecessor written earlier in
// this transaction or stored in the table.
func checkChain(ctx context.Context, tx pgx.Tx, l processor.Ledger, inBatch map[uint32]string) error {
	want, ok := inBatch[l.Sequence-1]
	if !ok {
		err := tx.QueryRow(ctx, `SELECT hash FROM ledgers WHERE sequence = $1`, int64(l.Sequence)-1).Scan(&want)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // first ledger, or predecessor never received / pruned
		}
		if err != nil {
			return err
		}
	}
	if want != l.PrevHash {
		return fmt.Errorf("%w: ledger %d prev_hash %s, stored ledger %d hash %s", ErrChainMismatch, l.Sequence, l.PrevHash, l.Sequence-1, want)
	}
	return nil
}

// Heartbeat records a pipeline component's progress for /status and /ready.
func (s *Store) Heartbeat(ctx context.Context, name, role string, ledger, tip uint32, info map[string]any) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO components (name, role, ledger, tip, info, updated_at) VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (name) DO UPDATE SET role = EXCLUDED.role, ledger = EXCLUDED.ledger, tip = EXCLUDED.tip, info = EXCLUDED.info, updated_at = now()`,
		name, role, int64(ledger), int64(tip), jsonb(info))
	return err
}

// LoadCodes returns the persisted contract executables (verifier cache).
func (s *Store) LoadCodes(ctx context.Context) ([]processor.ContractCode, error) {
	rows, err := s.Pool.Query(ctx, `SELECT contract_id, executable, coalesce(wasm_hash, ''), ledger FROM contract_code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []processor.ContractCode
	for rows.Next() {
		var c processor.ContractCode
		var l int64
		if err := rows.Scan(&c.ContractID, &c.Executable, &c.WasmHash, &l); err != nil {
			return nil, err
		}
		c.Ledger = uint32(l)
		out = append(out, c)
	}
	return out, rows.Err()
}

func insertLedger(ctx context.Context, tx pgx.Tx, l processor.Ledger) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO ledgers (sequence, hash, prev_hash, closed_at, protocol, tx_count, tx_success,
		tx_failed, op_count, soroban_tx_count, base_fee, base_reserve, total_coins, fee_pool, fees_charged)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT (sequence) DO NOTHING`,
		int64(l.Sequence), l.Hash, l.PrevHash, l.ClosedAt, int32(l.Protocol), l.TxCount, l.TxSuccess, l.TxFailed,
		l.OpCount, l.SorobanTxCount, int64(l.BaseFee), int64(l.BaseReserve), l.TotalCoins, l.FeePool, l.FeesCharged)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func writeBatch(ctx context.Context, tx pgx.Tx, b *processor.Batch) (int, error) {
	total := 0
	copyRows := func(table string, cols []string, n int, row func(i int) []any) error {
		if n == 0 {
			return nil
		}
		c, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromSlice(n, func(i int) ([]any, error) { return row(i), nil }))
		total += int(c)
		if err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
		return nil
	}

	if err := copyRows("transactions", []string{"hash", "ledger", "tx_index", "id", "closed_at", "source", "fee_account", "seq_num",
		"fee_charged", "max_fee", "op_count", "successful", "result_code", "memo_type", "memo", "is_soroban", "contract_id",
		"function", "resource_fee", "instructions", "inner_hash"}, len(b.Transactions), func(i int) []any {
		t := b.Transactions[i]
		return []any{t.Hash, int64(t.Ledger), int32(t.Index), t.ID, t.ClosedAt, t.Source, t.FeeAccount, t.SeqNum, t.FeeCharged,
			t.MaxFee, int32(t.OpCount), t.Successful, t.ResultCode, t.MemoType, sanitize(t.Memo), t.IsSoroban, null(t.ContractID),
			null(t.Function), t.ResourceFee, int64(t.Instructions), null(t.InnerHash)}
	}); err != nil {
		return total, err
	}

	if err := copyRows("operations", []string{"id", "tx_hash", "ledger", "tx_index", "op_index", "closed_at", "type", "source",
		"successful", "result_code", "details"}, len(b.Operations), func(i int) []any {
		o := b.Operations[i]
		return []any{o.ID, o.TxHash, int64(o.Ledger), int32(o.TxIndex), int32(o.Index), o.ClosedAt, o.Type, o.Source,
			o.Successful, o.ResultCode, jsonb(o.Details)}
	}); err != nil {
		return total, err
	}

	if err := copyRows("trades", []string{"ledger", "tx_index", "op_index", "trade_index", "tx_hash", "closed_at", "venue", "pool",
		"maker", "taker", "sold_asset", "sold_amount", "bought_asset", "bought_amount", "base", "counter", "price",
		"base_volume", "counter_volume", "verified"}, len(b.Trades), func(i int) []any {
		t := b.Trades[i]
		tk, _ := processor.TickOf(t)
		return []any{int64(t.Ledger), int32(t.TxIndex), int32(t.OpIndex), int32(t.Index), t.TxHash, t.ClosedAt, t.Venue, t.Pool,
			t.Maker, t.Taker, t.SoldAsset, num(t.SoldAmount), t.BoughtAsset, num(t.BoughtAmount), tk.Base, tk.Counter, tk.Price,
			tk.BaseVol, tk.CountVol, t.Verified}
	}); err != nil {
		return total, err
	}

	if err := copyRows("transfers", []string{"ledger", "tx_index", "transfer_index", "op_index", "tx_hash", "closed_at", "type",
		"from_addr", "to_addr", "asset", "contract_id", "amount"}, len(b.Transfers), func(i int) []any {
		t := b.Transfers[i]
		var op any
		if t.OpIndex != nil {
			op = int32(*t.OpIndex)
		}
		return []any{int64(t.Ledger), int32(t.TxIndex), int32(t.Index), op, t.TxHash, t.ClosedAt, t.Type, null(t.From), null(t.To),
			t.Asset, null(t.ContractID), num(t.Amount)}
	}); err != nil {
		return total, err
	}

	if err := copyRows("contract_events", []string{"ledger", "tx_index", "op_index", "event_index", "tx_hash", "closed_at",
		"contract_id", "topic0", "topics", "data", "protocol", "verified", "successful"}, len(b.ContractEvents), func(i int) []any {
		e := b.ContractEvents[i]
		return []any{int64(e.Ledger), int32(e.TxIndex), int32(e.OpIndex), int32(e.Index), e.TxHash, e.ClosedAt, e.ContractID,
			sanitize(e.Topic0), jsonb(e.Topics), jsonb(e.Data), null(e.Protocol), e.Verified, e.Successful}
	}); err != nil {
		return total, err
	}

	if err := copyRows("protocol_events", []string{"ledger", "tx_index", "op_index", "event_index", "tx_hash", "closed_at",
		"protocol", "contract_id", "action", "user_addr", "asset", "amount0", "amount1", "data", "verified"}, len(b.ProtocolEvents), func(i int) []any {
		e := b.ProtocolEvents[i]
		return []any{int64(e.Ledger), int32(e.TxIndex), int32(e.OpIndex), int32(e.Index), e.TxHash, e.ClosedAt, e.Protocol,
			e.ContractID, sanitize(e.Action), null(e.User), null(e.Asset), num(e.Amount0), num(e.Amount1), jsonb(e.Data), e.Verified}
	}); err != nil {
		return total, err
	}

	pb := &pgx.Batch{}
	for _, a := range b.Assets {
		pb.Queue(`INSERT INTO assets (asset_key, type, code, issuer, contract_id, first_seen) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (asset_key) DO NOTHING`, a.Key, a.Type, null(sanitize(a.Code)), null(a.Issuer), null(a.ContractID), int64(a.FirstSeen))
	}
	for _, a := range b.Accounts {
		pb.Queue(`INSERT INTO accounts (account_id, balance, buying_liab, selling_liab, seq_num, num_subentries, num_sponsoring,
			num_sponsored, flags, home_domain, master_weight, num_signers, sponsor, last_modified, deleted)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (account_id) DO UPDATE SET balance=EXCLUDED.balance, buying_liab=EXCLUDED.buying_liab,
			selling_liab=EXCLUDED.selling_liab, seq_num=EXCLUDED.seq_num, num_subentries=EXCLUDED.num_subentries,
			num_sponsoring=EXCLUDED.num_sponsoring, num_sponsored=EXCLUDED.num_sponsored, flags=EXCLUDED.flags,
			home_domain=EXCLUDED.home_domain, master_weight=EXCLUDED.master_weight, num_signers=EXCLUDED.num_signers,
			sponsor=EXCLUDED.sponsor, last_modified=EXCLUDED.last_modified, deleted=EXCLUDED.deleted
			WHERE accounts.last_modified <= EXCLUDED.last_modified`,
			a.ID, a.Balance, a.BuyingLiab, a.SellingLiab, a.SeqNum, int32(a.NumSubentries), int32(a.NumSponsoring),
			int32(a.NumSponsored), int64(a.Flags), sanitize(a.HomeDomain), int32(a.MasterWeight), a.NumSigners, null(a.Sponsor),
			int64(a.LastModified), a.Deleted)
	}
	for _, t := range b.Trustlines {
		if t.Deleted {
			pb.Queue(`DELETE FROM trustlines WHERE account_id=$1 AND asset=$2`, t.AccountID, t.Asset)
			continue
		}
		pb.Queue(`INSERT INTO trustlines (account_id, asset, balance, trust_limit, buying_liab, selling_liab, flags, last_modified)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (account_id, asset) DO UPDATE SET balance=EXCLUDED.balance,
			trust_limit=EXCLUDED.trust_limit, buying_liab=EXCLUDED.buying_liab, selling_liab=EXCLUDED.selling_liab,
			flags=EXCLUDED.flags, last_modified=EXCLUDED.last_modified WHERE trustlines.last_modified <= EXCLUDED.last_modified`,
			t.AccountID, t.Asset, t.Balance, t.Limit, t.BuyingLiab, t.SellingLiab, int64(t.Flags), int64(t.LastModified))
	}
	for _, p := range b.Pools {
		pb.Queue(`INSERT INTO liquidity_pools (pool_id, asset_a, asset_b, reserve_a, reserve_b, total_shares, trustline_count,
			fee_bp, last_modified, deleted) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (pool_id) DO UPDATE SET reserve_a=EXCLUDED.reserve_a, reserve_b=EXCLUDED.reserve_b,
			total_shares=EXCLUDED.total_shares, trustline_count=EXCLUDED.trustline_count, last_modified=EXCLUDED.last_modified,
			deleted=EXCLUDED.deleted WHERE liquidity_pools.last_modified <= EXCLUDED.last_modified`,
			p.ID, p.AssetA, p.AssetB, p.ReserveA, p.ReserveB, p.TotalShares, p.TrustlineCount, p.FeeBP, int64(p.LastModified), p.Deleted)
	}
	for _, o := range b.Offers {
		if o.Deleted {
			pb.Queue(`DELETE FROM offers WHERE offer_id=$1`, o.ID)
			continue
		}
		pr := 0.0
		if o.PriceD != 0 {
			pr = float64(o.PriceN) / float64(o.PriceD)
		}
		pb.Queue(`INSERT INTO offers (offer_id, seller, selling, buying, amount, price_n, price_d, price, passive, last_modified)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (offer_id) DO UPDATE SET amount=EXCLUDED.amount,
			price_n=EXCLUDED.price_n, price_d=EXCLUDED.price_d, price=EXCLUDED.price, last_modified=EXCLUDED.last_modified
			WHERE offers.last_modified <= EXCLUDED.last_modified`,
			o.ID, o.Seller, o.Selling, o.Buying, o.Amount, o.PriceN, o.PriceD, pr, o.Passive, int64(o.LastModified))
	}
	for _, c := range b.Codes {
		pb.Queue(`INSERT INTO contract_code (contract_id, executable, wasm_hash, ledger, checked_at) VALUES ($1,$2,$3,$4,now())
			ON CONFLICT (contract_id) DO UPDATE SET executable=EXCLUDED.executable, wasm_hash=EXCLUDED.wasm_hash,
			ledger=EXCLUDED.ledger, checked_at=now() WHERE contract_code.ledger <= EXCLUDED.ledger`,
			c.ContractID, c.Executable, null(c.WasmHash), int64(c.Ledger))
	}
	for _, c := range Candles(b.Trades) {
		pb.Queue(`INSERT INTO candles_1m (base, counter, bucket, open, high, low, close, base_volume, counter_volume, trades)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (base, counter, bucket) DO UPDATE SET
			high=GREATEST(candles_1m.high, EXCLUDED.high), low=LEAST(candles_1m.low, EXCLUDED.low), close=EXCLUDED.close,
			base_volume=candles_1m.base_volume+EXCLUDED.base_volume,
			counter_volume=candles_1m.counter_volume+EXCLUDED.counter_volume, trades=candles_1m.trades+EXCLUDED.trades`,
			c.Base, c.Counter, c.Bucket, c.Open, c.High, c.Low, c.Close, c.BaseVol, c.CountVol, c.Trades)
	}
	if pb.Len() > 0 {
		res := tx.SendBatch(ctx, pb)
		for i := 0; i < pb.Len(); i++ {
			if _, err := res.Exec(); err != nil {
				_ = res.Close()
				return total, fmt.Errorf("state upsert: %w", err)
			}
		}
		if err := res.Close(); err != nil {
			return total, err
		}
		total += pb.Len()
	}
	return total, nil
}

// Candle is a 1-minute OHLCV aggregate for one canonical pair.
type Candle struct {
	Base, Counter          string
	Bucket                 time.Time
	Open, High, Low, Close float64
	BaseVol, CountVol      float64
	Trades                 int
}

// Candles aggregates verified trades (in execution order) into 1-minute
// candles. Unverified fills (unknown pool code) never move prices.
func Candles(trades []processor.Trade) []Candle {
	type key struct {
		b, c string
		t    time.Time
	}
	idx := map[key]int{}
	var out []Candle
	for _, t := range trades {
		if !t.Verified {
			continue
		}
		tk, ok := processor.TickOf(t)
		if !ok || tk.Price <= 0 {
			continue
		}
		k := key{tk.Base, tk.Counter, t.ClosedAt.Truncate(time.Minute)}
		i, seen := idx[k]
		if !seen {
			idx[k] = len(out)
			out = append(out, Candle{Base: tk.Base, Counter: tk.Counter, Bucket: k.t,
				Open: tk.Price, High: tk.Price, Low: tk.Price, Close: tk.Price,
				BaseVol: tk.BaseVol, CountVol: tk.CountVol, Trades: 1})
			continue
		}
		c := &out[i]
		if tk.Price > c.High {
			c.High = tk.Price
		}
		if tk.Price < c.Low {
			c.Low = tk.Price
		}
		c.Close = tk.Price
		c.BaseVol += tk.BaseVol
		c.CountVol += tk.CountVol
		c.Trades++
	}
	// Deterministic upsert order avoids deadlocks between concurrent writers.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Base != out[j].Base {
			return out[i].Base < out[j].Base
		}
		if out[i].Counter != out[j].Counter {
			return out[i].Counter < out[j].Counter
		}
		return out[i].Bucket.Before(out[j].Bucket)
	})
	return out
}

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func num(s string) any {
	if s == "" {
		return nil
	}
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return nil
	}
	return n
}

func jsonb(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	// jsonb rejects the \u0000 escape that json.Marshal emits for NUL.
	return []byte(strings.ReplaceAll(string(b), `\u0000`, ""))
}

// sanitize makes arbitrary on-chain bytes (memos, symbols, home domains)
// storable as Postgres text: valid UTF-8 and no NUL bytes.
func sanitize(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return s
}
