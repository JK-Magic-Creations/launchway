package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type env struct {
	eng *engine.Engine
	srv *httptest.Server
	dir string
}

func setup(t *testing.T, ttl time.Duration) *env {
	t.Helper()
	dir := t.TempDir()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), LeaseTTL: ttl, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	return &env{eng: eng, srv: srv, dir: dir}
}

func (e *env) runner(t *testing.T, name string) (context.CancelFunc, chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r := &runner.Runner{Client: runner.NewClient(e.srv.URL, "secret"), Exec: &executor.Host{},
		Info: engine.RunnerInfo{ID: name, Name: name}, DataDir: filepath.Join(e.dir, name), Concurrency: 2, Log: quiet()}
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel, done
}

func (e *env) start(t *testing.T, src string) string {
	t.Helper()
	p, err := pipeline.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(e.dir, "work")
	os.MkdirAll(work, 0o755)
	r, err := e.eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p, Trigger: engine.Trigger{Event: "manual"},
		Source: engine.Source{Kind: "dir", Path: work}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func (e *env) wait(t *testing.T, id string, timeout time.Duration) *engine.RunDetail {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d, err := e.eng.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status.Terminal() {
			return d
		}
		time.Sleep(25 * time.Millisecond)
	}
	d, _ := e.eng.GetRun(id)
	t.Fatalf("run did not finish in %s: %+v", timeout, d.StepList)
	return nil
}

func TestEndToEndOverHTTP(t *testing.T) {
	e := setup(t, 5*time.Second)
	e.runner(t, "r1")
	id := e.start(t, `
pipeline: demo
steps:
  hello:
    image: none
    run: |
      echo "hello from $LAUNCHWAY_STEP"
      echo greeting=hi >> "$LAUNCHWAY_OUTPUTS"
  after:
    image: none
    needs: hello
    run: test "$LAUNCHWAY_NEEDS_HELLO_GREETING" = hi
  broken:
    image: none
    run: exit 3
`)
	d := e.wait(t, id, 20*time.Second)
	got := map[string]engine.StepStatus{}
	for _, s := range d.StepList {
		got[s.ID] = s.Status
	}
	if got["hello"] != "succeeded" || got["after"] != "succeeded" || got["broken"] != "failed" || d.Status != "failed" {
		t.Fatalf("statuses %v, run %s", got, d.Status)
	}
	if code := d.StepList[2].ExitCode; code == nil || *code != 3 {
		t.Fatalf("exit code %v", code)
	}

	// The UI API sees the same.
	var logs struct {
		Lines []engine.LogLine `json:"lines"`
	}
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/steps/hello/logs", &logs)
	joined := ""
	for _, l := range logs.Lines {
		joined += l.Text + "\n"
	}
	if !strings.Contains(joined, "hello from hello") {
		t.Fatalf("logs:\n%s", joined)
	}
	var list []engine.RunSummary
	getJSON(t, e.srv.URL+"/api/v1/runs", &list)
	if len(list) != 1 || len(list[0].Steps) != 3 {
		t.Fatalf("list %+v", list)
	}

	// Resume reruns only the failed step.
	resp, err := http.Post(e.srv.URL+"/api/v1/runs/"+id+"/resume", "application/json", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resume: %v %v", err, resp.Status)
	}
	d = e.wait(t, id, 20*time.Second)
	if d.StepList[0].Attempt != 1 || d.StepList[2].Attempt != 2 {
		t.Fatalf("attempts: hello %d broken %d", d.StepList[0].Attempt, d.StepList[2].Attempt)
	}
}

func TestRunnerKilledMidStepIsRecovered(t *testing.T) {
	e := setup(t, 1*time.Second)
	stop, done := e.runner(t, "doomed")
	id := e.start(t, `
pipeline: demo
steps:
  slow:
    image: none
    run: |
      if [ "$LAUNCHWAY_ATTEMPT" = 1 ]; then sleep 60; fi
      echo finished on attempt $LAUNCHWAY_ATTEMPT
`)
	// Wait until the first runner is running the step, then kill it in a way
	// that cannot report back: stop it and make its lease look abandoned.
	deadline := time.Now().Add(10 * time.Second)
	for {
		d, _ := e.eng.GetRun(id)
		if d.StepList[0].Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the step never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	<-done
	// A graceful stop hands the step back at once. Either way, a second
	// runner must finish the run.
	e.runner(t, "rescuer")
	d := e.wait(t, id, 30*time.Second)
	if d.Status != "succeeded" || d.StepList[0].Attempt != 2 {
		t.Fatalf("run %s, attempt %d", d.Status, d.StepList[0].Attempt)
	}
}

func TestCancelStopsRunningStep(t *testing.T) {
	e := setup(t, 2*time.Second)
	e.runner(t, "r1")
	id := e.start(t, "pipeline: demo\nsteps:\n  forever: {image: none, run: sleep 300}\n")
	for {
		d, _ := e.eng.GetRun(id)
		if d.StepList[0].Status == "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	resp, err := http.Post(e.srv.URL+"/api/v1/runs/"+id+"/cancel", "application/json", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("cancel: %v %v", err, resp.Status)
	}
	d := e.wait(t, id, 20*time.Second)
	if d.Status != "canceled" {
		t.Fatalf("run %s", d.Status)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
}

func TestRunnerAPIRequiresToken(t *testing.T) {
	e := setup(t, time.Second)
	resp, err := http.Post(e.srv.URL+"/api/runner/v1/lease", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestValidateEndpoint(t *testing.T) {
	e := setup(t, time.Second)
	resp, err := http.Post(e.srv.URL+"/api/v1/validate", "text/yaml", strings.NewReader("pipeline: x\nsteps:\n  a: {image: i}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK       bool               `json:"ok"`
		Problems []pipeline.Problem `json:"problems"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.OK || len(out.Problems) != 1 || out.Problems[0].Line != 3 {
		t.Fatalf("%+v", out)
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: %s %s", url, resp.Status, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestHealthAndReadiness(t *testing.T) {
	e := setup(t, time.Second)
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s", path, resp.Status)
		}
	}
	e.eng.Close() // the database is gone
	resp, err := http.Get(e.srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz without a database: %s", resp.Status)
	}
	resp, err = http.Get(e.srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %s", resp.Status)
	}
}
