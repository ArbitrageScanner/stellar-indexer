package api

import (
	"net/http"

	"github.com/ArbitrageScanner/stellar-indexer/internal/query"
)

func (s *Server) transactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := query.TxFilter{Source: q.Get("source"), Contract: q.Get("contract"), Function: q.Get("function"), Limit: limitParam(r, 50, 500)}
	var err error
	if f.Before, err = int64Param(r, "before"); err == nil {
		if f.Successful, err = boolParam(r, "successful"); err == nil {
			f.Soroban, err = boolParam(r, "soroban")
		}
	}
	if err == nil && q.Get("ledger") != "" {
		var l int64
		if l, err = int64Param(r, "ledger"); err == nil {
			f.Ledger = &l
		}
	}
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.Transactions(r.Context(), f)
	s.reply(w, r, rows, err)
}

func (s *Server) transaction(w http.ResponseWriter, r *http.Request) {
	row, err := orNotFound(s.repo.Transaction(r.Context(), lower(r.PathValue("hash"))))
	s.reply(w, r, row, err)
}

func (s *Server) operations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := query.OpFilter{Type: q.Get("type"), Source: q.Get("source"), Limit: limitParam(r, 50, 500)}
	var err error
	if f.Before, err = int64Param(r, "before"); err == nil {
		f.Successful, err = boolParam(r, "successful")
	}
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.Operations(r.Context(), f)
	s.reply(w, r, rows, err)
}

func (s *Server) transfers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	asset := q.Get("asset")
	if asset != "" {
		asset = assetParam(asset)
	}
	rows, err := s.repo.Transfers(r.Context(), query.TransferFilter{Before: q.Get("before"), Asset: asset, Type: q.Get("type"), Limit: limitParam(r, 50, 500)})
	s.reply(w, r, rows, err)
}

func (s *Server) eventFilter(r *http.Request, contract string) query.EventFilter {
	q := r.URL.Query()
	if contract == "" {
		contract = q.Get("contract")
	}
	return query.EventFilter{Before: q.Get("before"), Contract: contract, Topic: q.Get("topic"), Protocol: q.Get("protocol"), Limit: limitParam(r, 50, 500)}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.Events(r.Context(), s.eventFilter(r, ""))
	s.reply(w, r, rows, err)
}

func (s *Server) contractEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.Events(r.Context(), s.eventFilter(r, r.PathValue("id")))
	s.reply(w, r, rows, err)
}

func (s *Server) contracts(w http.ResponseWriter, r *http.Request) {
	_, since, err := s.window(r.Context(), hoursParam(r, 24))
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.Contracts(r.Context(), since, limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) contract(w http.ResponseWriter, r *http.Request) {
	row, err := s.repo.Contract(r.Context(), r.PathValue("id"))
	s.reply(w, r, row, err)
}
