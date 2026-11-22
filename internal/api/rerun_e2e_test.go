package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// Who may re-run, what they see first, and what the audit trail keeps.
func TestRerunPermissionsAndAudit(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada, otto, vic, dana := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic"), e.signIn(t, "dana")
	orig := e.startRun(t, "pay-api")
	runURL := e.srv.URL + "/api/v1/runs/" + orig
	if r := ada.do(http.MethodPost, runURL+"/steps/gate/reject", `{"comment":"not today"}`); r.status != http.StatusOK {
		t.Fatalf("reject: %d %s", r.status, r.body)
	}
	waitRun(t, e.eng, orig)

	// Viewers may see what a re-run would do; the step's preview says so.
	var plan engine.RerunPlan
	if r := vic.get(runURL + "/rerun?from=gate"); r.status != http.StatusOK {
		t.Fatalf("plan as a viewer: %d %s", r.status, r.body)
	} else {
		r.json(t, &plan)
	}
	if !slices.Equal(plan.Run, []string{"gate", "after"}) || len(plan.Reuse) != 0 {
		t.Fatalf("plan %+v", plan)
	}
	if r := vic.get(runURL + "/rerun?from=after"); r.status != http.StatusConflict || !strings.Contains(string(r.body), "it needs gate (failed)") {
		t.Fatalf("plan from after: %d %s", r.status, r.body)
	}

	for _, c := range []struct {
		who  *browser
		body string
		want int
	}{
		{vic, `{"from":"gate"}`, http.StatusForbidden},  // a viewer
		{dana, `{"from":"gate"}`, http.StatusNotFound},  // another team's project
		{otto, `{}`, http.StatusBadRequest},             // which step?
		{otto, `{"from":"after"}`, http.StatusConflict}, // gate failed and comes before it
		{otto, `{"from":"gate"}`, http.StatusCreated},   // an operator
		{newBrowser(t), `{"from":"gate"}`, http.StatusUnauthorized},
	} {
		if r := c.who.do(http.MethodPost, runURL+"/rerun", c.body); r.status != c.want {
			t.Fatalf("%s: %d %s, want %d", c.body, r.status, r.body, c.want)
		}
	}
	d := mustRun(t, e.eng, orig)
	if len(d.Reruns) != 1 {
		t.Fatalf("re-runs: %+v", d.Reruns)
	}
	re := mustRun(t, e.eng, d.Reruns[0].ID)
	if re.RerunOf != orig || re.RerunFrom != "gate" || re.Actor != "otto" || re.SHA != d.SHA || re.Number != 2 {
		t.Fatalf("re-run %+v", re.RunSummary)
	}
	// otto asked for the re-run, so (self-approval being off here) he may not approve it.
	if r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/runs/"+re.ID+"/steps/gate/approve", ""); r.status != http.StatusForbidden {
		t.Fatalf("otto approving his own re-run: %d %s", r.status, r.body)
	}

	var got []string
	for _, a := range e.auditEntries(t) {
		if a.Action == "run.rerun" {
			got = append(got, a.User+" "+a.Project+" "+a.Target+" "+a.Outcome+": "+a.Detail)
		}
	}
	want := []string{
		"vic pay-api " + orig + " denied: you need the operator role on pay-api to do this",
		"dana pay-api " + orig + " denied: not found: run " + orig,
		"otto pay-api " + orig + " failed: say which step to re-run from: {\"from\": \"<step>\"}",
		"otto pay-api " + orig + " failed: cannot re-run from after: it needs gate (failed), which did not succeed in run #1, so there is nothing to reuse. Re-run from an earlier step, or resume the run",
		"otto pay-api " + orig + " ok: re-run of #1 from step gate: new Sortie #2 " + re.ID,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Re-running a draft run runs its unsaved text again, so it needs the right
// to start draft runs; the re-run is a draft too.
func TestRerunOfADraftNeedsTheDraftRight(t *testing.T) {
	e := setupAuth(t, envOpts{drafts: &auth.DraftRules{Default: auth.DraftRule{Allow: auth.Operator},
		Projects: map[string]auth.DraftRule{"pay-api": {Allow: auth.Admin}}}})
	ada := e.signIn(t, "ada")
	text := "pipeline: app\nsteps:\n  use:\n    image: none\n    secrets: { T: { store: env, key: K } }\n    run: echo\n"
	r := ada.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/draft-runs", jsonBody(t, map[string]string{"ref": "main", "pipeline": text}))
	var run engine.RunSummary
	r.json(t, &run)
	waitRun(t, e.eng, run.ID)

	otto := e.signIn(t, "otto") // an operator of pay-api, but not an admin
	if r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/runs/"+run.ID+"/rerun", `{"from":"use"}`); r.status != http.StatusForbidden ||
		!strings.Contains(string(r.body), "admin role on pay-api") {
		t.Fatalf("operator re-running an admin-only draft: %d %s", r.status, r.body)
	}
	r = ada.do(http.MethodPost, e.srv.URL+"/api/v1/runs/"+run.ID+"/rerun", `{"from":"use"}`)
	var re engine.RunSummary
	r.json(t, &re)
	if r.status != http.StatusCreated || !re.Draft || re.PipelineHash != run.PipelineHash || re.RerunOf != run.ID {
		t.Fatalf("admin re-running a draft: %d %+v", r.status, re)
	}
	for _, a := range e.auditEntries(t) {
		if a.Action == "run.rerun" && a.User == "ada" && !strings.Contains(a.Detail, "draft run, pipeline "+run.PipelineHash) {
			t.Fatalf("audit detail %q", a.Detail)
		}
	}
}

// On a real runner: step 3 of 4 fails because of something outside the
// workspace. Once that is put right, re-running from step 3 runs steps 3
// and 4 only, at the same commit, and step 4 still receives step 1's
// output, which was not produced again.
func TestRerunEndToEndOnTheHost(t *testing.T) {
	e := setup(t, 5*time.Second)
	e.runner(t, "r1")
	flag := filepath.Join(t.TempDir(), "service-is-up")
	id := e.start(t, `
pipeline: demo
steps:
  build:
    image: none
    run: |
      echo "building"
      echo "artifact=app-$LAUNCHWAY_RUN_NUMBER.tar" >> "$LAUNCHWAY_OUTPUTS"
  test:
    image: none
    needs: build
    run: echo "testing $LAUNCHWAY_NEEDS_BUILD_ARTIFACT"
  deploy:
    image: none
    needs: test
    env: { FLAG: "`+flag+`" }
    run: |
      test -f "$FLAG" || { echo "the service is down" >&2; exit 7; }
      echo "deployed=yes" >> "$LAUNCHWAY_OUTPUTS"
  smoke:
    image: none
    needs: [deploy, build]
    run: |
      echo "smoke-testing $LAUNCHWAY_NEEDS_BUILD_ARTIFACT"
      echo "saw=$LAUNCHWAY_NEEDS_BUILD_ARTIFACT" >> "$LAUNCHWAY_OUTPUTS"
`)
	d := e.wait(t, id, 20*time.Second)
	if d.Status != engine.RunFailed {
		t.Fatalf("first run: %s", d.Status)
	}
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(e.srv.URL+"/api/v1/runs/"+id+"/rerun", "application/json", strings.NewReader(`{"from":"deploy"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden { // no CSRF header: refused
		t.Fatalf("without the CSRF header: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/runs/"+id+"/rerun", strings.NewReader(`{"from":"deploy"}`))
	req.Header.Set(csrfHeader, "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var re engine.RunSummary
	decodeBody(t, resp, &re)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-run: %d", resp.StatusCode)
	}
	rd := e.wait(t, re.ID, 20*time.Second)
	if rd.Status != engine.RunSucceeded || rd.SHA != d.SHA {
		t.Fatalf("re-run %s", rd.Status)
	}
	ran := map[string]int{}
	for _, s := range rd.StepList {
		ran[s.ID] = s.Attempt
		lines, _, _ := e.eng.Logs(re.ID, s.ID, 0, 0, 0)
		if (s.ID == "build" || s.ID == "test") && (len(lines) != 0 || s.ReusedFrom != id || s.Attempt != 0) {
			t.Fatalf("%s ran in the re-run: %d log lines, reused from %q", s.ID, len(lines), s.ReusedFrom)
		}
	}
	if ran["deploy"] != 1 || ran["smoke"] != 1 {
		t.Fatalf("attempts in the re-run: %v", ran)
	}
	// Step 1's output, from run #1, reached step 4 in run #2.
	if out := outputsOf(t, rd, "smoke"); out["saw"] != "app-1.tar" {
		t.Fatalf("smoke saw %q", out["saw"])
	}
	// And its inputs say where that came from.
	for _, s := range rd.StepList {
		if s.ID != "smoke" {
			continue
		}
		in := s.Inputs[0].Inputs
		if !slices.ContainsFunc(in.Env, func(v engine.InputVar) bool {
			return v.Name == "LAUNCHWAY_NEEDS_BUILD_ARTIFACT" && v.Value == "app-1.tar" && v.From == engine.FromNeeds && v.Step == "build"
		}) {
			t.Fatalf("smoke's inputs: %+v", in.Env)
		}
	}
}

// A re-run uses each library at the commit the original run recorded, even
// after the library's tag has moved; a new run follows the tag.
func TestRerunKeepsLibraryCommitsAfterTheTagMoved(t *testing.T) {
	le := setupLibrary(t, &executor.Host{}, libApp("v1", "none"), nil)
	v1 := le.lib.Head()
	id := le.startRun(t, nil)
	if d := le.wait(t, id, 30*time.Second); d.Status != engine.RunSucceeded {
		t.Fatalf("run %s", d.Status)
	}
	tip := le.lib.Commit(map[string]string{"scripts/check.sh": testlib.CheckScript("2")}, "check.sh version 2")
	le.lib.Tag("v1", tip) // the tag moves

	req, _ := http.NewRequest(http.MethodPost, le.srv.URL+"/api/v1/runs/"+id+"/rerun", strings.NewReader(`{"from":"ci/run"}`))
	req.Header.Set(csrfHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var re engine.RunSummary
	decodeBody(t, resp, &re)
	d := le.wait(t, re.ID, 30*time.Second)
	if out := outputsOf(t, d, "ci/run"); out["version"] != "1" {
		t.Fatalf("the re-run ran check.sh version %q, want 1 (the recorded commit)", out["version"])
	}
	if l := d.Graph.Libraries[0]; l.Commit != v1 {
		t.Fatalf("the re-run's library: %+v", l)
	}
	if out := outputsOf(t, d, "after"); out["seen"] != "1" {
		t.Fatalf("after: %v", out)
	}
	fresh := le.startRun(t, nil)
	if out := outputsOf(t, le.wait(t, fresh, 30*time.Second), "ci/run"); out["version"] != "2" {
		t.Fatalf("a new run ran version %q, want 2 (where the tag is now)", out["version"])
	}
}

func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}
