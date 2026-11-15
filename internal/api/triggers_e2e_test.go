package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// The pipeline of pay-inputs: typed inputs, a loop over a list input, and
// an approval so runs stay alive without a runner.
const inputsPipeline = `
pipeline: app
inputs:
  environment: { type: choice, options: [dev, prod], default: dev, description: Where to deploy }
  version: { pattern: "^v[0-9]+$", required: true }
  dry_run: { type: boolean, default: true }
  replicas: { type: number, min: 1, max: 20, default: 2 }
  systems: { type: list, default: [debian] }
steps:
  gate: {kind: approval, message: "Ship it?"}
  test:
    needs: gate
    for_each: { os: { input: systems } }
    image: none
    run: echo test
`

func inputsEnv(t *testing.T) *authEnv {
	t.Helper()
	dir := t.TempDir()
	return setupAuth(t, envOpts{projects: []source.Project{
		{Name: "pay-inputs", URL: gitRepo(t, filepath.Join(dir, "inputs"), inputsPipeline), Poll: time.Hour},
	}})
}

func TestStartRunWithInputs(t *testing.T) {
	e := inputsEnv(t)
	otto := e.signIn(t, "otto")
	base := e.srv.URL + "/api/v1/projects/pay-inputs"

	var info source.InputsInfo
	r := otto.get(base + "/inputs?ref=main")
	if r.status != 200 {
		t.Fatalf("inputs: %d %s", r.status, r.body)
	}
	r.json(t, &info)
	if len(info.Inputs) != 5 || info.Inputs[0].Name != "environment" || info.Inputs[0].Description != "Where to deploy" || info.SHA == "" {
		t.Fatalf("inputs: %+v", info)
	}

	// Typed JSON values, checked and recorded.
	r = otto.do(http.MethodPost, base+"/runs", `{"ref":"main","inputs":{"environment":"prod","version":"v12","dry_run":false,"replicas":5,"systems":["debian","alpine"]}}`)
	if r.status != http.StatusCreated {
		t.Fatalf("start: %d %s", r.status, r.body)
	}
	var run engine.RunSummary
	r.json(t, &run)
	var d engine.RunDetail
	otto.get(e.srv.URL + "/api/v1/runs/" + run.ID).json(t, &d)
	want := map[string]string{"environment": "prod", "version": "v12", "dry_run": "false", "replicas": "5", "systems": `["debian","alpine"]`}
	for k, v := range want {
		if d.TriggerDetail == nil || d.TriggerDetail.Inputs[k] != v {
			t.Fatalf("recorded %s: %+v", k, d.TriggerDetail)
		}
	}
	if len(d.StepList) != 3 || d.StepList[2].ID != "test[os=alpine]" {
		t.Fatalf("steps: %+v", d.StepList)
	}
	// The audit trail says which values the run was started with.
	found := false
	for _, a := range e.auditEntries(t) {
		if a.Action == "run.start" && a.Outcome == "ok" && strings.Contains(a.Detail, "inputs dry_run=false") {
			found = true
		}
	}
	if !found {
		t.Fatal("no audit entry with the inputs")
	}

	// Values that do not fit are refused, each under its name; the UI
	// shows them by the field.
	r = otto.do(http.MethodPost, base+"/runs", `{"ref":"main","inputs":{"environment":"qa","version":"12","replicas":"99","systems":[{"x":1}],"colour":"red"}}`)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad values: %d %s", r.status, r.body)
	}
	var bad struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	r.json(t, &bad)
	if bad.Fields["systems"] == "" {
		t.Fatalf("a list of objects: %+v", bad)
	}
	r = otto.do(http.MethodPost, base+"/runs", `{"ref":"main","inputs":{"environment":"qa","version":"12","replicas":"99","colour":"red"}}`)
	r.json(t, &bad)
	for _, k := range []string{"environment", "version", "replicas", "colour"} {
		if bad.Fields[k] == "" {
			t.Fatalf("no problem for %s: %+v", k, bad)
		}
	}
	// A required input without a default must be given.
	r = otto.do(http.MethodPost, base+"/runs", `{"ref":"main"}`)
	r.json(t, &bad)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(bad.Fields["version"], "required") {
		t.Fatalf("missing version: %d %+v", r.status, bad)
	}
	// A viewer may read the inputs but not start a run.
	vic := e.signIn(t, "vic")
	if r := vic.get(base + "/inputs?ref=main"); r.status != 200 {
		t.Fatalf("viewer reading inputs: %d", r.status)
	}
	if r := vic.do(http.MethodPost, base+"/runs", `{"ref":"main","inputs":{"version":"v1"}}`); r.status != http.StatusForbidden {
		t.Fatalf("viewer starting: %d", r.status)
	}
	// A ref that does not exist.
	if r := otto.get(base + "/inputs?ref=nope"); r.status != http.StatusNotFound {
		t.Fatalf("unknown ref: %d %s", r.status, r.body)
	}
}
