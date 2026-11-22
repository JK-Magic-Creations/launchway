package api

import (
	"sync"

	"context"
	"encoding/json"
	"github.com/jk-magic-creations/launchway/internal/source"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/github/githubfake"
	"github.com/jk-magic-creations/launchway/internal/hub"
)

// hostsEnv is a server with an outside-host connection to a fake host and the Flight pay-hooks attached to its repository.
func hostsEnv(t *testing.T) (*authEnv, *githubfake.Host) {
	t.Helper()
	e := hooksEnv(t)
	h := githubfake.New()
	t.Cleanup(h.Close)
	t.Setenv("LW_OH_TOKEN", githubfake.Token)
	t.Setenv("LW_OH_HOOK", "oh-secret")
	g, _ := egress.New([]string{"127.0.0.1"})
	svc := hosts.New(e.eng.Database(), e.eng, g)
	svc.PublicURL = "https://lw.example.com"
	if _, err := svc.Add(context.Background(), hosts.AddInput{Name: "gh", Kind: forge.KindGitHub, BaseURL: h.BaseURL(),
		Credential: forge.SecretRef{Env: "LW_OH_TOKEN"}, WebhookSecret: forge.SecretRef{Env: "LW_OH_HOOK"}}, "dana"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Attach(context.Background(), "pay-hooks", "gh", "acme/app"); err != nil {
		t.Fatal(err)
	}
	e.api.Hosts = svc
	return e, h
}

const ohPush = `{"ref":"refs/heads/main","before":"0000000000000000000000000000000000000000","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository":{"full_name":"acme/app"},"sender":{"login":"bob"}}`

func TestOutsideHostWebhookRoute(t *testing.T) {
	e, _ := hostsEnv(t)
	b := newBrowser(t)
	url := e.srv.URL + "/api/v1/outside-hosts/gh/webhook"
	good := githubfake.Sign([]byte("oh-secret"), []byte(ohPush))
	send := func(u, sig, delivery string) response {
		return b.do("POST", u, ohPush, "X-Hub-Signature-256", sig, "X-GitHub-Delivery", delivery, "X-GitHub-Event", "push")
	}
	if r := send(url, good, "o-1"); r.status != 202 || !strings.Contains(string(r.body), "polling") {
		t.Fatalf("a signed push: %d %s", r.status, r.body)
	}
	if r := send(url, good, "o-1"); r.status != 202 || !strings.Contains(string(r.body), "already received") {
		t.Fatalf("a replay: %d %s", r.status, r.body)
	}
	// A wrong signature, an unknown connection and a missing signature look alike.
	wrong := send(url, "sha256="+strings.Repeat("0", 64), "o-2")
	none := send(e.srv.URL+"/api/v1/outside-hosts/nope/webhook", good, "o-3")
	unsigned := send(url, "", "o-4")
	for _, r := range []response{wrong, none, unsigned} {
		if r.status != 404 || string(r.body) != string(wrong.body) {
			t.Fatalf("the refusals differ: %d %s", r.status, r.body)
		}
	}
	// The event of a repository no Flight uses changes nothing.
	other := strings.Replace(ohPush, "acme/app", "acme/other", 1)
	r := b.do("POST", url, other, "X-Hub-Signature-256", githubfake.Sign([]byte("oh-secret"), []byte(other)), "X-GitHub-Delivery", "o-5", "X-GitHub-Event", "push")
	if r.status != 202 || !strings.Contains(string(r.body), "ignored") {
		t.Fatalf("another repository: %d %s", r.status, r.body)
	}
}

func TestOutsideHostWebhookHasARequestCap(t *testing.T) {
	e, _ := hostsEnv(t)
	e.api.Hosts.HookCap = 3
	b := newBrowser(t)
	url := e.srv.URL + "/api/v1/outside-hosts/gh/webhook"
	good := githubfake.Sign([]byte("oh-secret"), []byte(ohPush))
	var last response
	for i := 0; i < 4; i++ {
		last = b.do("POST", url, ohPush, "X-Hub-Signature-256", good, "X-GitHub-Delivery", "cap-"+string(rune('a'+i)), "X-GitHub-Event", "push")
	}
	if last.status != 429 || last.header.Get("Retry-After") == "" {
		t.Fatalf("the fourth call in a minute: %d %s", last.status, last.body)
	}
}

// The reporter follows the live updates: a status for each Sortie, a status for a failed step, and one
// summary comment edited in place on a pull request.
func TestOutsideHostReporterFollowsTheHub(t *testing.T) {
	e, h := hostsEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.api.StartOutsideReports(ctx)
	sha := strings.Repeat("c", 40)
	publish := func(status engine.RunStatus, pr int, steps ...engine.StepBrief) {
		e.eng.Hub().Publish(hub.Message{Type: "run", RunID: "run_1", Data: &engine.RunSummary{ID: "run_1", Project: "pay-hooks", Number: 4,
			Pipeline: "app", Status: status, SHA: sha, PullRequest: pr, Steps: steps}})
	}
	publish(engine.RunRunning, 7)
	publish(engine.RunRunning, 7) // the same state again is not posted again
	publish(engine.RunFailed, 7, engine.StepBrief{ID: "test", Status: engine.StepFailed, Reason: "exit 1"})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.Lock()
		n, c := len(h.Statuses), len(h.Comments)
		h.Unlock()
		if n >= 3 && c >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.Lock()
	defer h.Unlock()
	states := []string{}
	for _, s := range h.Statuses {
		states = append(states, s.Context+"="+s.State)
		if s.TargetURL != "" && !strings.HasPrefix(s.TargetURL, "https://lw.example.com/runs/") {
			t.Errorf("link %q", s.TargetURL)
		}
	}
	want := []string{"launchway/app=pending", "launchway/app=failure", "launchway/app/test=failure"}
	if len(states) != 3 {
		t.Fatalf("statuses %v, want %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Errorf("status %d: %s, want %s", i, states[i], want[i])
		}
	}
	if len(h.Comments) != 1 {
		t.Fatalf("comments %v", h.Comments)
	}
	for _, c := range h.Comments {
		if !strings.Contains(c, "Sortie #4 failed") || !strings.Contains(c, "Failed: test") {
			t.Errorf("summary %q", c)
		}
	}
}

func wbJob(mode, merge string) *engine.Job {
	return &engine.Job{RunID: "run_9", Project: "pay-hooks", Deploy: &engine.DeployJob{Branch: "main", Deployment: "dep1",
		WriteBack: &engine.WriteBackJob{WriteBack: engine.WriteBack{Repo: "gh:acme/app", Mode: mode, Merge: merge}, WorkBranch: "launchway/deploy/dep1"}}}
}

func TestOutsideWriteBackCredentialAndRefusals(t *testing.T) {
	e, h := hostsEnv(t)
	ctx := context.Background()
	job := wbJob("direct", "")
	e.api.mintWriteBack(ctx, job)
	wb := job.Deploy.WriteBack
	if wb.Error != "" || !wb.Outside || wb.Token != githubfake.Token || wb.Username != "x-access-token" || !strings.HasPrefix(wb.Handle, "outside.") {
		t.Fatalf("%+v", wb)
	}
	if strings.Contains(wb.Handle, githubfake.Token) {
		t.Fatal("the handle holds the credential")
	}
	// A protected branch refuses a direct write, naming the pull request mode.
	h.Protect = true
	job = wbJob("direct", "")
	e.api.mintWriteBack(ctx, job)
	if !strings.Contains(job.Deploy.WriteBack.Error, "pull_request") || job.Deploy.WriteBack.Token != "" {
		t.Fatalf("%+v", job.Deploy.WriteBack)
	}
	// Launchway does not merge on this host: merge auto is refused in words.
	job = wbJob("pull_request", "auto")
	e.api.mintWriteBack(ctx, job)
	if !strings.Contains(job.Deploy.WriteBack.Error, "person") {
		t.Fatalf("%+v", job.Deploy.WriteBack)
	}
	// A tampered or expired handle opens nothing.
	good := wbJob("pull_request", "person")
	e.api.mintWriteBack(ctx, good)
	pull := func(token, op string, n int) (int, map[string]any) {
		rec := httptest.NewRecorder()
		e.api.outsideDeployPull(rec, httptest.NewRequest("POST", "/x", nil), token, op, n, "Deploy dep1", "body")
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := pull(good.Deploy.WriteBack.Handle+"x", "open", 0); code != 403 {
		t.Fatalf("a tampered handle: %d", code)
	}
	if code, _ := pull(wbJob("direct", "").Deploy.WriteBack.Handle, "open", 0); code != 403 {
		t.Fatalf("an empty handle: %d", code)
	}
	code, out := pull(good.Deploy.WriteBack.Handle, "open", 0)
	if code != 200 || out["state"] != "open" || out["number"] != float64(1) || out["waiting_for"] == "" {
		t.Fatalf("open: %d %v", code, out)
	}
	if code, out = pull(good.Deploy.WriteBack.Handle, "open", 0); code != 200 || out["number"] != float64(1) || len(h.Pulls) != 1 {
		t.Fatalf("opening again finds the same pull request: %d %v %d", code, out, len(h.Pulls))
	}
	h.Lock()
	h.Pulls[0]["state"], h.Pulls[0]["merged_at"], h.Pulls[0]["merge_commit_sha"] = "closed", "2026-01-01T00:00:00Z", strings.Repeat("9", 40)
	h.Unlock()
	if code, out = pull(good.Deploy.WriteBack.Handle, "status", 1); code != 200 || out["state"] != "merged" || out["commit"] != strings.Repeat("9", 40) {
		t.Fatalf("status after the merge: %d %v", code, out)
	}
	if len(h.Violations) > 0 {
		t.Errorf("violations %v", h.Violations)
	}
}

func TestOutsideSourceGivesTheConnectionsCredentialOnlyToAttachedFlights(t *testing.T) {
	e, _ := hostsEnv(t)
	// The fake host names its repositories on git.example.com, which the allow-list does not list: refused, with no secret in the message.
	if _, ok, err := e.api.Credential(context.Background(), "pay-hooks"); ok || err == nil || strings.Contains(err.Error(), githubfake.Token) {
		t.Fatalf("an address outside the allow-list was given a credential: %v %v", ok, err)
	}
	// Listed, the connection's credential is given, to the attached Flight only.
	g, _ := egress.New([]string{"127.0.0.1", "git.example.com"})
	svc := hosts.New(e.eng.Database(), e.eng, g)
	e.api.Hosts = svc
	c, ok, err := e.api.Credential(context.Background(), "pay-hooks")
	if err != nil || !ok || c.Secret != githubfake.Token || !strings.HasPrefix(c.URL, "https://git.example.com/") {
		t.Fatalf("credential = %+v %v %v", c, ok, err)
	}
	if _, ok, err := e.api.Credential(context.Background(), "not-attached"); ok || err != nil {
		t.Fatalf("a project that is not attached got a credential: %v %v", ok, err)
	}
}

// With no webhook arriving, the watcher asks the host about pull requests; what it learns (title, author, draft,
// fork, address) is noted for the project's poller. A connection that receives deliveries is left alone.
func TestOutsideWatcherPollsQuietConnections(t *testing.T) {
	e, h := hostsEnv(t)
	sha := strings.Repeat("d", 40)
	h.Lock()
	h.Pulls = append(h.Pulls, map[string]any{"number": 9, "title": "Retry the hook", "state": "open", "draft": true, "html_url": "https://git.example.com/acme/app/pull/9",
		"user": map[string]any{"login": "dana"},
		"head": map[string]any{"ref": "fix", "sha": sha, "repo": map[string]any{"full_name": "mallory/app"}},
		"base": map[string]any{"ref": "main", "sha": strings.Repeat("a", 40), "repo": map[string]any{"full_name": "acme/app"}}})
	h.Unlock()
	cursors := &sync.Map{}
	e.api.pollQuietFlights(context.Background(), cursors, 0)
	got, ok := e.api.Projects.PullNote("pay-hooks", 9)
	if !ok || got.Title != "Retry the hook" || got.Author != "dana" || !got.Draft || !got.Fork || got.Head != sha || got.Base != "main" || got.URL != "https://git.example.com/acme/app/pull/9" {
		t.Fatalf("note = %+v %v", got, ok)
	}
	// A delivery just now: the connection is not polled (the pull request is not asked about again).
	h.Lock()
	h.Pulls = h.Pulls[:0]
	h.Unlock()
	e.api.Projects.NotePullRequest("pay-hooks", source.PullRequestInfo{Number: 9, Head: sha, Title: "kept"})
	if _, err := e.api.Hosts.Store().SeeDelivery(context.Background(), connID(t, e), "d-1"); err != nil {
		t.Fatal(err)
	}
	e.api.pollQuietFlights(context.Background(), cursors, time.Hour)
	if got, _ := e.api.Projects.PullNote("pay-hooks", 9); got.Title != "kept" {
		t.Fatalf("a connection with deliveries was polled: %+v", got)
	}
}

func connID(t *testing.T, e *authEnv) string {
	t.Helper()
	cs, err := e.api.Hosts.Store().Connections(context.Background())
	if err != nil || len(cs) != 1 {
		t.Fatalf("connections %v %v", cs, err)
	}
	return cs[0].ID
}

func TestSummaryCommentCarriesTestAndFindingTotals(t *testing.T) {
	sum := &engine.RunSummary{Number: 4, Status: engine.RunFailed, SHA: strings.Repeat("c", 40),
		Tests:    &engine.TestTotals{Total: 120, Passed: 115, Failed: 3, Errored: 1, Skipped: 1},
		Findings: &engine.FindingCounts{Total: 5, Critical: 1, High: 2, Medium: 1, Low: 1}}
	b := summaryBody(sum, "https://lw.example.com/runs/run_1")
	for _, w := range []string{"Tests: 115 passed, 4 failed, 1 skipped of 120.", "Findings: 5 (1 critical, 2 high, 1 medium, 1 low)."} {
		if !strings.Contains(b, w) {
			t.Errorf("missing %q in\n%s", w, b)
		}
	}
	if strings.Contains(summaryBody(&engine.RunSummary{Number: 1, SHA: sum.SHA}, ""), "Tests:") {
		t.Error("totals shown when there are none")
	}
}
