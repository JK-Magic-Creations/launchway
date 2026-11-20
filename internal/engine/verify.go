package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/verify"
)

// Verifications (schema 19).
//
// A verify step watches an environment for a window of intervals. When its
// needs are met, the server works out everything the decision depends on
// and records it in verify.started: the environment and target, when the
// first interval starts, the parameters' values (facts about the deployment
// as deploy runners last observed them included), the spec the statistics
// judge by, and the baseline: what earlier passed verifications of the same
// environment, project and service saw. The step is then queued for a deploy
// runner serving the target, which reads each interval's metrics and logs
// from the health sources its operator configured once the interval is over,
// and reports them (VerifyInterval). Each report is a verify.interval event;
// every server folds the same events with verify.Evaluate and so reaches the
// same verdict. When the verdict is final (the window is over, or a check
// failed early) the runner is told, may ask a model to explain it, and
// finishes the attempt; the server records verify.verdict and acts:
// succeeds, fails (aborting the deployment's rollout first, with on_fail:
// abort or rollback), or waits for a person (on_inconclusive: pause).
//
// A runner that stops is replaced as for any lease; the intervals already
// recorded count, so the next runner reads only the rest. A person may mark
// the verification passed or failed at any time before it ends
// (verify.override), with the rules of commands on protected environments.

// WaitVerify: a verify step that ended inconclusive with on_inconclusive:
// pause waits for a person to mark it passed or failed.
const WaitVerify = "verify"

// OriginVerify is the origin of an abort a failed verification gave.
const OriginVerify = "verify"

// HealthSource is a health source as servers know it: a name, a type and
// the projects that may use it. Where it is and how to reach it is the
// deploy runners' configuration.
type HealthSource struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Projects []string `json:"projects"`
}

// HealthSourceTypes are the types a health source may have.
var HealthSourceTypes = []string{"prometheus", "loki", "clickhouse", "elasticsearch", "opensearch", "postgres", "http"}

// Allows reports whether a project may use the source.
func (h HealthSource) Allows(project string) bool {
	for _, p := range h.Projects {
		if ok, _ := path.Match(p, project); ok {
			return true
		}
	}
	return false
}

// queryLanguage reports whether pipelines write the source's queries
// themselves (true) or name statements its operator wrote (false).
func queryLanguage(typ string) bool {
	switch typ {
	case "prometheus", "loki", "elasticsearch", "opensearch":
		return true
	}
	return false
}

func checkHealthSources(list []HealthSource) (map[string]HealthSource, error) {
	out := map[string]HealthSource{}
	for _, h := range list {
		switch {
		case !validName(h.Name):
			return nil, fmt.Errorf("verification.sources: %q: use lower-case letters, digits and hyphens", h.Name)
		case out[h.Name].Name != "":
			return nil, fmt.Errorf("verification.sources: %s is listed twice", h.Name)
		case !slices.Contains(HealthSourceTypes, h.Type):
			return nil, fmt.Errorf("verification.sources: %s: type %q: use one of %s", h.Name, h.Type, strings.Join(HealthSourceTypes, ", "))
		case len(h.Projects) == 0:
			return nil, fmt.Errorf("verification.sources: %s: list the projects that may use it under projects (patterns such as shop-* work)", h.Name)
		}
		for _, p := range h.Projects {
			if _, err := path.Match(p, ""); err != nil {
				return nil, fmt.Errorf("verification.sources: %s: %q is not a valid pattern", h.Name, p)
			}
		}
		out[h.Name] = h
	}
	return out, nil
}

// HealthSources returns the configured health sources, by name.
func (e *Engine) HealthSources() []HealthSource {
	out := make([]HealthSource, 0, len(e.live().sources))
	for _, h := range e.live().sources {
		out = append(out, h)
	}
	slices.SortFunc(out, func(a, b HealthSource) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// VerifyEvent carries the details of the verify.* events.
type VerifyEvent struct {
	// verify.started
	Environment string            `json:"environment,omitempty"`
	Service     string            `json:"service,omitempty"`
	Target      string            `json:"target,omitempty"`
	Deployment  string            `json:"deployment,omitempty"` // the deployment watched, if any (dep_...)
	Version     string            `json:"version,omitempty"`
	Start       int64             `json:"start,omitempty"` // the first interval's start (Unix ms)
	Deadline    int64             `json:"deadline,omitempty"`
	Spec        *verify.Spec      `json:"spec,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
	Baseline    *verify.Baseline  `json:"baseline,omitempty"`
	// verify.interval
	Interval *verify.Interval `json:"interval,omitempty"`
	// verify.verdict
	Verdict     *verify.Verdict `json:"verdict,omitempty"`
	Explanation *Explanation    `json:"explanation,omitempty"`
	Pause       bool            `json:"pause,omitempty"` // it now waits for a person
	// verify.override: passed or failed (Actor and Comment are the
	// payload's).
	Outcome string `json:"outcome,omitempty"`
}

// Explanation is what a model said about a verdict (or a failed step). It
// is untrusted text, shown as such; it decides nothing unless the pipeline
// asked for decide: model-assisted, and then only towards failing.
type Explanation struct {
	Model      string `json:"model"`
	Summary    string `json:"summary,omitempty"`
	Cause      string `json:"cause,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
	// Outcome is what the model thinks: passed, failed or inconclusive.
	Outcome string `json:"outcome,omitempty"`
	// Changed says the model turned the statistics' pass into something
	// else (decide: model-assisted).
	Changed bool     `json:"changed,omitempty"`
	Error   string   `json:"error,omitempty"` // why there is no explanation
	Usage   *AIUsage `json:"usage,omitempty"`
}

// AIUsage is what one or more model calls used.
type AIUsage struct {
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
}

// VerifyState is a verify step's verification, as its events describe it.
type VerifyState struct {
	Environment, Service, Target, Deployment, Version string
	Start, Deadline                                   int64
	Spec                                              verify.Spec
	Params                                            map[string]string
	Baseline                                          verify.Baseline
	Intervals                                         []verify.Interval
	Verdict                                           *verify.Verdict
	Explanation                                       *Explanation
	Override, OverrideBy, OverrideComment             string
	StartedAt                                         int64
}

func (v *VerifyState) has(index int) bool {
	for _, iv := range v.Intervals {
		if iv.Index == index {
			return true
		}
	}
	return false
}

// verdict is what the intervals so far say.
func (v *VerifyState) verdict() verify.Verdict {
	return verify.Evaluate(v.Spec, v.Baseline, v.Intervals)
}

// applyVerify folds the verify.* events.
func (st *RunState) applyVerify(ev Event, s *StepState) {
	d := ev.Data.Verify
	if d == nil || s == nil {
		return
	}
	switch ev.Type {
	case VerifyStarted:
		v := &VerifyState{Environment: d.Environment, Service: d.Service, Target: d.Target, Deployment: d.Deployment, Version: d.Version,
			Start: d.Start, Deadline: d.Deadline, Params: d.Params, StartedAt: ev.At}
		if d.Spec != nil {
			v.Spec = *d.Spec
		}
		if d.Baseline != nil {
			v.Baseline = *d.Baseline
		}
		s.Ver = v
		s.Deadline = d.Deadline
		if st.Status == RunQueued {
			st.Status, st.StartedAt = RunRunning, ev.At
		}
	case VerifyInterval:
		if s.Ver != nil && d.Interval != nil && !s.Ver.has(d.Interval.Index) {
			s.Ver.Intervals = append(s.Ver.Intervals, *d.Interval)
		}
	case VerifyVerdict:
		if s.Ver != nil {
			s.Ver.Verdict, s.Ver.Explanation = d.Verdict, d.Explanation
		}
		if d.Pause {
			s.Status = StepWaitingS
			s.WaitKind = WaitVerify
			s.LeaseID = ""
		}
	case VerifyOverride:
		if s.Ver != nil {
			s.Ver.Override, s.Ver.OverrideBy, s.Ver.OverrideComment = d.Outcome, ev.Data.Actor, ev.Data.Comment
		}
	}
}

// ---- starting, in advance ----

// verifyRefused says why verify steps may not run in this run, or "".
func (st *RunState) verifyRefused() string {
	switch {
	case st.Source.Kind != "git":
		return "a verify step is done by a deploy runner, so it runs only on a server, not with `launchway run`"
	case !st.secretsAllowed() && st.Draft != nil:
		return "verify steps read health sources with the deploy runner's credentials, so they do not run in draft runs unless the drafts setting allows secrets"
	case !st.secretsAllowed():
		return "verify steps read health sources with the deploy runner's credentials, so they do not run in pull-request runs " +
			"unless the project allows secrets for pull requests (pull_requests.secrets)"
	}
	return ""
}

// CheckVerify is the check of a pipeline's verify and ai steps against the
// server's configuration: environments, health sources and models exist and
// allow the project (unless project is empty), and queries put parameters
// only where they are safe.
func (e *Engine) CheckVerify(project string, p *pipeline.Pipeline) pipeline.Problems {
	var out pipeline.Problems
	add := func(s *pipeline.Step, path, format string, a ...any) {
		out = append(out, pipeline.Problem{Line: s.Line, Path: "steps." + s.ID + path, Message: s.ID + ": " + fmt.Sprintf(format, a...)})
	}
	for _, s := range p.Steps {
		switch {
		case s.IsVerify() && s.Verify != nil:
			if s.Deployment == "" {
				env, ok := e.live().envs[s.Environment]
				switch {
				case !ok:
					add(s, ".environment", "environment %q is not configured on this server%s", s.Environment, knownList(envNames(e.live().envs)))
				case project != "" && !env.Allows(project):
					add(s, ".environment", "project %s may not use environment %s (see the environment's projects in the server's configuration)", project, s.Environment)
				}
			}
			type q struct {
				name, source, query, stable, statement string
				line                                   int
			}
			var qs []q
			for _, m := range s.Verify.Metrics {
				qs = append(qs, q{m.Name, m.Source, m.Query, m.StableQuery, m.Statement, m.Line})
			}
			for _, l := range s.Verify.Logs {
				qs = append(qs, q{l.Name, l.Source, l.Query, l.StableQuery, l.Statement, l.Line})
			}
			declared := append(slices.Sorted(mapKeys(s.Verify.Params)), pipeline.ReservedParams...)
			for _, x := range qs {
				h, ok := e.live().sources[x.source]
				where := func(format string, a ...any) {
					out = append(out, pipeline.Problem{Line: x.line, Path: "steps." + s.ID, Message: s.ID + ": " + x.name + ": " + fmt.Sprintf(format, a...)})
				}
				switch {
				case !ok && len(e.live().sources) == 0:
					where("this server has no health sources configured (verification.sources), so it cannot read %s", x.source)
				case !ok:
					where("health source %q is not configured on this server%s", x.source, knownList(sourceNames(e.live().sources)))
				case project != "" && !h.Allows(project):
					where("project %s may not use health source %s (see its projects in the server's configuration)", project, x.source)
				case queryLanguage(h.Type) && x.query == "":
					where("%s is a %s source: write the query (statement is for SQL and HTTP sources, whose operator writes the queries)", x.source, h.Type)
				case !queryLanguage(h.Type) && x.query != "":
					where("%s is a %s source: pipelines cannot write its queries; name one its operator wrote with statement: <name>", x.source, h.Type)
				default:
					for _, text := range []string{x.query, x.stable} {
						if text == "" {
							continue
						}
						if err := checkQuery(h.Type, text, declared); err != nil {
							where("%v", err)
						}
					}
				}
			}
			if m := s.Verify.Model; m != "" {
				out = append(out, e.checkModel(project, s, m)...)
			}
		case s.IsAI() && s.AI != nil:
			out = append(out, e.checkModel(project, s, s.AI.Model)...)
			if s.AI.Schema != "" {
				if err := checkSchema(s.AI.Schema); err != nil {
					add(s, ".schema", "%v", err)
				}
			}
			if m, ok := e.model(s.AI.Model); ok {
				if s.AI.Temperature != nil && !m.SupportsTemperature() {
					add(s, ".temperature", "model %s (%s) does not take a temperature; take it out", m.Name, m.Provider)
				}
			}
		}
	}
	return out
}

func knownList(names string) string {
	if names == "" {
		return ""
	}
	return "; it knows " + names
}

func sourceNames(m map[string]HealthSource) string {
	return strings.Join(slices.Sorted(mapKeys(m)), ", ")
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// startVerify moves a verify step whose needs are met to its first
// interval: it works out and records everything the decision depends on,
// and queues the step for a deploy runner.
func (c *cmd) startVerify(id string, def *pipeline.Step) {
	st := c.st
	s := st.Steps[id]
	if why := st.verifyRefused(); why != "" {
		c.failStep(id, why)
		return
	}
	vf := def.Verify
	ve := &VerifyEvent{}
	var dep *DeployState
	if def.Deployment != "" {
		d := st.Steps[def.Deployment]
		switch {
		case d == nil || !d.Dep.Opened():
			c.failStep(id, "its deployment was never made")
			return
		case d.Dep.Closed:
			c.failStep(id, "its deployment is already over")
			return
		}
		dep = d.Dep
		ve.Environment, ve.Service, ve.Deployment, ve.Version = dep.Environment, dep.Service, dep.ID, dep.Version
	} else {
		ve.Environment, ve.Service = def.Environment, def.Service
		if ve.Service == "" {
			ve.Service = st.Project
		}
	}
	env, ok := c.e.live().envs[ve.Environment]
	switch {
	case !ok:
		c.failStep(id, fmt.Sprintf("environment %s is not configured on this server", ve.Environment))
		return
	case !env.Allows(st.Project):
		c.failStep(id, fmt.Sprintf("project %s may not use environment %s", st.Project, env.Name))
		return
	}
	ve.Target = env.Target
	if ps := c.e.CheckVerify(st.Project, &pipeline.Pipeline{Steps: []*pipeline.Step{def}}); len(ps) > 0 {
		c.failStep(id, ps[0].Message)
		return
	}
	params, err := c.verifyParams(def, dep)
	if err != nil {
		c.failStep(id, err.Error())
		return
	}
	if ve.Version == "" {
		ve.Version = st.Trigger.SHA[:min(len(st.Trigger.SHA), 8)]
	}
	ve.Params = params
	spec := verifySpec(def)
	ve.Spec = &spec
	if vf.Baseline > 0 {
		b, err := c.loadBaseline(ve.Environment, st.Project, ve.Service, vf.Baseline)
		if err != nil {
			c.dbFail(err)
			return
		}
		ve.Baseline = b
	}
	ve.Start = c.now + vf.Delay.D().Milliseconds()
	ve.Deadline = c.now + def.Timeout.D().Milliseconds()
	c.emit(VerifyStarted, Payload{Step: id, Verify: ve})
	c.emit(StepQueued, Payload{Step: id, Attempt: s.Attempt + 1})
}

// verifySpec is what the statistics judge a verify step by.
func verifySpec(def *pipeline.Step) verify.Spec {
	vf := def.Verify
	spec := verify.Spec{Sensitivity: vf.Sensitivity, IntervalMS: vf.Interval.D().Milliseconds(),
		Intervals: int(vf.Duration.D() / vf.Interval.D())}
	for _, m := range vf.Metrics {
		spec.Metrics = append(spec.Metrics, verify.MetricSpec{Name: m.Name, Direction: m.Direction, Max: m.Max, Min: m.Min,
			FailFastMax: m.FailFastMax, FailFastMin: m.FailFastMin, Stable: m.StableQuery != "", Baseline: !m.NoBaseline && vf.Baseline > 0,
			Failures: m.Failures, MinIntervals: m.MinIntervals, MinPoints: m.MinPoints})
	}
	for _, l := range vf.Logs {
		spec.Logs = append(spec.Logs, verify.LogSpec{Name: l.Name, Errors: l.Errors, Stable: l.StableQuery != "", Baseline: !l.NoBaseline && vf.Baseline > 0})
	}
	return spec
}

// verifyParams works out a verify step's parameters.
func (c *cmd) verifyParams(def *pipeline.Step, dep *DeployState) (map[string]string, error) {
	out := map[string]string{}
	var row *deploymentRow
	for _, name := range slices.Sorted(mapKeys(def.Verify.Params)) {
		v := def.Verify.Params[name]
		if v.Fact == "" {
			val, err := c.resolve(def, v)
			if err != nil {
				return nil, fmt.Errorf("params.%s: %w", name, err)
			}
			if strings.ContainsAny(val, "\r\n") {
				return nil, fmt.Errorf("params.%s: the value is more than one line", name)
			}
			out[name] = val
			continue
		}
		if dep == nil {
			return nil, fmt.Errorf("params.%s: { deployment: %s } needs a deployment", name, v.Fact)
		}
		deployDef := c.st.Pipeline.Step(c.st.deploymentStepID(dep.ID))
		val := ""
		switch v.Fact {
		case "environment":
			val = dep.Environment
		case "service":
			val = dep.Service
		case "application":
			val = dep.Application
		case "version":
			val = dep.Version
		case "previous_version":
			if dep.Previous != nil {
				val = dep.Previous.Version
			}
			if val == "" {
				return nil, fmt.Errorf("params.%s: Launchway does not know what the environment ran before this deployment", name)
			}
		case "namespace", "rollout":
			if deployDef == nil || deployDef.Rollout == nil {
				if v.Fact == "namespace" && deployDef != nil && deployDef.ArgoCD != nil {
					for _, r := range deployDef.ArgoCD.Sync.Resources {
						if r.Namespace != "" {
							val = r.Namespace
							break
						}
					}
				}
				if val == "" {
					return nil, fmt.Errorf("params.%s: the deployment names no rollout, so its %s is not known", name, v.Fact)
				}
			} else if v.Fact == "namespace" {
				val = deployDef.Rollout.Namespace
			} else {
				val = deployDef.Rollout.Name
			}
		case "canary_hash", "stable_hash":
			if row == nil {
				r, err := c.deploymentRow(dep.ID)
				if err != nil {
					return nil, err
				}
				row = &r
			}
			ro := row.snap.Rollout
			if v.Fact == "canary_hash" {
				val = row.hash
				if val == "" && ro != nil {
					val = ro.CurrentPodHash
				}
			} else if ro != nil {
				val = ro.StableRS
			}
			if val == "" {
				return nil, fmt.Errorf("params.%s: the rollout's %s is not known yet: no deploy runner has observed it", name, strings.ReplaceAll(v.Fact, "_", " "))
			}
		}
		out[name] = val
	}
	return out, nil
}

// deploymentStepID names the deploy step that made a deployment.
func (st *RunState) deploymentStepID(depID string) string {
	id, _ := st.deploymentStep(depID)
	return id
}

// baselineData is what a passed verification keeps for later ones.
type baselineData struct {
	Metrics map[string][]float64          `json:"metrics,omitempty"`
	Logs    map[string]verify.BaselineLog `json:"logs,omitempty"`
}

// loadBaseline reads the newest n passed verifications of a unit and merges
// them.
func (c *cmd) loadBaseline(env, project, service string, n int) (*verify.Baseline, error) {
	rows, err := c.tx.Query(`SELECT run_id, step_id, run_number, version, data, created_at FROM verify_baselines
		WHERE environment = ? AND project = ? AND service = ? ORDER BY created_at DESC, run_id DESC LIMIT ?`, env, project, service, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recs []verify.BaselineRecord
	for rows.Next() {
		var r verify.BaselineRun
		var data string
		if err := rows.Scan(&r.Run, &r.Step, &r.Number, &r.Version, &data, &r.At); err != nil {
			return nil, err
		}
		var d baselineData
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			continue // a row no one can read is no baseline
		}
		recs = append(recs, verify.BaselineRecord{Run: r, Metrics: d.Metrics, Logs: d.Logs})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	b := verify.MergeBaselines(recs)
	return &b, nil
}

// ---- deploy runners: leasing, intervals ----

// VerifyJob is what a deploy runner needs to read a verification's
// intervals.
type VerifyJob struct {
	Environment string            `json:"environment"`
	Service     string            `json:"service"`
	Target      string            `json:"target"`
	Deployment  string            `json:"deployment,omitempty"`
	Start       int64             `json:"start"`
	IntervalMS  int64             `json:"interval_ms"`
	Intervals   int               `json:"intervals"`
	Done        []int             `json:"done,omitempty"` // intervals already recorded
	Spec        verify.Spec       `json:"spec"`
	Metrics     []VerifyQuery     `json:"metrics,omitempty"`
	Logs        []VerifyQuery     `json:"logs,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
	Deadline    int64             `json:"deadline"`
	// Model, when the step names one, is the model to ask for an
	// explanation once the verdict is final.
	Model  *ModelJob `json:"model,omitempty"`
	Decide string    `json:"decide,omitempty"`
	// ModelNote says why the model may not be asked now (its budget, say).
	ModelNote string `json:"model_note,omitempty"`
}

// VerifyQuery is one metric or log query.
type VerifyQuery struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Query       string `json:"query,omitempty"`
	StableQuery string `json:"stable_query,omitempty"`
	Statement   string `json:"statement,omitempty"`
	Index       string `json:"index,omitempty"`
	StepMS      int64  `json:"step_ms,omitempty"`
	MaxLines    int    `json:"max_lines,omitempty"`
}

// VerifyResult is what a deploy runner adds when it finishes a
// verification: a model's explanation, if the step named a model.
type VerifyResult struct {
	Explanation *Explanation `json:"explanation,omitempty"`
}

// VerifyAck answers an interval report: the verdict so far, and whether it
// is final (the runner then stops reading and finishes).
type VerifyAck struct {
	Final   bool           `json:"final"`
	Verdict verify.Verdict `json:"verdict"`
}

// LeaseVerify hands the oldest queued verify step of the given targets to
// a deploy runner, waiting up to wait for one.
func (e *Engine) LeaseVerify(ctx context.Context, r RunnerInfo, targets []string, wait time.Duration) (*Job, error) {
	if !e.knows(schemaVerify) {
		return nil, fmt.Errorf("%w: this server does not know verify steps", ErrConflict)
	}
	deadline := time.Now().Add(wait)
	for {
		ch := e.waitChan()
		select {
		case <-e.stop:
			return nil, ErrClosed
		default:
		}
		e.touchRunner(r)
		job, err := e.tryLeaseVerify(r, targets)
		if err != nil || job != nil {
			return job, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, nil
		}
		if e.db.pg {
			left = min(left, recheckEvery)
		}
		t := time.NewTimer(left)
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-e.stop:
			t.Stop()
			return nil, ErrClosed
		}
	}
}

func (e *Engine) tryLeaseVerify(r RunnerInfo, targets []string) (*Job, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	args := []any{}
	for _, t := range targets {
		args = append(args, t)
	}
	args = append(args, e.schema)
	rows, err := e.db.Query(`SELECT s.run_id, s.step_id, s.attempt FROM steps s JOIN runs ON runs.id = s.run_id
		WHERE s.status = 'queued' AND s.target IN (`+inList(len(targets))+`) AND runs.min_schema <= ?
		ORDER BY s.queued_at, s.run_id, s.position LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	type cand struct {
		run, step string
		attempt   int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.run, &c.step, &c.attempt); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, cd := range cands {
		var job *Job
		err := e.withRun(cd.run, func(c *cmd) error {
			job = nil
			s := c.st.Steps[cd.step]
			def := c.st.Pipeline.Step(cd.step)
			if s == nil || def == nil || !def.IsVerify() || s.Status != StepQueuedS || s.Attempt != cd.attempt ||
				c.st.CancelRequested || s.Ver == nil || !slices.Contains(targets, s.Ver.Target) {
				return nil
			}
			leaseID := ids.New("lease")
			if _, err := c.tx.Exec(`INSERT INTO leases(id, run_id, step_id, attempt, runner, expires_at) VALUES(?,?,?,?,?,?)`,
				leaseID, cd.run, cd.step, s.Attempt, r.ID, c.leaseUntil()); err != nil {
				return err
			}
			c.emit(StepLeased, Payload{Step: cd.step, Attempt: s.Attempt, Lease: leaseID, Runner: r.ID})
			job = c.verifyJob(cd.step, leaseID)
			return nil
		})
		if errors.Is(err, ErrNeedsNewer) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if job != nil {
			return job, nil
		}
	}
	return nil, nil
}

// verifyJob builds a deploy runner's job for a leased verify step.
func (c *cmd) verifyJob(stepID, leaseID string) *Job {
	st := c.st
	def := st.Pipeline.Step(stepID)
	s := st.Steps[stepID]
	v := s.Ver
	vj := &VerifyJob{Environment: v.Environment, Service: v.Service, Target: v.Target, Deployment: v.Deployment, Start: v.Start,
		IntervalMS: v.Spec.IntervalMS, Intervals: v.Spec.Intervals, Spec: v.Spec, Params: v.Params, Deadline: v.Deadline,
		Decide: def.Verify.Decide}
	for _, iv := range v.Intervals {
		vj.Done = append(vj.Done, iv.Index)
	}
	slices.Sort(vj.Done)
	for _, m := range def.Verify.Metrics {
		vj.Metrics = append(vj.Metrics, VerifyQuery{Name: m.Name, Source: m.Source, Query: m.Query, StableQuery: m.StableQuery,
			Statement: m.Statement, Index: m.Index, StepMS: m.Step.D().Milliseconds()})
	}
	for _, l := range def.Verify.Logs {
		vj.Logs = append(vj.Logs, VerifyQuery{Name: l.Name, Source: l.Source, Query: l.Query, StableQuery: l.StableQuery,
			Statement: l.Statement, Index: l.Index, MaxLines: l.MaxLines})
	}
	if def.Verify.Model != "" {
		vj.Model, vj.ModelNote = c.modelJob(def.Verify.Model, 0)
	}
	trig := st.Trigger
	return &Job{LeaseID: leaseID, LeaseTTL: pipeline.Duration(c.e.ttl), RunID: st.ID, Project: st.Project, RunNumber: st.Number,
		Pipeline: st.Pipeline.Name, StepID: stepID, Attempt: s.Attempt, SHA: trig.SHA, Timeout: def.Timeout, Source: st.Source, Verify: vj}
}

// VerifyInterval records what a deploy runner read in one interval of the
// verification its lease is for, and says what the intervals so far
// decide. A report of an interval already recorded changes nothing.
func (e *Engine) VerifyInterval(leaseID string, iv *verify.Interval) (*VerifyAck, error) {
	if !e.knows(schemaVerify) {
		return nil, fmt.Errorf("%w: this server does not know verify steps", ErrConflict)
	}
	l, err := e.findLease(leaseID)
	if err != nil {
		return nil, err
	}
	var ack *VerifyAck
	err = e.withRun(l.runID, func(c *cmd) error {
		ack = nil
		s := c.st.Steps[l.stepID]
		def := c.st.Pipeline.Step(l.stepID)
		if s == nil || def == nil || !def.IsVerify() || s.LeaseID != leaseID || !s.Status.Active() || s.Ver == nil {
			return ErrLeaseLost
		}
		v := s.Ver
		if err := checkInterval(v, iv, c.now); err != nil {
			return err
		}
		if err := c.renewLease(leaseID); err != nil {
			return err
		}
		if iv != nil && !v.has(iv.Index) {
			c.emit(VerifyInterval, Payload{Step: l.stepID, Attempt: s.Attempt, Verify: &VerifyEvent{Interval: iv}})
		}
		vd := v.verdict()
		ack = &VerifyAck{Final: vd.Final, Verdict: vd}
		return nil
	})
	return ack, err
}

// checkInterval checks an interval report against the verification and
// cuts it to the limits: the runner keeps to them, and the server does not
// rely on that.
func checkInterval(v *VerifyState, iv *verify.Interval, now int64) error {
	if iv == nil {
		return nil // only asking what the intervals so far decide
	}
	sp := v.Spec
	switch {
	case iv.Index < 0 || iv.Index >= sp.Intervals:
		return fmt.Errorf("%w: interval %d: the window has %d intervals", ErrInvalid, iv.Index, sp.Intervals)
	case iv.Start != v.Start+int64(iv.Index)*sp.IntervalMS || iv.End != iv.Start+sp.IntervalMS:
		return fmt.Errorf("%w: interval %d is from %d to %d, not %d to %d", ErrInvalid, iv.Index,
			v.Start+int64(iv.Index)*sp.IntervalMS, v.Start+int64(iv.Index+1)*sp.IntervalMS, iv.Start, iv.End)
	case iv.End > now+5_000:
		return fmt.Errorf("%w: interval %d is not over yet (it ends in %ds)", ErrConflict, iv.Index, (iv.End-now)/1000)
	}
	metrics := map[string]bool{}
	for _, m := range sp.Metrics {
		metrics[m.Name] = true
	}
	logs := map[string]bool{}
	for _, l := range sp.Logs {
		logs[l.Name] = true
	}
	var ms []verify.MetricData
	for _, m := range iv.Metrics {
		if !metrics[m.Name] {
			return fmt.Errorf("%w: interval %d: there is no metric %q", ErrInvalid, iv.Index, m.Name)
		}
		metrics[m.Name] = false // once
		m.Canary = clipPoints(m.Canary, iv.Start, iv.End)
		m.Stable = clipPoints(m.Stable, iv.Start, iv.End)
		m.Error = clipText(m.Error, verify.MaxErrorBytes)
		ms = append(ms, m)
	}
	var ls []verify.LogData
	for _, l := range iv.Logs {
		if !logs[l.Name] {
			return fmt.Errorf("%w: interval %d: there is no log query %q", ErrInvalid, iv.Index, l.Name)
		}
		logs[l.Name] = false
		l.Clusters = clipClusters(l.Clusters)
		l.Stable = clipClusters(l.Stable)
		l.Lines, l.StableN = max(l.Lines, 0), max(l.StableN, 0)
		l.Error = clipText(l.Error, verify.MaxErrorBytes)
		ls = append(ls, l)
	}
	iv.Metrics, iv.Logs = ms, ls
	return nil
}

func clipPoints(ps []verify.Point, start, end int64) []verify.Point {
	var out []verify.Point
	for _, p := range ps {
		if p.T < start-60_000 || p.T > end+60_000 { // a little slack for the source's alignment
			continue
		}
		out = append(out, p)
		if len(out) == verify.MaxPointsPerSeries {
			break
		}
	}
	return out
}

func clipClusters(cs []verify.Cluster) []verify.Cluster {
	if len(cs) > verify.MaxClusters {
		cs = cs[:verify.MaxClusters]
	}
	out := make([]verify.Cluster, 0, len(cs))
	for _, c := range cs {
		c.Template = clipText(c.Template, verify.MaxTemplateBytes)
		c.Count = max(c.Count, 0)
		if len(c.Samples) > verify.MaxSamples {
			c.Samples = c.Samples[:verify.MaxSamples]
		}
		for i := range c.Samples {
			c.Samples[i] = clipText(strings.ToValidUTF8(c.Samples[i], "?"), verify.MaxSampleBytes)
		}
		out = append(out, c)
	}
	return out
}

// finishVerify records how a deploy runner's verification attempt ended.
func (c *cmd) finishVerify(stepID string, s *StepState, def *pipeline.Step, leaseID string, res Result) {
	st := c.st
	p := Payload{Step: stepID, Attempt: s.Attempt, Lease: leaseID}
	switch {
	case st.CancelRequested && (res.Outcome == "canceled" || res.Outcome == "lost"):
		p.Reason = st.stoppedBecause()
		c.emit(StepCanceled, p)
		return
	case s.Ver == nil:
		c.failStep(stepID, "the verification was never started")
		return
	}
	v := s.Ver.verdict()
	if !v.Final {
		why := nonEmpty(res.Error, "the deploy runner stopped before the window was over")
		if res.Outcome == "failed" {
			why = "the deploy runner could not go on: " + why
		}
		c.lose(stepID, why)
		return
	}
	var expl *Explanation
	if res.Verify != nil && res.Verify.Explanation != nil {
		x := *res.Verify.Explanation
		x.Summary, x.Cause, x.Suggestion = clipText(x.Summary, 2000), clipText(x.Cause, 2000), clipText(x.Suggestion, 2000)
		x.Error = clipText(x.Error, 1000)
		x.Changed = false
		expl = &x
	}
	c.decideVerify(stepID, def, v, expl)
}

// decideVerify records a final verdict and acts on it.
func (c *cmd) decideVerify(stepID string, def *pipeline.Step, v verify.Verdict, expl *Explanation) {
	if expl != nil && def.Verify.Decide == "model-assisted" && expl.Error == "" &&
		(expl.Outcome == verify.Failed || expl.Outcome == verify.Inconclusive) && v.Outcome != expl.Outcome {
		before := v.Outcome
		v = verify.Downgrade(v, expl.Model, expl.Outcome, nonEmpty(expl.Cause, expl.Summary))
		expl.Changed = v.Outcome != before
	}
	pause := v.Outcome == verify.Inconclusive && def.Verify.OnInconclusive == "pause"
	s := c.st.Steps[stepID]
	c.emit(VerifyVerdict, Payload{Step: stepID, Attempt: s.Attempt, Verify: &VerifyEvent{Verdict: &v, Explanation: expl, Pause: pause}})
	if pause {
		return
	}
	c.applyVerdict(stepID, def, v.Outcome, v.Summary, "")
}

// applyVerdict ends a verify step as a verdict (or a person) says.
func (c *cmd) applyVerdict(stepID string, def *pipeline.Step, outcome, summary, by string) {
	s := c.st.Steps[stepID]
	vf := def.Verify
	outputs := map[string]string{"verdict": outcome, "summary": clipText(summary, 2000)}
	p := Payload{Step: stepID, Attempt: s.Attempt, Outputs: outputs}
	switch {
	case outcome == verify.Passed, outcome == verify.Inconclusive && vf.OnInconclusive == "pass":
		p.Outcome = string(StepSucceeded)
		c.emit(StepFinished, p)
	default:
		if outcome == verify.Failed && (vf.OnFail == "abort" || vf.OnFail == "rollback") {
			c.abortForVerify(stepID, def, summary, by)
		}
		p.Outcome = string(StepFailed)
		what := "the verification failed"
		if outcome == verify.Inconclusive {
			what = "the verification was inconclusive"
		}
		if by != "" {
			what = "marked failed by " + by
		}
		p.Reason = what + ": " + summary
		c.emit(StepFinished, p)
	}
}

// abortForVerify aborts the rollout of the deployment a failed
// verification watched, unless it is already aborted or being aborted.
func (c *cmd) abortForVerify(stepID string, def *pipeline.Step, summary, by string) {
	st := c.st
	d := st.Steps[def.Deployment]
	deployDef := st.Pipeline.Step(def.Deployment)
	if d == nil || !d.Dep.Opened() || d.Dep.Closed || deployDef == nil || deployDef.Rollout == nil || !c.e.knows(schemaControl) {
		return
	}
	row, err := c.deploymentRow(d.Dep.ID)
	if err != nil {
		c.dbFail(err)
		return
	}
	if row.hash == "" {
		return // its version was never seen in the rollout: nothing to abort
	}
	x := deploy.Expect{PodHash: row.hash}
	if ro := row.snap.Rollout; ro != nil && ro.CurrentPodHash == row.hash {
		x.Step = ro.Step
		if ro.Aborted {
			return
		}
	}
	for _, cs := range st.Commands {
		if cs.Deployment == d.Dep.ID && cs.Action == pipeline.ActionAbort && (cs.open() || cs.applied()) {
			return
		}
	}
	for _, cs := range st.Commands {
		if cs.Deployment == d.Dep.ID && cs.State == CommandPending {
			c.withdraw(cs, "launchway", "withdrawn by the abort verification "+stepID+" asked for")
		}
	}
	actor := nonEmpty(by, "launchway")
	c.giveCommand(def.Deployment, d.Dep.ID, pipeline.ActionAbort, actor, clipText("verification "+stepID+" failed: "+summary, MaxCommentLen), OriginVerify, x)
}

// expireVerify fails a verify step whose deadline passed while it was
// queued or paused.
func (c *cmd) expireVerify(id string, def *pipeline.Step) error {
	s := c.st.Steps[id]
	reason := ""
	switch {
	case s.Status == StepQueuedS:
		target := ""
		if s.Ver != nil {
			target = s.Ver.Target
		}
		reason = fmt.Sprintf("no deploy runner serving target %s took the verification within %s (do its runners have verify sources configured?)", target, def.Timeout.Short())
	case s.Status == StepWaitingS && s.WaitKind == WaitVerify:
		reason = fmt.Sprintf("the verification was inconclusive and nobody marked it passed or failed within %s", def.Timeout.Short())
	default:
		return nil
	}
	c.emit(StepFinished, Payload{Step: id, Attempt: s.Attempt, Outcome: string(StepFailed), Reason: reason})
	c.advance()
	return nil
}

// ---- marking a verification passed or failed ----

// OverrideRequest is a person marking a verification passed or failed.
type OverrideRequest struct {
	Run, Step, Actor, Outcome, Comment string
}

// OverrideVerify marks a verification that has not ended passed or failed,
// as a person decided. It fails with ErrForbidden when the person started
// the run, the environment is protected and they mark it passed (marking
// failed is always allowed, as aborting is); with ErrConflict when the step
// is not a verification going on. Marking failed acts as a failed verdict
// does (on_fail). The caller checks the person's role.
func (e *Engine) OverrideVerify(rq OverrideRequest) error {
	if !e.knows(schemaVerify) {
		return fmt.Errorf("%w: this server does not know verify steps", ErrConflict)
	}
	rq.Actor, rq.Comment = strings.TrimSpace(rq.Actor), strings.TrimSpace(rq.Comment)
	switch {
	case rq.Actor == "":
		return fmt.Errorf("%w: marking a verification needs someone to do it", ErrInvalid)
	case rq.Outcome != verify.Passed && rq.Outcome != verify.Failed:
		return fmt.Errorf("%w: mark it passed or failed, not %q", ErrInvalid, rq.Outcome)
	case rq.Comment == "":
		return fmt.Errorf("%w: say why (a reason is required)", ErrInvalid)
	case utf8.RuneCountInString(rq.Comment) > MaxCommentLen:
		return fmt.Errorf("%w: the reason is longer than %d characters", ErrInvalid, MaxCommentLen)
	}
	return e.withRun(rq.Run, func(c *cmd) error {
		def := c.st.Pipeline.Step(rq.Step)
		if def == nil {
			return fmt.Errorf("%w: step %s", ErrNotFound, rq.Step)
		}
		if why := c.overrideRefusal(def, rq.Actor, rq.Outcome); why != nil {
			return why
		}
		s := c.st.Steps[rq.Step]
		if s.LeaseID != "" {
			if _, err := c.tx.Exec(`DELETE FROM leases WHERE id = ?`, s.LeaseID); err != nil {
				return err
			}
		}
		c.emit(VerifyOverride, Payload{Step: rq.Step, Attempt: s.Attempt, Actor: rq.Actor, Comment: rq.Comment,
			Verify: &VerifyEvent{Outcome: rq.Outcome}})
		c.applyVerdict(rq.Step, def, rq.Outcome, fmt.Sprintf("marked %s by %s: %s", rq.Outcome, rq.Actor, rq.Comment), rq.Actor)
		c.advance()
		return nil
	})
}

// overrideRefusal says why actor may not mark a verify step outcome now.
func (c *cmd) overrideRefusal(def *pipeline.Step, actor, outcome string) error {
	if !def.IsVerify() {
		return fmt.Errorf("%w: %s is not a verify step", ErrConflict, def.ID)
	}
	s := c.st.Steps[def.ID]
	switch s.Status {
	case StepQueuedS, StepLeasedS, StepRunning:
	case StepWaitingS:
		if s.WaitKind != WaitVerify {
			return fmt.Errorf("%w: %s is not waiting for a decision", ErrConflict, def.ID)
		}
	default:
		return fmt.Errorf("%w: %s is %s, so there is no verification going on to mark", ErrConflict, def.ID, s.Status)
	}
	if s.Ver == nil {
		return fmt.Errorf("%w: %s has not started", ErrConflict, def.ID)
	}
	env := c.e.live().envs[s.Ver.Environment]
	if env.Protected && !c.e.live().selfOK && outcome == verify.Passed && actor == c.st.Trigger.Actor {
		return fmt.Errorf("%w: %s started this run, and %s is a protected environment, so someone else must mark the verification passed (you may still mark it failed)",
			ErrForbidden, actor, env.Name)
	}
	return nil
}

// ---- persisting ----

// persistVerify brings the schema-19 tables up to date with the command's
// events.
func (c *cmd) persistVerify() error {
	st := c.st
	for _, id := range c.changedSteps() {
		def := st.Pipeline.Step(id)
		if def != nil && def.IsAI() {
			// Any runner that calls models may take an ai step, whoever
			// holds the run's workspace (see tryLease).
			if _, err := c.tx.Exec(`UPDATE steps SET kind = ? WHERE run_id = ? AND step_id = ?`, pipeline.KindAI, st.ID, id); err != nil {
				return err
			}
		}
		if def == nil || !def.IsVerify() {
			continue
		}
		s := st.Steps[id]
		target := ""
		if s.Ver != nil {
			target = s.Ver.Target
		}
		if _, err := c.tx.Exec(`UPDATE steps SET target = ?, wait_kind = ? WHERE run_id = ? AND step_id = ?`,
			target, s.WaitKind, st.ID, id); err != nil {
			return err
		}
	}
	for _, ev := range c.events {
		s := st.Steps[ev.Data.Step]
		def := st.Pipeline.Step(ev.Data.Step)
		var err error
		switch {
		case ev.Type == VerifyStarted && s != nil && s.Ver != nil:
			v := s.Ver
			_, err = c.tx.Exec(`INSERT INTO verifications(run_id, step_id, project, run_number, environment, service, deployment, target, version,
					outcome, summary, intervals, total, overridden_by, started_at, decided_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(run_id, step_id) DO UPDATE SET environment = excluded.environment, service = excluded.service,
					deployment = excluded.deployment, target = excluded.target, version = excluded.version, outcome = excluded.outcome,
					summary = excluded.summary, intervals = 0, total = excluded.total, overridden_by = '', started_at = excluded.started_at, decided_at = 0`,
				st.ID, ev.Data.Step, st.Project, st.Number, v.Environment, v.Service, v.Deployment, v.Target, v.Version,
				verify.Running, "", 0, v.Spec.Intervals, "", ev.At, 0)
		case ev.Type == VerifyInterval && s != nil && s.Ver != nil:
			vd := s.Ver.verdict()
			_, err = c.tx.Exec(`UPDATE verifications SET intervals = ?, summary = ? WHERE run_id = ? AND step_id = ? AND decided_at = 0`,
				len(s.Ver.Intervals), clipText(vd.Summary, 2000), st.ID, ev.Data.Step)
		case ev.Type == VerifyVerdict && ev.Data.Verify.Verdict != nil:
			vd := ev.Data.Verify.Verdict
			_, err = c.tx.Exec(`UPDATE verifications SET outcome = ?, summary = ?, decided_at = ? WHERE run_id = ? AND step_id = ?`,
				vd.Outcome, clipText(vd.Summary, 2000), ev.At, st.ID, ev.Data.Step)
		case ev.Type == VerifyOverride:
			_, err = c.tx.Exec(`UPDATE verifications SET outcome = ?, overridden_by = ?, summary = ?, decided_at = ? WHERE run_id = ? AND step_id = ?`,
				ev.Data.Verify.Outcome, ev.Data.Actor, clipText("marked "+ev.Data.Verify.Outcome+" by "+ev.Data.Actor+": "+ev.Data.Comment, 2000),
				ev.At, st.ID, ev.Data.Step)
		case ev.Type == StepFinished && def != nil && def.IsVerify() && s != nil && s.Ver != nil && s.Ver.Override == "" &&
			ev.Data.Outcome == string(StepSucceeded) && s.Ver.Verdict != nil && s.Ver.Verdict.Outcome == verify.Passed:
			// Only what the statistics passed teaches later verifications;
			// a pass marked by a person does not.
			ms, ls := verify.BaselineData(s.Ver.Spec, s.Ver.Intervals)
			data, _ := json.Marshal(baselineData{Metrics: ms, Logs: ls})
			_, err = c.tx.Exec(`INSERT INTO verify_baselines(run_id, step_id, environment, project, service, run_number, version, data, created_at)
				VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id, step_id) DO UPDATE SET data = excluded.data, created_at = excluded.created_at`,
				st.ID, ev.Data.Step, s.Ver.Environment, st.Project, s.Ver.Service, st.Number, s.Ver.Version, string(data), ev.At)
		case (ev.Type == StepFinished || ev.Type == StepCanceled) && ev.Data.AI != nil:
			err = c.addUsage(st.Project, ev.Data.AI.Model, ev.At, ev.Data.AI.Usage)
		}
		if err != nil {
			return err
		}
		// A verdict carries both the decision (above) and, when a model
		// explained it, the tokens that cost: count them toward the
		// project's daily budget too.
		if ev.Type == VerifyVerdict && ev.Data.Verify.Explanation != nil && ev.Data.Verify.Explanation.Usage != nil {
			if err := c.addUsage(st.Project, ev.Data.Verify.Explanation.Model, ev.At, *ev.Data.Verify.Explanation.Usage); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- reading ----

// VerificationView is a verification as the run page and the Environments
// page show it.
type VerificationView struct {
	RunID           string                  `json:"run_id"`
	RunNumber       int                     `json:"run_number"`
	Project         string                  `json:"project"`
	Step            string                  `json:"step"`
	Status          string                  `json:"status"` // the step's
	Environment     string                  `json:"environment"`
	Service         string                  `json:"service"`
	Target          string                  `json:"target"`
	Deployment      string                  `json:"deployment,omitempty"`
	Version         string                  `json:"version,omitempty"`
	Start           int64                   `json:"start"`
	IntervalMS      int64                   `json:"interval_ms"`
	Total           int                     `json:"total"`
	Deadline        int64                   `json:"deadline,omitempty"`
	Sensitivity     string                  `json:"sensitivity"`
	OnFail          string                  `json:"on_fail"`
	OnInconclusive  string                  `json:"on_inconclusive"`
	Decide          string                  `json:"decide,omitempty"`
	Model           string                  `json:"model,omitempty"`
	Params          map[string]string       `json:"params,omitempty"`
	Baseline        []verify.BaselineRun    `json:"baseline,omitempty"`
	BaselineMetrics map[string]verify.Stats `json:"baseline_metrics,omitempty"`
	Intervals       []verify.Interval       `json:"intervals"`
	// Verdict is the recorded one, or what the intervals so far say.
	Verdict         verify.Verdict `json:"verdict"`
	Decided         bool           `json:"decided"`
	Explanation     *Explanation   `json:"explanation,omitempty"`
	Override        string         `json:"override,omitempty"`
	OverrideBy      string         `json:"override_by,omitempty"`
	OverrideComment string         `json:"override_comment,omitempty"`
	Queries         []VerifyQuery  `json:"queries"`
	StartedAt       int64          `json:"started_at"`
}

// RunVerifications returns the verifications of a run, in pipeline order.
func (e *Engine) RunVerifications(runID string) ([]VerificationView, error) {
	out := []VerificationView{}
	if !e.knows(schemaVerify) {
		return out, nil
	}
	err := e.readRun(runID, func(c *cmd) error {
		for _, id := range c.st.Order {
			def := c.st.Pipeline.Step(id)
			s := c.st.Steps[id]
			if !def.IsVerify() || s.Ver == nil {
				continue
			}
			out = append(out, verificationView(c.st, def, s))
		}
		return nil
	})
	if errors.Is(err, ErrNeedsNewer) {
		return out, nil
	}
	return out, err
}

func verificationView(st *RunState, def *pipeline.Step, s *StepState) VerificationView {
	v := s.Ver
	vw := VerificationView{RunID: st.ID, RunNumber: st.Number, Project: st.Project, Step: s.ID, Status: string(s.Status),
		Environment: v.Environment, Service: v.Service, Target: v.Target, Deployment: v.Deployment, Version: v.Version,
		Start: v.Start, IntervalMS: v.Spec.IntervalMS, Total: v.Spec.Intervals, Deadline: v.Deadline, Sensitivity: v.Spec.Sensitivity,
		OnFail: def.Verify.OnFail, OnInconclusive: def.Verify.OnInconclusive, Decide: def.Verify.Decide, Model: def.Verify.Model,
		Params: v.Params, Baseline: v.Baseline.Runs, Intervals: append([]verify.Interval{}, v.Intervals...), Explanation: v.Explanation,
		Override: v.Override, OverrideBy: v.OverrideBy, OverrideComment: v.OverrideComment, StartedAt: v.StartedAt}
	slices.SortFunc(vw.Intervals, func(a, b verify.Interval) int { return a.Index - b.Index })
	if v.Verdict != nil {
		vw.Verdict, vw.Decided = *v.Verdict, true
	} else {
		vw.Verdict = v.verdict()
	}
	if len(v.Baseline.Metrics) > 0 {
		vw.BaselineMetrics = map[string]verify.Stats{}
		for name, vals := range v.Baseline.Metrics {
			vw.BaselineMetrics[name] = verify.Summarize(vals)
		}
	}
	for _, m := range def.Verify.Metrics {
		vw.Queries = append(vw.Queries, VerifyQuery{Name: m.Name, Source: m.Source, Query: m.Query, StableQuery: m.StableQuery, Statement: m.Statement, Index: m.Index})
	}
	for _, l := range def.Verify.Logs {
		vw.Queries = append(vw.Queries, VerifyQuery{Name: l.Name, Source: l.Source, Query: l.Query, StableQuery: l.StableQuery, Statement: l.Statement, Index: l.Index})
	}
	return vw
}

// VerificationSummary is a verification in a list.
type VerificationSummary struct {
	RunID        string `json:"run_id"`
	RunNumber    int    `json:"run_number"`
	Project      string `json:"project"`
	Step         string `json:"step"`
	Environment  string `json:"environment"`
	Service      string `json:"service"`
	Deployment   string `json:"deployment,omitempty"`
	Version      string `json:"version,omitempty"`
	Outcome      string `json:"outcome"`
	Summary      string `json:"summary"`
	Intervals    int    `json:"intervals"`
	Total        int    `json:"total"`
	OverriddenBy string `json:"overridden_by,omitempty"`
	StartedAt    int64  `json:"started_at"`
	DecidedAt    int64  `json:"decided_at,omitempty"`
	// Status is the verify step's status: waiting while an inconclusive
	// verdict waits for a person (on_inconclusive: pause).
	Status string `json:"status"`
}

// VerificationFilter narrows a list of verifications.
type VerificationFilter struct {
	Environment, Project, Service string
	// Visible says which projects the caller may see; nil sees all.
	Visible func(project string) bool
	Limit   int
}

// Verifications lists verifications, newest first.
func (e *Engine) Verifications(f VerificationFilter) ([]VerificationSummary, error) {
	out := []VerificationSummary{}
	if !e.knows(schemaVerify) {
		return out, nil
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where, args := []string{"1 = 1"}, []any{}
	for col, v := range map[string]string{"environment": f.Environment, "project": f.Project, "service": f.Service} {
		if v != "" {
			where = append(where, "v."+col+" = ?")
			args = append(args, v)
		}
	}
	slices.Sort(where)
	args = append(args, f.Limit*4)
	rows, err := e.db.Query(`SELECT v.run_id, v.run_number, v.project, v.step_id, v.environment, v.service, v.deployment, v.version, v.outcome,
			v.summary, v.intervals, v.total, v.overridden_by, v.started_at, v.decided_at, COALESCE(s.status, '')
		FROM verifications v LEFT JOIN steps s ON s.run_id = v.run_id AND s.step_id = v.step_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY v.started_at DESC, v.run_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v VerificationSummary
		if err := rows.Scan(&v.RunID, &v.RunNumber, &v.Project, &v.Step, &v.Environment, &v.Service, &v.Deployment, &v.Version,
			&v.Outcome, &v.Summary, &v.Intervals, &v.Total, &v.OverriddenBy, &v.StartedAt, &v.DecidedAt, &v.Status); err != nil {
			return nil, err
		}
		if f.Visible != nil && !f.Visible(v.Project) {
			continue
		}
		out = append(out, v)
		if len(out) == f.Limit {
			break
		}
	}
	return out, rows.Err()
}

// VerifyControls says whether actor could mark a verification passed and
// failed now, and why not.
func (e *Engine) VerifyControls(runID, stepID, actor string) (env string, out []Control, err error) {
	if !e.knows(schemaVerify) {
		return "", nil, fmt.Errorf("%w: this server does not know verify steps", ErrConflict)
	}
	err = e.readRun(runID, func(c *cmd) error {
		def := c.st.Pipeline.Step(stepID)
		if def == nil || !def.IsVerify() {
			return fmt.Errorf("%w: verify step %s in run %s", ErrNotFound, stepID, runID)
		}
		if s := c.st.Steps[stepID]; s.Ver != nil {
			env = s.Ver.Environment
		} else {
			env, _ = c.st.Pipeline.VerifyEnvironment(def)
		}
		for _, o := range []string{verify.Passed, verify.Failed} {
			ctl := Control{Action: "mark-" + o, Available: true}
			if err := c.overrideRefusal(def, actor, o); err != nil {
				switch {
				case errors.Is(err, ErrForbidden):
					ctl.Available, ctl.Forbidden, ctl.Reason = false, true, strings.TrimPrefix(err.Error(), ErrForbidden.Error()+": ")
				case errors.Is(err, ErrConflict):
					ctl.Available, ctl.Reason = false, strings.TrimPrefix(err.Error(), ErrConflict.Error()+": ")
				default:
					return err
				}
			}
			out = append(out, ctl)
		}
		return nil
	})
	return env, out, err
}
