package engine

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// reportsLinear is linear, with test reports declared on every step.
const reportsLinear = `
pipeline: demo
steps:
  a: {image: x, run: a, reports: {tests: "reports/*.xml"}}
  b: {image: x, run: b, needs: a, reports: {tests: "reports/*.xml"}}
  c: {image: x, run: c, needs: b, reports: {tests: "reports/*.xml"}}
`

// junitFile makes a JUnit report with one test per status given (pass,
// fail, error or skip), named t0, t1, ... in the suite demo.Suite, each
// taking 10ms more than the one before.
func junitFile(path string, statuses ...string) ReportFile {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<testsuites>\n<testsuite name=\"demo\">\n")
	for i, s := range statuses {
		fmt.Fprintf(&b, `<testcase classname="demo.Suite" name="t%d" time="%.3f">`, i, float64(i+1)/100)
		switch s {
		case "fail":
			fmt.Fprintf(&b, `<failure message="t%d broke">trace of t%d</failure><system-out>output of t%d</system-out>`, i, i, i)
		case "error":
			fmt.Fprintf(&b, `<error message="t%d crashed">stack</error>`, i)
		case "skip":
			b.WriteString(`<skipped message="not today"/>`)
		}
		b.WriteString("</testcase>\n")
	}
	b.WriteString("</testsuite>\n</testsuites>\n")
	return ReportFile{Path: path, Data: []byte(b.String()), Size: int64(b.Len())}
}

// finishTests finishes a job with the outcome, sending report files.
func finishTests(t *testing.T, e *Engine, j *Job, outcome string, files ...ReportFile) {
	t.Helper()
	code := 0
	if outcome == "failed" {
		code = 1
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: outcome, ExitCode: &code, Tests: &CollectedReports{Files: files}}); err != nil {
		t.Fatal(err)
	}
}

func hasTests(evs []Event) bool {
	for _, ev := range evs {
		if ev.Data.Tests != nil {
			return true
		}
	}
	return false
}

func countCases(t *testing.T, e *Engine, where string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM test_cases WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func cases(t *testing.T, e *Engine, runID string, q CaseQuery) *CasePage {
	t.Helper()
	p, err := e.RunTestCases(runID, q)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func names(p *CasePage) string {
	var out []string
	for _, c := range p.Cases {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

// A failing test step: its outcome stands, the event carries a summary,
// and the results are stored in the same transaction, to be read back
// failures first.
func TestTestResultsAreStoredWithTheEvent(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, reportsLinear, Trigger{Event: "manual"})
	j := take(t, e)
	if j.Reports == nil || j.Reports.Tests[0] != "reports/*.xml" || j.Reports.MaxBytes != DefaultTestUploadBytes {
		t.Fatalf("the job does not ask for the reports: %+v", j.Reports)
	}
	finishTests(t, e, j, "failed", junitFile("reports/unit.xml", "pass", "fail", "skip", "pass", "error"))
	want(t, statuses(t, e, id), "run", "failed", "a", "failed", "b", "skipped")

	evs, _ := e.Events(id)
	var sum *TestSummary
	for _, ev := range evs {
		if ev.Type == StepFinished {
			sum = ev.Data.Tests
		}
	}
	if sum == nil || sum.Total != 5 || sum.Passed != 2 || sum.Failed != 1 || sum.Errored != 1 || sum.Skipped != 1 || sum.Stored != 5 ||
		len(sum.Files) != 1 || sum.Files[0].Format != "junit" || sum.Files[0].Cases != 5 || sum.DurationMs != 150 {
		t.Fatalf("the event's summary: %+v", sum)
	}
	if n := countCases(t, e, "run_id = ?", id); n != 5 {
		t.Fatalf("%d results stored, want 5", n)
	}

	rt, err := e.RunTests(id)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Totals == nil || rt.Totals.Total != 5 || len(rt.Steps) != 1 || rt.Steps[0].Step != "a" || len(rt.Declared) != 3 {
		t.Fatalf("run tests: %+v", rt)
	}
	// Failures first (the slowest of them first), then the slowest.
	p := cases(t, e, id, CaseQuery{})
	if got := names(p); got != "t4,t1,t3,t2,t0" || p.Matching != 5 || p.Counts["failed"] != 2 || p.Counts["all"] != 5 {
		t.Fatalf("cases %s, %+v", got, p)
	}
	if got := names(cases(t, e, id, CaseQuery{Status: "failed"})); got != "t4,t1" {
		t.Fatalf("failed cases: %s", got)
	}
	if got := names(cases(t, e, id, CaseQuery{Search: "T3"})); got != "t3" {
		t.Fatalf("search: %s", got)
	}
	if got := names(cases(t, e, id, CaseQuery{Search: "broke"})); got != "t1" {
		t.Fatalf("search in messages: %s", got)
	}
	if got := names(cases(t, e, id, CaseQuery{Search: "%"})); got != "" {
		t.Fatalf("a %% is a percent sign, not a pattern: %s", got)
	}
	if got := cases(t, e, id, CaseQuery{Limit: 2, Offset: 2}); names(got) != "t3,t2" || got.Matching != 5 {
		t.Fatalf("second page: %s", names(got))
	}
	if _, err := e.RunTestCases(id, CaseQuery{Status: "weird"}); err == nil {
		t.Fatal("an unknown status was accepted")
	}
	failed := cases(t, e, id, CaseQuery{Status: "failed"}).Cases[1]
	if failed.Message != "t1 broke" || !failed.HasDetails || failed.Details != "" || failed.Key == "" {
		t.Fatalf("a failed result in a list: %+v", failed)
	}
	full, err := e.TestCase(id, failed.ID)
	if err != nil || full.Details != "trace of t1" || full.Output != "output of t1" {
		t.Fatalf("the full result: %+v, %v", full, err)
	}
	// Totals in lists and on steps.
	runs, _ := e.ListRuns(RunFilter{})
	if runs[0].Tests == nil || runs[0].Tests.Failed != 1 || runs[0].Tests.Errored != 1 {
		t.Fatalf("totals in the list: %+v", runs[0].Tests)
	}
	d, _ := e.GetRun(id)
	if d.StepList[0].Tests == nil || d.StepList[0].Tests.Total != 5 || d.StepList[1].Tests != nil || len(d.StepList[0].Reports) != 1 {
		t.Fatalf("step totals: %+v / %+v", d.StepList[0], d.StepList[1])
	}
}

// What cannot be read is described, never fatal, and never changes the
// step's outcome; reports from a step that declares none, or from a runner
// that failed, are not kept.
func TestReportsNeverChangeTheOutcome(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, reportsLinear, Trigger{Event: "manual"})
	j := take(t, e)
	finishTests(t, e, j, "succeeded",
		ReportFile{Path: "reports/page.xml", Data: []byte("<html><body>oops</body></html>")},
		ReportFile{Path: "reports/empty.xml"},
		ReportFile{Path: "reports/\x1b[31mbad\x00name.xml", Data: junitFile("", "pass").Data},
		junitFile("reports/ok.xml", "fail"))
	want(t, statuses(t, e, id), "run", "running", "a", "succeeded")
	rt, _ := e.RunTests(id)
	s := rt.Steps[0]
	if s.Total != 2 || s.Failed != 1 || len(s.Files) != 4 {
		t.Fatalf("step results: %+v", s)
	}
	if !strings.HasPrefix(s.Files[0].Error, "not a test report: ") || !strings.Contains(s.Files[0].Error, "html") ||
		!strings.HasPrefix(s.Files[1].Error, "not a test report") {
		t.Fatalf("files that are not reports: %+v", s.Files[:2])
	}
	if p := s.Files[2].Path; strings.ContainsAny(p, "\x1b\x00") {
		t.Fatalf("a file name kept control characters: %q", p)
	}

	// The runner gave up on the next step (it is shutting down): the step
	// runs again, and nothing it sent is kept.
	j = take(t, e)
	if err := e.Finish(j.LeaseID, Result{Outcome: "canceled", Tests: &CollectedReports{Files: []ReportFile{junitFile("r.xml", "pass")}}}); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "b", "queued")
	if n := countCases(t, e, "run_id = ? AND step_id = 'b'", id); n != 0 {
		t.Fatalf("a lost attempt's results were kept: %d", n)
	}

	// A step that declares no reports: whatever a runner sends is ignored.
	plain := create(t, e, linear, Trigger{Event: "manual"})
	for {
		j = take(t, e)
		if j.RunID == plain {
			break
		}
		ok(t, e, j, nil)
	}
	if j.Reports != nil {
		t.Fatalf("a step without reports asked for some: %+v", j.Reports)
	}
	finishTests(t, e, j, "succeeded", junitFile("r.xml", "pass"))
	if evs, _ := e.Events(plain); hasTests(evs) {
		t.Fatal("reports of a step that declares none were kept")
	}
}

// A step that was canceled while running still reports what it wrote.
func TestCanceledStepKeepsItsReports(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, reportsLinear, Trigger{Event: "manual"})
	j := take(t, e)
	if err := e.Cancel(id, "ann"); err != nil {
		t.Fatal(err)
	}
	finishTests(t, e, j, "canceled", junitFile("reports/partial.xml", "pass", "fail"))
	want(t, statuses(t, e, id), "run", "canceled", "a", "canceled")
	rt, _ := e.RunTests(id)
	if rt.Totals == nil || rt.Totals.Total != 2 {
		t.Fatalf("a canceled step's results: %+v", rt.Totals)
	}
	evs, _ := e.Events(id)
	for _, ev := range evs {
		if ev.Type == StepCanceled && ev.Data.Step == "a" && ev.Data.Tests == nil {
			t.Fatal("the step.canceled event has no summary")
		}
	}
}

// Each attempt's results are kept. A test that failed and then passed on a
// retry in the same run is flaky in that run; the totals are the last
// attempt's.
func TestRetriesAndFlakyTests(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `pipeline: demo
steps:
  test: {image: x, run: t, retry: 2, reports: {tests: "r.xml"}}
`, Trigger{Event: "manual"})
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "pass", "fail", "fail"))
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "pass", "pass", "fail"))
	finishTests(t, e, take(t, e), "succeeded", junitFile("r.xml", "pass", "pass", "pass"))
	want(t, statuses(t, e, id), "run", "succeeded", "test", "succeeded")
	if n := countCases(t, e, "run_id = ?", id); n != 9 {
		t.Fatalf("%d results kept over three attempts, want 9", n)
	}
	rt, _ := e.RunTests(id)
	if rt.Totals.Total != 3 || rt.Totals.Failed != 0 || rt.Flaky != 2 || len(rt.Steps[0].Attempts) != 3 || rt.Steps[0].Attempt != 3 {
		t.Fatalf("run tests: totals %+v flaky %d attempts %+v", rt.Totals, rt.Flaky, rt.Steps[0].Attempts)
	}
	p := cases(t, e, id, CaseQuery{Status: "flaky"})
	if names(p) != "t2,t1" || !p.Cases[0].Flaky || p.Counts["flaky"] != 2 {
		t.Fatalf("flaky: %s %+v", names(p), p.Counts)
	}
	// An earlier attempt can be looked at.
	first := cases(t, e, id, CaseQuery{Step: "test", Attempt: 1})
	if first.Counts["failed"] != 2 || first.Cases[0].Attempt != 1 {
		t.Fatalf("attempt 1: %+v", first.Counts)
	}
	if _, err := e.RunTestCases(id, CaseQuery{Step: "test", Attempt: 9}); err == nil {
		t.Fatal("an attempt that did not happen was found")
	}
	// A test the tool itself ran again counts as flaky too.
	tool := ReportFile{Path: "s.xml", Data: []byte(`<testsuite name="x"><testcase classname="A" name="again">` +
		`<flakyFailure message="timing"/></testcase></testsuite>`)}
	id2 := create(t, e, "pipeline: demo\nsteps:\n  test: {image: x, run: t, reports: {tests: s.xml}}\n", Trigger{Event: "manual"})
	finishTests(t, e, take(t, e), "succeeded", tool)
	if rt, _ := e.RunTests(id2); rt.Flaky != 1 || rt.Totals.Flaky != 1 || rt.Totals.Passed != 1 {
		t.Fatalf("a test the tool ran again: %+v %d", rt.Totals, rt.Flaky)
	}
}

// A test that passed in the previous run of the same ref and fails now is a
// new failure.
func TestNewFailures(t *testing.T) {
	e, _ := newEngine(t)
	one := "pipeline: demo\nsteps:\n  test: {image: x, run: t, reports: {tests: r.xml}}\n"
	main := Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: "a1"}
	first := create(t, e, one, main)
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "pass", "pass", "fail"))
	// Another branch in between does not count.
	other := create(t, e, one, Trigger{Event: "push", Ref: "refs/heads/dev", Branch: "dev", SHA: "d1"})
	finishTests(t, e, take(t, e), "succeeded", junitFile("r.xml", "fail", "fail", "pass"))
	main.SHA = "a2"
	second := create(t, e, one, main)
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "fail", "pass", "fail"))
	rt, _ := e.RunTests(second)
	if rt.NewFailures != 1 || rt.Previous == nil || rt.Previous.ID != first {
		t.Fatalf("new failures %d, previous %+v (first %s, other %s)", rt.NewFailures, rt.Previous, first, other)
	}
	p := cases(t, e, second, CaseQuery{Status: "failed"})
	for _, c := range p.Cases {
		if c.NewFailure != (c.Name == "t0") {
			t.Fatalf("%s new failure %v", c.Name, c.NewFailure)
		}
	}
	if rt, _ := e.RunTests(first); rt.Previous != nil || rt.NewFailures != 0 {
		t.Fatalf("the first run has a previous one: %+v", rt.Previous)
	}
}

// A re-run shows the results of the steps it reused, linked to the run
// where they ran; one of those results is readable through the re-run, and
// no other run's.
func TestRerunShowsReusedResults(t *testing.T) {
	e, _ := newEngine(t)
	src := `pipeline: demo
steps:
  test:   {image: x, run: t, reports: {tests: r.xml}}
  deploy: {image: x, run: d, needs: test}
`
	orig := create(t, e, src, Trigger{Event: "manual", SHA: "c1"})
	finishTests(t, e, take(t, e), "succeeded", junitFile("r.xml", "pass", "skip"))
	fail(t, e, take(t, e))
	re := rerun(t, e, orig, "deploy", "ann")
	rt, err := e.RunTests(re.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Totals == nil || rt.Totals.Total != 2 || rt.Steps[0].ReusedFrom != orig || rt.Steps[0].ReusedFromNumber != 1 {
		t.Fatalf("re-run tests: %+v", rt)
	}
	if sum, _ := e.Summary(re.ID); sum.Tests == nil || sum.Tests.Skipped != 1 {
		t.Fatalf("re-run totals in lists: %+v", sum.Tests)
	}
	p := cases(t, e, re.ID, CaseQuery{})
	if len(p.Cases) != 2 || p.Cases[0].Run != orig {
		t.Fatalf("re-run cases: %+v", p.Cases)
	}
	if n := countCases(t, e, "run_id = ?", re.ID); n != 0 {
		t.Fatalf("the re-run copied %d results", n)
	}
	if _, err := e.TestCase(re.ID, p.Cases[0].ID); err != nil {
		t.Fatalf("a reused result through the re-run: %v", err)
	}
	unrelated := create(t, e, src, Trigger{Event: "manual"})
	if _, err := e.TestCase(unrelated, p.Cases[0].ID); err == nil {
		t.Fatal("another run's result was readable through an unrelated run")
	}
}

// A run keeps at most MaxCasesPerRun results; the totals count them all.
// More than the server accepts is not read at all.
func TestRunAndUploadLimits(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now,
		Tests: TestLimits{MaxCasesPerRun: 5, MaxCasesPerProject: 100, MaxUploadBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	id := create(t, e, reportsLinear, Trigger{Event: "manual"})
	finishTests(t, e, take(t, e), "succeeded", junitFile("a.xml", "pass", "pass", "fail"), junitFile("b.xml", "pass", "pass", "pass", "pass"))
	finishTests(t, e, take(t, e), "succeeded", junitFile("c.xml", "pass"))
	big := junitFile("big.xml", strings.Split(strings.Repeat("pass,", 60), ",")[:60]...)
	finishTests(t, e, take(t, e), "succeeded", big)
	rt, _ := e.RunTests(id)
	a, b, c := rt.Steps[0], rt.Steps[1], rt.Steps[2]
	if a.Total != 7 || a.Stored != 5 || !a.Truncated || len(a.Notes) == 0 || !strings.Contains(a.Notes[0], "5 of the 7 results") {
		t.Fatalf("a: %+v", a)
	}
	if b.Total != 1 || b.Stored != 0 || !b.Truncated {
		t.Fatalf("b: %+v", b)
	}
	if c.Total != 0 || !strings.Contains(c.Files[0].Error, "more than the 4 KiB the server accepts") {
		t.Fatalf("c: %+v", c)
	}
	if n := countCases(t, e, "run_id = ?", id); n != 5 {
		t.Fatalf("%d results kept, want 5", n)
	}
	if _, err := Open(Options{Database: testdb.New(t), Tests: TestLimits{MaxCasesPerRun: 10, MaxCasesPerProject: 5}}); err == nil {
		t.Fatal("a project limit below the run limit was accepted")
	}
}

// A project keeps at most MaxCasesPerProject results: the oldest runs' go
// first, their totals stay. Two servers prune at the same moment, and the
// result is the same as one.
func TestPruningOldestRunsFirstWithTwoServers(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	lim := TestLimits{MaxCasesPerRun: 4, MaxCasesPerProject: 10}
	openWith := func() *Engine {
		e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Tests: lim})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		return e
	}
	a, b := openWith(), openWith()
	one := "pipeline: demo\nsteps:\n  test: {image: x, run: t, reports: {tests: r.xml}}\n"
	var ids []string
	for i := range 5 {
		srv := a
		if i%2 == 1 {
			srv = b
		}
		id := create(t, srv, one, Trigger{Event: "manual"})
		finishTests(t, srv, take(t, srv), "succeeded", junitFile("r.xml", "pass", "fail", "pass", "pass"))
		ids = append(ids, id)
	}
	// Another project, under its limit, is left alone.
	other, err := a.CreateRun(NewRun{Project: "other", Pipeline: parse(t, one), Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	finishTests(t, a, take(t, a), "succeeded", junitFile("r.xml", "pass"))

	var wg sync.WaitGroup
	for _, e := range []*Engine{a, b, a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); e.pruneTests() }()
	}
	wg.Wait()
	// The newest two runs fit (8 results); the third would make 12.
	for i, id := range ids {
		n := countCases(t, a, "run_id = ?", id)
		rt, _ := b.RunTests(id)
		if i < 3 && (n != 0 || !rt.Steps[0].Pruned || rt.Totals.Total != 4) {
			t.Fatalf("run %d: %d results, %+v", i+1, n, rt.Steps[0])
		}
		if i >= 3 && (n != 4 || rt.Steps[0].Pruned) {
			t.Fatalf("run %d was pruned: %d results", i+1, n)
		}
	}
	var stored, rows int
	if err := a.db.QueryRow(`SELECT COALESCE(SUM(stored), 0) FROM test_reports WHERE project = 'demo'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	rows = countCases(t, a, "project = 'demo'")
	if stored != rows || rows != 8 {
		t.Fatalf("stored %d, rows %d, want 8 and 8", stored, rows)
	}
	if n := countCases(t, a, "run_id = ?", other.ID); n != 1 {
		t.Fatalf("the other project lost results: %d", n)
	}
	// Listing a pruned run's results says there are none, without failing.
	if p := cases(t, a, ids[0], CaseQuery{}); len(p.Cases) != 0 {
		t.Fatalf("a pruned run lists %d results", len(p.Cases))
	}
}

// One test's history, and a project's overview: trend, slowest, most
// failing and flaky.
func TestHistoryAndOverview(t *testing.T) {
	e, _ := newEngine(t)
	one := "pipeline: demo\nsteps:\n  test: {image: x, run: t, retry: 1, reports: {tests: r.xml}}\n"
	main := func(sha string) Trigger {
		return Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: sha}
	}
	// #1: t1 fails. #2 (same commit): t1 passes, so it is flaky on a1.
	// #3: t1 and t2 fail, then pass on the retry: flaky in the run.
	r1 := create(t, e, one, main("a1"))
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "pass", "fail", "pass"))
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "pass", "fail", "pass"))
	r2 := create(t, e, one, main("a1"))
	finishTests(t, e, take(t, e), "succeeded", junitFile("r.xml", "pass", "pass", "pass"))
	r3 := create(t, e, one, main("a3"))
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "pass", "fail", "fail"))
	finishTests(t, e, take(t, e), "succeeded", junitFile("r.xml", "pass", "pass", "pass"))
	dev := create(t, e, one, Trigger{Event: "push", Ref: "refs/heads/dev", Branch: "dev", SHA: "d1"})
	finishTests(t, e, take(t, e), "succeeded", junitFile("r.xml", "pass", "pass", "pass"))

	var key string
	p := cases(t, e, r3, CaseQuery{Search: "t1"})
	key = p.Cases[0].Key
	h, err := e.TestHistory("demo", key, "refs/heads/main", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range h.Results {
		got = append(got, fmt.Sprintf("#%d %s %v", r.Number, r.Status, r.Flaky))
	}
	if strings.Join(got, "; ") != "#3 passed true; #2 passed false; #1 failed false" || h.Name != "t1" || h.Suite != "demo.Suite" {
		t.Fatalf("history: %v (%+v)", got, h)
	}
	if h, _ := e.TestHistory("demo", key, "", 0); len(h.Results) != 4 || h.Results[0].Run != dev {
		t.Fatalf("history on every ref: %+v", h.Results)
	}
	if _, err := e.TestHistory("demo", "nope", "", 0); err == nil {
		t.Fatal("a test that never ran has a history")
	}
	if _, err := e.TestHistory("elsewhere", key, "", 0); err == nil {
		t.Fatal("a test's history leaked into another project")
	}

	o, err := e.TestOverview("demo", "refs/heads/main", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Runs) != 3 || o.Runs[0].ID != r1 || o.Runs[1].ID != r2 || o.Runs[2].Tests.Total != 3 || o.Refs[0] != "refs/heads/dev" {
		t.Fatalf("overview runs: %+v refs %v", o.Runs, o.Refs)
	}
	if o.Slowest[0].Name != "t2" || o.Slowest[0].AvgMs != 30 {
		t.Fatalf("slowest: %+v", o.Slowest)
	}
	// Results that count only: t1 failed in #1 (attempt 2); #3's failures
	// were retried away.
	if len(o.Failing) != 1 || o.Failing[0].Name != "t1" || o.Failing[0].Failures != 1 {
		t.Fatalf("failing: %+v", o.Failing)
	}
	flaky := map[string]int{}
	for _, f := range o.Flaky {
		flaky[f.Name] = f.Commits
	}
	if len(flaky) != 2 || flaky["t1"] != 2 || flaky["t2"] != 1 {
		t.Fatalf("flaky: %+v", o.Flaky)
	}
	// With no ref, the latest ref with results.
	if o, _ := e.TestOverview("demo", "", 5); o.Ref != "refs/heads/dev" || len(o.Runs) != 1 {
		t.Fatalf("default ref: %s %d", o.Ref, len(o.Runs))
	}
	if o, _ := e.TestOverview("nobody", "", 5); len(o.Runs) != 0 {
		t.Fatalf("a project with no runs: %+v", o)
	}
}
