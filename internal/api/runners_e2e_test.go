package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Generous limits: the machine running the tests may be busy.
const (
	idWait = 60 * time.Second
	idTTL  = 10 * time.Second
)

var idPools = []engine.RunnerPool{
	{Name: "web", Kind: engine.PoolBuild, Projects: []string{"web-*"}},
}

// idEnv is two servers sharing one database, as behind a load balancer.
type idEnv struct {
	eng  *engine.Engine
	a, b *httptest.Server
	dir  string
	db   string
}

func setupIdentities(t *testing.T) *idEnv {
	t.Helper()
	db := testdb.New(t)
	e := &idEnv{dir: t.TempDir(), db: db}
	open := func() (*engine.Engine, *httptest.Server) {
		eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: idTTL, Logger: quiet(), RunnerPools: idPools})
		if err != nil {
			t.Fatal(err)
		}
		s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet()}
		srv := httptest.NewServer(s.Handler())
		t.Cleanup(func() { srv.Close(); eng.Close() })
		return eng, srv
	}
	e.eng, e.a = open()
	_, e.b = open()
	return e
}

// joinToken makes a join token through the API, as a person would.
func (e *idEnv) joinToken(t *testing.T, srv *httptest.Server, body string) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	if code := postJSONCode(t, srv.URL+"/api/v1/runner-join-tokens", body, &out); code != http.StatusCreated {
		t.Fatalf("making a join token: %d", code)
	}
	return out.Token
}

func postJSONCode(t *testing.T, url, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// enrol enrols a runner through server srv and returns a client for it
// that talks to server via (which may be the other one).
func (e *idEnv) enrol(t *testing.T, join, name string, srv, via *httptest.Server) (*runner.Client, *runner.Identity) {
	t.Helper()
	data := filepath.Join(e.dir, name)
	id, err := runner.Join(context.Background(), http.DefaultClient, srv.URL, join, data, name,
		runner.JoinFacts{Name: name, OS: "linux", Arch: "amd64", Version: "test", Executor: "host"})
	if err != nil {
		t.Fatal(err)
	}
	id.Server = via.URL
	c := runner.NewClient(via.URL, "")
	c.Auth = &runner.IdentityAuth{ID: id, HTTP: http.DefaultClient, Log: quiet()}
	return c, id
}

func (e *idEnv) startRun(t *testing.T, project, src string) string {
	t.Helper()
	p, err := pipeline.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(e.dir, "work")
	os.MkdirAll(work, 0o755)
	r, err := e.eng.CreateRun(engine.NewRun{Project: project, Pipeline: p, Trigger: engine.Trigger{Event: "manual"},
		Source: engine.Source{Kind: "dir", Path: work}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func (e *idEnv) runWith(t *testing.T, c *runner.Client, name string, mod func(*runner.Runner)) {
	t.Helper()
	r := &runner.Runner{Client: c, Exec: &executor.Host{}, Info: engine.RunnerInfo{ID: "whatever-" + name, Name: name},
		DataDir: filepath.Join(e.dir, name, "data"), Concurrency: 2, Log: quiet()}
	if mod != nil {
		mod(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (e *idEnv) wait(t *testing.T, id string, timeout time.Duration, until func(*engine.RunDetail) bool) *engine.RunDetail {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		d, err := e.eng.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if until(d) {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited %s: %s %+v", timeout, d.Status, d.StepList)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func terminalRun(d *engine.RunDetail) bool { return d.Status.Terminal() }

func (e *idEnv) audit(t *testing.T, action string) []engine.AuditEntry {
	t.Helper()
	list, err := e.eng.AuditLog(engine.AuditFilter{Action: action})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A runner enrols through one server and works through the other; the
// join token cannot be used again by anyone; the server lists it with its
// identity, and its join is in the audit trail.
func TestEnrolledRunnerWorksAcrossServers(t *testing.T) {
	e := setupIdentities(t)
	join := e.joinToken(t, e.a, `{"pool":"web","labels":["gpu"]}`)
	c, id := e.enrol(t, join, "r1", e.a, e.b)
	if !strings.HasPrefix(id.RunnerID, "rnr_") || id.Pool != "web" {
		t.Fatalf("%+v", id)
	}
	// The key file is the runner's only.
	if fi, err := os.Stat(filepath.Join(e.dir, "r1", "identity", "key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi.Mode(), err)
	}
	// The stolen join token, on either server.
	for _, srv := range []*httptest.Server{e.a, e.b} {
		_, err := runner.Join(context.Background(), http.DefaultClient, srv.URL, join, filepath.Join(e.dir, "thief"), "thief", runner.JoinFacts{})
		if err == nil || !strings.Contains(err.Error(), "not valid") {
			t.Fatalf("stolen join token: %v", err)
		}
	}
	e.runWith(t, c, "r1", nil)
	run := e.startRun(t, "web-shop", "pipeline: p\nsteps:\n  a: {image: none, run: echo hi}\n")
	if d := e.wait(t, run, idWait, terminalRun); d.Status != engine.RunSucceeded {
		t.Fatalf("%s %+v", d.Status, d.StepList)
	}
	list, _ := e.eng.Runners()
	var me *engine.RunnerView
	for i := range list {
		if list[i].ID == id.RunnerID {
			me = &list[i]
		}
	}
	if me == nil || me.Auth != engine.AuthIdentity || me.Identity == nil || me.Pool != "web" || !strings.Contains(strings.Join(me.Labels, ","), "gpu") {
		t.Fatalf("listed as %+v", me)
	}
	if len(e.audit(t, "runner.join")) != 1 || len(e.audit(t, "runner.join_token.create")) != 1 {
		t.Fatalf("audit: join %v create %v", e.audit(t, "runner.join"), e.audit(t, "runner.join_token.create"))
	}
}

// A second runner presenting another runner's lease is refused on every
// lease-scoped call, whether it is enrolled or uses the shared token; a
// runner that says it is another is still itself.
func TestSecondRunnerCannotUseAnothersLease(t *testing.T) {
	e := setupIdentities(t)
	ca, ida := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "a", e.a, e.a)
	cb, idb := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "b", e.a, e.b)
	e.startRun(t, "web-x", "pipeline: p\nsteps:\n  a: {image: none, run: echo hi}\n")
	ctx := context.Background()
	// a asks for work saying it is b.
	job, err := ca.Lease(ctx, engine.RunnerInfo{ID: idb.RunnerID, Name: "liar", Features: engine.FeatureArtifacts}, 0)
	if err != nil || job == nil {
		t.Fatalf("lease: %v %v", job, err)
	}
	holder, _, _, enrolled, _ := e.eng.LeaseHolder(job.LeaseID)
	if holder != ida.RunnerID || !enrolled {
		t.Fatalf("the lease went to %s, not to the runner that took it (%s)", holder, ida.RunnerID)
	}
	shared := runner.NewClient(e.a.URL, "secret")
	for name, c := range map[string]*runner.Client{"enrolled b": cb, "shared token": shared} {
		if _, err := c.Start(ctx, job.LeaseID); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s start: %v", name, err)
		}
		if _, err := c.Heartbeat(ctx, job.LeaseID); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s heartbeat: %v", name, err)
		}
		if err := c.Logs(ctx, job.LeaseID, []engine.LogLine{{Text: "forged"}}); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s logs: %v", name, err)
		}
		if err := c.Finish(ctx, job.LeaseID, engine.Result{Outcome: "succeeded"}); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s finish: %v", name, err)
		}
		if _, err := c.BeginArtifact(ctx, job.LeaseID, engine.BeginArtifact{Name: "x"}); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s artifact: %v", name, err)
		}
	}
	if n := len(e.audit(t, "runner.lease_refused")); n < 10 {
		t.Fatalf("refusals audited: %d", n)
	}
	// The owner may.
	if _, err := ca.Start(ctx, job.LeaseID); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := ca.Finish(ctx, job.LeaseID, engine.Result{Outcome: "succeeded", ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	// b says it leaves as a: only b leaves. The shared token cannot make an
	// enrolled runner leave.
	if err := cb.Leave(ctx, ida.RunnerID); err != nil {
		t.Fatal(err)
	}
	if err := shared.Leave(ctx, ida.RunnerID); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("shared leave: %v", err)
	}
	list, _ := e.eng.Runners()
	for _, r := range list {
		if r.ID == ida.RunnerID && r.LastSeen == 0 {
			t.Fatal("a was made to leave")
		}
	}
}

// A runner of the web pool never takes a step of a project its pool does
// not serve, over HTTP, however long it asks; the run says why it waits.
func TestRunnerOutsideItsPoolGetsNothing(t *testing.T) {
	e := setupIdentities(t)
	c, _ := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "w", e.a, e.a)
	run := e.startRun(t, "shop", "pipeline: p\nsteps:\n  a: {image: none, run: echo hi}\n")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if job, err := c.Lease(ctx, engine.RunnerInfo{Name: "w", Features: engine.FeatureArtifacts}, time.Second); err != nil || job != nil {
			t.Fatalf("web runner got %+v (%v)", job, err)
		}
	}
	d, _ := e.eng.GetRun(run)
	if !strings.Contains(d.PoolHint, "does not serve project shop") {
		t.Fatalf("hint: %q", d.PoolHint)
	}
	// The shared token's runners (the default pool) take it.
	shared := runner.NewClient(e.a.URL, "secret")
	if job, err := shared.Lease(ctx, engine.RunnerInfo{ID: "old", Name: "old", Features: engine.FeatureArtifacts}, 0); err != nil || job == nil || job.RunID != run {
		t.Fatalf("shared: %+v %v", job, err)
	}
}

// Revoking a runner in the middle of a step: its requests are refused at
// once, the step runs again on another runner, and the revocation is in
// the audit trail.
func TestRevokingARunnerMidStep(t *testing.T) {
	e := setupIdentities(t)
	c, id := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "victim", e.a, e.a)
	e.runWith(t, c, "victim", nil)
	marker := filepath.Join(e.dir, "attempts")
	run := e.startRun(t, "web-x", fmt.Sprintf(`pipeline: p
steps:
  a:
    image: none
    run: |
      echo "$LAUNCHWAY_ATTEMPT" >> %s
      if [ "$LAUNCHWAY_ATTEMPT" = 1 ]; then sleep 60; fi
`, marker))
	e.wait(t, run, idWait, func(d *engine.RunDetail) bool { return d.StepList[0].Status == engine.StepRunning })
	var out struct {
		Released int `json:"released"`
	}
	if code := postJSONCode(t, e.b.URL+"/api/v1/runners/"+id.RunnerID+"/revoke", `{"reason":"stolen laptop"}`, &out); code != 200 || out.Released != 1 {
		t.Fatalf("revoke: %d %+v", code, out)
	}
	// Its next request is refused, on both servers.
	for _, srv := range []*httptest.Server{e.a, e.b} {
		c2 := runner.NewClient(srv.URL, "")
		c2.Auth = &runner.IdentityAuth{ID: &runner.Identity{Server: srv.URL, RunnerID: id.RunnerID, Key: id.Key}, HTTP: http.DefaultClient, Log: quiet()}
		if _, err := c2.Lease(context.Background(), engine.RunnerInfo{}, 0); err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("revoked runner: %v", err)
		}
	}
	// Another runner finishes it, as attempt 2.
	c3, _ := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "rescuer", e.a, e.b)
	e.runWith(t, c3, "rescuer", nil)
	d := e.wait(t, run, idWait, terminalRun)
	if d.Status != engine.RunSucceeded || d.StepList[0].Attempt != 2 {
		t.Fatalf("%s %+v", d.Status, d.StepList)
	}
	if a := e.audit(t, "runner.revoke"); len(a) != 1 || !strings.Contains(a[0].Detail, "stolen laptop") {
		t.Fatalf("audit: %+v", a)
	}
}

// Asking a runner to rotate its key in the middle of a step: the runner
// moves to a new key at its next request, saves it, and the step finishes
// on its first attempt.
func TestKeyRotationMidStepOverHTTP(t *testing.T) {
	e := setupIdentities(t)
	c, id := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "rot", e.a, e.a)
	keyFile := filepath.Join(e.dir, "rot", "identity", "key")
	before, _ := os.ReadFile(keyFile)
	e.runWith(t, c, "rot", nil)
	run := e.startRun(t, "web-x", "pipeline: p\nsteps:\n  a:\n    image: none\n    run: for i in 1 2 3 4 5 6 7 8; do echo tick $i; sleep 0.5; done\n")
	e.wait(t, run, idWait, func(d *engine.RunDetail) bool { return d.StepList[0].Status == engine.StepRunning })
	if code := postJSONCode(t, e.b.URL+"/api/v1/runners/"+id.RunnerID+"/rotate", "{}", nil); code != 200 {
		t.Fatalf("rotate: %d", code)
	}
	d := e.wait(t, run, idWait, terminalRun)
	if d.Status != engine.RunSucceeded || d.StepList[0].Attempt != 1 {
		t.Fatalf("%s %+v", d.Status, d.StepList)
	}
	after, _ := os.ReadFile(keyFile)
	if bytes.Equal(before, after) {
		t.Fatal("the key file did not change")
	}
	got, _ := e.eng.RunnerIdentity(id.RunnerID)
	if got.RotateAsked != 0 || got.KeyFingerprint == "" {
		t.Fatalf("%+v", got)
	}
	if len(e.audit(t, "runner.rotate")) != 2 { // the person's request, and the runner's rotation
		t.Fatalf("audit: %+v", e.audit(t, "runner.rotate"))
	}
	// The identity on disk is the new one.
	loaded, err := runner.LoadIdentity(filepath.Join(e.dir, "rot"))
	if err != nil || !loaded.Key.Equal(id.Key) || bytes.Equal(before, []byte(ed25519.PrivateKey(id.Key).Seed())) {
		t.Fatalf("loaded %v %v", loaded, err)
	}
}

// The runner's accept: section refuses work before anything runs; the step
// fails saying why, without retries, and the refusal is audited. What it
// says it accepts keeps other projects' work away from it altogether.
func TestAcceptRefusals(t *testing.T) {
	e := setupIdentities(t)
	no := false
	c, _ := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "picky", e.a, e.a)
	marker := filepath.Join(e.dir, "ran")
	e.runWith(t, c, "picky", func(r *runner.Runner) {
		r.Accept = &accept.Config{Projects: []string{"web-*"}, Images: []string{"registry.example.com/*"}, PullRequests: &no,
			MaxTimeout: "2h", EnvDeny: []string{"LD_*"}}
	})
	cases := map[string]string{
		"image":   "pipeline: p\nsteps:\n  a: {image: docker.io/evil/x, run: touch " + marker + "}\n",
		"timeout": "pipeline: p\nsteps:\n  a: {image: registry.example.com/a, run: touch " + marker + ", timeout: 3h}\n",
		"env":     "pipeline: p\nsteps:\n  a: {image: registry.example.com/a, run: touch " + marker + ", env: {LD_PRELOAD: x}}\n",
	}
	want := map[string]string{"image": "accept.images", "timeout": "accept.max_timeout", "env": "accept.env_deny"}
	for name, src := range cases {
		run := e.startRun(t, "web-"+name, src+"  b: {image: registry.example.com/a, run: 'true', retry: 2}\n")
		d := e.wait(t, run, idWait, terminalRun)
		var a engine.StepView
		for _, s := range d.StepList {
			if s.ID == "a" {
				a = s
			}
		}
		if a.Status != engine.StepFailed || a.Attempt != 1 || !strings.Contains(a.Error, "the runner refused the step") || !strings.Contains(a.Error, want[name]) {
			t.Errorf("%s: %+v", name, a)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a refused step ran")
	}
	// Four: in the image case, b is refused too, because its run's
	// containers include a's image, which the runner would have started.
	if a := e.audit(t, "runner.refused"); len(a) != 4 {
		t.Fatalf("refusals audited: %+v", a)
	}
	// What it accepts is known to the server, which does not offer it
	// other projects' work (it is not even in its pool here, but the same
	// holds for the default pool's runners).
	list, _ := e.eng.Runners()
	for _, r := range list {
		if r.Name == "picky" && (r.Accept == nil || r.Accept.Projects[0] != "web-*") {
			t.Fatalf("accept not reported: %+v", r)
		}
	}
}

// A runner on another machine never uses a directory of its own host as a
// run's source, and never joins a server ID into a path.
func TestRemoteRunnerRefusesDirSources(t *testing.T) {
	e := setupIdentities(t)
	c, _ := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "remote", e.a, e.a)
	e.runWith(t, c, "remote", func(r *runner.Runner) { r.Remote = true })
	run := e.startRun(t, "web-x", "pipeline: p\nsteps:\n  a: {image: none, run: 'true'}\n")
	d := e.wait(t, run, idWait, terminalRun)
	if s := d.StepList[0]; s.Status != engine.StepFailed || !strings.Contains(s.Error, "directory of this host") {
		t.Fatalf("%+v", s)
	}
}

// Version skew. A runner of this release against a server of the previous
// one (which has no enrolment endpoints) is told so plainly; a runner of the
// previous release (shared token, no new fields) against this server works
// as before and is listed as using a shared token.
func TestRunnerServerSkew(t *testing.T) {
	e := setupIdentities(t)
	// The previous release answers every /api/runner/v2/ path with its
	// catch-all 404, and knows only the shared token.
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/runner/v2/") {
			writeErr(w, http.StatusNotFound, "no such API endpoint")
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(mustURL(t, e.a.URL))
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer lwr_") {
			writeErr(w, http.StatusUnauthorized, "a valid runner token is required")
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer old.Close()
	_, err := runner.Join(context.Background(), http.DefaultClient, old.URL, "lwj_x", t.TempDir(), "h", runner.JoinFacts{})
	if !errors.Is(err, runner.ErrNoEnrolment) {
		t.Fatalf("join against an old server: %v", err)
	}
	// An identity made with a new server, used against an old one.
	c, _ := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web"}`), "skew", e.a, e.a)
	c.Server = old.URL
	c.Auth.ID.Server = old.URL
	c.Auth.Invalidate("")
	if _, err := c.Lease(context.Background(), engine.RunnerInfo{}, 0); err == nil || !strings.Contains(err.Error(), "schema 20") {
		t.Fatalf("access token against an old server: %v", err)
	}
	// The previous release's runner: raw JSON with only the old fields.
	e.startRun(t, "shop", "pipeline: p\nsteps:\n  a: {image: none, run: 'true'}\n")
	req, _ := http.NewRequest(http.MethodPost, e.b.URL+"/api/runner/v1/lease",
		strings.NewReader(`{"runner":{"id":"runner-old","name":"old","executor":"podman","version":"0.9","features":"artifacts"},"wait_seconds":0}`))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"project":"shop"`) {
		t.Fatalf("old runner: %d %s", resp.StatusCode, body)
	}
	list, _ := e.eng.Runners()
	for _, r := range list {
		if r.ID == "runner-old" && (r.Auth != engine.AuthShared || r.Pool != engine.DefaultPool) {
			t.Fatalf("%+v", r)
		}
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// The runner reaches its server through the proxy the environment names
// (HTTP_PROXY, HTTPS_PROXY), or the one its settings name, and trusts the
// certificates of ca_file.
func TestRunnerHonoursProxyAndCA(t *testing.T) {
	e := setupIdentities(t)
	var through atomic.Int64
	var mu sync.Mutex
	hosts := map[string]bool{}
	target := mustURL(t, e.a.URL)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy: the request names the absolute URL.
		through.Add(1)
		mu.Lock()
		hosts[r.URL.Host] = true
		mu.Unlock()
		rp := httputil.NewSingleHostReverseProxy(target)
		r.Host = target.Host
		rp.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	// Loopback addresses are never proxied, so the runner uses a name.
	server := "http://launchway.invalid:" + target.Port()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	hc, err := runner.NewHTTPClient(runner.HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	join := e.joinToken(t, e.a, `{"pool":"web"}`)
	id, err := runner.Join(context.Background(), hc, server, join, t.TempDir(), "proxied", runner.JoinFacts{})
	if err != nil {
		t.Fatalf("join through the proxy: %v", err)
	}
	c := runner.NewClient(server, "")
	c.HTTP = hc
	c.Auth = &runner.IdentityAuth{ID: id, HTTP: hc, Log: quiet()}
	if _, err := c.Lease(context.Background(), engine.RunnerInfo{}, 0); err != nil {
		t.Fatal(err)
	}
	if through.Load() < 3 || !hosts["launchway.invalid:"+target.Port()] {
		t.Fatalf("requests through the proxy: %d %v", through.Load(), hosts)
	}
	// no_proxy in the settings wins over the environment's proxy: the
	// request goes straight to the (unresolvable) name and fails.
	direct, _ := runner.NewHTTPClient(runner.HTTPOptions{NoProxy: "launchway.invalid", Timeout: 5 * time.Second})
	before := through.Load()
	if _, err := direct.Get(server + "/healthz"); err == nil || through.Load() != before {
		t.Fatalf("no_proxy: %v (%d through the proxy)", err, through.Load()-before)
	}

	// ca_file: a server whose certificate a private authority signed.
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer tlsSrv.Close()
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	plain, _ := runner.NewHTTPClient(runner.HTTPOptions{})
	if _, err := plain.Get(tlsSrv.URL); err == nil {
		t.Fatal("an unknown authority was trusted")
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw}), 0o644)
	withCA, err := runner.NewHTTPClient(runner.HTTPOptions{CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := withCA.Get(tlsSrv.URL)
	if err != nil {
		t.Fatalf("with ca_file: %v", err)
	}
	resp.Body.Close()
}

// An ephemeral runner (a pod that enrols at start) is revoked when it
// leaves.
func TestEphemeralRunnerIsRevokedWhenItLeaves(t *testing.T) {
	e := setupIdentities(t)
	c, id := e.enrol(t, e.joinToken(t, e.a, `{"pool":"web","ephemeral":true,"max_uses":5}`), "pod", e.a, e.a)
	if !id.Ephemeral {
		t.Fatal("not ephemeral")
	}
	if err := c.Leave(context.Background(), "ignored"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.eng.RunnerIdentity(id.RunnerID)
	if got.RevokedAt == 0 {
		t.Fatal("the ephemeral runner is still enrolled")
	}
}

// Managing runners needs the role runner_admin names; with sign-in off,
// runner_admin: none turns it off for everyone.
func TestManagingRunnersCanBeTurnedOff(t *testing.T) {
	db := testdb.New(t)
	eng, err := engine.Open(engine.Options{Database: db, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	none := auth.NoRole
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet(), RunnersRole: &none}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	var out struct {
		Error string `json:"error"`
	}
	if code := postJSONCode(t, srv.URL+"/api/v1/runner-join-tokens", `{}`, &out); code != 403 || !strings.Contains(out.Error, "runner_admin: none") {
		t.Fatalf("%d %+v", code, out)
	}
}
