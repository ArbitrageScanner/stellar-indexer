package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/query"
)

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if s.cfg.UI != nil && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	var eps []string
	for _, rt := range s.routes() {
		if rt.pattern != "/{$}" {
			eps = append(eps, rt.method+" "+rt.pattern)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "stellar-indexer", "network": s.cfg.Network, "ui": s.cfg.UI != nil, "openapi": "/openapi.yaml",
		"endpoints": eps, "usd_asset": s.cfg.USDAsset,
		"labels_writable": s.cfg.LabelsOpen || s.cfg.LabelsToken != "", "labels_auth": s.cfg.LabelsToken != "",
		"api_keys": len(s.cfg.APIKeys) > 0,
	})
}

// health is the liveness probe: the process serves and Postgres answers.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.repo.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "down", "reason": "database unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready is the readiness probe: Postgres answers and, when ReadyMaxLag is
// set, the newest indexed ledger closed recently enough.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	fail := func(reason string, extra map[string]any) {
		body := map[string]any{"status": "not ready", "reason": reason}
		for k, v := range extra {
			body[k] = v
		}
		writeJSON(w, http.StatusServiceUnavailable, body)
	}
	if err := s.repo.Ping(r.Context()); err != nil {
		fail("database unreachable", nil)
		return
	}
	st, err := s.repo.Status(r.Context(), s.cfg.CursorName)
	if err != nil {
		fail("status query failed", nil)
		return
	}
	if s.cfg.ReadyMaxLag > 0 {
		if st.LastClosedAt == nil {
			fail("no ledgers indexed yet", nil)
			return
		}
		if lag := time.Since(*st.LastClosedAt); lag > s.cfg.ReadyMaxLag {
			fail("index is behind", map[string]any{"lag_seconds": int(lag.Seconds()), "max_lag_seconds": int(s.cfg.ReadyMaxLag.Seconds())})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "last_closed_at": st.LastClosedAt})
}

// statusResponse extends the stored status with derived lag figures.
type statusResponse struct {
	query.Status
	Network    string `json:"network"`
	RPCTip     *int64 `json:"rpc_tip"`
	LagLedgers *int64 `json:"lag_ledgers"`
	LagSeconds *int   `json:"lag_seconds"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.repo.Status(r.Context(), s.cfg.CursorName)
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	out := statusResponse{Status: st, Network: s.cfg.Network}
	for _, c := range st.Components {
		if c.Tip != nil && (out.RPCTip == nil || *c.Tip > *out.RPCTip) {
			out.RPCTip = c.Tip
		}
	}
	if out.RPCTip != nil && st.Cursor != nil && *out.RPCTip >= *st.Cursor {
		lag := *out.RPCTip - *st.Cursor
		out.LagLedgers = &lag
	}
	if st.LastClosedAt != nil {
		sec := int(time.Since(*st.LastClosedAt).Seconds())
		out.LagSeconds = &sec
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ledgers(w http.ResponseWriter, r *http.Request) {
	before, err := int64Param(r, "before")
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.Ledgers(r.Context(), before, limitParam(r, 20, 200))
	s.reply(w, r, rows, err)
}

func (s *Server) ledger(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		s.reply(w, r, nil, badf("bad ledger sequence"))
		return
	}
	row, err := orNotFound(s.repo.Ledger(r.Context(), seq))
	s.reply(w, r, row, err)
}

func (s *Server) networkStats(w http.ResponseWriter, r *http.Request) {
	bucket := r.URL.Query().Get("bucket")
	if bucket != "minute" && bucket != "day" {
		bucket = "hour"
	}
	_, since, err := s.window(r.Context(), hoursParam(r, 24))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.NetworkStats(r.Context(), since, bucket)
	s.reply(w, r, rows, err)
}

func (s *Server) operationStats(w http.ResponseWriter, r *http.Request) {
	_, since, err := s.window(r.Context(), hoursParam(r, 24))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.OperationStats(r.Context(), since)
	s.reply(w, r, rows, err)
}

func (s *Server) tableStats(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.TableStats(r.Context())
	s.reply(w, r, rows, err)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		s.reply(w, r, nil, badf("q is required"))
		return
	}
	hits, err := s.repo.Search(r.Context(), q)
	s.reply(w, r, hits, err)
}
