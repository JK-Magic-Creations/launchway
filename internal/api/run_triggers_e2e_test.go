package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// A run of one project ending starts a run of another, when the upstream
// allows it: refused until allowed, inputs mapped from the result, filtered
// and refused results explained, a cycle refused, and exactly one start.
func TestRunResultTriggerAcrossProjects(t *testing.T) {
	dir := t.TempDir()
	e := setupAuth(t, envOpts{projects: []source.Project{
		{Name: "pay-inputs", URL: gitRepo(t, filepath.Join(dir, "inputs"), inputsPipeline), Poll: time.Hour},
	}})
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	ctx := t.Context()
	e.api.Projects.ProcessResults(ctx) // the cursor starts here: history starts nothing
	down := e.srv.URL + "/api/v1/projects/pay-inputs"
	up := e.srv.URL + "/api/v1/projects/pay-api"
	trig := `{"name":"after-api","upstream_project":"pay-api","when":"any","filter":"run.number >= 1 && run.status != \"running\"","mapping":{"version":"\"v7\"","replicas":"run.number"}}`

	// Not allowed yet: refused at save, with what to do.
	if r := ada.do("POST", down+"/run-triggers", trig); r.status != 409 || !strings.Contains(string(r.body), "has not allowed pay-inputs") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := vic.do("POST", up+"/run-trigger-allows", `{"downstream_project":"pay-inputs"}`); r.status != 403 {
		t.Fatalf("a viewer allowed: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", up+"/run-trigger-allows", `{"downstream_project":"nothing"}`); r.status != 404 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do("POST", up+"/run-trigger-allows", `{"downstream_project":"pay-inputs"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do("POST", down+"/run-triggers", strings.Replace(trig, `run.number >= 1`, `run.number >=`, 1)); r.status != 400 {
		t.Fatalf("a filter that cannot be read was saved: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", down+"/run-triggers", trig); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A cycle: pay-api may not trigger on pay-inputs now.
	ada.do("POST", down+"/run-trigger-allows", `{"downstream_project":"pay-api"}`)
	if r := ada.do("POST", up+"/run-triggers", `{"name":"loop","upstream_project":"pay-inputs","when":"any"}`); r.status != 409 || !strings.Contains(string(r.body), "cycle") {
		t.Fatalf("%d %s", r.status, r.body)
	}

	// The upstream run ends; the trigger starts one run downstream, once.
	api1 := e.startRun(t, "pay-api")
	if err := e.eng.Cancel(api1, "ada"); err != nil {
		t.Fatal(err)
	}
	e.api.Projects.ProcessResults(ctx)
	e.api.Projects.ProcessResults(ctx)
	runs, _ := e.eng.ListRuns(engine.RunFilter{Project: "pay-inputs", Limit: 10})
	if len(runs) != 1 {
		t.Fatalf("%d downstream runs, want 1", len(runs))
	}
	var d engine.RunDetail
	ada.get(e.srv.URL+"/api/v1/runs/"+runs[0].ID).json(t, &d)
	if d.TriggerDetail == nil || d.TriggerDetail.Inputs["version"] != "v7" || d.TriggerDetail.Inputs["replicas"] != "1" || !strings.HasPrefix(d.Actor, "result:pay-api#1") {
		t.Fatalf("%+v %q", d.TriggerDetail, d.Actor)
	}
	evs, _ := e.eng.EventLog(engine.EventFilter{Project: "pay-inputs", Source: engine.SourceRunResult})
	if len(evs) != 1 || evs[0].Decisions[0].Decision != engine.DecStarted || evs[0].SourceName != "pay-api" || !strings.Contains(evs[0].Summary, "Run #1 of pay-api ended: canceled") {
		t.Fatalf("%+v", evs)
	}
	// Only the run's address and status crossed, never a secret.
	if strings.Contains(string(evs[0].Payload), "secret") {
		t.Fatalf("%s", evs[0].Payload)
	}

	// A filter that is false: recorded, nothing started.
	ada.do("DELETE", down+"/run-triggers/after-api", "")
	r := ada.do("POST", down+"/run-triggers", `{"name":"only-success","upstream_project":"pay-api","when":"succeeded","mapping":{"version":"\"v1\""}}`)
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	api2 := e.startRun(t, "pay-api")
	e.eng.Cancel(api2, "ada")
	e.api.Projects.ProcessResults(ctx)
	evs, _ = e.eng.EventLog(engine.EventFilter{Project: "pay-inputs", Decision: engine.DecFiltered})
	if len(evs) != 1 || !strings.Contains(evs[0].Decisions[0].Reason, "the result was canceled, and this trigger waits for succeeded") {
		t.Fatalf("%+v", evs)
	}
	// Permission withdrawn: refused with the reason, nothing started.
	ada.do("DELETE", down+"/run-triggers/only-success", "")
	ada.do("POST", down+"/run-triggers", `{"name":"again","upstream_project":"pay-api","when":"any","mapping":{"version":"\"v2\""}}`)
	if r := ada.do("DELETE", up+"/run-trigger-allows/pay-inputs", ""); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	api3 := e.startRun(t, "pay-api")
	e.eng.Cancel(api3, "ada")
	e.api.Projects.ProcessResults(ctx)
	evs, _ = e.eng.EventLog(engine.EventFilter{Project: "pay-inputs", Decision: engine.DecRefused})
	if len(evs) != 1 || !strings.Contains(evs[0].Decisions[0].Reason, "has not allowed pay-inputs") {
		t.Fatalf("%+v", evs)
	}
	if runs, _ = e.eng.ListRuns(engine.RunFilter{Project: "pay-inputs", Limit: 10}); len(runs) != 1 {
		t.Fatalf("%d downstream runs, want still 1", len(runs))
	}
	var list []engine.RunTrigger
	ada.get(down+"/run-triggers").json(t, &list)
	if len(list) != 1 || list[0].Name != "again" {
		t.Fatalf("%+v", list)
	}
}
