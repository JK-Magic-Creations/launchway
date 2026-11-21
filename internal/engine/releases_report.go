package engine

import (
	"fmt"
	"sort"
	"time"

	rls "github.com/jk-magic-creations/launchway/internal/release"
)

// The four delivery measures are worked out from what the product already
// keeps: deployments (when a delivery reached an Airfield and how it ended)
// and releases (the commit time of what was released).
//
//   - Lead time: from the release's commit to its delivery reaching an Airfield.
//   - Frequency: successful deliveries a week.
//   - Change failure rate: deliveries that failed or were returned to base,
//     divided by all deliveries.
//   - Time to restore: from a failed delivery to the next successful one in
//     the same place (a return to base counts: it is a delivery).

// ReleaseLaunches reads deliveries as the measures need them.
func (e *Engine) ReleaseLaunches(project, airfield string, from, to time.Time) ([]rls.Launch, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	q := `SELECT id, run_id, project, service, environment, version, mode, verdict, opened_at, deployed_at, closed_at FROM deployments WHERE closed_at > 0 AND opened_at < ?`
	args := []any{to.UnixMilli()}
	if project != "" {
		q += ` AND project = ?`
		args = append(args, project)
	}
	if airfield != "" {
		q += ` AND environment = ?`
		args = append(args, airfield)
	}
	q += ` ORDER BY project, service, environment, opened_at`
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	type dep struct {
		id, run, project, service, env, version, mode, verdict string
		opened, deployed, closed                               int64
	}
	var deps []dep
	for rows.Next() {
		var d dep
		if err := rows.Scan(&d.id, &d.run, &d.project, &d.service, &d.env, &d.version, &d.mode, &d.verdict, &d.opened, &d.deployed, &d.closed); err != nil {
			rows.Close()
			return nil, err
		}
		deps = append(deps, d)
	}
	rows.Close()
	rels, err := e.ListReleases(ReleaseFilter{Project: project})
	if err != nil {
		return nil, err
	}
	byID := map[string]Release{}
	for _, r := range rels {
		byID[r.ID] = r
	}
	var out []rls.Launch
	for i, d := range deps {
		if d.mode == "rollback" || d.verdict == VerdictAbandoned {
			continue // a return to base is how a failure is restored, not a launch of its own
		}
		at := d.deployed
		if at == 0 {
			at = d.closed
		}
		l := rls.Launch{Project: d.project, Airfield: d.env, Version: d.version, At: time.UnixMilli(at)}
		l.Failed = d.verdict == VerdictFailed || d.verdict == VerdictRolledBack || d.verdict == VerdictRollbackFailed
		if id := e.releaseFor(d.project, d.version, d.run, rels); id != "" {
			if r := byID[id]; r.CommitTime > 0 {
				l.Merged = time.UnixMilli(r.CommitTime)
			}
		}
		if l.Failed {
			for _, n := range deps[i+1:] {
				if n.project != d.project || n.service != d.service || n.env != d.env {
					break
				}
				if n.verdict == VerdictDeployed {
					t := n.deployed
					if t == 0 {
						t = n.closed
					}
					l.Restored = time.UnixMilli(t)
					break
				}
			}
		}
		out = append(out, l)
	}
	return out, nil
}

// ReleaseReportRow is one release's journey.
type ReleaseReportRow struct {
	Release  Release          `json:"release"`
	Reached  []ReportAirfield `json:"reached"`
	Failures int              `json:"failures"`
	Returns  int              `json:"returns"`
}

// ReportAirfield is when a release reached an Airfield and the lead time to it.
type ReportAirfield struct {
	Airfield    string `json:"airfield"`
	At          int64  `json:"at"`
	LeadSeconds int64  `json:"lead_seconds,omitempty"`
	State       string `json:"state"`
	By          string `json:"by,omitempty"`
}

// ReleaseReport is what shipped and how delivery went over a period.
type ReleaseReport struct {
	Project  string         `json:"project,omitempty"`
	From     int64          `json:"from"`
	To       int64          `json:"to"`
	Measures rls.Measures   `json:"measures"`
	Series   []rls.Measures `json:"series"`
	// ByAirfield are the measures of each Airfield.
	ByAirfield map[string]rls.Measures `json:"by_airfield"`
	ByProject  map[string]rls.Measures `json:"by_project"`
	Shipped    []ReleaseReportRow      `json:"shipped"`
	// StepSeconds is the length of a point of the series.
	StepSeconds int64 `json:"step_seconds"`
}

// BuildReleaseReport works out the report of a period for a project (all if empty).
func (e *Engine) BuildReleaseReport(project string, from, to time.Time, visible func(string) bool) (*ReleaseReport, error) {
	if !to.After(from) || to.Sub(from) > 3*366*24*time.Hour {
		return nil, fmt.Errorf("%w: the period must end after it starts and be at most three years", ErrInvalid)
	}
	ls, err := e.ReleaseLaunches(project, "", from.Add(-time.Hour), to)
	if err != nil {
		return nil, err
	}
	if visible != nil {
		kept := ls[:0]
		for _, l := range ls {
			if visible(l.Project) {
				kept = append(kept, l)
			}
		}
		ls = kept
	}
	rep := &ReleaseReport{Project: project, From: from.UnixMilli(), To: to.UnixMilli(), ByAirfield: map[string]rls.Measures{}, ByProject: map[string]rls.Measures{}}
	rep.Measures = rls.Compute(ls, from, to)
	step := 7 * 24 * time.Hour
	if to.Sub(from) <= 31*24*time.Hour {
		step = 24 * time.Hour
	} else if to.Sub(from) > 400*24*time.Hour {
		step = 30 * 24 * time.Hour
	}
	rep.StepSeconds = int64(step / time.Second)
	rep.Series = rls.Series(ls, from, to, step)
	per := map[string][]rls.Launch{}
	perP := map[string][]rls.Launch{}
	for _, l := range ls {
		per[l.Airfield] = append(per[l.Airfield], l)
		perP[l.Project] = append(perP[l.Project], l)
	}
	for k, v := range per {
		rep.ByAirfield[k] = rls.Compute(v, from, to)
	}
	for k, v := range perP {
		rep.ByProject[k] = rls.Compute(v, from, to)
	}
	// What shipped: published releases in the period, with when each reached each Airfield.
	rels, err := e.ListReleases(ReleaseFilter{Project: project, State: ReleasePublished, Visible: visible})
	if err != nil {
		return nil, err
	}
	for _, r := range rels {
		if r.PublishedAt < from.UnixMilli() || r.PublishedAt >= to.UnixMilli() {
			continue
		}
		row := ReleaseReportRow{Release: r, Reached: []ReportAirfield{}}
		ps, err := e.ReleasePromotions(r.ID, "", 0)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for i := len(ps) - 1; i >= 0; i-- { // oldest first
			p := ps[i]
			switch p.State {
			case "failed":
				row.Failures++
			case "returned":
				row.Returns++
			}
			if p.State != "launched" || seen[p.Airfield] {
				continue
			}
			seen[p.Airfield] = true
			ra := ReportAirfield{Airfield: p.Airfield, At: p.RequestedAt, State: p.State, By: p.RequestedBy}
			var dep int64
			if e.db.QueryRow(`SELECT deployed_at FROM deployments WHERE run_id = ? AND environment = ? ORDER BY opened_at DESC LIMIT 1`, p.RunID, p.Airfield).Scan(&dep) == nil && dep > 0 {
				ra.At = dep
			}
			if r.CommitTime > 0 && ra.At > r.CommitTime {
				ra.LeadSeconds = (ra.At - r.CommitTime) / 1000
			}
			row.Reached = append(row.Reached, ra)
		}
		rep.Shipped = append(rep.Shipped, row)
	}
	sort.SliceStable(rep.Shipped, func(i, j int) bool { return rep.Shipped[i].Release.PublishedAt > rep.Shipped[j].Release.PublishedAt })
	return rep, nil
}
