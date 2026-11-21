package api

// The built-in registry (internal/registry) as the server serves it: the
// /v2/ endpoints beside the API, and who may do what there. Everything is
// decided by Launchway's own roles: a person's API token is judged by the
// role it carries on the project (the first path component of a repository),
// and a run's credential by the project of the run.
//
//	pull    viewer on the project
//	push    operator on the project
//	delete  admin on the project (and never a run)
//
// A repository someone may not pull is answered exactly like one that does not
// exist. There is no anonymous access, and none at all while sign-in is off.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/registry"
)

// RegistryHosting is the built-in registry, set up. Nil: the registry is off.
type RegistryHosting struct {
	Svc *registry.Service
	// Signer makes and checks run credentials and bearer tokens.
	Signer registry.Signer
	// PublicURL is the address clients use (no path); empty: the address a
	// request came in on.
	PublicURL string
	// AllowInsecureHTTP accepts a password or token over plain HTTP from anywhere.
	AllowInsecureHTTP bool
	// StoreName is the blob store kind ("database", "filesystem", "s3"), for status.
	StoreName string

	once    sync.Once
	handler *registry.Handler
}

const registryOff = "the built-in registry is off on this server (registry.enabled); turn it on to push and pull images and charts"

// registryMiddleware answers /v2/ requests with the registry and passes every
// other request on.
func (s *Server) registryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !registry.IsRegistryPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case s.Registry == nil || s.Registry.Svc == nil:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": registryOff, "code": "feature_off"})
		case s.Auth == nil:
			// Private by default: with sign-in off nobody could be told from anybody.
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "the built-in registry needs sign-in, and this server has none: it would be open to everyone who can reach it",
				"code":  "needs_sign_in", "next": "Turn on sign-in (auth.mode), then restart; see the sign-in guide."})
		default:
			s.Registry.serve(s, w, r)
		}
	})
}

func (h *RegistryHosting) serve(s *Server, w http.ResponseWriter, r *http.Request) {
	h.once.Do(func() {
		h.handler = &registry.Handler{Svc: h.Svc, Auth: registryAuthority{s}, Signer: h.Signer,
			Base:         func(r *http.Request) string { return s.registryBase(r) },
			SecureEnough: func(r *http.Request) bool { return s.isHTTPS(r) || loopbackListener(r) || h.AllowInsecureHTTP }}
	})
	h.handler.ServeHTTP(w, r)
}

// registryBase is the address (scheme and host) clients reach the registry at.
func (s *Server) registryBase(r *http.Request) string {
	if s.Registry != nil && s.Registry.PublicURL != "" {
		return strings.TrimSuffix(s.Registry.PublicURL, "/")
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if s.TrustProxy {
		if h := lastListItem(r.Header.Values("X-Forwarded-Host")); h != "" {
			host = h
		}
	}
	return scheme + "://" + host
}

// RegistryHost is what clients write before a repository name: the host (and
// port) of the registry's address, and whether it is plain HTTP.
func (s *Server) registryHost(r *http.Request) (host string, insecure bool) {
	b := s.registryBase(r)
	insecure = strings.HasPrefix(b, "http://")
	return strings.TrimPrefix(strings.TrimPrefix(b, "http://"), "https://"), insecure
}

// registryAuthority is Launchway's answer to who a registry caller is.
type registryAuthority struct{ s *Server }

func (a registryAuthority) Source(r *http.Request) string { return a.s.clientAddr(r) }

func (a registryAuthority) Blocked(w http.ResponseWriter, r *http.Request) bool {
	return a.s.tooManyFailures(w, r)
}

func (a registryAuthority) Authenticate(r *http.Request, user, secret string) (*registry.Principal, error) {
	s := a.s
	switch {
	case strings.HasPrefix(secret, registry.RunPrefix):
		c, err := s.Registry.Signer.VerifyRun(secret)
		if err != nil {
			s.tokenDenied(r, true, "", "", "an invalid or expired run credential")
			return nil, nil
		}
		if s.runEnded(c.Run) {
			s.tokenDenied(r, true, "", "", "a run credential of a run that has ended")
			return nil, nil
		}
		return s.Registry.Svc.RunPrincipal(c), nil
	case strings.HasPrefix(secret, tokenPrefix):
		id, err := s.tokenIdentity(r, secret, true)
		if err != nil || id == nil {
			return nil, err
		}
		return &registry.Principal{Subject: id.User, Via: "token", Can: func(_ context.Context, repo string, act registry.Action) bool {
			project := registry.ProjectOf(repo)
			switch act {
			case registry.Pull:
				return s.authorize(id, auth.Read, project)
			case registry.Push:
				return s.authorize(id, auth.Operate, project)
			case registry.Delete:
				return s.authorize(id, auth.AdministerProject, project)
			}
			return false
		}}, nil
	}
	s.tokenDenied(r, true, "", "", "the credential is not a Launchway API token or run credential")
	return nil, nil
}

// runEnded reports whether a run is known to have finished: its credential
// stops working with it, even before it expires.
func (s *Server) runEnded(run string) bool {
	if run == "" {
		return false
	}
	var status string
	if err := s.Engine.Database().QueryRow(context.Background(), `SELECT status FROM runs WHERE id = ?`, run).Scan(&status); err != nil {
		return false // an unknown run's credential is judged by its signature and expiry alone
	}
	switch status {
	case "succeeded", "failed", "canceled":
		return true
	}
	return false
}

func (a registryAuthority) Denied(r *http.Request, p *registry.Principal, e registry.Event) {
	a.s.recordRegistry(a.s.clientAddr(r), e)
}

// RegistryEvent records what happened to the registry's content in the audit
// trail. cmd/launchway hands it to the registry service.
func (s *Server) RegistryEvent(e registry.Event) { s.recordRegistry(e.Source, e) }

// registryAuditEntry names the audit action of each kind of registry event.
// The literals are written as AuditEntry values so that the documentation
// test finds them (gofmt -s would shorten a map of them, so this is a switch).
func registryAuditEntry(kind string) engine.AuditEntry {
	switch kind {
	case "push":
		return engine.AuditEntry{Action: "registry.push"}
	case "delete":
		return engine.AuditEntry{Action: "registry.delete"}
	case "untag":
		return engine.AuditEntry{Action: "registry.untag"}
	case "retention":
		return engine.AuditEntry{Action: "registry.retention"}
	case "gc":
		return engine.AuditEntry{Action: "registry.gc"}
	case "quota":
		return engine.AuditEntry{Action: "registry.quota"}
	case "pull":
		return engine.AuditEntry{Action: "registry.pull"}
	}
	return engine.AuditEntry{Action: "registry." + kind}
}

func (s *Server) recordRegistry(source string, e registry.Event) {
	outcome := e.Outcome
	if outcome == "" {
		outcome = "ok"
	}
	target := e.Repo
	switch {
	case e.Digest != "":
		target += "@" + e.Digest
	case e.Tag != "":
		target += ":" + e.Tag
	}
	detail := e.Detail
	if e.Run != "" {
		detail = strings.TrimPrefix("run "+e.Run+"; "+detail, "; ")
	}
	user := e.Actor
	if user == "" {
		user = "system"
	}
	action := registryAuditEntry(e.Type).Action
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: user, Source: source, Action: action,
		Project: registryProject(e.Repo), Target: target, Outcome: outcome, Detail: detail})
}

func registryProject(repo string) string {
	if repo == "" {
		return ""
	}
	return registry.ProjectOf(repo)
}

// mintRegistryCredential makes the credential a run's image step pushes and
// pulls with: for its project only, never for a pull-request or draft run's
// pushes, and ending with the step's own time limit (and a margin).
func (s *Server) mintRegistryCredential(project, run, event string, ttl time.Duration) (string, error) {
	push := event != "pull_request" && event != "draft"
	if ttl < 5*time.Minute {
		ttl = 5 * time.Minute
	}
	return s.Registry.Signer.MintRun(project, run, push, min(ttl+10*time.Minute, registry.MaxRunCredentialTTL))
}

// writeLeased answers a lease with the job, with what an image step needs of
// the built-in registry added.
func (s *Server) writeLeased(w http.ResponseWriter, r *http.Request, job *engine.Job) {
	s.registryJobFor(r, job)
	writeJSON(w, http.StatusOK, job)
}

// registryJobFor adds to an image job what it needs to use the built-in
// registry: its address and a credential made for this run, and the
// repositories of the built-in registry named with their project prefix.
func (s *Server) registryJobFor(r *http.Request, job *engine.Job) {
	if s.Registry == nil || s.Registry.Svc == nil || s.Auth == nil || job == nil || job.ImageBuild == nil {
		return
	}
	b := job.ImageBuild
	uses := b.PullCredentials
	for _, t := range b.Tags {
		uses = uses || t.Registry == engine.BuiltinRegistryName
	}
	if b.Cache != nil && b.Cache.Registry == engine.BuiltinRegistryName {
		uses = true
	}
	if !uses {
		return
	}
	tok, err := s.mintRegistryCredential(job.Project, job.RunID, b.Event, job.Timeout.D())
	if err != nil {
		s.Log.Error("making a registry credential for an image step", "err", err)
		return
	}
	host, insecure := s.registryHost(r)
	b.Registry = &engine.ImageJobRegistry{Host: host, Insecure: insecure, Username: "launchway-run", Token: tok, Push: b.Event != "pull_request" && b.Event != "draft"}
	for i := range b.Tags {
		if b.Tags[i].Registry == engine.BuiltinRegistryName {
			b.Tags[i].Repository = registryRepoName(job.Project, b.Tags[i].Repository)
		}
	}
	if b.Cache != nil && b.Cache.Registry == engine.BuiltinRegistryName {
		b.Cache.Repository = registryRepoName(job.Project, b.Cache.Repository)
	}
}

// registryRepoName puts an image's name under its project, which is what
// makes a repository the project's: "api" in project cart is cart/api, and
// "cart/api" is itself.
func registryRepoName(project, repo string) string {
	if repo == project || strings.HasPrefix(repo, project+"/") {
		return repo
	}
	return project + "/" + repo
}
