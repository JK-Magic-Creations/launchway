package api

import (
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"

	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// The check routes make the server contact an address a caller gives. They
// are audited, rate limited per caller and for the server as a whole, and
// answer with short, generic text; the detail goes to the log. Local
// addresses and internal networks are refused by internal/egress.
const (
	checkPerMinute = 12
	checkBurst     = 4
	checkAllBurst  = 30
	maxCheckUsers  = 2000
)

// checkLimiter limits the check routes.
type checkLimiter struct {
	mu   sync.Mutex
	per  map[string]*rate.Limiter
	all  *rate.Limiter
	conf bool
}

func (c *checkLimiter) allow(who string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.all == nil {
		c.all, c.per = rate.NewLimiter(rate.Limit(2*checkPerMinute)/60, checkAllBurst), map[string]*rate.Limiter{}
	}
	l := c.per[who]
	if l == nil {
		if len(c.per) >= maxCheckUsers {
			c.per = map[string]*rate.Limiter{}
		}
		l = rate.NewLimiter(rate.Limit(checkPerMinute)/60, checkBurst)
		c.per[who] = l
	}
	// Both must allow; the per-caller token is only spent if the server's is.
	if !c.all.Allow() {
		return false
	}
	return l.Allow()
}

// throttleCheck answers 429 when the caller has made too many checks.
func (s *Server) throttleCheck(w http.ResponseWriter, r *http.Request) bool {
	who := identityFrom(r).User
	if who == "" {
		who = "addr:" + s.clientAddr(r)
	}
	if s.checks.allow(who) {
		return true
	}
	w.Header().Set("Retry-After", "10")
	writeErr(w, http.StatusTooManyRequests, "too many checks; wait a few seconds and try again")
	return false
}

// POST /api/v1/config/check/repository: reads a repository the way the
// server would (its Git credentials), keeping nothing, so a wizard can offer
// the branches and say whether a pipeline file is there.
func (s *Server) checkRepository(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.throttleCheck(w, r) {
		return
	}
	setAudit(r, auditAddress(b.Repo), "")
	res := source.CheckRepository(r.Context(), b.Repo, b.Branch)
	switch {
	case res.Blocked:
		setAudit(r, "", "refused: internal or local address")
	case res.OK:
		setAudit(r, "", "readable")
	default:
		setAudit(r, "", "not readable")
	}
	writeJSON(w, http.StatusOK, res)
}

// POST /api/v1/config/check/signin: checks sign-in answers against the
// identity provider.
func (s *Server) checkSignIn(w http.ResponseWriter, r *http.Request) {
	var b settings.SignInRequest
	if !decode(w, r, &b) {
		return
	}
	if !s.throttleCheck(w, r) {
		return
	}
	setAudit(r, auditAddress(b.Issuer), "")
	res := settings.CheckSignIn(r.Context(), b, nil)
	if res.Blocked {
		setAudit(r, "", "refused: internal address")
	}
	writeJSON(w, http.StatusOK, res)
}

// auditAddress is an address as the audit trail keeps it: without
// credentials, query or fragment, and short.
func auditAddress(a string) string {
	a = strings.TrimSpace(a)
	if i := strings.IndexAny(a, "?#"); i >= 0 {
		a = a[:i]
	}
	if i := strings.Index(a, "://"); i >= 0 {
		rest := a[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 && at < strings.Index(rest+"/", "/") {
			a = a[:i+3] + rest[at+1:]
		}
	}
	return clip(a, 200)
}
