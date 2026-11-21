package api

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/debrief"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

func fetch(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// A run on a real runner whose only failures are quarantined by a file in
// the workspace ends as succeeded, the Debrief shows them loudly, every
// export works, and the quarantine file is not parsed as a test report.
func TestDebriefEndToEnd(t *testing.T) {
	e := setup(t, 5*time.Second)
	e.runner(t, "r1")
	id := e.start(t, strings.ReplaceAll(`
pipeline: demo
steps:
  test:
    image: none
    run: |
      mkdir -p reports .launchway
      cat > .launchway/quarantine.yml <<'YML'
      quarantine:
        - test: "calc::divides"
          owner: ana
          expires: EXPIRY
          reason: division by zero is known
      YML
      cat > reports/unit.xml <<'XML'
      <testsuites><testsuite name="unit">
        <testcase classname="calc" name="adds" time="0.002"/>
        <testcase classname="calc" name="divides" time="0.010"><failure message="expected:&lt;1&gt; but was:&lt;0&gt;">at calc.go:12</failure></testcase>
      </testsuite></testsuites>
      XML
      exit 1
    reports:
      tests: [reports/*.xml]
`, "EXPIRY", time.Now().AddDate(0, 6, 0).Format("2006-01-02")))
	d := e.wait(t, id, 30*time.Second)
	if d.Status != "succeeded" {
		var logs struct {
			Lines []engine.LogLine `json:"lines"`
		}
		getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/steps/test/logs", &logs)
		for _, l := range logs.Lines {
			t.Log(l.Stream, l.Text)
		}
		var rt engine.RunTests
		getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/tests", &rt)
		b, _ := json.Marshal(rt.Steps)
		t.Log(string(b))
		t.Fatalf("a run whose only failure is quarantined is %s", d.Status)
	}
	var rt engine.RunTests
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/tests", &rt)
	if len(rt.Steps) != 1 || len(rt.Steps[0].Files) != 1 || rt.Steps[0].Files[0].Path != "reports/unit.xml" {
		t.Fatalf("the quarantine file must not be listed as a test report: %+v", rt.Steps)
	}
	var rep debrief.Report
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/debrief", &rep)
	if rep.Summary.Total != 2 || rep.Summary.Quarantined != 1 || len(rep.Failures) != 0 || len(rep.Quarantined) != 1 || rep.Quarantined[0].Owner != "ana" {
		t.Fatalf("debrief: %+v", rep)
	}
	for format, want := range map[string]string{"html": "Quarantined tests", "pdf": "%PDF-", "json": `"quarantined"`, "junit": "<testsuites"} {
		resp, body := fetch(t, e.srv.URL+"/api/v1/runs/"+id+"/debrief/export?format="+format)
		if resp.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Errorf("%s export: %d, lacks %q", format, resp.StatusCode, want)
		}
		if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "debrief-demo-1.") {
			t.Errorf("%s export: Content-Disposition %q", format, cd)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s export is not nosniff", format)
		}
		if format == "junit" {
			if err := xml.Unmarshal(body, new(struct{})); err != nil {
				t.Errorf("junit export is not XML: %v", err)
			}
			if !bytes.Contains(body, []byte("quarantined")) {
				t.Error("the merged JUnit lost the quarantine")
			}
		}
	}
	if resp, _ := fetch(t, e.srv.URL+"/api/v1/runs/"+id+"/debrief/export?format=docx"); resp.StatusCode != 400 {
		t.Errorf("an unknown format: %d", resp.StatusCode)
	}
	resp, body := fetch(t, e.srv.URL+"/api/v1/runs/"+id+"/debrief/summary")
	if resp.StatusCode != 200 || !strings.Contains(string(body), "### Tests") || !strings.Contains(resp.Header.Get("Content-Type"), "markdown") {
		t.Errorf("summary: %d %s", resp.StatusCode, body)
	}
	var fl engine.FlakyList
	getJSON(t, e.srv.URL+"/api/v1/projects/demo/tests/flaky", &fl)
	if fl.Rules == "" || fl.Tests == nil {
		t.Errorf("flaky: %+v", fl)
	}
	var tl engine.TimingList
	getJSON(t, e.srv.URL+"/api/v1/projects/demo/tests/timings", &tl)
	if len(tl.Tests) != 2 {
		t.Errorf("timings: %+v", tl)
	}
	var sh engine.TestShards
	getJSON(t, e.srv.URL+"/api/v1/projects/demo/tests/shards?count=2", &sh)
	if sh.Tests != 2 || len(sh.Shards) != 2 {
		t.Errorf("shards: %+v", sh)
	}
	if resp, _ := fetch(t, e.srv.URL+"/api/v1/projects/demo/tests/shards?count=0"); resp.StatusCode != 400 {
		t.Errorf("0 shards: %d", resp.StatusCode)
	}
	if resp, _ := fetch(t, e.srv.URL+"/api/v1/runs/run_nope/debrief"); resp.StatusCode != 404 {
		t.Errorf("an unknown run: %d", resp.StatusCode)
	}
	_ = json.Marshal
}
