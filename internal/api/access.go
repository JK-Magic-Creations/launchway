package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/work"
)

// Who is calling, and what they may do.

// identity is the person a request acts for.
type identity struct {
	User    string
	Groups  []string
	Profile engine.Profile // display name and e-mail, when the provider gave them
	Via     string         // "none" (sign-in is off), "session" or "token"
	session string         // the session's hash, when Via is "session"
	// A token may be limited to a role and to projects (see limit).
	tokenID  string
	capRole  auth.Role
	scope    []string
	tokenTag string // the token's name, for the audit trail
	// agent is set when the token is an agent's (internal/work).
	agent *work.Agent
}

const (
	viaNone    = "none"
	viaSession = "session"
	viaToken   = "token"

	sessionCookie = "launchway_session"
	// csrfHeader must be sent with every request that changes something
	// and is authenticated by a cookie. Browsers do not let another site
	// add it without asking this server first (CORS), and this server never
	// agrees.
	csrfHeader = "X-Launchway-CSRF"
	// backgroundHeader marks requests the UI makes by itself (polling,
	// refreshing after a live update), which do not count as activity for
	// the idle timeout.
	backgroundHeader = "X-Launchway-Background"
	// touchEvery bounds how often a session's or token's last use is
	// written: at most once a minute each.
	touchEvery = time.Minute
)

type ctxKey int

const (
	identityKey ctxKey = iota
	auditKey
)

// identityFrom returns the identity guard attached to the request.
func identityFrom(r *http.Request) *identity {
	id, _ := r.Context().Value(identityKey).(*identity)
	if id == nil {
		return &identity{} // unreachable behind guard; grants nothing
	}
	return id
}

// authorize is the one place that decides whether someone may do something.
func (s *Server) authorize(id *identity, a auth.Action, project string) bool {
	if a == auth.RunDraft {
		// The drafts setting applies with sign-in off too: none turns
		// drafts off for everyone, who is then an admin.
		role := auth.Admin
		if s.Auth != nil {
			if id == nil || id.Via == viaNone || id.User == "" {
				return false
			}
			role = s.roleOn(id, project)
		}
		return s.cur().Drafts.For(project).AllowsDraft(role)
	}
	if s.Auth == nil {
		return true // sign-in is off: everyone has every right
	}
	if id == nil || id.Via == viaNone || id.User == "" {
		return false
	}
	if id.limited() {
		return s.limitedAllows(id, a, project)
	}
	return s.cur().Policy.Allows(id.User, id.Groups, a, project)
}

// draftRefusal explains why someone may not start a draft run of a project.
func (s *Server) draftRefusal(project string) string {
	switch rule := s.cur().Drafts.For(project); rule.Allow {
	case auth.NoRole:
		return fmt.Sprintf("draft runs are turned off for %s (drafts.allow: none); save the pipeline to a branch and run that", project)
	default:
		return fmt.Sprintf("you need the %s role on %s to start draft runs, which run pipeline text that is not in Git", rule.Allow, project)
	}
}

// identify works out who a request is from: nil if nobody valid. With
// touch, using a session or token is recorded (at most once a minute).
func (s *Server) identify(r *http.Request, touch bool) (*identity, error) {
	if s.Auth == nil {
		return &identity{User: s.Actor, Via: viaNone}, nil
	}
	now := s.Now()
	if h := r.Header.Get("Authorization"); h != "" {
		// A request that names a token is judged by the token alone.
		tok, ok := apiToken(h)
		if ok && strings.HasPrefix(tok, work.TokenPrefix) {
			return s.agentIdentity(r, tok, touch)
		}
		if !ok || !strings.HasPrefix(tok, tokenPrefix) {
			s.tokenDenied(r, touch, "", "", "the Authorization header holds no Launchway API token")
			return nil, nil
		}
		return s.tokenIdentity(r, tok, touch)
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	hash := auth.Hash(c.Value)
	sess, err := s.Engine.SessionByHash(hash)
	if errors.Is(err, engine.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ms := now.UnixMilli()
	if ms >= sess.ExpiresAt || ms-sess.LastSeen >= s.Auth.IdleTimeout.Milliseconds() {
		if _, err := s.Engine.EndSession(hash); err != nil {
			s.Log.Warn("ending an expired session", "err", err)
		}
		return nil, nil
	}
	if touch && r.Header.Get(backgroundHeader) == "" {
		if err := s.Engine.TouchSession(hash, ms, now.Add(-touchEvery).UnixMilli()); err != nil {
			s.Log.Warn("recording that a session was used", "err", err)
		}
	}
	return &identity{User: sess.User, Groups: sess.Groups, Profile: sess.Profile, Via: viaSession, session: hash}, nil
}

// requestIdentity is who a request acts for. A call that a tool of the
// MCP endpoint makes in process for someone signed in with the provider's
// access token carries their identity, which /mcp worked out; nothing from
// the network can put it there. Every other request is identified from
// its token or session.
func (s *Server) requestIdentity(r *http.Request) (*identity, error) {
	if id, ok := r.Context().Value(mcpIdentityKey).(*identity); ok && id != nil {
		return id, nil
	}
	return s.identify(r, true)
}

// guard wraps a route's handler with everything access needs: who is
// calling, the CSRF check, the permission check, and the audit record.
func (s *Server) guard(rt route) http.Handler {
	h := s.guardAccess(rt)
	if rt.access == public || rt.access == runnerOnly {
		return h
	}
	// A new installation has no administrator: nothing signed-in works until setup is done.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.refuseUntilSetup(w) {
			h.ServeHTTP(w, r)
		}
	})
}

func (s *Server) guardAccess(rt route) http.Handler {
	switch rt.access {
	case public:
		return rt.handler
	case runnerOnly:
		return s.runnerAuth(rt.handler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" && s.tooManyFailures(w, r) {
			return
		}
		id, err := s.requestIdentity(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		if id == nil {
			writeErr(w, http.StatusUnauthorized, "sign in first, or send a valid API token")
			return
		}
		var entry *engine.AuditEntry
		if rt.audit != "" {
			entry = &engine.AuditEntry{At: s.Now().UnixMilli(), User: id.User, Source: s.clientAddr(r), RequestID: reqID(r),
				Action: rt.audit, Target: auditTarget(r)}
		}
		deny := func(code int, msg string) {
			writeErr(w, code, msg)
			if entry != nil {
				entry.Outcome, entry.Detail = "denied", msg
				markMCP(r, id, entry)
				s.record(entry)
			} else if id.Via == viaToken || id.Via == viaOAuth {
				e := &engine.AuditEntry{At: s.Now().UnixMilli(), User: id.User, Source: s.clientAddr(r), RequestID: reqID(r), Action: "token.use",
					Project: r.PathValue("name"), Target: id.tokenTag, Outcome: "denied", Detail: r.Method + " " + r.URL.Path + ": " + msg}
				markMCP(r, id, e)
				s.record(e)
			}
		}
		if id.Via != viaToken && id.Via != viaOAuth && fromSandboxOrOtherSite(r) {
			// A page of another site, or a sandboxed HTML report, must
			// not act as whoever is viewing it (see reports.go).
			deny(http.StatusForbidden, "requests from other sites or from sandboxed reports cannot use a Launchway session")
			return
		}
		if !isSafeMethod(r.Method) && id.Via != viaToken && id.Via != viaOAuth {
			if msg := checkCSRF(r); msg != "" {
				deny(http.StatusForbidden, msg)
				return
			}
		}
		project := ""
		switch rt.access {
		case projectRead, projectOperate, projectDraft:
			project = r.PathValue("name")
		case runRead, runOperate:
			if project, err = s.Engine.RunProject(r.PathValue("id")); err != nil {
				if errors.Is(err, engine.ErrNotFound) {
					deny(http.StatusNotFound, err.Error())
				} else {
					s.fail(w, err)
				}
				return
			}
		}
		if entry != nil {
			entry.Project = project
		}
		if code, msg := s.check(id, rt.access, project, r); code != 0 {
			deny(code, msg)
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, id)
		if entry == nil {
			rt.handler(w, r.WithContext(ctx))
			return
		}
		rec := &recorder{ResponseWriter: w}
		rt.handler(rec, r.WithContext(context.WithValue(ctx, auditKey, entry)))
		entry.Outcome = "ok"
		if rec.status >= 400 {
			entry.Outcome = "failed"
			if rec.status == http.StatusForbidden {
				entry.Outcome = "denied" // such as approving a run you started
			}
			var body struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(rec.body, &body) == nil && body.Error != "" {
				entry.Detail = body.Error
			}
		}
		markMCP(r, id, entry)
		s.record(entry)
	})
}

// check applies a route's access rule. Something the caller may not see is
// reported exactly as if it did not exist.
func (s *Server) check(id *identity, a access, project string, r *http.Request) (int, string) {
	notFound := func() (int, string) {
		if a == runRead || a == runOperate {
			return http.StatusNotFound, fmt.Errorf("%w: run %s", engine.ErrNotFound, r.PathValue("id")).Error()
		}
		return http.StatusNotFound, fmt.Errorf("%w: project %s", engine.ErrNotFound, project).Error()
	}
	if a == repoRead || a == repoWrite || a == repoAdmin {
		return s.checkRepoRequest(id, a, r)
	}
	switch a {
	case signedIn:
		return 0, ""
	case readAny:
		if !s.authorize(id, auth.ReadAny, "") {
			return http.StatusForbidden, "you have no access to any project"
		}
	case administer:
		if !s.authorize(id, auth.Administer, "") {
			return http.StatusForbidden, "only admins of every project may do this"
		}
	case projectRead, runRead:
		if !s.authorize(id, auth.Read, project) {
			return notFound()
		}
	case projectOperate, runOperate:
		if !s.authorize(id, auth.Read, project) {
			return notFound()
		}
		if !s.authorize(id, auth.Operate, project) {
			return http.StatusForbidden, fmt.Sprintf("you need the operator role on %s to do this", project)
		}
	case projectDraft:
		if !s.authorize(id, auth.Read, project) {
			return notFound()
		}
		if !s.authorize(id, auth.RunDraft, project) {
			return http.StatusForbidden, s.draftRefusal(project)
		}
	default:
		return http.StatusForbidden, "not allowed"
	}
	return 0, ""
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// checkCSRF returns why a cookie-authenticated request that changes
// something must be refused, or "" if it may go ahead: it must carry the
// CSRF header, and an Origin, if it has one, must be this server.
func checkCSRF(r *http.Request) string {
	if r.Header.Get(csrfHeader) != "1" {
		return "this request did not come from Launchway's own pages (the " + csrfHeader + " header is missing)"
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return "this request came from another site (Origin " + o + ")"
		}
	}
	return ""
}

// isHTTPS reports whether the browser reached this server over HTTPS.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.TrustProxy {
		return strings.EqualFold(lastListItem(r.Header.Values("X-Forwarded-Proto")), "https")
	}
	return false
}

// clientAddr is the address of whoever made the request. Behind a trusted
// proxy it is the last address in X-Forwarded-For: the one the proxy itself
// saw. Addresses before it were sent by the client and could be anything.
func (s *Server) clientAddr(r *http.Request) string {
	if s.TrustProxy {
		if ip := net.ParseIP(lastListItem(r.Header.Values("X-Forwarded-For"))); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// lastListItem returns the last item of a comma-separated header that may
// appear several times.
func lastListItem(values []string) string {
	if len(values) == 0 {
		return ""
	}
	items := strings.Split(values[len(values)-1], ",")
	return strings.TrimSpace(items[len(items)-1])
}

// cookie returns a cookie with the attributes every Launchway cookie has.
func (s *Server) cookie(r *http.Request, name, value, path string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{Name: name, Value: value, Path: path, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.isHTTPS(r) || (s.Auth != nil && s.Auth.SecureCookies)}
	if maxAge > 0 {
		c.MaxAge = int(maxAge / time.Second)
	} else {
		c.MaxAge = -1 // delete it
	}
	return c
}

// securityHeaders stops other sites framing the UI (so nobody can be
// tricked into clicking Approve), stops browsers guessing content types,
// and keeps addresses out of Referer headers sent elsewhere.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", uiContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// visibleProjects returns the projects whose runs the caller may see, or
// nil if they may see every project's.
func (s *Server) visibleProjects(id *identity) ([]string, error) {
	if s.Auth == nil || (!id.limited() && s.cur().Policy.RoleEverywhere(id.User, id.Groups) >= auth.Viewer) {
		return nil, nil
	}
	known, err := s.knownProjects()
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, p := range known {
		if s.authorize(id, auth.Read, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// knownProjects lists the configured projects and every project that has
// had a run.
func (s *Server) knownProjects() ([]string, error) {
	names, err := s.Engine.ProjectsWithRuns()
	if err != nil {
		return nil, err
	}
	for _, p := range s.projects() {
		names = append(names, p.Name)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// me says who the caller is and what they may do, so the UI can hide what
// they cannot. The server still checks every request.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	known, err := s.knownProjects()
	if err != nil {
		s.fail(w, err)
		return
	}
	roles := map[string]string{}
	for _, p := range known {
		role := auth.Admin
		if s.Auth != nil {
			role = s.roleOn(id, p)
		}
		if role > auth.NoRole {
			roles[p] = role.String()
		}
	}
	mode := "none"
	if s.Auth != nil {
		mode = "oidc"
		if s.Auth.OIDC == nil {
			mode = "local"
		}
	}
	groups := id.Groups
	if groups == nil {
		groups = []string{}
	}
	// Whether agents can connect, so the UI can show how.
	agents := map[string]any{"enabled": s.mcpOpts() != nil, "path": mcpPath}
	if m := s.mcpOpts(); m != nil && m.OAuth != nil {
		agents["oauth_resource"] = m.OAuth.Resource
	}
	view := map[string]any{
		"user": id.User, "groups": groups, "auth_mode": mode, "via": id.Via,
		"admin":   s.authorize(id, auth.Administer, ""),
		"runners": s.authorize(id, auth.ReadAny, ""),
		"roles":   roles,
		"mcp":     agents,
		// Whether this server hosts Git repositories (forge.hosting), so the UI offers
		// what hosting makes work (importing repositories, a repository's mirrors) only then.
		"hosting": s.Forge != nil,
	}
	if s.Auth == nil {
		s.addSignInOff(view)
	}
	writeJSON(w, http.StatusOK, view)
}

// ---- the audit trail ----

// recorder notes the status of a response, and the body of an error, for
// the audit trail.
type recorder struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= 400 && len(r.body) < 2048 {
		r.body = append(r.body, b[:min(len(b), 2048-len(r.body))]...)
	}
	return r.ResponseWriter.Write(b)
}

// setAudit lets a handler say what its call acted on. Empty values are
// left as they were.
func setAudit(r *http.Request, target, detail string) {
	if e, ok := r.Context().Value(auditKey).(*engine.AuditEntry); ok {
		if target != "" {
			e.Target = target
		}
		if detail != "" {
			e.Detail = detail
		}
	}
}

// maxDetail and maxTarget bound what an audit entry keeps. The detail is
// long enough for a run's libraries with the full commits they resolved to.
const (
	maxDetail = 1000
	maxTarget = 300
)

// record appends an entry to the audit trail. A failure is logged: the
// action it describes has already happened.
func (s *Server) record(e *engine.AuditEntry) {
	e.Detail = clip(e.Detail, maxDetail)
	e.Target = clip(e.Target, maxTarget)
	if err := s.Engine.Audit(*e); err != nil {
		s.Log.Error("writing the audit trail", "action", e.Action, "user", e.User, "err", err)
	}
}

// clip shortens s to at most n bytes of valid text, without control
// characters.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "?"))
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func shortSHA(sha string) string { return sha[:min(len(sha), 8)] }

// tokenIdentity is who an API token stands for: nil if it is unknown,
// revoked or expired (counted as a failed attempt), with touch recording
// that it was used.
func (s *Server) tokenIdentity(r *http.Request, tok string, touch bool) (*identity, error) {
	if strings.HasPrefix(tok, work.TokenPrefix) {
		return s.agentIdentity(r, tok, touch)
	}
	now := s.Now()
	t, groups, err := s.Engine.LookupToken(auth.Hash(tok))
	if errors.Is(err, engine.ErrNotFound) {
		s.tokenDenied(r, touch, "", "", "an unknown or revoked token")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if now.UnixMilli() >= t.ExpiresAt {
		s.tokenDenied(r, touch, t.User, t.Name, "the token "+t.Name+" has expired")
		return nil, nil
	}
	if touch {
		if err := s.Engine.TouchTokenFrom(t.ID, now.UnixMilli(), now.Add(-touchEvery).UnixMilli(), s.clientAddr(r)); err != nil {
			s.Log.Warn("recording that a token was used", "err", err)
		}
	}
	id := &identity{User: t.User, Groups: groups, Profile: t.Owner, Via: viaToken, tokenID: t.ID, scope: t.Projects, tokenTag: t.Name}
	if t.Role != "" {
		if id.capRole, err = auth.ParseRole(t.Role); err != nil {
			return nil, nil // a role this server does not know: refuse rather than guess
		}
	}
	return id, nil
}

// apiToken reads a token from an Authorization header: "Bearer <token>", the
// "token <token>" Gitea's clients send, or Basic where the password (or, with
// no password, the user name) is the token.
func apiToken(h string) (string, bool) {
	scheme, rest, _ := strings.Cut(h, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(scheme) {
	case "bearer", "token":
		return rest, rest != ""
	case "basic":
		req := &http.Request{Header: http.Header{"Authorization": {h}}}
		user, pass, ok := req.BasicAuth()
		if !ok {
			return "", false
		}
		if pass == "" {
			pass = user
		}
		return pass, pass != ""
	}
	return "", false
}

// auditTarget is what a guarded call acted on: the run, or the person an
// administrator managed.
func auditTarget(r *http.Request) string {
	if t := r.PathValue("id"); t != "" || !strings.HasPrefix(r.URL.Path, "/api/v1/users/") {
		return t
	}
	return r.PathValue("name")
}

// agentIdentity is who an agent's token stands for: the agent, limited to
// Operator on the projects it is registered for. A switched-off agent is
// refused (counted as a failed attempt), so its token stops working at once.
func (s *Server) agentIdentity(r *http.Request, tok string, touch bool) (*identity, error) {
	a, err := s.work().AgentForToken(r.Context(), tok)
	if err != nil {
		return nil, err
	}
	if a == nil {
		s.tokenDenied(r, touch, "", "", "an unknown or rotated agent token")
		return nil, nil
	}
	if a.Disabled {
		s.tokenDenied(r, touch, "agent:"+a.Name, a.Name, "the agent "+a.Name+" is switched off")
		return nil, nil
	}
	return &identity{User: "agent:" + a.Name, Via: viaToken, tokenTag: "agent " + a.Name, capRole: auth.Operator, scope: a.Projects, agent: a}, nil
}
