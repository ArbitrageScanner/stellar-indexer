package config

import (
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	t.Setenv("STELLAR_NETWORK", "pubnet")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.StartLedger != "latest" || c.KafkaTopic != "stellar.pubnet.ledgers" || !c.VerifyProtocols || c.LabelsOpen {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if !strings.HasPrefix(c.USDAsset, "USDC:") || c.RPCBuffer != 10 {
		t.Fatalf("pubnet defaults: %q %d", c.USDAsset, c.RPCBuffer)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"retention below partition": {"RETAIN_RAW_LEDGERS": "100", "PARTITION_LEDGERS": "20000"},
		"bad verify mode":           {"PROTOCOL_VERIFY": "maybe"},
		"bad log format":            {"LOG_FORMAT": "xml"},
		"custom without rpc":        {"STELLAR_NETWORK": "futurenet", "NETWORK_PASSPHRASE": "x"},
		"bad end ledger":            {"END_LEDGER": "soon"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestListsAndRedaction(t *testing.T) {
	t.Setenv("API_KEYS", " a, b ,,c")
	t.Setenv("DATABASE_URL", "postgres://user:secret@db:5432/x")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.APIKeys) != 3 || c.APIKeys[1] != "b" {
		t.Fatalf("api keys %q", c.APIKeys)
	}
	r := c.Redacted()
	if s := r["db"].(string); strings.Contains(s, "secret") || !strings.Contains(s, "@db:5432") {
		t.Fatalf("db not redacted: %s", s)
	}
	if r["api_keys"] != 3 {
		t.Fatalf("redacted keys: %v", r["api_keys"])
	}
}
