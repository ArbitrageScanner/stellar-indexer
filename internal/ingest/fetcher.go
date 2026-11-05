package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/source"
)

// Producer is the queue side of the fetcher.
type Producer interface {
	Produce(ctx context.Context, seq uint32, closedAt time.Time, xdr []byte) error
	LastSequence(ctx context.Context) (uint32, error)
}

// Fetcher streams ledgers from RPC onto the queue. It keeps no state of its
// own: on every (re)start it resumes after the newest ledger already on the
// topic, or after the writer's DB cursor if that is newer (topic retention
// expired), so restarts neither duplicate nor skip ledgers.
type Fetcher struct {
	Src         source.Source
	Producer    Producer
	Cursor      func(ctx context.Context) (uint32, error) // optional: writer's DB cursor
	Heartbeat   func(ctx context.Context, ledger, tip uint32)
	Log         *slog.Logger
	StartLedger string // used only when there is nothing to resume from
	Backoff     time.Duration
	MaxRetries  int // 0 = forever
}

func (f *Fetcher) resume(ctx context.Context, oldest, latest uint32) (uint32, error) {
	last, err := f.Producer.LastSequence(ctx)
	if err != nil {
		return 0, fmt.Errorf("read topic end: %w", err)
	}
	if f.Cursor != nil {
		cur, err := f.Cursor(ctx)
		if err != nil {
			return 0, fmt.Errorf("read db cursor: %w", err)
		}
		last = max(last, cur)
	}
	if last == 0 {
		from, err := ResolveStart(f.StartLedger, oldest, latest)
		if err != nil {
			return 0, err
		}
		f.Log.Info("fetcher starting fresh", "from", from, "rpc_oldest", oldest, "rpc_latest", latest)
		return from, nil
	}
	from := last + 1
	if from < oldest {
		// Down longer than RPC retention. The writer records the gap when the
		// next ledger arrives; we continue from what RPC still has.
		f.Log.Warn("resume point fell out of RPC retention; a gap will be recorded", "resume", from, "rpc_oldest", oldest)
		from = oldest
	}
	f.Log.Info("fetcher resuming", "from", from, "last_on_topic_or_db", last)
	return from, nil
}

func (f *Fetcher) Run(ctx context.Context) error {
	failures := 0
	for {
		progressed, err := f.runOnce(ctx)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutdown is not an error
		}
		if progressed {
			failures = 0
		}
		failures++
		if f.MaxRetries > 0 && failures > f.MaxRetries {
			return fmt.Errorf("fetcher giving up after %d failures: %w", failures-1, err)
		}
		wait := f.Backoff * time.Duration(min(failures, 10))
		f.Log.Error("fetcher error, resuming", "attempt", failures, "wait", wait, "err", err)
		if !sleep(ctx, wait) {
			return nil
		}
	}
}

func (f *Fetcher) runOnce(ctx context.Context) (progressed bool, err error) {
	oldest, latest, err := f.Src.Window(ctx)
	if err != nil {
		metrics.FetchErrors.WithLabelValues("rpc_health").Inc()
		return false, fmt.Errorf("rpc health: %w", err)
	}
	metrics.RPCTip.Set(float64(latest))
	from, err := f.resume(ctx, oldest, latest)
	if err != nil {
		metrics.FetchErrors.WithLabelValues("resume").Inc()
		return false, err
	}
	if err := f.Src.Prepare(ctx, from, 0); err != nil {
		metrics.FetchErrors.WithLabelValues("rpc_prepare").Inc()
		return false, err
	}
	lastBeat := time.Time{}
	tip := latest
	for seq := from; ; seq++ {
		lcm, err := f.Src.GetLedger(ctx, seq)
		if err != nil {
			if ctx.Err() == nil {
				metrics.FetchErrors.WithLabelValues("rpc_get").Inc()
			}
			return progressed, fmt.Errorf("get ledger %d: %w", seq, err)
		}
		// Key records by the sequence inside the ledger, never by our counter.
		if got := lcm.LedgerSequence(); got < seq {
			return progressed, fmt.Errorf("source returned ledger %d when asked for %d", got, seq)
		} else {
			seq = got
		}
		raw, err := lcm.MarshalBinary()
		if err != nil {
			return progressed, fmt.Errorf("encode ledger %d: %w", seq, err)
		}
		if err := f.Producer.Produce(ctx, seq, lcm.ClosedAt().UTC(), raw); err != nil {
			metrics.ProduceErrors.Inc()
			return progressed, fmt.Errorf("produce ledger %d: %w", seq, err)
		}
		progressed = true
		metrics.FetchedLedgers.Inc()
		metrics.Produced.Inc()
		metrics.FetchLast.Set(float64(seq))
		tip = max(tip, seq)
		if f.Heartbeat != nil && time.Since(lastBeat) > 5*time.Second {
			f.Heartbeat(ctx, seq, tip)
			lastBeat = time.Now()
			if time.Since(lcm.ClosedAt()) > time.Minute {
				f.Log.Info("fetcher progress", "ledger", seq, "lag", time.Since(lcm.ClosedAt()).Round(time.Second).String())
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
