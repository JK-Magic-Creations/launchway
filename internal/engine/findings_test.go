package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/sbom"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const findingsPipeline = `
pipeline: demo
steps:
  scan: {image: x, run: scan, reports: {findings: "reports/*.sarif"}}
`

// sarif makes a SARIF file of one tool with one result per rule given, each
// in its own file at line 1.
func sarif(tool string, rules ...string) ReportFile {
	var res []string
	for _, r := range rules {
		res = append(res, fmt.Sprintf(`{"ruleId":%q,"level":"error","message":{"text":"problem %s"},
			"locations":[{"physicalLocation":{"artifactLocation":{"uri":"src/%s.go"},"region":{"startLine":1}}}]}`, r, r, r))
	}
	return ReportFile{Path: "reports/" + tool + ".sarif", Data: []byte(fmt.Sprintf(
		`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":%q,"rules":[{"id":"x","helpUri":"https://example.com/x"}]}},"results":[%s]}]}`,
		tool, strings.Join(res, ",")))}
}

func scanRun(t *testing.T, e *Engine, ref string, files ...ReportFile) string {
	t.Helper()
	id := create(t, e, findingsPipeline, Trigger{Event: "push", Ref: ref, Branch: strings.TrimPrefix(ref, "refs/heads/")})
	j := take(t, e)
	zero := 0
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Findings: &CollectedReports{Files: files}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func statusOf(rf *RunFindings) map[string]string {
	out := map[string]string{}
	for _, f := range rf.Findings {
		out[f.RuleID] = f.Status
	}
	return out
}

// Findings are stored with the event, and each run says which are new,
// still there or fixed, comparing only tools that ran both times.
func TestFindingsNewExistingFixed(t *testing.T) {
	e, _ := newEngine(t)
	first := scanRun(t, e, "refs/heads/main", sarif("semgrep", "a", "b"), sarif("trivy", "cve-1"))
	rf, err := e.RunFindings(first, FindingQuery{})
	if err != nil || rf.Counts.Total != 3 || rf.Counts.High != 3 || rf.Previous != nil || rf.Statuses != (StatusCounts{}) {
		t.Fatalf("first run %+v %v", rf, err)
	}
	evs, _ := e.Events(first)
	var sum *FindingsSummary
	for _, ev := range evs {
		if ev.Type == StepFinished {
			sum = ev.Data.Findings
		}
	}
	if sum == nil || sum.Total != 3 || sum.Stored != 3 || len(sum.Tools) != 2 {
		t.Fatalf("the event's summary %+v", sum)
	}
	// Another branch is not compared with main.
	scanRun(t, e, "refs/heads/dev", sarif("semgrep", "z"))
	// b fixed, c new, a still there; trivy did not run, so cve-1 is not
	// "fixed".
	second := scanRun(t, e, "refs/heads/main", sarif("semgrep", "a", "c"))
	rf, _ = e.RunFindings(second, FindingQuery{})
	if rf.Previous == nil || rf.Previous.ID != first {
		t.Fatalf("compared with %+v", rf.Previous)
	}
	if got := statusOf(rf); got["a"] != FindingExisting || got["c"] != FindingNew || rf.Statuses != (StatusCounts{New: 1, Existing: 1, Fixed: 1}) {
		t.Fatalf("statuses %v %+v", got, rf.Statuses)
	}
	fixed, _ := e.RunFindings(second, FindingQuery{Status: FindingFixed})
	if len(fixed.Findings) != 1 || fixed.Findings[0].RuleID != "b" || fixed.Findings[0].Status != FindingFixed {
		t.Fatalf("fixed %+v", fixed.Findings)
	}
	// Filters.
	for q, want := range map[*FindingQuery]int{
		{Status: FindingNew}:           1,
		{Search: "PROBLEM A"}:          1,
		{Tool: "trivy"}:                0,
		{Severities: []string{"low"}}:  0,
		{Severities: []string{"high"}}: 2,
		{Limit: 1}:                     1,
		{Offset: 1}:                    1,
		{Step: "nope"}:                 0,
	} {
		rf, _ := e.RunFindings(second, *q)
		if len(rf.Findings) != want {
			t.Errorf("%+v: %d findings, want %d", *q, len(rf.Findings), want)
		}
	}
	// The project view: the latest run's open findings, each with the run
	// that first had it.
	ps, err := e.ProjectSecurity("demo", "refs/heads/main", 10)
	if err != nil || len(ps.Runs) != 2 || ps.Latest.ID != second || len(ps.Open) != 2 {
		t.Fatalf("project %+v %v", ps, err)
	}
	for _, f := range ps.Open {
		want := first
		if f.RuleID == "c" {
			want = second
		}
		if f.FirstSeen == nil || f.FirstSeen.ID != want {
			t.Errorf("%s first seen %+v", f.RuleID, f.FirstSeen)
		}
	}
	if sum, _ := e.Summary(second); sum.Findings == nil || sum.Findings.Total != 2 {
		t.Fatalf("run summary %+v", sum.Findings)
	}
}

// A file that is not SARIF, and SARIF files that came to more than the
// server accepts, are described, never failing the step.
func TestFindingFilesThatCannotBeRead(t *testing.T) {
	e, _ := newEngine(t)
	id := scanRun(t, e, "refs/heads/main", ReportFile{Path: "reports/x.sarif", Data: []byte("<html>")}, sarif("semgrep", "a"))
	rf, _ := e.RunFindings(id, FindingQuery{})
	if rf.Counts.Total != 1 || len(rf.Steps) != 1 || len(rf.Steps[0].Files) != 2 || rf.Steps[0].Files[0].Error == "" {
		t.Fatalf("%+v", rf.Steps)
	}
	want(t, statuses(t, e, id), "run", "succeeded")
	// The runner sent none at all.
	create(t, e, findingsPipeline, Trigger{Event: "manual"})
	ok(t, e, take(t, e), nil)
}

// A re-run shows the findings of a step it reused, from the run where it
// ran.
func TestRerunShowsReusedFindings(t *testing.T) {
	e, _ := newEngine(t)
	src := findingsPipeline + "  after: {image: x, run: y, needs: scan}\n"
	id := create(t, e, src, Trigger{Event: "manual"})
	j := take(t, e)
	zero := 0
	e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Findings: &CollectedReports{Files: []ReportFile{sarif("semgrep", "a")}}})
	ok(t, e, take(t, e), nil)
	re, err := e.Rerun(RerunRequest{Run: id, From: "after", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), nil)
	rf, _ := e.RunFindings(re.ID, FindingQuery{})
	if rf.Counts == nil || rf.Counts.Total != 1 || rf.Steps[0].ReusedRun == nil || rf.Steps[0].ReusedRun.ID != id {
		t.Fatalf("re-run findings %+v", rf.Steps)
	}
}

// Each run keeps at most so many findings; a project at most so many, the
// oldest runs' removed first by two servers at once, counts kept.
func TestFindingLimitsAndPruningWithTwoServers(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	lim := FindingLimits{MaxPerRun: 3, MaxPerProject: 5}
	open := func() *Engine {
		e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Findings: lim})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		return e
	}
	a, b := open(), open()
	var ids []string
	for i := range 4 {
		ids = append(ids, scanRun(t, a, "refs/heads/main", sarif("semgrep", fmt.Sprint("r", i, "a"), fmt.Sprint("r", i, "b"), fmt.Sprint("r", i, "c"), fmt.Sprint("r", i, "d"))))
	}
	rf, _ := a.RunFindings(ids[3], FindingQuery{})
	if rf.Counts.Total != 4 || len(rf.Findings) != 3 || rf.Steps[0].Stored != 3 || !rf.Counts.Truncated {
		t.Fatalf("over the run's limit: %+v", rf.Steps)
	}
	var wg sync.WaitGroup
	for range 3 {
		for _, e := range []*Engine{a, b} {
			wg.Add(1)
			go func() { defer wg.Done(); e.pruneFindings() }()
		}
	}
	wg.Wait()
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&n)
	if n != 3 {
		t.Fatalf("%d findings kept, want the newest run's 3", n)
	}
	old, _ := a.RunFindings(ids[0], FindingQuery{})
	if old.Counts.Total != 4 || len(old.Findings) != 0 || !old.Steps[0].Pruned {
		t.Fatalf("a pruned run: %+v", old.Steps)
	}
}

// SBOM components: summarised, searchable by name (any case) and version,
// only in the projects the caller may see, bounded.
func TestComponentSearch(t *testing.T) {
	e, _ := newEngine(t)
	add := func(project, name, version string) string {
		r, err := e.CreateRun(NewRun{Project: project, Pipeline: parse(t, findingsPipeline), Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}})
		if err != nil {
			t.Fatal(err)
		}
		err = e.withRun(r.ID, func(c *cmd) error {
			c.st.Number = r.Number
			c.s12().sbom[stepAttempt{"scan", 1}] = &keptSBOM{list: []sbom.Component{{Name: name, Version: version}}, licences: map[string]int{"MIT": 1}}
			return c.persistSBOM(1, "scan", 1, &SBOMSummary{Components: 1, Stored: 1, Licences: map[string]int{"MIT": 1}})
		})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	add("pay", "left-pad", "1.3.0")
	add("web", "Left-Pad", "1.3.0")
	add("web", "left-pad", "1.0.0")
	all, err := e.SearchComponents("LEFT-PAD", "", nil, 0)
	if err != nil || len(all.Hits) != 3 || len(all.Projects) != 2 {
		t.Fatalf("all %+v %v", all, err)
	}
	v, _ := e.SearchComponents("left-pad", "1.3.0", nil, 0)
	if len(v.Hits) != 2 {
		t.Fatalf("by version %+v", v.Hits)
	}
	only, _ := e.SearchComponents("left-pad", "", []string{"pay"}, 0)
	if len(only.Hits) != 1 || only.Hits[0].Project != "pay" {
		t.Fatalf("visible only %+v", only.Hits)
	}
	none, _ := e.SearchComponents("left-pad", "", []string{}, 0)
	if len(none.Hits) != 0 {
		t.Fatal("an empty list of projects found something")
	}
	limited, _ := e.SearchComponents("left-pad", "", nil, 1)
	if len(limited.Hits) != 1 || !limited.More {
		t.Fatalf("limited %+v", limited)
	}
	for _, bad := range []string{"", strings.Repeat("x", 301)} {
		if _, err := e.SearchComponents(bad, "", nil, 0); err == nil {
			t.Errorf("searched for %q", bad)
		}
	}
}

// An older build reads events whose finishing event carries schema-12
// summaries on a run it understands (the summaries only record), and
// carries the run on, ignoring them.
func TestOlderBuildIgnoresFindingSummaries(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	var buf syncBuffer
	cur := mustOpenAs(t, addr, clk, 0, &buf)
	id := create(t, cur, linear, Trigger{Event: "manual"})
	j := take(t, cur)
	if err := cur.Finish(j.LeaseID, Result{Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	// As a newer server would record it: summaries of findings, an SBOM
	// and artifacts on the event that ends the step.
	data, _ := json.Marshal(Payload{Step: "a", Attempt: 1, Outcome: "succeeded",
		Findings:  &FindingsSummary{FindingCounts: FindingCounts{Total: 1, High: 1}, Tools: []string{"x"}, Files: []FindingFile{}, Stored: 1},
		SBOM:      &SBOMSummary{Components: 2, Licences: map[string]int{"MIT": 2}, Files: []SBOMFile{}},
		Artifacts: []ArtifactSummary{{ID: "art_x", Name: "x", Files: 1, Bytes: 3}}})
	if _, err := cur.db.Exec(`UPDATE events SET data = ? WHERE run_id = ? AND type = 'step.finished'`, string(data), id); err != nil {
		t.Fatal(err)
	}
	old := mustOpenAs(t, addr, clk, schemaArtifacts-1, &buf)
	d, err := old.GetRun(id)
	if err != nil || d.NeedsNewerServer || len(d.NotUnderstood) > 0 || d.Findings != nil {
		t.Fatalf("older build's view: %+v %v", d, err)
	}
	ok(t, old, take(t, old), nil)
	ok(t, old, take(t, old), nil)
	want(t, statuses(t, old, id), "run", "succeeded")
}
