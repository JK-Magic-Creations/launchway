package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const twoApprovers = `
pipeline: demo
steps:
  build: {image: x, run: b}
  approve:
    kind: approval
    needs: build
    message: Deploy to production?
    approvers: { groups: [release-managers], users: [zoe], count: 2 }
  deploy: {image: x, run: d, needs: approve}
`

func toTwoApprovals(t *testing.T, e *Engine) string {
	t.Helper()
	id := create(t, e, twoApprovers, onMain) // started by dana
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "approve", "waiting")
	return id
}

var (
	ann = Decider{Actor: "ann", Groups: []string{"release-managers"}}
	bob = Decider{Actor: "bob", Groups: []string{"release-managers", "devs"}}
)

// The step waits until count distinct people have approved; the same person
// twice is refused; the progress is on the step and in the list.
func TestApprovalNeedsTwoDistinctPeople(t *testing.T) {
	e, _ := newEngine(t)
	id := toTwoApprovals(t, e)
	d, err := e.DecideAs(id, "approve", true, ann, "first")
	if err != nil || d.Done || d.Needed != 2 || len(d.Approvals) != 1 || d.Approvals[0].Actor != "ann" {
		t.Fatalf("first approval: %+v %v", d, err)
	}
	want(t, statuses(t, e, id), "approve", "waiting")
	if _, err := e.DecideAs(id, "approve", true, ann, "again"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already approved") {
		t.Fatalf("the same person twice: %v", err)
	}
	s := stepOf(t, e, id, "approve")
	if s.ApprovalsNeeded != 2 || len(s.Approvals) != 1 || s.Approvals[0].Comment != "first" || s.Approvers == nil || s.Approvers.Count != 2 {
		t.Fatalf("view: %+v", s)
	}
	list, _ := e.Approvals()
	if len(list) != 1 || list[0].ApprovalsNeeded != 2 || len(list[0].Approvals) != 1 || list[0].Approvers == nil {
		t.Fatalf("list: %+v", list)
	}
	if countEvents(t, e, id, StepApprovalRecorded) != 1 || countEvents(t, e, id, StepApproved) != 0 {
		t.Fatal("the first approval is recorded, not final")
	}
	d, err = e.DecideAs(id, "approve", true, bob, "second")
	if err != nil || !d.Done || len(d.Approvals) != 2 {
		t.Fatalf("second approval: %+v %v", d, err)
	}
	want(t, statuses(t, e, id), "approve", "succeeded", "deploy", "queued")
	s = stepOf(t, e, id, "approve")
	if len(s.Approvals) != 2 || s.Approvals[0].Actor != "ann" || s.Approvals[1].Actor != "bob" || s.DecidedBy != "bob" {
		t.Fatalf("decided: %+v", s)
	}
	if _, err := e.DecideAs(id, "approve", true, Decider{Actor: "zoe"}, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("after it is decided: %v", err)
	}
}

// Who may decide is judged by the engine from the name and the groups at the
// time of the decision, for approving and rejecting alike.
func TestApproversLimitWhoMayDecide(t *testing.T) {
	e, _ := newEngine(t)
	id := toTwoApprovals(t, e)
	for _, who := range []Decider{{Actor: "eve"}, {Actor: "eve", Groups: []string{"devs"}}} {
		for _, approve := range []bool{true, false} {
			_, err := e.DecideAs(id, "approve", approve, who, "")
			if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "release-managers") || !strings.Contains(err.Error(), "zoe") {
				t.Fatalf("%+v approve=%v: %v", who, approve, err)
			}
		}
	}
	if err := e.Decide(id, "approve", true, "ann", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Decide knows no groups: %v", err)
	}
	// A user named in users decides without a group; a group's member loses
	// the right when the group is gone from their identity.
	if _, err := e.DecideAs(id, "approve", true, Decider{Actor: "zoe"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DecideAs(id, "approve", true, Decider{Actor: "ann"}, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ann left the group: %v", err)
	}
	// Anyone allowed may reject, which ends it at once.
	d, err := e.DecideAs(id, "approve", false, bob, "no")
	if err != nil || !d.Done {
		t.Fatalf("%+v %v", d, err)
	}
	want(t, statuses(t, e, id), "approve", "failed", "deploy", "skipped")
}

// The person who started the run does not count as an approver.
func TestSelfApprovalDoesNotCount(t *testing.T) {
	no := false
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: newClock().Now, AllowSelfApproval: &no})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	id := toTwoApprovals(t, e)
	_, err = e.DecideAs(id, "approve", true, Decider{Actor: "dana", Groups: []string{"release-managers"}}, "")
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "started this run") {
		t.Fatalf("%v", err)
	}
	if s := stepOf(t, e, id, "approve"); len(s.Approvals) != 0 {
		t.Fatalf("%+v", s.Approvals)
	}
}

// An expiry with one of two approvals rejects the step.
func TestExpiryWithOneOfTwoApprovals(t *testing.T) {
	e, clk := newEngine(t)
	id := toTwoApprovals(t, e)
	if _, err := e.DecideAs(id, "approve", true, ann, ""); err != nil {
		t.Fatal(err)
	}
	clk.Add(25 * time.Hour)
	e.expireWaits()
	want(t, statuses(t, e, id), "approve", "failed")
	if _, err := e.DecideAs(id, "approve", true, bob, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("after the expiry: %v", err)
	}
	// Resuming asks again from nothing.
	if err := e.Resume(id, "ann"); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "approve"); len(s.Approvals) != 0 || s.Status != StepWaitingS {
		t.Fatalf("asked again: %+v", s)
	}
}

// A restart does not lose the first approval, and two servers on one database
// agree; a re-run reuses the decision with the people.
func TestApprovalsSurviveServersAndRerunsReuseThem(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	a := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	b := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	id := create(t, a, twoApprovers, onMain)
	ok(t, a, take(t, a), nil)
	if _, err := a.DecideAs(id, "approve", true, ann, ""); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if _, err := b.DecideAs(id, "approve", true, ann, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("the other server knows ann approved: %v", err)
	}
	if _, err := b.DecideAs(id, "approve", true, bob, ""); err != nil {
		t.Fatal(err)
	}
	ok(t, b, take(t, b), nil)
	want(t, statuses(t, b, id), "run", "succeeded")
	re, err := b.Rerun(RerunRequest{Run: id, From: "deploy", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	s := stepOf(t, b, re.ID, "approve")
	if s.ReusedFrom != id || len(s.Approvals) != 2 || s.Approvals[0].Actor != "ann" || s.Approvals[1].Actor != "bob" {
		t.Fatalf("reused: %+v", s)
	}
	// A server that does not know schema 27 leaves the run alone.
	old := mustOpenAs(t, addr, clk, schemaFlow, &syncBuffer{})
	if d, err := old.GetRun(id); err != nil || !d.NeedsNewerServer {
		t.Fatalf("%+v %v", d, err)
	}
	if err := old.Decide(id, "approve", true, "bob", ""); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("an older server decided: %v", err)
	}
}

// A protected environment's min_approvals raises a lower count, and nothing
// in the pipeline lowers it.
func TestEnvironmentFloorBeatsTheStepsCount(t *testing.T) {
	envs := []Environment{{Name: "prod", Target: "cluster-a", Projects: []string{"shop"}, Protected: true, MinApprovals: 3}}
	e, _ := newDeployEngine(t, envs)
	id := createShop(t, e, strings.Replace(gatedPipeline, "    message: Promote?\n", "    message: Promote?\n    approvers: { count: 2 }\n", 1), 1)
	toPause(t, e, id)
	ok(t, e, take(t, e), nil) // smoke
	want(t, statuses(t, e, id), "approve", "waiting")
	s := stepOf(t, e, id, "approve")
	if s.ApprovalsNeeded != 3 || !strings.Contains(s.ApprovalsNeededFrom, "min_approvals of the environment prod") {
		t.Fatalf("effective count: %d from %q", s.ApprovalsNeeded, s.ApprovalsNeededFrom)
	}
	for i, who := range []string{"ann", "bob"} {
		d, err := e.DecideAs(id, "approve", true, Decider{Actor: who}, "")
		if err != nil || d.Done || d.Needed != 3 || len(d.Approvals) != i+1 {
			t.Fatalf("%s: %+v %v", who, d, err)
		}
	}
	want(t, statuses(t, e, id), "approve", "waiting")
	d, err := e.DecideAs(id, "approve", true, Decider{Actor: "cy"}, "")
	if err != nil || !d.Done {
		t.Fatalf("%+v %v", d, err)
	}
	want(t, statuses(t, e, id), "approve", "succeeded")

	// Without approvers a floor still applies, and a floor needs protected.
	if _, err := checkEnvironments([]Environment{{Name: "x", Target: "t", Projects: []string{"*"}, MinApprovals: 2}}); err == nil || !strings.Contains(err.Error(), "needs protected") {
		t.Fatalf("%v", err)
	}
	if _, err := checkEnvironments([]Environment{{Name: "x", Target: "t", Projects: []string{"*"}, Protected: true, MinApprovals: 9}}); err == nil {
		t.Fatal("9 approvals is too many")
	}
}
