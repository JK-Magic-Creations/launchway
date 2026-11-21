package registry_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/registry"
	"github.com/jk-magic-creations/launchway/internal/registry/enginedb"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// who maps a secret to what its holder may do: project -> actions.
type who struct {
	subject string
	can     map[string]map[registry.Action]bool
}

type fakeAuth struct {
	mu      sync.Mutex
	secrets map[string]who
	denied  []registry.Event
	svc     *registry.Service
	signer  registry.Signer
	fails   int
}

func (a *fakeAuth) Authenticate(r *http.Request, user, secret string) (*registry.Principal, error) {
	if strings.HasPrefix(secret, registry.RunPrefix) {
		c, err := a.signer.VerifyRun(secret)
		if err != nil {
			a.mu.Lock()
			a.fails++
			a.mu.Unlock()
			return nil, nil
		}
		return a.svc.RunPrincipal(c), nil
	}
	a.mu.Lock()
	w, ok := a.secrets[secret]
	a.mu.Unlock()
	if !ok {
		a.mu.Lock()
		a.fails++
		a.mu.Unlock()
		return nil, nil
	}
	return &registry.Principal{Subject: w.subject, Via: "token", Can: func(_ context.Context, repo string, act registry.Action) bool {
		return w.can[registry.ProjectOf(repo)][act]
	}}, nil
}
func (a *fakeAuth) Blocked(http.ResponseWriter, *http.Request) bool { return false }
func (a *fakeAuth) Denied(_ *http.Request, _ *registry.Principal, e registry.Event) {
	a.mu.Lock()
	a.denied = append(a.denied, e)
	a.mu.Unlock()
}
func (a *fakeAuth) Source(*http.Request) string { return "192.0.2.1" }

func rw(projects ...string) map[string]map[registry.Action]bool {
	m := map[string]map[registry.Action]bool{}
	for _, p := range projects {
		m[p] = map[registry.Action]bool{registry.Pull: true, registry.Push: true, registry.Delete: true}
	}
	return m
}
func ro(projects ...string) map[string]map[registry.Action]bool {
	m := map[string]map[registry.Action]bool{}
	for _, p := range projects {
		m[p] = map[registry.Action]bool{registry.Pull: true}
	}
	return m
}

type env struct {
	t      *testing.T
	eng    *engine.Engine
	svc    *registry.Service
	srv    *httptest.Server
	auth   *fakeAuth
	signer registry.Signer
	clock  *clock
	events []registry.Event
	evMu   sync.Mutex
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type opt func(*registry.Options)

// newEnv starts a registry on a fresh database and a filesystem blob store.
func newEnv(t *testing.T, opts ...opt) *env { return newEnvAt(t, testdb.New(t), t.TempDir(), opts...) }

func newEnvAt(t *testing.T, addr, blobDir string, opts ...opt) *env {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: addr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	store, err := blobstore.NewFilesystem(filepath.Join(blobDir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, eng: eng, clock: &clock{t: time.Now()}}
	o := registry.Options{DB: enginedb.New(eng.Database()), Blobs: store, Now: e.clock.Now, Server: "s-" + filepath.Base(t.TempDir()),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnEvent: func(ev registry.Event) { e.evMu.Lock(); e.events = append(e.events, ev); e.evMu.Unlock() }}
	for _, f := range opts {
		f(&o)
	}
	e.svc = registry.New(o)
	e.signer = registry.Signer{Key: []byte("0123456789abcdef0123456789abcdef"), Now: e.clock.Now}
	e.auth = &fakeAuth{secrets: map[string]who{}, svc: e.svc, signer: e.signer}
	h := &registry.Handler{Svc: e.svc, Auth: e.auth, Signer: e.signer, Base: func(r *http.Request) string { return "http://" + r.Host }}
	mux := http.NewServeMux()
	mux.Handle("/v2/", h)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

// host is the registry address as clients write it.
func (e *env) host() string { return strings.TrimPrefix(e.srv.URL, "http://") }

func (e *env) grant(secret string, w who) {
	e.auth.mu.Lock()
	e.auth.secrets[secret] = w
	e.auth.mu.Unlock()
}

func (e *env) eventsOf(typ string) []registry.Event {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	var out []registry.Event
	for _, ev := range e.events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func dbAddr(t *testing.T) string { return testdb.New(t) }
