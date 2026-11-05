package analytics

import (
	"errors"
	"strings"
	"time"
)

// NormalizeAsset maps "XLM"/"native" (any case) to "native".
func NormalizeAsset(v string) string {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "xlm") || strings.EqualFold(v, "native") {
		return "native"
	}
	return v
}

// ErrBadPair is returned for a missing or degenerate pair.
var ErrBadPair = errors.New("base and counter asset keys are required (native, CODE:ISSUER or C... contract) and must differ")

// CanonicalPair orders a pair as stored (base < counter); inverted reports
// whether prices must be flipped for the caller's orientation.
func CanonicalPair(base, counter string) (b, c string, inverted bool, err error) {
	b, c = NormalizeAsset(base), NormalizeAsset(counter)
	if b == "" || c == "" || b == c {
		return "", "", false, ErrBadPair
	}
	if b <= c {
		return b, c, false, nil
	}
	return c, b, true, nil
}

// Ticker is a pair's last price and 24h statistics (counter per base).
type Ticker struct {
	Base             string     `json:"base"`
	Counter          string     `json:"counter"`
	Last             *float64   `json:"last"`
	LastTradeAt      *time.Time `json:"last_trade_at"`
	Open24h          *float64   `json:"open_24h"`
	High24h          *float64   `json:"high_24h"`
	Low24h           *float64   `json:"low_24h"`
	BaseVolume24h    *float64   `json:"base_volume_24h"`
	CounterVolume24h *float64   `json:"counter_volume_24h"`
	Trades24h        *int64     `json:"trades_24h"`
	Change24hPct     *float64   `json:"change_24h_pct,omitempty"`
}

func inv(p *float64) *float64 {
	if p == nil || *p == 0 {
		return p
	}
	return ptr(1 / *p)
}

// Orient flips a canonical ticker into the caller's orientation when
// inverted, and fills the 24h change.
func (t Ticker) Orient(inverted bool) Ticker {
	if inverted {
		t.Base, t.Counter = t.Counter, t.Base
		t.Last, t.Open24h = inv(t.Last), inv(t.Open24h)
		t.High24h, t.Low24h = inv(t.Low24h), inv(t.High24h)
		t.BaseVolume24h, t.CounterVolume24h = t.CounterVolume24h, t.BaseVolume24h
	}
	if t.Last != nil && t.Open24h != nil && *t.Open24h != 0 {
		t.Change24hPct = ptr((*t.Last / *t.Open24h - 1) * 100)
	}
	return t
}

// Candle is one OHLCV bucket (counter per base).
type Candle struct {
	T             time.Time `json:"t"`
	Open          float64   `json:"open"`
	High          float64   `json:"high"`
	Low           float64   `json:"low"`
	Close         float64   `json:"close"`
	BaseVolume    float64   `json:"base_volume"`
	CounterVolume float64   `json:"counter_volume"`
	Trades        int64     `json:"trades"`
}

// Invert flips a candle to the opposite orientation.
func (c Candle) Invert() Candle {
	r := func(v float64) float64 {
		if v == 0 {
			return 0
		}
		return 1 / v
	}
	c.Open, c.Close = r(c.Open), r(c.Close)
	c.High, c.Low = r(c.Low), r(c.High)
	c.BaseVolume, c.CounterVolume = c.CounterVolume, c.BaseVolume
	return c
}

// Intervals maps candle interval names to Postgres intervals.
var Intervals = map[string]string{
	"1m": "1 minute", "5m": "5 minutes", "15m": "15 minutes", "30m": "30 minutes",
	"1h": "1 hour", "4h": "4 hours", "1d": "1 day",
}
