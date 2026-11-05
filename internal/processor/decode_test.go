package processor

import (
	"math"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

func sym(s string) xdr.ScVal {
	v := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &v}
}

func strv(s string) xdr.ScVal {
	v := xdr.ScString(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &v}
}

func i128(hi int64, lo uint64) xdr.ScVal {
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{Hi: xdr.Int64(hi), Lo: xdr.Uint64(lo)}}
}

func n(v int64) xdr.ScVal {
	if v < 0 {
		return i128(-1, uint64(v))
	}
	return i128(0, uint64(v))
}

func smap(kv ...any) xdr.ScVal {
	m := xdr.ScMap{}
	for i := 0; i < len(kv); i += 2 {
		m = append(m, xdr.ScMapEntry{Key: sym(kv[i].(string)), Val: kv[i+1].(xdr.ScVal)})
	}
	mp := &m
	return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mp}
}

func TestScValBigInt(t *testing.T) {
	cases := []struct {
		v    xdr.ScVal
		want string
	}{
		{n(5), "5"},
		{n(-846668629), "-846668629"},
		{i128(1, 0), "18446744073709551616"},
		{xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &xdr.UInt256Parts{LoLo: 7, HiHi: 1}}, "6277101735386680763835789423207666416102355444464034512903"},
		{xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &xdr.Int256Parts{HiHi: -1, HiLo: math.MaxUint64, LoHi: math.MaxUint64, LoLo: math.MaxUint64}}, "-1"},
	}
	for _, c := range cases {
		got, ok := ScValBigInt(c.v)
		if !ok || got.String() != c.want {
			t.Errorf("got %v want %s", got, c.want)
		}
	}
	if j := ScValToJSON(smap("amount", n(-3), "name", strv("x"))); j.(map[string]any)["amount"] != "-3" {
		t.Errorf("json map: %#v", j)
	}
}

func TestDecodeSoroswapPairSwap(t *testing.T) {
	pi := newProtocolIndex()
	d := pi.decode("CPAIR", []xdr.ScVal{strv("SoroswapPair"), sym("swap")},
		smap("amount_0_in", n(0), "amount_0_out", n(127679), "amount_1_in", n(19131), "amount_1_out", n(0)))
	if d.protocol != "soroswap" || d.swap == nil {
		t.Fatalf("not decoded: %+v", d)
	}
	if d.swap.amountIn.Int64() != 19131 || d.swap.amountOut.Int64() != 127679 {
		t.Fatalf("direction wrong: in=%v out=%v", d.swap.amountIn, d.swap.amountOut)
	}
	// Later non-swap events from the same contract stay attributed.
	if d := pi.decode("CPAIR", []xdr.ScVal{sym("skim_custom")}, n(1)); d.protocol != "soroswap" {
		t.Fatalf("contract attribution lost: %+v", d)
	}
}

func TestDecodeCLMMSwap(t *testing.T) {
	pi := newProtocolIndex()
	d := pi.decode("CPOOL", []xdr.ScVal{sym("swap")}, smap("amount0", n(-846668629), "amount1", n(5839827)))
	if d.protocol != "clmm" || d.swap == nil {
		t.Fatalf("not decoded: %+v", d)
	}
	if d.swap.amountIn.Int64() != 5839827 || d.swap.amountOut.Int64() != 846668629 {
		t.Fatalf("clmm amounts: in=%v out=%v", d.swap.amountIn, d.swap.amountOut)
	}
}

func TestDecodeIgnoresUnknown(t *testing.T) {
	pi := newProtocolIndex()
	if d := pi.decode("CX", []xdr.ScVal{sym("hello")}, n(1)); d.protocol != "" || d.event != nil {
		t.Fatalf("unexpected decode: %+v", d)
	}
}

func TestTickOf(t *testing.T) {
	// Taker sells 100 XLM for 25 USDC: canonical pair is (USDC..., native)
	// because "USDC:..." < "native"; price = XLM per USDC = 4.
	tk, ok := TickOf(Trade{SoldAsset: "native", SoldAmount: "1000000000", BoughtAsset: "USDC:G", BoughtAmount: "250000000"})
	if !ok || tk.Base != "USDC:G" || tk.Counter != "native" || tk.Price != 4 || tk.BaseVol != 25 || tk.CountVol != 100 {
		t.Fatalf("tick: %+v", tk)
	}
	if _, ok := TickOf(Trade{SoldAsset: "a", SoldAmount: "0", BoughtAsset: "b", BoughtAmount: "1"}); ok {
		t.Fatal("zero amount must not tick")
	}
}
