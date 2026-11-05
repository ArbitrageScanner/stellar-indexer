package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/query"
)

func (s *Server) leaderboards(w http.ResponseWriter, r *http.Request) {
	out, err := s.buildLeaderboards(r.Context(), hoursParam(r, 24), limitParam(r, 15, 100))
	s.reply(w, r, out, err)
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	def := analytics.DefaultAlertThresholds
	th := analytics.AlertThresholds{
		MinXLM: floatParam(r, "min_xlm", def.MinXLM), MinMove: floatParam(r, "min_move", def.MinMove),
		MinProfit: floatParam(r, "min_profit", def.MinProfit),
	}
	kinds := map[string]bool{}
	for _, k := range strings.Split(r.URL.Query().Get("kinds"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds[k] = true
		}
	}
	out, err := s.buildAlerts(r.Context(), hoursParam(r, 24), limitParam(r, 200, 1000), th, kinds)
	s.reply(w, r, out, err)
}

func (s *Server) labels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tag := q.Get("tag")
	if tag != "" {
		tag = analytics.NormalizeTag(tag)
	}
	rows, err := s.repo.Labels(r.Context(), query.LabelFilter{Tag: tag, Address: q.Get("address"), Limit: limitParam(r, 200, 2000)})
	s.reply(w, r, rows, err)
}

func (s *Server) labelTags(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.LabelTags(r.Context())
	s.reply(w, r, rows, err)
}

func (s *Server) putLabel(w http.ResponseWriter, r *http.Request) {
	if code, err := s.labelWriteAllowed(r); err != nil {
		writeError(w, r, code, err.Error())
		return
	}
	var in analytics.Label
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err == nil {
		err = json.Unmarshal(body, &in)
	}
	if err != nil {
		s.reply(w, r, nil, badf("body must be JSON {address, tag, note} up to 4 KB"))
		return
	}
	l, err := analytics.NormalizeLabel(in)
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	row, err := s.repo.PutLabel(r.Context(), l)
	s.reply(w, r, row, err)
}

func (s *Server) deleteLabel(w http.ResponseWriter, r *http.Request) {
	if code, err := s.labelWriteAllowed(r); err != nil {
		writeError(w, r, code, err.Error())
		return
	}
	ok, err := s.repo.DeleteLabel(r.Context(), r.PathValue("address"), analytics.NormalizeTag(r.PathValue("tag")))
	if err == nil && !ok {
		err = errNotFound
	}
	s.reply(w, r, map[string]bool{"deleted": true}, err)
}
