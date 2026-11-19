package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The people in the permission matrix, in the order of each row's
// expectations: nobody signed in, then signed in with no binding, a viewer
// of pay-*, an operator of pay-*, an operator of web only (so pay-api is
// another team's project), and an admin.
var matrixPeople = []string{"", "nobody", "vic", "otto", "dana", "ada"}

// allowed means the permission check let the call through; what happened
// next (200, 201, 409...) is the handler's business.
const allowed = -1

type matrixRow struct {
	method, path, body string
	want               [6]int
}

// permissionTable has one row for every route. A route missing from it
// fails TestEveryRouteIsInThePermissionTable. {pay} is a run of pay-api.
var permissionTable = map[string]matrixRow{
	"GET /healthz":                            {"GET", "/healthz", "", [6]int{200, 200, 200, 200, 200, 200}},
	"GET /readyz":                             {"GET", "/readyz", "", [6]int{200, 200, 200, 200, 200, 200}},
	"POST /api/runner/v1/lease":               {"POST", "/api/runner/v1/lease", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/start":               {"POST", "/api/runner/v1/start", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/heartbeat":           {"POST", "/api/runner/v1/heartbeat", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/logs":                {"POST", "/api/runner/v1/logs", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/finish":              {"POST", "/api/runner/v1/finish", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/runs":                {"POST", "/api/runner/v1/runs", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/leave":               {"POST", "/api/runner/v1/leave", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/deploy/lease":        {"POST", "/api/runner/v1/deploy/lease", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/deploy/watch":        {"POST", "/api/runner/v1/deploy/watch", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/deploy/observe":      {"POST", "/api/runner/v1/deploy/observe", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/deploy/commands":     {"POST", "/api/runner/v1/deploy/commands", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/deploy/command-done": {"POST", "/api/runner/v1/deploy/command-done", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	// Commands on a deployment: operators of the run's project (and of the
	// environment); the run has no such deployment, so the handler says 404.
	"GET /api/v1/runs/{id}/deployments/{dep}/controls":                 {"GET", "/api/v1/runs/{pay}/deployments/dep_x/controls", "", [6]int{401, 404, 404, 404, 404, 404}},
	"POST /api/v1/runs/{id}/deployments/{dep}/promote":                 {"POST", "/api/v1/runs/{pay}/deployments/dep_x/promote", "", [6]int{401, 404, 403, 404, 404, 404}},
	"POST /api/v1/runs/{id}/deployments/{dep}/promote-full":            {"POST", "/api/v1/runs/{pay}/deployments/dep_x/promote-full", "", [6]int{401, 404, 403, 404, 404, 404}},
	"POST /api/v1/runs/{id}/deployments/{dep}/abort":                   {"POST", "/api/v1/runs/{pay}/deployments/dep_x/abort", "", [6]int{401, 404, 403, 404, 404, 404}},
	"POST /api/v1/runs/{id}/deployments/{dep}/retry":                   {"POST", "/api/v1/runs/{pay}/deployments/dep_x/retry", "", [6]int{401, 404, 403, 404, 404, 404}},
	"POST /api/v1/runs/{id}/deployments/{dep}/commands/{cmd}/withdraw": {"POST", "/api/v1/runs/{pay}/deployments/dep_x/commands/cmd_x/withdraw", "", [6]int{401, 404, 403, 404, 404, 404}},
	"GET /auth/login":                  {"GET", "/auth/login", "", [6]int{302, 302, 302, 302, 302, 302}},
	"GET /auth/callback":               {"GET", "/auth/callback", "", [6]int{302, 302, 302, 302, 302, 302}},
	"POST /auth/logout":                {"POST", "/auth/logout", "", [6]int{204, 204, 204, 204, 204, 204}},
	"GET /api/v1/info":                 {"GET", "/api/v1/info", "", [6]int{200, 200, 200, 200, 200, 200}},
	"GET /api/v1/me":                   {"GET", "/api/v1/me", "", [6]int{401, 200, 200, 200, 200, 200}},
	"GET /api/v1/projects":             {"GET", "/api/v1/projects", "", [6]int{401, 200, 200, 200, 200, 200}},
	"GET /api/v1/projects/{name}":      {"GET", "/api/v1/projects/pay-api", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/projects/{name}/refs": {"GET", "/api/v1/projects/pay-api/refs", "", [6]int{401, 404, 200, 200, 404, 200}},
	"POST /api/v1/projects/{name}/runs": {"POST", "/api/v1/projects/pay-api/runs", `{"ref":"main"}`,
		[6]int{401, 404, 403, 201, 404, 201}},
	"GET /api/v1/projects/{name}/pipeline": {"GET", "/api/v1/projects/pay-api/pipeline?ref=main", "", [6]int{401, 404, 200, 200, 404, 200}},
	// An empty save is refused by the handler (no base commit), after the permission check.
	"POST /api/v1/projects/{name}/pipeline": {"POST", "/api/v1/projects/pay-api/pipeline", `{}`, [6]int{401, 404, 403, 400, 404, 400}},
	// Draft runs need the role drafts.allow names: operator by default.
	"POST /api/v1/projects/{name}/draft-runs": {"POST", "/api/v1/projects/pay-api/draft-runs", draftBody, [6]int{401, 404, 403, 201, 404, 201}},
	"GET /api/v1/runs":                        {"GET", "/api/v1/runs", "", [6]int{401, 200, 200, 200, 200, 200}},
	"GET /api/v1/runs/{id}":                   {"GET", "/api/v1/runs/{pay}", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/runs/{id}/events":            {"GET", "/api/v1/runs/{pay}/events", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/runs/{id}/pipeline-file":     {"GET", "/api/v1/runs/{pay}/pipeline-file", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/runs/{id}/steps/{step}/logs": {"GET", "/api/v1/runs/{pay}/steps/gate/logs", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/runs/{id}/tests":             {"GET", "/api/v1/runs/{pay}/tests", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/runs/{id}/tests/cases":       {"GET", "/api/v1/runs/{pay}/tests/cases", "", [6]int{401, 404, 200, 200, 404, 200}},
	// {pay} has no test results: 404 after the permission check too (see TestTestResultsFollowAccess).
	"GET /api/v1/runs/{id}/tests/cases/{case}": {"GET", "/api/v1/runs/{pay}/tests/cases/1", "", [6]int{401, 404, 404, 404, 404, 404}},
	"GET /api/v1/projects/{name}/tests":        {"GET", "/api/v1/projects/pay-api/tests", "", [6]int{401, 404, 200, 200, 404, 200}},
	// No test has run: 404 after the permission check too (see TestTestResultsFollowAccess).
	"GET /api/v1/projects/{name}/tests/history": {"GET", "/api/v1/projects/pay-api/tests/history?key=k", "", [6]int{401, 404, 404, 404, 404, 404}},
	"POST /api/v1/runs/{id}/cancel":             {"POST", "/api/v1/runs/{pay}/cancel", "", [6]int{401, 404, 403, allowed, 404, allowed}},
	"POST /api/v1/runs/{id}/resume":             {"POST", "/api/v1/runs/{pay}/resume", "", [6]int{401, 404, 403, allowed, 404, allowed}},
	// {pay} is waiting for an approval, so it cannot be re-run yet: 409 after the permission check.
	"GET /api/v1/runs/{id}/rerun":                 {"GET", "/api/v1/runs/{pay}/rerun?from=gate", "", [6]int{401, 404, 409, 409, 404, 409}},
	"POST /api/v1/runs/{id}/rerun":                {"POST", "/api/v1/runs/{pay}/rerun", `{"from":"gate"}`, [6]int{401, 404, 403, allowed, 404, allowed}},
	"POST /api/v1/runs/{id}/steps/{step}/approve": {"POST", "/api/v1/runs/{pay}/steps/gate/approve", "", [6]int{401, 404, 403, allowed, 404, allowed}},
	"POST /api/v1/runs/{id}/steps/{step}/reject":  {"POST", "/api/v1/runs/{pay}/steps/gate/reject", "", [6]int{401, 404, 403, allowed, 404, allowed}},
	"GET /api/v1/approvals":                       {"GET", "/api/v1/approvals", "", [6]int{401, 200, 200, 200, 200, 200}},
	"GET /api/v1/runners":                         {"GET", "/api/v1/runners", "", [6]int{401, 403, 200, 200, 200, 200}},
	"GET /api/v1/environments":                    {"GET", "/api/v1/environments", "", [6]int{401, 200, 200, 200, 200, 200}},
	"GET /api/v1/deployments":                     {"GET", "/api/v1/deployments", "", [6]int{401, 200, 200, 200, 200, 200}},
	"POST /api/v1/validate":                       {"POST", "/api/v1/validate", "pipeline: x", [6]int{401, 403, 200, 200, 200, 200}},
	"GET /api/v1/stream":                          {"GET", "/api/v1/stream", "", [6]int{401, 200, 200, 200, 200, 200}},
	"GET /api/v1/tokens":                          {"GET", "/api/v1/tokens", "", [6]int{401, 200, 200, 200, 200, 200}},
	"POST /api/v1/tokens":                         {"POST", "/api/v1/tokens", `{"name":"matrix"}`, [6]int{401, 201, 201, 201, 201, 201}},
	"DELETE /api/v1/tokens/{id}":                  {"DELETE", "/api/v1/tokens/tok_nope", "", [6]int{401, 404, 404, 404, 404, 404}},
	"GET /api/v1/audit":                           {"GET", "/api/v1/audit", "", [6]int{401, 403, 403, 403, 403, 200}},

	// Artifacts, findings and SBOMs. {pay} keeps no artifact: 404 after the
	// permission check (see TestArtifactAccess).
	"POST /api/runner/v1/artifacts/begin":    {"POST", "/api/runner/v1/artifacts/begin", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"PUT /api/runner/v1/artifacts/chunk":     {"PUT", "/api/runner/v1/artifacts/chunk?offset=0", "x", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/artifacts/commit":   {"POST", "/api/runner/v1/artifacts/commit", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"POST /api/runner/v1/artifacts/download": {"POST", "/api/runner/v1/artifacts/download", "{}", [6]int{401, 401, 401, 401, 401, 401}},
	"GET /api/v1/runs/{id}/artifacts":        {"GET", "/api/v1/runs/{pay}/artifacts", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/runs/{id}/artifacts/{artifact}/file": {"GET", "/api/v1/runs/{pay}/artifacts/art_nope/file?path=a", "",
		[6]int{401, 404, 404, 404, 404, 404}},
	"GET /api/v1/runs/{id}/artifacts/{artifact}/archive": {"GET", "/api/v1/runs/{pay}/artifacts/art_nope/archive", "",
		[6]int{401, 404, 404, 404, 404, 404}},
	"DELETE /api/v1/runs/{id}/artifacts/{artifact}": {"DELETE", "/api/v1/runs/{pay}/artifacts/art_nope", "",
		[6]int{401, 404, 403, 404, 404, 404}},
	"GET /api/v1/runs/{id}/findings":       {"GET", "/api/v1/runs/{pay}/findings", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/projects/{name}/security": {"GET", "/api/v1/projects/pay-api/security", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/components":               {"GET", "/api/v1/components?name=left-pad", "", [6]int{401, 200, 200, 200, 200, 200}},

	// Images: what a run built; a search over the projects the caller may see.
	"GET /api/v1/runs/{id}/images": {"GET", "/api/v1/runs/{pay}/images", "", [6]int{401, 404, 200, 200, 404, 200}},
	"GET /api/v1/images": {"GET", "/api/v1/images?repository=harbor.example.com/team/shop", "",
		[6]int{401, 200, 200, 200, 200, 200}},
}

// draftBody starts a draft run that waits for an approval, so it needs no
// runner.
const draftBody = `{"ref": "main", "pipeline": "pipeline: app\nsteps:\n  gate: {kind: approval, message: Ship it}\n"}`

// The routes anyone may call without signing in. Anything else must need
// an identity (or the runner token).
var publicRoutes = []string{"GET /healthz", "GET /readyz", "GET /auth/login", "GET /auth/callback", "POST /auth/logout", "GET /api/v1/info"}

// Every registered route must be in the permission table, and the table
// must not list routes that no longer exist.
func TestEveryRouteIsInThePermissionTable(t *testing.T) {
	var registered, open []string
	for _, rt := range (&Server{}).routes() {
		registered = append(registered, rt.pattern)
		if rt.access == public {
			open = append(open, rt.pattern)
		}
		if _, ok := permissionTable[rt.pattern]; !ok {
			t.Errorf("route %q is not in the permission table in access_test.go; add a row saying who may call it", rt.pattern)
		}
		if strings.HasPrefix(rt.pattern, "POST ") || strings.HasPrefix(rt.pattern, "DELETE ") {
			if rt.audit == "" && rt.access != public && rt.access != runnerOnly && rt.pattern != "POST /api/v1/validate" {
				t.Errorf("route %q changes something but records nothing in the audit trail", rt.pattern)
			}
		}
	}
	for p := range permissionTable {
		if !slices.Contains(registered, p) {
			t.Errorf("the permission table lists %q, which is not a route", p)
		}
	}
	sort.Strings(open)
	want := slices.Clone(publicRoutes)
	sort.Strings(want)
	if !slices.Equal(open, want) {
		t.Errorf("public routes are %v, want %v", open, want)
	}
}

// statusOf sends a request and returns its status without reading the
// body, which for the live stream never ends.
func (b *browser) statusOf(method, url, body string, header ...string) int {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(csrfHeader, "1")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPermissionMatrix(t *testing.T) {
	e := setupAuth(t, envOpts{})
	pay := e.startRun(t, "pay-api")
	e.startRun(t, "web")
	patterns := make([]string, 0, len(permissionTable))
	for p := range permissionTable {
		if p != "POST /auth/logout" {
			patterns = append(patterns, p)
		}
	}
	sort.Strings(patterns)
	patterns = append(patterns, "POST /auth/logout") // last: it signs the person out

	for i, user := range matrixPeople {
		b := newBrowser(t)
		name := user
		if user == "" {
			name = "(not signed in)"
		} else {
			b = e.signIn(t, user)
		}
		for _, p := range patterns {
			row := permissionTable[p]
			got := b.statusOf(row.method, e.srv.URL+strings.ReplaceAll(row.path, "{pay}", pay), row.body)
			want := row.want[i]
			ok := got == want
			if want == allowed {
				ok = got != 401 && got != 403 && got != 404 && got < 500
			}
			if !ok {
				t.Errorf("%s: %s gave %d, want %s", name, p, got, describe(want))
			}
		}
	}

	// The runner token opens the runner API and nothing else.
	b := newBrowser(t)
	if got := b.statusOf("POST", e.srv.URL+"/api/runner/v1/runs", `{"ids":[]}`, bearer("runner-secret")...); got != 200 {
		t.Errorf("runner API with the runner token: %d", got)
	}
	if got := b.statusOf("GET", e.srv.URL+"/api/v1/runs", "", "Authorization", "Bearer runner-secret"); got != 401 {
		t.Errorf("UI API with the runner token: %d", got)
	}
}

func describe(code int) string {
	if code == allowed {
		return "anything the permission check lets through"
	}
	return fmt.Sprint(code)
}

// Lists show only what the caller may see.
func TestListsAreFiltered(t *testing.T) {
	e := setupAuth(t, envOpts{})
	pay, web := e.startRun(t, "pay-api"), e.startRun(t, "web")
	type views struct{ runs, projects, approvals []string }
	see := func(user string) views {
		b := e.signIn(t, user)
		var v views
		var runs []engine.RunSummary
		b.get(e.srv.URL+"/api/v1/runs").json(t, &runs)
		for _, r := range runs {
			v.runs = append(v.runs, r.ID)
		}
		var projects []struct{ Name string }
		b.get(e.srv.URL+"/api/v1/projects").json(t, &projects)
		for _, p := range projects {
			v.projects = append(v.projects, p.Name)
		}
		var approvals []engine.Approval
		b.get(e.srv.URL+"/api/v1/approvals").json(t, &approvals)
		for _, a := range approvals {
			v.approvals = append(v.approvals, a.RunID)
		}
		// Asking for another project's runs by name shows nothing either.
		var filtered []engine.RunSummary
		b.get(e.srv.URL+"/api/v1/runs?project=web").json(t, &filtered)
		for _, r := range filtered {
			if r.Project != "web" || !slices.Contains(v.runs, r.ID) {
				t.Errorf("%s: ?project=web gave %s of %s", user, r.ID, r.Project)
			}
		}
		return v
	}
	for user, want := range map[string]views{
		"nobody": {},
		"vic":    {[]string{pay}, []string{"pay-api"}, []string{pay}},
		"dana":   {[]string{web}, []string{"web"}, []string{web}},
		"ada":    {[]string{web, pay}, []string{"pay-api", "web"}, []string{web, pay}},
	} {
		got := see(user)
		if !slices.Equal(got.runs, want.runs) || !slices.Equal(got.projects, want.projects) || !slices.Equal(got.approvals, want.approvals) {
			t.Errorf("%s sees %+v, want %+v", user, got, want)
		}
	}
}

// sse reads a live stream in the background.
type sse struct {
	mu     sync.Mutex
	events []string // "type project" for run events, "type" for others
	done   chan struct{}
	cancel context.CancelFunc
	status int
}

func (b *browser) stream(t *testing.T, url string) *sse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := b.client.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &sse{done: make(chan struct{}), cancel: cancel, status: resp.StatusCode}
	t.Cleanup(func() { cancel(); <-s.done })
	go func() {
		defer close(s.done)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		typ := ""
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				typ = v
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				var d struct{ Project string }
				json.Unmarshal([]byte(v), &d)
				s.mu.Lock()
				s.events = append(s.events, strings.TrimSpace(typ+" "+d.Project))
				s.mu.Unlock()
			}
		}
	}()
	return s
}

func (s *sse) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

// The live stream delivers only runs the caller may see, and stops when
// their session ends.
func TestLiveStreamIsFiltered(t *testing.T) {
	e := setupAuth(t, envOpts{ping: 50 * time.Millisecond})
	streams := map[string]*sse{}
	for _, user := range []string{"nobody", "vic", "dana", "ada"} {
		streams[user] = e.signIn(t, user).stream(t, e.srv.URL+"/api/v1/stream")
	}
	pay, web := e.startRun(t, "pay-api"), e.startRun(t, "web")
	want := map[string][]string{"vic": {"run pay-api"}, "dana": {"run web"}, "ada": {"run pay-api", "run web"}, "nobody": nil}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(streams["ada"].seen()) < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // anything that should not arrive has had time to
	for user, s := range streams {
		got := slices.Compact(slices.Sorted(slices.Values(s.seen())))
		if !slices.Equal(got, want[user]) {
			t.Errorf("%s saw %v, want %v", user, got, want[user])
		}
	}

	// One run's stream, steps and logs included, needs the right to see it.
	vic := e.signIn(t, "vic")
	if s := vic.stream(t, e.srv.URL+"/api/v1/stream?run="+web); s.status != http.StatusNotFound {
		t.Errorf("vic streaming a web run: %d", s.status)
	}
	own := vic.stream(t, e.srv.URL+"/api/v1/stream?run="+pay)
	if own.status != http.StatusOK {
		t.Fatalf("vic streaming a pay-api run: %d", own.status)
	}

	// Signing out ends the streams that session opened.
	if r := vic.do(http.MethodPost, e.srv.URL+"/auth/logout", ""); r.status != http.StatusNoContent {
		t.Fatalf("logout: %d", r.status)
	}
	select {
	case <-own.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream went on after signing out")
	}
}

// Rights are checked against the project of the run itself, whatever the
// URL claims.
func TestRunRoutesUseTheRunsProject(t *testing.T) {
	e := setupAuth(t, envOpts{})
	web := e.startRun(t, "web")
	otto := e.signIn(t, "otto") // operator of pay-* only
	for _, path := range []string{"/api/v1/runs/" + web, "/api/v1/runs/" + web + "/events", "/api/v1/runs/" + web + "/steps/gate/logs"} {
		r := otto.get(e.srv.URL + path)
		if r.status != http.StatusNotFound || !strings.Contains(string(r.body), "not found: run "+web) {
			t.Errorf("%s: %d %s", path, r.status, r.body)
		}
	}
	// A run that does not exist looks the same as one that may not be seen.
	missing := otto.get(e.srv.URL + "/api/v1/runs/run_doesnotexist")
	hidden := otto.get(e.srv.URL + "/api/v1/runs/" + web)
	if missing.status != hidden.status || strings.ReplaceAll(string(hidden.body), web, "X") != strings.ReplaceAll(string(missing.body), "run_doesnotexist", "X") {
		t.Errorf("missing %d %s, hidden %d %s", missing.status, missing.body, hidden.status, hidden.body)
	}
	if r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/runs/"+web+"/steps/gate/approve", ""); r.status != http.StatusNotFound {
		t.Errorf("approving another team's run: %d", r.status)
	}
	if d, _ := e.eng.GetRun(web); stepStatus(d, "gate") != engine.StepWaitingS {
		t.Errorf("the gate is %s", stepStatus(d, "gate"))
	}
}

// With sign-in on, the person who started a run may not approve it.
func TestSelfApprovalWithSignIn(t *testing.T) {
	e := setupAuth(t, envOpts{})
	otto := e.signIn(t, "otto")
	r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`)
	var run engine.RunSummary
	r.json(t, &run)
	if run.Actor != "otto" {
		t.Fatalf("the run was started by %q", run.Actor)
	}
	base := e.srv.URL + "/api/v1/runs/" + run.ID + "/steps/gate/"
	if r := otto.do(http.MethodPost, base+"approve", ""); r.status != http.StatusForbidden {
		t.Fatalf("approving your own run: %d %s", r.status, r.body)
	}
	ada := e.signIn(t, "ada")
	if r := ada.do(http.MethodPost, base+"approve", `{"comment":"looks good"}`); r.status != http.StatusOK {
		t.Fatalf("someone else approving: %d %s", r.status, r.body)
	}
	d, _ := e.eng.GetRun(run.ID)
	if d.StepList[0].DecidedBy != "ada" {
		t.Fatalf("decided by %q", d.StepList[0].DecidedBy)
	}
}
