package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/debrief"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testreport"
)

// Test reports. A step that declares reports.tests has the files it wrote
// collected by its runner once it ends, passed or failed, and sent with the
// result. The server parses them here (they are untrusted: anyone who can
// push code writes them) and keeps:
//
//   - in the event that ends the attempt (step.finished or step.canceled), a
//     short summary: the totals, the files and what was wrong with them. The
//     event log stays the truth, and stays small.
//   - in test_reports, the same summary, one row per step attempt, derived
//     from those events like every other table.
//   - in test_cases, the individual results, written in the same
//     transaction as the event: at most MaxCasesPerRun per run, messages and
//     traces cut to a few KiB, output only for tests that failed. Like logs,
//     they live beside the event log rather than in it. A project keeps at
//     most MaxCasesPerProject of them: the oldest runs' are removed first
//     (see pruneTests); their totals stay.
//
// Nothing about reports changes a step's outcome.

// TestLimits bound what is kept of test reports. Zero fields take the
// defaults.
type TestLimits struct {
	// MaxUploadBytes is how many bytes of report files the server accepts
	// for one step attempt; runners are told, and send no more. Default 24
	// MiB.
	MaxUploadBytes int64
	// MaxCasesPerRun is how many individual results one run keeps; beyond
	// it only the totals are kept. Default 50,000.
	MaxCasesPerRun int
	// MaxCasesPerProject is how many individual results a project keeps,
	// over all its runs; the oldest runs' are removed first. Default
	// 1,000,000.
	MaxCasesPerProject int
	// MaxAgeDays is how long individual results and per-test statistics
	// are kept; older ones are removed even when the project is under its
	// count (schema 44). Default 180.
	MaxAgeDays int
}

// Defaults for TestLimits.
const (
	DefaultTestUploadBytes     = 24 << 20
	DefaultTestCasesPerRun     = 50_000
	DefaultTestCasesPerProject = 1_000_000
	DefaultTestAgeDays         = 180
)

func (l TestLimits) withDefaults() (TestLimits, error) {
	if l.MaxUploadBytes == 0 {
		l.MaxUploadBytes = DefaultTestUploadBytes
	}
	if l.MaxCasesPerRun == 0 {
		l.MaxCasesPerRun = DefaultTestCasesPerRun
	}
	if l.MaxCasesPerProject == 0 {
		l.MaxCasesPerProject = DefaultTestCasesPerProject
	}
	if l.MaxAgeDays == 0 {
		l.MaxAgeDays = DefaultTestAgeDays
	}
	switch {
	case l.MaxUploadBytes < 0 || l.MaxCasesPerRun < 0 || l.MaxCasesPerProject < 0 || l.MaxAgeDays < 0:
		return l, errors.New("test report limits cannot be negative")
	case l.MaxCasesPerProject < l.MaxCasesPerRun:
		return l, fmt.Errorf("tests: max_cases_per_project (%d) must be at least max_cases_per_run (%d)", l.MaxCasesPerProject, l.MaxCasesPerRun)
	}
	return l, nil
}

// TestLimits returns the limits the engine keeps test reports within.
func (e *Engine) TestLimits() TestLimits { return e.live().tests }

// Limits on what is kept from one attempt's files, apart from the cases.
const (
	maxReportFiles  = 200 // files listed in a summary
	maxReportNotes  = 50
	maxReportText   = 300 // bytes in a file's path, error or warning, or a note
	reportParseTime = 30 * time.Second
)

// TestTotals counts test results.
type TestTotals struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errored int `json:"errored"`
	Skipped int `json:"skipped"`
	// Flaky counts tests that passed only after the test tool itself ran
	// them again (see TestCase.Flaky).
	Flaky      int   `json:"flaky"`
	DurationMs int64 `json:"duration_ms"`
	// Truncated says that not every result was kept: a file was cut short,
	// or there were more than a run keeps.
	Truncated bool `json:"truncated,omitempty"`
}

func (t *TestTotals) add(o TestTotals) {
	t.Total += o.Total
	t.Passed += o.Passed
	t.Failed += o.Failed
	t.Errored += o.Errored
	t.Skipped += o.Skipped
	t.Flaky += o.Flaky
	t.DurationMs += o.DurationMs
	t.Truncated = t.Truncated || o.Truncated
}

// TestFile is one report file of a step attempt, as the server read it.
type TestFile struct {
	Path   string `json:"path"`
	Format string `json:"format,omitempty"` // junit or go-test-json
	Bytes  int64  `json:"bytes"`            // received
	Cases  int    `json:"cases"`
	// Error says why nothing could be read from it, such as "not a test
	// report: ...". Warning is a problem that did not stop it being read,
	// such as the file ending part-way through.
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// TestSummary is what one step attempt's test reports said, as kept in its
// event.
type TestSummary struct {
	TestTotals
	Files []TestFile `json:"files"`
	// Notes say what was not collected or kept and why: from the runner
	// (globs that matched nothing, files cut short) and from the server.
	Notes []string `json:"notes,omitempty"`
	// Stored is how many results were kept in test_cases.
	Stored int `json:"stored"`
	// Attachments is how many attachments of the results can be shown, and
	// AttachmentsMissing how many the reports named that cannot (schema
	// 18). Older servers ignore them: they only record.
	Attachments        int `json:"attachments,omitempty"`
	AttachmentsMissing int `json:"attachments_missing,omitempty"`
	// Quarantine says which failing tests the repository's quarantine file
	// excused in this attempt (see quarantine.go); nil when the file was
	// not sent.
	Quarantine *QuarantineRecord `json:"quarantine,omitempty"`
}

// stepAttempt names one attempt of one step.
type stepAttempt struct {
	step    string
	attempt int
}

// storedCase is one result as it is written to test_cases.
type storedCase struct {
	testreport.Case
	key  string
	file string // the report file it came from
	// attach are its attachments as kept (schema 18; see attachTests).
	attach []TestAttachment
}

// parsedTests is what an attempt's report files said, before the run's
// limits are applied (see keepTests).
type parsedTests struct {
	summary TestSummary
	cases   []storedCase
}

// parseSlots bounds how many attempts' reports are parsed at once on one
// server, so that many large uploads arriving together cannot exhaust its
// memory or CPU.
var parseSlots = make(chan struct{}, 4)

// parseTests reads the report files a runner sent. It is called before the
// transaction that records the attempt's end, and never fails: what cannot
// be read is described in the summary.
func (e *Engine) parseTests(in *CollectedReports) *parsedTests {
	parseSlots <- struct{}{}
	defer func() { <-parseSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), reportParseTime)
	defer cancel()

	out := &parsedTests{summary: TestSummary{Files: []TestFile{}}}
	s := &out.summary
	for _, n := range in.Notes {
		s.note(n)
	}
	budget := e.live().tests.MaxUploadBytes
	room := e.live().tests.MaxCasesPerRun
	files := in.Files
	var quarantine []byte
	haveQuarantine := false
	if e.knows(schemaDebrief) {
		files = make([]ReportFile, 0, len(in.Files))
		for _, f := range in.Files {
			if f.Path == debrief.QuarantinePath {
				quarantine, haveQuarantine = f.Data, true
				continue
			}
			files = append(files, f)
		}
	}
	for i, f := range files {
		if i >= maxReportFiles {
			s.note(fmt.Sprintf("%d more files were sent; only the first %d were read", len(files)-i, maxReportFiles))
			s.Truncated = true
			break
		}
		tf := TestFile{Path: cleanText(f.Path, maxReportText), Bytes: int64(len(f.Data))}
		if tf.Path == "" {
			tf.Path = "(no name)"
		}
		if int64(len(f.Data)) > budget {
			tf.Error = fmt.Sprintf("not read: the step's report files came to more than the %s the server accepts", sizeText(e.live().tests.MaxUploadBytes))
			s.Truncated = true
			s.Files = append(s.Files, tf)
			budget = 0
			continue
		}
		budget -= int64(len(f.Data))
		left := time.Until(deadlineOf(ctx))
		if left <= 0 {
			tf.Error = "not read: reading the step's reports took too long"
			s.Truncated = true
			s.Files = append(s.Files, tf)
			continue
		}
		lim := testreport.DefaultLimits()
		lim.MaxCases = max(room, 1)
		lim.Timeout = min(lim.Timeout, left)
		lim.InputCut = f.Cut
		rep, err := testreport.Parse(ctx, f.Data, lim)
		if err != nil {
			tf.Error = cleanText(err.Error(), maxReportText)
			s.Files = append(s.Files, tf)
			continue
		}
		tf.Format, tf.Cases = rep.Format, rep.Counts.Total
		tf.Warning = cleanText(rep.Warning, maxReportText)
		if f.Cut {
			// The report is only its start. The tests in that part are counted and shown; a test that began and
			// shows no result there is missing, never errored.
			size := sizeText(int64(len(f.Data)))
			if f.Size > int64(len(f.Data)) {
				size = fmt.Sprintf("%s of its %s", size, sizeText(f.Size))
			}
			cut := fmt.Sprintf("the report was larger than the limit; %d tests in the part that was kept (the first %s)", rep.Counts.Total, size)
			tf.Warning = joinText(cut, tf.Warning)
			s.note(cut + ": " + tf.Path)
			s.Truncated = true
		}
		c := rep.Counts
		s.add(TestTotals{Total: c.Total, Passed: c.Passed, Failed: c.Failed, Errored: c.Errored, Skipped: c.Skipped,
			Flaky: c.Flaky, DurationMs: rep.DurationMs, Truncated: rep.Truncated})
		cases := rep.Cases
		if len(cases) > room {
			cases = cases[:room]
			s.Truncated = true
		}
		room -= len(cases)
		for _, tc := range cases {
			out.cases = append(out.cases, storedCase{Case: tc, key: tc.Key(), file: tf.Path})
		}
		s.Files = append(s.Files, tf)
	}
	if haveQuarantine {
		out.quarantine(e.now(), quarantine)
	}
	return out
}

func deadlineOf(ctx context.Context) time.Time {
	d, _ := ctx.Deadline()
	return d
}

func (s *TestSummary) note(n string) {
	if n = cleanText(n, maxReportText); n == "" {
		return
	}
	if len(s.Notes) >= maxReportNotes {
		return
	}
	s.Notes = append(s.Notes, n)
}

// keepTests applies the run's limit to an attempt's results and keeps them
// to be stored with the command's events. It returns the summary for the
// event.
func (c *cmd) keepTests(step string, attempt int, t *parsedTests) (*TestSummary, error) {
	sum := t.summary
	sum.Files = append([]TestFile{}, t.summary.Files...)
	sum.Notes = append([]string(nil), t.summary.Notes...)
	var kept int
	if err := c.tx.QueryRow(`SELECT COALESCE(SUM(stored), 0) FROM test_reports WHERE run_id = ?`, c.runID).Scan(&kept); err != nil {
		return nil, err
	}
	room := max(c.e.live().tests.MaxCasesPerRun-kept, 0)
	cases := t.cases
	if len(cases) > room {
		cases = cases[:room]
		sum.Truncated = true
	}
	sum.Stored = len(cases)
	if sum.Stored < sum.Total {
		sum.note(fmt.Sprintf("%d of the %d results were kept with the run, which keeps at most %d; the totals count them all",
			sum.Stored, sum.Total, c.e.live().tests.MaxCasesPerRun))
	}
	if c.tests == nil {
		c.tests = map[stepAttempt][]storedCase{}
	}
	c.tests[stepAttempt{step, attempt}] = cases
	return &sum, nil
}

// dropTests forgets results kept for an attempt that is not recorded as
// having ended after all (its runner failed).
func (c *cmd) dropTests(step string, attempt int) {
	delete(c.tests, stepAttempt{step, attempt})
	c.drop12(step, attempt)
}

// jobReports says what a runner collects after a step, or nil if nothing.
func (c *cmd) jobReports(def *pipeline.Step) *JobReports {
	if !c.e.knows(schemaTests) || def.Reports == nil {
		return nil
	}
	jr := &JobReports{Tests: append([]string(nil), def.Reports.Tests...), MaxBytes: c.e.live().tests.MaxUploadBytes,
		JobReportsDebrief: JobReportsDebrief{Quarantine: c.e.knows(schemaDebrief) && len(def.Reports.Tests) > 0}}
	if c.e.knows(schemaArtifacts) && len(def.Reports.Findings) > 0 {
		jr.Findings, jr.FindingsMaxBytes = append([]string(nil), def.Reports.Findings...), c.e.live().finds.MaxUploadBytes
	}
	if c.e.knows(schemaReports) && len(def.Reports.Coverage) > 0 {
		jr.Coverage, jr.CoverageMaxBytes = append([]string(nil), def.Reports.Coverage...), c.e.live().reps.CoverageUploadBytes
	}
	if len(jr.Tests) == 0 && len(jr.Findings) == 0 && len(jr.Coverage) == 0 {
		return nil
	}
	return jr
}

// persistSchema10 writes what schema 10 added to the derived tables: the
// summary of each attempt that ended with test reports, and its results.
func (c *cmd) persistSchema10() error {
	st := c.st
	for _, ev := range c.events {
		p := ev.Data
		if p.Tests == nil || (ev.Type != StepFinished && ev.Type != StepCanceled) {
			continue
		}
		t := p.Tests
		detail, err := json.Marshal(struct {
			Files      []TestFile        `json:"files"`
			Notes      []string          `json:"notes,omitempty"`
			Quarantine *QuarantineRecord `json:"quarantine,omitempty"`
		}{t.Files, t.Notes, t.Quarantine})
		if err != nil {
			return err
		}
		if _, err := c.tx.Exec(`INSERT INTO test_reports(run_id, step_id, attempt, project, run_number, at, total, passed, failed, errored,
				skipped, flaky, duration_ms, truncated, stored, pruned, detail)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)
			ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET at = excluded.at, total = excluded.total, passed = excluded.passed,
				failed = excluded.failed, errored = excluded.errored, skipped = excluded.skipped, flaky = excluded.flaky,
				duration_ms = excluded.duration_ms, truncated = excluded.truncated, stored = excluded.stored, pruned = 0,
				detail = excluded.detail`,
			st.ID, p.Step, p.Attempt, st.Project, st.Number, ev.At, t.Total, t.Passed, t.Failed, t.Errored,
			t.Skipped, t.Flaky, t.DurationMs, boolInt(t.Truncated), t.Stored, string(detail)); err != nil {
			return err
		}
		if _, err := c.tx.Exec(`DELETE FROM test_cases WHERE run_id = ? AND step_id = ? AND attempt = ?`, st.ID, p.Step, p.Attempt); err != nil {
			return err
		}
		if err := insertCases(c.tx, st, p.Step, p.Attempt, c.tests[stepAttempt{p.Step, p.Attempt}], c.e.knows(schemaReports)); err != nil {
			return err
		}
		if c.e.knows(schemaDebrief) {
			if err := upsertTestStats(c.tx, c.e.db.pg, st, ev.At, c.tests[stepAttempt{p.Step, p.Attempt}]); err != nil {
				return err
			}
		}
	}
	return nil
}

// casesPerInsert is how many results one INSERT writes: few statements
// for many rows, well within both databases' limits on parameters.
const casesPerInsert = 200

// insertCases writes an attempt's results; rich adds what schema 18 keeps
// (tags, variant and extra), failing and flaky results first within the
// attempt's budget for extra.
func insertCases(tx *txx, st *RunState, step string, attempt int, cases []storedCase, rich bool) error {
	cols := 16
	if rich {
		cols = 19
	}
	extras := make([]string, len(cases))
	if rich {
		budget := maxExtraAttemptBytes
		for pass := 0; pass < 2; pass++ {
			for i := range cases {
				tc := &cases[i]
				failing := tc.Status == testreport.Failed || tc.Status == testreport.Errored || tc.Flaky
				if failing == (pass == 0) {
					extras[i] = extraOf(tc, &budget)
				}
			}
		}
	}
	marks := "(" + strings.TrimSuffix(strings.Repeat("?,", cols), ",") + ")"
	for start := 0; start < len(cases); start += casesPerInsert {
		n := min(len(cases)-start, casesPerInsert)
		batch := cases[start : start+n]
		var q strings.Builder
		q.WriteString(`INSERT INTO test_cases(run_id, step_id, attempt, project, run_number, test_key, suite, name, file, status,
			duration_ms, message, details, output, flaky, report_file`)
		if rich {
			q.WriteString(`, tags, variant, extra`)
		}
		q.WriteString(`) VALUES `)
		args := make([]any, 0, n*cols)
		for i, tc := range batch {
			if i > 0 {
				q.WriteString(",")
			}
			q.WriteString(marks)
			args = append(args, st.ID, step, attempt, st.Project, st.Number, tc.key, tc.Suite, tc.Name, tc.File, string(tc.Status),
				tc.DurationMs, tc.Message, tc.Details, tc.Output, boolInt(tc.Flaky), tc.file)
			if rich {
				args = append(args, tagsColumn(tc.Tags), tc.Variant, extras[start+i])
			}
		}
		if _, err := tx.Exec(q.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// pruneBatch is how many results one statement removes when pruning, so
// that no transaction grows large.
const pruneBatch = 5000

// pruneTests removes the individual results of each project's oldest runs
// while the project keeps more than MaxCasesPerProject; the runs' totals
// stay. Every server does this; they may do it at the same moment, which is
// harmless: each step only removes what is beyond the limit, and a result
// is removed once.
//
// The rows of test_reports are marked first, then the results removed. A
// run whose results are stored between the two is then counted although
// its results are gone, which only makes the next pass prune a little
// sooner; a result is never left behind uncounted.
func (e *Engine) pruneTests() {
	if !e.knows(schemaTests) {
		return
	}
	e.pruneTestsByAge()
	limit := e.live().tests.MaxCasesPerProject
	rows, err := e.db.Query(`SELECT project FROM test_reports GROUP BY project HAVING SUM(stored) > ?`, limit)
	if err != nil {
		e.log.Error("looking for projects with too many test results", "err", err)
		return
	}
	var projects []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			e.log.Error("looking for projects with too many test results", "err", err)
			return
		}
		projects = append(projects, p)
	}
	rows.Close()
	for _, p := range projects {
		if err := e.pruneProject(p, limit); err != nil {
			e.log.Warn("removing a project's oldest test results; trying again later", "project", p, "err", err)
		}
	}
}

func (e *Engine) pruneProject(project string, limit int) error {
	// The newest runs whose results fit; everything older goes.
	rows, err := e.db.Query(`SELECT run_number, SUM(stored) FROM test_reports WHERE project = ? AND stored > 0
		GROUP BY run_number ORDER BY run_number DESC`, project)
	if err != nil {
		return err
	}
	cutoff, kept := -1, 0
	for rows.Next() {
		var number, n int
		if err := rows.Scan(&number, &n); err != nil {
			rows.Close()
			return err
		}
		if kept+n > limit {
			cutoff = number
			break
		}
		kept += n
	}
	rows.Close()
	if err := rows.Err(); err != nil || cutoff < 0 {
		return err
	}
	unlock := e.writeLock()
	_, err = e.db.Exec(`UPDATE test_reports SET stored = 0, pruned = 1 WHERE project = ? AND run_number <= ? AND (stored > 0 OR pruned = 0)`,
		project, cutoff)
	unlock()
	if err != nil {
		return err
	}
	removed := int64(0)
	for {
		unlock := e.writeLock()
		res, err := e.db.Exec(`DELETE FROM test_cases WHERE id IN (SELECT id FROM test_cases WHERE project = ? AND run_number <= ?
			ORDER BY id LIMIT ?)`, project, cutoff, pruneBatch)
		unlock()
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		removed += n
		if n < pruneBatch {
			break
		}
	}
	if removed > 0 {
		e.log.Info("removed the oldest test results of a project, keeping their totals", "project", project,
			"up_to_run", cutoff, "results", removed, "limit", limit)
	}
	return nil
}

// cleanText makes untrusted text safe to keep and show: valid UTF-8, no
// control characters, at most n bytes.
func cleanText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			if r == '\n' || r == '\t' {
				return ' '
			}
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func joinText(a, b string) string {
	if b == "" {
		return a
	}
	return a + "; " + b
}

// sizeText writes a number of bytes for people: 512 bytes, 3.5 KiB, 24 MiB.
func sizeText(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1024)) + " KiB"
	default:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/(1<<20))) + " MiB"
	}
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }
