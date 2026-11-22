package api

import (
	"context"
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
