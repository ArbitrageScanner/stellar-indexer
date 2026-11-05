package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
	"github.com/ArbitrageScanner/stellar-indexer/internal/queue"
	"github.com/ArbitrageScanner/stellar-indexer/internal/store"
)

// Consumer is the queue side of the writer.
type Consumer interface {
	Poll(ctx context.Context, max int) ([]queue.Message, error)
	Commit(ctx context.Context, msgs []queue.Message) error
}

// Annotator marks protocol data verified (see package verify).
type Annotator interface {
	Annotate(ctx context.Context, batches []*processor.Batch) error
}

// QueueWriter consumes ledgers from the queue, decodes them and commits them
// to Postgres. Offsets are committed only after the DB transaction, and the
// store skips ledgers at or below its cursor, so crashes and redeliveries are
// harmless. A DB or RPC (code lookup) failure retries the same batch; a broken
// ledger chain or an undecodable ledger stops the writer (the operator must
// look), because skipping would silently corrupt the index.
type QueueWriter struct {
	Consumer   Consumer
	Proc       *processor.Processor
	Verifier   Annotator // optional
	Store      Writer
	Log        *slog.Logger
	CursorName string
	BatchSize  int
	Backoff    time.Duration
}

func (w *QueueWriter) Run(ctx context.Context) error {
	if w.BatchSize < 1 {
		w.BatchSize = 1
	}
	lastLog := time.Now()
	for {
		msgs, err := w.Consumer.Poll(ctx, w.BatchSize)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutdown is not an error
		}
		if err != nil {
			w.Log.Error("queue poll failed", "err", err)
			if !sleep(ctx, w.Backoff) {
				return nil
			}
			continue
		}
		batches, err := w.decode(msgs)
		if err != nil {
			return err
		}
		if len(batches) > 0 {
			st, err := w.write(ctx, batches)
			if err != nil {
				return err
			}
			last := batches[len(batches)-1].Ledger
			metrics.WrittenLedgers.Add(float64(st.Ledgers))
			metrics.SkippedLedgers.Add(float64(st.Skipped))
			metrics.WrittenRows.Add(float64(st.Rows))
			metrics.Gaps.Add(float64(st.Gaps))
			metrics.LastWritten.Set(float64(last.Sequence))
			metrics.CloseLag.Set(time.Since(last.ClosedAt).Seconds())
			if st.Gaps > 0 {
				w.Log.Warn("ledger gap recorded", "before", batches[0].Ledger.Sequence)
			}
			if time.Since(lastLog) > 10*time.Second {
				w.Log.Info("writer progress", "ledger", last.Sequence, "lag", time.Since(last.ClosedAt).Round(time.Second).String(),
					"written", st.Ledgers, "skipped", st.Skipped, "rows", st.Rows)
				lastLog = time.Now()
			}
		}
		// Committing can fail transiently; the records are then redelivered and skipped.
		if err := w.Consumer.Commit(ctx, msgs); err != nil && ctx.Err() == nil {
			w.Log.Warn("offset commit failed; records will be redelivered and skipped", "err", err)
		}
		metrics.Consumed.Add(float64(len(msgs)))
	}
}

func (w *QueueWriter) decode(msgs []queue.Message) ([]*processor.Batch, error) {
	var out []*processor.Batch
	for _, m := range msgs {
		if m.Rejected != nil {
			metrics.RejectedRecords.WithLabelValues("foreign").Inc()
			w.Log.Error("ignoring record", "err", m.Rejected)
			continue
		}
		var lcm xdr.LedgerCloseMeta
		if err := xdr.SafeUnmarshal(m.XDR, &lcm); err != nil {
			metrics.ProcessError.Inc()
			return nil, fmt.Errorf("ledger %d: undecodable record: %w", m.Seq, err)
		}
		if lcm.LedgerSequence() != m.Seq {
			metrics.RejectedRecords.WithLabelValues("key_mismatch").Inc()
			return nil, fmt.Errorf("record key %d holds ledger %d", m.Seq, lcm.LedgerSequence())
		}
		res, err := w.Proc.Process(lcm)
		if err != nil {
			metrics.ProcessError.Inc()
			return nil, err
		}
		for _, warn := range res.Warnings {
			w.Log.Debug("decode warning", "msg", warn)
		}
		out = append(out, res.Batch)
	}
	return out, nil
}

// write annotates and stores batches, retrying transient failures forever
// (until ctx ends); only a chain mismatch is returned.
func (w *QueueWriter) write(ctx context.Context, batches []*processor.Batch) (store.WriteStats, error) {
	var st store.WriteStats
	for attempt := 1; ; attempt++ {
		err := func() (err error) {
			if w.Verifier != nil {
				if err := w.Verifier.Annotate(ctx, batches); err != nil {
					return err
				}
			}
			start := time.Now()
			st, err = w.Store.Write(ctx, w.CursorName, batches)
			metrics.WriteDuration.Observe(time.Since(start).Seconds())
			return err
		}()
		if err == nil {
			return st, nil
		}
		if errors.Is(err, store.ErrChainMismatch) {
			metrics.ChainBreaks.Inc()
			return store.WriteStats{}, err
		}
		if ctx.Err() != nil {
			return store.WriteStats{}, ctx.Err()
		}
		metrics.WriteErrors.Inc()
		wait := w.Backoff * time.Duration(min(attempt, 10))
		w.Log.Error("write failed, retrying the same batch", "first", batches[0].Ledger.Sequence, "n", len(batches), "attempt", attempt, "wait", wait, "err", err)
		if !sleep(ctx, wait) {
			return store.WriteStats{}, ctx.Err()
		}
	}
}
