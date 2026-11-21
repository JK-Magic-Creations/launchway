package engine

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/resources"
)

// Step resources (schema 22). A step may ask for CPU and memory
// (pipeline.Resources); what it leaves out comes from the installation's
// default for its kind, and what it may ask for is bounded by the
// installation's limits. Both are configuration, so they live in the
// database (step_resource_defaults, step_resource_limits), where every server
// of an installation reads the same values and an administrator changes them
// through the API, the CLI or the MCP server. The values compiled into the
// program (pipeline.SeedStepResources) seed the tables, once, when a server
// first finds them empty, and only on a fresh installation (see
// seedStepResources).
//
// A run's containers are sized when its pod is made, from the run's
// pipeline and the configuration at that moment (runContainers); nothing is
// written into the run's own record, so servers that do not know schema 22
// read such runs as before.

// schemaResources is the schema that brought step resources.
const schemaResources = pipeline.SchemaResources

// limitsRow is the name of the one row of step_resource_limits. The column
// is there so that organisations can have limits of their own later.
const limitsRow = "installation"

// Seeds of the configuration, used once to fill empty tables.
const (
	// seedMemoryPerThread: see resources.DefaultMemoryPerThread.
	seedMemoryPerThread = "1Gi"
)

// ResourceDefault is the default resources of one step kind.
type ResourceDefault struct {
	Kind string `json:"kind"`
	resources.Request
	UpdatedAt int64  `json:"updated_at"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// ResourceLimits are the most any step may ask for on this installation, and
// the memory one parallel job is assumed to need. Empty means not set (no
// maximum; the default for memory_per_thread).
type ResourceLimits struct {
	MaxCPU          string `json:"max_cpu"`
	MaxMemory       string `json:"max_memory"`
	MemoryPerThread string `json:"memory_per_thread"`
	UpdatedAt       int64  `json:"updated_at"`
	UpdatedBy       string `json:"updated_by,omitempty"`
}

// StepResourcesConfig is the installation's configuration of step resources.
type StepResourcesConfig struct {
	Defaults []ResourceDefault `json:"defaults"`
	Limits   ResourceLimits    `json:"limits"`
	// Kinds are the step kinds that have defaults: those that run in a
	// container Launchway sizes.
	Kinds []string `json:"kinds"`
}

// resourcePolicy is the configuration, in numbers.
type resourcePolicy struct {
	defaults map[string]resources.Amount
	limits   resources.Limits
}

// rowQuerier is what *dbx and *txx have in common.
type rowQuerier interface {
	Query(q string, a ...any) (*sql.Rows, error)
	QueryRow(q string, a ...any) *rowx
}

func (e *Engine) readStepResources(q rowQuerier) (StepResourcesConfig, error) {
	cfg := StepResourcesConfig{Defaults: []ResourceDefault{}, Kinds: slices.Clone(pipeline.ResourceKinds)}
	rows, err := q.Query(`SELECT kind, cpu, cpu_limit, memory, memory_limit, updated_at, updated_by FROM step_resource_defaults ORDER BY kind`)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	for rows.Next() {
		var d ResourceDefault
		if err := rows.Scan(&d.Kind, &d.CPU, &d.CPULimit, &d.Memory, &d.MemoryLimit, &d.UpdatedAt, &d.UpdatedBy); err != nil {
			return cfg, err
		}
		cfg.Defaults = append(cfg.Defaults, d)
	}
	if err := rows.Err(); err != nil {
		return cfg, err
	}
	err = q.QueryRow(`SELECT max_cpu, max_memory, memory_per_thread, updated_at, updated_by FROM step_resource_limits WHERE name = ?`, limitsRow).
		Scan(&cfg.Limits.MaxCPU, &cfg.Limits.MaxMemory, &cfg.Limits.MemoryPerThread, &cfg.Limits.UpdatedAt, &cfg.Limits.UpdatedBy)
	if err != nil && err != sql.ErrNoRows {
		return cfg, err
	}
	return cfg, nil
}

// policy turns the configuration into numbers. Rows that do not read (the
// tables were edited by hand) are ignored, with a warning: the run then gets
// what the namespace gives, as it did before step resources.
func (e *Engine) policy(cfg StepResourcesConfig) resourcePolicy {
	pol := resourcePolicy{defaults: map[string]resources.Amount{}}
	for _, d := range cfg.Defaults {
		a, err := d.Request.Parse()
		if err != nil {
			e.log.Warn("ignoring the default resources of a step kind that do not read", "kind", d.Kind, "err", err)
			continue
		}
		pol.defaults[d.Kind] = a
	}
	for _, f := range []struct {
		text string
		to   *int64
		fn   func(string) (int64, error)
		name string
	}{
		{cfg.Limits.MaxCPU, &pol.limits.MaxCPU, resources.ParseCPU, "max_cpu"},
		{cfg.Limits.MaxMemory, &pol.limits.MaxMemory, resources.ParseMemory, "max_memory"},
		{cfg.Limits.MemoryPerThread, &pol.limits.MemoryPerThread, resources.ParseMemory, "memory_per_thread"},
	} {
		if f.text == "" {
			continue
		}
		v, err := f.fn(f.text)
		if err != nil {
			e.log.Warn("ignoring a step resource limit that does not read", "limit", f.name, "err", err)
			continue
		}
		*f.to = v
	}
	return pol
}

// loadPolicy reads the configuration. A build limited to an older schema has
// no tables and so no policy: nothing is sized, as before.
func (e *Engine) loadPolicy(q rowQuerier) (resourcePolicy, error) {
	if !e.knows(schemaResources) {
		return resourcePolicy{defaults: map[string]resources.Amount{}}, nil
	}
	cfg, err := e.readStepResources(q)
	if err != nil {
		return resourcePolicy{}, err
	}
	return e.policy(cfg), nil
}

// seedStepResources fills empty configuration tables. A fresh installation,
// one that has never had a run, gets the values compiled into the program
// (pipeline.SeedStepResources). An installation that is upgraded to schema 22
// already has runs, and its pipelines must run as they did, so every kind is
// seeded with empty values: nothing is sized, and the namespace decides, as
// before. An administrator opts in with the recommended values (set-default
// --recommended) or with values of their own.
//
// It never changes a row that exists, so what an administrator set stays,
// even if it is empty. Servers starting together do not collide: each insert
// does nothing if the row exists.
func (e *Engine) seedStepResources() error {
	if !e.knows(schemaResources) {
		return nil
	}
	var anyRun int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM runs LIMIT 1) r`).Scan(&anyRun); err != nil {
		return err
	}
	fresh := anyRun == 0
	by := "seed"
	if !fresh {
		by = "upgrade"
	}
	unlock := e.writeLock()
	defer unlock()
	now := e.now().UnixMilli()
	for _, kind := range pipeline.ResourceKinds {
		var seed resources.Request
		if fresh {
			seed = pipeline.SeedStepResources[kind]
		}
		if _, err := e.db.Exec(`INSERT INTO step_resource_defaults(kind, cpu, cpu_limit, memory, memory_limit, updated_at, updated_by)
			VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(kind) DO NOTHING`,
			kind, seed.CPU, seed.CPULimit, seed.Memory, seed.MemoryLimit, now, by); err != nil {
			return err
		}
	}
	_, err := e.db.Exec(`INSERT INTO step_resource_limits(name, max_cpu, max_memory, memory_per_thread, updated_at, updated_by)
		VALUES(?, '', '', ?, ?, ?) ON CONFLICT(name) DO NOTHING`, limitsRow, seedMemoryPerThread, now, by)
	return err
}

// RecommendedStepResource is the default Launchway recommends for a step
// kind: what a fresh installation starts with. An administrator of an
// upgraded installation, which starts with nothing sized, sets it to opt in.
func (e *Engine) RecommendedStepResource(kind string) (resources.Request, error) {
	if !slices.Contains(pipeline.ResourceKinds, kind) {
		return resources.Request{}, fmt.Errorf("%w: %q has no default resources; the kinds that have are %s", ErrInvalid, kind, strings.Join(pipeline.ResourceKinds, ", "))
	}
	return pipeline.SeedStepResources[kind], nil
}

// StepResources returns the installation's configuration of step resources.
func (e *Engine) StepResources() (StepResourcesConfig, error) {
	if !e.knows(schemaResources) {
		return StepResourcesConfig{}, fmt.Errorf("%w: this server does not know step resources (schema %d)", ErrConflict, schemaResources)
	}
	return e.readStepResources(e.db)
}

// SetStepResourceDefault replaces the default resources of a step kind. It
// returns the row before and after. A default must read as quantities, its
// limits may not be below what it promises, and it may not be above the
// installation's limits.
func (e *Engine) SetStepResourceDefault(kind string, req resources.Request, actor string) (before, after ResourceDefault, err error) {
	if !e.knows(schemaResources) {
		return before, after, fmt.Errorf("%w: this server does not know step resources (schema %d)", ErrConflict, schemaResources)
	}
	if !slices.Contains(pipeline.ResourceKinds, kind) {
		return before, after, fmt.Errorf("%w: %q has no default resources: only steps that run a script (%s) run in a container Launchway sizes; "+
			"an image step is built by the builder, and deploy, rollout, rollback, verify and ai steps and approvals run in no container of the run's",
			ErrInvalid, kind, strings.Join(pipeline.ResourceKinds, ", "))
	}
	amount, perr := req.Parse()
	if perr != nil {
		return before, after, fmt.Errorf("%w: default resources of %s: %v", ErrInvalid, kind, perr)
	}
	// The default as a step gets it: what it promises, and what it may use.
	req = amount.Request()
	err = e.runWrite(func(tx *txx) error {
		cfg, err := e.readStepResources(tx)
		if err != nil {
			return err
		}
		pol := e.policy(cfg)
		if msg := violation(kind+" default", pol.limits, merged(amount)); msg != "" {
			return fmt.Errorf("%w: %s; raise the limit first, or ask for less", ErrInvalid, msg)
		}
		for _, d := range cfg.Defaults {
			if d.Kind == kind {
				before = d
			}
		}
		after = ResourceDefault{Kind: kind, Request: req, UpdatedAt: e.now().UnixMilli(), UpdatedBy: actor}
		_, err = tx.Exec(`INSERT INTO step_resource_defaults(kind, cpu, cpu_limit, memory, memory_limit, updated_at, updated_by)
			VALUES(?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(kind) DO UPDATE SET cpu = excluded.cpu, cpu_limit = excluded.cpu_limit, memory = excluded.memory,
				memory_limit = excluded.memory_limit, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			kind, req.CPU, req.CPULimit, req.Memory, req.MemoryLimit, after.UpdatedAt, actor)
		return err
	})
	return before, after, err
}

// SetStepResourceLimits replaces the installation's limits. No default may
// be above the new maximum: lower it first.
func (e *Engine) SetStepResourceLimits(l ResourceLimits, actor string) (before, after ResourceLimits, err error) {
	if !e.knows(schemaResources) {
		return before, after, fmt.Errorf("%w: this server does not know step resources (schema %d)", ErrConflict, schemaResources)
	}
	var pol resourcePolicy
	pol.defaults = map[string]resources.Amount{}
	for _, f := range []struct {
		name, text string
		to         *int64
		fn         func(string) (int64, error)
		out        *string
	}{
		{"max_cpu", l.MaxCPU, &pol.limits.MaxCPU, resources.ParseCPU, &l.MaxCPU},
		{"max_memory", l.MaxMemory, &pol.limits.MaxMemory, resources.ParseMemory, &l.MaxMemory},
		{"memory_per_thread", l.MemoryPerThread, &pol.limits.MemoryPerThread, resources.ParseMemory, &l.MemoryPerThread},
	} {
		if f.text == "" {
			continue
		}
		v, perr := f.fn(f.text)
		if perr != nil {
			return before, after, fmt.Errorf("%w: %s: %v", ErrInvalid, f.name, perr)
		}
		if f.name == "memory_per_thread" && v < 64<<20 {
			return before, after, fmt.Errorf("%w: memory_per_thread %s is below 64Mi; no parallel job runs in less", ErrInvalid, f.text)
		}
		*f.to = v
		if f.name == "max_cpu" {
			*f.out = resources.FormatCPU(v)
		} else {
			*f.out = resources.FormatMemory(v)
		}
	}
	err = e.runWrite(func(tx *txx) error {
		cfg, err := e.readStepResources(tx)
		if err != nil {
			return err
		}
		before = cfg.Limits
		for _, d := range e.policy(cfg).defaultsSorted() {
			if msg := violation(d.kind+" default", pol.limits, merged(d.amount)); msg != "" {
				return fmt.Errorf("%w: %s; lower the default first", ErrInvalid, msg)
			}
		}
		after = ResourceLimits{MaxCPU: l.MaxCPU, MaxMemory: l.MaxMemory, MemoryPerThread: l.MemoryPerThread, UpdatedAt: e.now().UnixMilli(), UpdatedBy: actor}
		_, err = tx.Exec(`INSERT INTO step_resource_limits(name, max_cpu, max_memory, memory_per_thread, updated_at, updated_by)
			VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET max_cpu = excluded.max_cpu, max_memory = excluded.max_memory,
				memory_per_thread = excluded.memory_per_thread, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			limitsRow, after.MaxCPU, after.MaxMemory, after.MemoryPerThread, after.UpdatedAt, actor)
		return err
	})
	return before, after, err
}

type kindAmount struct {
	kind   string
	amount resources.Amount
}

func (p resourcePolicy) defaultsSorted() []kindAmount {
	var out []kindAmount
	for k, a := range p.defaults {
		out = append(out, kindAmount{k, a})
	}
	slices.SortFunc(out, func(a, b kindAmount) int { return strings.Compare(a.kind, b.kind) })
	return out
}

// merged is what a step with nothing of its own gets from a default: the
// default, with a limit left out being the amount promised.
func merged(a resources.Amount) resources.Amount { return resources.Merge(resources.Amount{}, a) }

// runWrite runs fn in a transaction of its own, for changes to the
// configuration, which are rare and need no run.
func (e *Engine) runWrite(fn func(tx *txx) error) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// violation says, naming the limit, what of a is above it; "" if nothing is.
func violation(what string, l resources.Limits, a resources.Amount) string {
	vs := l.Check(a)
	if len(vs) == 0 {
		return ""
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = v.String()
	}
	return fmt.Sprintf("%s: %s", what, strings.Join(parts, "; "))
}

// limitSource says where a limit comes from, for messages.
const limitSource = "the installation's step resource limits, which an administrator sets (launchway step-resources set-limits, " +
	"or PUT /api/v1/step-resources/limits)"

// CheckResources checks what a pipeline's steps and containers ask for
// against the installation's limits, so that a run that could never get what
// it asks for is refused when it is created, not killed or rejected later.
// Every problem names the step, the value and the limit.
func (e *Engine) CheckResources(p *pipeline.Pipeline) pipeline.Problems {
	if !e.knows(schemaResources) {
		return nil
	}
	pol, err := e.loadPolicy(e.db)
	if err != nil {
		e.log.Error("reading the step resource limits", "err", err)
		return nil
	}
	return checkResources(p, pol.limits)
}

func checkResources(p *pipeline.Pipeline, l resources.Limits) pipeline.Problems {
	var out pipeline.Problems
	if l.MaxCPU == 0 && l.MaxMemory == 0 {
		return nil
	}
	for _, s := range p.Steps {
		if s.Resources == nil {
			continue
		}
		a, err := s.ResourceAmount()
		if err != nil {
			continue // the parser checked it
		}
		for _, v := range l.Check(a) {
			out = append(out, pipeline.Problem{Line: s.ResourcesLine(), Path: "steps." + s.ID + ".resources." + v.Field,
				Message: fmt.Sprintf("step %s asks for %s %s, which is above %s %s, the most a step may ask for: %s",
					s.ID, v.Field, v.Asked, v.Name, v.Limit, limitSource)})
		}
	}
	for _, c := range p.Containers {
		if c.Service {
			continue
		}
		a, err := resources.Request{CPU: c.CPU, Memory: c.Memory}.Parse()
		if err != nil {
			continue
		}
		for _, v := range l.Check(a) {
			out = append(out, pipeline.Problem{Path: "containers." + c.Name + "." + v.Field,
				Message: fmt.Sprintf("container %s asks for %s %s, which is above %s %s, the most a step may ask for: %s",
					c.Name, v.Field, v.Asked, v.Name, v.Limit, limitSource)})
		}
	}
	return out
}

// resourcesRefused fails a step, before any runner sees it, whose resources
// are above the installation's limits as they are now: they may have been
// lowered since the run was created. It holds the step to what it asked for
// (resources) and to what its container under containers asks for (cpu and
// memory, which existed before step resources), as the run's creation did,
// and never to a default, which is the installation's own and is not the
// step's fault. It reports whether it failed the step.
func (c *cmd) resourcesRefused(id string, def *pipeline.Step) bool {
	if !c.e.knows(schemaResources) || def.ResourceKind() == "" {
		return false
	}
	pol, err := c.e.loadPolicy(c.tx)
	if err != nil {
		c.err = err
		return false
	}
	declared, err := def.ResourceAmount()
	if err != nil {
		return false
	}
	who := "step " + id
	vs := pol.limits.Check(declared)
	if len(vs) == 0 {
		ct := c.st.Pipeline.Container(c.st.Pipeline.ContainerFor(def))
		if ct == nil || ct.Service {
			return false
		}
		legacy, err := resources.Request{CPU: ct.CPU, Memory: ct.Memory}.Parse()
		if err != nil {
			return false
		}
		who = "container " + ct.Name
		vs = pol.limits.Check(legacy)
	}
	if len(vs) == 0 {
		return false
	}
	v := vs[0]
	c.emit(StepFinished, Payload{Step: id, Outcome: string(StepFailed), Reason: fmt.Sprintf(
		"%s asks for %s %s, which is above %s %s, the most a step may ask for: %s", who, v.Field, v.Asked, v.Name, v.Limit, limitSource)})
	return true
}

// runContainers is the containers of the run as the runner is told to make
// them: the run's containers, each sized for the steps that run in it.
//
// A run's steps that use one image run in one container, which on
// Kubernetes is made once for the whole run, so a container gets the largest
// of what its steps ask for, and every step in it may use that much. A step
// that asks for nothing gets the installation's default for its kind; a
// container written under containers with its own cpu or memory (which
// existed before step resources) stands in for the default for the steps in
// it, and is passed on as it was written if none of them asks for anything.
// Services are not steps and are left as written.
func (c *cmd) runContainers() []pipeline.Container {
	cs := c.st.Pipeline.RunContainers()
	if !c.e.knows(schemaResources) {
		return cs
	}
	pol, err := c.e.loadPolicy(c.tx)
	if err != nil {
		// Without the policy the run still goes ahead, sized as it was
		// before step resources existed; the failure is logged.
		c.e.log.Error("reading the step resources configuration", "err", err)
		return cs
	}
	return sizeContainers(c.st.Pipeline, cs, pol)
}

func sizeContainers(p *pipeline.Pipeline, cs []pipeline.Container, pol resourcePolicy) []pipeline.Container {
	for i := range cs {
		ct := &cs[i]
		if ct.Service {
			continue
		}
		legacy, _ := resources.Request{CPU: ct.CPU, Memory: ct.Memory}.Parse()
		if legacy.Memory > 0 {
			legacy.MemoryLimit = legacy.Memory // as it was: what is asked for is also the cap
		}
		var folded resources.Amount
		declaredAny, any := false, false
		// What the container's CPU and memory come from: asked for by a
		// step or by the container's own cpu and memory, or the defaults.
		askedCPU, askedMemory := legacy.CPU > 0, legacy.Memory > 0
		for _, s := range p.Steps {
			kind := s.ResourceKind()
			if kind == "" || p.ContainerFor(s) != ct.Name {
				continue
			}
			any = true
			declared, err := s.ResourceAmount()
			if err != nil {
				continue
			}
			base := pol.defaults[kind]
			if !legacy.IsZero() {
				base = legacy
			}
			if s.Resources != nil {
				declaredAny = true
				askedCPU = askedCPU || declared.CPU > 0 || declared.CPULimit > 0
				askedMemory = askedMemory || declared.Memory > 0 || declared.MemoryLimit > 0
			}
			folded = resources.Max(folded, resources.Merge(declared, base))
		}
		switch {
		case !any || (!legacy.IsZero() && !declaredAny):
			folded = legacy
		default:
			folded = resources.Max(folded, legacy)
		}
		r := folded.Request()
		ct.CPU, ct.CPULimit, ct.Memory, ct.MemoryLimit = r.CPU, r.CPULimit, r.Memory, r.MemoryLimit
		ct.Threads = resources.Threads(folded.CPULimit, folded.MemoryLimit, pol.limits.MemoryPerThread)
		ct.ExplicitCPU, ct.ExplicitMemory = askedCPU, askedMemory
	}
	return cs
}

// stepLimits turns limits written as text into numbers and the canonical
// text, as SetStepResourceLimits does.
func stepLimits(l ResourceLimits) (resources.Limits, ResourceLimits, error) {
	var lim resources.Limits
	for _, f := range []struct {
		name string
		text *string
		to   *int64
		fn   func(string) (int64, error)
		fmt  func(int64) string
	}{
		{"max_cpu", &l.MaxCPU, &lim.MaxCPU, resources.ParseCPU, resources.FormatCPU},
		{"max_memory", &l.MaxMemory, &lim.MaxMemory, resources.ParseMemory, resources.FormatMemory},
		{"memory_per_thread", &l.MemoryPerThread, &lim.MemoryPerThread, resources.ParseMemory, resources.FormatMemory},
	} {
		if *f.text == "" {
			continue
		}
		v, err := f.fn(*f.text)
		if err != nil {
			return lim, l, fmt.Errorf("%w: %s: %v", ErrInvalid, f.name, err)
		}
		if f.name == "memory_per_thread" && v < 64<<20 {
			return lim, l, fmt.Errorf("%w: memory_per_thread %s is below 64Mi; no parallel job runs in less", ErrInvalid, *f.text)
		}
		*f.to, *f.text = v, f.fmt(v)
	}
	return lim, l, nil
}

// ValidateStepResources checks a whole configuration of step resources,
// as ReplaceStepResources would, without storing it: every quantity reads,
// a default's limits are not below what it promises, only kinds that run
// in a container Launchway sizes have a default, and no default is above
// the limits. It returns the configuration in its canonical spelling.
func ValidateStepResources(limits ResourceLimits, defaults map[string]resources.Request) (ResourceLimits, map[string]resources.Request, error) {
	lim, canon, err := stepLimits(limits)
	if err != nil {
		return limits, nil, err
	}
	out := map[string]resources.Request{}
	for kind, req := range defaults {
		if !slices.Contains(pipeline.ResourceKinds, kind) {
			return canon, nil, fmt.Errorf("%w: %q has no default resources: only steps that run a script (%s) run in a container Launchway sizes",
				ErrInvalid, kind, strings.Join(pipeline.ResourceKinds, ", "))
		}
		a, err := req.Parse()
		if err != nil {
			return canon, nil, fmt.Errorf("%w: default resources of %s: %v", ErrInvalid, kind, err)
		}
		if msg := violation(kind+" default", lim, merged(a)); msg != "" {
			return canon, nil, fmt.Errorf("%w: %s; raise the limit or ask for less", ErrInvalid, msg)
		}
		out[kind] = a.Request()
	}
	return canon, out, nil
}

// ReplaceStepResources makes the configuration exactly this: the limits,
// and the defaults of the kinds listed (a kind not listed has none). It is
// how a change to the configuration object "step-resources" reaches the
// tables the engine reads, on every server; it changes nothing, and
// reports that, when the tables already say so.
func (e *Engine) ReplaceStepResources(limits ResourceLimits, defaults map[string]resources.Request, actor string) (changed bool, err error) {
	if !e.knows(schemaResources) {
		return false, fmt.Errorf("%w: this server does not know step resources (schema %d)", ErrConflict, schemaResources)
	}
	canon, canonDefaults, err := ValidateStepResources(limits, defaults)
	if err != nil {
		return false, err
	}
	err = e.runWrite(func(tx *txx) error {
		cur, err := e.readStepResources(tx)
		if err != nil {
			return err
		}
		now := e.now().UnixMilli()
		have := map[string]resources.Request{}
		for _, d := range cur.Defaults {
			have[d.Kind] = d.Request
		}
		for _, kind := range pipeline.ResourceKinds {
			want, wanted := canonDefaults[kind]
			if !wanted {
				want = resources.Request{}
			}
			if old, ok := have[kind]; ok && old == want {
				continue
			}
			changed = true
			if _, err := tx.Exec(`INSERT INTO step_resource_defaults(kind, cpu, cpu_limit, memory, memory_limit, updated_at, updated_by)
				VALUES(?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(kind) DO UPDATE SET cpu = excluded.cpu, cpu_limit = excluded.cpu_limit, memory = excluded.memory,
					memory_limit = excluded.memory_limit, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
				kind, want.CPU, want.CPULimit, want.Memory, want.MemoryLimit, now, actor); err != nil {
				return err
			}
		}
		if cur.Limits.MaxCPU != canon.MaxCPU || cur.Limits.MaxMemory != canon.MaxMemory || cur.Limits.MemoryPerThread != canon.MemoryPerThread {
			changed = true
			if _, err := tx.Exec(`INSERT INTO step_resource_limits(name, max_cpu, max_memory, memory_per_thread, updated_at, updated_by)
				VALUES(?, ?, ?, ?, ?, ?)
				ON CONFLICT(name) DO UPDATE SET max_cpu = excluded.max_cpu, max_memory = excluded.max_memory,
					memory_per_thread = excluded.memory_per_thread, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
				limitsRow, canon.MaxCPU, canon.MaxMemory, canon.MemoryPerThread, now, actor); err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

// StepResourcesKnown reports whether this server's database has the step
// resources tables (schema 22).
func (e *Engine) StepResourcesKnown() bool { return e.knows(schemaResources) }
