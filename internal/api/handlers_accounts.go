package api

import (
	"net/http"
	"time"
)

func (s *Server) topAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.TopAccounts(r.Context(), limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	anchor, err := s.repo.Anchor(r.Context())
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	row, err := s.repo.Account(r.Context(), r.PathValue("id"), anchor.Add(-24*time.Hour))
	s.reply(w, r, row, err)
}

func (s *Server) accountOperations(w http.ResponseWriter, r *http.Request) {
	before, err := int64Param(r, "before")
	if err != nil {
		s.reply(w, r, nil, err)
		return
	}
	rows, err := s.repo.AccountOperations(r.Context(), r.PathValue("id"), before, limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) accountTransfers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.AccountTransfers(r.Context(), r.PathValue("id"), limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) accountTrades(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.AccountTrades(r.Context(), r.PathValue("id"), limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) accountEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.repo.AccountEvents(r.Context(), r.PathValue("id"), limitParam(r, 50, 500))
	s.reply(w, r, rows, err)
}

func (s *Server) portfolio(w http.ResponseWriter, r *http.Request) {
	p, err := s.buildPortfolio(r.Context(), r.PathValue("id"), r.URL.Query().Get("history") != "false")
	s.reply(w, r, p, err)
}

func (s *Server) accountStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.buildStats(r.Context(), r.PathValue("id"), hoursParam(r, 24*7))
	s.reply(w, r, st, err)
}

func (s *Server) accountTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.buildTags(r.Context(), r.PathValue("id"))
	s.reply(w, r, tags, err)
}
