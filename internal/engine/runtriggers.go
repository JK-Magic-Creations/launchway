package engine

// Triggers on another project's result (schema 49): a run (or a launch) of an
// upstream project ending starts a run of a downstream project, with inputs
// mapped from the result. The upstream project must allow it, by project, and
// says whether the downstream may read its runs' outputs; nothing else crosses
// (no secret, no log, no artifact content: the run is named by reference).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// The kinds of result trigger.
const (
	TriggerOnRun    = "run"
	TriggerOnLaunch = "launch"
)

// RunTrigger starts a run of Project when an upstream project's run or launch ends.
type RunTrigger struct {
	ID               string            `json:"id"`
	Project          string            `json:"project"`
	Name             string            `json:"name"`
	Kind             string            `json:"kind"`
	UpstreamProject  string            `json:"upstream_project"`
	UpstreamPipeline string            `json:"upstream_pipeline,omitempty"`
	When             string            `json:"when"`
	Filter           string            `json:"filter,omitempty"`
	Ref              string            `json:"ref,omitempty"`
	Mapping          map[string]string `json:"mapping"`
	Enabled          bool              `json:"enabled"`
	CreatedAt        int64             `json:"created_at"`
	CreatedBy        string            `json:"created_by"`
}

// RunTriggerAllow is an upstream project's permission for a downstream one.
type RunTriggerAllow struct {
	Upstream     string `json:"upstream_project"`
	Downstream   string `json:"downstream_project"`
	ShareOutputs bool   `json:"share_outputs"`
	CreatedAt    int64  `json:"created_at"`
	CreatedBy    string `json:"created_by"`
}

var runWhen = map[string][]string{
	TriggerOnRun:    {"succeeded", "failed", "canceled", "any"},
	TriggerOnLaunch: {"deployed", "failed", "rolled_back", "any"},
}

// CheckRunTrigger says what is wrong with a trigger's settings.
func CheckRunTrigger(t *RunTrigger) error {
	if !ValidEventSourceName(t.Name) {
		return fmt.Errorf("%w: a trigger's name is lower-case letters, digits and hyphens, up to 63", ErrInvalid)
	}
	whens, ok := runWhen[t.Kind]
	if !ok {
		return fmt.Errorf("%w: kind is run or launch", ErrInvalid)
	}
	found := false
	for _, w := range whens {
		found = found || w == t.When
	}
	if !found {
		return fmt.Errorf("%w: for a %s, when is one of %s", ErrInvalid, t.Kind, strings.Join(whens, ", "))
	}
	if t.UpstreamProject == "" {
		return fmt.Errorf("%w: say which project's results to trigger on", ErrInvalid)
	}
	if t.UpstreamProject == t.Project {
		return fmt.Errorf("%w: a project cannot trigger on its own results", ErrInvalid)
	}
	if len(t.Mapping) > 50 {
		return fmt.Errorf("%w: at most 50 inputs are mapped", ErrInvalid)
	}
	return nil
}

// reaches reports whether, following triggers from project "from", the
// project "to" is a downstream of it (so that adding the opposite edge would
// make a cycle).
func (e *Engine) reaches(from, to string) (bool, error) {
	rows, err := e.db.Query(`SELECT upstream_project, project FROM run_triggers`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	next := map[string][]string{}
	for rows.Next() {
		var up, down string
		if err := rows.Scan(&up, &down); err != nil {
			return false, err
		}
		next[up] = append(next[up], down)
	}
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == to {
			return true, nil
		}
		for _, n := range next[cur] {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return false, rows.Err()
}

// CreateRunTrigger makes a trigger. It is refused if the upstream project has
// not allowed the downstream one, and if it would make a cycle (A triggers B
// triggers A).
func (e *Engine) CreateRunTrigger(t RunTrigger) (*RunTrigger, error) {
	if !e.knows(schemaEvents) {
		return nil, fmt.Errorf("%w: this server does not know result triggers (schema %d)", ErrConflict, schemaEvents)
	}
	if t.When == "" {
		t.When = "succeeded"
		if t.Kind == TriggerOnLaunch {
			t.When = "deployed"
		}
	}
	if t.Kind == "" {
		t.Kind = TriggerOnRun
	}
	if err := CheckRunTrigger(&t); err != nil {
		return nil, err
	}
	if _, err := e.runTriggerAllow(t.UpstreamProject, t.Project); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: project %s has not allowed %s to trigger on its results; an operator of %s allows it first (launchway run-triggers allow %s %s)",
				ErrConflict, t.UpstreamProject, t.Project, t.UpstreamProject, t.UpstreamProject, t.Project)
		}
		return nil, err
	}
	if cyc, err := e.reaches(t.Project, t.UpstreamProject); err != nil {
		return nil, err
	} else if cyc {
		return nil, fmt.Errorf("%w: %s already triggers (directly or through other projects) on the results of %s, so this would make a cycle", ErrConflict, t.UpstreamProject, t.Project)
	}
	t.ID, t.Enabled, t.CreatedAt = ids.New("rtr"), true, e.now().UnixMilli()
	mapping, _ := json.Marshal(t.Mapping)
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`INSERT INTO run_triggers(id, project, name, kind, upstream_project, upstream_pipeline, when_result, filter_expr, ref, mapping, enabled, created_at, created_by)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.Project, t.Name, t.Kind, t.UpstreamProject, t.UpstreamPipeline, t.When, t.Filter, t.Ref, string(mapping), 1, t.CreatedAt, t.CreatedBy); err != nil {
		if isUnique(err) {
			return nil, fmt.Errorf("%w: project %s already has a trigger called %s", ErrConflict, t.Project, t.Name)
		}
		return nil, err
	}
	return &t, nil
}

const runTriggerColumns = `id, project, name, kind, upstream_project, upstream_pipeline, when_result, filter_expr, ref, mapping, enabled, created_at, created_by`

func scanRunTrigger(rows interface{ Scan(...any) error }) (RunTrigger, error) {
	var t RunTrigger
	var mapping string
	var en int
	if err := rows.Scan(&t.ID, &t.Project, &t.Name, &t.Kind, &t.UpstreamProject, &t.UpstreamPipeline, &t.When, &t.Filter, &t.Ref, &mapping, &en, &t.CreatedAt, &t.CreatedBy); err != nil {
		return t, err
	}
	t.Enabled = en == 1
	t.Mapping = map[string]string{}
	_ = json.Unmarshal([]byte(mapping), &t.Mapping)
	return t, nil
}

// RunTriggers lists a project's triggers (as the downstream).
func (e *Engine) RunTriggers(project string) ([]RunTrigger, error) {
	return e.runTriggersWhere(`project = ?`, project)
}

// RunTriggersOn lists the triggers that wait for an upstream project's results.
func (e *Engine) RunTriggersOn(upstream string) ([]RunTrigger, error) {
	return e.runTriggersWhere(`upstream_project = ? AND enabled = 1`, upstream)
}

func (e *Engine) runTriggersWhere(where string, arg string) ([]RunTrigger, error) {
	if !e.knows(schemaEvents) {
		return []RunTrigger{}, nil
	}
	rows, err := e.db.Query(`SELECT `+runTriggerColumns+` FROM run_triggers WHERE `+where+` ORDER BY name`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunTrigger{}
	for rows.Next() {
		t, err := scanRunTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteRunTrigger removes a trigger of a project.
func (e *Engine) DeleteRunTrigger(project, name string) error {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`DELETE FROM run_triggers WHERE project = ? AND name = ?`, project, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: trigger %s of %s", ErrNotFound, name, project)
	}
	return nil
}

// AllowRunTriggers lets downstream trigger on upstream's results.
func (e *Engine) AllowRunTriggers(a RunTriggerAllow) error {
	if !e.knows(schemaEvents) {
		return fmt.Errorf("%w: this server does not know result triggers (schema %d)", ErrConflict, schemaEvents)
	}
	if a.Upstream == "" || a.Downstream == "" || a.Upstream == a.Downstream {
		return fmt.Errorf("%w: name two different projects", ErrInvalid)
	}
	a.CreatedAt = e.now().UnixMilli()
	share := 0
	if a.ShareOutputs {
		share = 1
	}
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO run_trigger_allow(upstream_project, downstream_project, share_outputs, created_at, created_by) VALUES(?,?,?,?,?)
		ON CONFLICT(upstream_project, downstream_project) DO UPDATE SET share_outputs = excluded.share_outputs`, a.Upstream, a.Downstream, share, a.CreatedAt, a.CreatedBy)
	return err
}

// RevokeRunTriggers withdraws the permission. Triggers that used it stay, and
// are refused (with the reason) until it is given again.
func (e *Engine) RevokeRunTriggers(upstream, downstream string) error {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`DELETE FROM run_trigger_allow WHERE upstream_project = ? AND downstream_project = ?`, upstream, downstream)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s does not allow %s", ErrNotFound, upstream, downstream)
	}
	return nil
}

func (e *Engine) runTriggerAllow(upstream, downstream string) (*RunTriggerAllow, error) {
	var a RunTriggerAllow
	var share int
	err := e.db.QueryRow(`SELECT upstream_project, downstream_project, share_outputs, created_at, created_by FROM run_trigger_allow WHERE upstream_project = ? AND downstream_project = ?`,
		upstream, downstream).Scan(&a.Upstream, &a.Downstream, &share, &a.CreatedAt, &a.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s does not allow %s", ErrNotFound, upstream, downstream)
	}
	a.ShareOutputs = share == 1
	return &a, err
}

// RunTriggerPermission returns the permission, or ErrNotFound.
func (e *Engine) RunTriggerPermission(upstream, downstream string) (*RunTriggerAllow, error) {
	return e.runTriggerAllow(upstream, downstream)
}

// RunTriggerAllows lists the permissions an upstream project has given.
func (e *Engine) RunTriggerAllows(upstream string) ([]RunTriggerAllow, error) {
	if !e.knows(schemaEvents) {
		return []RunTriggerAllow{}, nil
	}
	rows, err := e.db.Query(`SELECT upstream_project, downstream_project, share_outputs, created_at, created_by FROM run_trigger_allow WHERE upstream_project = ? ORDER BY downstream_project`, upstream)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunTriggerAllow{}
	for rows.Next() {
		var a RunTriggerAllow
		var share int
		if err := rows.Scan(&a.Upstream, &a.Downstream, &share, &a.CreatedAt, &a.CreatedBy); err != nil {
			return nil, err
		}
		a.ShareOutputs = share == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- results ----

// ResultEvent is a run or a launch that ended.
type ResultEvent struct {
	Seq         int64  `json:"seq"`
	Kind        string `json:"kind"`
	RunID       string `json:"run_id"`
	Verdict     string `json:"verdict"` // run: succeeded, failed or canceled; launch: deployed, failed, rolled_back...
	Environment string `json:"environment,omitempty"`
	Service     string `json:"service,omitempty"`
	Version     string `json:"version,omitempty"`
}

// ResultsAfter lists runs and launches that ended after event number seq,
// oldest first.
func (e *Engine) ResultsAfter(seq int64, limit int) ([]ResultEvent, error) {
	if !e.knows(schemaEvents) {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT seq, run_id, type, data FROM events WHERE seq > ? AND type IN (?, ?) ORDER BY seq LIMIT ?`, seq, string(RunFinished), string(DeployClosed), limit)
	if err != nil {
		return nil, err
	}
	var out []ResultEvent
	for rows.Next() {
		var r ResultEvent
		var typ, data string
		if err := rows.Scan(&r.Seq, &r.RunID, &typ, &data); err != nil {
			rows.Close()
			return nil, err
		}
		var p Payload
		_ = json.Unmarshal([]byte(data), &p)
		if typ == string(RunFinished) {
			r.Kind, r.Verdict = TriggerOnRun, p.Status
		} else if p.Deploy != nil {
			r.Kind, r.Verdict = TriggerOnLaunch, p.Deploy.Verdict
			r.Environment, r.Service, r.Version = e.deployFacts(r.RunID, p.Deploy.ID)
		}
		out = append(out, r)
	}
	rows.Close()
	return out, rows.Err()
}

// deployFacts reads where a deployment went from its deploy.opened event.
func (e *Engine) deployFacts(runID, deployID string) (env, service, version string) {
	rows, err := e.db.Query(`SELECT data FROM events WHERE run_id = ? AND type = ?`, runID, string(DeployOpened))
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if rows.Scan(&data) != nil {
			continue
		}
		var p Payload
		if json.Unmarshal([]byte(data), &p) == nil && p.Deploy != nil && p.Deploy.ID == deployID {
			return p.Deploy.Environment, p.Deploy.Service, p.Deploy.Version
		}
	}
	return
}

// ResultCursor returns how far this installation's servers have read results,
// starting, the first time, at the newest event (history starts nothing).
func (e *Engine) ResultCursor() (int64, error) {
	var seq int64
	err := e.db.QueryRow(`SELECT seq FROM event_cursors WHERE name = 'results'`).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		unlock := e.writeLock()
		defer unlock()
		if err := e.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq); err != nil {
			return 0, err
		}
		_, err = e.db.Exec(`INSERT INTO event_cursors(name, seq) VALUES('results', ?) ON CONFLICT DO NOTHING`, seq)
		if err != nil {
			return 0, err
		}
		return e.ResultCursor()
	}
	return seq, err
}

// MoveResultCursor moves the cursor forward, never back.
func (e *Engine) MoveResultCursor(seq int64) {
	unlock := e.writeLock()
	defer unlock()
	_, _ = e.db.Exec(`UPDATE event_cursors SET seq = ? WHERE name = 'results' AND seq < ?`, seq, seq)
}

// RunOutputs returns the outputs the steps of a run wrote, by step. It is
// given only to a project the upstream allowed to read them.
func (e *Engine) RunOutputs(runID string) (map[string]map[string]string, error) {
	d, err := e.GetRun(runID)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, s := range d.StepList {
		if len(s.Outputs) > 0 {
			out[s.ID] = s.Outputs
		}
	}
	return out, nil
}
