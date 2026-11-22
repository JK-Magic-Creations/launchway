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
	list, err := s.Engine.TokenList(identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// tokenRequest is the body that makes a token.
type tokenRequest struct {
	Name          string `json:"name"`
	ExpiresInDays int    `json:"expires_in_days"`
	// Role limits the token to viewer or operator (or admin); empty is
	// whatever its owner has when it is used. Projects limits it to
	// Flights matching these patterns. Repositories (owner/name, or
	// owner/*) with RepoAccess ("read" or "write") limits it to hosted
	// repositories and nothing else. SourceRanges limits where it may be used
	// from. Repositories cannot be combined with Projects.
	Role         string   `json:"role"`
	Projects     []string `json:"projects"`
	Repositories []string `json:"repositories"`
	RepoAccess   string   `json:"repo_access"`
	SourceRanges []string `json:"source_ranges"`
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	id := identityFrom(r)
	var in tokenRequest
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
	s.mintToken(w, r, id, id.User, "", in)
}

// mintToken validates a request and stores the token for owner, who is the
// maker (a person) or a service account's principal. floor is the service
// account's role ("" for a person).
func (s *Server) mintToken(w http.ResponseWriter, r *http.Request, id *identity, owner, floor string, in tokenRequest) {
	if !auth.ValidName(in.Name) || len(in.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "give the token a name of 1 to 100 characters")
		return
	}
	most := s.tokenMaxDays()
	days := in.ExpiresInDays
	if days == 0 {
		days = min(defaultTokenDays, most)
	}
	if days < 1 || days > most {
		writeErr(w, http.StatusBadRequest, "a token must expire in 1 to "+strconv.Itoa(most)+" days")
		return
	}
	if len(in.Repositories) > 0 && len(in.Projects) > 0 {
		writeErr(w, http.StatusBadRequest, "a token limited to repositories can do nothing else, so it cannot also name Flights: make two tokens")
		return
	}
	if floor == "" {
		if msg := s.checkTokenLimits(id, in.Role, in.Projects); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	} else if msg := s.checkServiceTokenLimits(floor, in.Role, in.Projects); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	repos, msg := s.parseTokenRepos(r, id, in.Repositories, in.RepoAccess)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	ranges, msg := parseSourceRanges(in.SourceRanges)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	now := s.Now()
	value := tokenPrefix + auth.RandomString(32)
	t := engine.APIToken{ID: ids.New("tok"), Name: in.Name, User: owner, CreatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour).UnixMilli(), Role: in.Role, Projects: in.Projects,
		Repositories: repos, RepoAccess: in.RepoAccess, SourceRanges: ranges}
	if err := s.Engine.CreateLimitedToken(t, auth.Hash(value)); err != nil {
		s.fail(w, err)
		return
	}
	detail := "expires in " + strconv.Itoa(days) + " days" + tokenLimitsDetail(in.Role, in.Projects)
	if len(repos) > 0 {
		detail += ", " + in.RepoAccess + " on " + strings.Join(in.Repositories, " ")
	}
	if len(ranges) > 0 {
		detail += ", only from " + strings.Join(ranges, " ")
	}
	if owner != id.User {
		detail += ", for " + owner
	}
	setAudit(r, in.Name, detail)
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
	limit := 100
	if n := num("limit"); n > 0 && n <= 500 { // checked as an int64, before it is narrowed
		limit = int(n)
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
