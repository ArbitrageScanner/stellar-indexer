// Command indexer ingests Stellar ledgers into Postgres and serves analytics.
//
//	indexer run       ingest + HTTP API in one process (default)
//	indexer ingest    ingest only
//	indexer api       HTTP API only
//	indexer migrate   apply the schema and exit
//	indexer inspect N decode ledger N from RPC and print a summary (no DB)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/api"
	"github.com/ArbitrageScanner/stellar-indexer/internal/config"
	"github.com/ArbitrageScanner/stellar-indexer/internal/ingest"
	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
	"github.com/ArbitrageScanner/stellar-indexer/internal/source"
	"github.com/ArbitrageScanner/stellar-indexer/internal/store"
	"github.com/ArbitrageScanner/stellar-indexer/web"
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "run", "ingest", "api", "migrate":
		err = serve(ctx, cfg, log, cmd)
	case "inspect":
		err = inspect(ctx, cfg, os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q (run|ingest|api|migrate|inspect)", cmd)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

type progress struct{ r *ingest.Runner }

func (p progress) Progress() (uint32, uint32) { return p.r.Current.Load(), p.r.Tip.Load() }

func serve(ctx context.Context, cfg config.Config, log *slog.Logger, cmd string) error {
	db, err := connect(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	log.Info("schema ready", "network", cfg.Network)
	if cmd == "migrate" {
		return nil
	}

	var runner *ingest.Runner
	if cmd != "api" {
		reg := processor.NewRegistry(cfg.NetworkPassphrase)
		n, err := db.LoadAssets(ctx, reg)
		if err != nil {
			return err
		}
		log.Info("asset registry loaded", "assets", n)
		src := source.NewRPC(cfg.RPCURL, cfg.RPCBuffer, cfg.RPCTimeout)
		defer src.Close()
		runner = &ingest.Runner{
			Src: src, Store: db, Log: log,
			Proc: processor.New(cfg.NetworkPassphrase, reg, processor.Options{
				Operations: cfg.IndexOps, State: cfg.IndexState, FeeEvents: cfg.IndexFeeEvents, SkipFailed: !cfg.IndexFailedTxs,
			}),
			CursorName: cfg.CursorName, StartLedger: cfg.StartLedger, EndLedger: cfg.EndLedger,
			ForceStart: cfg.ForceStart, CommitEvery: cfg.CommitEvery, MaxRetries: cfg.MaxRetries, Backoff: cfg.RetryBackoff,
		}
	}

	errc := make(chan error, 2)
	if cmd != "ingest" {
		var st api.Status
		if runner != nil {
			st = progress{runner}
		}
		a := api.New(db.Pool, log, st, cfg.Network, cfg.CursorName)
		a.USDAsset, a.LabelsToken = cfg.USDAsset, cfg.LabelsToken
		switch {
		case cfg.UIDir != "":
			a.UI = os.DirFS(cfg.UIDir)
		case cfg.UI:
			a.UI = web.FS()
		}
		srv := &http.Server{Addr: cfg.HTTPAddr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			log.Info("http listening", "addr", cfg.HTTPAddr, "ui", a.UI != nil)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
	}
	ret := store.Retention{Raw: cfg.RetainRaw, Markets: cfg.RetainMarkets, Candles: cfg.RetainCandles}
	if runner != nil && (ret.Raw|ret.Markets|ret.Candles) != 0 {
		go prune(ctx, db, cfg, ret, log)
	}
	if runner != nil {
		go func() {
			err := runner.Run(ctx)
			if err == nil && cmd == "run" && cfg.EndLedger != 0 {
				log.Info("ingestion finished; API keeps serving")
				return
			}
			errc <- err
		}()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

func prune(ctx context.Context, db *store.Store, cfg config.Config, ret store.Retention, log *slog.Logger) {
	t := time.NewTicker(cfg.PruneEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n, err := db.Prune(ctx, cfg.CursorName, ret)
		if err != nil {
			log.Error("prune failed", "err", err)
			continue
		}
		log.Info("pruned", "rows", n)
	}
}

// connect retries so the service can start alongside Postgres in compose.
func connect(ctx context.Context, url string, log *slog.Logger) (*store.Store, error) {
	var last error
	for i := 0; i < 30; i++ {
		db, err := store.Open(ctx, url)
		if err == nil {
			return db, nil
		}
		last = err
		log.Warn("waiting for database", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, last
}

func inspect(ctx context.Context, cfg config.Config, args []string) error {
	src := source.NewRPC(cfg.RPCURL, 1, cfg.RPCTimeout)
	defer src.Close()
	var seq uint32
	if len(args) > 0 {
		n, err := strconv.ParseUint(args[0], 10, 32)
		if err != nil {
			return err
		}
		seq = uint32(n)
	} else {
		_, latest, err := src.Window(ctx)
		if err != nil {
			return err
		}
		seq = latest
	}
	if err := src.Prepare(ctx, seq, seq); err != nil {
		return err
	}
	lcm, err := src.GetLedger(ctx, seq)
	if err != nil {
		return err
	}
	p := processor.New(cfg.NetworkPassphrase, processor.NewRegistry(cfg.NetworkPassphrase),
		processor.Options{Operations: true, State: true})
	res, err := p.Process(lcm)
	if err != nil {
		return err
	}
	b := res.Batch
	venues := map[string]int{}
	for _, t := range b.Trades {
		venues[t.Venue]++
	}
	protos := map[string]int{}
	for _, e := range b.ProtocolEvents {
		protos[e.Protocol+"/"+e.Action]++
	}
	out := map[string]any{
		"ledger": b.Ledger, "operations": len(b.Operations), "trades": venues, "transfers": len(b.Transfers),
		"contract_events": len(b.ContractEvents), "protocol_events": protos, "accounts": len(b.Accounts),
		"trustlines": len(b.Trustlines), "pools": len(b.Pools), "offers": len(b.Offers), "new_assets": len(b.Assets),
		"warnings": res.Warnings,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
