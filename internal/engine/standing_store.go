package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/standards"
)

// What the interfaces read and change about Standing orders: the settings of
// each level, people's decisions about findings, the backlog the default
// branch has, and the verdict of a run. Who may do what is decided by the
// callers (the API, the CLI and the MCP tools all go through the same
// routes); this file only keeps the data and refuses what is invalid.

// ---- settings ----

// StandingSettingsDoc is the settings of one level, as stored.
type StandingSettingsDoc struct {
	Level     string          `json:"level"`
	Scope     string          `json:"scope"`
	Doc       json.RawMessage `json:"settings"`
	Version   int             `json:"version"`
	UpdatedAt int64           `json:"updated_at"`
	UpdatedBy string          `json:"updated_by,omitempty"`
}

// StandingSettings returns the settings of a level and scope, or nil if
// there are none yet.
func (e *Engine) StandingSettings(level, scope string) (*StandingSettingsDoc, error) {
	if !e.knows(schemaStandings) {
		return nil, nil
	}
	var d StandingSettingsDoc
	var doc string
	err := e.db.QueryRow(`SELECT level, scope, doc, version, updated_at, updated_by FROM standing_settings WHERE level = ? AND scope = ?`, level, scope).
		Scan(&d.Level, &d.Scope, &doc, &d.Version, &d.UpdatedAt, &d.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	d.Doc = json.RawMessage(doc)
	return &d, err
}

// AllStandingSettings lists every level's settings, for the pages that show
// where each repository's values come from.
func (e *Engine) AllStandingSettings() ([]StandingSettingsDoc, error) {
	if !e.knows(schemaStandings) {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT level, scope, doc, version, updated_at, updated_by FROM standing_settings ORDER BY level, scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StandingSettingsDoc
	for rows.Next() {
		var d StandingSettingsDoc
		var doc string
		if err := rows.Scan(&d.Level, &d.Scope, &doc, &d.Version, &d.UpdatedAt, &d.UpdatedBy); err != nil {
			return nil, err
		}
		d.Doc = json.RawMessage(doc)
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetStandingSettings replaces a level's settings. expect is the version the
// caller read (0: there were none); a different version is a conflict, so
// two people editing at once never overwrite each other unseen.
func (e *Engine) SetStandingSettings(level, scope string, doc json.RawMessage, expect int, by string) (before, after *StandingSettingsDoc, err error) {
	if !e.knows(schemaStandings) {
		return nil, nil, fmt.Errorf("%w: this server does not know Standing orders (schema %d)", ErrConflict, schemaStandings)
	}
	if len(doc) > 256<<10 {
		return nil, nil, fmt.Errorf("%w: the settings are larger than 256 KiB", ErrInvalid)
	}
	err = e.runWrite(func(tx *txx) error {
		var cur int
		var old string
		switch err := tx.QueryRow(`SELECT version, doc FROM standing_settings WHERE level = ? AND scope = ?`, level, scope).Scan(&cur, &old); {
		case errors.Is(err, sql.ErrNoRows):
			cur = 0
		case err != nil:
			return err
		default:
			before = &StandingSettingsDoc{Level: level, Scope: scope, Doc: json.RawMessage(old), Version: cur}
		}
		if cur != expect {
			return fmt.Errorf("%w: these settings changed since you read them (now version %d, you had %d); read them again and redo the change", ErrConflict, cur, expect)
		}
		now := e.now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO standing_settings(level, scope, doc, version, updated_at, updated_by) VALUES(?,?,?,?,?,?)
			ON CONFLICT(level, scope) DO UPDATE SET doc = excluded.doc, version = excluded.version, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			level, scope, string(doc), cur+1, now, by); err != nil {
			return err
		}
		after = &StandingSettingsDoc{Level: level, Scope: scope, Doc: doc, Version: cur + 1, UpdatedAt: now, UpdatedBy: by}
		return nil
	})
	return before, after, err
}

// ---- decisions ----

// DecisionView is a person's decision about a finding.
type DecisionView struct {
	Status    string `json:"status"` // accepted-risk or false-positive
	Reason    string `json:"reason"`
	By        string `json:"by"`
	At        int64  `json:"at"`
	ExpiresAt int64  `json:"expires_at"`
	// Active: it has not ended and has not been reopened.
	Active bool `json:"active"`
}

const (
	minDecisionReason = 10
	maxDecisionReason = 500
)

// DecideFinding accepts a risk or marks a false positive. A reason and an
// end date within a year are required: a decision that cannot expire is
// forgotten, and one without a reason cannot be reviewed. The decision
// counts for the project's finding with this fingerprint wherever it
// appears.
func (e *Engine) DecideFinding(project, fingerprint, status, reason, by string, expires time.Time) (*DecisionView, error) {
	if !e.knows(schemaStandings) {
		return nil, fmt.Errorf("%w: this server does not know Standing orders (schema %d)", ErrConflict, schemaStandings)
	}
	reason = strings.TrimSpace(reason)
	now := e.now()
	switch {
	case project == "" || fingerprint == "":
		return nil, fmt.Errorf("%w: say which project and which finding", ErrInvalid)
	case status != standards.StatusAccepted && status != standards.StatusFalsePositive:
		return nil, fmt.Errorf("%w: the decision is accepted-risk or false-positive", ErrInvalid)
	case len(reason) < minDecisionReason:
		return nil, fmt.Errorf("%w: a reason is needed, at least %d characters: why is this safe to live with, or why is it not a problem?", ErrInvalid, minDecisionReason)
	case len(reason) > maxDecisionReason:
		return nil, fmt.Errorf("%w: the reason is longer than %d characters", ErrInvalid, maxDecisionReason)
	case !expires.After(now):
		return nil, fmt.Errorf("%w: the end date must be in the future", ErrInvalid)
	case expires.After(now.AddDate(0, 0, standards.MaxExemptionDays)):
		return nil, fmt.Errorf("%w: the end date is more than %d days away; choose a date within a year and renew it when it comes", ErrInvalid, standards.MaxExemptionDays)
	case strings.TrimSpace(by) == "":
		return nil, fmt.Errorf("%w: a decision records who made it", ErrInvalid)
	}
	d := &DecisionView{Status: status, Reason: reason, By: by, At: now.UnixMilli(), ExpiresAt: expires.UnixMilli(), Active: true}
	err := e.runWrite(func(tx *txx) error {
		// A new decision ends the one before it for the same finding.
		if _, err := tx.Exec(`UPDATE standing_decisions SET reopened_at = ?, reopened_by = ? WHERE project = ? AND track = ? AND reopened_at = 0`,
			d.At, by, project, fingerprint); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO standing_decisions(project, track, status, reason, decided_by, decided_at, expires_at) VALUES(?,?,?,?,?,?,?)`,
			project, fingerprint, status, reason, by, d.At, d.ExpiresAt)
		return err
	})
	return d, err
}

// DecideFindingsInPath records one decision for every open finding of the project whose file matches the glob
// (** crosses folders, * does not cross a slash), optionally only those of one check: the way to say "these are test
// vectors, not secrets" for a folder in one step. Each finding gets its own decision, with the same reason, person
// and end date, so each can be ended on its own. It returns how many findings were decided; none is an error, so a
// misspelt path is never taken for success.
func (e *Engine) DecideFindingsInPath(project, glob, check, status, reason, by string, expires time.Time) (int, error) {
	glob = strings.TrimSpace(glob)
	if glob == "" || strings.HasPrefix(glob, "/") || strings.Contains(glob, "..") {
		return 0, fmt.Errorf("%w: name a path inside the repository, such as docs/** or docs/assertion_vectors.json", ErrInvalid)
	}
	list, err := e.StandingFindings(project, StandingQuery{Status: "open", Check: check, Limit: 500})
	if err != nil {
		return 0, err
	}
	var fps []string
	for off := 0; ; off += 500 {
		page, err := e.StandingFindings(project, StandingQuery{Status: "open", Check: check, Limit: 500, Offset: off})
		if err != nil {
			return 0, err
		}
		for _, f := range page.Findings {
			if f.File != "" && standards.GlobMatch(glob, f.File) {
				fps = append(fps, f.Fingerprint)
			}
		}
		if off+500 >= list.Matched {
			break
		}
	}
	if len(fps) == 0 {
		return 0, fmt.Errorf("%w: no open finding of %s is in %s", ErrNotFound, project, glob)
	}
	for _, fp := range fps {
		if _, err := e.DecideFinding(project, fp, status, reason, by, expires); err != nil {
			return 0, err
		}
	}
	return len(fps), nil
}

// ReopenFinding ends the decision about a finding, so it counts again.
func (e *Engine) ReopenFinding(project, fingerprint, by string) error {
	if !e.knows(schemaStandings) {
		return fmt.Errorf("%w: this server does not know Standing orders (schema %d)", ErrConflict, schemaStandings)
	}
	var n int64
	err := e.runWrite(func(tx *txx) error {
		res, err := tx.Exec(`UPDATE standing_decisions SET reopened_at = ?, reopened_by = ? WHERE project = ? AND track = ? AND reopened_at = 0 AND expires_at > ?`,
			e.now().UnixMilli(), by, project, fingerprint, e.now().UnixMilli())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err == nil && n == 0 {
		return fmt.Errorf("%w: there is no decision in force for this finding", ErrNotFound)
	}
	return err
}

// ---- the backlog ----

// TrackedFinding is a finding the default branch has had, with the decision
// about it in force.
type TrackedFinding struct {
	Project     string        `json:"project,omitempty"`
	Fingerprint string        `json:"fingerprint"`
	Check       string        `json:"check,omitempty"`
	Tool        string        `json:"tool"`
	Rule        string        `json:"rule"`
	Severity    string        `json:"severity"`
	Kind        string        `json:"kind"`
	Title       string        `json:"title"`
	File        string        `json:"file,omitempty"`
	Line        int           `json:"line,omitempty"`
	Package     string        `json:"package,omitempty"`
	Version     string        `json:"version,omitempty"`
	FixedIn     string        `json:"fixed_in,omitempty"`
	Advisories  []string      `json:"advisories,omitempty"`
	Reachable   string        `json:"reachable,omitempty"`
	Fix         string        `json:"fix,omitempty"`
	FirstRun    int           `json:"first_run"`
	FirstSeen   int64         `json:"first_seen"`
	LastRun     int           `json:"last_run"`
	LastSeen    int64         `json:"last_seen"`
	Status      string        `json:"status"` // open, fixed, accepted, false-positive
	FixedAt     int64         `json:"fixed_at,omitempty"`
	Decision    *DecisionView `json:"decision,omitempty"`
}

// StandingQuery filters a project's backlog.
type StandingQuery struct {
	Status     string   // open (default), fixed, accepted, all
	Severities []string // any of critical, high, medium, low, unknown
	Check      string
	Search     string
	Oldest     bool // oldest first instead of most severe first
	Limit      int  // default 100, at most 500
	Offset     int
}

// StandingList is a page of the backlog with its totals.
type StandingList struct {
	Findings []TrackedFinding `json:"findings"`
	Matched  int              `json:"matched"`
	// Totals of what is open (accepted ones are counted apart).
	BySeverity map[string]int `json:"by_severity"`
	ByCheck    map[string]int `json:"by_check"`
	Open       int            `json:"open"`
	Accepted   int            `json:"accepted"`
	Fixed      int            `json:"fixed"`
}

// StandingFindings lists a project's backlog.
func (e *Engine) StandingFindings(project string, q StandingQuery) (*StandingList, error) {
	out := &StandingList{Findings: []TrackedFinding{}, BySeverity: map[string]int{}, ByCheck: map[string]int{}}
	if !e.knows(schemaStandings) {
		return out, nil
	}
	now := e.now().UnixMilli()
	decisions, err := activeDecisions(e.db, project, now)
	if err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT track, check_id, tool, rule_id, severity, kind, title, file, start_line, package, version, fixed_in, advisories, reachable, fix,
		first_run, first_at, last_run, last_at, status, fixed_at FROM standing_tracked WHERE project = ? ORDER BY first_at, track`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []TrackedFinding
	for rows.Next() {
		var f TrackedFinding
		var adv string
		if err := rows.Scan(&f.Fingerprint, &f.Check, &f.Tool, &f.Rule, &f.Severity, &f.Kind, &f.Title, &f.File, &f.Line, &f.Package, &f.Version, &f.FixedIn, &adv, &f.Reachable, &f.Fix,
			&f.FirstRun, &f.FirstSeen, &f.LastRun, &f.LastSeen, &f.Status, &f.FixedAt); err != nil {
			return nil, err
		}
		if adv != "" {
			f.Advisories = strings.Split(adv, ",")
		}
		if d, ok := decisions[f.Fingerprint]; ok {
			f.Decision = &DecisionView{Status: d.Status, Reason: d.Reason, By: d.By, At: d.At.UnixMilli(), ExpiresAt: d.Expires.UnixMilli(), Active: true}
			if f.Status == "open" {
				f.Status = d.Status
			}
		}
		all = append(all, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sevs := map[string]bool{}
	for _, s := range q.Severities {
		sevs[s] = true
	}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	want := q.Status
	if want == "" {
		want = "open"
	}
	var match []TrackedFinding
	for _, f := range all {
		switch f.Status {
		case "open":
			out.Open++
			out.BySeverity[f.Severity]++
			out.ByCheck[f.Check]++
		case "fixed":
			out.Fixed++
		default:
			out.Accepted++
		}
		if !q.keeps(f, want, sevs, search) {
			continue
		}
		match = append(match, f)
	}
	sort.SliceStable(match, func(i, j int) bool {
		if q.Oldest {
			return match[i].FirstSeen < match[j].FirstSeen
		}
		if ri, rj := severityRank[match[i].Severity], severityRank[match[j].Severity]; ri != rj {
			return ri < rj
		}
		return match[i].FirstSeen < match[j].FirstSeen
	})
	out.Matched = len(match)
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if q.Offset > len(match) {
		q.Offset = len(match)
	}
	match = match[q.Offset:]
	if len(match) > limit {
		match = match[:limit]
	}
	out.Findings = append(out.Findings, match...)
	return out, nil
}

// StandingSeries is the open findings of a project over time, one point a
// day, and the mean time to fix.
type StandingSeries struct {
	Project string          `json:"project"`
	Points  []StandingPoint `json:"points"`
	// MeanDaysToFix is over the findings fixed so far; FixedCount how many.
	MeanDaysToFix float64 `json:"mean_days_to_fix"`
	FixedCount    int     `json:"fixed_count"`
	// OldestOpen is the first-seen time of the oldest open finding, 0 if none.
	OldestOpen int64          `json:"oldest_open,omitempty"`
	Open       int            `json:"open"`
	BySeverity map[string]int `json:"by_severity"`
}

// StandingPoint is one day.
type StandingPoint struct {
	Day      string `json:"day"`
	Total    int    `json:"total"`
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	Medium   int    `json:"medium"`
	Low      int    `json:"low"`
}

// StandingHistory returns a project's series for the last days.
func (e *Engine) StandingHistory(project string, days int) (*StandingSeries, error) {
	s := &StandingSeries{Project: project, Points: []StandingPoint{}, BySeverity: map[string]int{}}
	if !e.knows(schemaStandings) {
		return s, nil
	}
	if days <= 0 || days > 365 {
		days = 90
	}
	since := e.now().AddDate(0, 0, -days).UTC().Format("2006-01-02")
	if err := queryEach(e.db, func(rows *sql.Rows) error {
		var p StandingPoint
		if err := rows.Scan(&p.Day, &p.Total, &p.Critical, &p.High, &p.Medium, &p.Low); err != nil {
			return err
		}
		s.Points = append(s.Points, p)
		return nil
	}, `SELECT day, total, critical, high, medium, low FROM standing_history WHERE project = ? AND day >= ? ORDER BY day`, project, since); err != nil {
		return nil, err
	}
	var sum sql.NullFloat64
	if err := e.db.QueryRow(`SELECT count(*), COALESCE(SUM(fixed_at - first_at), 0) FROM standing_tracked WHERE project = ? AND status = 'fixed' AND fixed_at >= first_at`, project).
		Scan(&s.FixedCount, &sum); err != nil {
		return nil, err
	}
	if s.FixedCount > 0 {
		s.MeanDaysToFix = float64(sum.Float64) / float64(s.FixedCount) / float64(24*time.Hour/time.Millisecond)
	}
	var oldest sql.NullInt64
	if err := e.db.QueryRow(`SELECT MIN(first_at) FROM standing_tracked WHERE project = ? AND status = 'open'`, project).Scan(&oldest); err != nil {
		return nil, err
	}
	s.OldestOpen = oldest.Int64
	srows, err := e.db.Query(`SELECT severity, count(*) FROM standing_tracked WHERE project = ? AND status = 'open' GROUP BY severity`, project)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var sev string
		var n int
		if err := srows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		s.BySeverity[sev] = n
		s.Open += n
	}
	return s, srows.Err()
}

// ---- a run's verdict ----

// InspectionCheck is one check of a run.
type InspectionCheck struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	Ran   bool   `json:"ran"`
	New   int    `json:"new"`
	Total int    `json:"total"`
}

// InspectionFinding is a finding of a run with what the gate made of it.
type InspectionFinding struct {
	Fingerprint string        `json:"fingerprint"`
	Check       string        `json:"check,omitempty"`
	Tool        string        `json:"tool"`
	Rule        string        `json:"rule"`
	Severity    string        `json:"severity"`
	Kind        string        `json:"kind"`
	Title       string        `json:"title"`
	File        string        `json:"file,omitempty"`
	Line        int           `json:"line,omitempty"`
	Package     string        `json:"package,omitempty"`
	Version     string        `json:"version,omitempty"`
	FixedIn     string        `json:"fixed_in,omitempty"`
	Advisories  []string      `json:"advisories,omitempty"`
	Reachable   string        `json:"reachable,omitempty"`
	Fix         string        `json:"fix,omitempty"`
	HelpURI     string        `json:"help_uri,omitempty"`
	Standing    string        `json:"standing"` // new, existing, accepted, false-positive, unscored
	Gating      bool          `json:"gating,omitempty"`
	Decision    *DecisionView `json:"decision,omitempty"`
	Step        string        `json:"step"`
}

// RunInspection is what the Inspection said about a run.
type RunInspection struct {
	Present        bool                `json:"present"` // the run has an Inspection
	Blocked        bool                `json:"blocked"`
	Summary        string              `json:"summary"`
	DefaultBranch  bool                `json:"default_branch"`
	NoBaseline     bool                `json:"no_baseline,omitempty"`
	Pending        bool                `json:"pending,omitempty"` // the verdict step has not finished
	ProfileVersion string              `json:"profile_version,omitempty"`
	FailOn         string              `json:"fail_on,omitempty"`
	Counts         map[string]int      `json:"counts"`
	Checks         []InspectionCheck   `json:"checks"`
	Notices        []string            `json:"notices,omitempty"`
	Incomplete     []string            `json:"incomplete,omitempty"`
	Findings       []InspectionFinding `json:"findings"`
	Matched        int                 `json:"matched"`
	Truncated      bool                `json:"truncated,omitempty"`
}

// InspectionQuery filters a run's findings.
type InspectionQuery struct {
	Standing      string // new, existing, accepted, ...; "" all
	Check         string
	Severities    []string
	Limit, Offset int
}

// RunInspection returns the Inspection of a run: the verdict, the checks and
// the findings with their standing.
func (e *Engine) RunInspection(runID string, q InspectionQuery) (*RunInspection, error) {
	out := &RunInspection{Counts: map[string]int{}, Checks: []InspectionCheck{}, Findings: []InspectionFinding{}}
	if !e.knows(schemaStandings) {
		return out, nil
	}
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	var gate *pipeline.Step
	for _, s := range st.Pipeline.Steps {
		if s.IsGate() {
			gate = s
		}
	}
	if gate == nil {
		return out, nil
	}
	det, err := e.loadVerdict(runID, gate, out)
	if err != nil {
		return nil, err
	}
	isNew := map[string]bool{}
	for _, f := range det.New {
		isNew[f] = true
	}
	gating := map[string]bool{}
	for _, f := range det.Gating {
		gating[f] = true
	}
	decisions, err := activeDecisions(e.db, st.Project, e.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	var ts []triple
	for _, need := range gate.Needs {
		if s := st.Steps[need]; s != nil && s.Attempt > 0 {
			ts = append(ts, triple{runID, need, s.Attempt})
		}
	}
	inspectionChecks(det, gate, st, out)
	idx := map[string]int{}
	for i, c := range out.Checks {
		idx[c.ID] = i
	}
	all, err := e.inspectionFindings(ts, decisions, out, isNew, gating, idx)
	if err != nil {
		return nil, err
	}
	if len(out.Counts) == 0 {
		for _, f := range all {
			out.Counts[f.Standing]++
		}
	}
	sevs := map[string]bool{}
	for _, s := range q.Severities {
		sevs[s] = true
	}
	standingRank := map[string]int{standards.StandingNew: 0, standards.StandingUnscored: 1, standards.StandingExisting: 2, standards.StandingAccepted: 3, standards.StandingFalse: 4}
	var match []InspectionFinding
	for _, f := range all {
		if (q.Standing != "" && f.Standing != q.Standing) || (q.Check != "" && f.Check != q.Check) || (len(sevs) > 0 && !sevs[f.Severity]) {
			continue
		}
		match = append(match, f)
	}
	sort.SliceStable(match, func(i, j int) bool {
		if a, b := standingRank[match[i].Standing], standingRank[match[j].Standing]; a != b {
			return a < b
		}
		return severityRank[match[i].Severity] < severityRank[match[j].Severity]
	})
	out.Matched = len(match)
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if q.Offset > len(match) {
		q.Offset = len(match)
	}
	match = match[q.Offset:]
	if len(match) > limit {
		match = match[:limit]
	}
	out.Findings = append(out.Findings, match...)
	return out, nil
}

// StandingsKnown says this server knows Standing orders (schema 45), so
// runs may carry the Inspection.
func (e *Engine) StandingsKnown() bool { return e.knows(schemaStandings) }

// inspectionFindings reads a run's findings for the steps of its checks and
// gives each its standing; it also counts them in the checks.
func (e *Engine) inspectionFindings(ts []triple, decisions map[string]standards.Decision, out *RunInspection, isNew, gating map[string]bool, idx map[string]int) ([]InspectionFinding, error) {
	var all []InspectionFinding
	seen := map[string]bool{}
	for len(ts) > 0 {
		n := min(len(ts), 20)
		cond, args := findingTripleFilter(ts[:n])
		ts = ts[n:]
		if err := queryEach(e.db, func(rows *sql.Rows) error {
			var f InspectionFinding
			var adv string
			if err := rows.Scan(&f.Step, &f.Check, &f.Tool, &f.Rule, &f.Severity, &f.Kind, &f.Title, &f.File, &f.Line, &f.Package, &f.Version, &f.FixedIn, &adv, &f.Reachable, &f.Fix, &f.Fingerprint, &f.HelpURI); err != nil {
				return err
			}
			if adv != "" {
				f.Advisories = strings.Split(adv, ",")
			}
			k := f.Step + "\x00" + f.Fingerprint
			if seen[k] {
				return nil // the same package and advisory reported twice is one finding
			}
			seen[k] = true
			standingOf(&f, decisions, out, isNew)
			f.Gating = gating[f.Fingerprint] && f.Standing == standards.StandingNew || gating[f.Fingerprint] && out.DefaultBranch
			if i, ok := idx[f.Check]; ok {
				out.Checks[i].Total++
				if f.Standing == standards.StandingNew {
					out.Checks[i].New++
				}
			}
			all = append(all, f)
			return nil
		}, `SELECT step_id, check_id, tool, rule_id, severity, kind, message, file, start_line, package, version, fixed_in, advisories,
				reachable, fix, track, COALESCE((SELECT help_uri FROM finding_rules r WHERE r.run_id = f.run_id AND r.step_id = f.step_id AND r.attempt = f.attempt AND r.tool = f.tool AND r.rule_id = f.rule_id), '')
			FROM findings f WHERE `+cond+` ORDER BY id`, args...); err != nil {
			return nil, err
		}
	}
	return all, nil
}

// standingOf says what a finding of a run is: accepted by a decision, or
// new, existing or not comparable.
func standingOf(f *InspectionFinding, decisions map[string]standards.Decision, out *RunInspection, isNew map[string]bool) {
	switch d, ok := decisions[f.Fingerprint]; {
	case ok:
		f.Standing = standards.StandingAccepted
		if d.Status == standards.StatusFalsePositive {
			f.Standing = standards.StandingFalse
		}
		f.Decision = &DecisionView{Status: d.Status, Reason: d.Reason, By: d.By, At: d.At.UnixMilli(), ExpiresAt: d.Expires.UnixMilli(), Active: true}
	case out.NoBaseline:
		f.Standing = standards.StandingUnscored
	case out.DefaultBranch:
		f.Standing = standards.StandingExisting
	case isNew[f.Fingerprint]:
		f.Standing = standards.StandingNew
	default:
		f.Standing = standards.StandingExisting
	}
}

// StandingInspected says the project's default branch has been inspected at
// least once, so a baseline exists to compare changes with.
func (e *Engine) StandingInspected(project string) bool {
	if !e.knows(schemaStandings) {
		return false
	}
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM standing_verdicts WHERE project = ? AND default_branch = 1`, project).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// keeps says whether a backlog finding matches the query.
func (q StandingQuery) keeps(f TrackedFinding, want string, sevs map[string]bool, search string) bool {
	switch {
	case want == "accepted" && f.Status != standards.StatusAccepted && f.Status != standards.StatusFalsePositive:
		return false
	case want != "all" && want != "accepted" && f.Status != want:
		return false
	case len(sevs) > 0 && !sevs[f.Severity]:
		return false
	case q.Check != "" && f.Check != q.Check:
		return false
	case search != "" && !strings.Contains(strings.ToLower(f.Title+" "+f.Rule+" "+f.File+" "+f.Package), search):
		return false
	}
	return true
}

// loadVerdict reads what the gate said about a run into out (or says it has
// not said anything yet) and returns the detail it kept.
func (e *Engine) loadVerdict(runID string, gate *pipeline.Step, out *RunInspection) (verdictDetail, error) {
	out.Present = true
	var raw string
	var blocked, def int
	err := e.db.QueryRow(`SELECT blocked, default_branch, summary, detail FROM standing_verdicts WHERE run_id = ?`, runID).Scan(&blocked, &def, &out.Summary, &raw)
	var det verdictDetail
	switch {
	case errors.Is(err, sql.ErrNoRows):
		out.Pending = true
		out.Summary = "The checks have not all finished, so there is no verdict yet."
		if gate.Gate != nil {
			_ = json.Unmarshal(gate.Gate.Policy, &det.Policy)
		}
	case err != nil:
		return det, err
	default:
		out.Blocked, out.DefaultBranch = blocked == 1, def == 1
		_ = json.Unmarshal([]byte(raw), &det)
		out.Counts, out.NoBaseline, out.Incomplete, out.Truncated = det.Counts, det.NoBaseline, det.Incomplete, det.Truncated
	}
	out.ProfileVersion, out.FailOn, out.Notices = det.Policy.ProfileVersion, string(det.Policy.FailOn), det.Policy.Notices
	if out.Counts == nil {
		out.Counts = map[string]int{}
	}
	return det, nil
}

// inspectionChecks lists the checks of a run and whether each one ran.
func inspectionChecks(det verdictDetail, gate *pipeline.Step, st *RunState, out *RunInspection) {
	for _, pc := range det.Policy.Checks {
		ran := false
		for _, need := range gate.Needs {
			if checkOfStep(st.Pipeline.Step(need)) == pc.ID {
				if s := st.Steps[need]; s != nil && s.Status.Terminal() && s.Status != StepSkippedS && s.Status != StepCanceledS {
					ran = true
				}
			}
		}
		out.Checks = append(out.Checks, InspectionCheck{ID: pc.ID, Title: pc.Title, State: string(pc.State), Ran: ran && !contains(out.Incomplete, pc.ID)})
	}
}
