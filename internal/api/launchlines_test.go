package api

import (
	"net/http"
	"time"

	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func stepOf(id, kind string, status engine.StepStatus, pos int) engine.StepView {
	return engine.StepView{ID: id, Kind: kind, Status: status, Position: pos}
}

func stationByKey(l LaunchLine, key string) *Station {
	for i := range l.Stations {
		if l.Stations[i].Key == key {
			return &l.Stations[i]
		}
	}
	return nil
}

func TestLaunchLineStationsFromSteps(t *testing.T) {
	test := stepOf("unit-tests", "", engine.StepFailed, 1)
	test.Error = "TestRefund expected 410 got 409\nmore"
	d := &engine.RunDetail{StepList: []engine.StepView{
		stepOf("lint", "", engine.StepSucceeded, 0), test,
		stepOf("build", "image", engine.StepPending, 2),
		func() engine.StepView {
			s := stepOf("ship-dev", "deploy", engine.StepPending, 3)
			s.Environment = "dev"
			return s
		}(),
	}}
	d.SHA = "0123456789abcdef"
	l := launchLineOf(d)
	want := []string{"commit", "checks", "tests", "package", "env:dev", "verified"}
	if len(l.Stations) != len(want) {
		t.Fatalf("stations %+v, want keys %v", l.Stations, want)
	}
	for i, k := range want {
		if l.Stations[i].Key != k {
			t.Errorf("station %d is %s, want %s", i, l.Stations[i].Key, k)
		}
	}
	if s := stationByKey(l, "tests"); s.State != stationFailed || s.Detail != "TestRefund expected 410 got 409" {
		t.Errorf("tests station %+v", s)
	}
	if l.Tone != "bad" || l.Now != "Stopped at tests. TestRefund expected 410 got 409" {
		t.Errorf("tone %q now %q", l.Tone, l.Now)
	}
	if s := stationByKey(l, "commit"); s.Detail != "01234567" {
		t.Errorf("commit detail %q", s.Detail)
	}
}

func TestLaunchLineWaitsSayWhy(t *testing.T) {
	ap := stepOf("approve-prod", "approval", engine.StepWaitingS, 2)
	ap.Message = "Release to production?"
	ap.Environment = "prod"
	dep := stepOf("ship-prod", "deploy", engine.StepWaitingS, 3)
	dep.Environment = "prod"
	dep.WaitKind = "lock"
	d := &engine.RunDetail{StepList: []engine.StepView{stepOf("ci", "", engine.StepSucceeded, 0), ap, dep}}
	l := launchLineOf(d)
	prod := stationByKey(l, "env:prod")
	if prod == nil || prod.State != stationWaiting || prod.Detail != "Waiting for a decision: Release to production?" {
		t.Fatalf("prod station %+v", prod)
	}
	if l.Tone != "wait" {
		t.Errorf("tone %q", l.Tone)
	}
	// A deploy that waits for its environment says so, not just "waiting".
	d.StepList[1].Status = engine.StepSucceeded
	l = launchLineOf(d)
	if p := stationByKey(l, "env:prod"); p.Detail != "Waiting for another deployment to leave prod" {
		t.Errorf("detail %q", p.Detail)
	}
}

func TestLaunchLineUsesDeploymentDetailAndVerified(t *testing.T) {
	ship := stepOf("ship", "deploy", engine.StepSucceeded, 1)
	ship.Environment = "staging"
	d := &engine.RunDetail{
		StepList:    []engine.StepView{stepOf("ci", "", engine.StepSucceeded, 0), ship},
		Deployments: []engine.DeploymentView{{Step: "ship", Phase: "rollout_progressing", Detail: "2 of 4 new copies are ready", Version: "1.4.0"}},
	}
	l := launchLineOf(d)
	if s := stationByKey(l, "env:staging"); s.State != stationRunning || s.Detail != "2 of 4 new copies are ready" {
		t.Errorf("staging %+v", s)
	}
	d.Deployments[0].Phase = "healthy"
	l = launchLineOf(d)
	if v := stationByKey(l, "verified"); v.State != stationDone {
		t.Errorf("verified %+v", v)
	}
	if l.Tone != "ok" {
		t.Errorf("tone %q now %q", l.Tone, l.Now)
	}
}

// Over HTTP: a run that waits for an approval is a line whose approval station
// waits and says what for.
func TestLaunchLinesOverHTTP(t *testing.T) {
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
    message: Carry on?
`)
	e.waitFor(t, id, "the approval to wait", func(d *engine.RunDetail) bool { return stepStatus(d, "gate") == engine.StepWaitingS })
	var lines []LaunchLine
	getJSON(t, e.srv.URL+"/api/v1/launch-lines?limit=5", &lines)
	if len(lines) != 1 || lines[0].Run != id {
		t.Fatalf("lines %+v", lines)
	}
	l := lines[0]
	if l.Tone != "wait" || l.Now != "Approval: Waiting for a decision: Carry on?" {
		t.Errorf("tone %q now %q stations %+v", l.Tone, l.Now, l.Stations)
	}
	// One run by its ID, which the pull request page uses for its latest run; an unknown one is a 404.
	var one []LaunchLine
	getJSON(t, e.srv.URL+"/api/v1/launch-lines?run="+id, &one)
	if len(one) != 1 || one[0].Run != id || one[0].Tone != "wait" {
		t.Errorf("the line of one run: %+v", one)
	}
	if resp, err := http.Get(e.srv.URL + "/api/v1/launch-lines?run=run_nope"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown run: %v %v", resp, err)
	}
}

func TestLaunchLineStepsAfterAnApprovalAreTheRelease(t *testing.T) {
	gate := stepOf("gate", "approval", engine.StepWaitingS, 2)
	pub := stepOf("publish", "", engine.StepPending, 3)
	pub.Needs = []string{"gate"}
	pkg := stepOf("package", "", engine.StepPending, 4)
	fail := stepOf("unit-tests", "", engine.StepFailed, 1)
	fail.Error = "exit code 1"
	d := &engine.RunDetail{StepList: []engine.StepView{stepOf("lint", "", engine.StepSucceeded, 0), fail, gate, pub, pkg}}
	l := launchLineOf(d)
	if s := stationByKey(l, "checks"); s == nil || s.State != stationDone {
		t.Errorf("checks %+v: the step after the approval is not a check", s)
	}
	if s := stationByKey(l, "release"); s == nil || s.State != stationIdle {
		t.Errorf("release %+v", s)
	}
	if s := stationByKey(l, "package"); s == nil || s.State != stationIdle {
		t.Errorf("package %+v", s)
	}
	if s := stationByKey(l, "tests"); s.Detail != "unit-tests ended with exit code 1" {
		t.Errorf("tests detail %q", s.Detail)
	}
}

// A station that stands for several steps says how many can run together and how each is doing.
func TestLaunchLineStationSaysWhatRunsTogether(t *testing.T) {
	d := &engine.RunDetail{
		Levels: [][]string{{"vet"}, {"lint-go", "lint-web", "audit"}, {"unit-tests"}},
		StepList: []engine.StepView{
			stepOf("vet", "", engine.StepSucceeded, 0),
			stepOf("lint-go", "", engine.StepSucceeded, 1), stepOf("lint-web", "", engine.StepSucceeded, 2), stepOf("audit", "", engine.StepRunning, 3),
			stepOf("unit-tests", "", engine.StepPending, 4),
		},
	}
	l := launchLineOf(d)
	checks := stationByKey(l, "checks")
	if checks == nil || checks.Parallel == nil {
		t.Fatalf("checks station %+v", checks)
	}
	p := checks.Parallel
	if p.Total != 4 || p.Together != 3 || p.Done != 3 || p.Running != 1 || len(p.Steps) != 4 || p.Steps[3].State != stationRunning {
		t.Errorf("parallel %+v", p)
	}
	if tests := stationByKey(l, "tests"); tests.Parallel != nil {
		t.Errorf("a one-step station is not parallel: %+v", tests.Parallel)
	}
}

// A matrix counts as one thing on the line: "3 of 4 passed".
func TestLaunchLineRollsAMatrixUp(t *testing.T) {
	bad := stepOf("test[go=1.26]", "", engine.StepFailed, 2)
	bad.Error = "exit code 1"
	d := &engine.RunDetail{
		StepList: []engine.StepView{
			stepOf("lint", "", engine.StepSucceeded, 0),
			stepOf("test[go=1.25]", "", engine.StepSucceeded, 1), bad,
			stepOf("test[go=1.27]", "", engine.StepSucceeded, 3), stepOf("test[go=1.28]", "", engine.StepSucceeded, 4),
		},
		Graph: &pipeline.Pipeline{Loops: []*pipeline.Loop{{ID: "test", Keys: []string{"go"}, Members: []pipeline.LoopMember{
			{ID: "test[go=1.25]"}, {ID: "test[go=1.26]"}, {ID: "test[go=1.27]"}, {ID: "test[go=1.28]"}}}}},
	}
	l := launchLineOf(d)
	if s := stationByKey(l, "tests"); s == nil || s.State != stationFailed || s.Detail != "3 of 4 passed: test[go=1.26] ended with exit code 1" {
		t.Fatalf("tests station %+v", s)
	}
	for i := range d.StepList {
		d.StepList[i].Status, d.StepList[i].Error = engine.StepSucceeded, ""
	}
	if s := stationByKey(launchLineOf(d), "tests"); s == nil || s.State != stationDone || s.Detail != "4 of 4 passed" {
		t.Fatalf("tests station %+v", s)
	}
}

// A step that is queued is never a running one: the station holds, with the reason, and the run says so.
func TestLaunchLineQueuedStepIsHoldingNotRunning(t *testing.T) {
	lost := stepOf("unit-tests", "", engine.StepQueuedS, 1)
	lost.Attempt, lost.FailureCause = 3, "infrastructure"
	queued := stepOf("unit-tests", "", engine.StepQueuedS, 1)
	room := stepOf("unit-tests", "", engine.StepQueuedS, 1)
	room.WaitingFor = "The cluster is full"
	running := stepOf("unit-tests", "", engine.StepRunning, 1)
	for name, c := range map[string]struct {
		step       engine.StepView
		state, now string
	}{
		"queued":  {queued, stationHolding, "Tests: Holding: waiting for a runner to pick up unit-tests"},
		"room":    {room, stationHolding, "Tests: Holding: The cluster is full"},
		"lost":    {lost, stationHolding, "Tests: Holding: retrying on another runner, attempt 3, after the last one stopped responding"},
		"running": {running, stationRunning, "Tests: unit-tests is running"},
	} {
		d := &engine.RunDetail{StepList: []engine.StepView{stepOf("lint", "", engine.StepSucceeded, 0), c.step}}
		d.Status = engine.RunRunning
		l := launchLineOf(d)
		if s := stationByKey(l, "tests"); s.State != c.state {
			t.Errorf("%s: station state %q, want %q", name, s.State, c.state)
		}
		if l.Now != c.now {
			t.Errorf("%s: now %q, want %q", name, l.Now, c.now)
		}
		if name != "running" && l.Tone != "hold" {
			t.Errorf("%s: tone %q", name, l.Tone)
		}
	}
}
