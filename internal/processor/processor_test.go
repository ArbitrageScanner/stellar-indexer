package processor

import (
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Fixtures are pubnet ledgers fetched with getLedgers (base64 XDR, gzipped).
//
//	65311337: Aquarius trades, Blend repay, SDEX fills
//	65312802: six Soroswap pair events
func loadLedger(t *testing.T, seq string) xdr.LedgerCloseMeta {
	t.Helper()
	f, err := os.Open("testdata/lcm_" + seq + ".b64.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var lcm xdr.LedgerCloseMeta
	if err := xdr.SafeUnmarshal(bin, &lcm); err != nil {
		t.Fatal(err)
	}
	return lcm
}

func process(t *testing.T, seq string) *Result {
	t.Helper()
	p := New(network.PublicNetworkPassphrase, NewRegistry(network.PublicNetworkPassphrase),
		Options{Operations: true, State: true})
	res, err := p.Process(loadLedger(t, seq))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func countBy[T any](xs []T, key func(T) string) map[string]int {
	m := map[string]int{}
	for _, x := range xs {
		m[key(x)]++
	}
	return m
}

func TestProcessAquariusBlendLedger(t *testing.T) {
	res := process(t, "65311337")
	b := res.Batch
	for _, w := range res.Warnings {
		t.Log("warning:", w)
	}
	if b.Ledger.Sequence != 65311337 || b.Ledger.Hash == "" || b.Ledger.ClosedAt.IsZero() {
		t.Fatalf("bad ledger header: %+v", b.Ledger)
	}
	if b.Ledger.TxCount != len(b.Transactions) || b.Ledger.TxCount == 0 {
		t.Fatalf("tx count %d vs %d rows", b.Ledger.TxCount, len(b.Transactions))
	}
	if b.Ledger.TxSuccess+b.Ledger.TxFailed != b.Ledger.TxCount {
		t.Fatal("success+failed != total")
	}
	t.Logf("ledger %+v", b.Ledger)
	t.Logf("ops=%d trades=%d transfers=%d events=%d protocol=%d accounts=%d trustlines=%d pools=%d offers=%d assets=%d",
		len(b.Operations), len(b.Trades), len(b.Transfers), len(b.ContractEvents), len(b.ProtocolEvents),
		len(b.Accounts), len(b.Trustlines), len(b.Pools), len(b.Offers), len(b.Assets))
	venues := countBy(b.Trades, func(x Trade) string { return x.Venue })
	protos := countBy(b.ProtocolEvents, func(x ProtocolEvent) string { return x.Protocol + "/" + x.Action })
	t.Logf("venues %v", venues)
	t.Logf("protocol events %v", protos)

	if venues["aquarius"] == 0 {
		t.Error("expected aquarius trades")
	}
	if protos["blend/repay"] == 0 {
		t.Error("expected blend repay event")
	}
	// Tx 04165e29... is a Blend flash-loan arbitrage XLM->AQUA->VELO->SHX->XLM
	// through four Aquarius pools. The first hop (event 6) sells XLM (SAC) for
	// AQUA (SAC); both must resolve to classic keys through the SAC registry.
	hops := 0
	found := false
	for _, tr := range b.Trades {
		if strings.HasPrefix(tr.TxHash, "04165e29") && tr.Venue == "aquarius" {
			hops++
		}
		if strings.HasPrefix(tr.TxHash, "04165e29") && tr.Venue == "aquarius" && tr.Index == 1006 {
			found = true
			if tr.SoldAsset != NativeKey || !strings.HasPrefix(tr.BoughtAsset, "AQUA:") {
				t.Errorf("aquarius trade assets not resolved to classic keys: %+v", tr)
			}
			if tr.SoldAmount != "250612971" || tr.BoughtAmount != "149593426184" {
				t.Errorf("aquarius amounts: %+v", tr)
			}
		}
	}
	if !found || hops != 4 {
		t.Errorf("aquarius arbitrage: first hop found=%v, hops=%d (want 4)", found, hops)
	}
	if protos["blend/flash_loan"] != 1 {
		t.Error("expected blend flash_loan event")
	}
	for _, tr := range b.Trades {
		if _, ok := TickOf(tr); !ok {
			t.Errorf("trade does not produce a price tick: %+v", tr)
		}
	}
	if len(b.Accounts) == 0 {
		t.Error("expected account state changes")
	}
}

func TestProcessSoroswapLedger(t *testing.T) {
	res := process(t, "65312802")
	b := res.Batch
	for _, w := range res.Warnings {
		t.Log("warning:", w)
	}
	venues := countBy(b.Trades, func(x Trade) string { return x.Venue })
	protos := countBy(b.ProtocolEvents, func(x ProtocolEvent) string { return x.Protocol + "/" + x.Action })
	t.Logf("venues %v protocol events %v", venues, protos)
	if protos["soroswap/swap"] == 0 {
		t.Fatal("expected soroswap swap events")
	}
	if venues["soroswap"] == 0 {
		t.Fatal("soroswap swaps did not resolve to trades")
	}
	for _, tr := range b.Trades {
		if tr.Venue == "soroswap" {
			t.Logf("soroswap trade %s %s -> %s %s", tr.SoldAmount, tr.SoldAsset, tr.BoughtAmount, tr.BoughtAsset)
		}
	}
}
