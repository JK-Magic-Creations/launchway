package engine

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Runner pools (schema 21). Every runner belongs to one pool. A build pool
// serves the projects its configuration names, and optionally only some
// events; its runners are only ever given steps of runs it serves, which
// tryLease enforces in SQL and again in the transaction that grants the
// lease. A deploy pool serves targets (deployments.go); its runners take
// deploy work only.
//
// The pool named default always exists. Configured, it serves what it
// says; not configured, it serves every project that no configured build
// pool names. Runners using the shared runner token belong to it, as every
// build runner did before pools existed.

// Kinds of pool.
const (
	PoolBuild  = "build"
	PoolDeploy = "deploy"
)

// DefaultPool is the pool of build runners that name none: those with the
// shared token, and those enrolled with a join token for it.
const DefaultPool = "default"

// RunnerPool is one pool, as the server's configuration describes it.
type RunnerPool struct {
	Name string
	Kind string // PoolBuild or PoolDeploy
	// Projects, for a build pool, are the projects it serves: names or
	// patterns such as web-*. A build pool must list some, except the
	// default pool, which without any serves what no other pool lists.
	Projects []string
	// Events, if not empty, are the only events whose runs it serves (push,
	// manual, schedule, webhook, pull_request, draft).
	Events []string
	// RequireIdentity refuses the shared token (for the default pool) or
	// the pool's own token (for a deploy pool): only enrolled runners may
	// serve it.
	RequireIdentity bool
	// Targets, for a deploy pool, are what it serves.
	Targets []string
}

// checkPools checks the pools and returns them by name, with the default
// pool added if it is not configured.
func checkPools(list []RunnerPool) (map[string]RunnerPool, error) {
	out := map[string]RunnerPool{}
	for _, p := range list {
		where := "runner pool " + p.Name
		switch {
		case !pipeline.ValidLabel(p.Name):
			return nil, fmt.Errorf("%w: %q is not a pool name: use lower-case letters, digits, '.', '_' and '-'", ErrInvalid, p.Name)
		case out[p.Name].Name != "":
			return nil, fmt.Errorf("%w: %s is configured twice", ErrInvalid, where)
		case p.Kind != PoolBuild && p.Kind != PoolDeploy:
			return nil, fmt.Errorf("%w: %s: kind is build or deploy", ErrInvalid, where)
		case p.Kind == PoolDeploy && p.Name == DefaultPool:
			return nil, fmt.Errorf("%w: the pool named default is the build pool of the shared token; give the deploy pool another name", ErrInvalid)
		case p.Kind == PoolBuild && len(p.Projects) == 0 && p.Name != DefaultPool:
			return nil, fmt.Errorf("%w: %s: list the projects it serves (names or patterns; [\"*\"] for all)", ErrInvalid, where)
		}
		for _, pat := range p.Projects {
			if _, err := path.Match(pat, ""); err != nil || pat == "" {
				return nil, fmt.Errorf("%w: %s: project pattern %q is not valid", ErrInvalid, where, pat)
			}
		}
		for _, ev := range p.Events {
			if !slices.Contains(pipeline.Events, ev) {
				return nil, fmt.Errorf("%w: %s: event %q is not one of %s", ErrInvalid, where, ev, strings.Join(pipeline.Events, ", "))
			}
		}
		out[p.Name] = p
	}
	if _, ok := out[DefaultPool]; !ok {
		out[DefaultPool] = RunnerPool{Name: DefaultPool, Kind: PoolBuild}
	}
	return out, nil
}

// buildPoolsConfigured reports whether any build pool other than an
// unconfigured default exists: then pools decide where runs go.
func (e *Engine) buildPoolsConfigured() bool {
	for _, p := range e.live().pools {
		if p.Kind == PoolBuild && (p.Name != DefaultPool || len(p.Projects) > 0 || len(p.Events) > 0) {
			return true
		}
	}
	return false
}

// Pool returns a configured pool, or the default one; false if there is no
// such pool.
func (e *Engine) Pool(name string) (RunnerPool, bool) {
	p, ok := e.live().pools[name]
	return p, ok
}

// Pools lists the pools, by name.
func (e *Engine) Pools() []RunnerPool {
	out := make([]RunnerPool, 0, len(e.live().pools))
	for _, p := range e.live().pools {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// poolServes says why a build pool does not serve a run of project started
// by event, or "" if it does.
func (e *Engine) poolServes(p RunnerPool, project, event string) string {
	if p.Kind != PoolBuild {
		return fmt.Sprintf("pool %s is a deploy pool, which takes deploy work only", p.Name)
	}
	if len(p.Events) > 0 && !slices.Contains(p.Events, event) {
		return fmt.Sprintf("pool %s serves only runs started by %s, and this run was started by %s", p.Name, strings.Join(p.Events, ", "), event)
	}
	if len(p.Projects) > 0 {
		for _, pat := range p.Projects {
			if ok, _ := path.Match(pat, project); ok {
				return ""
			}
		}
		return fmt.Sprintf("pool %s does not serve project %s (its projects: %s)", p.Name, project, strings.Join(p.Projects, ", "))
	}
	// The default pool without projects: every run no other build pool
	// serves (a pool limited to some events leaves the others to it).
	for _, o := range e.live().pools {
		if o.Kind != PoolBuild || o.Name == p.Name || len(o.Projects) == 0 {
			continue
		}
		if e.poolServes(o, project, event) == "" {
			return fmt.Sprintf("project %s is served by pool %s, not by the default pool", project, o.Name)
		}
	}
	return ""
}

// runnerPoolName is the build pool a runner belongs to: RunnerInfo.Pool is
// "" for the default pool.
func runnerPoolName(r RunnerInfo) string { return nonEmpty(r.Pool, DefaultPool) }

// refusesRun says why runner r may not take steps of a run, or "" if it may:
// its pool must serve the run's project and event, the pool the pipeline
// chose (if any) must be its own, it must have every label the pipeline
// asks for, and what its accept: section says it allows must allow the
// project and event. (The runner checks the rest of accept: itself, and
// refuses what it does not allow.)
func (e *Engine) refusesRun(r RunnerInfo, project, event string, sel *pipeline.RunnerSelector) string {
	pool, ok := e.live().pools[runnerPoolName(r)]
	if !ok {
		return fmt.Sprintf("the runner's pool %s is not configured on this server", runnerPoolName(r))
	}
	if pool.RequireIdentity && r.Auth != AuthIdentity {
		return fmt.Sprintf("pool %s takes only enrolled runners, and this one uses a shared token", pool.Name)
	}
	if why := e.poolServes(pool, project, event); why != "" {
		return why
	}
	if sel != nil {
		if sel.Pool != "" && sel.Pool != pool.Name {
			return fmt.Sprintf("the pipeline asks for pool %s", sel.Pool)
		}
		for _, l := range sel.Labels {
			if !slices.Contains(r.Labels, l) {
				return fmt.Sprintf("the pipeline asks for label %s", l)
			}
		}
	}
	if why := r.Accept.Project(project); why != "" {
		return why
	}
	return r.Accept.Event(event)
}

// leaseFilter is the SQL condition limiting the runs whose steps a runner
// may lease to those its pool serves; "" when every queued run qualifies.
// It looks at the distinct (project, event, pool, labels) of the queued
// steps, which are few, and lists the ones the runner may take, so that the
// query that picks steps stays a plain indexed one and never fills its
// limit with steps the runner may not take. ok is false when there are
// none.
func (e *Engine) leaseFilter(r RunnerInfo) (where string, args []any, ok bool, err error) {
	if !e.knows(schemaPools) {
		return "", nil, true, nil
	}
	// A step with a runner of its own is matched on its own selector, not
	// the run's (schema 27).
	poolCol, labelsCol, ownCol, ownSel := `runs.runner_pool`, `runs.runner_labels`, `0`, ``
	if e.knows(schemaParity2) {
		poolCol = `CASE WHEN s.own_workspace = 1 THEN s.runner_pool ELSE runs.runner_pool END`
		labelsCol = `CASE WHEN s.own_workspace = 1 THEN s.runner_labels ELSE runs.runner_labels END`
		ownCol, ownSel = `s.own_workspace`, `s.own_workspace = ? AND `
	}
	rows, err := e.db.Query(`SELECT DISTINCT runs.project, runs.event, `+poolCol+`, `+labelsCol+`, `+ownCol+` FROM steps s
		JOIN runs ON runs.id = s.run_id
		WHERE s.status = 'queued' AND s.target = '' AND runs.min_schema <= ?`, e.schema)
	if err != nil {
		return "", nil, false, err
	}
	defer rows.Close()
	type combo struct {
		project, event, pool, labels string
		own                          int
	}
	var allowed []combo
	all := true
	for rows.Next() {
		var c combo
		if err := rows.Scan(&c.project, &c.event, &c.pool, &c.labels, &c.own); err != nil {
			return "", nil, false, err
		}
		var sel *pipeline.RunnerSelector
		if c.pool != "" || c.labels != "" {
			sel = &pipeline.RunnerSelector{Pool: c.pool, Labels: strings.Fields(c.labels)}
		}
		if e.refusesRun(r, c.project, c.event, sel) != "" {
			all = false
			continue
		}
		allowed = append(allowed, c)
	}
	if err := rows.Err(); err != nil {
		return "", nil, false, err
	}
	switch {
	case len(allowed) == 0:
		return "", nil, false, nil
	case all:
		return "", nil, true, nil
	}
	var b strings.Builder
	b.WriteString(` AND (`)
	for i, c := range allowed {
		if i > 0 {
			b.WriteString(` OR `)
		}
		b.WriteString(`(runs.project = ? AND runs.event = ? AND ` + ownSel + `(` + poolCol + `) = ? AND (` + labelsCol + `) = ?)`)
		args = append(args, c.project, c.event)
		if ownSel != "" {
			args = append(args, c.own)
		}
		args = append(args, c.pool, c.labels)
	}
	b.WriteString(`)`)
	return b.String(), args, true, nil
}

// poolHint says why a run that is waiting for a runner may wait for a long
// time: no runner seen lately may take it, because of pools, labels or
// what runners accept. "" if some runner may, or the run waits for nothing.
func (e *Engine) poolHint(st *RunState, steps []StepView) (string, error) {
	if !e.knows(schemaPools) || st.Status.Terminal() || st.Pipeline == nil {
		return "", nil
	}
	queued := false
	for _, s := range steps {
		if s.Status == StepQueuedS {
			if def := st.Pipeline.Step(s.ID); def != nil && !def.IsDeployKind() {
				queued = true
			}
		}
	}
	if !queued {
		return "", nil
	}
	sel := st.Pipeline.Runner
	// What the configuration allows, whichever runners are connected.
	if sel != nil && sel.Pool != "" {
		p, ok := e.live().pools[sel.Pool]
		switch {
		case !ok:
			return fmt.Sprintf("The pipeline asks for runner pool %s (runner.pool), which this server does not have.", sel.Pool), nil
		case p.Kind != PoolBuild:
			return fmt.Sprintf("The pipeline asks for runner pool %s (runner.pool), which is a deploy pool: it takes deploy work only.", sel.Pool), nil
		}
		if why := e.poolServes(p, st.Project, st.Trigger.Event); why != "" {
			return "The pipeline asks for runner pool " + sel.Pool + ", but " + why + ".", nil
		}
	} else {
		serving := []string{}
		for _, p := range e.Pools() {
			if p.Kind == PoolBuild && e.poolServes(p, st.Project, st.Trigger.Event) == "" {
				serving = append(serving, p.Name)
			}
		}
		if len(serving) == 0 {
			return fmt.Sprintf("No runner pool serves project %s for runs started by %s, so no runner may take its steps. "+
				"Add the project to a pool's projects (runner_pools in the server's configuration).", st.Project, st.Trigger.Event), nil
		}
	}
	// The runners seen lately, and why each may not take it.
	rows, err := e.db.Query(`SELECT id, name, pool, kind, auth, labels, accept FROM runners WHERE last_seen >= ?`, e.now().Add(-RunnerOffline).UnixMilli())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	reasons := map[string]int{}
	seen := 0
	for rows.Next() {
		var id, name, pool, kind, auth, labels, acc string
		if err := rows.Scan(&id, &name, &pool, &kind, &auth, &labels, &acc); err != nil {
			return "", err
		}
		if kind == PoolDeploy || (kind == "" && pool != "") {
			continue // a deploy runner
		}
		seen++
		r := RunnerInfo{ID: id, Pool: pool, Kind: kind, Auth: auth, Labels: strings.Fields(labels), Accept: decodeAccept(acc)}
		why := e.refusesRun(r, st.Project, st.Trigger.Event, sel)
		if why == "" {
			return "", nil
		}
		reasons[why]++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	want := "a runner"
	if sel != nil && sel.Pool != "" {
		want = "a runner of pool " + sel.Pool
	}
	if sel != nil && len(sel.Labels) > 0 {
		want += " with labels " + strings.Join(sel.Labels, ", ")
	}
	if seen == 0 {
		if sel == nil && !e.buildPoolsConfigured() {
			return "", nil // as before pools: nothing to say about them
		}
		return fmt.Sprintf("It needs %s, and no build runner has been seen in the last %d minutes.", want, int(RunnerOffline.Minutes())), nil
	}
	why := make([]string, 0, len(reasons))
	for k, n := range reasons {
		why = append(why, fmt.Sprintf("%s (%d)", k, n))
	}
	sort.Strings(why)
	return fmt.Sprintf("It needs %s, and none of the %d build runners seen in the last %d minutes may take it: %s.",
		want, seen, int(RunnerOffline.Minutes()), strings.Join(why, "; ")), nil
}

// persistPools writes the runners a new run's pipeline chose, so leasing can
// filter on them.
func (c *cmd) persistPools() error {
	for _, ev := range c.events {
		if ev.Type != RunCreated {
			continue
		}
		sel := c.st.Pipeline.Runner
		if sel == nil {
			return nil
		}
		_, err := c.tx.Exec(`UPDATE runs SET runner_pool = ?, runner_labels = ? WHERE id = ?`, sel.Pool, labelText(sel.Labels), c.st.ID)
		return err
	}
	return nil
}

// labelText writes labels as stored: sorted, separated by spaces.
func labelText(labels []string) string {
	s := slices.Clone(labels)
	slices.Sort(s)
	return strings.Join(slices.Compact(s), " ")
}

// decodeAccept reads what a runner said its accept: section allows.
func decodeAccept(s string) *accept.Config {
	if s == "" {
		return nil
	}
	var c accept.Config
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return nil
	}
	return &c
}
