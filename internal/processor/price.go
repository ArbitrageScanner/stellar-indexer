package processor

import (
	"math/big"
)

// Tick is a trade projected onto a canonical (base, counter) pair, where
// base < counter lexicographically. Price is counter units per base unit.
type Tick struct {
	Base, Counter     string
	Price             float64
	BaseVol, CountVol float64
}

// CanonicalPair orders two asset keys the way candles are stored.
func CanonicalPair(a, b string) (base, counter string, swapped bool) {
	if a <= b {
		return a, b, false
	}
	return b, a, true
}

// TickOf converts a trade into a candle tick. Amounts are raw base units; all
// classic assets and most Soroban tokens use 7 decimals so ratios are prices.
func TickOf(t Trade) (Tick, bool) {
	sold, ok1 := new(big.Float).SetString(t.SoldAmount)
	bought, ok2 := new(big.Float).SetString(t.BoughtAmount)
	if !ok1 || !ok2 || sold.Sign() <= 0 || bought.Sign() <= 0 || t.SoldAsset == t.BoughtAsset {
		return Tick{}, false
	}
	base, counter, _ := CanonicalPair(t.SoldAsset, t.BoughtAsset)
	var baseAmt, counterAmt *big.Float
	if base == t.SoldAsset {
		baseAmt, counterAmt = sold, bought
	} else {
		baseAmt, counterAmt = bought, sold
	}
	bv, _ := baseAmt.Float64()
	cv, _ := counterAmt.Float64()
	p, _ := new(big.Float).Quo(counterAmt, baseAmt).Float64()
	return Tick{Base: base, Counter: counter, Price: p, BaseVol: bv / 1e7, CountVol: cv / 1e7}, true
}
