package api

// API tokens limited to a role and to projects, and limits on failed
// authentication.

import (
	"fmt"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// limited reports whether the identity is a token with limits.
func (id *identity) limited() bool {
	return id != nil && id.Via == viaToken && (id.capRole > auth.NoRole || len(id.scope) > 0)
}

// limit applies a token's limits to a role its owner has on a project.
func (id *identity) limit(r auth.Role, project string) auth.Role {
	if !id.limited() {
		return r
	}
	if len(id.scope) > 0 && !matchPattern(id.scope, project) {
		return auth.NoRole
	}
	if id.capRole > auth.NoRole && r > id.capRole {
		return id.capRole
	}
	return r
}

func matchPattern(patterns []string, project string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, project); ok {
			return true
		}
	}
	return false
}

// roleOn is the role the identity acts with on a project.
func (s *Server) roleOn(id *identity, project string) auth.Role {
	return id.limit(s.cur().Policy.RoleOn(id.User, id.Groups, project), project)
}

// limitedAllows is Policy.Allows for a token with limits.
func (s *Server) limitedAllows(id *identity, a auth.Action, project string) bool {
	switch a {
	case auth.Read:
		return s.roleOn(id, project) >= auth.Viewer
	case auth.Operate:
		return s.roleOn(id, project) >= auth.Operator
	case auth.AdministerProject:
		return s.roleOn(id, project) >= auth.Admin
	case auth.ReadAny:
		known, err := s.knownProjects()
		if err != nil {
			return false
		}
		for _, p := range known {
			if s.roleOn(id, p) >= auth.Viewer {
				return true
			}
		}
		return false
	case auth.Administer:
		// Admin of every project: a token limited to some projects never is.
		return len(id.scope) == 0 && (id.capRole == auth.NoRole || id.capRole >= auth.Admin) &&
			s.cur().Policy.RoleEverywhere(id.User, id.Groups) >= auth.Admin
	}
	return false
}

// ---- failed authentication ----

// failureLimiter counts failed attempts to authenticate per source address
// and refuses a source that fails too often, for the rest of a window. It
// keeps at most maxSources addresses: when it is full, windows that have
// ended are dropped first, then the oldest. Each server counts on its own.
type failureLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	sources map[string]*failures
}

type failures struct {
	start time.Time
	n     int
}

const (
	maxFailuresPerWindow = 20
	failureWindow        = time.Minute
	maxSources           = 10000
)

var limiters sync.Map // *Server → *failureLimiter

func (s *Server) limiter() *failureLimiter {
	if l, ok := limiters.Load(s); ok {
		return l.(*failureLimiter)
	}
	l, _ := limiters.LoadOrStore(s, &failureLimiter{window: failureWindow, max: maxFailuresPerWindow, sources: map[string]*failures{}})
	return l.(*failureLimiter)
}

// fail records a failure from addr.
func (l *failureLimiter) fail(addr string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.sources[addr]
	if f == nil || now.Sub(f.start) >= l.window {
		if f == nil && len(l.sources) >= maxSources {
			l.evict(now)
		}
		f = &failures{start: now}
		l.sources[addr] = f
	}
	f.n++
}

func (l *failureLimiter) evict(now time.Time) {
	var oldest string
	var at time.Time
	for a, f := range l.sources {
		if now.Sub(f.start) >= l.window {
			delete(l.sources, a)
			continue
		}
		if oldest == "" || f.start.Before(at) {
			oldest, at = a, f.start
		}
	}
	if len(l.sources) >= maxSources && oldest != "" {
		delete(l.sources, oldest)
	}
}

// blocked reports for how much longer addr is refused; 0 if it is not.
func (l *failureLimiter) blocked(addr string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.sources[addr]
	if f == nil || f.n < l.max {
		return 0
	}
	left := l.window - now.Sub(f.start)
	if left <= 0 {
		delete(l.sources, addr)
		return 0
	}
	return left
}

// tooManyFailures answers 429 to a source that has failed to authenticate
// too often lately, and reports whether it did.
func (s *Server) tooManyFailures(w http.ResponseWriter, r *http.Request) bool {
	left := s.limiter().blocked(s.clientAddr(r), s.Now())
	if left <= 0 {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(left.Seconds())+1))
	writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("too many failed attempts to authenticate from this address; try again in %d seconds", int(left.Seconds())+1))
	return true
}

// tokenDenied counts a failed token and records it in the audit trail,
// unless the source is already refused (a flood is recorded once).
func (s *Server) tokenDenied(r *http.Request, touch bool, user, name, why string) {
	if !touch {
		return
	}
	addr, now := s.clientAddr(r), s.Now()
	l := s.limiter()
	wasBlocked := l.blocked(addr, now) > 0
	l.fail(addr, now)
	if wasBlocked {
		return
	}
	detail := why
	if l.blocked(addr, now) > 0 {
		detail += fmt.Sprintf("; %d failures within %s, so this address is refused for the rest of it", maxFailuresPerWindow, failureWindow)
	}
	s.record(&engine.AuditEntry{At: now.UnixMilli(), User: user, Source: addr, RequestID: reqID(r), Action: "token.use", Target: name, Outcome: "denied",
		Detail: r.Method + " " + r.URL.Path + ": " + detail})
}
