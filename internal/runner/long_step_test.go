package runner_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A step may run for longer than the time its result is given to be delivered in. Once the runner talked to
// the server during a step on a context that ended five minutes after the lease was taken: from the sixth
// minute every heartbeat and every log upload failed, the server took the step back, and a step longer than
// about ten minutes ran again for ever. Five minutes cannot be waited for, so the time is shortened to one
// second, the lease to three, and the step runs for six: it must still be on its first attempt, with all its
// lines, when it ends.
func TestAStepLongerThanTheResultDeadlineKeepsItsLeaseAndItsLogs(t *testing.T) {
	defer runner.SetResultWithin(time.Second)()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), LeaseTTL: 3 * time.Second, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&api.Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet}).Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })

	dir := t.TempDir()
	r := &runner.Runner{Client: runner.NewClient(srv.URL, "secret"), Exec: &executor.Host{},
		Info: engine.RunnerInfo{ID: "long", Name: "long"}, DataDir: filepath.Join(dir, "long"), Concurrency: 1, Log: quiet}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	p, err := pipeline.Parse([]byte("pipeline: long\nsteps:\n  slow:\n    image: x\n    run: |\n      echo begin\n      sleep 6\n      echo end\n"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p, Trigger: engine.Trigger{Event: "manual"}, Source: engine.Source{Kind: "dir", Path: dir}})
	if err != nil {
		t.Fatal(err)
	}
	var d *engine.RunDetail
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
		if d, err = eng.GetRun(run.ID); err != nil {
			t.Fatal(err)
		}
		if d.Status.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run did not end: %+v", d.StepList)
		}
	}
	if d.Status != engine.RunSucceeded {
		t.Fatalf("the run %s: %+v", d.Status, d.StepList)
	}
	for _, s := range d.StepList {
		if s.ID == "slow" && s.Attempt != 1 {
			t.Errorf("the step ran %d times: its lease was lost while it ran", s.Attempt)
		}
	}
	lines, _, err := eng.Logs(run.ID, "slow", 0, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text + "\n")
	}
	if !strings.Contains(b.String(), "begin") || !strings.Contains(b.String(), "end") {
		t.Errorf("the step's log is incomplete: %q", b.String())
	}
}
