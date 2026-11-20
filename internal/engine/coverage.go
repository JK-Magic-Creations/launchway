package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/coverage"
)

// Coverage reports (schema 18). A step that declares reports.coverage has
// the files collected by its runner once it ends, like test reports. The
// server parses and merges them (they are untrusted) and keeps a summary in
// the event that ends the attempt, coverage_reports derived from it, and
// each source file's counts in coverage_files beside the events, at most
// CoverageFilesPerRun per run and CoverageFilesPerProject per project (the
// oldest runs' go first; their totals stay).

// CoverageCounter counts covered items of one kind.
type CoverageCounter struct {
	Covered int64 `json:"covered"`
	Total   int64 `json:"total"`
}

func (c *CoverageCounter) add(o CoverageCounter) {
	c.Covered = satAdd(c.Covered, o.Covered)
	c.Total = satAdd(c.Total, o.Total)
}

func satAdd(a, b int64) int64 {
	if b > 0 && a > (1<<62)-b {
		return 1 << 62
	}
	return a + b
}

// CoverageTotals are lines, branches and functions covered.
type CoverageTotals struct {
	Lines     CoverageCounter `json:"lines"`
	Branches  CoverageCounter `json:"branches"`
	Functions CoverageCounter `json:"functions"`
	// Unit is what Lines counts: "lines", or "statements" for Go.
	Unit string `json:"unit,omitempty"`
}

func (t *CoverageTotals) add(o CoverageTotals) {
	t.Lines.add(o.Lines)
	t.Branches.add(o.Branches)
	t.Functions.add(o.Functions)
	if t.Unit == "" {
		t.Unit = o.Unit
	}
}

// CoverageReportFile is one coverage report file of an attempt.
type CoverageReportFile struct {
	Path    string `json:"path"`
	Format  string `json:"format,omitempty"`
	Bytes   int64  `json:"bytes"`
	Files   int    `json:"files"` // source files it counts
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// CoverageSummary is what one attempt's coverage reports said.
type CoverageSummary struct {
	CoverageTotals
	// SourceFiles is how many source files the reports count; Stored how
	// many of them were kept in coverage_files.
	SourceFiles int                  `json:"source_files"`
	Stored      int                  `json:"stored"`
	Reports     []CoverageReportFile `json:"reports"`
	ReadFiles   int                  `json:"read_files"` // report files read without an error
	Notes       []string             `json:"notes,omitempty"`
	Truncated   bool                 `json:"truncated,omitempty"`
	// FailedUnder, when not empty, is why the step failed its fail_under.
	FailedUnder string `json:"failed_under,omitempty"`
}

func (s *CoverageSummary) note(n string) {
	if n = cleanText(n, maxReportText); n != "" && len(s.Notes) < maxReportNotes {
		s.Notes = append(s.Notes, n)
	}
}

// parsedCoverage is what an attempt's coverage reports said.
type parsedCoverage struct {
	summary CoverageSummary
	files   []coverage.File
}

// parseCoverage reads the coverage reports a runner sent. It never fails:
// what cannot be read is described in the summary.
func (e *Engine) parseCoverage(in *CollectedReports) *parsedCoverage {
	parseSlots <- struct{}{}
	defer func() { <-parseSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), reportParseTime)
	defer cancel()
	out := &parsedCoverage{summary: CoverageSummary{Reports: []CoverageReportFile{}}}
	s := &out.summary
	for _, n := range in.Notes {
		s.note(n)
	}
	budget := e.reps.CoverageUploadBytes
	lim := coverage.DefaultLimits()
	lim.MaxFiles = max(e.reps.CoverageFilesPerRun, 1)
	var reports []*coverage.Report
	for i, f := range in.Files {
		if i >= maxReportFiles {
			s.note(fmt.Sprintf("%d more files were sent; only the first %d were read", len(in.Files)-i, maxReportFiles))
			s.Truncated = true
			break
		}
		cf := CoverageReportFile{Path: nonEmpty(cleanText(f.Path, maxReportText), "(no name)"), Bytes: int64(len(f.Data))}
		if int64(len(f.Data)) > budget {
			cf.Error = fmt.Sprintf("not read: the step's coverage files came to more than the %s the server accepts", sizeText(e.reps.CoverageUploadBytes))
			s.Truncated = true
			s.Reports = append(s.Reports, cf)
			budget = 0
			continue
		}
		budget -= int64(len(f.Data))
		left := time.Until(deadlineOf(ctx))
		if left <= 0 {
			cf.Error = "not read: reading the step's coverage took too long"
			s.Reports = append(s.Reports, cf)
			continue
		}
		l := lim
		l.Timeout = min(l.Timeout, left)
		rep, err := coverage.Parse(ctx, f.Data, l)
		if err != nil {
			cf.Error = cleanText(err.Error(), maxReportText)
			s.Reports = append(s.Reports, cf)
			continue
		}
		cf.Format, cf.Files, cf.Warning = rep.Format, rep.FileCount, cleanText(rep.Warning, maxReportText)
		if f.Cut {
			cf.Warning = joinText(fmt.Sprintf("the runner kept only the first %s of it", sizeText(int64(len(f.Data)))), cf.Warning)
			s.Truncated = true
		}
		s.ReadFiles++
		reports = append(reports, rep)
		s.Reports = append(s.Reports, cf)
	}
	if len(reports) == 0 {
		return out
	}
	merged := coverage.Merge(reports, lim)
	t := merged.Totals
	s.CoverageTotals = CoverageTotals{
		Lines:     CoverageCounter{t.Lines.Covered, t.Lines.Total},
		Branches:  CoverageCounter{t.Branches.Covered, t.Branches.Total},
		Functions: CoverageCounter{t.Functions.Covered, t.Functions.Total},
		Unit:      merged.Unit,
	}
	s.SourceFiles = merged.FileCount
	s.Truncated = s.Truncated || merged.Truncated
	if merged.Warning != "" {
		s.note(merged.Warning)
	}
	out.files = merged.Files
	return out
}

// keepCoverage applies the run's limit to an attempt's files and keeps
// them to be stored with the command's events. It returns the summary for
// the event.
func (c *cmd) keepCoverage(step string, attempt int, pc *parsedCoverage) (*CoverageSummary, error) {
	sum := pc.summary
	sum.Reports = append([]CoverageReportFile{}, pc.summary.Reports...)
	sum.Notes = append([]string(nil), pc.summary.Notes...)
	var kept int
	if err := c.tx.QueryRow(`SELECT COALESCE(SUM(stored), 0) FROM coverage_reports WHERE run_id = ?`, c.runID).Scan(&kept); err != nil {
		return nil, err
	}
	room := max(c.e.reps.CoverageFilesPerRun-kept, 0)
	files := pc.files
	if len(files) > room {
		files = files[:room]
		sum.Truncated = true
	}
	sum.Stored = len(files)
	if sum.Stored < sum.SourceFiles {
		sum.note(fmt.Sprintf("%d of the %d source files' counts were kept with the run, which keeps at most %d; the totals count them all",
			sum.Stored, sum.SourceFiles, c.e.reps.CoverageFilesPerRun))
	}
	if c.cover == nil {
		c.cover = map[stepAttempt][]coverage.File{}
	}
	c.cover[stepAttempt{step, attempt}] = files
	return &sum, nil
}

// persistSchema18 writes what schema 18 added to the derived tables: the
// coverage summary of each attempt that ended with one, and its files.
func (c *cmd) persistSchema18() error {
	st := c.st
	for _, ev := range c.events {
		p := ev.Data
		if p.Coverage == nil || (ev.Type != StepFinished && ev.Type != StepCanceled) {
			continue
		}
		cv := p.Coverage
		detail, err := json.Marshal(struct {
			Reports     []CoverageReportFile `json:"reports"`
			Notes       []string             `json:"notes,omitempty"`
			Unit        string               `json:"unit,omitempty"`
			FailedUnder string               `json:"failed_under,omitempty"`
			Truncated   bool                 `json:"truncated,omitempty"`
		}{cv.Reports, cv.Notes, cv.Unit, cv.FailedUnder, cv.Truncated})
		if err != nil {
			return err
		}
		if _, err := c.tx.Exec(`INSERT INTO coverage_reports(run_id, step_id, attempt, project, run_number, at, lines_covered, lines_total,
				branches_covered, branches_total, functions_covered, functions_total, files, stored, pruned, detail)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)
			ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET at = excluded.at, lines_covered = excluded.lines_covered,
				lines_total = excluded.lines_total, branches_covered = excluded.branches_covered, branches_total = excluded.branches_total,
				functions_covered = excluded.functions_covered, functions_total = excluded.functions_total, files = excluded.files,
				stored = excluded.stored, pruned = 0, detail = excluded.detail`,
			st.ID, p.Step, p.Attempt, st.Project, st.Number, ev.At, cv.Lines.Covered, cv.Lines.Total, cv.Branches.Covered,
			cv.Branches.Total, cv.Functions.Covered, cv.Functions.Total, cv.SourceFiles, cv.Stored, string(detail)); err != nil {
			return err
		}
		if _, err := c.tx.Exec(`DELETE FROM coverage_files WHERE run_id = ? AND step_id = ? AND attempt = ?`, st.ID, p.Step, p.Attempt); err != nil {
			return err
		}
		files := c.cover[stepAttempt{p.Step, p.Attempt}]
		for len(files) > 0 {
			n := min(len(files), casesPerInsert)
			batch := files[:n]
			files = files[n:]
			var q strings.Builder
			q.WriteString(`INSERT INTO coverage_files(run_id, step_id, attempt, project, run_number, path, package, lines_covered, lines_total,
				branches_covered, branches_total, functions_covered, functions_total) VALUES `)
			args := make([]any, 0, n*13)
			for i, f := range batch {
				if i > 0 {
					q.WriteString(",")
				}
				q.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?)")
				args = append(args, st.ID, p.Step, p.Attempt, st.Project, st.Number, f.Path, f.Package, f.Lines.Covered, f.Lines.Total,
					f.Branches.Covered, f.Branches.Total, f.Functions.Covered, f.Functions.Total)
			}
			if _, err := c.tx.Exec(q.String(), args...); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- queries ----

// CoverageStep is one step's coverage in a run.
type CoverageStep struct {
	Step    string `json:"step"`
	Attempt int    `json:"attempt"`
	// ReusedFrom is set when a re-run reused the step: the run where it ran.
	ReusedFrom       string `json:"reused_from,omitempty"`
	ReusedFromNumber int    `json:"reused_from_number,omitempty"`
	CoverageTotals
	SourceFiles int                  `json:"source_files"`
	Stored      int                  `json:"stored"`
	Pruned      bool                 `json:"pruned,omitempty"`
	Reports     []CoverageReportFile `json:"reports"`
	Notes       []string             `json:"notes,omitempty"`
	FailedUnder string               `json:"failed_under,omitempty"`
	Threshold   *CoverageThresholdV  `json:"threshold,omitempty"`
}

// CoverageThresholdV is a step's fail_under, as shown.
type CoverageThresholdV struct {
	Lines     float64 `json:"lines,omitempty"`
	Branches  float64 `json:"branches,omitempty"`
	Functions float64 `json:"functions,omitempty"`
}

// CoverageCompare is a run's coverage beside another run's.
type CoverageCompare struct {
	Run    RunLink        `json:"run"`
	Ref    string         `json:"ref"`
	Totals CoverageTotals `json:"totals"`
}

// RunCoverage is a run's coverage.
type RunCoverage struct {
	Totals *CoverageTotals `json:"totals"` // nil when no step reported coverage
	Steps  []CoverageStep  `json:"steps"`
	// Declared are the steps whose pipeline declares coverage reports.
	Declared []string `json:"declared"`
	// Previous is the run before this one on the same ref with coverage;
	// Base the latest run of the project's default branch (when this run
	// is not on it).
	Previous *CoverageCompare `json:"previous,omitempty"`
	Base     *CoverageCompare `json:"base,omitempty"`
}

// coverageRow is one row of coverage_reports.
type coverageRow struct {
	run, step   string
	attempt     int
	number      int
	totals      CoverageTotals
	files       int
	stored      int
	pruned      bool
	detail      string
	reusedFrom  string
	reusedFromN int
}

// effectiveCoverage returns, for each step of a run, the coverage of the
// attempt that counts: the latest, or for a step a re-run reused, the
// attempt where it ran.
func (e *Engine) effectiveCoverage(st *RunState) ([]coverageRow, error) {
	var out []coverageRow
	for _, id := range st.Order {
		s := st.Steps[id]
		if s == nil {
			continue
		}
		run, attempt := st.ID, 0
		if s.ReusedFrom != "" {
			run, attempt = s.ReusedFrom, s.ReusedAttempt
		}
		q := `SELECT run_id, step_id, attempt, run_number, lines_covered, lines_total, branches_covered, branches_total, functions_covered,
			functions_total, files, stored, pruned, detail FROM coverage_reports WHERE run_id = ? AND step_id = ?`
		args := []any{run, id}
		if attempt > 0 {
			q += ` AND attempt = ?`
			args = append(args, attempt)
		}
		q += ` ORDER BY attempt DESC LIMIT 1`
		var r coverageRow
		var pruned int
		err := e.db.QueryRow(q, args...).Scan(&r.run, &r.step, &r.attempt, &r.number, &r.totals.Lines.Covered, &r.totals.Lines.Total,
			&r.totals.Branches.Covered, &r.totals.Branches.Total, &r.totals.Functions.Covered, &r.totals.Functions.Total,
			&r.files, &r.stored, &pruned, &r.detail)
		if err != nil {
			if isNoRows(err) {
				continue
			}
			return nil, err
		}
		r.pruned = pruned != 0
		var d struct {
			Unit string `json:"unit"`
		}
		_ = json.Unmarshal([]byte(r.detail), &d)
		r.totals.Unit = d.Unit
		if s.ReusedFrom != "" {
			r.reusedFrom = s.ReusedFrom
			_ = e.db.QueryRow(`SELECT number FROM runs WHERE id = ?`, s.ReusedFrom).Scan(&r.reusedFromN)
		}
		out = append(out, r)
	}
	return out, nil
}

// runCoverageTotals adds up a run's coverage, or nil if it has none.
func (e *Engine) runCoverageTotals(runID string) (*CoverageTotals, error) {
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	rows, err := e.effectiveCoverage(st)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	t := &CoverageTotals{}
	for _, r := range rows {
		t.add(r.totals)
	}
	return t, nil
}

// RunCoverage returns a run's coverage, step by step, and how it compares
// with the run before it on the same ref and with the default branch.
func (e *Engine) RunCoverage(runID, defaultRef string) (*RunCoverage, error) {
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	out := &RunCoverage{Steps: []CoverageStep{}, Declared: []string{}}
	for _, s := range st.Pipeline.Steps {
		if needsCoverage(s) {
			out.Declared = append(out.Declared, s.ID)
		}
	}
	if !e.knows(schemaReports) {
		return out, nil
	}
	rows, err := e.effectiveCoverage(st)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		cs := CoverageStep{Step: r.step, Attempt: r.attempt, ReusedFrom: r.reusedFrom, ReusedFromNumber: r.reusedFromN,
			CoverageTotals: r.totals, SourceFiles: r.files, Stored: r.stored, Pruned: r.pruned, Reports: []CoverageReportFile{}}
		var d struct {
			Reports     []CoverageReportFile `json:"reports"`
			Notes       []string             `json:"notes"`
			FailedUnder string               `json:"failed_under"`
		}
		_ = json.Unmarshal([]byte(r.detail), &d)
		if d.Reports != nil {
			cs.Reports = d.Reports
		}
		cs.Notes, cs.FailedUnder = d.Notes, d.FailedUnder
		if def := st.Pipeline.Step(r.step); def != nil && def.Reports != nil && def.Reports.CoverageFailUnder != nil {
			th := def.Reports.CoverageFailUnder
			cs.Threshold = &CoverageThresholdV{Lines: th.Lines, Branches: th.Branches, Functions: th.Functions}
		}
		if out.Totals == nil {
			out.Totals = &CoverageTotals{}
		}
		out.Totals.add(r.totals)
		out.Steps = append(out.Steps, cs)
	}
	if out.Totals == nil {
		return out, nil
	}
	if prev, err := e.coverageBefore(st, st.Trigger.Ref); err != nil {
		return nil, err
	} else if prev != nil {
		out.Previous = prev
	}
	if defaultRef != "" && defaultRef != st.Trigger.Ref {
		base, err := e.coverageBefore(st, defaultRef)
		if err != nil {
			return nil, err
		}
		out.Base = base
	}
	return out, nil
}

// coverageBefore finds the latest run of a ref, before st, with coverage.
func (e *Engine) coverageBefore(st *RunState, ref string) (*CoverageCompare, error) {
	if ref == "" {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT DISTINCT r.id, r.number FROM runs r JOIN coverage_reports c ON c.run_id = r.id
		WHERE r.project = ? AND r.ref = ? AND r.number < ? ORDER BY r.number DESC LIMIT 5`, st.Project, ref, st.Number)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id     string
		number int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.number); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	for _, c := range cands {
		t, err := e.runCoverageTotals(c.id)
		if err != nil {
			return nil, err
		}
		if t != nil {
			return &CoverageCompare{Run: RunLink{ID: c.id, Number: c.number}, Ref: ref, Totals: *t}, nil
		}
	}
	return nil, nil
}

// CoverageFileQuery narrows a run's per-file coverage.
type CoverageFileQuery struct {
	Step   string // only this step
	Search string // in the path or package
	// Sort is "path" (the default), "lines" (least covered first) or
	// "missed" (most lines missed first).
	Sort   string
	Limit  int // default 500, at most 5000
	Offset int
}

// CoverageFileRow is one source file's coverage.
type CoverageFileRow struct {
	Step      string          `json:"step"`
	Path      string          `json:"path"`
	Package   string          `json:"package,omitempty"`
	Lines     CoverageCounter `json:"lines"`
	Branches  CoverageCounter `json:"branches"`
	Functions CoverageCounter `json:"functions"`
	// Change is the change in covered lines' percentage against the
	// previous run on the same ref, in points; nil when the file was not
	// in it.
	Change *float64 `json:"change,omitempty"`
}

// CoveragePackage is a package's (a directory's) coverage, added up.
type CoveragePackage struct {
	Package   string          `json:"package"`
	Files     int             `json:"files"`
	Lines     CoverageCounter `json:"lines"`
	Branches  CoverageCounter `json:"branches"`
	Functions CoverageCounter `json:"functions"`
}

// CoverageFiles is a page of a run's per-file coverage, and its packages.
type CoverageFiles struct {
	Files    []CoverageFileRow `json:"files"`
	Packages []CoveragePackage `json:"packages"`
	Matching int               `json:"matching"`
	Offset   int               `json:"offset"`
	Limit    int               `json:"limit"`
}

// RunCoverageFiles lists a run's per-file coverage.
func (e *Engine) RunCoverageFiles(runID string, q CoverageFileQuery) (*CoverageFiles, error) {
	if q.Limit <= 0 {
		q.Limit = 500
	}
	q.Limit = min(q.Limit, 5000)
	q.Offset = min(max(q.Offset, 0), 1_000_000)
	order := `path`
	switch q.Sort {
	case "", "path":
	case "lines":
		order = `CASE WHEN lines_total = 0 THEN 2 ELSE 1 END, CAST(lines_covered AS REAL) / CAST(MAX(lines_total, 1) AS REAL), path`
	case "missed":
		order = `(lines_total - lines_covered) DESC, path`
	default:
		return nil, fmt.Errorf("%w: sort is path, lines or missed", ErrInvalid)
	}
	out := &CoverageFiles{Files: []CoverageFileRow{}, Packages: []CoveragePackage{}, Offset: q.Offset, Limit: q.Limit}
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	if !e.knows(schemaReports) {
		return out, nil
	}
	rows, err := e.effectiveCoverage(st)
	if err != nil {
		return nil, err
	}
	var where []string
	var args []any
	for _, r := range rows {
		if q.Step != "" && r.step != q.Step {
			continue
		}
		where = append(where, `(run_id = ? AND step_id = ? AND attempt = ?)`)
		args = append(args, r.run, r.step, r.attempt)
	}
	if len(where) == 0 {
		return out, nil
	}
	base := `(` + strings.Join(where, " OR ") + `)`
	if s := strings.TrimSpace(q.Search); s != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(s)) + "%"
		base += ` AND (LOWER(path) LIKE ? ESCAPE '\' OR LOWER(package) LIKE ? ESCAPE '\')`
		args = append(args, like, like)
	}
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM coverage_files WHERE `+base, args...).Scan(&out.Matching); err != nil {
		return nil, err
	}
	if e.db.pg {
		order = strings.ReplaceAll(order, "MAX(lines_total, 1)", "GREATEST(lines_total, 1)")
	}
	res, err := e.db.Query(`SELECT step_id, path, package, lines_covered, lines_total, branches_covered, branches_total, functions_covered,
		functions_total FROM coverage_files WHERE `+base+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, err
	}
	for res.Next() {
		var f CoverageFileRow
		if err := res.Scan(&f.Step, &f.Path, &f.Package, &f.Lines.Covered, &f.Lines.Total, &f.Branches.Covered, &f.Branches.Total,
			&f.Functions.Covered, &f.Functions.Total); err != nil {
			res.Close()
			return nil, err
		}
		out.Files = append(out.Files, f)
	}
	res.Close()
	if err := res.Err(); err != nil {
		return nil, err
	}
	pk, err := e.db.Query(`SELECT package, COUNT(*), SUM(lines_covered), SUM(lines_total), SUM(branches_covered), SUM(branches_total),
		SUM(functions_covered), SUM(functions_total) FROM coverage_files WHERE `+base+` GROUP BY package ORDER BY package LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	for pk.Next() {
		var p CoveragePackage
		if err := pk.Scan(&p.Package, &p.Files, &p.Lines.Covered, &p.Lines.Total, &p.Branches.Covered, &p.Branches.Total,
			&p.Functions.Covered, &p.Functions.Total); err != nil {
			pk.Close()
			return nil, err
		}
		out.Packages = append(out.Packages, p)
	}
	pk.Close()
	if err := pk.Err(); err != nil {
		return nil, err
	}
	// Change against the previous run on the same ref, for the files shown.
	if prev, err := e.coverageBefore(st, st.Trigger.Ref); err == nil && prev != nil && len(out.Files) > 0 {
		pst, err := e.runState(prev.Run.ID)
		if err != nil {
			return nil, err
		}
		prows, err := e.effectiveCoverage(pst)
		if err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(out.Files))
		for _, f := range out.Files {
			paths = append(paths, f.Path)
		}
		paths = slices.Compact(slices.Sorted(slices.Values(paths)))
		before := map[string]CoverageCounter{}
		for _, r := range prows {
			if err := inChunks(paths, func(marks string, pargs []any) error {
				rs, err := e.db.Query(`SELECT path, lines_covered, lines_total FROM coverage_files WHERE run_id = ? AND step_id = ? AND attempt = ?
					AND path IN (`+marks+`)`, append([]any{r.run, r.step, r.attempt}, pargs...)...)
				if err != nil {
					return err
				}
				defer rs.Close()
				for rs.Next() {
					var p string
					var c CoverageCounter
					if err := rs.Scan(&p, &c.Covered, &c.Total); err != nil {
						return err
					}
					b := before[p]
					b.add(c)
					before[p] = b
				}
				return rs.Err()
			}); err != nil {
				return nil, err
			}
		}
		for i := range out.Files {
			f := &out.Files[i]
			b, ok := before[f.Path]
			if !ok || b.Total == 0 || f.Lines.Total == 0 {
				continue
			}
			d := 100*float64(f.Lines.Covered)/float64(f.Lines.Total) - 100*float64(b.Covered)/float64(b.Total)
			f.Change = &d
		}
	}
	return out, nil
}

// CoverageTrend is a project's coverage over the last runs of a ref.
type CoverageTrend struct {
	Ref  string             `json:"ref"`
	Runs []CoverageTrendRun `json:"runs"` // oldest first
}

// CoverageTrendRun is one run's coverage in a trend.
type CoverageTrendRun struct {
	ID        string         `json:"id"`
	Number    int            `json:"number"`
	SHA       string         `json:"sha"`
	CreatedAt int64          `json:"created_at"`
	Totals    CoverageTotals `json:"totals"`
}

// ProjectCoverage returns the coverage of a project's last runs of a ref
// ("" for every ref) that reported some, oldest first.
func (e *Engine) ProjectCoverage(project, ref string, window int) (*CoverageTrend, error) {
	if window <= 0 {
		window = 30
	}
	window = min(window, 200)
	out := &CoverageTrend{Ref: ref, Runs: []CoverageTrendRun{}}
	if !e.knows(schemaReports) {
		return out, nil
	}
	q := `SELECT r.id, r.number, r.sha, r.created_at FROM runs r WHERE r.project = ? AND EXISTS (SELECT 1 FROM coverage_reports c WHERE c.run_id = r.id)`
	args := []any{project}
	if ref != "" {
		q += ` AND r.ref = ?`
		args = append(args, ref)
	}
	q += ` ORDER BY r.number DESC LIMIT ?`
	rows, err := e.db.Query(q, append(args, window)...)
	if err != nil {
		return nil, err
	}
	var runs []CoverageTrendRun
	for rows.Next() {
		var r CoverageTrendRun
		if err := rows.Scan(&r.ID, &r.Number, &r.SHA, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		t, err := e.runCoverageTotals(runs[i].ID)
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue
		}
		runs[i].Totals = *t
		out.Runs = append(out.Runs, runs[i])
	}
	return out, nil
}

// pruneCoverage removes the per-file counts of each project's oldest runs
// while the project keeps more than CoverageFilesPerProject; the totals
// stay. It works like pruneTests.
func (e *Engine) pruneCoverage() {
	if !e.knows(schemaReports) {
		return
	}
	limit := e.reps.CoverageFilesPerProject
	rows, err := e.db.Query(`SELECT project FROM coverage_reports GROUP BY project HAVING SUM(stored) > ?`, limit)
	if err != nil {
		e.log.Error("looking for projects with too much coverage kept", "err", err)
		return
	}
	var projects []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			projects = append(projects, p)
		}
	}
	rows.Close()
	for _, p := range projects {
		if err := e.pruneCoverageOf(p, limit); err != nil {
			e.log.Warn("removing a project's oldest coverage files; trying again later", "project", p, "err", err)
		}
	}
}

func (e *Engine) pruneCoverageOf(project string, limit int) error {
	rows, err := e.db.Query(`SELECT run_number, SUM(stored) FROM coverage_reports WHERE project = ? AND stored > 0
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
	_, err = e.db.Exec(`UPDATE coverage_reports SET stored = 0, pruned = 1 WHERE project = ? AND run_number <= ? AND (stored > 0 OR pruned = 0)`,
		project, cutoff)
	unlock()
	if err != nil {
		return err
	}
	for {
		unlock := e.writeLock()
		res, err := e.db.Exec(`DELETE FROM coverage_files WHERE id IN (SELECT id FROM coverage_files WHERE project = ? AND run_number <= ?
			ORDER BY id LIMIT ?)`, project, cutoff, pruneBatch)
		unlock()
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n < pruneBatch {
			return nil
		}
	}
}
