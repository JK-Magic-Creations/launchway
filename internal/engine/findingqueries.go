package engine

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

// Reading findings and SBOMs back. As for test results, a run's findings
// are, for each step that has any, those of its latest attempt that
// reported, or, for a step a re-run reused, those of the attempt it reused
// in the run where it ran.
//
// A finding's status in a run compares it with the previous run of the
// same ref that reported findings: new (its fingerprint was not there),
// existing (it was), or fixed (it was there, and is not now). Only tools
// that ran in both runs are compared, so a scanner that did not run this
// time does not make its findings look fixed.

// findingReportRow is a row of finding_reports.
type findingReportRow struct {
	run, step       string
	attempt, number int
	counts          FindingCounts
	stored          int
	pruned          bool
	at              int64
	tools           []string
	detail          string
}

func (r *findingReportRow) triple() triple { return triple{r.run, r.step, r.attempt} }

// effectiveFindings finds the finding reports that count for each run:
// run → step → report.
func (e *Engine) effectiveFindings(runIDs []string) (map[string]map[string]*findingReportRow, map[string]map[string]reusedRef, error) {
	out := map[string]map[string]*findingReportRow{}
	reused := map[string]map[string]reusedRef{}
	if !e.knows(schemaArtifacts) || len(runIDs) == 0 {
		return out, reused, nil
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
			if reused[run] == nil {
				reused[run] = map[string]reusedRef{}
			}
			reused[run][step] = r
			sources[r.run] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	all := slices.Clone(runIDs)
	for id := range sources {
		all = append(all, id)
	}
	all = slices.Compact(slices.Sorted(slices.Values(all)))
	byTriple := map[triple]*findingReportRow{}
	latest := map[string]map[string]*findingReportRow{}
	err = inChunks(all, func(marks string, args []any) error {
		rows, err := e.db.Query(`SELECT run_id, step_id, attempt, run_number, total, critical, high, medium, low, unknown, suppressed,
				truncated, stored, pruned, at, tools, detail
			FROM finding_reports WHERE run_id IN (`+marks+`) ORDER BY run_id, step_id, attempt`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := &findingReportRow{}
			var truncated, pruned int
			var tools string
			c := &r.counts
			if err := rows.Scan(&r.run, &r.step, &r.attempt, &r.number, &c.Total, &c.Critical, &c.High, &c.Medium, &c.Low, &c.Unknown,
				&c.Suppressed, &truncated, &r.stored, &pruned, &r.at, &tools, &r.detail); err != nil {
				return err
			}
			c.Truncated, r.pruned = truncated != 0, pruned != 0
			_ = json.Unmarshal([]byte(tools), &r.tools)
			byTriple[r.triple()] = r
			if latest[r.run] == nil {
				latest[r.run] = map[string]*findingReportRow{}
			}
			latest[r.run][r.step] = r
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	for _, run := range runIDs {
		m := map[string]*findingReportRow{}
		for step, r := range latest[run] {
			if _, ok := reused[run][step]; !ok {
				m[step] = r
			}
		}
		for step, ref := range reused[run] {
			if r := byTriple[triple{ref.run, step, ref.attempt}]; r != nil {
				m[step] = r
			}
		}
		if len(m) > 0 {
			out[run] = m
		}
	}
	return out, reused, nil
}

func findingTotals(steps map[string]*findingReportRow) *FindingCounts {
	if len(steps) == 0 {
		return nil
	}
	t := &FindingCounts{}
	for _, r := range steps {
		t.add(r.counts)
	}
	return t
}

func findingTriples(steps map[string]*findingReportRow) []triple {
	var out []triple
	for _, r := range steps {
		out = append(out, r.triple())
	}
	slices.SortFunc(out, func(a, b triple) int { return cmp.Or(strings.Compare(a.step, b.step), strings.Compare(a.run, b.run)) })
	return out
}

func findingTools(steps map[string]*findingReportRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range steps {
		for _, t := range r.tools {
			out[t] = true
		}
	}
	return out
}

// attachFindingCounts adds finding counts to runs as listed.
func (e *Engine) attachFindingCounts(runs []*RunSummary) error {
	if !e.knows(schemaArtifacts) || len(runs) == 0 {
		return nil
	}
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	ef, _, err := e.effectiveFindings(ids)
	if err != nil {
		return err
	}
	for _, r := range runs {
		r.Findings = findingTotals(ef[r.ID])
	}
	return nil
}

// FindingView is one finding as the API shows it.
type FindingView struct {
	ID          int64  `json:"id"`
	Step        string `json:"step"`
	Attempt     int    `json:"attempt"`
	RunID       string `json:"run_id"`
	RunNumber   int    `json:"run_number"`
	Fingerprint string `json:"fingerprint"`
	Tool        string `json:"tool"`
	RuleID      string `json:"rule_id"`
	Severity    string `json:"severity"`
	Level       string `json:"level,omitempty"`
	Message     string `json:"message"`
	File        string `json:"file,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	StartColumn int    `json:"start_column,omitempty"`
	// Secret says the finding is about a secret: its value is never kept.
	Secret bool `json:"secret,omitempty"`
	// Decision is the person's decision about it that is in force: accepted risk or false positive.
	Decision *DecisionView `json:"decision,omitempty"`
	track    string
	// Status is new, existing or fixed, compared with the previous run of
	// the same ref; empty when there is none to compare with.
	Status string `json:"status,omitempty"`
	// FirstSeen is the oldest run of the ref still kept that had the
	// finding (project views only).
	FirstSeen *RunLink  `json:"first_seen,omitempty"`
	Rule      *RuleView `json:"rule,omitempty"`
}

// RuleView describes the rule a finding broke, as its tool does. Help is
// plain text; HelpURI is an http or https link.
type RuleView struct {
	Name    string   `json:"name,omitempty"`
	Short   string   `json:"short,omitempty"`
	Full    string   `json:"full,omitempty"`
	Help    string   `json:"help,omitempty"`
	HelpURI string   `json:"help_uri,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

// Finding statuses.
const (
	FindingNew      = "new"
	FindingExisting = "existing"
	FindingFixed    = "fixed"
)

// StatusCounts counts findings by status.
type StatusCounts struct {
	New      int `json:"new"`
	Existing int `json:"existing"`
	Fixed    int `json:"fixed"`
}

// StepFindings is one step's findings: those of the attempt that counts.
type StepFindings struct {
	Step      string        `json:"step"`
	Attempt   int           `json:"attempt"`
	Counts    FindingCounts `json:"counts"`
	Tools     []string      `json:"tools"`
	Files     []FindingFile `json:"files"`
	Notes     []string      `json:"notes,omitempty"`
	Stored    int           `json:"stored"`
	Pruned    bool          `json:"pruned,omitempty"`
	ReusedRun *RunLink      `json:"reused_from,omitempty"`
}

// StepSBOM is one step's SBOM summary.
type StepSBOM struct {
	Step       string         `json:"step"`
	Attempt    int            `json:"attempt"`
	Components int            `json:"components"`
	Stored     int            `json:"stored"`
	Truncated  bool           `json:"truncated,omitempty"`
	Pruned     bool           `json:"pruned,omitempty"`
	Licences   map[string]int `json:"licences"`
	Files      []SBOMFile     `json:"files"`
	Notes      []string       `json:"notes,omitempty"`
	Artifact   string         `json:"artifact,omitempty"`
	RunID      string         `json:"run_id"`
}

// FindingQuery filters a run's findings.
type FindingQuery struct {
	Severities []string // any of critical, high, medium, low, unknown; none: all
	Tool       string
	Status     string // new, existing or fixed; "": new and existing
	Search     string // in the rule, message or file
	Step       string
	Limit      int // default 100, at most 500
	Offset     int
}

// RunFindings is a run's security findings with what stands out.
type RunFindings struct {
	Counts   *FindingCounts `json:"counts"`
	Statuses StatusCounts   `json:"statuses"`
	// Previous is the run the statuses compare with.
	Previous *RunLink       `json:"previous,omitempty"`
	Steps    []StepFindings `json:"steps"`
	SBOMs    []StepSBOM     `json:"sboms"`
	Declared []string       `json:"declared"` // steps that declare reports.findings or reports.sbom
	Tools    []string       `json:"tools"`
	// Findings are those that match the query, most severe first; Matched
	// is how many match in all.
	Findings []FindingView `json:"findings"`
	Matched  int           `json:"matched"`
	// Open is what still counts: Counts without the findings a person has accepted as a risk or marked a false
	// positive (see the project's Standing orders), which are Accepted. Nil when none is decided.
	Open     *FindingCounts `json:"open,omitempty"`
	Accepted int            `json:"accepted,omitempty"`
}

var severityRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "unknown": 4}

// loadFindings reads the findings of some step attempts.
func (e *Engine) loadFindings(ts []triple, extra string, extraArgs ...any) ([]FindingView, error) {
	if len(ts) == 0 {
		return nil, nil
	}
	cond, args := findingTripleFilter(ts)
	trackCol := "track" // a database older than the Standing orders schema has no such column
	if !e.knows(schemaStandings) {
		trackCol = "'' AS track"
	}
	rows, err := e.db.Query(`SELECT id, step_id, attempt, run_id, run_number, fingerprint, tool, rule_id, severity, level, message, file,
			start_line, end_line, start_column, secret, `+trackCol+` FROM findings f WHERE `+cond+extra+` ORDER BY id`, append(args, extraArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FindingView
	for rows.Next() {
		var f FindingView
		var secret int
		if err := rows.Scan(&f.ID, &f.Step, &f.Attempt, &f.RunID, &f.RunNumber, &f.Fingerprint, &f.Tool, &f.RuleID, &f.Severity, &f.Level,
			&f.Message, &f.File, &f.StartLine, &f.EndLine, &f.StartColumn, &secret, &f.track); err != nil {
			return nil, err
		}
		f.Secret = secret != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// findingTripleFilter is tripleFilter for the findings table, aliased f.
func findingTripleFilter(ts []triple) (string, []any) {
	parts := make([]string, 0, len(ts))
	args := make([]any, 0, 3*len(ts))
	for _, t := range ts {
		parts = append(parts, "(f.run_id = ? AND f.step_id = ? AND f.attempt = ?)")
		args = append(args, t.run, t.step, t.attempt)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// previousWithFindings is the run before st of the same ref that reported
// findings, and the reports that count in it.
func (e *Engine) previousWithFindings(st *RunState) (*RunLink, map[string]*findingReportRow, error) {
	if st.Trigger.Ref == "" {
		return nil, nil, nil
	}
	ids, err := e.idsOf(`SELECT id FROM runs WHERE project = ? AND ref = ? AND number < ? ORDER BY number DESC LIMIT 25`,
		st.Project, st.Trigger.Ref, st.Number)
	if err != nil || len(ids) == 0 {
		return nil, nil, err
	}
	ef, _, err := e.effectiveFindings(ids)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		if steps := ef[id]; len(steps) > 0 {
			var number int
			if err := e.db.QueryRow(`SELECT number FROM runs WHERE id = ?`, id).Scan(&number); err != nil {
				return nil, nil, err
			}
			return &RunLink{ID: id, Number: number}, steps, nil
		}
	}
	return nil, nil, nil
}

// compare marks each finding new or existing, and returns the previous
// run's findings that are fixed now (of the tools that ran in both).
func (e *Engine) compare(now []FindingView, toolsNow map[string]bool, prev map[string]*findingReportRow) ([]FindingView, error) {
	if prev == nil {
		return nil, nil
	}
	toolsPrev := findingTools(prev)
	before, err := e.loadFindings(findingTriples(prev), "")
	if err != nil {
		return nil, err
	}
	had := map[string]bool{}
	for _, f := range before {
		had[f.Fingerprint] = true
	}
	present := map[string]bool{}
	for i := range now {
		present[now[i].Fingerprint] = true
		if !toolsPrev[now[i].Tool] {
			continue // a tool that did not run before: nothing to compare with
		}
		if had[now[i].Fingerprint] {
			now[i].Status = FindingExisting
		} else {
			now[i].Status = FindingNew
		}
	}
	var fixed []FindingView
	seen := map[string]bool{}
	for _, f := range before {
		if present[f.Fingerprint] || seen[f.Fingerprint] || !toolsNow[f.Tool] {
			continue
		}
		seen[f.Fingerprint] = true
		f.Status = FindingFixed
		fixed = append(fixed, f)
	}
	return fixed, nil
}

// RunFindings returns a run's findings, step by step, and those that match
// the query.
func (e *Engine) RunFindings(runID string, q FindingQuery) (*RunFindings, error) {
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	out := &RunFindings{Steps: []StepFindings{}, SBOMs: []StepSBOM{}, Declared: []string{}, Tools: []string{}, Findings: []FindingView{}}
	for _, s := range st.Pipeline.Steps {
		if s.Reports != nil && (len(s.Reports.Findings) > 0 || len(s.Reports.SBOM) > 0) {
			out.Declared = append(out.Declared, s.ID)
		}
	}
	if !e.knows(schemaArtifacts) {
		return out, nil
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	q.Limit = min(q.Limit, 500)
	ef, reused, err := e.effectiveFindings([]string{runID})
	if err != nil {
		return nil, err
	}
	steps := ef[runID]
	out.Counts = findingTotals(steps)
	position := map[string]int{}
	for i, id := range st.Order {
		position[id] = i
	}
	for step, r := range steps {
		sf := StepFindings{Step: step, Attempt: r.attempt, Counts: r.counts, Tools: nonNilStrings(r.tools), Files: []FindingFile{},
			Stored: r.stored, Pruned: r.pruned}
		var d struct {
			Files []FindingFile `json:"files"`
			Notes []string      `json:"notes"`
		}
		if json.Unmarshal([]byte(r.detail), &d) == nil {
			sf.Files, sf.Notes = nonNilFiles(d.Files), d.Notes
		}
		if ref, ok := reused[runID][step]; ok {
			sf.ReusedRun = &RunLink{ID: ref.run, Number: ref.number}
		}
		out.Steps = append(out.Steps, sf)
	}
	slices.SortFunc(out.Steps, func(a, b StepFindings) int { return position[a.Step] - position[b.Step] })
	tools := findingTools(steps)
	for t := range tools {
		out.Tools = append(out.Tools, t)
	}
	slices.Sort(out.Tools)
	if out.SBOMs, err = e.runSBOMs(st, position); err != nil {
		return nil, err
	}

	all, err := e.loadFindings(findingTriples(steps), "")
	if err != nil {
		return nil, err
	}
	prevLink, prev, err := e.previousWithFindings(st)
	if err != nil {
		return nil, err
	}
	out.Previous = prevLink
	fixed, err := e.compare(all, tools, prev)
	if err != nil {
		return nil, err
	}
	out.Statuses = countStatuses(all, len(fixed))
	if err := e.applyDecisions(st.Project, out, all); err != nil {
		return nil, err
	}
	pool := all
	if q.Status == FindingFixed {
		pool = fixed
	}
	matched := filterFindings(pool, q)
	slices.SortStableFunc(matched, func(a, b FindingView) int {
		return cmp.Or(cmp.Compare(severityRank[a.Severity], severityRank[b.Severity]),
			cmp.Compare(statusRank(a.Status), statusRank(b.Status)),
			strings.Compare(a.File, b.File), cmp.Compare(a.StartLine, b.StartLine), strings.Compare(a.RuleID, b.RuleID))
	})
	out.Matched = len(matched)
	if q.Offset < len(matched) {
		matched = matched[q.Offset:]
	} else {
		matched = nil
	}
	if len(matched) > q.Limit {
		matched = matched[:q.Limit]
	}
	if err := e.attachRules(matched); err != nil {
		return nil, err
	}
	out.Findings = append(out.Findings, matched...)
	return out, nil
}

func statusRank(s string) int {
	switch s {
	case FindingNew:
		return 0
	case FindingExisting:
		return 1
	}
	return 2
}

func nonNilFiles(f []FindingFile) []FindingFile {
	if f == nil {
		return []FindingFile{}
	}
	return f
}

// attachRules adds the rule each finding broke, as its report described it.
func (e *Engine) attachRules(list []FindingView) error {
	type key struct {
		run, step  string
		attempt    int
		tool, rule string
	}
	cache := map[key]*RuleView{}
	for i := range list {
		f := &list[i]
		k := key{f.RunID, f.Step, f.Attempt, f.Tool, f.RuleID}
		r, ok := cache[k]
		if !ok {
			var rv RuleView
			var tags string
			err := e.db.QueryRow(`SELECT name, short, full_text, help, help_uri, tags FROM finding_rules
				WHERE run_id = ? AND step_id = ? AND attempt = ? AND tool = ? AND rule_id = ?`, k.run, k.step, k.attempt, k.tool, k.rule).
				Scan(&rv.Name, &rv.Short, &rv.Full, &rv.Help, &rv.HelpURI, &tags)
			if err == nil {
				_ = json.Unmarshal([]byte(tags), &rv.Tags)
				r = &rv
			} else if !isNoRows(err) {
				return err
			}
			cache[k] = r
		}
		f.Rule = r
	}
	return nil
}

// runSBOMs lists the SBOM summaries that count for a run.
func (e *Engine) runSBOMs(st *RunState, position map[string]int) ([]StepSBOM, error) {
	out := []StepSBOM{}
	latest := map[string]StepSBOM{}
	load := func(run, step string, attempt int) error {
		q := `SELECT run_id, step_id, attempt, components, stored, truncated, pruned, licences, detail FROM sbom_reports WHERE run_id = ?`
		args := []any{run}
		if step != "" {
			q += ` AND step_id = ? AND attempt = ?`
			args = append(args, step, attempt)
		}
		rows, err := e.db.Query(q+` ORDER BY step_id, attempt`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s StepSBOM
			var truncated, pruned int
			var lic, detail string
			if err := rows.Scan(&s.RunID, &s.Step, &s.Attempt, &s.Components, &s.Stored, &truncated, &pruned, &lic, &detail); err != nil {
				return err
			}
			s.Truncated, s.Pruned = truncated != 0, pruned != 0
			_ = json.Unmarshal([]byte(lic), &s.Licences)
			var d struct {
				Files    []SBOMFile `json:"files"`
				Notes    []string   `json:"notes"`
				Artifact string     `json:"artifact"`
			}
			if json.Unmarshal([]byte(detail), &d) == nil {
				s.Files, s.Notes, s.Artifact = d.Files, d.Notes, d.Artifact
			}
			if s.Files == nil {
				s.Files = []SBOMFile{}
			}
			if s.Licences == nil {
				s.Licences = map[string]int{}
			}
			latest[s.Step] = s
		}
		return rows.Err()
	}
	if err := load(st.ID, "", 0); err != nil {
		return nil, err
	}
	for _, id := range st.Order {
		if s := st.Steps[id]; s.ReusedFrom != "" {
			delete(latest, id)
			if err := load(s.ReusedFrom, id, s.ReusedAttempt); err != nil {
				return nil, err
			}
		}
	}
	for _, s := range latest {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b StepSBOM) int { return position[a.Step] - position[b.Step] })
	return out, nil
}

// ProjectSecurity is a project's findings over the last runs of a ref.
type ProjectSecurity struct {
	Ref  string   `json:"ref"`
	Refs []string `json:"refs"` // refs with findings, latest first
	// Runs are the last runs of the ref, oldest first, with their counts
	// (nil for a run without findings).
	Runs []SecurityRun `json:"runs"`
	// Latest is the newest run of the ref with findings; Open are its
	// findings, most severe first, each with the oldest run that had it.
	Latest   *RunLink       `json:"latest,omitempty"`
	Counts   *FindingCounts `json:"counts,omitempty"`
	Statuses StatusCounts   `json:"statuses"`
	Open     []FindingView  `json:"open"`
	// OpenMatched is how many findings are open in all; Open holds at most
	// maxOpenListed.
	OpenMatched int `json:"open_matched"`
}

// SecurityRun is one run in a project's security trend.
type SecurityRun struct {
	ID        string         `json:"id"`
	Number    int            `json:"number"`
	SHA       string         `json:"sha"`
	Status    string         `json:"status"`
	CreatedAt int64          `json:"created_at"`
	Findings  *FindingCounts `json:"findings,omitempty"`
}

const maxOpenListed = 500

// ProjectSecurity returns a project's findings over its last runs of a ref
// (the ref with the latest findings when ref is empty).
func (e *Engine) ProjectSecurity(project, ref string, window int) (*ProjectSecurity, error) {
	if window <= 0 {
		window = 30
	}
	window = min(window, 100)
	out := &ProjectSecurity{Ref: ref, Refs: []string{}, Runs: []SecurityRun{}, Open: []FindingView{}}
	if !e.knows(schemaArtifacts) {
		return out, nil
	}
	refs, err := e.idsOf(`SELECT r.ref FROM finding_reports fr JOIN runs r ON r.id = fr.run_id WHERE fr.project = ?
		GROUP BY r.ref ORDER BY MAX(fr.run_number) DESC LIMIT 20`, project)
	if err != nil {
		return nil, err
	}
	out.Refs = append(out.Refs, refs...)
	if out.Ref == "" {
		if len(refs) == 0 {
			return out, nil
		}
		out.Ref = refs[0]
	}
	rows, err := e.db.Query(`SELECT id, number, sha, status, created_at FROM runs WHERE project = ? AND ref = ? ORDER BY number DESC LIMIT ?`,
		project, out.Ref, window)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var r SecurityRun
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
	ef, _, err := e.effectiveFindings(ids)
	if err != nil {
		return nil, err
	}
	var latest string
	for i := range out.Runs {
		out.Runs[i].Findings = findingTotals(ef[out.Runs[i].ID])
		if latest == "" && out.Runs[i].Findings != nil {
			latest = out.Runs[i].ID
			out.Latest = &RunLink{ID: out.Runs[i].ID, Number: out.Runs[i].Number}
			out.Counts = out.Runs[i].Findings
		}
	}
	slices.Reverse(out.Runs)
	if latest == "" {
		return out, nil
	}
	st, err := e.runState(latest)
	if err != nil {
		return nil, err
	}
	open, err := e.loadFindings(findingTriples(ef[latest]), "")
	if err != nil {
		return nil, err
	}
	_, prev, err := e.previousWithFindings(st)
	if err != nil {
		return nil, err
	}
	fixed, err := e.compare(open, findingTools(ef[latest]), prev)
	if err != nil {
		return nil, err
	}
	for _, f := range open {
		switch f.Status {
		case FindingNew:
			out.Statuses.New++
		case FindingExisting:
			out.Statuses.Existing++
		}
	}
	out.Statuses.Fixed = len(fixed)
	// When each open finding was first seen on this ref, as far as the
	// findings still kept go back.
	first := map[string]RunLink{}
	fps := make([]string, 0, len(open))
	for _, f := range open {
		fps = append(fps, f.Fingerprint)
	}
	fps = slices.Compact(slices.Sorted(slices.Values(fps)))
	err = inChunks(fps, func(marks string, args []any) error {
		rows, err := e.db.Query(`SELECT f.fingerprint, f.run_number, f.run_id FROM findings f JOIN runs r ON r.id = f.run_id
			WHERE f.project = ? AND r.ref = ? AND f.fingerprint IN (`+marks+`)
			AND f.run_number = (SELECT MIN(g.run_number) FROM findings g JOIN runs s ON s.id = g.run_id
				WHERE g.project = f.project AND s.ref = r.ref AND g.fingerprint = f.fingerprint)`,
			append([]any{project, out.Ref}, args...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var fp, id string
			var n int
			if err := rows.Scan(&fp, &n, &id); err != nil {
				return err
			}
			if cur, ok := first[fp]; !ok || n < cur.Number {
				first[fp] = RunLink{ID: id, Number: n}
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for i := range open {
		if l, ok := first[open[i].Fingerprint]; ok {
			l := l
			open[i].FirstSeen = &l
		}
	}
	slices.SortStableFunc(open, func(a, b FindingView) int {
		return cmp.Or(cmp.Compare(severityRank[a.Severity], severityRank[b.Severity]), cmp.Compare(firstNumber(a), firstNumber(b)),
			strings.Compare(a.File, b.File), cmp.Compare(a.StartLine, b.StartLine))
	})
	out.OpenMatched = len(open)
	if len(open) > maxOpenListed {
		open = open[:maxOpenListed]
	}
	if err := e.attachRules(open); err != nil {
		return nil, err
	}
	out.Open = append(out.Open, open...)
	return out, nil
}

func firstNumber(f FindingView) int {
	if f.FirstSeen == nil {
		return int(^uint(0) >> 1)
	}
	return f.FirstSeen.Number
}

// ComponentHit is a component found in a run's SBOM.
type ComponentHit struct {
	Project   string `json:"project"`
	RunID     string `json:"run_id"`
	RunNumber int    `json:"run_number"`
	Ref       string `json:"ref"`
	Step      string `json:"step"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Type      string `json:"type,omitempty"`
	PURL      string `json:"purl,omitempty"`
	Licences  string `json:"licences,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// ComponentSearch is the result of a search for a component.
type ComponentSearch struct {
	Hits []ComponentHit `json:"hits"`
	// Projects are the projects with a hit, and the newest run of each
	// that has it.
	Projects []ComponentHit `json:"projects"`
	// More says there were more hits than listed.
	More bool `json:"more,omitempty"`
}

// SearchComponents finds the runs whose SBOMs list a component, by name
// (exactly, ignoring case) and, if given, version: the newest first, at
// most limit (default 100, at most 500). onlyProjects, if not nil, limits
// the search to those projects.
func (e *Engine) SearchComponents(name, version string, onlyProjects []string, limit int) (*ComponentSearch, error) {
	out := &ComponentSearch{Hits: []ComponentHit{}, Projects: []ComponentHit{}}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 300 || len(version) > 200 {
		return nil, fmt.Errorf("%w: give a component name of at most 300 bytes, and a version of at most 200", ErrInvalid)
	}
	if !e.knows(schemaArtifacts) || (onlyProjects != nil && len(onlyProjects) == 0) {
		return out, nil
	}
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	q := `SELECT c.project, c.run_id, c.run_number, r.ref, c.step_id, c.name, c.version, c.type, c.purl, c.licences, r.created_at
		FROM sbom_components c JOIN runs r ON r.id = c.run_id WHERE c.name_key = ?`
	args := []any{strings.ToLower(name)}
	if version = strings.TrimSpace(version); version != "" {
		q += ` AND c.version = ?`
		args = append(args, version)
	}
	if onlyProjects != nil {
		q += ` AND c.project IN (` + strings.TrimSuffix(strings.Repeat("?,", len(onlyProjects)), ",") + `)`
		for _, p := range onlyProjects {
			args = append(args, p)
		}
	}
	q += ` ORDER BY r.created_at DESC, c.id LIMIT ?`
	args = append(args, limit*4+1)
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	projects := map[string]bool{}
	n := 0
	for rows.Next() {
		n++
		var h ComponentHit
		if err := rows.Scan(&h.Project, &h.RunID, &h.RunNumber, &h.Ref, &h.Step, &h.Name, &h.Version, &h.Type, &h.PURL, &h.Licences, &h.CreatedAt); err != nil {
			return nil, err
		}
		// One hit per run, name and version: retries and several SBOM
		// files of one run list the same component again.
		k := h.RunID + "\x00" + h.Name + "\x00" + h.Version
		if seen[k] {
			continue
		}
		seen[k] = true
		if len(out.Hits) >= limit {
			out.More = true
			break
		}
		out.Hits = append(out.Hits, h)
		if !projects[h.Project] {
			projects[h.Project] = true
			out.Projects = append(out.Projects, h)
		}
	}
	if n > limit*4 {
		out.More = true
	}
	return out, rows.Err()
}

// severities lists the severities findings are sorted into, for checks.
var severities = func() []string {
	out := make([]string, 0, len(findings.Severities))
	for _, s := range findings.Severities {
		out = append(out, string(s))
	}
	return out
}()

// ValidSeverity reports whether s is a severity findings have.
func ValidSeverity(s string) bool { return slices.Contains(severities, s) }

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// applyDecisions marks the findings a person has decided about and counts what still counts: accepted findings stop
// counting as high (or critical) on the run, as they do on the project.
func (e *Engine) applyDecisions(project string, out *RunFindings, all []FindingView) error {
	if !e.knows(schemaStandings) || out.Counts == nil || len(all) == 0 {
		return nil
	}
	ds, err := activeDecisions(e.db, project, e.now().UnixMilli())
	if err != nil || len(ds) == 0 {
		return err
	}
	open := *out.Counts
	for i := range all {
		d, ok := ds[all[i].track]
		if !ok || all[i].track == "" {
			continue
		}
		all[i].Decision = &DecisionView{Status: d.Status, Reason: d.Reason, By: d.By, At: d.At.UnixMilli(), ExpiresAt: d.Expires.UnixMilli(), Active: true}
		out.Accepted++
		open.Total--
		switch all[i].Severity {
		case "critical":
			open.Critical--
		case "high":
			open.High--
		case "medium":
			open.Medium--
		case "low":
			open.Low--
		default:
			open.Unknown--
		}
	}
	if out.Accepted > 0 {
		out.Open = &open
	}
	return nil
}

// countStatuses counts the findings that are new and those already there, with the number that were fixed.
func countStatuses(all []FindingView, fixed int) StatusCounts {
	c := StatusCounts{Fixed: fixed}
	for _, f := range all {
		switch f.Status {
		case FindingNew:
			c.New++
		case FindingExisting:
			c.Existing++
		}
	}
	return c
}

// filterFindings keeps the findings that match the query's severities, tool, step, status and search text.
func filterFindings(pool []FindingView, q FindingQuery) []FindingView {
	search := strings.ToLower(strings.TrimSpace(q.Search))
	var matched []FindingView
	for _, f := range pool {
		switch {
		case len(q.Severities) > 0 && !slices.Contains(q.Severities, f.Severity):
		case q.Tool != "" && f.Tool != q.Tool:
		case q.Step != "" && f.Step != q.Step:
		case (q.Status == FindingNew || q.Status == FindingExisting) && f.Status != q.Status:
		case search != "" && !strings.Contains(strings.ToLower(f.RuleID+"\x00"+f.Message+"\x00"+f.File+"\x00"+f.Tool), search):
		default:
			matched = append(matched, f)
		}
	}
	return matched
}
