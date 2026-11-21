package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/notify"
	rls "github.com/jk-magic-creations/launchway/internal/release"
)

// ---- checklists (gates) ----

// ReleaseGate is the checklist of an Airfield for a project.
type ReleaseGate struct {
	Project   string   `json:"project"`
	Airfield  string   `json:"airfield"`
	Gate      rls.Gate `json:"gate"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
}

// SetReleaseGate saves the checklist for promoting a project's releases to an Airfield.
func (e *Engine) SetReleaseGate(project, airfield string, g rls.Gate, actor string) (*ReleaseGate, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	if err := e.checkAirfield(airfield); err != nil {
		return nil, err
	}
	if err := g.Check(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	if g.PreviousAirfield != "" {
		if err := e.checkAirfield(g.PreviousAirfield); err != nil {
			return nil, err
		}
		if g.PreviousAirfield == airfield {
			return nil, fmt.Errorf("%w: an Airfield cannot be its own previous Airfield", ErrInvalid)
		}
	}
	b, _ := json.Marshal(g)
	now := e.now().UnixMilli()
	_, err := e.db.Exec(`INSERT INTO release_gates(project, airfield, gate, updated_at, updated_by) VALUES(?,?,?,?,?)
		ON CONFLICT(project, airfield) DO UPDATE SET gate = excluded.gate, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		project, airfield, string(b), now, actor)
	if err != nil {
		return nil, err
	}
	return &ReleaseGate{Project: project, Airfield: airfield, Gate: g, UpdatedAt: now, UpdatedBy: actor}, nil
}

func (e *Engine) checkAirfield(name string) error {
	for _, env := range e.Environments() {
		if env.Name == name {
			return nil
		}
	}
	return fmt.Errorf("%w: there is no Airfield %s", ErrNotFound, name)
}

// ReleaseGates lists the checklists of a project (all projects if empty).
func (e *Engine) ReleaseGates(project string) ([]ReleaseGate, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	q, args := `SELECT project, airfield, gate, updated_at, updated_by FROM release_gates`, []any{}
	if project != "" {
		q += ` WHERE project = ?`
		args = append(args, project)
	}
	rows, err := e.db.Query(q+` ORDER BY project, airfield`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReleaseGate{}
	for rows.Next() {
		var g ReleaseGate
		var j string
		if err := rows.Scan(&g.Project, &g.Airfield, &j, &g.UpdatedAt, &g.UpdatedBy); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(j), &g.Gate)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (e *Engine) gateOf(project, airfield string) (rls.Gate, error) {
	var j string
	err := e.db.QueryRow(`SELECT gate FROM release_gates WHERE project = ? AND airfield = ?`, project, airfield).Scan(&j)
	var g rls.Gate
	if errors.Is(err, sql.ErrNoRows) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	_ = json.Unmarshal([]byte(j), &g)
	return g, err
}

// ---- clearances ----

// ReleaseClearance is one person's clearance of a release for an Airfield.
type ReleaseClearance struct {
	ID        string `json:"id"`
	ReleaseID string `json:"release_id"`
	Airfield  string `json:"airfield"`
	Who       string `json:"who"`
	Role      string `json:"role"`
	Comment   string `json:"comment,omitempty"`
	At        int64  `json:"at"`
}

// GiveReleaseClearance records a clearance. The person who published the
// release may not clear it for an Airfield whose checklist asks for
// clearances: two people are needed.
func (e *Engine) GiveReleaseClearance(releaseID, airfield, who, role, comment string) (*ReleaseClearance, error) {
	r, err := e.GetRelease(releaseID)
	if err != nil {
		return nil, err
	}
	if err := e.checkAirfield(airfield); err != nil {
		return nil, err
	}
	g, err := e.gateOf(r.Project, airfield)
	if err != nil {
		return nil, err
	}
	if g.Clearances == 0 {
		return nil, fmt.Errorf("%w: the checklist of %s asks for no clearance", ErrConflict, airfield)
	}
	if r.State != ReleasePublished && r.State != ReleaseCandidate {
		return nil, fmt.Errorf("%w: a %s release is not cleared", ErrConflict, r.State)
	}
	if who == r.PublishedBy || who == r.CreatedBy && r.PublishedBy == "" {
		return nil, fmt.Errorf("%w: you published this release; someone else must give the clearance", ErrForbidden)
	}
	if len(g.ClearanceRoles) > 0 {
		ok := false
		for _, x := range g.ClearanceRoles {
			ok = ok || x == role
		}
		if !ok {
			return nil, fmt.Errorf("%w: clearance for %s needs the role %s; you have %s", ErrForbidden, airfield, strings.Join(g.ClearanceRoles, " or "), role)
		}
	}
	if len(comment) > 500 {
		return nil, fmt.Errorf("%w: the comment is limited to 500 characters", ErrInvalid)
	}
	c := ReleaseClearance{ID: ids.New("rcl"), ReleaseID: r.ID, Airfield: airfield, Who: who, Role: role, Comment: comment, At: e.now().UnixMilli()}
	_, err = e.db.Exec(`INSERT INTO release_clearances(id, release_id, airfield, who, role, comment, at) VALUES(?,?,?,?,?,?,?)`, c.ID, c.ReleaseID, c.Airfield, c.Who, c.Role, c.Comment, c.At)
	if err != nil {
		if isUnique(err) {
			return nil, fmt.Errorf("%w: you already gave clearance for %s in %s", ErrConflict, r.Version, airfield)
		}
		return nil, err
	}
	return &c, nil
}

// ReleaseClearances lists the clearances of a release.
func (e *Engine) ReleaseClearances(releaseID string) ([]ReleaseClearance, error) {
	rows, err := e.db.Query(`SELECT id, release_id, airfield, who, role, comment, at FROM release_clearances WHERE release_id = ? ORDER BY at`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReleaseClearance{}
	for rows.Next() {
		var c ReleaseClearance
		if err := rows.Scan(&c.ID, &c.ReleaseID, &c.Airfield, &c.Who, &c.Role, &c.Comment, &c.At); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- freezes ----

// CreateReleaseFreeze makes a freeze: nothing is promoted in it unless an administrator overrides, with a reason.
func (e *Engine) CreateReleaseFreeze(project, airfield string, from, to time.Time, reason, actor string) (*rls.Freeze, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "" || len(reason) > 500:
		return nil, fmt.Errorf("%w: say why (1 to 500 characters)", ErrInvalid)
	case !to.After(from):
		return nil, fmt.Errorf("%w: a freeze must end after it starts", ErrInvalid)
	case to.Sub(from) > 366*24*time.Hour:
		return nil, fmt.Errorf("%w: a freeze is at most a year long", ErrInvalid)
	}
	if airfield != "" {
		if err := e.checkAirfield(airfield); err != nil {
			return nil, err
		}
	}
	f := rls.Freeze{ID: ids.New("frz"), Project: project, Airfield: airfield, From: from.UnixMilli(), To: to.UnixMilli(), Reason: reason, By: actor}
	_, err := e.db.Exec(`INSERT INTO release_freezes(id, project, airfield, starts_at, ends_at, reason, created_by, created_at) VALUES(?,?,?,?,?,?,?,?)`,
		f.ID, f.Project, f.Airfield, f.From, f.To, f.Reason, actor, e.now().UnixMilli())
	return &f, err
}

// CancelReleaseFreeze ends a freeze early; it stays in the record.
func (e *Engine) CancelReleaseFreeze(id, actor string) error {
	res, err := e.db.Exec(`UPDATE release_freezes SET canceled_at = ?, canceled_by = ? WHERE id = ? AND canceled_at = 0`, e.now().UnixMilli(), actor, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: freeze %s, or it was already canceled", ErrNotFound, id)
	}
	return nil
}

// ReleaseFreezes lists freezes that have not ended before since (ms), canceled ones included.
func (e *Engine) ReleaseFreezes(since int64) ([]rls.Freeze, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT id, project, airfield, starts_at, ends_at, reason, created_by, canceled_at FROM release_freezes WHERE ends_at >= ? ORDER BY starts_at`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rls.Freeze{}
	for rows.Next() {
		var f rls.Freeze
		var canceled int64
		if err := rows.Scan(&f.ID, &f.Project, &f.Airfield, &f.From, &f.To, &f.Reason, &f.By, &canceled); err != nil {
			return nil, err
		}
		f.Canceled = canceled > 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- where releases are (the matrix) ----

// Cell is what one Airfield runs of one project.
type Cell struct {
	Airfield string `json:"airfield"`
	// Version is what runs there (a release's version when it is one).
	Version   string `json:"version,omitempty"`
	ReleaseID string `json:"release_id,omitempty"`
	// Since is when it got there (ms); By who started the run.
	Since int64  `json:"since,omitempty"`
	By    string `json:"by,omitempty"`
	// Health is healthy, launching, failed, returned or empty (nothing is there).
	Health     string `json:"health"`
	Run        string `json:"run,omitempty"`
	RunNumber  int    `json:"run_number,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	// Launching is the release being launched now, if any.
	Launching *CellLaunch `json:"launching,omitempty"`
}

// CellLaunch is a launch in progress.
type CellLaunch struct {
	Version   string `json:"version"`
	ReleaseID string `json:"release_id,omitempty"`
	Run       string `json:"run"`
	Since     int64  `json:"since"`
}

// MatrixRow is a project's row of the release × Airfield matrix.
type MatrixRow struct {
	Project string `json:"project"`
	// Latest is the newest published release.
	Latest   string `json:"latest,omitempty"`
	Cells    []Cell `json:"cells"`
	Releases int    `json:"releases"`
}

// ReleaseMatrix says which release is where, since when, by whom and how it is.
func (e *Engine) ReleaseMatrix(visible func(project string) bool) (airfields []string, rows []MatrixRow, err error) {
	if err := e.needReleases(); err != nil {
		return nil, nil, err
	}
	for _, env := range e.AirfieldOrder() {
		airfields = append(airfields, env)
	}
	// Projects that have releases or run somewhere.
	projects := map[string]bool{}
	for _, q := range []string{`SELECT DISTINCT project FROM releases`, `SELECT DISTINCT project FROM environment_state`} {
		rs, err := e.db.Query(q)
		if err != nil {
			return nil, nil, err
		}
		for rs.Next() {
			var p string
			if rs.Scan(&p) == nil {
				projects[p] = true
			}
		}
		rs.Close()
	}
	names := make([]string, 0, len(projects))
	for p := range projects {
		if visible == nil || visible(p) {
			names = append(names, p)
		}
	}
	sort.Strings(names)
	for _, p := range names {
		row := MatrixRow{Project: p, Cells: []Cell{}}
		rel, err := e.ListReleases(ReleaseFilter{Project: p})
		if err != nil {
			return nil, nil, err
		}
		row.Releases = len(rel)
		set, _ := e.ReleaseSettingsOf(p)
		for _, r := range rel {
			if (r.State == ReleasePublished) && (row.Latest == "" || rls.Compare(set.Scheme, r.Version, row.Latest) > 0 || set.Scheme == rls.SchemeCreated) {
				row.Latest = r.Version
			}
		}
		for _, af := range airfields {
			c, err := e.cellOf(p, af, rel)
			if err != nil {
				return nil, nil, err
			}
			row.Cells = append(row.Cells, c)
		}
		rows = append(rows, row)
	}
	return airfields, rows, nil
}

// AirfieldOrder is the Airfields in the order a release goes through them: a
// checklist's previous Airfield puts one after another; the rest keep the
// order of the configuration.
func (e *Engine) AirfieldOrder() []string {
	var names []string
	for _, env := range e.Environments() {
		names = append(names, env.Name)
	}
	gates, _ := e.ReleaseGates("")
	prev := map[string]string{}
	for _, g := range gates {
		if g.Gate.PreviousAirfield != "" {
			prev[g.Airfield] = g.Gate.PreviousAirfield
		}
	}
	depth := func(n string) int {
		d := 0
		for seen := map[string]bool{}; prev[n] != "" && !seen[n]; n = prev[n] {
			seen[n] = true
			d++
		}
		return d
	}
	sort.SliceStable(names, func(i, j int) bool { return depth(names[i]) < depth(names[j]) })
	return names
}

func (e *Engine) cellOf(project, airfield string, rels []Release) (Cell, error) {
	c := Cell{Airfield: airfield, Health: "empty"}
	var dep, version, run string
	var runNo int
	var since int64
	err := e.db.QueryRow(`SELECT current_deployment, current_version, current_run, current_run_number, since FROM environment_state
		WHERE environment = ? AND project = ? ORDER BY CASE WHEN service = project THEN 0 ELSE 1 END, service LIMIT 1`, airfield, project).Scan(&dep, &version, &run, &runNo, &since)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	if err == nil && version != "" {
		c.Version, c.Since, c.Run, c.RunNumber, c.Deployment, c.Health = version, since, run, runNo, dep, "healthy"
		_ = e.db.QueryRow(`SELECT actor FROM runs WHERE id = ?`, run).Scan(&c.By)
		var verdict string
		if e.db.QueryRow(`SELECT verdict FROM deployments WHERE id = ?`, dep).Scan(&verdict) == nil {
			switch verdict {
			case VerdictFailed, VerdictRollbackFailed:
				c.Health = "failed"
			case VerdictRolledBack:
				c.Health = "returned"
			}
		}
		c.ReleaseID = e.releaseFor(project, version, run, rels)
	}
	// A launch in progress.
	var lv, lrun string
	var lat int64
	if err := e.db.QueryRow(`SELECT version, run_id, opened_at FROM deployments WHERE project = ? AND environment = ? AND closed_at = 0 ORDER BY opened_at DESC LIMIT 1`,
		project, airfield).Scan(&lv, &lrun, &lat); err == nil {
		c.Launching = &CellLaunch{Version: lv, Run: lrun, Since: lat, ReleaseID: e.releaseFor(project, lv, lrun, rels)}
	}
	return c, nil
}

// releaseFor finds the release a deployment is of: the promotion that started its run, else a release whose version or tag it carries.
func (e *Engine) releaseFor(project, version, runID string, rels []Release) string {
	var id string
	if runID != "" && e.db.QueryRow(`SELECT release_id FROM release_promotions WHERE run_id = ?`, runID).Scan(&id) == nil {
		return id
	}
	for _, r := range rels {
		if r.Project == project && (r.Version == version || (r.Tag != "" && r.Tag == version)) {
			return r.ID
		}
	}
	return ""
}

// ---- the checklist ----

// Checklist is a release judged for an Airfield.
type Checklist struct {
	ReleaseID string     `json:"release_id"`
	Project   string     `json:"project"`
	Version   string     `json:"version"`
	Airfield  string     `json:"airfield"`
	Items     []rls.Item `json:"items"`
	Ready     bool       `json:"ready"`
	// CanOverride says every line that stops it may be overridden by an administrator with a reason.
	CanOverride bool               `json:"can_override"`
	Clearances  []ReleaseClearance `json:"clearances"`
	Gate        rls.Gate           `json:"gate"`
	// AlreadyThere says the release is what runs in the Airfield now.
	AlreadyThere bool `json:"already_there,omitempty"`
}

// ReleaseChecklist judges a release for an Airfield now.
func (e *Engine) ReleaseChecklist(releaseID, airfield string) (*Checklist, error) {
	r, err := e.GetRelease(releaseID)
	if err != nil {
		return nil, err
	}
	if err := e.checkAirfield(airfield); err != nil {
		return nil, err
	}
	g, err := e.gateOf(r.Project, airfield)
	if err != nil {
		return nil, err
	}
	now := e.now()
	f := rls.Facts{Now: now, Project: r.Project, Airfield: airfield, State: r.State, HasNotes: strings.TrimSpace(r.Notes) != "", Publisher: r.PublishedBy}
	f.ChecksGreen, f.ChecksDetail, err = e.checksGreen(r)
	if err != nil {
		return nil, err
	}
	f.BlockingFindings = e.blockingFindings(r)
	rels, _ := e.ListReleases(ReleaseFilter{Project: r.Project})
	if g.PreviousAirfield != "" {
		pc, err := e.cellOf(r.Project, g.PreviousAirfield, rels)
		if err != nil {
			return nil, err
		}
		f.InPrevious = pc.ReleaseID == r.ID
		f.PreviousHealthy = pc.Health == "healthy"
		f.PreviousSince = time.UnixMilli(pc.Since)
	}
	cl, err := e.ReleaseClearances(r.ID)
	if err != nil {
		return nil, err
	}
	given := []ReleaseClearance{}
	for _, c := range cl {
		if c.Airfield == airfield {
			given = append(given, c)
			f.Clearances = append(f.Clearances, c.Who)
		}
	}
	fz, err := e.ReleaseFreezes(now.UnixMilli())
	if err != nil {
		return nil, err
	}
	f.Freezes = fz
	items := rls.Evaluate(g, f)
	// A candidate goes only where the Airfield is not protected.
	if r.State == ReleaseCandidate {
		for _, env := range e.Environments() {
			if env.Name == airfield && env.Protected {
				for i := range items {
					if items[i].ID == "state" {
						items[i] = rls.Item{ID: "state", Label: items[i].Label, Status: rls.Fail, Detail: airfield + " is protected; only a published release goes there"}
					}
				}
			}
		}
	}
	list, over := rls.Blockers(items)
	ck := &Checklist{ReleaseID: r.ID, Project: r.Project, Version: r.Version, Airfield: airfield, Items: items, Ready: len(list) == 0, CanOverride: len(list) > 0 && over, Clearances: given, Gate: g}
	pc, _ := e.cellOf(r.Project, airfield, rels)
	ck.AlreadyThere = pc.ReleaseID == r.ID && pc.Health == "healthy"
	return ck, nil
}

func (e *Engine) checksGreen(r *Release) (bool, string, error) {
	rows, err := e.db.Query(`SELECT pipeline, status, number FROM runs WHERE project = ? AND sha = ? AND event <> 'rollback'
		AND id NOT IN (SELECT run_id FROM release_promotions) ORDER BY number DESC`, r.Project, r.SHA)
	if err != nil {
		return false, "", err
	}
	defer rows.Close()
	seen := map[string]bool{}
	green, bad := 0, []string{}
	for rows.Next() {
		var pipe, st string
		var n int
		if err := rows.Scan(&pipe, &st, &n); err != nil {
			return false, "", err
		}
		if seen[pipe] {
			continue
		}
		seen[pipe] = true
		if st == string(RunSucceeded) {
			green++
		} else {
			bad = append(bad, fmt.Sprintf("run #%d is %s", n, st))
		}
	}
	switch {
	case green+len(bad) == 0:
		return false, "no run has checked commit " + shortSHA(r.SHA), rows.Err()
	case len(bad) > 0:
		return false, strings.Join(bad, "; "), rows.Err()
	}
	return true, fmt.Sprintf("%d run(s) succeeded at %s", green, shortSHA(r.SHA)), rows.Err()
}

func (e *Engine) blockingFindings(r *Release) int {
	if r.RunID == "" || !e.knows(schemaArtifacts) {
		return 0
	}
	f, err := e.RunFindings(r.RunID, FindingQuery{Limit: 1})
	if err != nil || f == nil || f.Counts == nil {
		return 0
	}
	return f.Counts.Critical + f.Counts.High
}

// ---- promotions ----

// ReleasePromotion is one promotion of a release to an Airfield.
type ReleasePromotion struct {
	ID          string          `json:"id"`
	ReleaseID   string          `json:"release_id"`
	Project     string          `json:"project"`
	Version     string          `json:"version"`
	Airfield    string          `json:"airfield"`
	SHA         string          `json:"sha"`
	Digests     []ReleaseDigest `json:"digests"`
	RunID       string          `json:"run_id,omitempty"`
	RunNumber   int             `json:"run_number,omitempty"`
	FormationID string          `json:"formation_id,omitempty"`
	PlannedFor  int64           `json:"planned_for,omitempty"`
	RequestedBy string          `json:"requested_by"`
	RequestedAt int64           `json:"requested_at"`
	// Override is the reason an administrator gave to promote past a checklist line; Overridden says which lines.
	Override   string `json:"override,omitempty"`
	Overridden string `json:"overridden,omitempty"`
	State      string `json:"state"`
	Detail     string `json:"detail,omitempty"`
}

const promoCols = `id, release_id, project, version, airfield, sha, digests, run_id, run_number, formation_id, planned_for, requested_by, requested_at, override, overridden, state, detail`

func scanPromotion(r rowScanner) (ReleasePromotion, error) {
	var p ReleasePromotion
	var d string
	err := r.Scan(&p.ID, &p.ReleaseID, &p.Project, &p.Version, &p.Airfield, &p.SHA, &d, &p.RunID, &p.RunNumber, &p.FormationID, &p.PlannedFor, &p.RequestedBy, &p.RequestedAt, &p.Override, &p.Overridden, &p.State, &p.Detail)
	p.Digests = []ReleaseDigest{}
	_ = json.Unmarshal([]byte(d), &p.Digests)
	return p, err
}

// RecordPromotion writes a promotion down.
func (e *Engine) RecordPromotion(p ReleasePromotion) (*ReleasePromotion, error) {
	p.ID, p.RequestedAt = ids.New("prm"), e.now().UnixMilli()
	if p.State == "" {
		p.State = "started"
	}
	if p.Digests == nil {
		p.Digests = []ReleaseDigest{}
	}
	_, err := e.db.Exec(`INSERT INTO release_promotions(`+promoCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.ReleaseID, p.Project, p.Version, p.Airfield, p.SHA, jsonString(p.Digests), p.RunID, p.RunNumber, p.FormationID, p.PlannedFor, p.RequestedBy, p.RequestedAt, p.Override, p.Overridden, p.State, p.Detail)
	if err != nil {
		return nil, err
	}
	e.notifyRelease(notify.ReleasePromoted, p.Project, p.Airfield, "prm:"+p.ID, fmt.Sprintf("%s %s was promoted to %s by %s", p.Project, p.Version, p.Airfield, p.RequestedBy),
		overrideNote(p.Override), "/deployments/releases/"+p.ReleaseID)
	return &p, nil
}

func overrideNote(o string) string {
	if o == "" {
		return ""
	}
	return "Promoted past the checklist: " + clipText(o, 200)
}

// SetPromotionState updates a promotion.
func (e *Engine) SetPromotionState(id, state, detail string) error {
	_, err := e.db.Exec(`UPDATE release_promotions SET state = ?, detail = ? WHERE id = ?`, state, clipText(detail, 500), id)
	return err
}

// ReleasePromotions lists a release's promotions (all of a project when releaseID is empty), newest first, with the state of their runs.
func (e *Engine) ReleasePromotions(releaseID, project string, limit int) ([]ReleasePromotion, error) {
	q, args := `SELECT `+promoCols+` FROM release_promotions WHERE 1=1`, []any{}
	if releaseID != "" {
		q += ` AND release_id = ?`
		args = append(args, releaseID)
	}
	if project != "" {
		q += ` AND project = ?`
		args = append(args, project)
	}
	q += ` ORDER BY requested_at DESC, id DESC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReleasePromotion{}
	for rows.Next() {
		p, err := scanPromotion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].State, out[i].Detail = e.promotionOutcome(out[i])
	}
	return out, nil
}

// promotionOutcome reads what became of a promotion from its run and deployment.
func (e *Engine) promotionOutcome(p ReleasePromotion) (string, string) {
	if p.RunID == "" || p.State == "refused" || p.State == "returned" {
		return p.State, p.Detail
	}
	var st string
	if e.db.QueryRow(`SELECT status FROM runs WHERE id = ?`, p.RunID).Scan(&st) != nil {
		return p.State, p.Detail
	}
	var verdict string
	_ = e.db.QueryRow(`SELECT verdict FROM deployments WHERE run_id = ? AND environment = ? ORDER BY opened_at DESC LIMIT 1`, p.RunID, p.Airfield).Scan(&verdict)
	switch {
	case verdict == VerdictRolledBack:
		return "returned", "returned to base"
	case verdict == VerdictDeployed || (verdict == "" && st == string(RunSucceeded)):
		return "launched", ""
	case st == string(RunFailed) || verdict == VerdictFailed || verdict == VerdictRollbackFailed:
		return "failed", "the Sortie " + st
	case st == string(RunCanceled) || verdict == VerdictAbandoned:
		return "canceled", ""
	}
	return "started", ""
}

// ---- notifications ----

func (e *Engine) notifyRelease(kind, project, airfield, key, title, detail, link string) {
	cfg := e.live().notify
	if len(cfg.Rules) == 0 {
		return
	}
	e.enqueueNotifications(context.Background(), cfg, notify.Event{Kind: kind, Project: project, Environment: airfield, At: e.now().UnixMilli(), Key: key, Title: title, Detail: detail, Link: link})
}

// NoticeReleasePublished tells the channels a release was published.
func (e *Engine) NoticeReleasePublished(r *Release) {
	e.notifyRelease(notify.ReleasePublished, r.Project, "", "rel:"+r.ID, fmt.Sprintf("%s %s was published by %s", r.Project, r.Version, r.PublishedBy), "", "/deployments/releases/"+r.ID)
}

// NoticeReleaseReturned tells the channels a release was returned to base.
func (e *Engine) NoticeReleaseReturned(project, airfield, from, to, who, reason, runID string) {
	e.notifyRelease(notify.ReleaseReturned, project, airfield, "ret:"+runID, fmt.Sprintf("%s in %s was returned to base from %s to %s by %s", project, airfield, from, to, who), clipText(reason, 200), "/runs/"+runID)
}

// eventsFreezes announces freezes that have begun.
func (e *Engine) eventsFreezes(ctx context.Context, n *Notifier) ([]notify.Event, error) {
	if !e.knows(schemaReleases) {
		return nil, nil
	}
	now := e.now().UnixMilli()
	rows, err := e.db.Query(`SELECT id, project, airfield, reason, ends_at FROM release_freezes WHERE announced = 0 AND canceled_at = 0 AND starts_at <= ? AND ends_at > ? LIMIT 50`, now, now)
	if err != nil {
		return nil, err
	}
	var out []notify.Event
	var done []string
	for rows.Next() {
		var id, project, af, reason string
		var ends int64
		if err := rows.Scan(&id, &project, &af, &reason, &ends); err != nil {
			rows.Close()
			return nil, err
		}
		where := "every Airfield"
		if af != "" {
			where = af
		}
		out = append(out, notify.Event{Kind: notify.FreezeStarted, Project: project, Environment: af, At: now, Key: "frz:" + id,
			Title: fmt.Sprintf("A freeze began for %s: nothing is promoted until %s", where, time.UnixMilli(ends).UTC().Format("2006-01-02 15:04 MST")), Detail: clipText(reason, 200), Link: n.link("/deployments/releases/calendar")})
		done = append(done, id)
	}
	rows.Close()
	for _, id := range done {
		_, _ = e.db.Exec(`UPDATE release_freezes SET announced = 1 WHERE id = ?`, id)
	}
	return out, nil
}

// AirfieldKnown returns nil if the Airfield exists.
func (e *Engine) AirfieldKnown(name string) error { return e.checkAirfield(name) }
