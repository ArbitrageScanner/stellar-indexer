// Package ingest drives the fetch → process → store loop with a resumable cursor.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
	"github.com/ArbitrageScanner/stellar-indexer/internal/source"
	"github.com/ArbitrageScanner/stellar-indexer/internal/store"
)

type Writer interface {
	Cursor(ctx context.Context, name string) (uint32, error)
	Write(ctx context.Context, cursor string, batches []*processor.Batch) (store.WriteStats, error)
}

// Runner is the queue-less pipeline (RPC → processor → Postgres in one
// process), for small deployments and development. Resume comes from the DB
// cursor; the store's idempotent writes make restarts safe.
type Runner struct {
	Src         source.Source
	Store       Writer
	Proc        *processor.Processor
	Verifier    Annotator // optional
	Heartbeat   func(ctx context.Context, ledger, tip uint32)
	Log         *slog.Logger
	CursorName  string
	StartLedger string
	EndLedger   uint32
	ForceStart  bool
	CommitEvery int
	MaxRetries  int // 0 = retry forever
	Backoff     time.Duration

	// Live progress, readable from other goroutines (API /status).
	Current atomic.Uint32
	Tip     atomic.Uint32
}

// ResolveStart converts the START_LEDGER expression against the RPC window.
func ResolveStart(expr string, oldest, latest uint32) (uint32, error) {
	expr = strings.TrimSpace(strings.ToLower(expr))
	var seq uint32
	switch {
	case expr == "" || expr == "latest":
		seq = latest
	case expr == "oldest":
		seq = oldest
	case strings.HasPrefix(expr, "-"):
		n, err := strconv.ParseUint(expr[1:], 10, 32)
		if err != nil {
			return 0, fmt.Errorf("bad START_LEDGER %q", expr)
		}
		if uint32(n) >= latest {
			seq = 1
		} else {
			seq = latest - uint32(n)
		}
	default:
		n, err := strconv.ParseUint(expr, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("bad START_LEDGER %q", expr)
		}
		seq = uint32(n)
	}
	if oldest > 0 && seq < oldest {
		return 0, fmt.Errorf("start ledger %d is older than the RPC retention window [%d, %d]; "+
			"use a history-retaining RPC or START_LEDGER=oldest", seq, oldest, latest)
	}
	return seq, nil
}

func (r *Runner) Run(ctx context.Context) error {
	oldest, latest, err := r.Src.Window(ctx)
	if err != nil {
		return fmt.Errorf("rpc health: %w", err)
	}
	r.Tip.Store(latest)

	cur, err := r.Store.Cursor(ctx, r.CursorName)
	if err != nil {
		return fmt.Errorf("read cursor: %w", err)
	}
	var from uint32
	if cur > 0 && !r.ForceStart {
		from = cur + 1
		if from < oldest {
			r.Log.Warn("cursor fell out of RPC retention; gap will exist", "cursor", cur, "oldest", oldest)
			from = oldest
		}
		r.Log.Info("resuming from cursor", "cursor", cur, "from", from)
	} else {
		from, err = ResolveStart(r.StartLedger, oldest, latest)
		if err != nil {
			return err
		}
		r.Log.Info("starting fresh", "from", from, "rpc_oldest", oldest, "rpc_latest", latest)
	}
	if r.EndLedger != 0 && from > r.EndLedger {
		r.Log.Info("nothing to do", "from", from, "end", r.EndLedger)
		return nil
	}

	failures := 0
	for {
		next, err := r.runFrom(ctx, from)
		if err == nil || ctx.Err() != nil {
			return nil //nolint:nilerr // reached EndLedger, or shutdown: not an error
		}
		if errors.Is(err, store.ErrChainMismatch) {
			metrics.ChainBreaks.Inc()
			return err
		}
		if next > from {
			failures = 0
		}
		from = next
		failures++
		if r.MaxRetries > 0 && failures > r.MaxRetries {
			return fmt.Errorf("giving up after %d failures: %w", failures-1, err)
		}
		wait := r.Backoff * time.Duration(min(failures, 10))
		r.Log.Error("ingest error, retrying", "from", from, "attempt", failures, "wait", wait, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// runFrom ingests until EndLedger/ctx end (returns nil) or a failure (returns
// the next ledger to resume from and the error).
func (r *Runner) runFrom(ctx context.Context, from uint32) (uint32, error) {
	if err := r.Src.Prepare(ctx, from, r.EndLedger); err != nil {
		return from, err
	}
	var pending []*processor.Batch
	pendingFrom := from
	lastLog := time.Now()
	var processed, rows int
	started := time.Now()

	lastBeat := time.Time{}
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		var st store.WriteStats
		err := error(nil)
		if r.Verifier != nil {
			err = r.Verifier.Annotate(ctx, pending)
		}
		if err == nil {
			start := time.Now()
			st, err = r.Store.Write(ctx, r.CursorName, pending)
			metrics.WriteDuration.Observe(time.Since(start).Seconds())
		}
		if err != nil {
			metrics.WriteErrors.Inc()
			for _, b := range pending {
				r.Proc.Registry().Forget(b.Assets)
			}
			pending = pending[:0]
			return err
		}
		processed += st.Ledgers
		rows += st.Rows
		last := pending[len(pending)-1].Ledger
		metrics.WrittenLedgers.Add(float64(st.Ledgers))
		metrics.SkippedLedgers.Add(float64(st.Skipped))
		metrics.WrittenRows.Add(float64(st.Rows))
		metrics.Gaps.Add(float64(st.Gaps))
		metrics.LastWritten.Set(float64(last.Sequence))
		metrics.CloseLag.Set(time.Since(last.ClosedAt).Seconds())
		if r.Heartbeat != nil && time.Since(lastBeat) > 5*time.Second {
			r.Heartbeat(ctx, last.Sequence, r.Tip.Load())
			lastBeat = time.Now()
		}
		if st.Skipped > 0 {
			r.Log.Debug("skipped already indexed ledgers", "n", st.Skipped)
		}
		pendingFrom = pending[len(pending)-1].Ledger.Sequence + 1
		pending = pending[:0]
		return nil
	}

	for seq := from; r.EndLedger == 0 || seq <= r.EndLedger; seq++ {
		lcm, err := r.Src.GetLedger(ctx, seq)
		if err != nil {
			if ctx.Err() != nil {
				_ = flush()
				return seq, nil //nolint:nilerr // shutdown: flush and stop cleanly
			}
			if ferr := flush(); ferr != nil {
				return pendingFrom, errors.Join(err, ferr)
			}
			return seq, fmt.Errorf("get ledger %d: %w", seq, err)
		}
		metrics.FetchedLedgers.Inc()
		metrics.FetchLast.Set(float64(seq))
		res, err := r.Proc.Process(lcm)
		if err != nil {
			metrics.ProcessError.Inc()
			if ferr := flush(); ferr != nil {
				return pendingFrom, errors.Join(err, ferr)
			}
			return seq, err
		}
		for _, w := range res.Warnings {
			r.Log.Debug("decode warning", "msg", w)
		}
		pending = append(pending, res.Batch)
		r.Current.Store(seq)
		if seq > r.Tip.Load() {
			r.Tip.Store(seq)
		}

		// Live when the ledger closed recently: commit every ledger for low latency.
		live := time.Since(res.Batch.Ledger.ClosedAt) < 30*time.Second
		if live || len(pending) >= r.CommitEvery {
			if err := flush(); err != nil {
				return pendingFrom, err
			}
		}
		if time.Since(lastLog) > 10*time.Second || (live && processed%20 == 1) {
			rate := float64(processed) / time.Since(started).Seconds()
			r.Log.Info("progress", "ledger", seq, "closed_at", res.Batch.Ledger.ClosedAt.Format(time.RFC3339),
				"lag", time.Since(res.Batch.Ledger.ClosedAt).Round(time.Second).String(), "ledgers", processed,
				"rows", rows, "ledgers_per_s", fmt.Sprintf("%.2f", rate), "txs", res.Batch.Ledger.TxCount)
			lastLog = time.Now()
		}
	}
	if err := flush(); err != nil {
		return pendingFrom, err
	}
	r.Log.Info("reached end ledger", "end", r.EndLedger, "ledgers", processed, "rows", rows)
	return r.EndLedger + 1, nil
}
