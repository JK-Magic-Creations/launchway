package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/ids"
)

// API tokens let scripts call the API as the person who made the token, with
// that person's rights at the moment of each call. A token is shown once,
// when it is made; only its SHA-256 is kept.

const (
	tokenPrefix      = "lw_"
	defaultTokenDays = 90
	maxTokenDays     = 365
)

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	list, err := s.Engine.Tokens(identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	id := identityFrom(r)
	var in struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	setAudit(r, in.Name, "")
	if id.Via != viaSession {
		// Otherwise a stolen token could make itself a successor and
		// outlive its own expiry.
		writeErr(w, http.StatusForbidden, "tokens can only be made from a signed-in browser session, not with another token")
		return
	}
	if !auth.ValidName(in.Name) || len(in.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "give the token a name of 1 to 100 characters")
		return
	}
	days := in.ExpiresInDays
	if days == 0 {
		days = defaultTokenDays
	}
	if days < 1 || days > maxTokenDays {
		writeErr(w, http.StatusBadRequest, "a token must expire in 1 to "+strconv.Itoa(maxTokenDays)+" days")
		return
	}
	now := s.Now()
	value := tokenPrefix + auth.RandomString(32)
	t := engine.APIToken{ID: ids.New("tok"), Name: in.Name, User: id.User, CreatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour).UnixMilli()}
	if err := s.Engine.CreateToken(t, auth.Hash(value)); err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, "", "expires in "+strconv.Itoa(days)+" days")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, struct {
		engine.APIToken
		Token string `json:"token"`
	}{t, value})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	t, err := s.Engine.RevokeToken(identityFrom(r).User, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, t.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

// tokensOn refuses token calls when sign-in is off, where they would mean
// nothing.
func (s *Server) tokensOn(w http.ResponseWriter) bool {
	if s.Auth == nil {
		writeErr(w, http.StatusNotFound, "API tokens need sign-in, which is off on this server (auth.mode: none)")
		return false
	}
	return true
}

// auditLog lists the audit trail, newest first, a page at a time:
// ?user= ?project= ?action= ?from= ?to= (Unix milliseconds) ?before= (the
// next_before of the previous page) ?limit=.
func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	num := func(k string) int64 { v, _ := strconv.ParseInt(q.Get(k), 10, 64); return v }
	limit := int(num("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	list, err := s.Engine.AuditLog(engine.AuditFilter{User: q.Get("user"), Project: q.Get("project"), Action: q.Get("action"),
		From: num("from"), To: num("to"), Before: num("before"), Limit: limit})
	if err != nil {
		s.fail(w, err)
		return
	}
	var next int64
	if len(list) == limit {
		next = list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": list, "next_before": next})
}
