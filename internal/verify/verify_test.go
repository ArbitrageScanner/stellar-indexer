package verify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArbitrageScanner/stellar-indexer/internal/processor"
)

type fakeResolver struct {
	codes map[string]processor.ContractCode
	calls int
	err   error
}

func (f *fakeResolver) Resolve(_ context.Context, ids []string) ([]processor.ContractCode, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []processor.ContractCode
	for _, id := range ids {
		if c, ok := f.codes[id]; ok {
			out = append(out, c)
		} else {
			out = append(out, processor.ContractCode{ContractID: id, Executable: "missing"})
		}
	}
	return out, nil
}

const good, fake = "aa", "bb"

func batch() *processor.Batch {
	return &processor.Batch{
		ProtocolEvents: []processor.ProtocolEvent{
			{Protocol: "soroswap", ContractID: "CPAIR"}, {Protocol: "soroswap", ContractID: "CSPOOF"},
		},
		ContractEvents: []processor.ContractEvent{{Protocol: "soroswap", ContractID: "CSPOOF"}, {ContractID: "COTHER"}},
		Trades: []processor.Trade{
			{Venue: "soroswap", Pool: "CPAIR"}, {Venue: "soroswap", Pool: "CSPOOF"}, {Venue: "sdex", Pool: "1", Verified: true},
		},
	}
}

func TestAnnotate(t *testing.T) {
	res := &fakeResolver{codes: map[string]processor.ContractCode{
		"CPAIR":  {ContractID: "CPAIR", Executable: "wasm", WasmHash: good},
		"CSPOOF": {ContractID: "CSPOOF", Executable: "wasm", WasmHash: fake},
	}}
	v := New(true, Allowlist{"soroswap": {good}}, res, nil, nil)
	b := batch()
	if err := v.Annotate(context.Background(), []*processor.Batch{b}); err != nil {
		t.Fatal(err)
	}
	if !b.ProtocolEvents[0].Verified || b.ProtocolEvents[1].Verified {
		t.Fatalf("protocol events %+v", b.ProtocolEvents)
	}
	if !b.Trades[0].Verified || b.Trades[1].Verified || !b.Trades[2].Verified {
		t.Fatalf("trades %+v", b.Trades)
	}
	if b.ContractEvents[0].Verified || b.ContractEvents[1].Verified {
		t.Fatalf("contract events %+v", b.ContractEvents)
	}
	if len(b.Codes) != 2 {
		t.Fatalf("resolved codes not persisted: %+v", b.Codes)
	}
	// Cached: a second batch resolves nothing new.
	if err := v.Annotate(context.Background(), []*processor.Batch{batch()}); err != nil || res.calls != 1 {
		t.Fatalf("calls %d err %v", res.calls, err)
	}
}

func TestInLedgerUpgradeWins(t *testing.T) {
	v := New(true, Allowlist{"soroswap": {good}}, &fakeResolver{}, []processor.ContractCode{
		{ContractID: "CPAIR", Executable: "wasm", WasmHash: fake, Ledger: 5}}, nil)
	b := batch()
	b.Codes = []processor.ContractCode{{ContractID: "CPAIR", Executable: "wasm", WasmHash: good, Ledger: 9}}
	if err := v.Annotate(context.Background(), []*processor.Batch{b}); err != nil {
		t.Fatal(err)
	}
	if !b.ProtocolEvents[0].Verified {
		t.Fatal("upgrade to allowlisted code in the ledger was ignored")
	}
}

func TestResolverErrorAborts(t *testing.T) {
	v := New(true, Allowlist{}, &fakeResolver{err: errors.New("rpc down")}, nil, nil)
	if err := v.Annotate(context.Background(), []*processor.Batch{batch()}); err == nil {
		t.Fatal("transient resolver failure must abort, not mark data unverified")
	}
}

func TestOff(t *testing.T) {
	v := New(false, nil, nil, nil, nil)
	b := batch()
	if err := v.Annotate(context.Background(), []*processor.Batch{b}); err != nil {
		t.Fatal(err)
	}
	if !b.ProtocolEvents[1].Verified || !b.Trades[1].Verified {
		t.Fatal("verification off must accept everything")
	}
}

func TestLoadAllowlist(t *testing.T) {
	a, err := LoadAllowlist("pubnet", "")
	if err != nil || len(a["aquarius"]) == 0 || len(a["amm_generic"]) != 0 {
		t.Fatalf("defaults: %v %v", a, err)
	}
	f := filepath.Join(t.TempDir(), "a.json")
	_ = os.WriteFile(f, []byte(`{"replace":true,"protocols":{"phoenix":["0000000000000000000000000000000000000000000000000000000000000001"]}}`), 0o600)
	a, err = LoadAllowlist("pubnet", f)
	if err != nil || len(a) != 1 || len(a["phoenix"]) != 1 {
		t.Fatalf("file: %v %v", a, err)
	}
	_ = os.WriteFile(f, []byte(`{"protocols":{"x":["short"]}}`), 0o600)
	if _, err := LoadAllowlist("pubnet", f); err == nil {
		t.Fatal("bad hash accepted")
	}
}
