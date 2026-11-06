// Package config loads indexer settings from environment variables and
// validates them once at startup.
package config

import (
	"errors"
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

	// StartLedger is used only when there is nothing to resume from (empty
	// topic and DB): "latest" (default: index from launch), "oldest", "-N",
	// or an absolute sequence.
	StartLedger string
	EndLedger   uint32 // stop after this ledger (direct mode); 0 = tail forever
	ForceStart  bool   // direct mode: ignore the stored cursor

	CommitEvery int // ledgers per DB transaction while catching up
	RPCBuffer   uint32

	IndexFeeEvents bool
	IndexOps       bool
	IndexState     bool
	IndexFailedTxs bool

	// Retention in ledgers behind the cursor (0 = keep forever).
	RetainRaw        uint32
	RetainMarkets    uint32
	RetainCandles    uint32
	PruneEvery       time.Duration
	PartitionLedgers int64

	// Queue (fetch/write split). Empty brokers = `run` uses the direct pipeline.
	KafkaBrokers        []string
	KafkaTopic          string
	KafkaGroup          string
	KafkaRetention      time.Duration
	KafkaRetentionBytes int64
	KafkaReplication    int16

	// Protocol verification.
	VerifyProtocols bool
	AllowlistFile   string

	// HTTP API.
	HTTPAddr     string
	MetricsAddr  string // fetch/write processes; the API serves /metrics itself
	UI           bool
	UIDir        string
	USDAsset     string
	LogoFetch    bool // resolve issuer images from stellar.toml (outbound HTTPS from the API process)
	LabelsToken  string
	LabelsOpen   bool
	APIKeys      []string
	CORSOrigins  []string
	RateRPS      float64
	RateBurst    int
	TrustProxy   bool
	CacheTTL     time.Duration
	ReadyMaxLag  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// TLS for the API listener; with a client CA, clients (the public
	// gateway) must present a certificate signed by it (mTLS).
	TLSCertFile     string
	TLSKeyFile      string
	TLSClientCAFile string

	LogLevel     string
	LogFormat    string // text | json
	RPCTimeout   time.Duration
	CursorName   string
	MaxRetries   int
	RetryBackoff time.Duration
}

func Load() (Config, error) {
	c := Config{
		Network:             env("STELLAR_NETWORK", "testnet"),
		RPCURL:              os.Getenv("STELLAR_RPC_URL"),
		DatabaseURL:         env("DATABASE_URL", "postgres://indexer:indexer@localhost:5432/indexer?sslmode=disable"),
		StartLedger:         env("START_LEDGER", "latest"),
		CommitEvery:         envInt("COMMIT_EVERY", 20),
		RPCBuffer:           uint32(envInt("RPC_BUFFER", 0)),
		IndexFeeEvents:      envBool("INDEX_FEE_EVENTS", false),
		IndexOps:            envBool("INDEX_OPERATIONS", true),
		IndexState:          envBool("INDEX_STATE", true),
		IndexFailedTxs:      envBool("INDEX_FAILED_TXS", true),
		RetainRaw:           uint32(envInt("RETAIN_RAW_LEDGERS", 0)),
		RetainMarkets:       uint32(envInt("RETAIN_MARKET_LEDGERS", 0)),
		RetainCandles:       uint32(envInt("RETAIN_CANDLE_LEDGERS", 0)),
		PruneEvery:          envDuration("PRUNE_INTERVAL", 5*time.Minute),
		PartitionLedgers:    int64(envInt("PARTITION_LEDGERS", 20000)),
		KafkaBrokers:        envList("KAFKA_BROKERS", nil),
		KafkaTopic:          os.Getenv("KAFKA_TOPIC"),
		KafkaGroup:          env("KAFKA_GROUP", "stellar-indexer-writer"),
		KafkaRetention:      envDuration("KAFKA_RETENTION", 24*time.Hour),
		KafkaRetentionBytes: int64(envInt("KAFKA_RETENTION_BYTES", 0)),
		KafkaReplication:    int16(envInt("KAFKA_REPLICATION", 1)),
		VerifyProtocols:     env("PROTOCOL_VERIFY", "enforce") != "off",
		AllowlistFile:       os.Getenv("PROTOCOL_ALLOWLIST_FILE"),
		ForceStart:          envBool("FORCE_START", false),
		HTTPAddr:            env("HTTP_ADDR", ":8080"),
		MetricsAddr:         env("METRICS_ADDR", ":9100"),
		UI:                  envBool("UI_ENABLED", true),
		UIDir:               os.Getenv("UI_DIR"),
		USDAsset:            os.Getenv("USD_ASSET"),
		LogoFetch:           envBool("LOGO_FETCH", true),
		LabelsToken:         os.Getenv("LABELS_TOKEN"),
		LabelsOpen:          envBool("LABELS_OPEN", false),
		APIKeys:             envList("API_KEYS", nil),
		CORSOrigins:         envList("CORS_ORIGINS", []string{"*"}),
		RateRPS:             envFloat("RATE_LIMIT_RPS", 20),
		RateBurst:           envInt("RATE_LIMIT_BURST", 40),
		TrustProxy:          envBool("TRUST_PROXY", false),
		CacheTTL:            envDuration("CACHE_TTL", 10*time.Second),
		ReadyMaxLag:         envDuration("READY_MAX_LAG", 2*time.Minute),
		ReadTimeout:         envDuration("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:        envDuration("HTTP_WRITE_TIMEOUT", 60*time.Second),
		TLSCertFile:         os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:          os.Getenv("TLS_KEY_FILE"),
		TLSClientCAFile:     os.Getenv("TLS_CLIENT_CA_FILE"),
		LogLevel:            env("LOG_LEVEL", "info"),
		LogFormat:           env("LOG_FORMAT", "text"),
		RPCTimeout:          envDuration("RPC_TIMEOUT", 60*time.Second),
		CursorName:          env("CURSOR_NAME", "main"),
		MaxRetries:          envInt("MAX_RETRIES", 0),
		RetryBackoff:        envDuration("RETRY_BACKOFF", 3*time.Second),
	}
	var errs []error
	if v := os.Getenv("END_LEDGER"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			errs = append(errs, fmt.Errorf("END_LEDGER: %w", err))
		}
		c.EndLedger = uint32(n)
	}

	switch strings.ToLower(c.Network) {
	case "testnet":
		c.Network = "testnet"
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
			errs = append(errs, fmt.Errorf("custom network %q requires NETWORK_PASSPHRASE and STELLAR_RPC_URL", c.Network))
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
	if c.KafkaTopic == "" {
		c.KafkaTopic = "stellar." + c.Network + ".ledgers"
	}
	if c.CommitEvery < 1 {
		errs = append(errs, errors.New("COMMIT_EVERY must be >= 1"))
	}
	if c.PartitionLedgers < 100 {
		errs = append(errs, errors.New("PARTITION_LEDGERS must be >= 100"))
	}
	if c.RetainRaw > 0 && int64(c.RetainRaw) < c.PartitionLedgers {
		errs = append(errs, fmt.Errorf("RETAIN_RAW_LEDGERS (%d) below PARTITION_LEDGERS (%d) cannot be honoured: retention drops whole partitions", c.RetainRaw, c.PartitionLedgers))
	}
	if c.RateRPS < 0 || c.RateBurst < 0 {
		errs = append(errs, errors.New("RATE_LIMIT_RPS and RATE_LIMIT_BURST must be >= 0"))
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") || (c.TLSClientCAFile != "" && c.TLSCertFile == "") {
		errs = append(errs, errors.New("TLS_CERT_FILE and TLS_KEY_FILE go together; TLS_CLIENT_CA_FILE needs them"))
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be text or json, got %q", c.LogFormat))
	}
	if v := env("PROTOCOL_VERIFY", "enforce"); v != "enforce" && v != "off" {
		errs = append(errs, fmt.Errorf("PROTOCOL_VERIFY must be enforce or off, got %q", v))
	}
	return c, errors.Join(errs...)
}

// Redacted returns the settings safe to log (no credentials).
func (c Config) Redacted() map[string]any {
	db := c.DatabaseURL
	if i := strings.Index(db, "@"); i > 0 {
		if j := strings.Index(db, "://"); j > 0 && j < i {
			db = db[:j+3] + "***" + db[i:]
		}
	}
	return map[string]any{
		"network": c.Network, "rpc": c.RPCURL, "db": db, "kafka": c.KafkaBrokers, "topic": c.KafkaTopic,
		"verify": c.VerifyProtocols, "partition_ledgers": c.PartitionLedgers, "api_keys": len(c.APIKeys),
		"labels_token": c.LabelsToken != "", "labels_open": c.LabelsOpen, "rate_rps": c.RateRPS,
		"tls": c.TLSCertFile != "", "mtls": c.TLSClientCAFile != "", "logo_fetch": c.LogoFetch,
	}
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

func envFloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
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

func envList(k string, def []string) []string {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
