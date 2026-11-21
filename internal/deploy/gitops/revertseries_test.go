package gitops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// applyOther is a deployment that changes other.yaml.
func applyOther(dep, value string) ChangeSet {
	return ChangeSet{
		Deployment: dep,
		Message:    "Deploy worker " + value,
		Files:      []FileEdit{{Path: "other.yaml", Edit: set("x: " + value + "\n")}},
	}
}

// Rolling back to an older version reverts every deployment after it, newest
// first, one revert commit each, pushed together; each revert carries its
// own deployment's trailers, and the files end as they were before them.
func TestRevertSeriesRevertsNewestFirstAndPushesOnce(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, l := s.repo(t)
	d1, err := r.Apply(ctx, deploy("dep_one", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := r.Apply(ctx, deploy("dep_two", "3.0"))
	if err != nil {
		t.Fatal(err)
	}
	d3, err := r.Apply(ctx, applyOther("dep_three", "9"))
	if err != nil {
		t.Fatal(err)
	}
	// Roll back to the state dep_one left: revert dep_three, then dep_two.
	res, err := r.RevertSeries(ctx, []RevertItem{
		{Deployment: "dep_three", Commit: d3.Commit, Message: "Roll back worker 9"},
		{Deployment: "dep_two", Commit: d2.Commit, Message: "Roll back webapp 3.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Existing || len(res.Commits) != 2 || res.Commits[1] != s.ref() {
		t.Fatalf("result = %+v, tip %s", res, s.ref())
	}
	if s.file("app.yaml") != "image: webapp:2.0" || s.file("other.yaml") != "x: 1" {
		t.Fatalf("files = %q, %q", s.file("app.yaml"), s.file("other.yaml"))
	}
	if !s.isAncestor(d1.Commit) {
		t.Fatal("history was rewritten")
	}
	want := "Roll back worker 9\n\nThis reverts commit " + d3.Commit + ".\n\nLaunchway-Deployment: dep_three\nLaunchway-Revert: dep_three"
	if got := s.message(res.Commits[0]); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if !strings.Contains(s.message(res.Commits[1]), "Launchway-Revert: dep_two") {
		t.Fatalf("second revert = %q", s.message(res.Commits[1]))
	}
	if n := strings.Count(l.all(), "Committed "); n != 3+2 {
		t.Fatalf("log has %d commits:\n%s", n, l.all())
	}

	// Asked again from another machine: every item found, nothing pushed.
	before := s.ref()
	r2, _ := s.repo(t)
	again, err := r2.RevertSeries(ctx, []RevertItem{
		{Deployment: "dep_three", Commit: d3.Commit, Message: "Roll back worker 9"},
		{Deployment: "dep_two", Commit: d2.Commit, Message: "Roll back webapp 3.0"},
	})
	if err != nil || !again.Existing || again.Commits[0] != res.Commits[0] || again.Commits[1] != res.Commits[1] || s.ref() != before {
		t.Fatalf("again = %+v, %v", again, err)
	}
}

// An item already reverted by an earlier try is skipped; the rest is done.
func TestRevertSeriesCompletesAHalfDoneSeries(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, _ := s.repo(t)
	d2, _ := r.Apply(ctx, deploy("dep_two", "3.0"))
	d3, _ := r.Apply(ctx, applyOther("dep_three", "9"))
	first, err := r.Revert(ctx, "dep_three", d3.Commit, "Roll back worker 9")
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.RevertSeries(ctx, []RevertItem{
		{Deployment: "dep_three", Commit: d3.Commit, Message: "Roll back worker 9"},
		{Deployment: "dep_two", Commit: d2.Commit, Message: "Roll back webapp 3.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Existing || res.Commits[0] != first.Commit || res.Commits[1] != s.ref() {
		t.Fatalf("result = %+v", res)
	}
	if s.file("app.yaml") != "image: webapp:1.0" {
		t.Fatalf("app.yaml = %q", s.file("app.yaml"))
	}
}

// When one revert does not apply, nothing is pushed, not even the reverts
// before it, the error names the deployment and the files, and the clone is
// clean.
func TestRevertSeriesRefusesWholeWhenOneConflicts(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, _ := s.repo(t)
	d2, _ := r.Apply(ctx, deploy("dep_two", "3.0"))
	d3, _ := r.Apply(ctx, applyOther("dep_three", "9"))
	s.commit(map[string]string{"app.yaml": "image: webapp:hotfix\n"}, "Someone edited app.yaml by hand")
	before := s.ref()
	_, err := r.RevertSeries(ctx, []RevertItem{
		{Deployment: "dep_three", Commit: d3.Commit, Message: "Roll back worker 9"},
		{Deployment: "dep_two", Commit: d2.Commit, Message: "Roll back webapp 3.0"},
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a ConflictError", err)
	}
	if conflict.Deployment != "dep_two" || len(conflict.Files) != 1 || conflict.Files[0] != "app.yaml" {
		t.Fatalf("conflict = %+v", conflict)
	}
	if s.ref() != before {
		t.Fatal("the first revert was pushed although the second did not apply")
	}
	if out := run(t, r.Dir, "status", "--porcelain", "--ignored"); out != "" {
		t.Fatalf("clone left dirty: %q", out)
	}
	if _, err := r.RevertSeries(ctx, nil); err == nil {
		t.Fatal("an empty series was accepted")
	}
}

// A push rejected because the branch moved starts the whole series again on
// the new tip.
func TestRevertSeriesRetriesWhenTheBranchMoves(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, _ := s.repo(t)
	d2, _ := r.Apply(ctx, deploy("dep_two", "3.0"))
	d3, _ := r.Apply(ctx, applyOther("dep_three", "9"))
	moved := false
	r.beforePush = func() {
		if !moved {
			moved = true
			s.commit(map[string]string{"third.yaml": "y: 1\n"}, "Unrelated")
		}
	}
	res, err := r.RevertSeries(ctx, []RevertItem{
		{Deployment: "dep_three", Commit: d3.Commit, Message: "Roll back worker 9"},
		{Deployment: "dep_two", Commit: d2.Commit, Message: "Roll back webapp 3.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits[1] != s.ref() || s.file("app.yaml") != "image: webapp:1.0" || s.file("third.yaml") != "y: 1" {
		t.Fatalf("result = %+v", res)
	}
}
