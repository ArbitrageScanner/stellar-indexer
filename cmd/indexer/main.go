// Command indexer ingests Stellar ledgers into Postgres and serves analytics.
//
// Deployment modes:
//
//	indexer fetch     RPC → Kafka topic (raw ledgers)              ┐ queue mode
//	indexer write     Kafka topic → decode → verify → Postgres     ┘ (production)
//	indexer api       HTTP API + explorer UI (any number of replicas)
//	indexer run       RPC → decode → verify → Postgres + API in one process (no queue)
//	indexer migrate   apply database migrations and exit
//	indexer inspect [seq]          decode one ledger from RPC (no DB)
//	indexer contract-code C...     show contract executables and allowlist status
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ArbitrageScanner/stellar-indexer/internal/config"
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
	log := newLogger(cfg.LogLevel, cfg.LogFormat).With("cmd", cmd)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "fetch":
		err = fetch(ctx, cfg, log)
	case "write":
		err = write(ctx, cfg, log)
	case "api":
		err = apiOnly(ctx, cfg, log)
	case "run":
		err = runDirect(ctx, cfg, log)
	case "migrate":
		err = migrateOnly(ctx, cfg, log)
	case "inspect":
		err = inspect(ctx, cfg, os.Args[2:])
	case "contract-code":
		err = contractCode(ctx, cfg, os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q (fetch|write|api|run|migrate|inspect|contract-code)", cmd)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(level, format string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	opts := &slog.HandlerOptions{Level: l}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
