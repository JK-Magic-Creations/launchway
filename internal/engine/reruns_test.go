package engine

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const fourSteps = `
pipeline: demo
steps:
  build:  {image: x, run: b}
  test:   {image: x, run: t, needs: build}
  deploy: {image: x, run: d, needs: test}
  verify: {image: x, run: v, needs: [deploy, build]}
`

// finishedWithFailedDeploy runs fourSteps with outputs, deploy failing.
func finishedWithFailedDeploy(t *testing.T, e *Engine) string {
	t.Helper()
	id := create(t, e, fourSteps, Trigger{Event: "manual", Branch: "main", SHA: "c0ffee", Actor: "dana"})
	ok(t, e, take(t, e), map[string]string{"image": "registry/app:1"})
	ok(t, e, take(t, e), map[string]string{"report": "all green"})
	fail(t, e, take(t, e))
	want(t, statuses(t, e, id), "run", "failed", "build", "succeeded", "test", "succeeded", "deploy", "failed", "verify", "skipped")
	return id
}

func rerun(t *testing.T, e *Engine, runID, from, actor string) *RunSummary {
	t.Helper()
	r, err := e.Rerun(RerunRequest{Run: runID, From: from, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A re-run from a step runs that step and the ones after it, with the
// outputs the steps before it had; those are reused, never leased.
func TestRerunFromAStep(t *testing.T) {
	e, _ := newEngine(t)
	orig := finishedWithFailedDeploy(t, e)

	plan, err := e.PlanRerun(orig, "deploy")
	if err != nil || !slices.Equal(plan.Run, []string{"deploy", "verify"}) || !slices.Equal(plan.Reuse, []string{"build", "test"}) {
		t.Fatalf("plan %+v, %v", plan, err)
	}
	re := rerun(t, e, orig, "deploy", "ann")
	if re.Number != 2 || re.RerunOf != orig || re.RerunOfNumber != 1 || re.RerunFrom != "deploy" || re.SHA != "c0ffee" ||
		re.Branch != "main" || re.Actor != "ann" || re.MinSchema != schemaReruns {
		t.Fatalf("re-run %+v", re)
	}
	want(t, statuses(t, e, re.ID), "run", "queued", "build", "succeeded", "test", "succeeded", "deploy", "queued", "verify", "pending")
	b := stepOf(t, e, re.ID, "build")
	if b.ReusedFrom != orig || b.ReusedFromNumber != 1 || b.ReusedAttempt != 1 || b.Outputs["image"] != "registry/app:1" || b.Attempt != 0 {
		t.Fatalf("reused build: %+v", b)
	}

	var leased []string
	j := take(t, e)
	leased = append(leased, j.StepID)
	if j.RunID != re.ID || j.Env["LAUNCHWAY_NEEDS_TEST_REPORT"] != "all green" || j.Env["LAUNCHWAY_RUN_NUMBER"] != "2" {
		t.Fatalf("deploy in the re-run: %s %v", j.RunID, j.Env)
	}
	ok(t, e, j, nil)
	j = take(t, e)
	leased = append(leased, j.StepID)
	if j.Env["LAUNCHWAY_NEEDS_BUILD_IMAGE"] != "registry/app:1" {
		t.Fatalf("verify did not receive build's output from the original run: %v", j.Env)
	}
	// Its inputs say where that value came from.
	ok(t, e, j, nil)
	none(t, e)
	if !slices.Equal(leased, []string{"deploy", "verify"}) {
		t.Fatalf("leased %v", leased)
	}
	want(t, statuses(t, e, re.ID), "run", "succeeded", "build", "succeeded", "test", "succeeded", "deploy", "succeeded", "verify", "succeeded")
	in := stepOf(t, e, re.ID, "verify").Inputs
	if len(in) != 1 || !slices.ContainsFunc(in[0].Inputs.Env, func(v InputVar) bool {
		return v.Name == "LAUNCHWAY_NEEDS_BUILD_IMAGE" && v.From == FromNeeds && v.Step == "build" && v.Output == "image" && v.Value == "registry/app:1"
	}) {
		t.Fatalf("verify's inputs: %+v", in)
	}
	if n := countEvents(t, e, re.ID, StepLeased); n != 2 {
		t.Fatalf("%d steps leased in the re-run", n)
	}

	// The original lists its re-runs, and is itself unchanged.
	d, err := e.GetRun(orig)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Reruns) != 1 || d.Reruns[0].ID != re.ID || d.Reruns[0].From != "deploy" || d.Reruns[0].Number != 2 {
		t.Fatalf("re-runs of the original: %+v", d.Reruns)
	}
	want(t, statuses(t, e, orig), "run", "failed", "deploy", "failed")

	// A re-run of the re-run: a step reused there still points at the run
	// where it actually ran.
	again := rerun(t, e, re.ID, "verify", "ann")
	for step, from := range map[string]string{"build": orig, "test": orig, "deploy": re.ID} {
		if s := stepOf(t, e, again.ID, step); s.ReusedFrom != from || s.Status != StepSucceeded {
			t.Fatalf("%s in the second re-run: reused from %q (%s), want %q", step, s.ReusedFrom, s.Status, from)
		}
	}
	// Resuming a re-run never runs what it reused.
	fail(t, e, take(t, e))
	if err := e.Resume(again.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	if j := take(t, e); j.StepID != "verify" {
		t.Fatalf("resume leased %s", j.StepID)
	}
}

func TestRerunRefusals(t *testing.T) {
	e, _ := newEngine(t)
	orig := finishedWithFailedDeploy(t, e) // #1

	// A failed step that is neither the step nor after it cannot be reused.
	side := create(t, e, `
pipeline: demo
steps:
  a: {image: x, run: a}
  x: {image: x, run: x}
  b: {image: x, run: b, needs: a}
`, Trigger{Event: "manual"}) // #2
	for j := leaseAs(t, e, runner); j != nil; j = leaseAs(t, e, runner) {
		if j.StepID == "x" {
			fail(t, e, j)
		} else {
			ok(t, e, j, nil)
		}
	}
	// A step skipped by its condition is reused as skipped.
	cond := create(t, e, `
pipeline: demo
steps:
  a: {image: x, run: a}
  only-tags: {image: x, run: t, needs: a, when: {tag: ["v*"]}}
  b: {image: x, run: b, needs: a}
`, Trigger{Event: "manual", Branch: "main"}) // #3
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	running := create(t, e, linear, Trigger{Event: "manual"}) // #4

	for _, c := range []struct {
		run, from string
		err       error
		says      string
	}{
		{orig, "nope", ErrNotFound, "run #1 has no step nope"},
		{orig, "verify", ErrConflict, "it needs deploy (failed), which did not succeed in run #1"},
		{"run_missing", "build", ErrNotFound, "run_missing"},
		{running, "a", ErrConflict, "run #4 is queued; only a finished run can be re-run"},
		{side, "b", ErrConflict, "x (failed) did not succeed in run #2 and does not come after b"},
	} {
		_, err := e.Rerun(RerunRequest{Run: c.run, From: c.from, Actor: "ann"})
		if !errors.Is(err, c.err) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("re-run of %s from %s: %v, want %v saying %q", c.run, c.from, err, c.err, c.says)
		}
	}
	// From x itself is fine: a and b are reused.
	if p, err := e.PlanRerun(side, "x"); err != nil || !slices.Equal(p.Run, []string{"x"}) {
		t.Fatalf("plan from x: %+v, %v", p, err)
	}
	re := rerun(t, e, cond, "b", "ann")
	if s := stepOf(t, e, re.ID, "only-tags"); s.Status != StepSkippedS || s.SkipKind != "condition" || s.ReusedFrom != cond {
		t.Fatalf("condition-skipped step: %+v", s)
	}
}

// Approvals after the step ask again; approvals before it are reused with
// the decision made in the original, and say who made it.
func TestRerunAndApprovals(t *testing.T) {
	no := false
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, AllowSelfApproval: &no})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	id := toApproval(t, e) // started by dana
	if err := e.Decide(id, "approve", true, "ann", "ship it"); err != nil {
		t.Fatal(err)
	}
	fail(t, e, take(t, e))

	re := rerun(t, e, id, "deploy", "bob")
	gate := stepOf(t, e, re.ID, "approve")
	if gate.Status != StepSucceeded || gate.DecidedBy != "ann" || gate.Comment != "ship it" || gate.ReusedFrom != id || gate.DecidedAt == 0 {
		t.Fatalf("reused approval: %+v", gate)
	}
	if j := take(t, e); j.RunID != re.ID || j.StepID != "deploy" {
		t.Fatalf("leased %s/%s", j.RunID, j.StepID)
	}

	re2 := rerun(t, e, id, "approve", "bob")
	want(t, statuses(t, e, re2.ID), "approve", "waiting", "deploy", "pending", "build", "succeeded")
	// bob asked for the re-run, so with self-approval off he may not approve it.
	if err := e.Decide(re2.ID, "approve", true, "bob", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob approving his own re-run: %v", err)
	}
	if err := e.Decide(re2.ID, "approve", true, "ann", ""); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, re2.ID), "approve", "succeeded")
}

// A draft run's re-run is a draft run of the same text, and whether its
// steps may read secrets is decided again.
func TestRerunOfADraft(t *testing.T) {
	e, _ := newEngine(t)
	id := createDraft(t, e, draftText, false)
	for j := leaseAs(t, e, runner); j != nil; j = leaseAs(t, e, runner) {
		ok(t, e, j, nil)
	}
	want(t, statuses(t, e, id), "run", "failed", "publish", "failed")

	if _, err := e.Rerun(RerunRequest{Run: id, From: "publish", Actor: "dana"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("draft re-run without saying about secrets: %v", err)
	}
	yes := true
	re, err := e.Rerun(RerunRequest{Run: id, From: "publish", Actor: "dana", DraftSecrets: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if !re.Draft || re.PipelineHash != DraftHash(draftText) || re.Event != "draft" {
		t.Fatalf("draft re-run: %+v", re)
	}
	text, isDraft, err := e.DraftText(re.ID)
	if err != nil || !isDraft || text.Text != draftText || !text.Secrets {
		t.Fatalf("draft re-run's text: %+v %v %v", text, isDraft, err)
	}
	j := take(t, e)
	if j.StepID != "publish" || j.Secrets["TOKEN"].Key != "apps/registry" {
		t.Fatalf("publish in the re-run: %s %+v", j.StepID, j.Secrets)
	}
	if s := stepOf(t, e, re.ID, "deploy"); s.Status != StepSkippedS || s.SkipKind != "condition" {
		t.Fatalf("deploy stays skipped in the draft: %+v", s)
	}

	plain := finishedWithFailedDeploy(t, e)
	if _, err := e.Rerun(RerunRequest{Run: plain, From: "deploy", Actor: "dana", DraftSecrets: &yes}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a run that is not a draft, re-run as one: %v", err)
	}
}

// A re-run uses the libraries at the commits the original recorded.
func TestRerunKeepsTheLibraryCommits(t *testing.T) {
	e, _ := newEngine(t)
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: expanded(t, commitA), Trigger: Trigger{Event: "manual"}, Source: libSource(commitA)})
	if err != nil {
		t.Fatal(err)
	}
	for j := leaseAs(t, e, runner); j != nil; j = leaseAs(t, e, runner) {
		ok(t, e, j, map[string]string{"coverage": "90"})
	}
	re := rerun(t, e, r.ID, "ci/test", "ann")
	j := take(t, e)
	if j.StepID != "ci/test" || j.Source.Libraries[0].Commit != commitA || j.Library != "platform" {
		t.Fatalf("re-run job: %s %+v", j.StepID, j.Source)
	}
	ok(t, e, j, map[string]string{"coverage": "91"})
	j = take(t, e)
	if j.StepID != "deploy" || j.Env["LAUNCHWAY_NEEDS_CI_COVERAGE"] != "91" {
		t.Fatalf("deploy after the re-run of ci/test: %s %v", j.StepID, j.Env)
	}
	d, _ := e.GetRun(re.ID)
	if d.Graph.Libraries[0].Commit != commitA {
		t.Fatalf("library in the re-run: %+v", d.Graph.Libraries[0])
	}
	in := stepOf(t, e, re.ID, "ci/test").Inputs[0].Inputs
	if in.Library == nil || in.Library.Commit != commitA || len(in.Params) != 1 || in.Params[0].Name != "name" || in.Params[0].Value != "web" {
		t.Fatalf("inputs of ci/test: %+v", in)
	}
}

// Two servers: a re-run made on one is run through the other, and re-runs
// made on both at once get numbers of their own.
func TestRerunAcrossServers(t *testing.T) {
	a, b, _ := pair(t)
	orig := finishedWithFailedDeploy(t, a)
	re := rerun(t, b, orig, "deploy", "ann")
	j := take(t, a)
	if j.RunID != re.ID || j.StepID != "deploy" || j.Env["LAUNCHWAY_NEEDS_TEST_REPORT"] != "all green" {
		t.Fatalf("server a leased %s/%s %v", j.RunID, j.StepID, j.Env)
	}
	ok(t, b, j, nil)
	j = take(t, b)
	if j.StepID != "verify" || j.Env["LAUNCHWAY_NEEDS_BUILD_IMAGE"] != "registry/app:1" {
		t.Fatalf("server b leased %s %v", j.StepID, j.Env)
	}
	ok(t, a, j, nil)
	none(t, a)
	none(t, b)
	want(t, statuses(t, a, re.ID), "run", "succeeded", "build", "succeeded", "test", "succeeded")
	want(t, statuses(t, b, re.ID), "run", "succeeded")

	var wg sync.WaitGroup
	numbers := make([]int, 10)
	for i := range numbers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := []*Engine{a, b}[i%2].Rerun(RerunRequest{Run: orig, From: "verify", Actor: "ann"})
			if err == nil {
				t.Errorf("a re-run from verify, which needs the failed deploy, was made: #%d", r.Number)
			}
			r, err = []*Engine{a, b}[i%2].Rerun(RerunRequest{Run: re.ID, From: "verify", Actor: "ann"})
			if err != nil {
				t.Error(err)
				return
			}
			numbers[i] = r.Number
		}()
	}
	wg.Wait()
	slices.Sort(numbers)
	if len(slices.Compact(slices.Clone(numbers))) != 10 {
		t.Fatalf("re-run numbers: %v", numbers)
	}
	d, _ := b.GetRun(re.ID)
	if len(d.Reruns) != 10 {
		t.Fatalf("%d re-runs listed", len(d.Reruns))
	}
}
