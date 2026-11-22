package api

import (
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/sshkeys"
)

// The account in one answer: who you are, your SSH keys, your API tokens with their limits and expiry, the state of the
// second factor, your signed-in browsers (each can be ended, or all but this one), and a map of which optional areas
// this installation has, so that a client shows links only to what exists. Public profiles and Squadron teams are in
// account_people.go.

func (s *Server) accountRoutes() []route {
	return []route{
		{"GET /api/v1/account/summary", signedIn, "", s.accountSummary},
		{"GET /api/v1/account/sessions", signedIn, "", s.accountSessions},
		{"DELETE /api/v1/account/sessions/{id}", signedIn, "auth.session_end", s.accountEndSession},
		{"POST /api/v1/account/sessions/end-others", signedIn, "auth.session_end_others", s.accountEndOthers},
	}
}

// features says which optional areas exist here. A client renders a link only when its feature is true.
func (s *Server) features() map[string]bool {
	m := s.mcpOpts()
	return map[string]bool{
		"sign_in":        s.Auth != nil,
		"tokens":         s.Auth != nil,
		"sessions":       s.Auth != nil,
		"local_accounts": s.localOn(),
		"second_factor":  s.localOn(),
		"hosting":        s.Forge != nil,
		"ssh_keys":       s.Forge != nil && s.Forge.Keys != nil,
		"pull_requests":  s.Forge != nil && s.Forge.Pulls != nil,
		"registry":       s.Registry != nil,
		"graph":          s.Graph != nil,
		"toggles":        s.Toggles != nil,
		"standards":      s.Standards != nil,
		"settings":       s.Settings != nil && s.Settings.Available(),
		"mcp":            m != nil,
		"ai":             s.AI != nil,
		"work":           s.Engine != nil,
	}
}

type accountTokens struct {
	Items       []engine.APIToken `json:"items"`
	MaxDays     int               `json:"max_days"`
	DefaultDays int               `json:"default_days"`
	YourRole    string            `json:"your_role,omitempty"`
}

type secondFactorState struct {
	// Available is false when the sign-in is by an identity provider, which has its own.
	Available         bool `json:"available"`
	Enabled           bool `json:"enabled"`
	Required          bool `json:"required"`
	RecoveryCodesLeft int  `json:"recovery_codes_left"`
}

type sessionOut struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
	ExpiresAt int64  `json:"expires_at"`
	Current   bool   `json:"current"`
}

func (s *Server) currentSessionHash(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return auth.Hash(c.Value)
	}
	return ""
}

func (s *Server) sessionsOf(r *http.Request) ([]sessionOut, error) {
	out := []sessionOut{}
	id := identityFrom(r)
	if s.Auth == nil || id.User == "" {
		return out, nil
	}
	rows, err := s.Engine.SessionsOf(id.User, s.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	mine := s.currentSessionHash(r)
	for _, x := range rows {
		out = append(out, sessionOut{ID: x.ID, CreatedAt: x.CreatedAt, LastSeen: x.LastSeen, ExpiresAt: x.ExpiresAt, Current: mine != "" && x.Hash == mine})
	}
	return out, nil
}

func (s *Server) accountSummary(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	f := s.features()
	groups := id.Groups
	if groups == nil {
		groups = []string{}
	}
	out := map[string]any{
		"profile":  map[string]any{"user": id.User, "display_name": id.Profile.Name, "email": id.Profile.Email, "groups": groups, "via": id.Via, "admin": s.authorize(id, auth.Administer, "")},
		"features": f,
	}
	if f["tokens"] {
		list, err := s.Engine.TokenList(id.User)
		if err != nil {
			s.fail(w, err)
			return
		}
		t := accountTokens{Items: list, MaxDays: s.tokenMaxDays(), DefaultDays: min(defaultTokenDays, s.tokenMaxDays())}
		if have := s.cur().Policy.RoleAnywhere(id.User, id.Groups); have > auth.NoRole {
			t.YourRole = have.String()
		}
		out["tokens"] = t
	}
	if f["ssh_keys"] {
		ks, err := s.Forge.Keys.List(r.Context(), sshkeys.User, id.User)
		if err != nil {
			s.fail(w, err)
			return
		}
		out["ssh_keys"] = ks
	}
	sf := secondFactorState{}
	if f["second_factor"] && id.Via == viaSession {
		if a, err := s.Engine.LocalAccountByName(id.User); err == nil {
			left, _ := s.Engine.RecoveryCodesLeft(a.Username)
			sf = secondFactorState{Available: true, Enabled: a.TOTPConfirmed, Required: s.Auth.Local.secondFactorRequired(a.Role), RecoveryCodesLeft: left}
		}
	}
	out["second_factor"] = sf
	if f["sessions"] {
		ss, err := s.sessionsOf(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		out["sessions"] = ss
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) accountSessions(w http.ResponseWriter, r *http.Request) {
	ss, err := s.sessionsOf(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ss})
}

func (s *Server) accountEndSession(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if s.Auth == nil || id.User == "" {
		writeErr(w, http.StatusNotFound, "there are no sessions: sign-in is off on this server")
		return
	}
	sid := r.PathValue("id")
	ok, err := s.Engine.EndSessionByID(id.User, sid)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session of yours (the id is the one `GET /api/v1/account/sessions` gave)")
		return
	}
	setAudit(r, id.User, "session "+sid)
	if mine := s.currentSessionHash(r); mine != "" && strings.HasPrefix(mine, sid) {
		http.SetCookie(w, s.cookie(r, sessionCookie, "", "/", 0)) // that was this browser
	}
	writeJSON(w, http.StatusOK, map[string]any{"ended": 1})
}

func (s *Server) accountEndOthers(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if s.Auth == nil || id.User == "" {
		writeErr(w, http.StatusNotFound, "there are no sessions: sign-in is off on this server")
		return
	}
	// From a browser the one it is signed in on stays; a call made with a token has no session of its own, so every
	// session ends.
	keep := ""
	if id.Via == viaSession {
		keep = s.currentSessionHash(r)
	}
	before, err := s.Engine.SessionsOf(id.User, s.Now().UnixMilli())
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Engine.EndOtherSessions(id.User, keep); err != nil {
		s.fail(w, err)
		return
	}
	n := 0
	for _, x := range before {
		if x.Hash != keep {
			n++
		}
	}
	setAudit(r, id.User, "all other sessions")
	writeJSON(w, http.StatusOK, map[string]any{"ended": n})
}
