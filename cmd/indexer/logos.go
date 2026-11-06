package main

import (
	"context"
	"log/slog"
	"net/http"

	rpc "github.com/stellar/go-stellar-sdk/clients/rpcclient"

	"github.com/ArbitrageScanner/stellar-indexer/internal/config"
	"github.com/ArbitrageScanner/stellar-indexer/internal/logos"
	"github.com/ArbitrageScanner/stellar-indexer/internal/store"
)

// startLogos runs the SEP-1 logo resolver next to the API (LOGO_FETCH=false
// turns it off, e.g. where the API host has no outbound internet).
func startLogos(ctx context.Context, cfg config.Config, db *store.Store, log *slog.Logger) {
	if !cfg.LogoFetch {
		log.Info("logo resolver disabled (LOGO_FETCH=false)")
		return
	}
	var lookup logos.DomainLookup
	if cfg.RPCURL != "" {
		lookup = logos.RPCLookup(rpc.NewClient(cfg.RPCURL, &http.Client{Timeout: cfg.RPCTimeout}))
	}
	go logos.New(logos.PG{DB: db.Pool}, lookup, log.With("component", "logos")).Run(ctx)
}
