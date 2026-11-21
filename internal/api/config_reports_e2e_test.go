package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// A step keeps the report files a configuration tool wrote as the artifact
// launchway-config; the server reads them, stores the parsed and redacted
// form once, and serves the report, the recap and the hosts to run again.
// The report files here are real ones, written by the callback against
// systemd containers (internal/cac/ansible/testdata).
func TestConfigReportsFromAKeptArtifact(t *testing.T) {
	e := setupStore(t)
	repo := filepath.Join(e.dir, "repo")
	for from, to := range map[string]string{
		"../cac/ansible/testdata/one-failed.json":  ".launchway/ansible/report.json",
		"../cac/tfplan/testdata/plan-replace.json": ".launchway/tofu/plan.json",
	} {
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(filepath.Join(repo, to)), 0o755)
		os.WriteFile(filepath.Join(repo, to), b, 0o644)
	}
	os.WriteFile(filepath.Join(repo, ".launchway/tofu/meta.json"), []byte(`{"tool":"tofu","workspace":"prod","environment":"production","exit_code":2,"state_serial":"7"}`), 0o644)
	src := `
pipeline: demo
steps:
  configure:
    image: none
    run: echo "the tool ran earlier; its files are in the checkout"
    artifacts:
      - {name: launchway-config, paths: [.launchway/ansible, .launchway/tofu]}
`
	gitRepo(t, repo, src)
	sha, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	p, err := pipeline.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	e.runner(t, "r1")
	r, err := e.eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p,
		Trigger: engine.Trigger{Event: "manual", SHA: strings.TrimSpace(string(sha)), Ref: "refs/heads/main", Branch: "main"},
		Source:  engine.Source{Kind: "git", URL: repo}})
	if err != nil {
		t.Fatal(err)
	}
	if d := e.wait(t, r.ID, 60*time.Second); d.Status != "succeeded" {
		t.Fatalf("run %s", d.Status)
	}

	// The recap is on the launch line of the run.
	var lines []LaunchLine
	getJSON(t, e.srv.URL+"/api/v1/launch-lines?run="+r.ID, &lines)
	var conf *Station
	for i := range lines[0].Stations {
		if lines[0].Stations[i].Key == "configuration" {
			conf = &lines[0].Stations[i]
		}
	}
	if conf == nil || conf.State != "failed" || !strings.Contains(conf.Detail, "1 failed") || !strings.Contains(conf.Detail, "to destroy") {
		t.Fatalf("the launch line has no recap: %+v", lines[0].Stations)
	}

	var list struct {
		Reports []engine.CacReport `json:"reports"`
	}
	getJSON(t, e.srv.URL+"/api/v1/runs/"+r.ID+"/config-reports", &list)
	if len(list.Reports) != 2 {
		t.Fatalf("reports: %+v", list.Reports)
	}
	by := map[string]engine.CacReport{}
	for _, rep := range list.Reports {
		by[rep.Kind] = rep
	}
	if a := by["ansible"]; a.Recap != "2 hosts: 1 changed, 0 ok, 1 failed" && a.Recap != "2 hosts: 0 changed, 1 ok, 1 failed" || a.Status != "failed" {
		t.Fatalf("ansible: %+v", a)
	}
	if p := by["plan"]; !strings.Contains(p.Recap, "to destroy") || p.Environment != "production" {
		t.Fatalf("plan: %+v", p)
	}

	var one struct {
		Summary  map[string]any `json:"summary"`
		Document struct {
			Plays []struct {
				Tasks []struct {
					Name string `json:"name"`
				} `json:"tasks"`
			} `json:"plays"`
			Failure struct{ Host, Msg string } `json:"failure"`
		} `json:"document"`
	}
	getJSON(t, e.srv.URL+"/api/v1/runs/"+r.ID+"/config-reports/configure?kind=ansible", &one)
	if one.Document.Failure.Host != "lw-cactest-2" || !strings.Contains(one.Document.Failure.Msg, "told to fail") {
		t.Fatalf("the failed task is not first: %+v", one.Document.Failure)
	}
	if hosts, _ := one.Summary["failed_hosts"].([]any); len(hosts) != 1 || hosts[0] != "lw-cactest-2" {
		t.Fatalf("hosts to run again: %v", one.Summary["failed_hosts"])
	}
	// Reading again stores nothing twice.
	getJSON(t, e.srv.URL+"/api/v1/runs/"+r.ID+"/config-reports", &list)
	if len(list.Reports) != 2 {
		t.Fatalf("a second read stored more: %d", len(list.Reports))
	}
	b, _ := json.Marshal(one)
	if strings.Contains(string(b), "CANARY-s3cr3t") {
		t.Fatal("the canary is in the served report")
	}
}
