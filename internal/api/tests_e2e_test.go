package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// A failing test step on a real runner (the host executor): the runner
// collects its reports from the workspace although it failed, skips what is
// not a regular file inside the workspace, and the server stores and serves
// the results.
func TestTestReportsEndToEnd(t *testing.T) {
	e := setup(t, 5*time.Second)
	e.runner(t, "r1")
	secret := filepath.Join(e.dir, "outside.xml")
	if err := os.WriteFile(secret, []byte(`<testsuite name="leaked"><testcase classname="secret" name="leaked"/></testsuite>`), 0o644); err != nil {
		t.Fatal(err)
	}
	id := e.start(t, `
pipeline: demo
steps:
  test:
    image: none
    run: |
      mkdir -p reports
      cat > reports/unit.xml <<'XML'
      <testsuites><testsuite name="unit">
        <testcase classname="calc" name="adds" time="0.002"/>
        <testcase classname="calc" name="divides" time="0.010"><failure message="division by zero">at calc.go:12</failure></testcase>
        <testcase classname="calc" name="later" time="0"><skipped/></testcase>
      </testsuite></testsuites>
      XML
      printf '{"Action":"run","Package":"x","Test":"TestA"}\n{"Action":"pass","Package":"x","Test":"TestA","Elapsed":0.5}\n{"Action":"pass","Package":"x","Elapsed":0.6}\n' > reports/go.json
      ln -sf `+secret+` reports/link.xml
      mkfifo reports/pipe.xml
      exit 1
    reports:
      tests: [reports/*.xml, reports/go.json, nothing/*.xml]
`)
	d := e.wait(t, id, 30*time.Second)
	if d.Status != "failed" || d.StepList[0].Status != "failed" {
		t.Fatalf("run %s, step %s", d.Status, d.StepList[0].Status)
	}
	var rt engine.RunTests
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/tests", &rt)
	if rt.Totals == nil || rt.Totals.Total != 4 || rt.Totals.Failed != 1 || rt.Totals.Skipped != 1 || len(rt.Steps) != 1 {
		t.Fatalf("totals: %+v", rt.Totals)
	}
	s := rt.Steps[0]
	if len(s.Files) != 2 || s.Files[0].Path != "reports/go.json" || s.Files[1].Path != "reports/unit.xml" {
		t.Fatalf("files: %+v", s.Files)
	}
	if !strings.Contains(strings.Join(s.Notes, "\n"), "nothing/*.xml matched no files") {
		t.Fatalf("notes: %v", s.Notes)
	}
	var page engine.CasePage
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/tests/cases?status=failed", &page)
	if len(page.Cases) != 1 || page.Cases[0].Name != "divides" || page.Cases[0].Message != "division by zero" {
		t.Fatalf("failed cases: %+v", page.Cases)
	}
	var one engine.TestCase
	getJSON(t, fmt.Sprintf("%s/api/v1/runs/%s/tests/cases/%d", e.srv.URL, id, page.Cases[0].ID), &one)
	if one.Details != "at calc.go:12" {
		t.Fatalf("the full result: %+v", one)
	}
	var all engine.CasePage
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/tests/cases?q=leaked", &all)
	if len(all.Cases) != 0 {
		t.Fatal("a file outside the workspace was read through a symbolic link")
	}
	var logs struct {
		Lines []engine.LogLine `json:"lines"`
	}
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/steps/test/logs", &logs)
	var text strings.Builder
	for _, l := range logs.Lines {
		if l.Stream == "system" {
			text.WriteString(l.Text + "\n")
		}
	}
	if !strings.Contains(text.String(), "Test reports: collected 2 files") || !strings.Contains(text.String(), "nothing/*.xml matched no files") {
		t.Fatalf("the step's system log:\n%s", text.String())
	}
	var list []engine.RunSummary
	getJSON(t, e.srv.URL+"/api/v1/runs", &list)
	if list[0].Tests == nil || list[0].Tests.Failed != 1 {
		t.Fatalf("totals in the list: %+v", list[0].Tests)
	}
	var overview engine.TestOverview
	getJSON(t, e.srv.URL+"/api/v1/projects/demo/tests", &overview)
	if len(overview.Runs) != 1 || len(overview.Failing) != 1 {
		t.Fatalf("overview: %+v", overview)
	}
	var hist engine.TestHistory
	getJSON(t, e.srv.URL+"/api/v1/projects/demo/tests/history?key="+page.Cases[0].Key, &hist)
	if len(hist.Results) != 1 || hist.Results[0].Status != "failed" {
		t.Fatalf("history: %+v", hist)
	}
	for _, bad := range []string{"/tests/cases?status=odd", "/tests/cases?limit=x", "/tests/cases/abc", "/tests/cases/999999"} {
		resp, err := http.Get(e.srv.URL + "/api/v1/runs/" + id + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
}

// A result larger than the server accepts is refused with 413 before it is
// read; the runner then reports the outcome without its files.
func TestOversizedResultsAreRefused(t *testing.T) {
	e := setup(t, 5*time.Second)
	limit := e.eng.ResultLimit()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/runner/v1/finish", io.LimitReader(zeros{}, limit+1))
	req.ContentLength = limit + 1
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized result: %d", resp.StatusCode)
	}
	// Without a length given, the limit holds while reading.
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte(`{"lease_id":"x","result":{"outcome":"succeeded","tests":{"files":[{"path":"a","data":"`))
		io.Copy(pw, io.LimitReader(as{}, limit))
		pw.Close()
	}()
	req, _ = http.NewRequest(http.MethodPost, e.srv.URL+"/api/runner/v1/finish", pr)
	req.Header.Set("Authorization", "Bearer secret")
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized result without a length: %d", resp.StatusCode)
	}

	// A server in front that refuses the first result with files, as one
	// configured with a lower limit than the server that handed out the
	// step would.
	var refused atomic.Bool
	inner := e.srv.Config.Handler
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runner/v1/finish" {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"data"`)) && refused.CompareAndSwap(false, true) {
				http.Error(w, `{"error":"too large"}`, http.StatusRequestEntityTooLarge)
				return
			}
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	r := e.newRunner("r1")
	r.Client.Server = front.URL
	e.startRunner(t, r)
	id := e.start(t, `
pipeline: demo
steps:
  test:
    image: none
    run: |
      mkdir -p reports
      echo '<testsuite name="s"><testcase classname="c" name="n"/></testsuite>' > reports/a.xml
      exit 2
    reports: {tests: reports/a.xml}
`)
	d := e.wait(t, id, 30*time.Second)
	if !refused.Load() || d.StepList[0].Status != "failed" || d.StepList[0].ExitCode == nil || *d.StepList[0].ExitCode != 2 {
		t.Fatalf("refused %v, step %+v", refused.Load(), d.StepList[0])
	}
	rt, _ := e.eng.RunTests(id)
	if len(rt.Steps) != 1 || rt.Steps[0].Total != 0 || !strings.Contains(strings.Join(rt.Steps[0].Notes, " "), "refused the report files") {
		t.Fatalf("after the refusal: %+v", rt.Steps)
	}
}

type zeros struct{}

func (zeros) Read(b []byte) (int, error) { clear(b); return len(b), nil }

type as struct{}

func (as) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 'A'
	}
	return len(b), nil
}

// Test results follow the run's and the project's access: a viewer of the
// project reads them, someone who may not see it gets exactly what they
// would for something that does not exist.
func TestTestResultsFollowAccess(t *testing.T) {
	e := setupAuth(t, envOpts{})
	src := "pipeline: app\nsteps:\n  test: {image: none, run: t, reports: {tests: r.xml}}\n"
	run := func(project string) (string, int64, string) {
		p, err := pipeline.Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.eng.CreateRun(engine.NewRun{Project: project, Pipeline: p, Trigger: engine.Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main"},
			Source: engine.Source{Kind: "dir", Path: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		j, err := e.eng.Lease(context.Background(), engine.RunnerInfo{ID: "r"}, 0)
		if err != nil || j == nil {
			t.Fatalf("lease: %v", err)
		}
		e.eng.Start(j.LeaseID)
		data := []byte(`<testsuite name="s"><testcase classname="c" name="n"><failure message="m">trace</failure></testcase></testsuite>`)
		if err := e.eng.Finish(j.LeaseID, engine.Result{Outcome: "failed", Tests: &engine.CollectedReports{Files: []engine.ReportFile{{Path: "r.xml", Data: data}}}}); err != nil {
			t.Fatal(err)
		}
		page, err := e.eng.RunTestCases(r.ID, engine.CaseQuery{})
		if err != nil || len(page.Cases) != 1 {
			t.Fatalf("cases: %+v %v", page, err)
		}
		return r.ID, page.Cases[0].ID, page.Cases[0].Key
	}
	pay, payCase, key := run("pay-api")
	web, webCase, _ := run("web")

	vic := e.signIn(t, "vic") // viewer of pay-*
	for _, path := range []string{
		"/api/v1/runs/" + pay + "/tests",
		"/api/v1/runs/" + pay + "/tests/cases?status=failed",
		fmt.Sprintf("/api/v1/runs/%s/tests/cases/%d", pay, payCase),
		"/api/v1/projects/pay-api/tests",
		"/api/v1/projects/pay-api/tests/history?key=" + key,
	} {
		if r := vic.get(e.srv.URL + path); r.status != http.StatusOK {
			t.Errorf("vic reading %s: %d %s", path, r.status, r.body)
		}
	}
	// Another team's run, or one of its results reached through a run vic
	// may see, look exactly like ones that do not exist.
	for path, wantMsg := range map[string]string{
		"/api/v1/runs/" + web + "/tests":                            "not found: run " + web,
		fmt.Sprintf("/api/v1/runs/%s/tests/cases/%d", web, webCase): "not found: run " + web,
		fmt.Sprintf("/api/v1/runs/%s/tests/cases/%d", pay, webCase): fmt.Sprintf("not found: test result %d", webCase),
		"/api/v1/projects/web/tests":                                "not found: project web",
		"/api/v1/projects/web/tests/history?key=" + key:             "not found: project web",
	} {
		r := vic.get(e.srv.URL + path)
		var body struct{ Error string }
		json.Unmarshal(r.body, &body)
		if r.status != http.StatusNotFound || body.Error != wantMsg {
			t.Errorf("vic reading %s: %d %q, want 404 %q", path, r.status, body.Error, wantMsg)
		}
	}
	missing := vic.get(fmt.Sprintf("%s/api/v1/runs/%s/tests/cases/%d", e.srv.URL, pay, webCase+1000))
	if missing.status != http.StatusNotFound {
		t.Errorf("a result that does not exist: %d", missing.status)
	}
	// Reading changes nothing, so nothing goes in the audit trail.
	for _, a := range e.auditEntries(t) {
		if strings.Contains(a.Action, "test") {
			t.Errorf("audit entry for reading tests: %+v", a)
		}
	}
}
