package api

import (
	"context"
	"strings"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/query"
)

// Services orchestrate repository reads and domain functions for endpoints
// that combine several inputs. They hold no SQL and no rules of their own.

// buildPortfolio values an address's holdings at the index anchor and, when
// asked, reconstructs up to 7 days of hourly history.
func (s *Server) buildPortfolio(ctx context.Context, id string, withHistory bool) (*analytics.Portfolio, error) {
	in, err := s.repo.PortfolioInput(ctx, id)
	if err != nil {
		return nil, err
	}
	anchor, err := s.repo.Anchor(ctx)
	if err != nil {
		return nil, err
	}
	hs := analytics.Holdings(in)
	assets := analytics.PricedAssets(hs, s.cfg.USDAsset)
	var px analytics.PriceSet
	for _, p := range []struct {
		dst *analytics.Prices
		at  time.Time
	}{{&px.Now, anchor}, {&px.Ago24h, anchor.Add(-24 * time.Hour)}, {&px.Ago7d, anchor.Add(-7 * 24 * time.Hour)}} {
		if *p.dst, err = s.repo.XLMPrices(ctx, assets, p.at); err != nil {
			return nil, err
		}
	}
	p := analytics.Value(in, hs, px, s.cfg.USDAsset)
	if !withHistory {
		return p, nil
	}
	start, err := s.repo.IndexStart(ctx)
	if err != nil {
		return nil, err
	}
	if weekAgo := anchor.Add(-7 * 24 * time.Hour); start.Before(weekAgo) {
		start = weekAgo
	}
	start = start.Truncate(time.Hour)
	liquid := make([]string, 0, len(px.Now))
	for _, a := range assets {
		if _, ok := px.Now[a]; ok {
			liquid = append(liquid, a)
		}
	}
	flows, err := s.repo.HourlyFlows(ctx, id, start)
	if err != nil {
		return nil, err
	}
	hourly, err := s.repo.HourlyPrices(ctx, liquid, start)
	if err != nil {
		return nil, err
	}
	analytics.History(p, flows, hourly, start, anchor, s.cfg.USDAsset)
	return p, nil
}

// statsResponse is an address's indicators over a window.
type statsResponse struct {
	Account   string            `json:"account"`
	Hours     int               `json:"hours"`
	Trading   analytics.Trading `json:"trading"`
	USDPerXLM *float64          `json:"usd_per_xlm,omitempty"`
	query.AccountActivity
}

func (s *Server) buildStats(ctx context.Context, id string, hours int) (*statsResponse, error) {
	anchor, since, err := s.window(ctx, hours)
	if err != nil {
		return nil, err
	}
	fills, truncated, err := s.repo.TakerFills(ctx, id, since)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, f := range fills {
		set[f.Sold], set[f.Bought] = true, true
	}
	assets := make([]string, 0, len(set)+1)
	for a := range set {
		assets = append(assets, a)
	}
	if s.cfg.USDAsset != "" {
		assets = append(assets, s.cfg.USDAsset)
	}
	px, err := s.repo.XLMPrices(ctx, assets, anchor)
	if err != nil {
		return nil, err
	}
	act, err := s.repo.AccountActivity(ctx, id, since, anchor.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	out := &statsResponse{Account: id, Hours: hours, Trading: analytics.SummarizeTrading(fills, px, truncated), AccountActivity: act}
	if u := px[s.cfg.USDAsset]; u > 0 {
		v := 1 / u
		out.USDPerXLM = &v
	}
	return out, nil
}

// buildTags returns automatic tags followed by user labels.
func (s *Server) buildTags(ctx context.Context, id string) ([]analytics.Tag, error) {
	anchor, err := s.repo.Anchor(ctx)
	if err != nil {
		return nil, err
	}
	sig, err := s.repo.TagSignals(ctx, id, anchor)
	if err != nil {
		return nil, err
	}
	tags := analytics.AutoTags(sig, anchor)
	user, err := s.repo.UserTags(ctx, id)
	if err != nil {
		return nil, err
	}
	tags = append(tags, user...)
	if tags == nil {
		tags = []analytics.Tag{}
	}
	return tags, nil
}

// buildLeaderboards ranks addresses and pools and attaches user labels.
func (s *Server) buildLeaderboards(ctx context.Context, hours, limit int) (map[string]any, error) {
	anchor, since, err := s.window(ctx, hours)
	if err != nil {
		return nil, err
	}
	boards, err := s.repo.Leaderboards(ctx, since, anchor.Add(-24*time.Hour), limit)
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, b := range boards {
		for _, row := range b.Rows {
			if a, ok := row[b.Key].(string); ok && (strings.HasPrefix(a, "G") || strings.HasPrefix(a, "C") || strings.HasPrefix(a, "M")) {
				addrs = append(addrs, a)
			}
		}
	}
	labels, err := s.repo.LabelsFor(ctx, addrs)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"hours": hours}
	for _, b := range boards {
		analytics.AttachLabels(b.Rows, b.Key, labels)
		out[b.Name] = b.Rows
	}
	return out, nil
}

type alertsResponse struct {
	Hours        int              `json:"hours"`
	MinXLM       float64          `json:"min_xlm"`
	MinMovePct   float64          `json:"min_move_pct"`
	MinProfitXLM float64          `json:"min_profit_xlm"`
	Counts       map[string]int   `json:"counts"`
	Alerts       []map[string]any `json:"alerts"`
}

func (s *Server) buildAlerts(ctx context.Context, hours, limit int, th analytics.AlertThresholds, kinds map[string]bool) (*alertsResponse, error) {
	anchor, since, err := s.window(ctx, hours)
	if err != nil {
		return nil, err
	}
	first, err := s.repo.FirstLedger(ctx)
	if err != nil {
		return nil, err
	}
	byKind, err := s.repo.Alerts(ctx, query.AlertQuery{
		Since: since, PxSince: anchor.Add(-24 * time.Hour), MoveSince: anchor.Add(-time.Hour),
		Thresholds: th, Kinds: kinds, IndexStartLedger: first,
	})
	if err != nil {
		return nil, err
	}
	all, counts := analytics.MergeAlerts(byKind, limit)
	return &alertsResponse{Hours: hours, MinXLM: th.MinXLM, MinMovePct: th.MinMove, MinProfitXLM: th.MinProfit, Counts: counts, Alerts: all}, nil
}
