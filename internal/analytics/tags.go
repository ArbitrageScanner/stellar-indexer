package analytics

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Heuristic thresholds for automatic tags (documented in docs/DATA.md and on
// the UI's Tags page).
const (
	WhaleXLM          = 1_000_000
	LargeHolderXLM    = 100_000
	HFTTxs24h         = 500
	HFTFills24h       = 1000
	MarketMakerOffers = 4   // open offers, at least one pair two-sided
	MarketMakerOps24h = 100 // offer updates in 24h
	ArbCycles7d       = 3
	FailingMinTxs     = 50
	NewAccountAge     = 7 * 24 * time.Hour
	DormantAfter      = 7 * 24 * time.Hour
	LedgerSeconds     = 5.5
)

// Signals are the per-address facts the tag heuristics consume.
type Signals struct {
	XLM           *float64 // nil when the account entry is not indexed
	CreatedLedger int64    // starting sequence >> 32; 0 = unknown
	Offers        int64
	TwoSidedOffer int64
	Txs24h        int64
	Failed24h     int64
	Fills24h      int64
	OfferOps24h   int64
	Cycles7d      int64
	FlashLoans    int64
	Protocols     []string
	IssuedCount   int64
	Issued        []string
	Token         string // asset key when the address is a token contract
	ProtoContract string // protocol when the address emits protocol events
	ProtoActions  []string
	LPShares      int64
	LPDeposits    int64
	Borrows       int64
	LastActive    *time.Time
	Tip           int64
	IndexStart    time.Time
}

// Tag is an automatic (heuristic) or user label.
type Tag struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Reason string `json:"reason"`
	Group  string `json:"group"` // size | behavior | role | defi | lifecycle | user
	Kind   string `json:"kind"`  // auto | user
	Note   string `json:"note,omitempty"`
}

// AutoTags derives behavioural tags. anchor is "now" in index time.
func AutoTags(s Signals, anchor time.Time) []Tag {
	var tags []Tag
	add := func(id, label, group, reason string, args ...any) {
		tags = append(tags, Tag{ID: id, Label: label, Group: group, Kind: "auto", Reason: fmt.Sprintf(reason, args...)})
	}
	if s.XLM != nil {
		switch x := *s.XLM; {
		case x >= WhaleXLM:
			add("whale", "Whale", "size", "Holds %s XLM (≥ %s).", Human(x), Human(WhaleXLM))
		case x >= LargeHolderXLM:
			add("shark", "Large holder", "size", "Holds %s XLM (≥ %s).", Human(x), Human(LargeHolderXLM))
		}
	}
	if s.Cycles7d >= ArbCycles7d || (s.Cycles7d >= 1 && s.FlashLoans > 0) {
		add("arbitrage_bot", "Arbitrage bot", "behavior", "%d same-transaction round trips (first sold asset = last bought) in 7 days.", s.Cycles7d)
	}
	if s.FlashLoans > 0 {
		add("flash_loans", "Flash loans", "defi", "Took %d Blend flash loans.", s.FlashLoans)
	}
	if (s.TwoSidedOffer > 0 && s.Offers >= MarketMakerOffers) || s.OfferOps24h >= MarketMakerOps24h {
		add("market_maker", "Market maker", "behavior", "%d open SDEX offers (%d two-sided) and %d offer updates in 24h.", s.Offers, s.TwoSidedOffer, s.OfferOps24h)
	}
	if s.Txs24h >= HFTTxs24h || s.Fills24h >= HFTFills24h {
		add("hft", "High-frequency", "behavior", "%d transactions and %d fills in the last 24h.", s.Txs24h, s.Fills24h)
	}
	if s.Txs24h >= FailingMinTxs && s.Failed24h*2 >= s.Txs24h {
		add("failing_bot", "Mostly failing", "behavior", "%d of %d transactions in 24h failed (typical of competing arbitrage bots).", s.Failed24h, s.Txs24h)
	}
	if s.IssuedCount > 0 {
		add("issuer", "Asset issuer", "role", "Issuer of %d asset(s): %s.", s.IssuedCount, strings.Join(s.Issued, ", "))
	}
	if s.Token != "" {
		add("token_contract", "Token contract", "role", "Stellar Asset Contract / token for %s.", s.Token)
	}
	if p := s.ProtoContract; p != "" {
		role := map[string]string{"blend": "lending pool", "aquarius": "pool", "soroswap": "pair", "clmm": "pool", "amm_generic": "pool"}[p]
		for _, a := range s.ProtoActions {
			if strings.Contains(strings.ToLower(a), "router") {
				role = "router"
			}
		}
		if role == "" {
			role = "contract"
		}
		acts := s.ProtoActions[:min(len(s.ProtoActions), 6)]
		add("protocol_"+p, ProtocolName(p)+" "+role, "role", "Emits %s events: %s.", p, strings.Join(acts, ", "))
	}
	if s.LPShares > 0 || s.LPDeposits > 0 {
		add("lp", "Liquidity provider", "defi", "Holds %d classic pool share(s); %d Soroban liquidity deposits.", s.LPShares, s.LPDeposits)
	}
	if s.Borrows > 0 {
		add("borrower", "Blend borrower", "defi", "%d borrow events on Blend.", s.Borrows)
	}
	if len(s.Protocols) > 0 {
		add("defi_user", "DeFi user", "defi", "Active on %s.", strings.Join(s.Protocols, ", "))
	}
	if s.CreatedLedger > 0 && s.Tip > 0 {
		age := time.Duration(float64(s.Tip-s.CreatedLedger)*LedgerSeconds) * time.Second
		if age >= 0 && age < NewAccountAge {
			add("new_account", "New account", "lifecycle", "Created ~%s ago (ledger %d, from its starting sequence number).", RoundDuration(age), s.CreatedLedger)
		}
	}
	if s.XLM != nil {
		switch {
		case s.LastActive == nil:
			add("dormant", "Dormant", "lifecycle", "No transactions or transfers since indexing began (%s).", s.IndexStart.UTC().Format("Jan 2 15:04 UTC"))
		case anchor.Sub(*s.LastActive) > DormantAfter:
			add("dormant", "Dormant", "lifecycle", "Last active %s ago.", RoundDuration(anchor.Sub(*s.LastActive)))
		}
	}
	return tags
}

// ProtocolName is the display name of a protocol key.
func ProtocolName(p string) string {
	switch p {
	case "amm_generic":
		return "Generic AMM"
	case "clmm":
		return "CLMM"
	case "":
		return ""
	}
	return strings.ToUpper(p[:1]) + p[1:]
}

// Human formats large numbers compactly (1.2K, 3.4M, 5.6B).
func Human(v float64) string {
	switch {
	case v >= 1e9:
		return strconv.FormatFloat(v/1e9, 'f', 1, 64) + "B"
	case v >= 1e6:
		return strconv.FormatFloat(v/1e6, 'f', 1, 64) + "M"
	case v >= 1e3:
		return strconv.FormatFloat(v/1e3, 'f', 1, 64) + "K"
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

// RoundDuration renders a coarse age: 3d, 5h, 12m.
func RoundDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
