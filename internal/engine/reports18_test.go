package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A tiny PNG: the signature, then enough for content sniffing.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{7}, 64)...)

const coveragePipeline = `
pipeline: demo
steps:
  unit:
    image: x
    run: make test
    retry: 2
    reports:
      tests: reports/junit.xml
      coverage: { paths: coverage/lcov.info, fail_under: { lines: 80 } }
`

// lcov makes an LCOV file with one source file, covered lines of total.
func lcov(file string, covered, total int) ReportFile {
	var b strings.Builder
	fmt.Fprintf(&b, "TN:\nSF:%s\n", file)
	for i := 1; i <= total; i++ {
		hits := 0
		if i <= covered {
			hits = 3
		}
		fmt.Fprintf(&b, "DA:%d,%d\n", i, hits)
	}
	b.WriteString("end_of_record\n")
	return ReportFile{Path: "coverage/lcov.info", Data: []byte(b.String())}
}

func finishCoverage(t *testing.T, e *Engine, j *Job, outcome string, cov ...ReportFile) {
	t.Helper()
	code := 0
	if outcome == "failed" {
		code = 1
	}
	res := Result{Outcome: outcome, ExitCode: &code, Tests: &CollectedReports{Files: []ReportFile{junitFile("reports/junit.xml", "pass")}},
		Coverage: &CollectedReports{Files: cov}}
	if err := e.Finish(j.LeaseID, res); err != nil {
		t.Fatal(err)
	}
}

// Coverage is kept with the run, fails a step under its fail_under without
// retrying it, and compares with the previous run of the branch.
func TestCoverageAndItsThreshold(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	main := Trigger{Event: "push", Ref: "refs/heads/main"}

	first := create(t, e, coveragePipeline, main)
	j := take(t, e)
	if j.Reports == nil || len(j.Reports.Coverage) != 1 || j.Reports.CoverageMaxBytes != DefaultCoverageUploadBytes {
		t.Fatalf("job reports %+v", j.Reports)
	}
	finishCoverage(t, e, j, "succeeded", lcov("src/a.go", 7, 10))
	s := stepOf(t, e, first, "unit")
	if s.Status != StepFailed || !strings.Contains(s.Error, "line coverage is 70% (7 of 10), under the 80% this step requires") {
		t.Fatalf("step %s: %q", s.Status, s.Error)
	}
	if s.Attempt != 1 {
		t.Fatalf("a step that fails its coverage was retried (attempt %d)", s.Attempt)
	}
	evs, _ := e.Events(first)
	var sum *CoverageSummary
	for _, ev := range evs {
		if ev.Type == StepFinished {
			sum = ev.Data.Coverage
		}
	}
	if sum == nil || sum.Lines != (CoverageCounter{7, 10}) || sum.SourceFiles != 1 || sum.Stored != 1 || sum.FailedUnder == "" ||
		len(sum.Reports) != 1 || sum.Reports[0].Format != "lcov" {
		t.Fatalf("event summary %+v", sum)
	}

	second := create(t, e, coveragePipeline, main)
	finishCoverage(t, e, take(t, e), "succeeded", lcov("src/a.go", 9, 10), func() ReportFile {
		f := lcov("src/b.go", 1, 2)
		f.Path = "coverage/b.info"
		return f
	}())
	if s := stepOf(t, e, second, "unit"); s.Status != StepSucceeded {
		t.Fatalf("90%% failed the step: %q", s.Error)
	}
	rc, err := e.RunCoverage(second, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if rc.Totals == nil || rc.Totals.Lines != (CoverageCounter{10, 12}) || len(rc.Steps) != 1 || rc.Steps[0].Threshold == nil ||
		rc.Steps[0].Threshold.Lines != 80 || rc.Previous == nil || rc.Previous.Run.ID != first || rc.Previous.Totals.Lines.Covered != 7 ||
		rc.Base != nil {
		t.Fatalf("run coverage %+v", rc)
	}
	files, err := e.RunCoverageFiles(second, CoverageFileQuery{Sort: "lines"})
	if err != nil {
		t.Fatal(err)
	}
	if files.Matching != 2 || files.Files[0].Path != "src/b.go" || files.Files[1].Change == nil || *files.Files[1].Change != 20 ||
		files.Files[0].Change != nil || len(files.Packages) != 1 || files.Packages[0].Files != 2 {
		t.Fatalf("files %+v", files)
	}
	if f, _ := e.RunCoverageFiles(second, CoverageFileQuery{Search: "B.GO"}); f.Matching != 1 {
		t.Fatalf("search: %+v", f)
	}
	if _, err := e.RunCoverageFiles(second, CoverageFileQuery{Sort: "x; DROP"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad sort: %v", err)
	}
	// A branch compares with the default branch too.
	feature := create(t, e, coveragePipeline, Trigger{Event: "push", Ref: "refs/heads/feature"})
	finishCoverage(t, e, take(t, e), "succeeded", lcov("src/a.go", 10, 10))
	rc, _ = e.RunCoverage(feature, "refs/heads/main")
	if rc.Previous != nil || rc.Base == nil || rc.Base.Run.ID != second {
		t.Fatalf("feature branch coverage %+v", rc)
	}
	trend, err := e.ProjectCoverage("demo", "refs/heads/main", 10)
	if err != nil || len(trend.Runs) != 2 || trend.Runs[0].ID != first || trend.Runs[1].Totals.Lines.Total != 12 {
		t.Fatalf("trend %+v %v", trend, err)
	}
	// A step that failed anyway keeps its coverage, and no threshold
	// message replaces its own reason.
	failedRun := create(t, e, coveragePipeline, main)
	j = take(t, e)
	finishCoverage(t, e, j, "failed", lcov("src/a.go", 1, 10))
	s = stepOf(t, e, failedRun, "unit")
	if strings.Contains(s.Error, "Coverage") {
		t.Fatalf("reason %q", s.Error)
	}
	// Unreadable reports cannot pass a threshold.
	create(t, e, coveragePipeline, main)
	for {
		j = take(t, e)
		if j.Attempt == 1 && j.RunID != failedRun {
			break
		}
		fail(t, e, j)
	}
	finishCoverage(t, e, j, "succeeded", ReportFile{Path: "coverage/lcov.info", Data: []byte("<html>not coverage</html>")})
	s = stepOf(t, e, j.RunID, "unit")
	if !strings.Contains(s.Error, "no coverage report could be read") {
		t.Fatalf("reason %q", s.Error)
	}
}

// A runner that does not collect coverage is not given a step that
// declares it.
func TestOlderRunnersDoNotGetCoverageSteps(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	create(t, e, coveragePipeline, Trigger{Event: "manual"})
	old := RunnerInfo{ID: "old", Name: "old", Features: FeatureArtifacts}
	if j, err := e.Lease(context.Background(), old, 0); err != nil || j != nil {
		t.Fatalf("an older runner got %+v, %v", j, err)
	}
}

// A build of schema 17 leaves runs that use schema 18's keys alone.
func TestOlderBuildLeavesReportRunsAlone(t *testing.T) {
	n := schemaReports
	if migrations[n-1].compat != additive {
		t.Fatalf("migration %d must be additive", n)
	}
	if pipeline.SchemaReports != schemaReports {
		t.Fatalf("pipeline.SchemaReports %d, engine %d", pipeline.SchemaReports, schemaReports)
	}
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	var newLog, oldLog syncBuffer
	cur := mustOpenAs(t, addr, clk, n, &newLog)
	rich := create(t, cur, coveragePipeline, Trigger{Event: "manual"})
	plain := create(t, cur, reportsLinear, Trigger{Event: "manual"})
	if sum, _ := cur.Summary(rich); sum.MinSchema != n {
		t.Fatalf("min_schema %d", sum.MinSchema)
	}
	old := mustOpenAs(t, addr, clk, n-1, &oldLog)
	d, err := old.GetRun(rich)
	if err != nil {
		t.Fatal(err)
	}
	if !d.NeedsNewerServer {
		t.Fatalf("older build's view: %v", d.NotUnderstood)
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, coveragePipeline), Trigger: Trigger{Event: "manual"},
		Source: Source{Kind: "dir", Path: "/x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("older build made a coverage run: %v", err)
	}
	// It runs the plain run, writing results without the new columns.
	for range 3 {
		j := takeAs(t, old, runner)
		if j.RunID != plain {
			t.Fatalf("older build leased %s/%s", j.RunID, j.StepID)
		}
		if err := old.Finish(j.LeaseID, Result{Outcome: "succeeded", Tests: &CollectedReports{Files: []ReportFile{junitFile("r.xml", "pass", "fail")}}}); err != nil {
			t.Fatal(err)
		}
	}
	// The current build reads them, and carries the other run on.
	if p, err := cur.RunTestCases(plain, CaseQuery{}); err != nil || p.Matching != 6 || len(p.Cases[0].Tags) != 0 {
		t.Fatalf("cases %+v %v", p, err)
	}
	if j := take(t, cur); j.RunID != rich {
		t.Fatalf("leased %s", j.RunID)
	}
}

const attachmentsPipeline = `
pipeline: demo
steps:
  e2e:
    image: x
    run: npx playwright test
    reports:
      tests: { paths: [results/*.xml, results/*.json], attachments: test-results }
      html: { name: site, path: site }
`

// junitWithAttachments names attachments the way Playwright's JUnit
// reporter does.
func junitWithAttachments(lines ...string) ReportFile {
	var b strings.Builder
	b.WriteString(`<testsuites><testsuite name="shop"><testcase classname="shop.spec.ts" name="checkout" time="1.5">` +
		`<failure message="expected 200">trace</failure><system-out>`)
	for _, l := range lines {
		b.WriteString("\n[[ATTACHMENT|" + l + "]]\n")
	}
	b.WriteString(`</system-out></testcase></testsuite></testsuites>`)
	return ReportFile{Path: "results/junit.xml", Data: []byte(b.String())}
}

// cucumberWithEmbedding is a Cucumber JSON report whose failing step embeds
// a screenshot.
func cucumberWithEmbedding(data []byte) ReportFile {
	doc := `[{"uri":"features/login.feature","name":"Login","keyword":"Feature","tags":[{"name":"@smoke"}],"elements":[{"type":"scenario",
"keyword":"Scenario","name":"wrong password","id":"login;wrong-password","tags":[{"name":"@smoke"},{"name":"@auth"}],"steps":[
{"keyword":"Given ","name":"I open the page","result":{"status":"passed","duration":1000000}},
{"keyword":"Then ","name":"I am refused","result":{"status":"failed","duration":2000000,"error_message":"expected refusal"},
 "embeddings":[{"mime_type":"image/png","data":"` + base64.StdEncoding.EncodeToString(data) + `"}]}]}]}]`
	return ReportFile{Path: "results/cucumber.json", Data: []byte(doc)}
}

// Attachments are shown only from the files kept of the attachments
// directory, or from what the reports carry inline, which the server keeps.
func TestTestAttachments(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := create(t, e, attachmentsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	if j.Artifacts == nil || len(j.Artifacts.Keep) != 2 || j.Artifacts.Keep[0].Name != "test-attachments" ||
		j.Artifacts.Keep[0].When != "always" || j.Artifacts.Keep[0].MaxFiles != DefaultAttachmentFiles || j.Artifacts.Keep[1].Name != "report-site" {
		t.Fatalf("job artifacts %+v", j.Artifacts)
	}
	trace := []byte("PK\x03\x04 a trace")
	keep(t, e, j.LeaseID, "test-attachments", []upFile{
		{"test-results/checkout/shot.png", pngBytes, 0o644},
		{"test-results/checkout/trace.zip", trace, 0o644},
	})
	keep(t, e, j.LeaseID, "report-site", []upFile{
		{"site/index.html", []byte("<html><script>fetch('/api/v1/runs')</script></html>"), 0o644},
		{"site/data/app.js", []byte("console.log(1)"), 0o644},
	})
	// An attachment directory beyond its limit is refused whole.
	if _, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "test-embedded", Files: []ArtifactFileInfo{}}); err == nil {
		t.Fatal("an empty artifact was accepted")
	}
	one := 1
	err := e.Finish(j.LeaseID, Result{Outcome: "failed", ExitCode: &one, Tests: &CollectedReports{Workspace: "/builds/ws", Files: []ReportFile{
		junitWithAttachments(
			"/builds/ws/test-results/checkout/shot.png", // absolute, in the workspace
			"test-results/checkout/trace.zip",           // relative to the workspace
			"/etc/passwd",                               // outside the workspace
			"../../test-results/checkout/shot.png",      // leaves it
			"/builds/ws/src/secret.txt",                 // in the workspace, not in the directory
			"test-results/checkout/missing.png",         // not kept
		),
		cucumberWithEmbedding(pngBytes),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := e.RunTestCases(id, CaseQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var junitCase, cukeCase int64
	for _, c := range page.Cases {
		switch c.Name {
		case "checkout":
			junitCase = c.ID
		case "wrong password":
			cukeCase = c.ID
			if strings.Join(c.Tags, ",") != "smoke,auth" {
				t.Errorf("tags %v", c.Tags)
			}
		}
	}
	c, err := e.TestCase(id, junitCase)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Attachments) != 6 {
		t.Fatalf("attachments %+v", c.Attachments)
	}
	want := []struct{ path, problem, kind string }{
		{"test-results/checkout/shot.png", "", "image"},
		{"test-results/checkout/trace.zip", "", "trace"},
		{"", "outside the workspace", ""},
		{"", "outside the attachments directory", ""},
		{"", "outside the attachments directory", ""},
		{"", "is not among the files kept", ""},
	}
	for i, w := range want {
		a := c.Attachments[i]
		if a.Path != w.path || !strings.Contains(a.Problem, w.problem) || (w.problem == "" && a.Problem != "") || (w.kind != "" && a.Kind != w.kind) {
			t.Errorf("attachment %d: %+v, want %+v", i, a, w)
		}
	}
	f, err := e.OpenTestAttachment(context.Background(), id, junitCase, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f.Body)
	f.Body.Close()
	if !bytes.Equal(got, pngBytes) {
		t.Fatal("the screenshot read back differently")
	}
	for _, n := range []int{2, 6, -1} {
		if _, err := e.OpenTestAttachment(context.Background(), id, junitCase, n, 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("attachment %d: %v", n, err)
		}
	}
	// The embedded screenshot is kept by the server, as test-embedded.
	cc, err := e.TestCase(id, cukeCase)
	if err != nil {
		t.Fatal(err)
	}
	if len(cc.Attachments) != 1 || cc.Attachments[0].Artifact != "test-embedded" || cc.Attachments[0].Kind != "image" ||
		!strings.HasSuffix(cc.Attachments[0].Path, ".png") || len(cc.Steps) != 2 || cc.Steps[1].Status != "failed" ||
		len(cc.Steps[1].Attachments) != 1 {
		t.Fatalf("cucumber case %+v", cc)
	}
	f, err = e.OpenTestAttachment(context.Background(), id, cukeCase, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(f.Body)
	f.Body.Close()
	if !bytes.Equal(got, pngBytes) {
		t.Fatal("the embedded screenshot read back differently")
	}
	// Results of another run are not reachable through this one.
	other := create(t, e, reportsLinear, Trigger{Event: "manual"})
	if _, err := e.OpenTestAttachment(context.Background(), other, junitCase, 0, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("through another run: %v", err)
	}
	evs, _ := e.Events(id)
	for _, ev := range evs {
		if ev.Type == StepFinished && (ev.Data.Tests.Attachments != 3 || ev.Data.Tests.AttachmentsMissing != 4) {
			t.Fatalf("summary %+v", ev.Data.Tests)
		}
	}

	// The HTML report is listed and its files opened only by path below it.
	reps, err := e.RunHTMLReports(id)
	if err != nil || len(reps.Reports) != 1 || !reps.Reports[0].HasIndex || reps.Reports[0].Name != "site" || reps.Reports[0].Files != 2 {
		t.Fatalf("reports %+v %v", reps, err)
	}
	art := reps.Reports[0].Artifact
	h, err := e.OpenHostedFile(context.Background(), art, "data/app.js", 0)
	if err != nil {
		t.Fatal(err)
	}
	h.Body.Close()
	for _, p := range []string{"../site/index.html", "/index.html", "data/../index.html", "", "nope.html"} {
		if _, err := e.OpenHostedFile(context.Background(), art, p, 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("hosted %q: %v", p, err)
		}
	}
	// Only HTML reports are hosted, never other artifacts.
	a, err := findArtifact(e.db, id, "e2e", 1, "test-attachments")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenHostedFile(context.Background(), a.ID, "checkout/shot.png", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("hosted an attachments artifact: %v", err)
	}
}

// The limits of an attachments directory and of an HTML report are the
// server's, whatever a runner sends.
func TestReportArtifactLimits(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now,
		Artifacts: func() ArtifactOptions { o := ArtifactOptions{}; o.Store = mustFS(t); return o }(),
		Reports:   ReportOptions{HTMLFiles: 2, AttachmentBytes: 10}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	create(t, e, attachmentsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	files := []upFile{{"site/a.html", []byte("a"), 0}, {"site/b.html", []byte("b"), 0}, {"site/c.html", []byte("c"), 0}}
	if _, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "report-site", Files: infos(files)}); !errors.Is(err, ErrQuota) ||
		!strings.Contains(err.Error(), "an HTML report holds at most 2 files") {
		t.Fatalf("three files: %v", err)
	}
	big := []upFile{{"test-results/x.png", bytes.Repeat([]byte("x"), 11), 0}}
	if _, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "test-attachments", Files: infos(big)}); !errors.Is(err, ErrQuota) {
		t.Fatalf("eleven bytes: %v", err)
	}
	if _, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "report-other", Files: infos(files[:1])}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an undeclared report: %v", err)
	}
}

// Tags, variants and suites filter a run's results, and a run is compared
// with the one before it.
func TestRichCasesAndComparison(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	main := Trigger{Event: "push", Ref: "refs/heads/main"}
	first := create(t, e, reportsLinear, main)
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "fail", "pass", "fail", "pass"))
	second := create(t, e, reportsLinear, main)
	finishTests(t, e, take(t, e), "failed", junitFile("r.xml", "pass", "fail", "fail", "pass"),
		cucumberWithEmbedding(pngBytes))
	cmp, err := e.CompareTests(second)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Previous == nil || cmp.Previous.ID != first || cmp.Counts["new_failures"] != 1 || cmp.NewFailures[0].Name != "t1" ||
		cmp.Counts["fixed"] != 1 || cmp.Fixed[0].Name != "t0" || cmp.Counts["still_failing"] != 1 || cmp.StillFailing[0].Name != "t2" {
		t.Fatalf("comparison %+v", cmp)
	}
	p, err := e.RunTestCases(second, CaseQuery{Tag: "@SMOKE"})
	if err != nil || p.Matching != 1 || p.Cases[0].Name != "wrong password" {
		t.Fatalf("by tag: %+v %v", p, err)
	}
	all, _ := e.RunTestCases(second, CaseQuery{})
	if len(all.Tags) != 2 || all.Tags[0].Value != "auth" && all.Tags[0].Value != "smoke" || len(all.Suites) != 2 {
		t.Fatalf("facets %+v %+v", all.Tags, all.Suites)
	}
	if p, _ := e.RunTestCases(second, CaseQuery{Suite: "demo.Suite", Sort: "slowest"}); p.Matching != 4 || p.Cases[0].Name != "t3" {
		t.Fatalf("by suite, slowest: %s", names(p))
	}
	if _, err := e.RunTestCases(second, CaseQuery{Sort: "random"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad sort: %v", err)
	}
	// A tag with LIKE's special characters matches only itself.
	if p, _ := e.RunTestCases(second, CaseQuery{Tag: "%"}); p.Matching != 0 {
		t.Fatalf("tag %%: %d", p.Matching)
	}
}

// A project keeps at most so many files' coverage: the oldest runs' go
// first, and their totals stay.
func TestPruningCoverage(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now,
		Reports: ReportOptions{CoverageFilesPerRun: 2, CoverageFilesPerProject: 3}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	var runs []string
	for range 3 {
		runs = append(runs, create(t, e, coveragePipeline, Trigger{Event: "push", Ref: "refs/heads/main"}))
		two := lcov("src/a.go", 9, 10)
		two.Data = append(two.Data, []byte("SF:src/b.go\nDA:1,1\nend_of_record\n")...)
		c := lcov("src/c.go", 1, 1)
		c.Path = "coverage/c.info"
		finishCoverage(t, e, take(t, e), "succeeded", two, c)
	}
	// Each run kept two of its three files.
	if f, _ := e.RunCoverageFiles(runs[2], CoverageFileQuery{}); f.Matching != 2 {
		t.Fatalf("the newest run kept %d files", f.Matching)
	}
	e.pruneCoverage()
	for i, want := range []int{0, 0, 2} {
		f, err := e.RunCoverageFiles(runs[i], CoverageFileQuery{})
		if err != nil || f.Matching != want {
			t.Errorf("run %d keeps %d files, want %d (%v)", i, f.Matching, want, err)
		}
		rc, _ := e.RunCoverage(runs[i], "")
		if rc.Totals == nil || rc.Totals.Lines.Total != 12 || rc.Steps[0].Pruned != (want == 0) {
			t.Errorf("run %d coverage %+v", i, rc)
		}
	}
}

func mustFS(t *testing.T) blobstore.Store {
	t.Helper()
	s, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
