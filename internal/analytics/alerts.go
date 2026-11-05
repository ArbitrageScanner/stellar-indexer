package analytics

import (
	"sort"
	"time"
)

// Alert kinds, in display order.
var AlertKinds = []string{"large_transfer", "large_trade", "flash_loan", "arbitrage", "price_move", "new_asset"}

// AlertThresholds parameterise the alert queries.
type AlertThresholds struct {
	MinXLM    float64 // large transfers, swaps
	MinProfit float64 // arbitrage profit in XLM
	MinMove   float64 // hourly price move, percent
}

// DefaultAlertThresholds are used when a parameter is missing or invalid.
var DefaultAlertThresholds = AlertThresholds{MinXLM: 25000, MinProfit: 5, MinMove: 5}

// MinPriceMoveVolumeXLM is the hourly XLM volume a pair needs before its
// price move counts as notable.
const MinPriceMoveVolumeXLM = 250

// NewAssetGraceLedgers: assets first seen within this many ledgers of the
// index start are old assets the index simply had not seen yet.
const NewAssetGraceLedgers = 15709 // ~1 day

// MergeAlerts tags rows with their kind, merges them newest first and caps
// the list; counts are per kind before capping.
func MergeAlerts(byKind map[string][]map[string]any, limit int) (all []map[string]any, counts map[string]int) {
	all = []map[string]any{}
	counts = map[string]int{}
	for _, k := range AlertKinds {
		rows, ok := byKind[k]
		if !ok {
			continue
		}
		for _, r := range rows {
			r["kind"] = k
		}
		counts[k] = len(rows)
		all = append(all, rows...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		ti, _ := all[i]["closed_at"].(time.Time)
		tj, _ := all[j]["closed_at"].(time.Time)
		return ti.After(tj)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, counts
}
