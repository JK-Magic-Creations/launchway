package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// GitHub's documented example: the secret "It's a Secret to Everybody"
// signs the payload "Hello, World!" as this.
const (
	ghSecret  = "It's a Secret to Everybody"
	ghPayload = "Hello, World!"
	ghSig     = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
)

func TestGitHubSignatureVector(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", strings.NewReader(ghPayload))
	r.Header.Set("X-Hub-Signature-256", ghSig)
	if ok, _, _, err := verifyGitHook("github", []byte(ghSecret), r); !ok || err != nil {
		t.Fatalf("GitHub's own example does not verify: %v", err)
	}
	// Gitea and Forgejo sign the same way, without the prefix.
	for _, h := range []string{"X-Gitea-Signature", "X-Forgejo-Signature"} {
		r, _ := http.NewRequest("POST", "/", strings.NewReader(ghPayload))
		r.Header.Set(h, strings.TrimPrefix(ghSig, "sha256="))
		if ok, _, _, _ := verifyGitHook("gitea", []byte(ghSecret), r); !ok {
			t.Fatalf("%s does not verify", h)
		}
	}
	for name, mutate := range map[string]func(r *http.Request){
		"another body":    func(r *http.Request) { r.Body = http.NoBody },
		"no signature":    func(r *http.Request) { r.Header.Del("X-Hub-Signature-256") },
		"no prefix":       func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", strings.TrimPrefix(ghSig, "sha256=")) },
		"not hex":         func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256=zz") },
		"SHA-1 signature": func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha1=01") },
	} {
		r, _ := http.NewRequest("POST", "/", strings.NewReader(ghPayload))
		r.Header.Set("X-Hub-Signature-256", ghSig)
		mutate(r)
		if ok, _, _, _ := verifyGitHook("github", []byte(ghSecret), r); ok {
			t.Errorf("%s verified", name)
		}
	}
	// GitLab sends its secret token as it is.
	r, _ = http.NewRequest("POST", "/", strings.NewReader("{}"))
	r.Header.Set("X-Gitlab-Token", ghSecret)
	if ok, _, _, _ := verifyGitHook("gitlab", []byte(ghSecret), r); !ok {
		t.Fatal("GitLab's token")
	}
	r.Header.Set("X-Gitlab-Token", ghSecret+"x")
	if ok, _, _, _ := verifyGitHook("gitlab", []byte(ghSecret), r); ok {
		t.Fatal("a wrong GitLab token")
	}
}

const hookPipeline = `
pipeline: app
inputs:
  version: { pattern: "^v[0-9]+$", required: true }
  target: { type: choice, options: [staging, prod], default: staging }
on:
  webhook:
    - { name: deploy, inputs: { version: body.release.version, target: body.target } }
    - { name: signed, inputs: { version: body.version } }
    - { name: unconfigured, inputs: { version: body.version } }
steps:
  gate: {kind: approval, message: "Ship it?"}
`

func hooksEnv(t *testing.T) *authEnv {
	t.Helper()
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "hmac")
	os.WriteFile(secretFile, []byte("hmac-secret\n"), 0o600)
	t.Setenv("LW_TEST_GH_SECRET", ghSecret)
	t.Setenv("LW_TEST_GL_SECRET", "gitlab-token")
	t.Setenv("LW_TEST_DEPLOY_TOKEN", "deploy-token")
	return setupAuth(t, envOpts{projects: []source.Project{
		{Name: "pay-hooks", URL: gitRepo(t, filepath.Join(dir, "hooks"), hookPipeline), Poll: time.Hour,
			Hook: &source.GitHook{Kind: "github", Secret: source.SecretSource{Env: "LW_TEST_GH_SECRET"}},
			Triggers: map[string]*source.TriggerAuth{
				"deploy": {Token: source.SecretSource{Env: "LW_TEST_DEPLOY_TOKEN"}},
				"signed": {HMAC: source.SecretSource{File: secretFile}},
			}},
		{Name: "pay-gitlab", URL: gitRepo(t, filepath.Join(dir, "gl"), hookPipeline), Poll: time.Hour,
			Hook: &source.GitHook{Kind: "gitlab", Secret: source.SecretSource{Env: "LW_TEST_GL_SECRET"}}},
	}})
}

func TestGitHostWebhooks(t *testing.T) {
	e := hooksEnv(t)
	b := newBrowser(t)
	url := e.srv.URL + "/api/v1/projects/pay-hooks/git-hook"
	send := func(sig, delivery, event string) response {
		return b.do("POST", url, ghPayload, "X-Hub-Signature-256", sig, "X-GitHub-Delivery", delivery, "X-GitHub-Event", event)
	}
	r := send(ghSig, "d-1", "push")
	if r.status != http.StatusAccepted || !strings.Contains(string(r.body), "polling") {
		t.Fatalf("a signed push: %d %s", r.status, r.body)
	}
	// The same delivery again is a replay: answered, nothing done.
	if r := send(ghSig, "d-1", "push"); r.status != http.StatusAccepted || !strings.Contains(string(r.body), "already received") {
		t.Fatalf("a replay: %d %s", r.status, r.body)
	}
	if r := send(ghSig, "d-2", "issues"); !strings.Contains(string(r.body), "ignored") {
		t.Fatalf("an event that moves no ref: %s", r.body)
	}
	// Whoever cannot prove themselves learns nothing: a wrong signature,
	// a project without the hook, and one that does not exist look alike.
	wrong := send("sha256="+strings.Repeat("0", 64), "d-3", "push")
	other := b.do("POST", e.srv.URL+"/api/v1/projects/pay-api/git-hook", ghPayload, "X-Hub-Signature-256", ghSig)
	none := b.do("POST", e.srv.URL+"/api/v1/projects/nope/git-hook", ghPayload, "X-Hub-Signature-256", ghSig)
	for _, x := range []response{wrong, other, none} {
		if x.status != 404 || string(x.body) != string(wrong.body) {
			t.Fatalf("refusals differ: %d %s / %s", x.status, x.body, wrong.body)
		}
	}
	// GitLab.
	gl := e.srv.URL + "/api/v1/projects/pay-gitlab/git-hook"
	if r := b.do("POST", gl, "{}", "X-Gitlab-Token", "gitlab-token", "X-Gitlab-Event", "Push Hook", "X-Gitlab-Event-UUID", "u-1"); r.status != 202 {
		t.Fatalf("GitLab: %d %s", r.status, r.body)
	}
	if r := b.do("POST", gl, "{}", "X-Gitlab-Token", "nope"); r.status != 404 {
		t.Fatalf("GitLab with a wrong token: %d", r.status)
	}
	n := 0
	for _, a := range e.auditEntries(t) {
		if a.Action == "webhook.git" && a.Outcome == "ok" {
			n++
		}
		if a.Action == "webhook.git" && a.Outcome == "denied" && a.User == "" {
			t.Fatalf("a denial without who it claimed to be: %+v", a)
		}
	}
	if n != 2 {
		t.Fatalf("%d successful Git webhook calls audited", n)
	}
}

func sign(secret string, ts int64, body string) (string, string) {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.%s", ts, body)
	return strconv.FormatInt(ts, 10), "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestWebhookTriggerCalls(t *testing.T) {
	e := hooksEnv(t)
	b := newBrowser(t)
	base := e.srv.URL + "/api/v1/projects/pay-hooks/webhooks/"
	body := `{"release":{"version":"v42"},"target":"prod"}`

	r := b.do("POST", base+"deploy", body, "Authorization", "Bearer deploy-token", "Idempotency-Key", "k-1")
	if r.status != http.StatusCreated {
		t.Fatalf("token call: %d %s", r.status, r.body)
	}
	var run engine.RunSummary
	r.json(t, &run)
	if run.Event != "webhook" || run.Actor != "webhook:deploy" {
		t.Fatalf("run: %+v", run)
	}
	again := b.do("POST", base+"deploy", body, "Authorization", "Bearer deploy-token", "Idempotency-Key", "k-1")
	var same engine.RunSummary
	again.json(t, &same)
	if again.status != 200 || same.ID != run.ID || again.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("repeat: %d %s", again.status, again.body)
	}
	if r := b.do("POST", base+"deploy", `{"release":{"version":"v43"}}`, "Authorization", "Bearer deploy-token", "Idempotency-Key", "k-1"); r.status != 409 {
		t.Fatalf("the key with another body: %d %s", r.status, r.body)
	}
	// Values that do not fit are refused by name.
	r = b.do("POST", base+"deploy", `{"release":{"version":"latest"},"target":"qa"}`, "Authorization", "Bearer deploy-token")
	if r.status != 422 || !strings.Contains(string(r.body), `"version"`) || !strings.Contains(string(r.body), `"target"`) {
		t.Fatalf("bad values: %d %s", r.status, r.body)
	}
	// Wrong or missing proof, an unconfigured trigger and an unknown
	// project all look the same.
	refusals := []response{
		b.do("POST", base+"deploy", body, "Authorization", "Bearer wrong"),
		b.do("POST", base+"deploy", body),
		b.do("POST", base+"unconfigured", body, "Authorization", "Bearer deploy-token"),
		b.do("POST", e.srv.URL+"/api/v1/projects/nope/webhooks/deploy", body, "Authorization", "Bearer deploy-token"),
	}
	for _, x := range refusals {
		if x.status != 404 || string(x.body) != string(refusals[0].body) {
			t.Fatalf("refusals differ: %d %s", x.status, x.body)
		}
	}
	// Too large a body.
	if r := b.do("POST", base+"deploy", `{"x":"`+strings.Repeat("a", maxTriggerBody)+`"}`, "Authorization", "Bearer deploy-token"); r.status != 413 {
		t.Fatalf("large body: %d", r.status)
	}

	// Signed calls: accepted once, within five minutes.
	sbody := `{"version":"v7"}`
	now := e.clk.Now().Unix()
	ts, sig := sign("hmac-secret", now, sbody)
	if r := b.do("POST", base+"signed", sbody, "X-Launchway-Timestamp", ts, "X-Launchway-Signature", sig); r.status != 201 {
		t.Fatalf("signed: %d %s", r.status, r.body)
	}
	if r := b.do("POST", base+"signed", sbody, "X-Launchway-Timestamp", ts, "X-Launchway-Signature", sig); r.status != 409 {
		t.Fatalf("signed replay: %d %s", r.status, r.body)
	}
	ts, sig = sign("hmac-secret", now-int64(6*60), sbody)
	if r := b.do("POST", base+"signed", sbody, "X-Launchway-Timestamp", ts, "X-Launchway-Signature", sig); r.status != 404 {
		t.Fatalf("stale signature: %d", r.status)
	}
	ts, sig = sign("other-secret", now, sbody)
	if r := b.do("POST", base+"signed", sbody, "X-Launchway-Timestamp", ts, "X-Launchway-Signature", sig); r.status != 404 {
		t.Fatalf("wrong secret: %d", r.status)
	}
	// Audit: the runs, and the refusals.
	var ok, denied int
	for _, a := range e.auditEntries(t) {
		if a.Action == "webhook.run" && a.Outcome == "ok" {
			ok++
		}
		if a.Action == "webhook.run" && a.Outcome == "denied" {
			denied++
		}
	}
	if ok != 3 || denied != 6 {
		t.Fatalf("audit: %d ok, %d denied", ok, denied)
	}
	runs, _ := e.eng.ListRuns(engine.RunFilter{Project: "pay-hooks"})
	if len(runs) != 2 {
		t.Fatalf("%d runs", len(runs))
	}
}

// Many failed webhook calls from one address are refused for a while.
func TestWebhookFailuresAreLimited(t *testing.T) {
	e := hooksEnv(t)
	b := newBrowser(t)
	url := e.srv.URL + "/api/v1/projects/pay-hooks/webhooks/deploy"
	for range maxFailuresPerWindow {
		b.do("POST", url, "{}", "Authorization", "Bearer wrong")
	}
	if r := b.do("POST", url, `{"release":{"version":"v1"}}`, "Authorization", "Bearer deploy-token"); r.status != 429 {
		t.Fatalf("after many failures: %d", r.status)
	}
}

func TestSchedulesRoute(t *testing.T) {
	e := hooksEnv(t)
	otto := e.signIn(t, "otto")
	var v source.SchedulesView
	r := otto.get(e.srv.URL + "/api/v1/projects/pay-hooks/schedules")
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r.json(t, &v)
	if !v.Enabled || v.GitHook != "github" || v.Branch != "main" {
		t.Fatalf("view: %+v", v)
	}
}
