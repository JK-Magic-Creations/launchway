package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
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

// gitHTTP authenticates, authorises and serves one Git request.
func (s *Server) gitHTTP(w http.ResponseWriter, r *http.Request) {
	f := s.Forge
	if f == nil || f.Git == nil {
		http.Error(w, featureOff, http.StatusNotFound)
		return
	}
	plain := func(code int, msg string) { http.Error(w, msg, code) }
	unauthorised := func() {
		w.Header().Set("WWW-Authenticate", gitRealm)
		plain(http.StatusUnauthorized, "authentication required")
	}
	svc, advertise, err := githttp.Service(r)
	if err != nil {
		plain(http.StatusForbidden, err.Error())
		return
	}
	q := r.URL.Query()
	for _, k := range tokenInURL {
		if q.Has(k) {
			plain(http.StatusBadRequest, "a token must not be sent in the address (it would be logged); send it in the Authorization header, as git does with a credential")
			return
		}
	}
	class := repoRead
	if svc == githttp.ReceivePack {
		class = repoWrite
	}
	owner, name := r.PathValue("owner"), strings.TrimSuffix(r.PathValue("name"), ".git")
	path := repoSubject(owner, name)

	tok, basic, present := gitCredential(r)
	if present && basic && !s.isHTTPS(r) && !loopbackListener(r) && !f.AllowInsecureHTTP {
		plain(http.StatusForbidden, "Launchway does not accept a password or token over plain HTTP; use HTTPS (or set forge.allow_insecure_http for a private network)")
		return
	}
	if present && s.tooManyFailures(w, r) {
		return
	}
	// Who is calling, before anything else is looked at.
	var id *identity
	var run *githttp.RunClaim
	switch {
	case s.Auth == nil:
		id = &identity{User: s.Actor, Via: viaNone}
	case present && strings.HasPrefix(tok, githttp.RunTokenPrefix):
		c, err := f.RunTokens.Verify(tok)
		if err != nil {
			s.tokenDenied(r, true, "", "", "an invalid or expired run token")
			unauthorised()
			return
		}
		run = &c
	case present && strings.HasPrefix(tok, tokenPrefix):
		if id, err = s.tokenIdentity(r, tok, true); err != nil {
			s.fail(w, err)
			return
		}
		if id == nil {
			unauthorised()
			return
		}
	case present:
		s.tokenDenied(r, true, "", "", "the credential is not a Launchway API token")
		unauthorised()
		return
	}
	repo, err := f.Provider.Repositories().Get(r.Context(), path)
	found := err == nil
	if err != nil && !errors.Is(err, forge.ErrNotFound) {
		s.fail(w, err)
		return
	}
	user := s.gitUser(id, run, r)
	deny := func(code int, msg string) {
		s.gitDenied(r, id, run, path, svc, msg)
		plain(code, msg)
	}
	switch {
	case id == nil && run == nil:
		// Anonymous: only a repository whose access class is public, and only to read.
		if !found || repo.Visibility != forge.Public || class != repoRead {
			unauthorised() // the same answer for a private repository and one that does not exist
			return
		}
		user = "anonymous " + s.clientAddr(r)
	case run != nil:
		if !found || run.Repo != repo.ID {
			plain(http.StatusNotFound, "repository not found")
			return
		}
		if class != repoRead {
			deny(http.StatusForbidden, "a run's credential can read a repository, not push to it")
			return
		}
	default:
		if !found {
			plain(http.StatusNotFound, "repository not found")
			return
		}
		if code, msg := s.checkRepo(id, class, path); code != 0 {
			if code == http.StatusNotFound {
				plain(code, "repository not found")
				return
			}
			deny(code, msg)
			return
		}
	}
	if class == repoWrite && (repo.Archived || repo.Mirror) {
		deny(http.StatusForbidden, "this repository is "+map[bool]string{true: "archived", false: "a mirror"}[repo.Archived]+" and takes no pushes")
		return
	}
	res := f.Git.Serve(w, r, githttp.Request{Repo: repo, Service: svc, Advertise: advertise, User: user})
	if svc == githttp.ReceivePack && !advertise {
		s.gitPushed(r, id, run, repo, res)
	}
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
	if isGitRoute(rt.pattern) && !strings.Contains(rt.pattern, " /git/") {
		return
	}
	mux.Handle(rt.pattern, s.guard(rt))
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
	http.Redirect(w, r, "/repositories/"+owner+"/"+name, http.StatusFound)
	return true
}
