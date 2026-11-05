// Package config loads indexer settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/network"
)

type Config struct {
	Network           string // testnet | pubnet | custom
	NetworkPassphrase string
	RPCURL            string
	DatabaseURL       string

	// StartLedger is the first ledger to ingest when no cursor exists.
	// Accepted forms: "" / "latest" (tip), "oldest" (start of RPC retention),
	// "-N" (N ledgers behind tip), or an absolute sequence.
	StartLedger string
	// EndLedger stops ingestion after this ledger; 0 means tail forever.
	EndLedger uint32
	// ForceStart ignores the stored cursor and starts from StartLedger.
	ForceStart bool

	// CommitEvery groups this many ledgers into a single DB transaction while
	// backfilling. In live mode every ledger is committed immediately.
	CommitEvery int
	RPCBuffer   uint32

	IndexFeeEvents bool // store per-tx fee token events (large volume)
	IndexOps       bool
	IndexState     bool // accounts / trustlines / pools / offers
	IndexFailedTxs bool

	// Retention in ledgers behind the cursor (0 = keep forever).
	RetainRaw     uint32
	RetainMarkets uint32
	RetainCandles uint32
	PruneEvery    time.Duration

	HTTPAddr     string
	UI           bool   // serve the embedded explorer at /ui/
	UIDir        string // serve the explorer from this directory instead (development)
	USDAsset     string // asset that prices XLM in USD (default: USDC of the network)
	LabelsToken  string // bearer token required for label writes (empty = open)
	LogLevel     string
	RPCTimeout   time.Duration
	CursorName   string
	MaxRetries   int
	RetryBackoff time.Duration
}

func Load() (Config, error) {
	c := Config{
		Network:        env("STELLAR_NETWORK", "testnet"),
		RPCURL:         os.Getenv("STELLAR_RPC_URL"),
		DatabaseURL:    env("DATABASE_URL", "postgres://indexer:indexer@localhost:5432/indexer?sslmode=disable"),
		StartLedger:    env("START_LEDGER", "-100"),
		CommitEvery:    envInt("COMMIT_EVERY", 20),
		RPCBuffer:      uint32(envInt("RPC_BUFFER", 0)),
		IndexFeeEvents: envBool("INDEX_FEE_EVENTS", false),
		IndexOps:       envBool("INDEX_OPERATIONS", true),
		IndexState:     envBool("INDEX_STATE", true),
		IndexFailedTxs: envBool("INDEX_FAILED_TXS", true),
		RetainRaw:      uint32(envInt("RETAIN_RAW_LEDGERS", 0)),
		RetainMarkets:  uint32(envInt("RETAIN_MARKET_LEDGERS", 0)),
		RetainCandles:  uint32(envInt("RETAIN_CANDLE_LEDGERS", 0)),
		PruneEvery:     envDuration("PRUNE_INTERVAL", 5*time.Minute),
		ForceStart:     envBool("FORCE_START", false),
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		UI:             envBool("UI_ENABLED", true),
		UIDir:          os.Getenv("UI_DIR"),
		USDAsset:       os.Getenv("USD_ASSET"),
		LabelsToken:    os.Getenv("LABELS_TOKEN"),
		LogLevel:       env("LOG_LEVEL", "info"),
		RPCTimeout:     envDuration("RPC_TIMEOUT", 60*time.Second),
		CursorName:     env("CURSOR_NAME", "main"),
		MaxRetries:     envInt("MAX_RETRIES", 0),
		RetryBackoff:   envDuration("RETRY_BACKOFF", 3*time.Second),
	}
	if v := os.Getenv("END_LEDGER"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return c, fmt.Errorf("END_LEDGER: %w", err)
		}
		c.EndLedger = uint32(n)
	}

	switch strings.ToLower(c.Network) {
	case "testnet":
		c.NetworkPassphrase = network.TestNetworkPassphrase
		if c.RPCURL == "" {
			c.RPCURL = "https://soroban-testnet.stellar.org"
		}
		if c.USDAsset == "" {
			c.USDAsset = "USDC:GBBD47IF6LWK7P7MDEVSCWR7DPUWV3NY3DTQEVFL4NAT4AQH3ZLLFLA5" // Circle testnet USDC
		}
	case "pubnet", "mainnet", "public":
		c.Network = "pubnet"
		c.NetworkPassphrase = network.PublicNetworkPassphrase
		if c.RPCURL == "" {
			c.RPCURL = "https://mainnet.sorobanrpc.com"
		}
		if c.USDAsset == "" {
			c.USDAsset = "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN" // Circle USDC
		}
	default:
		c.NetworkPassphrase = os.Getenv("NETWORK_PASSPHRASE")
		if c.NetworkPassphrase == "" || c.RPCURL == "" {
			return c, fmt.Errorf("custom network %q requires NETWORK_PASSPHRASE and STELLAR_RPC_URL", c.Network)
		}
	}
	if p := os.Getenv("NETWORK_PASSPHRASE"); p != "" {
		c.NetworkPassphrase = p
	}
	if c.RPCBuffer == 0 {
		// getLedgers batch size; pubnet ledgers are ~2-3 MB of XDR each.
		c.RPCBuffer = 50
		if c.Network == "pubnet" {
			c.RPCBuffer = 10
		}
	}
	if c.CommitEvery < 1 {
		c.CommitEvery = 1
	}
	return c, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
