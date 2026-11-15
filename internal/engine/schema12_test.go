package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// newestLinear is a pipeline that uses keys of schema 12 (reports.findings).
const newestLinear = `
pipeline: demo
steps:
  a: {image: x, run: a, reports: {tests: "reports/*.xml", findings: "reports/*.sarif"}}
  b: {image: x, run: b, needs: a, reports: {tests: "reports/*.xml", findings: "reports/*.sarif"}}
  c: {image: x, run: c, needs: b, reports: {tests: "reports/*.xml", findings: "reports/*.sarif"}}
`

// Schema 12 against a build of schema 11: runs whose pipeline keeps or
// fetches artifacts, or declares findings or an SBOM, are left alone by
// the older build, which neither creates nor touches them, while it runs
// plain runs; the newer build carries them on and stores their findings.
func TestOlderBuildLeavesArtifactRunsAlone(t *testing.T) {
	n := schemaArtifacts
	if migrations[n-1].compat != additive {
		t.Fatalf("migration %d must be additive", n)
	}
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	var newLog, oldLog syncBuffer
	cur := mustOpenAs(t, addr, clk, n, &newLog)
	findings := create(t, cur, newestLinear, Trigger{Event: "manual"})
	kept := create(t, cur, artifactsPipeline, Trigger{Event: "manual"})
	plain := create(t, cur, linear, Trigger{Event: "manual"})
	for _, id := range []string{findings, kept} {
		if sum, _ := cur.Summary(id); sum.MinSchema != n {
			t.Fatalf("run %s has min_schema %d, want %d", id, sum.MinSchema, n)
		}
	}
	if sum, _ := cur.Summary(plain); sum.MinSchema >= n {
		t.Fatalf("a plain run needs schema %d", sum.MinSchema)
	}
	old := mustOpenAs(t, addr, clk, n-1, &oldLog)
	if !strings.Contains(oldLog.String(), "running against a database written by a newer version") {
		t.Fatalf("older build's log: %s", oldLog.String())
	}
	for _, id := range []string{findings, kept} {
		d, err := old.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if !d.NeedsNewerServer || !strings.Contains(strings.Join(d.NotUnderstood, " "), "schema 12") {
			t.Fatalf("older build's view of %s: %v", id, d.NotUnderstood)
		}
		if err := old.Cancel(id, "x"); !errors.Is(err, ErrNeedsNewer) {
			t.Fatalf("older build cancelled %s: %v", id, err)
		}
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, artifactsPipeline), Trigger: Trigger{Event: "manual"},
		Source: Source{Kind: "dir", Path: "/x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("older build made a run with artifacts: %v", err)
	}
	for range 3 {
		j := take(t, old)
		if j.RunID != plain || j.Artifacts != nil || len(j.Fetch) > 0 {
			t.Fatalf("older build leased %s/%s", j.RunID, j.StepID)
		}
		ok(t, old, j, nil)
	}
	none(t, old)
	for range 5 {
		j := take(t, cur)
		zero := 0
		if err := cur.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero,
			Findings: &CollectedReports{Files: []ReportFile{sarif("semgrep", "a")}}}); err != nil {
			t.Fatal(err)
		}
	}
	none(t, cur)
	want(t, statuses(t, cur, findings), "run", "succeeded")
	if rf, _ := cur.RunFindings(findings, FindingQuery{}); rf.Counts == nil || rf.Counts.Total != 3 {
		t.Fatalf("findings of the run %+v", rf.Counts)
	}
}
