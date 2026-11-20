package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/fakeidp"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The people the sign-in tests use, and what the policy below gives them.
var testPeople = map[string][]string{
	"ada":    {"admins"},         // admin everywhere
	"otto":   {"pay-ops"},        // operator on pay-*
	"vic":    {"pay-view"},       // viewer on pay-*
	"dana":   {"web-team"},       // operator on web only (a user binding)
	"nobody": {"somewhere-else"}, // no binding at all
}

func testPolicy(t testing.TB) *auth.Policy {
	p, err := auth.NewPolicy([]auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{Group: "pay-ops", Role: auth.Operator, Projects: []string{"pay-*"}},
		{Group: "pay-view", Role: auth.Viewer, Projects: []string{"pay-*"}},
		{User: "dana", Role: auth.Operator, Projects: []string{"web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var testClockStart = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

// testClock is the clock sessions, tokens and ID tokens are judged by.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// authEnv is a server with sign-in on, a fake provider, and two Git
// projects: pay-api and web.
type authEnv struct {
	eng *engine.Engine
	srv *httptest.Server
	api *Server
	idp *fakeidp.Provider
	clk *testClock
	db  string
	dir string
}

// The pipeline of both projects: it waits for an approval, so runs need no
// runner to stay alive.
const gatedPipeline = `
pipeline: app
steps:
  gate: {kind: approval, message: "Ship it?"}
  after: {image: none, needs: gate, run: echo shipped}
`

// gitRepo makes a repository with one commit on main.
func gitRepo(t testing.TB, dir, pipelineYAML string) string {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, ".launchway"), 0o755)
	os.WriteFile(filepath.Join(dir, ".launchway", "pipeline.yaml"), []byte(pipelineYAML), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=d@example.com",
			"GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=d@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

type envOpts struct {
	db          string            // share a database (another server); default a new one
	idp         *fakeidp.Provider // share a provider; default a new one
	clk         *testClock
	redirectURL string // default this server's own /auth/callback
	trustProxy  bool
	ping        time.Duration
	drafts      *auth.DraftRules // who may start draft runs; default the built-in rule
	// More projects, beside pay-api and web, and the libraries their
	// pipelines may use.
	projects  []source.Project
	libraries []source.Library
	// Environments of deploy steps, and a policy to use instead of
	// testPolicy.
	environments []engine.Environment
	policy       *auth.Policy
	// artifacts, if set, is where artifacts are kept and how.
	artifacts *engine.ArtifactOptions
	// policies are delivery policies; waiveRole who may waive them.
	policies  engine.PolicyOptions
	waiveRole *auth.Role
	// mcp, if set, turns the MCP endpoint on.
	mcp *MCPOptions
	// healthSources are the health sources verify steps may name.
	healthSources []engine.HealthSource
}

func setupAuth(t *testing.T, o envOpts) *authEnv {
	t.Helper()
	if o.db == "" {
		o.db = testdb.New(t)
	}
	if o.clk == nil {
		o.clk = &testClock{t: testClockStart}
	}
	if o.idp == nil {
		o.idp = fakeidp.New()
		t.Cleanup(o.idp.Close)
	}
	o.idp.Now = o.clk.Now
	no := false
	var arts engine.ArtifactOptions
	if o.artifacts != nil {
		arts = *o.artifacts
	}
	eng, err := engine.Open(engine.Options{Database: o.db, Logger: quiet(), AllowSelfApproval: &no, Artifacts: arts, Environments: o.environments,
		Policy: o.policies, HealthSources: o.healthSources})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	repos := filepath.Join(dir, "repos")
	mgr, err := source.NewManagerWithLibraries(eng, filepath.Join(dir, "data"), append([]source.Project{
		// The editor may save to pay-api (new branches only), not to web.
		{Name: "pay-api", URL: gitRepo(t, filepath.Join(repos, "pay"), gatedPipeline), Poll: time.Hour, Push: &source.PushSettings{}},
		{Name: "web", URL: gitRepo(t, filepath.Join(repos, "web"), gatedPipeline), Poll: time.Hour},
	}, o.projects...), o.libraries, quiet())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng, Projects: mgr, RunnerToken: "runner-secret", Log: quiet(), Now: o.clk.Now,
		TrustProxy: o.trustProxy, StreamPing: o.ping, Drafts: o.drafts, WaiveRole: o.waiveRole, MCP: o.mcp}
	s.Settings = testSettings(t, eng)
	srv := httptest.NewUnstartedServer(nil)
	redirect := o.redirectURL
	if redirect == "" {
		redirect = "http://" + srv.Listener.Addr().String() + "/auth/callback"
	}
	s.Auth = &Auth{
		OIDC: auth.NewOIDC(auth.OIDCOptions{Issuer: o.idp.URL, ClientID: o.idp.ClientID, ClientSecret: o.idp.ClientSecret,
			RedirectURL: redirect, Now: o.clk.Now}),
		Policy: testPolicy(t), IdleTimeout: 8 * time.Hour, Lifetime: 24 * time.Hour,
	}
	if o.policy != nil {
		s.Auth.Policy = o.policy
	}
	srv.Config.Handler = s.Handler()
	srv.Start()
	t.Cleanup(func() { srv.Close(); eng.Close() })
	return &authEnv{eng: eng, srv: srv, api: s, idp: o.idp, clk: o.clk, db: o.db, dir: dir}
}

// startRun starts a run of a project as someone who is not one of the
// test people, so anyone may approve it.
func (e *authEnv) startRun(t *testing.T, project string) string {
	t.Helper()
	r, err := e.api.Projects.StartRun(context.Background(), project, "main", "someone-else", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// browser is a web browser with a cookie jar that does not follow
// redirects by itself.
type browser struct {
	t      *testing.T
	client *http.Client
	jar    http.CookieJar
}

func newBrowser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, jar: jar, client: &http.Client{Jar: jar, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// response is what a request returned.
type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) location() string { return r.header.Get("Location") }

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decoding %s: %v", r.body, err)
	}
}

// do sends a request. Requests that change something carry the CSRF header
// unless header says otherwise.
func (b *browser) do(method, url, body string, header ...string) response {
	b.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		b.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(csrfHeader, "1")
	}
	for i := 0; i+1 < len(header); i += 2 {
		if header[i+1] == "" {
			req.Header.Del(header[i])
		} else {
			req.Header.Set(header[i], header[i+1])
		}
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: data}
}

func (b *browser) get(url string, header ...string) response {
	b.t.Helper()
	return b.do(http.MethodGet, url, "", header...)
}

// beginLogin asks base to start a sign-in and returns the provider's
// authorize URL it redirected to.
func (b *browser) beginLogin(base, next string) string {
	b.t.Helper()
	u := base + "/auth/login"
	if next != "" {
		u += "?next=" + url.QueryEscape(next)
	}
	r := b.get(u)
	if r.status != http.StatusFound {
		b.t.Fatalf("login: %d %s", r.status, r.body)
	}
	return r.location()
}

// authorize visits the provider, which signs in at once, and returns the
// callback URL it sends the browser back to.
func (b *browser) authorize(authURL string) string {
	b.t.Helper()
	r := b.get(authURL)
	if r.status != http.StatusFound {
		b.t.Fatalf("authorize: %d %s", r.status, r.body)
	}
	return r.location()
}

// login signs in through base and returns the callback's response.
func (b *browser) login(base, next string) response {
	b.t.Helper()
	return b.get(b.authorize(b.beginLogin(base, next)))
}

// signIn returns a browser signed in as a test person.
func (e *authEnv) signIn(t *testing.T, user string) *browser {
	t.Helper()
	e.idp.SignInAs(user, testPeople[user]...)
	b := newBrowser(t)
	if r := b.login(e.srv.URL, "/"); r.status != http.StatusFound || r.location() != "/" {
		t.Fatalf("signing in %s: %d %s %s", user, r.status, r.location(), r.body)
	}
	return b
}

// cookieValue returns the value of a cookie the browser holds for base.
func (b *browser) cookieValue(base, name string) string {
	u, _ := url.Parse(base)
	for _, c := range b.jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// setCookie returns the Set-Cookie line for a cookie, or "".
func setCookie(r response, name string) string {
	for _, line := range r.header.Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			return line
		}
	}
	return ""
}

// bearer returns a header pair that authenticates with an API token.
func bearer(token string) []string { return []string{"Authorization", "Bearer " + token} }

// newToken makes an API token as the browser's user and returns it.
func (b *browser) newToken(base, name string) (id, token string) {
	b.t.Helper()
	r := b.do(http.MethodPost, base+"/api/v1/tokens", `{"name": "`+name+`"}`)
	if r.status != http.StatusCreated {
		b.t.Fatalf("creating a token: %d %s", r.status, r.body)
	}
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	r.json(b.t, &out)
	return out.ID, out.Token
}

// auditEntries reads the whole audit trail from the database, oldest first.
func (e *authEnv) auditEntries(t *testing.T) []engine.AuditEntry {
	t.Helper()
	list, err := e.eng.AuditLog(engine.AuditFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}
