package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

var (
	armRunner = RunnerInfo{ID: "arm", Name: "arm", Features: FeatureArtifacts, Labels: []string{"arm64"}}
	amdRunner = RunnerInfo{ID: "amd", Name: "amd", Features: FeatureArtifacts, Labels: []string{"amd64"}}
	anyRunner = RunnerInfo{ID: "any", Name: "any", Features: FeatureArtifacts}
)

const twoPlatforms = `
pipeline: multi
steps:
  prepare: {image: x, run: prep}
  build:
    needs: prepare
    for_each: { arch: [amd64, arm64] }
    runner: { labels: [ { each: arch } ] }
    image: x
    run: go build
    artifacts: [{ name: bin, paths: [out] }]
  package:
    needs: build
    image: x
    run: tar
    fetch: [ { from: build, name: bin, into: dist } ]
`

// The members of a loop run on the runners that match them, each in a
// workspace of its own; the shared steps stay with the runner that holds the
// run, which the members never take over; a runner without the label never
// receives a member.
func TestStepsWithTheirOwnRunnerAndWorkspace(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := createBound(t, e, twoPlatforms, nil, Trigger{Event: "manual"})
	j := mustLease(t, e, anyRunner)
	if j.StepID != "prepare" {
		t.Fatalf("%s", j.StepID)
	}
	ok(t, e, j, nil)
	// The holder of the run is anyRunner; it gets neither member.
	if got := leaseAs(t, e, anyRunner); got != nil {
		t.Fatalf("a runner without the label got %s", got.StepID)
	}
	arm := mustLease(t, e, armRunner)
	amd := mustLease(t, e, amdRunner)
	if arm.StepID != "build[arch=arm64]" || amd.StepID != "build[arch=amd64]" {
		t.Fatalf("members: arm %s, amd %s", arm.StepID, amd.StepID)
	}
	if arm.Workspace == "" || arm.Workspace == amd.Workspace || !strings.HasPrefix(arm.Workspace, "own-") {
		t.Fatalf("workspaces %q %q", arm.Workspace, amd.Workspace)
	}
	if st, _ := e.State(id); st.Runner != anyRunner.ID {
		t.Fatalf("the run's runner moved to %s", st.Runner)
	}
	if s := stepOf(t, e, id, arm.StepID); s.Runner != "arm" || s.Workspace != arm.Workspace || s.RunnerSelector == nil {
		t.Fatalf("view %+v", s)
	}
	// A retry gets the same workspace.
	keep(t, e, arm.LeaseID, "bin", []upFile{{"out", []byte("arm"), 0o755}})
	keep(t, e, amd.LeaseID, "bin", []upFile{{"out", []byte("amd"), 0o755}})
	ok(t, e, arm, nil)
	ok(t, e, amd, nil)
	p := mustLease(t, e, anyRunner)
	if p.StepID != "package" || len(p.Fetch) != 2 || p.Workspace == arm.Workspace {
		t.Fatalf("package %s %+v ws %q", p.StepID, p.Fetch, p.Workspace)
	}
	ok(t, e, p, nil)
	want(t, statuses(t, e, id), "run", "succeeded")
	if ownWorkspace(id, arm.StepID) != arm.Workspace {
		t.Fatal("the workspace is derived from the run and the step")
	}
}

// A step whose label no runner has waits, says why, and does not hold back
// the steps that do not need it.
func TestStepWaitsForALabelNoRunnerHas(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := createBound(t, e, `
pipeline: p
steps:
  odd: {image: x, run: o, runner: { labels: [riscv] }}
  plain: {image: x, run: p}
`, nil, Trigger{Event: "manual"})
	j := mustLease(t, e, anyRunner)
	if j.StepID != "plain" {
		t.Fatalf("%s", j.StepID)
	}
	ok(t, e, j, nil)
	if got := leaseAs(t, e, anyRunner); got != nil {
		t.Fatalf("got %s", got.StepID)
	}
	s := stepOf(t, e, id, "odd")
	if s.Status != StepQueuedS || s.WaitingFor != "waiting for a runner with labels riscv" {
		t.Fatalf("%+v", s)
	}
	// A runner with the label takes it.
	if j := leaseAs(t, e, RunnerInfo{ID: "rv", Name: "rv", Labels: []string{"riscv"}}); j == nil || j.StepID != "odd" {
		t.Fatalf("%+v", j)
	}
}

// A pool that does not serve the project makes the pipeline invalid when the
// run is created.
func TestStepRunnerPoolMustServeTheProject(t *testing.T) {
	e, _ := newEngine(t)
	_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, "pipeline: p\nsteps:\n  a: {image: x, run: y, runner: { pool: gpu }}\n"),
		Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err == nil || !strings.Contains(err.Error(), "no runner pool gpu") {
		t.Fatalf("%v", err)
	}
}

// A server that does not know schema 27 leaves such runs alone.
func TestOlderServerLeavesStepRunnerRunsAlone(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	cur := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	id := createBound(t, cur, twoPlatforms, nil, Trigger{Event: "manual"})
	cur.Close()
	old := mustOpenAs(t, addr, clk, schemaFlow, &syncBuffer{})
	none(t, old)
	if d, err := old.GetRun(id); err != nil || !d.NeedsNewerServer {
		t.Fatalf("%+v %v", d, err)
	}
	_ = time.Second
}
