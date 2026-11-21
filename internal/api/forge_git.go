package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
)

// Git over smart HTTP. The same handler answers /git/<owner>/<name>.git/...
// and the address Gitea serves, /<owner>/<name>.git/..., so a remote moves
// from one host to the other by changing the host. The name may omit .git.
//
// These routes do not use the guard's session logic: a git client sends HTTP
// Basic (or a bearer token), never a cookie, and must be told to authenticate
// with a 401. Everything is decided before the repository storage is touched.

// gitGuard marks the routes served by gitGuard (see guard).
func isGitRoute(pattern string) bool {
	return strings.HasSuffix(pattern, "/info/refs") || strings.HasSuffix(pattern, "/git-upload-pack") || strings.HasSuffix(pattern, "/git-receive-pack")
}

func (s *Server) gitRoutes() []route {
	var out []route
	for _, prefix := range []string{"/git", ""} {
		out = append(out,
			route{"GET " + prefix + "/{owner}/{name}/info/refs", repoRead, "", s.gitHTTP},
			route{"POST " + prefix + "/{owner}/{name}/git-upload-pack", repoRead, "", s.gitHTTP},
			route{"POST " + prefix + "/{owner}/{name}/git-receive-pack", repoWrite, "git.push", s.gitHTTP},
		)
	}
	return out
}

// gitCredential is what a request carries to authenticate: a token from a
// Basic password (or user name, when the password is empty), a Bearer header
// or Gitea's "token" scheme.
func gitCredential(r *http.Request) (tok string, basic, present bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false, false
	}
	scheme, rest, _ := strings.Cut(h, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(scheme) {
	case "basic":
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return "", true, true
		}
		user, pass, _ := strings.Cut(string(raw), ":")
		if pass == "" {
			pass = user // https://<token>@host/... clients send the token as the user name
		}
		return pass, true, pass != ""
	case "bearer", "token":
		return rest, false, rest != ""
	}
	return "", false, true
}

// tokenInURL names the query parameters a token must never travel in.
var tokenInURL = []string{"token", "access_token", "private_token", "password", "api_key"}

func loopbackListener(r *http.Request) bool {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const gitRealm = `Basic realm="Launchway Git", charset="UTF-8"`

// gitReq is what a Git request asks for and carries.
type gitReq struct {
	svc       string
	advertise bool
	class     access
	path      string
	tok       string
	basic     bool
	present   bool
}

// gitCaller is who a Git request is from: a person (an API token), a run
// (a run token), or nobody (anonymous).
type gitCaller struct {
	id   *identity
	run  *githttp.RunClaim
	user string
}

// gitHTTP authenticates, authorises and serves one Git request, in that
// order and before the storage is touched.
func (s *Server) gitHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Forge == nil || s.Forge.Git == nil {
		http.Error(w, featureOff, http.StatusNotFound)
		return
	}
	q, ok := s.gitParse(w, r)
	if !ok {
		return
	}
	c, ok := s.gitAuthenticate(w, r, q)
	if !ok {
		return
	}
	repo, ok := s.gitAuthorise(w, r, q, c)
	if !ok {
		return
	}
	res := s.Forge.Git.Serve(w, r, githttp.Request{Repo: repo, Service: q.svc, Advertise: q.advertise, User: c.user})
	if q.svc == githttp.ReceivePack && !q.advertise {
		s.gitPushed(r, c.id, c.run, repo, res)
	}
}

func unauthorised(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", gitRealm)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// gitParse reads what is asked for, and refuses what is never accepted: the
// dumb protocol, a token in the address, Basic over plain HTTP.
func (s *Server) gitParse(w http.ResponseWriter, r *http.Request) (gitReq, bool) {
	svc, advertise, err := githttp.Service(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return gitReq{}, false
	}
	for _, k := range tokenInURL {
		if r.URL.Query().Has(k) {
			http.Error(w, "a token must not be sent in the address (it would be logged); send it in the Authorization header, as git does with a credential", http.StatusBadRequest)
			return gitReq{}, false
		}
	}
	q := gitReq{svc: svc, advertise: advertise, class: repoRead,
		path: repoSubject(r.PathValue("owner"), strings.TrimSuffix(r.PathValue("name"), ".git"))}
	if svc == githttp.ReceivePack {
		q.class = repoWrite
	}
	q.tok, q.basic, q.present = gitCredential(r)
	if q.present && q.basic && !s.isHTTPS(r) && !loopbackListener(r) && !s.Forge.AllowInsecureHTTP {
		http.Error(w, "Launchway does not accept a password or token over plain HTTP; use HTTPS (or set forge.allow_insecure_http for a private network)", http.StatusForbidden)
		return gitReq{}, false
	}
	return q, true
}

// gitAuthenticate works out who is calling. A credential that is present and
// wrong is a 401 (and counts against the sender); none at all is anonymous.
func (s *Server) gitAuthenticate(w http.ResponseWriter, r *http.Request, q gitReq) (gitCaller, bool) {
	var c gitCaller
	if q.present && s.tooManyFailures(w, r) {
		return c, false
	}
	switch {
	case s.Auth == nil:
		c.id = &identity{User: s.Actor, Via: viaNone}
	case q.present && strings.HasPrefix(q.tok, githttp.RunTokenPrefix):
		claim, err := s.Forge.RunTokens.Verify(q.tok)
		if err != nil {
			s.tokenDenied(r, true, "", "", "an invalid or expired run token")
			unauthorised(w)
			return c, false
		}
		c.run = &claim
	case q.present && strings.HasPrefix(q.tok, tokenPrefix):
		id, err := s.tokenIdentity(r, q.tok, true)
		if err != nil {
			s.fail(w, err)
			return c, false
		}
		if id == nil {
			unauthorised(w)
			return c, false
		}
		c.id = id
	case q.present:
		s.tokenDenied(r, true, "", "", "the credential is not a Launchway API token")
		unauthorised(w)
		return c, false
	}
	c.user = s.gitUser(c.id, c.run, r)
	return c, true
}

// gitAuthorise finds the repository and decides whether this caller may do
// this to it. Everything that cannot be told apart is answered alike.
func (s *Server) gitAuthorise(w http.ResponseWriter, r *http.Request, q gitReq, c gitCaller) (forge.Repository, bool) {
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), q.path)
	found := err == nil
	if err != nil && !errors.Is(err, forge.ErrNotFound) {
		s.fail(w, err)
		return repo, false
	}
	deny := func(code int, msg string) (forge.Repository, bool) {
		s.gitDenied(r, c.id, c.run, q.path, q.svc, msg)
		http.Error(w, msg, code)
		return repo, false
	}
	switch {
	case c.id == nil && c.run == nil:
		// Anonymous: only a repository whose access class is public, and only to read.
		if !found || repo.Visibility != forge.Public || q.class != repoRead {
			unauthorised(w) // the same answer for a private repository and one that does not exist
			return repo, false
		}
	case c.run != nil:
		if !found || c.run.Repo != repo.ID {
			http.Error(w, "repository not found", http.StatusNotFound)
			return repo, false
		}
		if q.class != repoRead {
			return deny(http.StatusForbidden, "a run's credential can read a repository, not push to it")
		}
	default:
		if !found {
			http.Error(w, "repository not found", http.StatusNotFound)
			return repo, false
		}
		if code, msg := s.checkRepo(c.id, q.class, q.path); code == http.StatusNotFound {
			http.Error(w, "repository not found", code)
			return repo, false
		} else if code != 0 {
			return deny(code, msg)
		}
	}
	if q.class == repoWrite && (repo.Archived || repo.Mirror) {
		return deny(http.StatusForbidden, "this repository is "+map[bool]string{true: "archived", false: "a mirror"}[repo.Archived]+" and takes no pushes")
	}
	return repo, true
}

func (s *Server) gitUser(id *identity, run *githttp.RunClaim, r *http.Request) string {
	switch {
	case id != nil:
		return id.User
	case run != nil:
		return "run " + run.Run
	}
	return "anonymous " + s.clientAddr(r)
}

// gitDenied records a refused push (and only pushes: reads that are refused
// are counted by the limiter, not recorded one by one).
func (s *Server) gitDenied(r *http.Request, id *identity, run *githttp.RunClaim, path, svc, why string) {
	if svc != githttp.ReceivePack {
		return
	}
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: s.gitUser(id, run, r), Source: s.clientAddr(r), Action: "git.push", Target: path,
		Outcome: "denied", Detail: why})
}

// gitPushed records a push: who, to which repository, which refs moved, and
// whether it committed.
func (s *Server) gitPushed(r *http.Request, id *identity, run *githttp.RunClaim, repo forge.Repository, res githttp.Result) {
	e := &engine.AuditEntry{At: s.Now().UnixMilli(), User: s.gitUser(id, run, r), Source: s.clientAddr(r), Action: "git.push", Target: repo.Path, Outcome: "ok"}
	if id != nil && id.Via == viaToken {
		e.Detail = "token " + id.tokenTag
	}
	var refs []string
	if rr := res.Receive; rr != nil {
		for _, u := range rr.Updates {
			refs = append(refs, fmt.Sprintf("%s %s..%s", u.Name, short(u.Old), short(u.New)))
		}
		if rr.Stale != nil {
			e.Outcome, e.Detail = "failed", e.Detail+"; lost a race: "+rr.Stale.Error()
		} else if !rr.Committed {
			e.Outcome = "failed"
		}
	}
	if res.Err != nil {
		e.Outcome, e.Detail = "failed", e.Detail+"; "+res.Err.Error()
	}
	if len(refs) > 0 {
		e.Detail = strings.TrimPrefix(e.Detail+"; "+strings.Join(refs, ", "), "; ")
	}
	s.record(e)
	// An accepted push is copied to the repository's push mirrors.
	if rr := res.Receive; rr != nil && rr.Committed && s.Forge != nil && s.Forge.Mirrors != nil {
		s.Forge.Mirrors.Pushed(r.Context(), repo.ID)
	}
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	if strings.Trim(sha, "0") == "" {
		return "none"
	}
	return sha
}

// register adds a route to the mux. The Gitea-compatible addresses
// (/<owner>/<name>/...) cannot be patterns of the mux: they overlap
// /hosted/{link}/{path...} and the mux refuses overlaps it cannot rank. They
// stay in the route table, with their access class, and are served by
// gitAlias instead.
func (s *Server) register(mux *http.ServeMux, rt route) {
	switch {
	case isGitRoute(rt.pattern) && !strings.Contains(rt.pattern, " /git/"):
		return
	case isGitRoute(rt.pattern):
		mux.Handle(rt.pattern, http.HandlerFunc(rt.handler)) // authenticates by itself: see gitHTTP
	default:
		mux.Handle(rt.pattern, s.guard(rt))
	}
}

// notOwners are first path segments that are Launchway's own, never an owner.
var notOwners = map[string]bool{"api": true, "auth": true, "git": true, "hosted": true, "mcp": true, "assets": true}

// gitAlias serves /<owner>/<name>(.git)/{info/refs, git-upload-pack,
// git-receive-pack}, exactly as /git/<owner>/<name>/... is served, and passes
// everything else on.
func (s *Server) gitAlias(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		ok := false
		switch {
		case len(segs) == 4 && segs[2] == "info" && segs[3] == "refs":
			ok = r.Method == http.MethodGet
		case len(segs) == 3 && (segs[2] == githttp.UploadPack || segs[2] == githttp.ReceivePack):
			ok = r.Method == http.MethodPost
		}
		if len(segs) == 2 && r.Method == http.MethodGet && s.Forge != nil && s.redirectToRepository(w, r, segs[0], segs[1]) {
			return
		}
		if !ok || segs[0] == "" || segs[1] == "" || notOwners[segs[0]] {
			next.ServeHTTP(w, r)
			return
		}
		r.SetPathValue("owner", segs[0])
		r.SetPathValue("name", segs[1])
		s.gitHTTP(w, r)
	})
}

// redirectToRepository sends a browser that opens /<owner>/<name>, the
// address of a repository on other Git hosts, to the repository's screen.
// Only a repository that exists is redirected: anything else is the web UI's.
func (s *Server) redirectToRepository(w http.ResponseWriter, r *http.Request, owner, name string) bool {
	if owner == "" || notOwners[owner] || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		return false
	}
	name = strings.TrimSuffix(name, ".git")
	// Only for someone who may read it: a stranger gets the web UI's own answer, so the
	// redirect does not say which repositories exist.
	id, err := s.identify(r, false)
	if err != nil || id == nil || !s.authorize(id, auth.Read, repoSubject(owner, name)) {
		return false
	}
	if _, err := s.Forge.Provider.Repositories().Get(r.Context(), repoSubject(owner, name)); err != nil {
		return false
	}
	http.Redirect(w, r, "/repositories/"+url.PathEscape(owner)+"/"+url.PathEscape(name), http.StatusFound)
	return true
}
