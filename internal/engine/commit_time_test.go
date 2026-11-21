package engine

import (
	"errors"
	"strings"
	"testing"
)

func TestCommitTimeFormat(t *testing.T) {
	if got := CommitTimeAt(1795155176); got != "2026-11-20T06:12:56Z" {
		t.Fatalf("CommitTimeAt = %q", got)
	}
	if got := CommitTimeAt(0); got != "" {
		t.Fatalf("no time must give %q, not a value", got)
	}
	tr := Trigger{CommitTime: "2026-11-20T06:12:56Z"}
	if got := tr.SourceDateEpoch(); got != "1795155176" {
		t.Fatalf("SourceDateEpoch = %q", got)
	}
	if got := (Trigger{}).SourceDateEpoch(); got != "" {
		t.Fatalf("SourceDateEpoch without a time = %q", got)
	}
}

const commitTimePipeline = `
pipeline: demo
steps:
  a: {image: x, run: a}
  image:
    kind: image
    needs: a
    build_args: { CREATED: { run: commit_time } }
    tags:
      - { registry: harbor, repository: team/shop, tag: { run: short_sha } }
`

// The commit time reaches steps as LAUNCHWAY_COMMIT_TIME and
// SOURCE_DATE_EPOCH, and a re-run of the run sees the same values.
func TestCommitTimeInEnvironmentAndKeptByRerun(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, fourSteps, Trigger{Event: "manual", Branch: "main", SHA: "c0ffee1234", CommitTime: "2026-11-20T06:12:56Z"})
	j := take(t, e)
	if j.Env["LAUNCHWAY_COMMIT_TIME"] != "2026-11-20T06:12:56Z" || j.Env["SOURCE_DATE_EPOCH"] != "1795155176" {
		t.Fatalf("environment: %v", j.Env)
	}
	ok(t, e, j, nil)
	ok(t, e, take(t, e), nil)
	fail(t, e, take(t, e))

	re := rerun(t, e, id, "deploy", "ann")
	tr, err := e.RunTrigger(re.ID)
	if err != nil || tr.CommitTime != "2026-11-20T06:12:56Z" {
		t.Fatalf("re-run trigger: %+v, %v", tr, err)
	}
	j = take(t, e)
	if j.Env["LAUNCHWAY_COMMIT_TIME"] != "2026-11-20T06:12:56Z" || j.Env["SOURCE_DATE_EPOCH"] != "1795155176" {
		t.Fatalf("re-run environment: %v", j.Env)
	}
}

func TestNoCommitTimeWithoutCommit(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, linear, Trigger{Event: "manual"})
	j := take(t, e)
	if _, set := j.Env["LAUNCHWAY_COMMIT_TIME"]; set {
		t.Fatalf("set without a commit: %v", j.Env)
	}
	if _, set := j.Env["SOURCE_DATE_EPOCH"]; set {
		t.Fatalf("set without a commit: %v", j.Env)
	}
	_ = id
}

// A pipeline that uses { run: commit_time } is refused when the run has no
// commit time, naming the step, instead of building with an empty value.
func TestCommitTimeRequiredWhenUsed(t *testing.T) {
	e, _ := newEngine(t)
	_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, commitTimePipeline), Trigger: Trigger{Event: "manual"},
		Source: Source{Kind: "dir", Path: "/tmp"}})
	if !errors.Is(err, ErrInvalid) || err == nil || !strings.Contains(err.Error(), "step image uses { run: commit_time }") {
		t.Fatalf("err = %v", err)
	}
}
