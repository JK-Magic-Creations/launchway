package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// post sends a JSON body (or none) and returns the status and the decoded
// error message, if any.
func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	resp, err := http.Post(url, "application/json", rd)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Error
}

// waitFor polls the run until cond holds.
func (e *env) waitFor(t *testing.T, id, what string, cond func(d *engine.RunDetail) bool) *engine.RunDetail {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		d, err := e.eng.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if cond(d) {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: run %s, steps %+v", what, d.Status, d.StepList)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func stepStatus(d *engine.RunDetail, id string) engine.StepStatus {
	for _, s := range d.StepList {
		if s.ID == id {
			return s.Status
		}
	}
	return ""
}

// A real runner runs the first step; the run then waits for a person, and
// the second step starts only once someone approves over HTTP.
func TestApprovalOverHTTP(t *testing.T) {
	e := setup(t, 5*time.Second)
	e.runner(t, "r1")
	id := e.start(t, `
pipeline: demo
steps:
  first:
    image: none
    run: echo first
  gate:
    kind: approval
    needs: first
    message: Carry on to the second step?
  second:
    image: none
    needs: gate
    run: echo "second ran"
`)
	waiting := func(d *engine.RunDetail) bool { return stepStatus(d, "gate") == engine.StepWaitingS }
	d := e.waitFor(t, id, "the approval to wait", waiting)
	if stepStatus(d, "first") != engine.StepSucceeded || d.Status != engine.RunRunning {
		t.Fatalf("run %s, steps %+v", d.Status, d.StepList)
	}
	var list []engine.Approval
	getJSON(t, e.srv.URL+"/api/v1/approvals", &list)
	if len(list) != 1 || list[0].RunID != id || list[0].StepID != "gate" || list[0].Message != "Carry on to the second step?" {
		t.Fatalf("approvals %+v", list)
	}
	// The runner is idle, but the second step must not start.
	time.Sleep(500 * time.Millisecond)
	if d, err := e.eng.GetRun(id); err != nil || stepStatus(d, "second") != engine.StepPending {
		t.Fatalf("second started before anyone approved: %v %+v", err, d.StepList)
	}

	// Rejecting skips the second step and fails the run.
	base := e.srv.URL + "/api/v1/runs/" + id + "/steps/gate/"
	if code, msg := post(t, base+"reject", `{"comment": "not yet"}`); code != http.StatusOK {
		t.Fatalf("reject: %d %s", code, msg)
	}
	d = e.wait(t, id, 20*time.Second)
	if d.Status != engine.RunFailed || stepStatus(d, "second") != engine.StepSkippedS || d.StepList[1].Error != "rejected by tester: not yet" {
		t.Fatalf("after rejecting: run %s, steps %+v", d.Status, d.StepList)
	}
	if code, msg := post(t, base+"approve", ""); code != http.StatusConflict || !strings.Contains(msg, "not waiting") {
		t.Fatalf("approving a rejected step: %d %q", code, msg)
	}

	// Resuming makes it wait again; approving lets the second step run.
	if code, msg := post(t, e.srv.URL+"/api/v1/runs/"+id+"/resume", ""); code != http.StatusOK {
		t.Fatalf("resume: %d %s", code, msg)
	}
	e.waitFor(t, id, "the approval to wait again", waiting)
	if code, msg := post(t, base+"approve", `{"comment": "ok now"}`); code != http.StatusOK {
		t.Fatalf("approve: %d %s", code, msg)
	}
	d = e.wait(t, id, 20*time.Second)
	if d.Status != engine.RunSucceeded {
		t.Fatalf("run %s, steps %+v", d.Status, d.StepList)
	}
	if g := d.StepList[1]; g.DecidedBy != "tester" || g.Comment != "ok now" || g.DecidedAt == 0 || g.Kind != "approval" {
		t.Fatalf("gate %+v", g)
	}
	var logs struct {
		Lines []engine.LogLine `json:"lines"`
	}
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/steps/second/logs", &logs)
	if !strings.Contains(joinText(logs.Lines), "second ran") {
		t.Fatalf("second step logs %+v", logs.Lines)
	}
	getJSON(t, e.srv.URL+"/api/v1/approvals", &list)
	if len(list) != 0 {
		t.Fatalf("approvals left %+v", list)
	}
	if code, _ := post(t, e.srv.URL+"/api/v1/runs/run_nope/steps/gate/approve", ""); code != http.StatusNotFound {
		t.Fatalf("approving in a missing run: %d", code)
	}
	if code, _ := post(t, base+"approve", "{not json"); code != http.StatusBadRequest {
		t.Fatalf("a malformed body: %d", code)
	}
}

func joinText(lines []engine.LogLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

// With self-approval turned off, the person who started a run is told
// plainly why they cannot approve it.
func TestSelfApprovalRefusedOverHTTP(t *testing.T) {
	no := false
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet(), AllowSelfApproval: &no})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{Engine: eng, Actor: "dana", Log: quiet()}).Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	p, err := pipeline.Parse([]byte("pipeline: demo\nsteps:\n  ok: {kind: approval, message: \"Ship it?\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p, Trigger: engine.Trigger{Event: "manual", Actor: "dana"}})
	if err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/v1/runs/" + r.ID + "/steps/ok/"
	code, msg := post(t, base+"approve", "")
	if code != http.StatusForbidden || msg != "dana started this run, so someone else must approve it (you may still reject it)" {
		t.Fatalf("self-approval: %d %q", code, msg)
	}
	if code, msg := post(t, base+"reject", ""); code != http.StatusOK {
		t.Fatalf("rejecting your own run: %d %q", code, msg)
	}
}
