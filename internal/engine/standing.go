package engine

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/standards"
)

// Standing orders in the engine: the findings of the Inspection's steps are
// put in the product's one model as they are kept (enrichFindings), the gate
// step is settled by the server from them (startGate), and what the default
// branch has is remembered as the backlog every other change is measured
// against (the tracked findings).

// checkOfStep is the check a step belongs to: the one the server added it
// for, or, for a step a team wrote under the same name, the name's.
func checkOfStep(def *pipeline.Step) string {
	if def == nil {
		return ""
	}
	if def.Added != nil && def.Added.Check != "" {
		return def.Added.Check
	}
	if id, ok := strings.CutPrefix(def.ID, "inspect-"); ok {
		if _, known := standards.CheckByID(id); known {
			return id
		}
	}
	return ""
}

// enrichFindings puts an attempt's findings in the product's model: kind,
// package, fixed version, the fingerprint that survives a moved line.
func (c *cmd) enrichFindings(step string, list []storedFinding, rules []findings.Rule) {
	if !c.e.knows(schemaStandings) {
		return
	}
	check := checkOfStep(c.st.Pipeline.Step(step))
	byRule := make(map[string]findings.Rule, len(rules))
	for _, r := range rules {
		byRule[r.Tool+"\x00"+r.ID] = r
	}
	seen := map[string]int{}
	for i := range list {
		list[i].std = standards.NormalizeOne(check, list[i].Finding, byRule[list[i].Tool+"\x00"+list[i].RuleID], seen)
		// What is shown is the path in the repository, not where the tool saw it in its container.
		list[i].Finding.File = list[i].std.File
	}
}

// gateEvidence is what the verdict is made from, read from the database.
type gateEvidence struct {
	findings  []standards.Finding
	ran       map[string]bool
	baseline  map[string]bool
	hasBase   bool
	decisions map[string]standards.Decision
	truncated bool
}

// advanceGate moves the Inspection's verdict step: it waits for the checks,
// is skipped if one of them ended in a way that stops what needs it, and is
// settled when they are done.
func (c *cmd) advanceGate(id string, def *pipeline.Step) bool {
	ready, blocked := c.needsState(def)
	switch {
	case blocked != "":
		c.emit(StepSkipped, Payload{Step: id, SkipKind: "upstream", Reason: "not run because " + blocked})
		return true
	case !ready:
		return false
	}
	before := len(c.events)
	c.startGate(id, def)
	return len(c.events) > before
}

// startGate settles the Inspection's verdict step: it reads what the checks
// of this run reported, measures it against the default branch, records the
// verdict, and finishes the step as succeeded or failed with the sentence
// that explains it. On the default branch it also records the backlog.
func (c *cmd) startGate(id string, def *pipeline.Step) {
	var pol standards.Policy
	if def.Gate == nil || json.Unmarshal(def.Gate.Policy, &pol) != nil {
		c.failStep(id, "the Standing orders kept with this run could not be read, so there is no verdict")
		return
	}
	ev, err := c.gateEvidence(def)
	if err != nil {
		c.dbFail(err)
		return
	}
	now := time.UnixMilli(c.now)
	v := standards.Judge(standards.JudgeInput{Policy: pol, DefaultBranch: pol.DefaultBranchRun, Findings: ev.findings, Ran: ev.ran,
		Baseline: ev.baseline, HasBaseline: ev.hasBase, Decisions: ev.decisions, Now: now})
	if err := c.recordVerdict(pol, v, ev, now); err != nil {
		c.dbFail(err)
		return
	}
	outcome := StepSucceeded
	if v.Blocked {
		outcome = StepFailed
	}
	c.emit(StepFinished, Payload{Step: id, Outcome: string(outcome), Reason: v.Summary})
}

func (c *cmd) gateEvidence(def *pipeline.Step) (*gateEvidence, error) {
	st := c.st
	ev := &gateEvidence{ran: map[string]bool{}, baseline: map[string]bool{}, decisions: map[string]standards.Decision{}}
	var ts []triple
	seenFP := map[string]bool{}
	for _, need := range def.Needs {
		nd, s := st.Pipeline.Step(need), st.Steps[need]
		if nd == nil || s == nil {
			continue
		}
		check := checkOfStep(nd)
		if s.Status == StepSucceeded {
			ev.ran[check] = true
		}
		if s.Attempt > 0 {
			// What the step that has just finished kept is not in the table
			// yet (it is written when this command ends): read it from memory.
			if c.schema12 != nil {
				if k := c.schema12.findings[stepAttempt{need, s.Attempt}]; k != nil {
					for _, f := range k.list {
						if key := f.std.Check + "\x00" + f.std.Fingerprint; !seenFP[key] {
							seenFP[key] = true
							ev.findings = append(ev.findings, f.std)
						}
					}
					ev.ran[check] = true
					continue
				}
			}
			ts = append(ts, triple{st.ID, need, s.Attempt})
			var n int
			if err := c.tx.QueryRow(`SELECT count(*) FROM finding_reports WHERE run_id = ? AND step_id = ? AND attempt = ?`, st.ID, need, s.Attempt).Scan(&n); err != nil {
				return nil, err
			}
			var m int
			if err := c.tx.QueryRow(`SELECT count(*) FROM sbom_reports WHERE run_id = ? AND step_id = ? AND attempt = ?`, st.ID, need, s.Attempt).Scan(&m); err != nil {
				return nil, err
			}
			if n+m > 0 {
				ev.ran[check] = true
			}
		}
	}
	for len(ts) > 0 {
		n := min(len(ts), 20)
		cond, args := findingTripleFilter(ts[:n])
		ts = ts[n:]
		rows, err := c.tx.Query(`SELECT check_id, tool, rule_id, severity, kind, message, file, start_line, package, version, fixed_in, advisories,
				reachable, fix, track FROM findings f WHERE `+cond+` ORDER BY id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var f standards.Finding
			var sev, kind, adv string
			if err := rows.Scan(&f.Check, &f.Tool, &f.Rule, &sev, &kind, &f.Title, &f.File, &f.Line, &f.Package, &f.Version, &f.FixedIn, &adv,
				&f.Reachable, &f.Fix, &f.Fingerprint); err != nil {
				rows.Close()
				return nil, err
			}
			f.Severity, f.Kind = findings.Severity(sev), standards.Kind(kind)
			if adv != "" {
				f.Advisories = strings.Split(adv, ",")
			}
			if k := f.Check + "\x00" + f.Fingerprint; !seenFP[k] {
				seenFP[k] = true
				ev.findings = append(ev.findings, f)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var kept, total int
	if err := c.tx.QueryRow(`SELECT COALESCE(SUM(stored), 0), COALESCE(SUM(total), 0) FROM finding_reports WHERE run_id = ?`, st.ID).Scan(&kept, &total); err != nil {
		return nil, err
	}
	ev.truncated = total > kept
	var n int
	if err := c.tx.QueryRow(`SELECT count(*) FROM standing_verdicts WHERE project = ? AND default_branch = 1`, st.Project).Scan(&n); err != nil {
		return nil, err
	}
	ev.hasBase = n > 0
	rows, err := c.tx.Query(`SELECT track FROM standing_tracked WHERE project = ? AND status = 'open'`, st.Project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		ev.baseline[t] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ds, err := activeDecisions(c.tx, st.Project, c.now)
	if err != nil {
		return nil, err
	}
	ev.decisions = ds
	return ev, nil
}

// verdictDetail is what is kept of a verdict beyond its counts.
type verdictDetail struct {
	DefaultBranch bool             `json:"default_branch"`
	Policy        standards.Policy `json:"policy"`
	Counts        map[string]int   `json:"counts"`
	New           []string         `json:"new,omitempty"` // fingerprints introduced by the change, at most maxVerdictNew
	Gating        []string         `json:"gating,omitempty"`
	Incomplete    []string         `json:"incomplete,omitempty"`
	NoBaseline    bool             `json:"no_baseline,omitempty"`
	Truncated     bool             `json:"truncated,omitempty"`
}

const maxVerdictNew = 5000

func (c *cmd) recordVerdict(pol standards.Policy, v standards.Verdict, ev *gateEvidence, now time.Time) error {
	d := verdictDetail{DefaultBranch: pol.DefaultBranchRun, Policy: pol, Counts: v.Counts, Incomplete: v.Incomplete, NoBaseline: v.NoBaseline, Truncated: ev.truncated}
	for _, g := range v.Gating {
		if len(d.Gating) < 200 {
			d.Gating = append(d.Gating, g.Fingerprint)
		}
	}
	if !pol.DefaultBranchRun && !v.NoBaseline {
		newSet := map[string]bool{}
		for _, f := range ev.findings {
			if !ev.baseline[f.Fingerprint] && len(d.New) < maxVerdictNew && !newSet[f.Fingerprint] {
				newSet[f.Fingerprint] = true
				d.New = append(d.New, f.Fingerprint)
			}
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	st := c.st
	if _, err := c.tx.Exec(`INSERT INTO standing_verdicts(run_id, project, run_number, at, blocked, default_branch, summary, new_count, existing, accepted, detail)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET at = excluded.at, blocked = excluded.blocked, summary = excluded.summary, default_branch = excluded.default_branch,
		new_count = excluded.new_count, existing = excluded.existing, accepted = excluded.accepted, detail = excluded.detail`,
		st.ID, st.Project, st.Number, c.now, boolInt(v.Blocked), boolInt(pol.DefaultBranchRun), v.Summary, v.Counts[standards.StandingNew], v.Counts[standards.StandingExisting]+v.Counts[standards.StandingUnscored],
		v.Counts[standards.StandingAccepted]+v.Counts[standards.StandingFalse], string(raw)); err != nil {
		return err
	}
	if !pol.DefaultBranchRun {
		return nil
	}
	return c.recordBacklog(ev, now)
}

// recordBacklog is what a run on the default branch leaves: every finding
// it saw is open (new ones are recorded with this run as the first), and a
// finding of a check that ran this time and is not seen any more is fixed.
func (c *cmd) recordBacklog(ev *gateEvidence, now time.Time) error {
	st := c.st
	seen := map[string]bool{}
	var uniq []standards.Finding
	for _, f := range ev.findings {
		if f.Fingerprint != "" && !seen[f.Fingerprint] {
			seen[f.Fingerprint] = true
			uniq = append(uniq, f)
		}
	}
	for len(uniq) > 0 {
		n := min(len(uniq), 100)
		var q strings.Builder
		q.WriteString(`INSERT INTO standing_tracked(project, track, check_id, tool, rule_id, severity, kind, title, file, start_line, package, version,
			fixed_in, advisories, reachable, fix, first_run, first_at, last_run, last_at, status) VALUES `)
		args := make([]any, 0, n*21)
		for i, f := range uniq[:n] {
			if i > 0 {
				q.WriteString(",")
			}
			q.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'open')")
			args = append(args, st.Project, f.Fingerprint, f.Check, f.Tool, f.Rule, string(f.Severity), string(f.Kind), clip(f.Title, 1000), f.File, f.Line, f.Package,
				f.Version, f.FixedIn, strings.Join(f.Advisories, ","), f.Reachable, clip(f.Fix, 500), st.Number, c.now, st.Number, c.now)
		}
		q.WriteString(` ON CONFLICT(project, track) DO UPDATE SET check_id = excluded.check_id, severity = excluded.severity, title = excluded.title,
			file = excluded.file, start_line = excluded.start_line, package = excluded.package, version = excluded.version, fixed_in = excluded.fixed_in,
			advisories = excluded.advisories, reachable = excluded.reachable, fix = excluded.fix, last_run = excluded.last_run, last_at = excluded.last_at,
			status = 'open', fixed_run = 0, fixed_at = 0`)
		if _, err := c.tx.Exec(q.String(), args...); err != nil {
			return err
		}
		uniq = uniq[n:]
	}
	for check, ran := range ev.ran {
		if !ran || check == "" || ev.truncated {
			continue // a check that did not run, or whose findings were cut short, proves nothing was fixed
		}
		if _, err := c.tx.Exec(`UPDATE standing_tracked SET status = 'fixed', fixed_run = ?, fixed_at = ? WHERE project = ? AND check_id = ? AND status = 'open' AND last_run < ?`,
			st.Number, c.now, st.Project, check, st.Number); err != nil {
			return err
		}
	}
	return c.recordHistory(now)
}

func (c *cmd) recordHistory(now time.Time) error {
	var total, crit, high, med, low int
	rows, err := c.tx.Query(`SELECT severity, count(*) FROM standing_tracked WHERE project = ? AND status = 'open' GROUP BY severity`, c.st.Project)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sev string
		var n int
		if err := rows.Scan(&sev, &n); err != nil {
			rows.Close()
			return err
		}
		total += n
		switch sev {
		case "critical":
			crit = n
		case "high":
			high = n
		case "medium":
			med = n
		case "low":
			low = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = c.tx.Exec(`INSERT INTO standing_history(project, day, total, critical, high, medium, low) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(project, day) DO UPDATE SET total = excluded.total, critical = excluded.critical, high = excluded.high, medium = excluded.medium, low = excluded.low`,
		c.st.Project, now.UTC().Format("2006-01-02"), total, crit, high, med, low)
	return err
}

// activeDecisions are the decisions about a project's findings that still
// count: not reopened, not past their end date. The newest of each wins.
func activeDecisions(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, project string, nowMS int64) (map[string]standards.Decision, error) {
	rows, err := q.Query(`SELECT track, status, reason, decided_by, decided_at, expires_at FROM standing_decisions
		WHERE project = ? AND reopened_at = 0 AND expires_at > ? ORDER BY id`, project, nowMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]standards.Decision{}
	for rows.Next() {
		var d standards.Decision
		var at, exp int64
		if err := rows.Scan(&d.Fingerprint, &d.Status, &d.Reason, &d.By, &at, &exp); err != nil {
			return nil, err
		}
		d.At, d.Expires = time.UnixMilli(at), time.UnixMilli(exp)
		out[d.Fingerprint] = d
	}
	return out, rows.Err()
}
