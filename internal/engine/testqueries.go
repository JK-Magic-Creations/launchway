package engine

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/testreport"
)

// Reading test results back. A run's results are, for each step that has
// any, those of its latest attempt that reported, or, for a step a re-run
// reused, those of the attempt it reused in the run where it ran: they are
// linked, not copied.

// reportRow is a row of test_reports.
type reportRow struct {
	run, step       string
	attempt, number int
	totals          TestTotals
	stored          int
	pruned          bool
	at              int64
	detail          string
}

// triple names one attempt of one step of one run.
type triple struct {
	run, step string
	attempt   int
}

func (r *reportRow) triple() triple { return triple{r.run, r.step, r.attempt} }

// effective is the reports that count for some runs.
type effective struct {
	// steps: run → step → the report that counts.
	steps map[string]map[string]*reportRow
	// own: run → step → every attempt's report in that run, oldest first.
	own map[string]map[string][]*reportRow
	// reused: run → step → where the step ran, for steps the run reused.
	reused map[string]map[string]reusedRef
}

type reusedRef struct {
	run     string
	number  int
	attempt int
}

// inChunks runs fn on the ids, at most 500 at a time, with the "?, ?, ..."
// to put in an IN list.
func inChunks(ids []string, fn func(marks string, args []any) error) error {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		args := make([]any, n)
		for i, id := range ids[:n] {
			args[i] = id
		}
		if err := fn(strings.TrimSuffix(strings.Repeat("?,", n), ","), args); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

// effectiveReports finds the reports that count for each of the runs.
func (e *Engine) effectiveReports(runIDs []string) (*effective, error) {
	ef := &effective{steps: map[string]map[string]*reportRow{}, own: map[string]map[string][]*reportRow{},
		reused: map[string]map[string]reusedRef{}}
	if !e.knows(schemaTests) || len(runIDs) == 0 {
		return ef, nil
	}
	runIDs = slices.Compact(slices.Sorted(slices.Values(runIDs)))
	sources := map[string]bool{}
	err := inChunks(runIDs, func(marks string, args []any) error {
		rows, err := e.db.Query(`SELECT run_id, step_id, reused_from, reused_from_number, reused_attempt FROM steps
			WHERE run_id IN (`+marks+`) AND reused_from <> ''`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var run, step string
			var r reusedRef
			if err := rows.Scan(&run, &step, &r.run, &r.number, &r.attempt); err != nil {
				return err
			}
			if ef.reused[run] == nil {
				ef.reused[run] = map[string]reusedRef{}
			}
			ef.reused[run][step] = r
			sources[r.run] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	all := append([]string{}, runIDs...)
	for id := range sources {
		all = append(all, id)
	}
	all = slices.Compact(slices.Sorted(slices.Values(all)))
	byTriple := map[triple]*reportRow{}
	err = inChunks(all, func(marks string, args []any) error {
		rows, err := e.db.Query(`SELECT run_id, step_id, attempt, run_number, total, passed, failed, errored, skipped, flaky,
				duration_ms, truncated, stored, pruned, at, detail
			FROM test_reports WHERE run_id IN (`+marks+`) ORDER BY run_id, step_id, attempt`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := &reportRow{}
			var truncated, pruned int
			t := &r.totals
			if err := rows.Scan(&r.run, &r.step, &r.attempt, &r.number, &t.Total, &t.Passed, &t.Failed, &t.Errored, &t.Skipped, &t.Flaky,
				&t.DurationMs, &truncated, &r.stored, &pruned, &r.at, &r.detail); err != nil {
				return err
			}
			t.Truncated, r.pruned = truncated != 0, pruned != 0
			byTriple[r.triple()] = r
			if ef.own[r.run] == nil {
				ef.own[r.run] = map[string][]*reportRow{}
			}
			ef.own[r.run][r.step] = append(ef.own[r.run][r.step], r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, run := range runIDs {
		m := map[string]*reportRow{}
		for step, list := range ef.own[run] {
			if _, reused := ef.reused[run][step]; !reused {
				m[step] = list[len(list)-1]
			}
		}
		for step, ref := range ef.reused[run] {
			if r := byTriple[triple{ref.run, step, ref.attempt}]; r != nil {
				m[step] = r
			}
		}
		if len(m) > 0 {
			ef.steps[run] = m
		}
	}
	return ef, nil
}

// totals adds up a run's reports that count; nil if it has none.
func (ef *effective) totals(run string) *TestTotals {
	steps := ef.steps[run]
	if len(steps) == 0 {
		return nil
	}
	t := &TestTotals{}
	for _, r := range steps {
		t.add(r.totals)
	}
	return t
}

// triples are the step attempts whose results count for a run.
func (ef *effective) triples(run string) []triple {
	var out []triple
	for _, r := range ef.steps[run] {
		out = append(out, r.triple())
	}
	slices.SortFunc(out, func(a, b triple) int { return cmp.Or(strings.Compare(a.step, b.step), strings.Compare(a.run, b.run)) })
	return out
}

// attachTests adds test totals to runs as listed.
func (e *Engine) attachTests(runs []*RunSummary) error {
	if !e.knows(schemaTests) || len(runs) == 0 {
		return nil
	}
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	ef, err := e.effectiveReports(ids)
	if err != nil {
		return err
	}
	for _, r := range runs {
		r.Tests = ef.totals(r.ID)
	}
	return nil
}

// tripleFilter is a WHERE condition on test_cases (as tc) that matches the
// results of the given step attempts.
func tripleFilter(ts []triple) (string, []any) {
	if len(ts) == 0 {
		return "1 = 0", nil
	}
	runs := map[string]bool{}
	var vals []string
	var args []any
	for _, t := range ts {
		runs[t.run] = true
	}
	var runArgs []any
	for _, r := range slices.Sorted(func(yield func(string) bool) {
		for r := range runs {
			if !yield(r) {
				return
			}
		}
	}) {
		runArgs = append(runArgs, r)
	}
	for _, t := range ts {
		vals = append(vals, "(CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS INTEGER))")
		args = append(args, t.run, t.step, t.attempt)
	}
	cond := `tc.run_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(runArgs)), ",") + `)
		AND (tc.run_id, tc.step_id, tc.attempt) IN (VALUES ` + strings.Join(vals, ",") + `)`
	return cond, append(runArgs, args...)
}

// ---- one run's results ----

// RunTests is a run's test results, step by step.
type RunTests struct {
	// Totals adds up the steps' results; nil when no step has reported.
	Totals *TestTotals `json:"totals"`
	Steps  []StepTests `json:"steps"`
	// Declared lists the steps whose definition asks for test reports, in
	// pipeline order, whether or not they have run.
	Declared []string `json:"declared"`
	// Flaky counts tests that failed and then passed on a retry of the same
	// step in this run, or that the test tool ran again until they passed.
	Flaky int `json:"flaky"`
	// NewFailures counts tests that fail here and passed in Previous: the
	// run before this one, of the same ref, that has test results.
	NewFailures int      `json:"new_failures"`
	Previous    *RunLink `json:"previous,omitempty"`
}

// RunLink names a run.
type RunLink struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
}

// StepTests is one step's results: those of the attempt that counts.
type StepTests struct {
	Step    string `json:"step"`
	Attempt int    `json:"attempt"`
	// For a step a re-run reused: the run where it ran, whose results these
	// are.
	ReusedFrom       string `json:"reused_from,omitempty"`
	ReusedFromNumber int    `json:"reused_from_number,omitempty"`
	TestTotals
	Stored int `json:"stored"`
	// Pruned says the individual results were removed to keep the project
	// within its limit; the totals remain.
	Pruned bool       `json:"pruned,omitempty"`
	Files  []TestFile `json:"files"`
	Notes  []string   `json:"notes,omitempty"`
	// Attempts are the totals of every attempt of the step that reported,
	// oldest first: more than one when it was retried.
	Attempts []AttemptTotals `json:"attempts"`
}

// AttemptTotals are one attempt's totals.
type AttemptTotals struct {
	Attempt int `json:"attempt"`
	TestTotals
}

// runState reads a run's state from its events, for the pipeline.
func (e *Engine) runState(runID string) (*RunState, error) {
	evs, _, err := loadEvents(e.db, runID)
	if err != nil {
		return nil, err
	}
	st := e.fold(evs)
	if st == nil || st.Pipeline == nil {
		return nil, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	return st, nil
}

// RunTests returns a run's test results, step by step.
func (e *Engine) RunTests(runID string) (*RunTests, error) {
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	out := &RunTests{Steps: []StepTests{}, Declared: []string{}}
	for _, s := range st.Pipeline.Steps {
		if s.Reports != nil {
			out.Declared = append(out.Declared, s.ID)
		}
	}
	if !e.knows(schemaTests) {
		return out, nil
	}
	ef, err := e.effectiveReports([]string{runID})
	if err != nil {
		return nil, err
	}
	out.Totals = ef.totals(runID)
	for _, id := range st.Order {
		r := ef.steps[runID][id]
		if r == nil {
			continue
		}
		s := StepTests{Step: id, Attempt: r.attempt, TestTotals: r.totals, Stored: r.stored, Pruned: r.pruned, Files: []TestFile{}}
		if ref, ok := ef.reused[runID][id]; ok {
			s.ReusedFrom, s.ReusedFromNumber = ref.run, ref.number
		}
		var d struct {
			Files []TestFile `json:"files"`
			Notes []string   `json:"notes"`
		}
		_ = json.Unmarshal([]byte(r.detail), &d)
		if d.Files != nil {
			s.Files = d.Files
		}
		s.Notes = d.Notes
		list := ef.own[runID][id]
		if s.ReusedFrom != "" {
			list = []*reportRow{r}
		}
		for _, a := range list {
			s.Attempts = append(s.Attempts, AttemptTotals{Attempt: a.attempt, TestTotals: a.totals})
		}
		out.Steps = append(out.Steps, s)
	}
	if out.Totals == nil {
		return out, nil
	}
	flaky, err := e.flakyInRun(runID, ef)
	if err != nil {
		return nil, err
	}
	for _, keys := range flaky {
		out.Flaky += len(keys)
	}
	prev, err := e.previousWithTests(st)
	if err != nil {
		return nil, err
	}
	if prev != nil {
		out.Previous = &RunLink{ID: prev.run, Number: prev.number}
		failing, err := e.keysWithStatus(ef.triples(runID), []string{"failed", "errored"}, nil, 10_000)
		if err != nil {
			return nil, err
		}
		passed, err := e.keysWithStatus(prev.triples, []string{"passed"}, failing, 0)
		if err != nil {
			return nil, err
		}
		out.NewFailures = len(passed)
	}
	return out, nil
}

// flakyInRun finds, for each step of a run, the tests that failed on one
// attempt and passed on a later one, the attempt that counts, and those the
// test tool itself ran again until they passed: step → keys.
func (e *Engine) flakyInRun(runID string, ef *effective) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	steps := ef.steps[runID]
	if len(steps) == 0 {
		return out, nil
	}
	add := func(step, key string) {
		if out[step] == nil {
			out[step] = map[string]bool{}
		}
		out[step][key] = true
	}
	// Retried steps: every attempt of the run where the step ran.
	var runs []string
	for step, r := range steps {
		if r.attempt > 1 || len(ef.own[runID][step]) > 1 {
			runs = append(runs, r.run)
		}
	}
	if len(runs) > 0 {
		runs = slices.Compact(slices.Sorted(slices.Values(runs)))
		err := inChunks(runs, func(marks string, args []any) error {
			rows, err := e.db.Query(`SELECT run_id, step_id, test_key,
					MIN(CASE WHEN status IN ('failed', 'errored') THEN attempt END),
					MAX(CASE WHEN status = 'passed' THEN attempt END)
				FROM test_cases WHERE run_id IN (`+marks+`)
				GROUP BY run_id, step_id, test_key
				HAVING SUM(CASE WHEN status IN ('failed', 'errored') THEN 1 ELSE 0 END) > 0
					AND SUM(CASE WHEN status = 'passed' THEN 1 ELSE 0 END) > 0`, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var run, step, key string
				var failedAt, passedAt sql.NullInt64
				if err := rows.Scan(&run, &step, &key, &failedAt, &passedAt); err != nil {
					return err
				}
				r := steps[step]
				if r != nil && r.run == run && int(passedAt.Int64) == r.attempt && failedAt.Int64 < passedAt.Int64 {
					add(step, key)
				}
			}
			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
	}
	// Tests the tool ran again, in the attempts that count.
	cond, args := tripleFilter(ef.triples(runID))
	rows, err := e.db.Query(`SELECT tc.step_id, tc.test_key FROM test_cases tc WHERE `+cond+` AND tc.flaky = 1 LIMIT 10000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var step, key string
		if err := rows.Scan(&step, &key); err != nil {
			return nil, err
		}
		add(step, key)
	}
	return out, rows.Err()
}

// previousRun is the run before another of the same ref that has test
// results, and the step attempts that count in it.
type previousRun struct {
	run     string
	number  int
	triples []triple
}

func (e *Engine) previousWithTests(st *RunState) (*previousRun, error) {
	if st.Trigger.Ref == "" {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT id FROM runs WHERE project = ? AND ref = ? AND number < ? ORDER BY number DESC LIMIT 25`,
		st.Project, st.Trigger.Ref, st.Number)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return nil, err
	}
	ef, err := e.effectiveReports(ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if t := ef.totals(id); t != nil && t.Total > 0 {
			var number int
			if err := e.db.QueryRow(`SELECT number FROM runs WHERE id = ?`, id).Scan(&number); err != nil {
				return nil, err
			}
			return &previousRun{run: id, number: number, triples: ef.triples(id)}, nil
		}
	}
	return nil, nil
}

// keysWithStatus returns the tests among the step attempts with one of the
// statuses, as a set of keys; only those in among, if it is not nil; at
// most limit (0: no limit).
func (e *Engine) keysWithStatus(ts []triple, statuses []string, among map[string]bool, limit int) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ts) == 0 || (among != nil && len(among) == 0) {
		return out, nil
	}
	cond, args := tripleFilter(ts)
	cond += ` AND tc.status IN (` + strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",") + `)`
	for _, s := range statuses {
		args = append(args, s)
	}
	collect := func(extra string, extraArgs []any) error {
		q := `SELECT DISTINCT tc.test_key FROM test_cases tc WHERE ` + cond + extra
		if limit > 0 {
			q += fmt.Sprintf(" LIMIT %d", limit)
		}
		rows, err := e.db.Query(q, append(append([]any{}, args...), extraArgs...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			out[k] = true
		}
		return rows.Err()
	}
	if among == nil {
		return out, collect("", nil)
	}
	keys := make([]string, 0, len(among))
	for k := range among {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return out, inChunks(keys, func(marks string, kargs []any) error {
		return collect(` AND tc.test_key IN (`+marks+`)`, kargs)
	})
}

// ---- the results of a run, one by one ----

// TestCase is one test result.
type TestCase struct {
	ID         int64  `json:"id"`
	Key        string `json:"key"`
	Run        string `json:"run_id"` // where it ran: another run for a step this one reused
	Step       string `json:"step"`
	Attempt    int    `json:"attempt"`
	Suite      string `json:"suite"`
	Name       string `json:"name"`
	File       string `json:"file,omitempty"`
	Status     string `json:"status"` // passed, failed, errored or skipped
	DurationMs int64  `json:"duration_ms"`
	// Message is the start of the failure, error or skip message.
	Message string `json:"message,omitempty"`
	// HasDetails says there is more to see: a trace or captured output
	// (see Engine.TestCase).
	HasDetails bool   `json:"has_details"`
	ReportFile string `json:"report_file,omitempty"`
	// Flaky: it failed and then passed on a retry of the step in this run,
	// or the test tool ran it again until it passed.
	Flaky bool `json:"flaky,omitempty"`
	// NewFailure: it fails here and passed in the previous run of the same
	// ref that has test results.
	NewFailure bool `json:"new_failure,omitempty"`
	// Details and Output, only when one result is asked for.
	Details string `json:"details,omitempty"`
	Output  string `json:"output,omitempty"`

	// Schema 18: its tags and variant (a browser or project), and, with
	// Attachments counted in lists, in full only when one result is asked
	// for: retries, earlier attempts, steps, attachments and properties.
	Tags        []string              `json:"tags,omitempty"`
	Variant     string                `json:"variant,omitempty"`
	HasSteps    bool                  `json:"has_steps,omitempty"`
	Retries     int                   `json:"retries,omitempty"`
	Attempts    []testreport.Attempt  `json:"attempts,omitempty"`
	Steps       []testreport.Step     `json:"steps,omitempty"`
	StepsCut    bool                  `json:"steps_cut,omitempty"`
	Attachments []TestAttachment      `json:"attachments,omitempty"`
	Properties  []testreport.Property `json:"properties,omitempty"`
}

// CaseQuery narrows a run's results.
type CaseQuery struct {
	Status string // failed (failed and errored), passed, skipped or flaky; "" for all
	Search string // in the suite, name, file or message, ignoring case
	Step   string // only this step
	// Attempt, with Step, shows that attempt instead of the one that counts.
	Attempt int
	Limit   int // default 200, at most 1000
	Offset  int
	// Schema 18: only results with this tag, of this suite, or of this
	// variant (a browser or project); Sort "slowest" lists the slowest
	// first, "name" by suite and name; the default is failures first, then
	// the slowest.
	Tag     string
	Suite   string
	Variant string
	Sort    string
}

// CasePage is a page of results.
type CasePage struct {
	Cases []TestCase `json:"cases"`
	// Matching counts every result the query matches, Counts those the
	// query would match with each status, for filters.
	Matching int            `json:"matching"`
	Counts   map[string]int `json:"counts"`
	// Tags, Variants and Suites are the choices the results offer for
	// filtering, with how many results each has (schema 18; at most 100
	// each, the commonest first).
	Tags     []Facet `json:"tags,omitempty"`
	Variants []Facet `json:"variants,omitempty"`
	Suites   []Facet `json:"suites,omitempty"`
	Offset   int     `json:"offset"`
	Limit    int     `json:"limit"`
}

// Statuses a query may ask for.
var caseStatuses = map[string][]string{
	"failed": {"failed", "errored"}, "passed": {"passed"}, "skipped": {"skipped"},
}

const messagePreview = 600 // bytes of a message in a list

// RunTestCases lists a run's results: failures first, then the slowest.
func (e *Engine) RunTestCases(runID string, q CaseQuery) (*CasePage, error) {
	if _, ok := caseStatuses[q.Status]; !ok && q.Status != "" && q.Status != "flaky" {
		return nil, fmt.Errorf("%w: status is failed, passed, skipped or flaky", ErrInvalid)
	}
	if q.Limit <= 0 {
		q.Limit = 200
	}
	q.Limit = min(q.Limit, 1000)
	q.Offset = min(max(q.Offset, 0), 1_000_000)
	page := &CasePage{Cases: []TestCase{}, Counts: map[string]int{}, Offset: q.Offset, Limit: q.Limit}
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	if !e.knows(schemaTests) {
		return page, nil
	}
	ef, err := e.effectiveReports([]string{runID})
	if err != nil {
		return nil, err
	}
	ts := ef.triples(runID)
	if q.Step != "" {
		ts = slices.DeleteFunc(ts, func(t triple) bool { return t.step != q.Step })
		if q.Attempt > 0 {
			ts = nil
			if _, reused := ef.reused[runID][q.Step]; !reused {
				for _, r := range ef.own[runID][q.Step] {
					if r.attempt == q.Attempt {
						ts = []triple{r.triple()}
					}
				}
			}
			if ts == nil {
				return nil, fmt.Errorf("%w: step %s has no test results from attempt %d in run #%d", ErrNotFound, q.Step, q.Attempt, st.Number)
			}
		}
	}
	if len(ts) == 0 {
		return page, nil
	}
	flaky, err := e.flakyInRun(runID, ef)
	if err != nil {
		return nil, err
	}
	var flakyKeys []string
	for _, keys := range flaky {
		for k := range keys {
			flakyKeys = append(flakyKeys, k)
		}
	}
	flakyKeys = slices.Compact(slices.Sorted(slices.Values(flakyKeys)))
	if len(flakyKeys) > 1000 {
		flakyKeys = flakyKeys[:1000]
	}

	base, args := tripleFilter(ts)
	if s := strings.TrimSpace(q.Search); s != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(s)) + "%"
		base += ` AND (LOWER(tc.name) LIKE ? ESCAPE '\' OR LOWER(tc.suite) LIKE ? ESCAPE '\' OR LOWER(tc.file) LIKE ? ESCAPE '\'
			OR LOWER(tc.message) LIKE ? ESCAPE '\')`
		args = append(args, like, like, like, like)
	}
	rich := e.knows(schemaReports)
	if rich {
		// The facets, before narrowing by them, so the filters can offer
		// every choice.
		if err := e.caseFacets(page, base, args); err != nil {
			return nil, err
		}
		if t := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(q.Tag, "@"))); t != "" {
			base += ` AND tc.tags LIKE ? ESCAPE '\'`
			args = append(args, "%|"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(t)+"|%")
		}
		if q.Variant != "" {
			base += ` AND tc.variant = ?`
			args = append(args, q.Variant)
		}
	}
	if q.Suite != "" {
		base += ` AND tc.suite = ?`
		args = append(args, q.Suite)
	}
	// Counts by status, for the filters.
	rows, err := e.db.Query(`SELECT tc.status, COUNT(*) FROM test_cases tc WHERE `+base+` GROUP BY tc.status`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, err
		}
		page.Counts["all"] += n
		if s == "errored" {
			s = "failed"
		}
		page.Counts[s] += n
	}
	rows.Close()
	if len(flakyKeys) > 0 {
		var n int
		marks := strings.TrimSuffix(strings.Repeat("?,", len(flakyKeys)), ",")
		fargs := append(append([]any{}, args...), anySlice(flakyKeys)...)
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM test_cases tc WHERE `+base+` AND tc.test_key IN (`+marks+`)`, fargs...).Scan(&n); err != nil {
			return nil, err
		}
		page.Counts["flaky"] = n
	}

	where, wargs := base, append([]any{}, args...)
	switch {
	case q.Status == "flaky":
		if len(flakyKeys) == 0 {
			return page, nil
		}
		where += ` AND tc.test_key IN (` + strings.TrimSuffix(strings.Repeat("?,", len(flakyKeys)), ",") + `)`
		wargs = append(wargs, anySlice(flakyKeys)...)
	case q.Status != "":
		sts := caseStatuses[q.Status]
		where += ` AND tc.status IN (` + strings.TrimSuffix(strings.Repeat("?,", len(sts)), ",") + `)`
		wargs = append(wargs, anySlice(sts)...)
	}
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM test_cases tc WHERE `+where, wargs...).Scan(&page.Matching); err != nil {
		return nil, err
	}
	order := `CASE WHEN tc.status IN ('failed', 'errored') THEN 0 ELSE 1 END, tc.duration_ms DESC, tc.suite, tc.name, tc.id`
	switch q.Sort {
	case "", "failures":
	case "slowest":
		order = `tc.duration_ms DESC, tc.suite, tc.name, tc.id`
	case "name":
		order = `tc.suite, tc.name, tc.id`
	default:
		return nil, fmt.Errorf("%w: sort is failures, slowest or name", ErrInvalid)
	}
	richCols := `'', '', 0`
	if rich {
		richCols = `tc.tags, tc.variant, LENGTH(tc.extra)`
	}
	rows, err = e.db.Query(`SELECT tc.id, tc.test_key, tc.run_id, tc.step_id, tc.attempt, tc.suite, tc.name, tc.file, tc.status,
			tc.duration_ms, SUBSTR(tc.message, 1, ?), (LENGTH(tc.details) > 0 OR LENGTH(tc.output) > 0 OR LENGTH(tc.message) > ?),
			tc.report_file, tc.flaky, `+richCols+`
		FROM test_cases tc WHERE `+where+`
		ORDER BY `+order+`
		LIMIT ? OFFSET ?`, append(append([]any{messagePreview, messagePreview}, wargs...), q.Limit, q.Offset)...)
	if err != nil {
		return nil, err
	}
	failing := map[string]bool{}
	for rows.Next() {
		var c TestCase
		var more bool
		var toolFlaky int
		var tags string
		var extraLen int
		if err := rows.Scan(&c.ID, &c.Key, &c.Run, &c.Step, &c.Attempt, &c.Suite, &c.Name, &c.File, &c.Status,
			&c.DurationMs, &c.Message, &more, &c.ReportFile, &toolFlaky, &tags, &c.Variant, &extraLen); err != nil {
			rows.Close()
			return nil, err
		}
		c.Tags = tagsFromColumn(tags)
		c.HasSteps = extraLen > 0
		c.HasDetails = more || extraLen > 0
		c.Flaky = toolFlaky != 0 || flaky[c.Step][c.Key]
		if c.Status == "failed" || c.Status == "errored" {
			failing[c.Key] = true
		}
		page.Cases = append(page.Cases, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(failing) > 0 {
		prev, err := e.previousWithTests(st)
		if err != nil {
			return nil, err
		}
		if prev != nil {
			passed, err := e.keysWithStatus(prev.triples, []string{"passed"}, failing, 0)
			if err != nil {
				return nil, err
			}
			for i := range page.Cases {
				c := &page.Cases[i]
				c.NewFailure = (c.Status == "failed" || c.Status == "errored") && passed[c.Key]
			}
		}
	}
	return page, nil
}

func anySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// TestCase returns one result of a run in full: its trace and captured
// output. It must be one of the run's own results, or one of a step the run
// reused; any other is not found.
func (e *Engine) TestCase(runID string, id int64) (*TestCase, error) {
	if !e.knows(schemaTests) {
		return nil, fmt.Errorf("%w: test result %d", ErrNotFound, id)
	}
	var c TestCase
	var toolFlaky int
	err := e.db.QueryRow(`SELECT id, test_key, run_id, step_id, attempt, suite, name, file, status, duration_ms, message, details, output,
			report_file, flaky FROM test_cases WHERE id = ?`, id).
		Scan(&c.ID, &c.Key, &c.Run, &c.Step, &c.Attempt, &c.Suite, &c.Name, &c.File, &c.Status, &c.DurationMs, &c.Message, &c.Details,
			&c.Output, &c.ReportFile, &toolFlaky)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: test result %d", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	c.Flaky = toolFlaky != 0
	c.HasDetails = c.Details != "" || c.Output != ""
	if err := e.richCase(&c); err != nil {
		return nil, err
	}
	if c.Run == runID {
		return &c, nil
	}
	var reusedFrom string
	var reusedAttempt int
	err = e.db.QueryRow(`SELECT reused_from, reused_attempt FROM steps WHERE run_id = ? AND step_id = ?`, runID, c.Step).Scan(&reusedFrom, &reusedAttempt)
	if err == nil && reusedFrom == c.Run && reusedAttempt == c.Attempt {
		return &c, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return nil, fmt.Errorf("%w: test result %d", ErrNotFound, id)
}

// ---- one test over time ----

// TestHistory is one test's results in a project's recent runs.
type TestHistory struct {
	Key     string          `json:"key"`
	Suite   string          `json:"suite"`
	Name    string          `json:"name"`
	File    string          `json:"file,omitempty"`
	Ref     string          `json:"ref"` // the ref looked at; "" for every ref
	Results []HistoryResult `json:"results"`
}

// HistoryResult is one run's result for a test: that of the latest attempt
// that ran it.
type HistoryResult struct {
	Run        string `json:"run_id"`
	Number     int    `json:"number"`
	SHA        string `json:"sha"`
	Ref        string `json:"ref"`
	CreatedAt  int64  `json:"created_at"`
	Step       string `json:"step"`
	Attempt    int    `json:"attempt"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
	// Flaky: an earlier attempt of the step in that run failed it.
	Flaky bool `json:"flaky,omitempty"`
}

// TestHistory returns a test's results in the last runs of a project (on a
// ref, unless ref is empty), newest first.
func (e *Engine) TestHistory(project, key, ref string, limit int) (*TestHistory, error) {
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 200)
	h := &TestHistory{Key: key, Ref: ref, Results: []HistoryResult{}}
	if !e.knows(schemaTests) {
		return nil, fmt.Errorf("%w: no results for test %s", ErrNotFound, key)
	}
	q := `SELECT tc.run_id, tc.run_number, tc.step_id, tc.attempt, tc.status, tc.duration_ms, SUBSTR(tc.message, 1, ?),
			tc.suite, tc.name, tc.file, r.sha, r.ref, r.created_at
		FROM test_cases tc JOIN runs r ON r.id = tc.run_id
		WHERE tc.project = ? AND tc.test_key = ?`
	args := []any{messagePreview, project, key}
	if ref != "" {
		q += ` AND r.ref = ?`
		args = append(args, ref)
	}
	// Several attempts, or steps, of a run may have run the test: read
	// enough rows for the runs asked for.
	q += ` ORDER BY tc.run_number DESC, tc.attempt DESC, tc.step_id LIMIT ?`
	args = append(args, limit*4)
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]int{} // run → index in Results
	failedBefore := map[string]bool{}
	for rows.Next() {
		var r HistoryResult
		if err := rows.Scan(&r.Run, &r.Number, &r.Step, &r.Attempt, &r.Status, &r.DurationMs, &r.Message,
			&h.Suite, &h.Name, &h.File, &r.SHA, &r.Ref, &r.CreatedAt); err != nil {
			return nil, err
		}
		if i, ok := seen[r.Run]; ok {
			// An earlier attempt (rows come latest attempt first).
			if (r.Status == "failed" || r.Status == "errored") && h.Results[i].Status == "passed" && r.Step == h.Results[i].Step {
				failedBefore[r.Run] = true
			}
			continue
		}
		if len(h.Results) >= limit {
			continue
		}
		seen[r.Run] = len(h.Results)
		h.Results = append(h.Results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(h.Results) == 0 {
		return nil, fmt.Errorf("%w: no results for this test in %s", ErrNotFound, project)
	}
	for i := range h.Results {
		h.Results[i].Flaky = failedBefore[h.Results[i].Run]
	}
	return h, nil
}

// ---- a project's tests over time ----

// TestOverview is a project's tests over its last runs of a ref.
type TestOverview struct {
	Ref string `json:"ref"`
	// Runs are the ref's last runs, oldest first, with their totals (nil
	// for a run without test results).
	Runs []OverviewRun `json:"runs"`
	// Slowest, Failing and Flaky are the tests that stand out in them.
	Slowest []TestStat `json:"slowest"`
	Failing []TestStat `json:"failing"`
	Flaky   []TestStat `json:"flaky"`
	// Refs are the project's refs that have test results, latest first.
	Refs []string `json:"refs"`
}

// OverviewRun is one run in an overview.
type OverviewRun struct {
	ID        string      `json:"id"`
	Number    int         `json:"number"`
	SHA       string      `json:"sha"`
	Status    RunStatus   `json:"status"`
	CreatedAt int64       `json:"created_at"`
	Tests     *TestTotals `json:"tests"`
}

// TestStat is one test and what makes it stand out.
type TestStat struct {
	Key     string `json:"key"`
	Suite   string `json:"suite"`
	Name    string `json:"name"`
	File    string `json:"file,omitempty"`
	Variant string `json:"variant,omitempty"` // the browser or project it ran as (schema 18)
	// Runs counts the runs it ran in, of those looked at.
	Runs int `json:"runs"`
	// Slowest: the average and longest duration.
	AvgMs int64 `json:"avg_ms,omitempty"`
	MaxMs int64 `json:"max_ms,omitempty"`
	// Failing: the runs it failed in, and the latest of them.
	Failures   int `json:"failures,omitempty"`
	LastFailed int `json:"last_failed,omitempty"`
	// Flaky: the commits it both passed and failed on (a retry in one run
	// counts), and whether the test tool ran it again until it passed.
	Commits   int  `json:"commits,omitempty"`
	ToolRetry bool `json:"tool_retry,omitempty"`
	LastSeen  int  `json:"last_seen,omitempty"` // the latest run number it was flaky in
}

const overviewTop = 10

// TestOverview looks at a project's last runs of a ref (window of them,
// default 30): their totals, and its slowest, most failing and flaky tests.
// An empty ref means the ref of the project's latest run with results.
func (e *Engine) TestOverview(project, ref string, window int) (*TestOverview, error) {
	if window <= 0 {
		window = 30
	}
	window = min(window, 100)
	out := &TestOverview{Ref: ref, Runs: []OverviewRun{}, Slowest: []TestStat{}, Failing: []TestStat{}, Flaky: []TestStat{}, Refs: []string{}}
	if !e.knows(schemaTests) {
		return out, nil
	}
	rows, err := e.db.Query(`SELECT r.ref FROM test_reports tr JOIN runs r ON r.id = tr.run_id WHERE tr.project = ?
		GROUP BY r.ref ORDER BY MAX(tr.run_number) DESC LIMIT 20`, project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return nil, err
		}
		out.Refs = append(out.Refs, r)
	}
	rows.Close()
	if out.Ref == "" {
		if len(out.Refs) == 0 {
			return out, nil
		}
		out.Ref = out.Refs[0]
	}
	rows, err = e.db.Query(`SELECT id, number, sha, status, created_at FROM runs WHERE project = ? AND ref = ?
		ORDER BY number DESC LIMIT ?`, project, out.Ref, window)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var r OverviewRun
		if err := rows.Scan(&r.ID, &r.Number, &r.SHA, &r.Status, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out.Runs = append(out.Runs, r)
		ids = append(ids, r.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out.Runs)
	ef, err := e.effectiveReports(ids)
	if err != nil {
		return nil, err
	}
	var ts []triple
	sources := map[string]bool{}
	for i := range out.Runs {
		r := &out.Runs[i]
		r.Tests = ef.totals(r.ID)
		for _, t := range ef.triples(r.ID) {
			ts = append(ts, t)
			sources[t.run] = true
		}
	}
	if len(ts) == 0 {
		return out, nil
	}
	// Each result counts once even when two runs link to the same one.
	ts = slices.CompactFunc(slices.SortedFunc(slices.Values(ts), func(a, b triple) int {
		return cmp.Or(strings.Compare(a.run, b.run), strings.Compare(a.step, b.step), cmp.Compare(a.attempt, b.attempt))
	}), func(a, b triple) bool { return a == b })
	cond, args := tripleFilter(ts)

	scanStats := func(q string, args []any, fn func(*TestStat) []any) ([]TestStat, error) {
		rows, err := e.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		list := []TestStat{}
		for rows.Next() {
			var s TestStat
			if err := rows.Scan(fn(&s)...); err != nil {
				return nil, err
			}
			list = append(list, s)
		}
		return list, rows.Err()
	}
	if out.Slowest, err = scanStats(`SELECT tc.test_key, MAX(tc.suite), MAX(tc.name), MAX(tc.file), COUNT(DISTINCT tc.run_id),
			CAST(AVG(tc.duration_ms) AS BIGINT), MAX(tc.duration_ms)
		FROM test_cases tc WHERE `+cond+` AND tc.status <> 'skipped'
		GROUP BY tc.test_key ORDER BY AVG(tc.duration_ms) DESC, tc.test_key LIMIT ?`, append(append([]any{}, args...), overviewTop),
		func(s *TestStat) []any { return []any{&s.Key, &s.Suite, &s.Name, &s.File, &s.Runs, &s.AvgMs, &s.MaxMs} }); err != nil {
		return nil, err
	}
	if out.Failing, err = scanStats(`SELECT tc.test_key, MAX(tc.suite), MAX(tc.name), MAX(tc.file), COUNT(DISTINCT tc.run_id),
			COUNT(DISTINCT CASE WHEN tc.status IN ('failed', 'errored') THEN tc.run_id END),
			MAX(CASE WHEN tc.status IN ('failed', 'errored') THEN tc.run_number END)
		FROM test_cases tc WHERE `+cond+`
		GROUP BY tc.test_key HAVING SUM(CASE WHEN tc.status IN ('failed', 'errored') THEN 1 ELSE 0 END) > 0
		ORDER BY COUNT(DISTINCT CASE WHEN tc.status IN ('failed', 'errored') THEN tc.run_id END) DESC, 7 DESC, tc.test_key LIMIT ?`,
		append(append([]any{}, args...), overviewTop),
		func(s *TestStat) []any {
			return []any{&s.Key, &s.Suite, &s.Name, &s.File, &s.Runs, &s.Failures, &s.LastFailed}
		}); err != nil {
		return nil, err
	}
	// Flaky: every attempt of the runs looked at, not only those that
	// count: a test that failed and passed on the same commit (on a retry,
	// in a re-run or in another run), or that the tool ran again.
	runs := make([]string, 0, len(sources))
	for r := range sources {
		runs = append(runs, r)
	}
	slices.Sort(runs)
	stats := map[string]*TestStat{}
	err = inChunks(runs, func(marks string, rargs []any) error {
		rows, err := e.db.Query(`SELECT tc.test_key, MAX(tc.suite), MAX(tc.name), MAX(tc.file), r.sha, MAX(tc.flaky), MAX(tc.run_number),
				SUM(CASE WHEN tc.status IN ('failed', 'errored') THEN 1 ELSE 0 END), SUM(CASE WHEN tc.status = 'passed' THEN 1 ELSE 0 END)
			FROM test_cases tc JOIN runs r ON r.id = tc.run_id
			WHERE tc.project = ? AND tc.run_id IN (`+marks+`)
			GROUP BY tc.test_key, r.sha
			HAVING (SUM(CASE WHEN tc.status IN ('failed', 'errored') THEN 1 ELSE 0 END) > 0 AND SUM(CASE WHEN tc.status = 'passed' THEN 1 ELSE 0 END) > 0)
				OR MAX(tc.flaky) = 1
			LIMIT 5000`, append([]any{project}, rargs...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, suite, name, file, sha string
			var tool, last, fails, passes int
			if err := rows.Scan(&key, &suite, &name, &file, &sha, &tool, &last, &fails, &passes); err != nil {
				return err
			}
			s := stats[key]
			if s == nil {
				s = &TestStat{Key: key, Suite: suite, Name: name, File: file}
				stats[key] = s
			}
			if fails > 0 && passes > 0 {
				s.Commits++
			}
			s.ToolRetry = s.ToolRetry || tool == 1
			s.LastSeen = max(s.LastSeen, last)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, s := range stats {
		out.Flaky = append(out.Flaky, *s)
	}
	slices.SortFunc(out.Flaky, func(a, b TestStat) int {
		return cmp.Or(cmp.Compare(b.Commits, a.Commits), cmp.Compare(b.LastSeen, a.LastSeen), strings.Compare(a.Key, b.Key))
	})
	if len(out.Flaky) > overviewTop {
		out.Flaky = out.Flaky[:overviewTop]
	}
	if err := e.statVariants(project, out.Slowest, out.Failing, out.Flaky); err != nil {
		return nil, err
	}
	return out, nil
}

// statVariants says which browser or project each test ran as (schema 18),
// so that the same test in two browsers can be told apart.
func (e *Engine) statVariants(project string, lists ...[]TestStat) error {
	if !e.knows(schemaReports) {
		return nil
	}
	var keys []string
	for _, l := range lists {
		for _, s := range l {
			keys = append(keys, s.Key)
		}
	}
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	variant := map[string]string{}
	err := inChunks(keys, func(marks string, args []any) error {
		rows, err := e.db.Query(`SELECT test_key, MAX(variant) FROM test_cases WHERE project = ? AND test_key IN (`+marks+`) GROUP BY test_key`,
			append([]any{project}, args...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			variant[k] = v
		}
		return rows.Err()
	})
	for _, l := range lists {
		for i := range l {
			l[i].Variant = variant[l[i].Key]
		}
	}
	return err
}

// stepTests adds each step's totals to a run's steps.
func (e *Engine) stepTests(runID string, steps []StepView) error {
	if !e.knows(schemaTests) || len(steps) == 0 {
		return nil
	}
	ef, err := e.effectiveReports([]string{runID})
	if err != nil {
		return err
	}
	for i := range steps {
		if r := ef.steps[runID][steps[i].ID]; r != nil {
			t := r.totals
			steps[i].Tests = &t
		}
	}
	return nil
}
