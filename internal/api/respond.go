package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArbitrageScanner/stellar-indexer/internal/analytics"
	"github.com/ArbitrageScanner/stellar-indexer/internal/httpx"
	"github.com/ArbitrageScanner/stellar-indexer/internal/query"
)

// errNotFound is answered with 404.
var errNotFound = errors.New("not found")

// badRequest wraps a parameter validation failure (400).
type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

func badf(format string, args ...any) error { return badRequest{fmt.Sprintf(format, args...)} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	httpx.WriteError(w, r, code, msg)
}

// reply encodes v, or maps err to a status: validation → 400, missing →
// 404, timeout → 504, anything else → 500 (logged; details not leaked).
func (s *Server) reply(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, v)
		return
	}
	var br badRequest
	switch {
	case errors.As(err, &br), query.IsInvalid(err), errors.Is(err, analytics.ErrBadPair), errors.Is(err, analytics.ErrInvalidLabel):
		writeError(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not found")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, r, http.StatusGatewayTimeout, "query timed out")
	case errors.Is(err, context.Canceled):
		// client went away; nothing useful to send
	default:
		s.log.Error("request failed", "path", r.URL.Path, "err", err, "request_id", requestIDOf(r.Context()))
		writeError(w, r, http.StatusInternalServerError, "internal error")
	}
}

// orNotFound turns a nil row into errNotFound.
func orNotFound(row query.Row, err error) (query.Row, error) {
	if err == nil && row == nil {
		return nil, errNotFound
	}
	return row, err
}

// ---- parameters ---------------------------------------------------------------

// MaxHours bounds analytics windows (90 days).
const MaxHours = 24 * 90

func limitParam(r *http.Request, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, max)
}

func hoursParam(r *http.Request, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("hours"))
	if err != nil || n <= 0 {
		return def
	}
	return min(n, MaxHours)
}

// int64Param parses an optional integer; absent → 0.
func int64Param(r *http.Request, name string) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, badf("%s must be an integer", name)
	}
	return n, nil
}

// boolParam parses an optional boolean; absent → nil.
func boolParam(r *http.Request, name string) (*bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, badf("%s must be true or false", name)
	}
	return &b, nil
}

func floatParam(r *http.Request, name string, def float64) float64 {
	f, err := strconv.ParseFloat(r.URL.Query().Get(name), 64)
	if err != nil || f <= 0 {
		return def
	}
	return f
}

// includeUnverified reads ?verified=all (default: verified only).
func includeUnverified(r *http.Request) bool { return r.URL.Query().Get("verified") == "all" }

func timeParam(r *http.Request, name string, def time.Time) (time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return def, badf("%s must be RFC 3339", name)
	}
	return t, nil
}

func assetParam(v string) string { return analytics.NormalizeAsset(v) }

// window returns the index anchor and the start of an hours-long window.
func (s *Server) window(ctx context.Context, hours int) (anchor, since time.Time, err error) {
	anchor, err = s.repo.Anchor(ctx)
	return anchor, anchor.Add(-time.Duration(hours) * time.Hour), err
}

func lower(s string) string { return strings.ToLower(s) }
