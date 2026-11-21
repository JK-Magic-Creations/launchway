package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/runner"
)

// leased starts a run of one step and hands its lease to a client.
func leased(t *testing.T, e *env) (*runner.Client, *engine.Job, string) {
	t.Helper()
	run := e.start(t, "pipeline: demo\nsteps:\n  one:\n    image: none\n    run: echo hi\n    artifacts:\n      - {name: empty, paths: [a.txt]}\n")
	c := runner.NewClient(e.srv.URL, "secret")
	job, err := c.Lease(context.Background(), engine.RunnerInfo{ID: "r1", Name: "r1", Features: engine.FeatureArtifacts}, 0)
	if err != nil || job == nil {
		t.Fatalf("lease: %v %v", job, err)
	}
	if _, err := c.Start(context.Background(), job.LeaseID); err != nil {
		t.Fatal(err)
	}
	return c, job, run
}

func logTexts(t *testing.T, e *env, run, step string) []string {
	t.Helper()
	lines, _, err := e.eng.Logs(run, step, 1, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

func batch(texts ...string) []engine.LogLine {
	var out []engine.LogLine
	for _, s := range texts {
		out = append(out, engine.LogLine{Stream: "stdout", Text: s})
	}
	return out
}

// A log batch carries its position; a batch sent again (the answer was
// lost) adds nothing, a partly repeated one adds only its new tail, and one
// that skips lines is refused with the count the server has.
func TestLogBatchesAreAppliedOnce(t *testing.T) {
	e := setup(t, 30*time.Second)
	c, job, run := leased(t, e)
	ctx := context.Background()
	if err := c.LogsAt(ctx, job.LeaseID, 0, batch("a", "b", "c")); err != nil {
		t.Fatal(err)
	}
	if err := c.LogsAt(ctx, job.LeaseID, 0, batch("a", "b", "c")); err != nil {
		t.Fatalf("a repeated batch must succeed: %v", err)
	}
	if err := c.LogsAt(ctx, job.LeaseID, 1, batch("b", "c", "d", "e")); err != nil {
		t.Fatalf("a partly repeated batch: %v", err)
	}
	var gap *runner.LogGap
	if err := c.LogsAt(ctx, job.LeaseID, 9, batch("x")); !errors.As(err, &gap) || gap.Have != 5 {
		t.Fatalf("a batch that skips lines: %v", err)
	}
	got := logTexts(t, e, run, "one")
	want := []string{"a", "b", "c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("log %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("log %v, want %v", got, want)
		}
	}
	// A runner that does not send a position still appends, as before.
	if err := c.Logs(ctx, job.LeaseID, batch("f")); err != nil {
		t.Fatal(err)
	}
	if n := len(logTexts(t, e, run, "one")); n != 6 {
		t.Fatalf("%d lines", n)
	}
}

// The end of a step attempt is applied once: sending the result again (the
// server restarted before the runner saw the answer) is a success, not a
// lost lease, and records nothing more.
func TestStepResultIsAppliedOnce(t *testing.T) {
	e := setup(t, 30*time.Second)
	c, job, run := leased(t, e)
	ctx := context.Background()
	res := engine.Result{Outcome: "succeeded", Outputs: map[string]string{"k": "v"}}
	if err := c.Finish(ctx, job.LeaseID, res); err != nil {
		t.Fatal(err)
	}
	n := countEvents(t, e, run, engine.StepFinished)
	if err := c.Finish(ctx, job.LeaseID, res); err != nil {
		t.Fatalf("the repeated result must get the first answer: %v", err)
	}
	if m := countEvents(t, e, run, engine.StepFinished); m != n || n != 1 {
		t.Fatalf("step.finished recorded %d then %d times", n, m)
	}
	// A different lease that never existed is still refused.
	if err := c.Finish(ctx, "no-such-lease", res); !errors.Is(err, runner.ErrLeaseLost) {
		t.Fatalf("unknown lease: %v", err)
	}
}

func countEvents(t *testing.T, e *env, run string, typ engine.EventType) int {
	t.Helper()
	evs, err := e.eng.Events(run)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// Declaring and committing an artifact twice gives the first answer both
// times and keeps one artifact.
func TestArtifactBeginAndCommitAreAppliedOnce(t *testing.T) {
	e := setupStore(t)
	c, job, _ := leased(t, e)
	ctx := context.Background()
	in := engine.BeginArtifact{Name: "empty", Files: []engine.ArtifactFileInfo{{Path: "a.txt", Size: 0, SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Mode: 0o644}}}
	first, err := c.BeginArtifact(ctx, job.LeaseID, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.BeginArtifact(ctx, job.LeaseID, in)
	if err != nil || again.ID != first.ID {
		t.Fatalf("a repeated begin must return the first answer: %v %v", again, err)
	}
	if err := c.CommitArtifact(ctx, job.LeaseID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.CommitArtifact(ctx, job.LeaseID, first.ID); err != nil {
		t.Fatalf("a repeated commit: %v", err)
	}
}
