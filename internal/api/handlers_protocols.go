package api

import (
	"net/http"

	"github.com/ArbitrageScanner/stellar-indexer/internal/query"
)

type protocolsResponse struct {
	Hours             int  `json:"hours"`
	IncludeUnverified bool `json:"include_unverified"`
	query.ProtocolOverview
}

func (s *Server) protocols(w http.ResponseWriter, r *http.Request) {
	h := hoursParam(r, 24)
	_, since, err := s.window(r.Context(), h)
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	all := includeUnverified(r)
	o, err := s.repo.Protocols(r.Context(), since, all)
	s.reply(w, r, protocolsResponse{Hours: h, IncludeUnverified: all, ProtocolOverview: o}, err)
}

type protocolSummaryResponse struct {
	Protocol          string `json:"protocol"`
	Hours             int    `json:"hours"`
	IncludeUnverified bool   `json:"include_unverified"`
	query.ProtocolSummary
}

func (s *Server) protocolSummary(w http.ResponseWriter, r *http.Request) {
	h, name := hoursParam(r, 24), r.PathValue("name")
	_, since, err := s.window(r.Context(), h)
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	all := includeUnverified(r)
	sum, err := s.repo.ProtocolSummary(r.Context(), name, since, all)
	s.reply(w, r, protocolSummaryResponse{Protocol: name, Hours: h, IncludeUnverified: all, ProtocolSummary: sum}, err)
}

func (s *Server) protocolEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	asset := q.Get("asset")
	if asset != "" {
		asset = assetParam(asset)
	}
	rows, err := s.repo.ProtocolEvents(r.Context(), query.ProtocolEventFilter{
		Protocol: r.PathValue("name"), Before: q.Get("before"), Action: q.Get("action"), User: q.Get("user"),
		Contract: q.Get("contract"), Asset: asset, IncludeUnverified: includeUnverified(r), Limit: limitParam(r, 50, 1000),
	})
	s.reply(w, r, rows, err)
}
