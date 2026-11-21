package engine

import (
	"fmt"
	"html"
	"strings"
	"testing"
	"time"
)

const debriefOne = `
pipeline: demo
steps:
  test: {image: x, run: t, reports: {tests: "reports/*.xml"}}
`

type tcase struct{ name, status, msg string }

// junitNamed is a JUnit report with the named tests and statuses (pass or
// fail), in suite pkg.
func junitNamed(files []ReportFile, cs ...tcase) []ReportFile {
	var b strings.Builder
	b.WriteString("<testsuites><testsuite name=\"pkg\">\n")
	for _, c := range cs {
		fmt.Fprintf(&b, `<testcase classname="pkg" name="%s" time="0.%03d">`, c.name, 100+len(c.name))
		if c.status == "fail" {
			fmt.Fprintf(&b, `<failure message="%s">at f_test.go:12</failure>`, html.EscapeString(c.msg))
		}
		b.WriteString("</testcase>\n")
	}
	b.WriteString("</testsuite></testsuites>")
	return append(files, ReportFile{Path: "reports/r.xml", Data: []byte(b.String()), Size: int64(b.Len())})
}

// runWith creates a run on a ref and commit, finishes its one step with the
// report and returns the run's ID.
func runWith(t *testing.T, e *Engine, ref, sha string, extra []ReportFile, cs ...tcase) string {
	t.Helper()
	id := create(t, e, debriefOne, Trigger{Event: "push", Ref: "refs/heads/" + ref, Branch: ref, SHA: sha, Actor: "ana"})
	j := take(t, e)
	outcome := "succeeded"
	for _, c := range cs {
		if c.status == "fail" {
			outcome = "failed"
		}
	}
	finishTests(t, e, j, outcome, junitNamed(extra, cs...)...)
	return id
}

func TestDebriefComparesWithTheLastGoodRunOfTheDefaultBranch(t *testing.T) {
	e, clk := newEngine(t)
	pass := []tcase{{"A", "pass", ""}, {"B", "pass", ""}, {"C", "pass", ""}, {"D", "pass", ""}}
	runWith(t, e, "main", "sha-main-1", nil, pass...)
	clk.t = clk.t.Add(time.Minute)
	// main breaks D on its next commit: that run is not good, so the base stays run 1.
	runWith(t, e, "main", "sha-main-2", nil, tcase{"A", "pass", ""}, tcase{"B", "pass", ""}, tcase{"C", "pass", ""}, tcase{"D", "fail", "expected:<4> but was:<5>"})
	// The branch run breaks B and D.
	id := runWith(t, e, "fix", "sha-fix-1", nil, tcase{"A", "pass", ""}, tcase{"B", "fail", "got 1, want 2"}, tcase{"C", "pass", ""}, tcase{"D", "fail", "expected:<4> but was:<5>"})

	r, err := e.Debrief(id, DebriefOptions{DefaultRef: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Total != 4 || r.Summary.Failed != 2 || r.Summary.Passed != 2 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if r.Change == nil || r.Change.Base.Number != 1 || r.Change.NewlyFailing != 2 {
		t.Fatalf("change %+v", r.Change)
	}
	if len(r.Failures) != 2 {
		t.Fatalf("failures %+v", r.Failures)
	}
	by := map[string]string{}
	for _, f := range r.Failures {
		by[f.Name] = f.FailingOnBase
		if !f.NewHere || f.FirstFailedIn == nil || f.FirstFailedIn.Number != 3 {
			t.Errorf("%s: new here %v, first failed in %+v", f.Name, f.NewHere, f.FirstFailedIn)
		}
		if f.Diff == nil || f.Diff.Expected == "" {
			t.Errorf("%s: no diff from %q", f.Name, f.Message)
		}
		if f.Line != 0 {
			t.Errorf("a trace line without a file must not become a line: %d", f.Line)
		}
	}
	// B passes on main (so this change broke it); D fails there too.
	if by["B"] != "no" || by["D"] != "yes" {
		t.Errorf("also failing on main: %v", by)
	}
	if len(r.Suites) == 0 || r.Suites[0].Total != 4 || len(r.Slowest) == 0 {
		t.Errorf("suites %+v slowest %+v", r.Suites, r.Slowest)
	}
}

func TestFlakyIsFoundFromTheSameCommitPassingAndFailing(t *testing.T) {
	e, clk := newEngine(t)
	ok := tcase{"Stable", "pass", ""}
	for i, st := range []string{"pass", "fail", "pass", "pass", "pass"} {
		sha := fmt.Sprintf("sha-%d", i/2) // runs 0,1 share a commit
		runWith(t, e, "main", sha, nil, ok, tcase{"Wobbly", st, "timeout"})
		clk.t = clk.t.Add(time.Minute)
	}
	fl, err := e.ProjectFlaky("demo", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.Tests) != 1 || fl.Tests[0].Name != "Wobbly" || fl.Tests[0].SameCommit != 1 || fl.Tests[0].Score == 0 || fl.Tests[0].Suggest == "" {
		t.Fatalf("flaky tests: %+v", fl)
	}
	if fl.Tests[0].Branches[0] != "main" || fl.Tests[0].FirstSeen == 0 || len(fl.Tests[0].History) != 5 {
		t.Errorf("flaky detail: %+v", fl.Tests[0])
	}
	if fl.Considered != 1 {
		t.Errorf("only the test that failed and passed is considered: %d", fl.Considered)
	}
	// A restricted reader sees only the refs it may: nothing from main here.
	only, err := e.ProjectFlaky("demo", []string{"refs/heads/other"}, 0)
	if err != nil || len(only.Tests) != 0 {
		t.Fatalf("history of a ref the reader may not see: %+v %v", only, err)
	}
	// The latest run's Debrief lists it (it passes there but is flaky by history).
	id := runWith(t, e, "main", "sha-9", nil, ok, tcase{"Wobbly", "fail", "timeout"})
	r, err := e.Debrief(id, DebriefOptions{DefaultRef: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Flaky) != 1 || r.Flaky[0].ThisRun != "failed" || r.Failures[0].FlakyScore == 0 {
		t.Fatalf("debrief flaky %+v failures %+v", r.Flaky, r.Failures)
	}
}

func TestAFixedBreakIsNotFlaky(t *testing.T) {
	e, clk := newEngine(t)
	for i, st := range []string{"pass", "fail", "fail", "fail", "pass", "pass"} {
		runWith(t, e, "main", fmt.Sprintf("sha-%d", i), nil, tcase{"Broken", st, "boom"})
		clk.t = clk.t.Add(time.Minute)
	}
	fl, err := e.ProjectFlaky("demo", nil, 0)
	if err != nil || len(fl.Tests) != 0 || fl.Considered != 1 {
		t.Fatalf("%+v %v", fl, err)
	}
}

const quarantineYML = `quarantine:
  - test: "pkg::Wobbly"
    owner: ana
    expires: 2026-12-31
    reason: timeouts in CI
`

func TestQuarantinedFailuresDoNotFailTheRunButAreCounted(t *testing.T) {
	e, _ := newEngine(t) // the clock is 2026-11-14
	q := []ReportFile{{Path: ".launchway/quarantine.yml", Data: []byte(quarantineYML)}}
	id := runWith(t, e, "main", "sha-1", q, tcase{"Stable", "pass", ""}, tcase{"Wobbly", "fail", "timeout"})
	if got := statuses(t, e, id)["run"]; got != "succeeded" {
		t.Fatalf("a run whose only failure is quarantined is %s", got)
	}
	r, err := e.Debrief(id, DebriefOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Failures) != 0 || len(r.Quarantined) != 1 || r.Summary.Quarantined != 1 || r.Quarantined[0].Owner != "ana" {
		t.Fatalf("failures %+v quarantined %+v", r.Failures, r.Quarantined)
	}
	if r.Summary.Failed != 1 {
		t.Errorf("the failure is still counted honestly: %+v", r.Summary)
	}
	// A different failure next to it is not excused, so the run fails.
	id2 := runWith(t, e, "main", "sha-2", q, tcase{"Wobbly", "fail", "timeout"}, tcase{"Other", "fail", "real"})
	if got := statuses(t, e, id2)["run"]; got != "failed" {
		t.Fatalf("an unexcused failure must fail the run, got %s", got)
	}
	r2, _ := e.Debrief(id2, DebriefOptions{})
	if len(r2.Failures) != 1 || r2.Failures[0].Name != "Other" || len(r2.Quarantined) != 1 {
		t.Fatalf("r2 failures %+v", r2.Failures)
	}
}

func TestAnExpiredQuarantineExcusesNothing(t *testing.T) {
	e, clk := newEngine(t)
	clk.t = time.Date(2027, 1, 5, 9, 0, 0, 0, time.UTC) // after the entry's 2026-12-31
	q := []ReportFile{{Path: ".launchway/quarantine.yml", Data: []byte(quarantineYML)}}
	id := runWith(t, e, "main", "sha-1", q, tcase{"Wobbly", "fail", "timeout"})
	if got := statuses(t, e, id)["run"]; got != "failed" {
		t.Fatalf("an expired entry must not excuse the failure, got %s", got)
	}
	r, _ := e.Debrief(id, DebriefOptions{})
	if len(r.Failures) != 1 || len(r.Quarantined) != 1 || !r.Quarantined[0].Expired || r.Summary.Quarantined != 0 {
		t.Fatalf("failures %+v quarantined %+v", r.Failures, r.Quarantined)
	}
}

func TestTimingsSurvivePruningAndSplitIsStable(t *testing.T) {
	e, clk := newEngine(t)
	for i := 0; i < 3; i++ {
		runWith(t, e, "main", fmt.Sprintf("sha-%d", i), nil, tcase{"A", "pass", ""}, tcase{"LongerName", "pass", ""}, tcase{"BBB", "pass", ""})
		clk.t = clk.t.Add(time.Minute)
	}
	tl, err := e.TestTimings("demo", 0)
	if err != nil || len(tl.Tests) != 3 || tl.Tests[0].Name != "LongerName" || tl.Tests[0].Samples != 3 || tl.Tests[0].AvgMs == 0 {
		t.Fatalf("timings %+v %v", tl, err)
	}
	// Remove every stored result: the timings stay.
	if _, err := e.db.Exec(`DELETE FROM test_cases`); err != nil {
		t.Fatal(err)
	}
	s1, err := e.SplitTests("demo", 2)
	if err != nil || s1.Tests != 3 || len(s1.Shards) != 2 {
		t.Fatalf("split %+v %v", s1, err)
	}
	s2, _ := e.SplitTests("demo", 2)
	if fmt.Sprint(s1.Shards) != fmt.Sprint(s2.Shards) {
		t.Error("the split changed between two calls")
	}
	if s1.Shards[0].Tests[0] == "" || !strings.Contains(strings.Join(s1.Shards[0].Tests, ","), "pkg::") {
		t.Errorf("shard names %v", s1.Shards)
	}
	if _, err := e.SplitTests("demo", 0); err == nil {
		t.Error("0 shards is an error")
	}
	// Retention: results older than the age are removed, the totals stay,
	// and so do not-yet-old statistics.
	runWith(t, e, "main", "sha-new", nil, tcase{"A", "pass", ""})
	clk.t = clk.t.Add(400 * 24 * time.Hour)
	e.pruneTests()
	if n := countCases(t, e, "1 = 1"); n != 0 {
		t.Errorf("%d results kept past the retention", n)
	}
	var stats int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM test_stats`).Scan(&stats)
	if stats != 0 {
		t.Errorf("%d statistics kept for tests not seen within the retention", stats)
	}
	var total int
	_ = e.db.QueryRow(`SELECT COALESCE(SUM(total), 0) FROM test_reports`).Scan(&total)
	if total == 0 {
		t.Error("the runs' totals must stay")
	}
}
