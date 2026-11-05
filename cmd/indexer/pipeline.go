package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	rpc "github.com/stellar/go-stellar-sdk/clients/rpcclient"

	"github.com/ArbitrageScanner/stellar-indexer/internal/config"
	"github.com/ArbitrageScanner/stellar-indexer/internal/ingest"
	"github.com/ArbitrageScanner/stellar-indexer/internal/metrics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
	"github.com/ArbitrageScanner/stellar-indexer/internal/queue"
	"github.com/ArbitrageScanner/stellar-indexer/internal/source"
	"github.com/ArbitrageScanner/stellar-indexer/internal/store"
	"github.com/ArbitrageScanner/stellar-indexer/internal/verify"
)

func queueConfig(cfg config.Config) (queue.Config, error) {
	if len(cfg.KafkaBrokers) == 0 {
		return queue.Config{}, errors.New("KAFKA_BROKERS is required for fetch/write (use `run` for the queue-less pipeline)")
	}
	return queue.Config{Brokers: cfg.KafkaBrokers, Topic: cfg.KafkaTopic, Group: cfg.KafkaGroup, Retention: cfg.KafkaRetention,
		RetentionBytes: cfg.KafkaRetentionBytes, ReplicationFactor: cfg.KafkaReplication, Network: cfg.NetworkPassphrase}, nil
}

// openStore connects (retrying while Postgres starts) and migrates.
func openStore(ctx context.Context, cfg config.Config, log *slog.Logger) (*store.Store, error) {
	var last error
	for i := 0; i < 30; i++ {
		db, err := store.Open(ctx, cfg.DatabaseURL)
		if err == nil {
			db.PartitionLedgers = cfg.PartitionLedgers
			if err := db.Migrate(ctx); err != nil {
				db.Close()
				return nil, fmt.Errorf("migrate: %w", err)
			}
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

func migrateOnly(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	db, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	db.Close()
	log.Info("migrations applied")
	return nil
}

func newProcessor(ctx context.Context, cfg config.Config, db *store.Store, log *slog.Logger) (*processor.Processor, *verify.Verifier, error) {
	reg := processor.NewRegistry(cfg.NetworkPassphrase)
	n, err := db.LoadAssets(ctx, reg)
	if err != nil {
		return nil, nil, err
	}
	allow, err := verify.LoadAllowlist(cfg.Network, cfg.AllowlistFile)
	if err != nil {
		return nil, nil, err
	}
	codes, err := db.LoadCodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	res := verify.RPCResolver{Client: rpc.NewClient(cfg.RPCURL, &http.Client{Timeout: cfg.RPCTimeout})}
	v := verify.New(cfg.VerifyProtocols, allow, res, codes, log)
	log.Info("processor ready", "assets", n, "known_contract_codes", len(codes), "verify", cfg.VerifyProtocols, "allowlisted_protocols", len(allow))
	p := processor.New(cfg.NetworkPassphrase, reg, processor.Options{
		Operations: cfg.IndexOps, State: cfg.IndexState, Codes: true, FeeEvents: cfg.IndexFeeEvents, SkipFailed: !cfg.IndexFailedTxs,
	})
	return p, v, nil
}

func heartbeat(db *store.Store, name, role string, log *slog.Logger) func(context.Context, uint32, uint32) {
	return func(ctx context.Context, ledger, tip uint32) {
		if err := db.Heartbeat(ctx, name, role, ledger, tip, map[string]any{"pid": os.Getpid()}); err != nil && ctx.Err() == nil {
			log.Warn("heartbeat failed", "err", err)
		}
	}
}

// fetch: RPC → queue.
func fetch(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	qc, err := queueConfig(cfg)
	if err != nil {
		return err
	}
	db, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer db.Close()
	prod, err := queue.NewProducer(ctx, qc)
	if err != nil {
		return fmt.Errorf("kafka producer: %w", err)
	}
	defer prod.Close()
	src := source.NewRPC(cfg.RPCURL, cfg.RPCBuffer, cfg.RPCTimeout)
	defer func() { _ = src.Close() }()
	stopMetrics := serveMetrics(cfg, log)
	defer stopMetrics()
	f := &ingest.Fetcher{
		Src: src, Producer: prod, Log: log, StartLedger: cfg.StartLedger, Backoff: cfg.RetryBackoff, MaxRetries: cfg.MaxRetries,
		Cursor:    func(ctx context.Context) (uint32, error) { return db.Cursor(ctx, cfg.CursorName) },
		Heartbeat: heartbeat(db, "fetcher:"+cfg.KafkaTopic, "fetcher", log),
	}
	log.Info("fetcher started", "settings", cfg.Redacted())
	return f.Run(ctx)
}

// write: queue → Postgres.
func write(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	qc, err := queueConfig(cfg)
	if err != nil {
		return err
	}
	db, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer db.Close()
	proc, ver, err := newProcessor(ctx, cfg, db, log)
	if err != nil {
		return err
	}
	cons, err := queue.NewConsumer(ctx, qc, func(lag int64) { metrics.ConsumerLag.Set(float64(lag)) })
	if err != nil {
		return fmt.Errorf("kafka consumer: %w", err)
	}
	defer cons.Close()
	stopMetrics := serveMetrics(cfg, log)
	defer stopMetrics()
	go prune(ctx, db, cfg, log)
	w := &ingest.QueueWriter{Consumer: cons, Proc: proc, Verifier: ver, Store: db, Log: log, CursorName: cfg.CursorName,
		BatchSize: cfg.CommitEvery, Backoff: cfg.RetryBackoff}
	log.Info("writer started", "settings", cfg.Redacted())
	return w.Run(ctx)
}

// runDirect: the whole pipeline and the API in one process, without a queue.
func runDirect(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	db, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer db.Close()
	proc, ver, err := newProcessor(ctx, cfg, db, log)
	if err != nil {
		return err
	}
	src := source.NewRPC(cfg.RPCURL, cfg.RPCBuffer, cfg.RPCTimeout)
	defer func() { _ = src.Close() }()
	r := &ingest.Runner{
		Src: src, Store: db, Proc: proc, Verifier: ver, Log: log, Heartbeat: heartbeat(db, "fetcher:"+cfg.CursorName, "fetcher", log),
		CursorName: cfg.CursorName, StartLedger: cfg.StartLedger, EndLedger: cfg.EndLedger,
		ForceStart: cfg.ForceStart, CommitEvery: cfg.CommitEvery, MaxRetries: cfg.MaxRetries, Backoff: cfg.RetryBackoff,
	}
	errc := make(chan error, 2)
	stopAPI := serveAPI(ctx, cfg, db, log, errc)
	defer stopAPI()
	go prune(ctx, db, cfg, log)
	go func() {
		err := r.Run(ctx)
		if err == nil && cfg.EndLedger != 0 {
			log.Info("ingestion finished; API keeps serving")
			return
		}
		errc <- err
	}()
	log.Info("direct pipeline started", "settings", cfg.Redacted())
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

func apiOnly(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	db, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer db.Close()
	errc := make(chan error, 1)
	stop := serveAPI(ctx, cfg, db, log, errc)
	defer stop()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

func prune(ctx context.Context, db *store.Store, cfg config.Config, log *slog.Logger) {
	ret := store.Retention{Raw: cfg.RetainRaw, Markets: cfg.RetainMarkets, Candles: cfg.RetainCandles}
	if ret.Raw|ret.Markets|ret.Candles == 0 {
		return
	}
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
		if len(n) > 0 {
			log.Info("pruned", "removed", n)
		}
	}
}

func inspect(ctx context.Context, cfg config.Config, args []string) error {
	src := source.NewRPC(cfg.RPCURL, 1, cfg.RPCTimeout)
	defer func() { _ = src.Close() }()
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
		processor.Options{Operations: true, State: true, Codes: true})
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
	return printJSON(map[string]any{
		"ledger": b.Ledger, "operations": len(b.Operations), "trades": venues, "transfers": len(b.Transfers),
		"contract_events": len(b.ContractEvents), "protocol_events": protos, "accounts": len(b.Accounts),
		"trustlines": len(b.Trustlines), "pools": len(b.Pools), "offers": len(b.Offers), "new_assets": len(b.Assets),
		"contract_code_changes": len(b.Codes), "warnings": res.Warnings,
	})
}

// contractCode resolves contract executables via RPC and reports, per
// protocol, whether each would be verified: the tool for reviewing and
// extending PROTOCOL_ALLOWLIST_FILE.
func contractCode(ctx context.Context, cfg config.Config, ids []string) error {
	if len(ids) == 0 {
		return errors.New("usage: indexer contract-code C... [C...]")
	}
	allow, err := verify.LoadAllowlist(cfg.Network, cfg.AllowlistFile)
	if err != nil {
		return err
	}
	res := verify.RPCResolver{Client: rpc.NewClient(cfg.RPCURL, &http.Client{Timeout: cfg.RPCTimeout})}
	codes, err := res.Resolve(ctx, ids)
	if err != nil {
		return err
	}
	v := verify.New(true, allow, nil, nil, nil)
	var out []map[string]any
	for _, c := range codes {
		ok := []string{}
		for p := range allow {
			if v.Allowed(p, c) {
				ok = append(ok, p)
			}
		}
		out = append(out, map[string]any{"contract": c.ContractID, "executable": c.Executable, "wasm_hash": c.WasmHash, "allowlisted_for": ok})
	}
	return printJSON(out)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
