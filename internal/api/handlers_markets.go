package api

import (
	"net/http"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/query"
)

func (s *Server) assets(w http.ResponseWriter, r *http.Request) {
	_, since, err := s.window(r.Context(), hoursParam(r, 24))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.Assets(r.Context(), since, limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	_, since, err := s.window(r.Context(), 24)
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	row, err := orNotFound(s.repo.Asset(r.Context(), assetParam(r.PathValue("key")), since))
	s.reply(w, r, row, err)
}

func (s *Server) assetHolders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.AssetHolders(r.Context(), assetParam(r.PathValue("key")), limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) pairs(w http.ResponseWriter, r *http.Request) {
	_, since, err := s.window(r.Context(), hoursParam(r, 24))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.Pairs(r.Context(), since, limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) trades(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := query.TradeFilter{Before: q.Get("before"), Venue: q.Get("venue"), Taker: q.Get("taker"), Pool: q.Get("pool"), Limit: limitParam(r, 50, 1000)}
	var err error
	if q.Get("base") != "" || q.Get("counter") != "" {
		f.Base, f.Counter, _, err = analytics.CanonicalPair(q.Get("base"), q.Get("counter"))
	}
	if err == nil {
		f.Verified, err = boolParam(r, "verified")
	}
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.Trades(r.Context(), f)
	s.reply(w, r, rows, err)
}

func (s *Server) price(w http.ResponseWriter, r *http.Request) {
	b, c, inv, err := analytics.CanonicalPair(r.URL.Query().Get("base"), r.URL.Query().Get("counter"))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	_, since, err := s.window(r.Context(), 24)
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	t, err := s.repo.Ticker(r.Context(), b, c, since)
	s.reply(w, r, t.Orient(inv), err)
}

type candlesResponse struct {
	Base     string             `json:"base"`
	Counter  string             `json:"counter"`
	Interval string             `json:"interval"`
	Candles  []analytics.Candle `json:"candles"`
}

func (s *Server) candles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	b, c, inv, err := analytics.CanonicalPair(q.Get("base"), q.Get("counter"))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	iv := q.Get("interval")
	if iv == "" {
		iv = "1h"
	}
	step, ok := analytics.Intervals[iv]
	if !ok {
		s.reply(w, r, nil, badf("interval must be one of 1m,5m,15m,30m,1h,4h,1d"))
		return
	}
	anchor, err := s.repo.Anchor(r.Context())
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	to, err := timeParam(r, "to", anchor.Add(time.Minute))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	from, err := timeParam(r, "from", to.Add(-7*24*time.Hour))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	cs, err := s.repo.Candles(r.Context(), b, c, step, from, to, limitParam(r, 500, 5000))
	if inv {
		for i := range cs {
			cs[i] = cs[i].Invert()
		}
		b, c = c, b
	}
	s.reply(w, r, candlesResponse{Base: b, Counter: c, Interval: iv, Candles: cs}, err)
}

func (s *Server) pools(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.Pools(r.Context(), limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}
