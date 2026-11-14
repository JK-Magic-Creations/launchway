package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/fakeidp"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// jsonBody encodes v for a request body.
func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const waitingDraft = "pipeline: app\nsteps:\n  gate: {kind: approval, message: Ship it}\n"

// Who may start draft runs follows the drafts setting and each project's
// override, checked by the server for everyone; the project view says the
// same, so the UI can explain it.
func TestDraftPermissionsFollowTheSetting(t *testing.T) {
	people := []string{"vic", "otto", "dana", "ada"}
	for _, c := range []struct {
		name     string
		rules    *auth.DraftRules
		pay, web [4]int
	}{
		{"default: operators", nil, [4]int{403, 201, 404, 201}, [4]int{404, 404, 201, 201}},
		{"admins only", &auth.DraftRules{Default: auth.DraftRule{Allow: auth.Admin}},
			[4]int{403, 403, 404, 201}, [4]int{404, 404, 403, 201}},
		{"nobody", &auth.DraftRules{Default: auth.DraftRule{Allow: auth.NoRole}},
			[4]int{403, 403, 404, 403}, [4]int{404, 404, 403, 403}},
		{"per project", &auth.DraftRules{Default: auth.DraftRule{Allow: auth.Operator},
			Projects: map[string]auth.DraftRule{"pay-api": {Allow: auth.Admin}, "web": {Allow: auth.NoRole}}},
			[4]int{403, 403, 404, 201}, [4]int{404, 404, 403, 403}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := setupAuth(t, envOpts{drafts: c.rules})
			body := jsonBody(t, map[string]string{"ref": "main", "pipeline": waitingDraft})
			for i, user := range people {
				b := e.signIn(t, user)
				for project, want := range map[string]int{"pay-api": c.pay[i], "web": c.web[i]} {
					r := b.do(http.MethodPost, e.srv.URL+"/api/v1/projects/"+project+"/draft-runs", body)
					if r.status != want {
						t.Errorf("%s drafting %s: %d %s, want %d", user, project, r.status, r.body, want)
					}
					if want == 201 || want == 403 {
						var v projectView
						b.get(e.srv.URL+"/api/v1/projects/"+project).json(t, &v)
						if v.Editor.MayDraft != (want == 201) {
							t.Errorf("%s on %s: the project says may_draft=%v", user, project, v.Editor.MayDraft)
						}
					}
				}
			}
		})
	}

	// The setting holds with sign-in off too, where everyone is an admin.
	e := setup(t, time.Second)
	s := &Server{Engine: e.eng, Actor: "tester", Log: quiet(), Drafts: &auth.DraftRules{Default: auth.DraftRule{Allow: auth.NoRole}}}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := uiPost(srv.URL+"/api/v1/projects/any/draft-runs", "application/json", strings.NewReader(`{"ref":"main","pipeline":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("drafts: none without sign-in: %d", resp.StatusCode)
	}
}

// A draft run, like every change, needs the CSRF header with a session;
// the refusal is in the audit trail.
func TestDraftRunNeedsCSRF(t *testing.T) {
	e := setupAuth(t, envOpts{})
	otto := e.signIn(t, "otto")
	body := jsonBody(t, map[string]string{"ref": "main", "pipeline": waitingDraft})
	for _, path := range []string{"/draft-runs", "/pipeline"} {
		r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api"+path, body, csrfHeader, "")
		if r.status != http.StatusForbidden {
			t.Fatalf("%s without the CSRF header: %d", path, r.status)
		}
		r = otto.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api"+path, body, "Origin", "https://evil.example")
		if r.status != http.StatusForbidden {
			t.Fatalf("%s from another site: %d", path, r.status)
		}
	}
	if runs, _ := e.eng.ListRuns(engine.RunFilter{}); len(runs) != 0 {
		t.Fatalf("%d runs started", len(runs))
	}
	denied := 0
	for _, a := range e.auditEntries(t) {
		if (a.Action == "run.draft" || a.Action == "pipeline.save") && a.Outcome == "denied" && a.User == "otto" {
			denied++
		}
	}
	if denied != 4 {
		t.Fatalf("%d refusals recorded, want 4", denied)
	}
	// With a token, no header is needed.
	_, tok := otto.newToken(e.srv.URL, "script")
	if r := newBrowser(t).do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/draft-runs", body, append(bearer(tok), csrfHeader, "")...); r.status != http.StatusCreated {
		t.Fatalf("with a token: %d %s", r.status, r.body)
	}
}

// Saving from the API commits as the signed-in person, refuses text with
// problems (unless an admin forces it) and stale bases, and records the
// branch and commit, never the text, in the audit trail.
func TestSaveOverTheAPI(t *testing.T) {
	e := setupAuth(t, envOpts{})
	repo := filepath.Join(e.dir, "repos", "pay")
	otto := e.signIn(t, "otto")
	var f source.PipelineFile
	if r := otto.get(e.srv.URL + "/api/v1/projects/pay-api/pipeline?ref=main"); r.status != 200 {
		t.Fatalf("reading: %d %s", r.status, r.body)
	} else {
		r.json(t, &f)
	}
	if f.Text != gatedPipeline || f.SHA == "" || !f.Exists {
		t.Fatalf("file %+v", f)
	}
	const marker = "zebra-marker-7"
	text := strings.Replace(gatedPipeline, "echo shipped", "echo "+marker, 1)
	save := func(b *browser, body map[string]any) response {
		return b.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/pipeline", jsonBody(t, body))
	}
	r := save(otto, map[string]any{"base_ref": "main", "base_sha": f.SHA, "pipeline": text, "message": "Say what shipped"})
	if r.status != http.StatusCreated {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	var res source.SaveResult
	r.json(t, &res)
	if res.Branch != "launchway/otto/20261114-090000" || res.Direct || len(res.SHA) != 40 {
		t.Fatalf("result %+v", res)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := git("log", "-1", "--format=%an <%ae>|%cn|%s", res.Branch); got != fakeidp.DisplayName("otto")+" <otto@example.com>|Launchway|Say what shipped" {
		t.Fatalf("commit %q", got)
	}

	// Problems: refused with their lines; forcing needs an admin.
	bad := map[string]any{"base_ref": "main", "base_sha": f.SHA, "pipeline": "pipeline: app\nsteps:\n  a: {image: x}\n"}
	r = save(otto, bad)
	var problems struct {
		Error    string             `json:"error"`
		Problems []pipeline.Problem `json:"problems"`
	}
	r.json(t, &problems)
	if r.status != http.StatusUnprocessableEntity || len(problems.Problems) != 1 || problems.Problems[0].Line != 3 {
		t.Fatalf("invalid save: %d %s", r.status, r.body)
	}
	bad["force"] = true
	if r := save(otto, bad); r.status != http.StatusForbidden {
		t.Fatalf("operator forcing: %d %s", r.status, r.body)
	}
	if r := save(e.signIn(t, "ada"), bad); r.status != http.StatusCreated {
		t.Fatalf("admin forcing: %d %s", r.status, r.body)
	}
	// Viewers may read but not save; another team cannot see it at all.
	if r := save(e.signIn(t, "vic"), map[string]any{"base_ref": "main", "base_sha": f.SHA, "pipeline": text}); r.status != http.StatusForbidden {
		t.Fatalf("viewer saving: %d", r.status)
	}
	// web has no push settings: it cannot be saved to.
	dana := e.signIn(t, "dana")
	var wf source.PipelineFile
	dana.get(e.srv.URL+"/api/v1/projects/web/pipeline").json(t, &wf)
	if r := dana.do(http.MethodPost, e.srv.URL+"/api/v1/projects/web/pipeline",
		jsonBody(t, map[string]any{"base_ref": "main", "base_sha": wf.SHA, "pipeline": text})); r.status != http.StatusForbidden ||
		!strings.Contains(string(r.body), "no push: section") {
		t.Fatalf("saving web: %d %s", r.status, r.body)
	}

	// Someone pushes to main: a save from the old base is a conflict.
	os.WriteFile(filepath.Join(repo, "other.txt"), []byte("x"), 0o644)
	git("add", "-A")
	git("-c", "user.name=Someone", "-c", "user.email=s@example.com", "commit", "-q", "-m", "meanwhile")
	r = save(otto, map[string]any{"base_ref": "main", "base_sha": f.SHA, "pipeline": text})
	if r.status != http.StatusConflict || !strings.Contains(string(r.body), "has moved on") {
		t.Fatalf("stale save: %d %s", r.status, r.body)
	}

	entries := e.auditEntries(t)
	var saved *engine.AuditEntry
	for i, a := range entries {
		for _, field := range []string{a.Detail, a.Target, a.Project} {
			if strings.Contains(field, marker) || strings.Contains(field, "image: x") {
				t.Fatalf("pipeline text in the audit trail: %+v", a)
			}
		}
		if a.Action == "pipeline.save" && a.User == "otto" && a.Outcome == "ok" {
			saved = &entries[i]
		}
	}
	if saved == nil || saved.Target != res.Branch || saved.Project != "pay-api" || !strings.Contains(saved.Detail, res.SHA) ||
		!strings.Contains(saved.Detail, engine.DraftHash(text)) {
		t.Fatalf("audit entry %+v", saved)
	}
	outcomes := map[string]int{}
	for _, a := range entries {
		if a.Action == "pipeline.save" {
			outcomes[a.User+" "+a.Outcome]++
		}
	}
	want := map[string]int{"otto ok": 1, "otto failed": 2, "otto denied": 1, "ada ok": 1, "vic denied": 1, "dana denied": 1}
	if fmt.Sprint(outcomes) != fmt.Sprint(want) {
		t.Fatalf("save outcomes %v, want %v", outcomes, want)
	}
}

// countingStore counts lookups: for checking that a runner never asked.
type countingStore struct{ n atomic.Int32 }

func (c *countingStore) Get(context.Context, string, string) (string, error) {
	c.n.Add(1)
	return "never-handed-out", nil
}

// A draft run executes on a runner (the host executor), its Pipeline tab
// shows the draft text that ran, conditions and secrets follow the draft
// rules, and a failing step's last lines, masked, are in the run API.
func TestDraftRunEndToEnd(t *testing.T) {
	e := setupAuth(t, envOpts{drafts: &auth.DraftRules{Default: auth.DraftRule{Allow: auth.Operator},
		Projects: map[string]auth.DraftRule{"pay-api": {Allow: auth.Operator, Secrets: true}}}})
	dir := t.TempDir()
	const secretValue = "Sup3r-Secret-Value-123"
	os.WriteFile(filepath.Join(dir, "robot"), []byte(secretValue), 0o600)
	counted := &countingStore{}
	stores := secrets.NewResolver()
	stores.Add("files", secrets.File{Dir: dir}, nil, nil)
	stores.Add("count", counted, nil, nil)
	r := &runner.Runner{Client: runner.NewClient(e.srv.URL, "runner-secret"), Exec: &executor.Host{},
		Info: engine.RunnerInfo{ID: "r1", Name: "r1"}, DataDir: filepath.Join(dir, "runner"), Concurrency: 2, Log: quiet(), Secrets: stores}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	draft := `pipeline: app
steps:
  hello:
    description: Say hello from the draft
    image: none
    run: echo "draft says hi"
  deploy:
    image: none
    needs: hello
    when: { branch: main }
    run: echo should not run
  login:
    image: none
    needs: hello
    secrets:
      TOKEN: { store: files, key: robot }
    run: |
      echo "trying to log in"
      echo "error: login refused for $TOKEN" >&2
      echo "error: giving up" >&2
      exit 4
`
	otto := e.signIn(t, "otto")
	resp := otto.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/draft-runs", jsonBody(t, map[string]string{"ref": "main", "pipeline": draft}))
	if resp.status != http.StatusCreated {
		t.Fatalf("draft: %d %s", resp.status, resp.body)
	}
	var started engine.RunSummary
	resp.json(t, &started)
	if !started.Draft || started.Event != "draft" || started.Actor != "otto" {
		t.Fatalf("run %+v", started)
	}
	d := waitRun(t, e.eng, started.ID)
	var got engine.RunDetail
	otto.get(e.srv.URL+"/api/v1/runs/"+started.ID).json(t, &got)
	steps := map[string]engine.StepView{}
	for _, s := range got.StepList {
		steps[s.ID] = s
	}
	if d.Status != engine.RunFailed || steps["hello"].Status != "succeeded" || steps["deploy"].Status != "skipped" || steps["login"].Status != "failed" {
		t.Fatalf("run %s: %+v", d.Status, got.StepList)
	}
	if steps["hello"].Description != "Say hello from the draft" || !got.Draft || got.PipelineHash != engine.DraftHash(draft) {
		t.Fatalf("detail %+v", got.RunSummary)
	}
	login := steps["login"]
	if login.Excerpt != "error: login refused for ***\nerror: giving up" || login.Error != "exit code 4" {
		t.Fatalf("excerpt %q, error %q", login.Excerpt, login.Error)
	}
	var file struct {
		Text  string `json:"text"`
		Draft bool   `json:"draft"`
		Hash  string `json:"hash"`
	}
	otto.get(e.srv.URL+"/api/v1/runs/"+started.ID+"/pipeline-file").json(t, &file)
	if file.Text != draft || !file.Draft || file.Hash != got.PipelineHash {
		t.Fatalf("pipeline file %+v", file)
	}
	// The event log names the draft by its hash, without repeating the text.
	evs := otto.get(e.srv.URL + "/api/v1/runs/" + started.ID + "/events")
	if evs.status != 200 || strings.Contains(string(evs.body), "draft says hi") || !strings.Contains(string(evs.body), got.PipelineHash) {
		t.Fatalf("events: %d %s", evs.status, evs.body)
	}
	// A run from Git shows the file at its commit, not a draft.
	plain := e.startRun(t, "pay-api")
	otto.get(e.srv.URL+"/api/v1/runs/"+plain+"/pipeline-file").json(t, &file)
	if file.Text != gatedPipeline || file.Draft {
		t.Fatalf("plain run's file %+v", file)
	}

	// web keeps the default: drafts may not read secrets. The step fails
	// before any runner sees it, and the store is never asked.
	dana := e.signIn(t, "dana")
	resp = dana.do(http.MethodPost, e.srv.URL+"/api/v1/projects/web/draft-runs", jsonBody(t, map[string]string{"ref": "main",
		"pipeline": "pipeline: app\nsteps:\n  ok: {image: none, run: echo fine}\n  use:\n    image: none\n    secrets: { T: { store: count, key: k } }\n    run: echo \"$T\"\n"}))
	if resp.status != http.StatusCreated {
		t.Fatalf("web draft: %d %s", resp.status, resp.body)
	}
	resp.json(t, &started)
	d = waitRun(t, e.eng, started.ID)
	for _, s := range d.StepList {
		if s.ID == "use" && (s.Status != "failed" || !strings.Contains(s.Error, "draft runs of web may not read secrets") || s.Runner != "") {
			t.Fatalf("use %+v", s)
		}
		if s.ID == "ok" && s.Status != "succeeded" {
			t.Fatalf("ok %+v", s)
		}
	}
	if counted.n.Load() != 0 {
		t.Fatalf("the secret store was asked %d times", counted.n.Load())
	}

	// Resuming a draft runs its text again, so it needs the draft right:
	// vic, a viewer, cannot; and the audit trail names the text by hash.
	entries := e.auditEntries(t)
	found := false
	for _, a := range entries {
		if a.Action == "run.draft" && a.User == "otto" && a.Outcome == "ok" {
			found = a.Project == "pay-api" && strings.HasPrefix(a.Target, "run_") && strings.Contains(a.Detail, engine.DraftHash(draft))
		}
		if strings.Contains(a.Detail, "draft says hi") || strings.Contains(a.Detail, secretValue) {
			t.Fatalf("pipeline text in the audit trail: %+v", a)
		}
	}
	if !found {
		t.Fatalf("no audit entry for the draft run: %+v", entries)
	}
}

// Resuming a draft run needs the right to start one.
func TestResumingADraftNeedsTheDraftRight(t *testing.T) {
	// Operators may run drafts of web; only admins of pay-api.
	e := setupAuth(t, envOpts{drafts: &auth.DraftRules{Default: auth.DraftRule{Allow: auth.Operator},
		Projects: map[string]auth.DraftRule{"pay-api": {Allow: auth.Admin}}}})
	ada := e.signIn(t, "ada")
	// A step with secrets fails at once in a draft, so the run is failed.
	text := "pipeline: app\nsteps:\n  use:\n    image: none\n    secrets: { T: { store: env, key: K } }\n    run: echo\n"
	r := ada.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/draft-runs", jsonBody(t, map[string]string{"ref": "main", "pipeline": text}))
	var run engine.RunSummary
	r.json(t, &run)
	waitRun(t, e.eng, run.ID)
	otto := e.signIn(t, "otto") // an operator of pay-api, but not an admin
	if r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/runs/"+run.ID+"/resume", ""); r.status != http.StatusForbidden ||
		!strings.Contains(string(r.body), "admin role on pay-api") {
		t.Fatalf("operator resuming an admin-only draft: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPost, e.srv.URL+"/api/v1/runs/"+run.ID+"/resume", ""); r.status != http.StatusOK {
		t.Fatalf("admin resuming: %d %s", r.status, r.body)
	}
}

func waitRun(t *testing.T, eng *engine.Engine, id string) *engine.RunDetail {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		d, err := eng.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status.Terminal() {
			return d
		}
		time.Sleep(25 * time.Millisecond)
	}
	d, _ := eng.GetRun(id)
	t.Fatalf("run did not finish: %+v", d.StepList)
	return nil
}
