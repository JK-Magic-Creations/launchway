package engine

// Delivery policies (schema 16; see internal/policy for the rules and the
// pure evaluator).
//
// Policies are judged at three points, each in the transaction that acts
// on the decision, so a decision and what it allows or stops are stored
// together or not at all:
//
//   - when a run is created, re-run or resumed (advance, gateRun): a
//     blocked decision fails each step of the run as it would start;
//   - when a deploy step is about to take its environment's lock and open
//     its deployment (startDeploy), and when a rollout step is about to
//     promote or retry it (startRolloutStep): a blocked decision fails the
//     step before anything reaches the cluster. Rollbacks and aborts are
//     never judged: they return to what ran before;
//   - when an image step that would push is about to be queued (advance):
//     a blocked decision fails the step before any runner sees it.
//
// Each decision is a policy.evaluated event holding the policies, the facts
// gathered from the run's events, the waivers and the time, so it can be
// reproduced from the log. Every server reads the same policies: the
// server's own configuration (which every server must share, like the
// environments) and the Git sources as stored in policy_sources by
// whichever server loaded them last. A source that is missing, broken or
// not confirmed for too long fails closed (policy.Set).

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

// PolicyOptions configure delivery policies.
type PolicyOptions struct {
	// Inline are the policies of the server's configuration (source
	// "config"). Every server sharing a database must have the same ones.
	Inline []policy.Policy
	// Sources are Git repositories of policies. Each server loads them
	// (see source.PolicyLoader) and stores what it loaded; every server
	// judges from what is stored.
	Sources []PolicySourceConfig
	// MaxWaiverDays is the longest a waiver may last; default 30, at most
	// 365.
	MaxWaiverDays int
}

// PolicySourceConfig is a Git source of policies: a library repository
// (from the server's libraries), pinned to a tag or a commit, and the
// directory of policy files in it.
type PolicySourceConfig struct {
	Name     string
	Library  string
	Version  string
	Path     string   // default "policies"
	Projects []string // the projects it may govern; empty means every project
	// Refresh is how often each server looks at it again (default 1m);
	// StaleAfter how long a version loaded earlier is still used when the
	// source cannot be reached (default 1h; after that it fails closed).
	Refresh    time.Duration
	StaleAfter time.Duration
}

// configSource is the name of the server's own policies.
const configSource = "config"

// checkPolicyOptions checks and completes the options.
func checkPolicyOptions(o PolicyOptions) (PolicyOptions, error) {
	if o.MaxWaiverDays == 0 {
		o.MaxWaiverDays = policy.DefaultMaxDays
	}
	if o.MaxWaiverDays < 1 || o.MaxWaiverDays > policy.MaxWaiverDays {
		return o, fmt.Errorf("policy.waivers.max_days must be between 1 and %d", policy.MaxWaiverDays)
	}
	if err := policy.Check(o.Inline); err != nil {
		return o, err
	}
	seen := map[string]bool{configSource: true}
	for i := range o.Sources {
		s := &o.Sources[i]
		switch {
		case !policy.ValidName(s.Name):
			return o, fmt.Errorf("policy.sources: %q: use lower-case letters, digits and hyphens", s.Name)
		case seen[s.Name]:
			return o, fmt.Errorf("policy.sources: %s is listed twice (or is %s, which names the configuration's own policies)", s.Name, configSource)
		case s.Library == "" || s.Version == "":
			return o, fmt.Errorf("policy.sources: %s: name its library and the version (a tag or a commit) to load", s.Name)
		}
		seen[s.Name] = true
		if s.Path == "" {
			s.Path = "policies"
		}
		if s.Refresh <= 0 {
			s.Refresh = time.Minute
		}
		if s.StaleAfter <= 0 {
			s.StaleAfter = time.Hour
		}
	}
	return o, nil
}

// policiesOn reports whether any policy or policy source is configured.
func (e *Engine) policiesOn() bool {
	return e.knows(schemaPolicy) && (len(e.live().pol.Inline) > 0 || len(e.live().pol.Sources) > 0)
}

// PolicySources returns the configured Git sources of policies.
func (e *Engine) PolicySources() []PolicySourceConfig { return slices.Clone(e.live().pol.Sources) }

// recordConfigPolicies notes this server's own policies in policy_sources,
// and warns if another server started recently with different ones.
func (e *Engine) recordConfigPolicies() error {
	if !e.knows(schemaPolicy) {
		return nil
	}
	unlock := e.writeLock()
	defer unlock()
	now := e.now().UnixMilli()
	var version, server string
	var at int64
	err := e.db.QueryRow(`SELECT version, server, checked_at FROM policy_sources WHERE name = ?`, configSource).Scan(&version, &server, &at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && server != e.instance && version != e.live().polHash && now-at < (10*time.Minute).Milliseconds() {
		e.log.Warn("another server started recently with different policies in its configuration; every server must have the same ones",
			"this_server", e.live().polHash, "other_server", version)
	}
	b, _ := json.Marshal(e.live().pol.Inline)
	_, err = e.db.Exec(`INSERT INTO policy_sources(name, version, policies, loaded_at, checked_at, server) VALUES(?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET version = excluded.version, policies = excluded.policies, loaded_at = excluded.loaded_at,
			checked_at = excluded.checked_at, server = excluded.server`, configSource, e.live().polHash, string(b), now, now, e.instance)
	return err
}

// sourceRow is a Git source as stored.
type sourceRow struct {
	Ref, Version, Policies, Error string
	LoadedAt, CheckedAt           int64
	FetchError                    string
	FetchErrorAt                  int64
}

func readSourceRow(q interface {
	QueryRow(string, ...any) *rowx
}, name string) (sourceRow, error) {
	var r sourceRow
	err := q.QueryRow(`SELECT ref, version, policies, error, loaded_at, checked_at, fetch_error, fetch_error_at FROM policy_sources WHERE name = ?`, name).
		Scan(&r.Ref, &r.Version, &r.Policies, &r.Error, &r.LoadedAt, &r.CheckedAt, &r.FetchError, &r.FetchErrorAt)
	return r, err
}

// policySet is the policies in force, as every server sees them at now.
func (e *Engine) policySet(q interface {
	QueryRow(string, ...any) *rowx
}, now int64) policy.Set {
	var set policy.Set
	if len(e.live().pol.Inline) > 0 {
		set.Sources = append(set.Sources, policy.Source{Name: configSource, Version: e.live().polHash, Policies: e.live().pol.Inline})
	}
	for _, sc := range e.live().pol.Sources {
		src := policy.Source{Name: sc.Name, Ref: sc.Version, Projects: sc.Projects}
		row, err := readSourceRow(q, sc.Name)
		switch {
		case errors.Is(err, sql.ErrNoRows) || (err == nil && row.Policies == "" && row.Error == "" && row.FetchError == ""):
			src.Error = "no server has loaded it yet"
		case err != nil:
			src.Error = "its state could not be read from the database: " + err.Error()
		case row.Ref != sc.Version:
			src.Error = fmt.Sprintf("the version stored is of %s, but this server is configured with %s; it is refused until a server loads %s",
				nonEmpty(row.Ref, "nothing"), sc.Version, sc.Version)
			if row.FetchError != "" {
				src.Error += " (the last attempt failed: " + row.FetchError + ")"
			}
		case row.Error != "":
			src.Version = row.Version
			src.Error = row.Error
		case row.Policies == "":
			src.Error = "it has never been loaded: " + row.FetchError
		case now-row.CheckedAt > sc.StaleAfter.Milliseconds():
			src.Version = row.Version
			src.Error = fmt.Sprintf("it was last confirmed at %s, longer ago than %s", time.UnixMilli(row.CheckedAt).UTC().Format(time.RFC3339), sc.StaleAfter)
			if row.FetchError != "" {
				src.Error += "; the last attempt failed: " + row.FetchError
			}
		default:
			src.Version = row.Version
			dec := json.NewDecoder(bytes.NewReader([]byte(row.Policies)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&src.Policies); err != nil {
				src.Error = "the stored policies use what this server does not know (a newer server loaded them): " + err.Error()
				src.Policies = nil
			}
		}
		set.Sources = append(set.Sources, src)
	}
	// Names are unique across sources: a later source that repeats one
	// fails closed rather than shadow it.
	seen := map[string]string{}
	for i := range set.Sources {
		src := &set.Sources[i]
		if src.Error != "" {
			continue
		}
		for _, p := range src.Policies {
			if other, dup := seen[p.Name]; dup {
				src.Error = fmt.Sprintf("its policy %s is also defined in %s; names must be unique", p.Name, other)
				break
			}
		}
		if src.Error == "" {
			for _, p := range src.Policies {
				seen[p.Name] = src.Name
			}
		}
	}
	return set
}

// ---- gathering facts ----

// policyInput is what every point records about the run.
func (c *cmd) policyInput(point string) policy.Input {
	st := c.st
	t := st.Trigger
	return policy.Input{Point: point, Project: st.Project, Run: st.ID, RunNumber: st.Number, Event: t.Event, Branch: t.Branch, Tag: t.Tag,
		Starter: t.Actor, SelfApproval: c.st.selfApproval(c.e)}
}

// pipelineFacts are what the run-point rules judge.
func (c *cmd) pipelineFacts() *policy.PipelineFacts {
	p := c.st.Pipeline
	f := &policy.PipelineFacts{Libraries: []policy.LibraryFact{}, Templates: []policy.TemplateFact{}, Images: []policy.ImageRef{},
		Timeouts: []policy.TimeoutFact{}, Secrets: []policy.SecretFact{}, SecretsAllowed: c.st.secretsAllowed()}
	for _, l := range p.Libraries {
		f.Libraries = append(f.Libraries, policy.LibraryFact{Name: l.Name, Repo: l.Repo, Version: l.Version, Override: l.Override, RefKind: l.RefKind, Commit: l.Commit})
	}
	for _, g := range p.Groups {
		tf := policy.TemplateFact{Group: g.ID, Library: g.Library, Template: g.Template}
		if l := p.Library(g.Library); l != nil {
			tf.Library, tf.Version, tf.RefKind = l.Repo, l.Wanted(), l.RefKind
		}
		f.Templates = append(f.Templates, tf)
	}
	for _, ct := range p.Containers {
		f.Images = append(f.Images, policy.ImageRef{Where: "container " + ct.Name, Image: ct.Image})
	}
	for _, s := range p.Steps {
		if s.Container == "" && !s.IsApproval() && !s.IsDeployKind() && !s.IsImage() && !s.IsUse() && s.Image != "" {
			f.Images = append(f.Images, policy.ImageRef{Where: "step " + s.ID, Image: s.Image})
		}
		if !s.IsApproval() {
			f.Timeouts = append(f.Timeouts, policy.TimeoutFact{Step: s.ID, Kind: s.Kind, Timeout: s.Timeout.D().Milliseconds()})
		}
		var names []string
		for k := range s.Secrets {
			names = append(names, k)
		}
		if s.ImageBuild != nil && s.ImageBuild.Sign != nil && s.ImageBuild.Sign.Key != nil {
			names = append(names, "(signing key "+s.ImageBuild.Sign.Key.String()+")")
		}
		if len(names) > 0 {
			sort.Strings(names)
			f.Secrets = append(f.Secrets, policy.SecretFact{Step: s.ID, Names: names})
		}
	}
	return f
}

// reusedState is the state of a run a step was reused from, read in the
// command's transaction (once per command).
func (c *cmd) reusedState(runID string) (*RunState, error) {
	if c.reused == nil {
		c.reused = map[string]*RunState{}
	}
	if st, ok := c.reused[runID]; ok {
		return st, nil
	}
	evs, _, err := loadEvents(c.tx, runID)
	if err != nil {
		return nil, err
	}
	st := c.e.fold(evs)
	c.reused[runID] = st
	return st, nil
}

func countsOf(c FindingCounts) policy.Counts {
	return policy.Counts{Total: c.Total, Critical: c.Critical, High: c.High, Medium: c.Medium, Low: c.Low, Unknown: c.Unknown}
}

// imageFacts are what an image step will do, as its job says.
func imageFacts(def *pipeline.Step, j *ImageJob) *policy.ImageStep {
	f := &policy.ImageStep{Push: j.Push, Registries: []string{}, SBOM: j.SBOM, Provenance: j.Provenance}
	for _, t := range j.Tags {
		if !slices.Contains(f.Registries, t.Registry) {
			f.Registries = append(f.Registries, t.Registry)
		}
	}
	if j.Cache != nil && j.Push {
		f.Cache = j.Cache.Registry
	}
	if j.Scan != nil {
		f.ScanMode, f.ScanFailOn = nonEmpty(j.Scan.Mode, "report"), j.Scan.FailOn
	}
	if s := def.ImageBuild.Sign; s != nil {
		f.Sign = "runner"
		if s.Key != nil {
			f.Sign = "key"
		}
	}
	return f
}

// ---- judging ----

// judge evaluates the policies for in and records the decision, unless no
// policy applies (at the run point: unless none governs the run). It
// returns the decision, or nil.
func (c *cmd) judge(in *policy.Input, fill func(set policy.Set) error) *policy.Evaluation {
	set := c.e.policySet(c.tx, c.now)
	if !set.Governed(in.Project, in.Branch, in.Event) {
		return nil
	}
	if fill != nil {
		if err := fill(set); err != nil {
			c.dbFail(err)
			return nil
		}
	}
	waivers, err := c.waiversOf(in.Project)
	if err != nil {
		c.dbFail(err)
		return nil
	}
	ev := policy.Evaluate(set, *in, waivers, c.now)
	if in.Point != policy.PointRun && len(ev.Policies) == 0 {
		return nil
	}
	c.emit(PolicyEvaluated, Payload{Step: in.Step, Policy: &ev})
	return &ev
}

// waiversOf lists a project's waivers that are not revoked and did not
// expire more than a month ago (so a decision can say one expired).
func (c *cmd) waiversOf(project string) ([]policy.Waiver, error) {
	rows, err := c.tx.Query(`SELECT id, project, policy, rule, run_id, environment, finding, reason, granted_by, granted_at, expires_at
		FROM policy_waivers WHERE project = ? AND revoked_at = 0 AND expires_at > ? ORDER BY id`, project, c.now-(30*24*time.Hour).Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []policy.Waiver
	for rows.Next() {
		var w policy.Waiver
		if err := rows.Scan(&w.ID, &w.Project, &w.Policy, &w.Rule, &w.Run, &w.Environment, &w.Finding, &w.Reason, &w.GrantedBy, &w.GrantedAt, &w.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// gateRun judges a run that was just created, re-run or resumed.
func (c *cmd) gateRun() {
	in := c.policyInput(policy.PointRun)
	in.Pipeline = c.pipelineFacts()
	c.judge(&in, nil)
}

// runBlocked says why the run's latest decision refuses it, or "".
func (st *RunState) runBlocked() string {
	if st.RunGate == nil || !st.RunGate.Blocked() {
		return ""
	}
	return runRefusedPrefix + " " + st.RunGate.Reason() + ". Grant a waiver or change what was found, then resume the run"
}

// How the errors of steps a policy refused begin (see StepView.PolicyBlocked).
const (
	policyBlockedPrefix = "blocked by policy:"
	runRefusedPrefix    = "the run was refused when it started:"
)

// gateDeploy judges a deploy step (or a rollout step that promotes or
// retries) before it acts. It reports false, having failed the step, if
// the decision blocks it.
func (c *cmd) gateDeploy(id string, def, deployDef *pipeline.Step, envName, action string, protected bool) bool {
	if !c.e.policiesOn() {
		return true
	}
	in := c.policyInput(policy.PointDeploy)
	in.Step, in.Action, in.Environment = id, action, envName
	in.Protected = protected
	ev := c.judge(&in, func(set policy.Set) error {
		var applicable []policy.Policy
		for _, p := range set.Policies() {
			if p.AppliesTo(policy.PointDeploy, in.Project, in.Branch, in.Event, envName) {
				applicable = append(applicable, p)
			}
		}
		e, err := c.stepEvidence(id, deployDef, policy.FindingFloor(applicable))
		in.Evidence = e
		return err
	})
	if c.err != nil {
		return false
	}
	if ev != nil && ev.Blocked() {
		c.failStep(id, ev.Reason()+". Grant a waiver or change what was found, then resume the run (or re-run it from "+id+")")
		return false
	}
	return true
}

// gateImage judges an image step that would push, before it is queued. It
// reports true, having failed the step, if the decision blocks it.
func (c *cmd) gateImage(id string, def *pipeline.Step) bool {
	if !c.e.policiesOn() {
		return false
	}
	j, err := c.imageJob(def)
	if err != nil {
		return false // imageRefused has failed it already
	}
	in := c.policyInput(policy.PointImage)
	in.Step, in.Action, in.Image = id, "push", imageFacts(def, j)
	ev := c.judge(&in, nil)
	if c.err != nil {
		return true
	}
	if ev != nil && ev.Blocked() {
		c.failStep(id, ev.Reason()+". Grant a waiver or change the step, then resume the run")
		return true
	}
	return false
}

// startedOrResumed reports whether this command created, re-ran or
// resumed the run: the run point's moment.
func (c *cmd) startedOrResumed() bool {
	for _, ev := range c.events {
		if ev.Type == RunCreated || ev.Type == RunResumed {
			return true
		}
	}
	return false
}

// persistPolicy derives policy_evaluations from policy.evaluated events.
func (c *cmd) persistPolicy() error {
	st := c.st
	for _, ev := range c.events {
		if ev.Type != PolicyEvaluated || ev.Data.Policy == nil {
			continue
		}
		e := ev.Data.Policy
		for _, p := range e.Policies {
			if _, err := c.tx.Exec(`INSERT INTO policy_evaluations(run_id, seq, project, run_number, point, step_id, environment, policy, source,
					version, mode, verdict, at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id, seq, policy) DO NOTHING`,
				st.ID, ev.Seq, st.Project, st.Number, e.Point, e.Step, e.Environment, p.Policy.Name, p.Policy.Source, p.Policy.Version,
				p.Policy.Mode, p.Verdict, ev.At); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- Git sources ----

// StorePolicySource records what a server loaded from a Git source:
// the ref it is pinned to, the commit, and the policies, or why the
// content was refused (contentErr), which fails it closed at once.
func (e *Engine) StorePolicySource(name, ref, commit string, ps []policy.Policy, contentErr string) error {
	if !e.knows(schemaPolicy) {
		return nil
	}
	data := ""
	if contentErr == "" {
		if ps == nil {
			ps = []policy.Policy{}
		}
		b, err := json.Marshal(ps)
		if err != nil {
			return err
		}
		data = string(b)
	}
	unlock := e.writeLock()
	defer unlock()
	now := e.now().UnixMilli()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := readSourceRow(tx, name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	loaded := now
	if err == nil && old.Ref == ref && old.Version == commit && old.Policies == data && old.Error == contentErr {
		loaded = old.LoadedAt
	}
	if _, err := tx.Exec(`INSERT INTO policy_sources(name, ref, version, policies, error, loaded_at, checked_at, fetch_error, fetch_error_at, server)
		VALUES(?,?,?,?,?,?,?,'',0,?) ON CONFLICT(name) DO UPDATE SET ref = excluded.ref, version = excluded.version, policies = excluded.policies,
			error = excluded.error, loaded_at = excluded.loaded_at, checked_at = excluded.checked_at, fetch_error = '', fetch_error_at = 0,
			server = excluded.server`, name, ref, commit, data, clipText(contentErr, 4000), loaded, now, e.instance); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if loaded == now && (old.Version != commit || old.Error != contentErr || old.Policies != data) {
		if contentErr != "" {
			e.log.Error("a policy source was refused; runs of the projects it covers are refused at every policy point until it is fixed",
				"source", name, "ref", ref, "commit", commit, "err", contentErr)
		} else {
			e.log.Info("loaded a policy source", "source", name, "ref", ref, "commit", commit, "policies", len(ps))
		}
	}
	return nil
}

// PolicySourceUnreachable records that a server could not reach a Git
// source. What was loaded before stays in use until StaleAfter.
func (e *Engine) PolicySourceUnreachable(name, ref, why string) error {
	if !e.knows(schemaPolicy) {
		return nil
	}
	unlock := e.writeLock()
	defer unlock()
	now := e.now().UnixMilli()
	_, err := e.db.Exec(`INSERT INTO policy_sources(name, ref, fetch_error, fetch_error_at, server) VALUES(?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET fetch_error = excluded.fetch_error, fetch_error_at = excluded.fetch_error_at`,
		name, ref, clipText(why, 2000), now, e.instance)
	return err
}

// ---- waivers ----

// WaiverRequest asks for a waiver.
type WaiverRequest struct {
	Project     string `json:"project"`
	Policy      string `json:"policy"`
	Rule        string `json:"rule"`
	Run         string `json:"run,omitempty"`
	Environment string `json:"environment,omitempty"`
	Finding     string `json:"finding,omitempty"`
	Reason      string `json:"reason"`
	ExpiresAt   int64  `json:"expires_at"` // Unix ms
	// ExpiresIn, if set, is used instead of ExpiresAt: from now, as this
	// server's clock says.
	ExpiresIn time.Duration `json:"-"`
	Actor     string        `json:"-"`
}

// WaiverView is a waiver as the API shows it.
type WaiverView struct {
	policy.Waiver
	RunNumber    int    `json:"run_number,omitempty"`
	RevokedBy    string `json:"revoked_by,omitempty"`
	RevokedAt    int64  `json:"revoked_at,omitempty"`
	RevokeReason string `json:"revoke_reason,omitempty"`
	State        string `json:"state"` // active, expired or revoked
}

// waiverEvent is a line of the waiver log.
type waiverEvent struct {
	Waiver *policy.Waiver `json:"waiver,omitempty"`
	Reason string         `json:"reason,omitempty"` // a revocation's
}

// GrantWaiver records a waiver. The caller has checked that the person may
// waive rules of the project (and environment).
func (e *Engine) GrantWaiver(rq WaiverRequest) (*WaiverView, error) {
	if !e.knows(schemaPolicy) {
		return nil, fmt.Errorf("%w: this server does not know policies (schema %d)", ErrConflict, schemaPolicy)
	}
	now := e.now().UnixMilli()
	if rq.ExpiresIn > 0 {
		rq.ExpiresAt = now + rq.ExpiresIn.Milliseconds()
	}
	rq.Reason = strings.TrimSpace(rq.Reason)
	switch {
	case rq.Project == "":
		return nil, fmt.Errorf("%w: name the project", ErrInvalid)
	case rq.Reason == "":
		return nil, fmt.Errorf("%w: say why the rule is waived (reason)", ErrInvalid)
	case len(rq.Reason) > policy.MaxReason:
		return nil, fmt.Errorf("%w: the reason is longer than %d characters", ErrInvalid, policy.MaxReason)
	case rq.ExpiresAt <= now:
		return nil, fmt.Errorf("%w: a waiver must expire in the future (expires_at, Unix milliseconds)", ErrInvalid)
	case rq.ExpiresAt > now+int64(e.live().pol.MaxWaiverDays)*24*time.Hour.Milliseconds():
		return nil, fmt.Errorf("%w: a waiver may last at most %d days (policy.waivers.max_days)", ErrInvalid, e.live().pol.MaxWaiverDays)
	case rq.Rule == policy.RuleSource:
		return nil, fmt.Errorf("%w: a policy source that cannot be loaded cannot be waived; fix the source or take it out of the configuration", ErrInvalid)
	case rq.Finding != "" && rq.Rule != policy.RuleFindings:
		return nil, fmt.Errorf("%w: only the findings rule can be waived for one finding", ErrInvalid)
	case len(rq.Finding) > 200 || len(rq.Run) > 100 || len(rq.Environment) > 63:
		return nil, fmt.Errorf("%w: a value is too long", ErrInvalid)
	}
	set := e.policySet(e.db, now)
	p := set.Policy(rq.Policy)
	if p == nil {
		var names []string
		for _, x := range set.Policies() {
			names = append(names, x.Name)
		}
		return nil, fmt.Errorf("%w: there is no policy called %q in force (there are: %s)", ErrInvalid, rq.Policy, nonEmpty(strings.Join(names, ", "), "none"))
	}
	if !p.HasRule(rq.Rule) {
		return nil, fmt.Errorf("%w: policy %s has no rule %q (it has %s)", ErrInvalid, p.Name, rq.Rule, strings.Join(p.RuleList(), ", "))
	}
	if !p.CoversProject(rq.Project) {
		return nil, fmt.Errorf("%w: policy %s does not apply to project %s", ErrInvalid, p.Name, rq.Project)
	}
	if rq.Environment != "" {
		if _, ok := e.live().envs[rq.Environment]; !ok {
			return nil, fmt.Errorf("%w: environment %q is not configured on this server", ErrInvalid, rq.Environment)
		}
		if policy.PointOf(rq.Rule) != policy.PointDeploy {
			return nil, fmt.Errorf("%w: only deploy rules can be waived for one environment", ErrInvalid)
		}
	}
	runNumber := 0
	if rq.Run != "" {
		st, err := e.State(rq.Run)
		if errors.Is(err, ErrNotFound) || (err == nil && st.Project != rq.Project) {
			return nil, fmt.Errorf("%w: project %s has no run %s", ErrInvalid, rq.Project, rq.Run)
		}
		if err != nil {
			return nil, err
		}
		runNumber = st.Number
		// The person who started a run may not waive a deploy gate of it on
		// a protected environment, unless people may approve their own runs.
		if policy.PointOf(rq.Rule) == policy.PointDeploy && !st.selfApproval(e) && rq.Actor != "" && rq.Actor == st.Trigger.Actor {
			for _, env := range e.Environments() {
				if !env.Protected || (rq.Environment != "" && env.Name != rq.Environment) {
					continue
				}
				if p.AppliesTo(policy.PointDeploy, rq.Project, st.Trigger.Branch, st.Trigger.Event, env.Name) {
					return nil, fmt.Errorf("%w: you started run #%d, and environment %s is protected: someone else must waive its deploy gate",
						ErrForbidden, st.Number, env.Name)
				}
			}
		}
	}
	w := policy.Waiver{ID: ids.New("wvr"), Project: rq.Project, Policy: rq.Policy, Rule: rq.Rule, Run: rq.Run, Environment: rq.Environment,
		Finding: rq.Finding, Reason: rq.Reason, GrantedBy: rq.Actor, GrantedAt: now, ExpiresAt: rq.ExpiresAt}
	if err := e.waiverTx(func(tx *txx) error {
		data, _ := json.Marshal(waiverEvent{Waiver: &w})
		if _, err := tx.Exec(`INSERT INTO policy_waiver_events(waiver_id, type, at, actor, data) VALUES(?,?,?,?,?)`, w.ID, "granted", now, rq.Actor, string(data)); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO policy_waivers(id, project, policy, rule, run_id, environment, finding, reason, granted_by, granted_at, expires_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.Project, w.Policy, w.Rule, w.Run, w.Environment, w.Finding, w.Reason, w.GrantedBy, w.GrantedAt, w.ExpiresAt)
		return err
	}); err != nil {
		return nil, err
	}
	return &WaiverView{Waiver: w, RunNumber: runNumber, State: "active"}, nil
}

// RevokeWaiver ends a waiver before it expires.
func (e *Engine) RevokeWaiver(project, id, actor, reason string) (*WaiverView, error) {
	if !e.knows(schemaPolicy) {
		return nil, fmt.Errorf("%w: waiver %s", ErrNotFound, id)
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > policy.MaxReason {
		return nil, fmt.Errorf("%w: the reason is longer than %d characters", ErrInvalid, policy.MaxReason)
	}
	now := e.now().UnixMilli()
	err := e.waiverTx(func(tx *txx) error {
		var revoked int64
		var proj string
		err := tx.QueryRow(`SELECT project, revoked_at FROM policy_waivers WHERE id = ?`, id).Scan(&proj, &revoked)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && proj != project) {
			return fmt.Errorf("%w: waiver %s", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		if revoked != 0 {
			return fmt.Errorf("%w: waiver %s is already revoked", ErrConflict, id)
		}
		data, _ := json.Marshal(waiverEvent{Reason: reason})
		if _, err := tx.Exec(`INSERT INTO policy_waiver_events(waiver_id, type, at, actor, data) VALUES(?,?,?,?,?)`, id, "revoked", now, actor, string(data)); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE policy_waivers SET revoked_by = ?, revoked_at = ?, revoke_reason = ? WHERE id = ? AND revoked_at = 0`, actor, now, reason, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	ws, err := e.Waivers(WaiverFilter{ID: id})
	if err != nil || len(ws) == 0 {
		return nil, err
	}
	return &ws[0], nil
}

func (e *Engine) waiverTx(fn func(tx *txx) error) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// WaiverFilter narrows Waivers.
type WaiverFilter struct {
	ID       string
	Project  string
	Run      string // waivers that apply to this run: of its project, for every run or this one
	Active   bool   // only waivers in force now
	Projects []string
	Limit    int
}

// Waivers lists waivers, newest first.
func (e *Engine) Waivers(f WaiverFilter) ([]WaiverView, error) {
	out := []WaiverView{}
	if !e.knows(schemaPolicy) || (f.Projects != nil && len(f.Projects) == 0) {
		return out, nil
	}
	now := e.now().UnixMilli()
	q := `SELECT w.id, w.project, w.policy, w.rule, w.run_id, w.environment, w.finding, w.reason, w.granted_by, w.granted_at, w.expires_at,
		w.revoked_by, w.revoked_at, w.revoke_reason, COALESCE(r.number, 0)
		FROM policy_waivers w LEFT JOIN runs r ON r.id = w.run_id WHERE 1 = 1`
	var args []any
	if f.ID != "" {
		q += ` AND w.id = ?`
		args = append(args, f.ID)
	}
	if f.Project != "" {
		q += ` AND w.project = ?`
		args = append(args, f.Project)
	}
	if f.Run != "" {
		q += ` AND (w.run_id = '' OR w.run_id = ?)`
		args = append(args, f.Run)
	}
	if f.Active {
		q += ` AND w.revoked_at = 0 AND w.expires_at > ?`
		args = append(args, now)
	}
	if f.Projects != nil {
		q += ` AND w.project IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.Projects)), ",") + `)`
		for _, p := range f.Projects {
			args = append(args, p)
		}
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	q += ` ORDER BY w.granted_at DESC, w.id LIMIT ?`
	args = append(args, f.Limit)
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v WaiverView
		w := &v.Waiver
		if err := rows.Scan(&w.ID, &w.Project, &w.Policy, &w.Rule, &w.Run, &w.Environment, &w.Finding, &w.Reason, &w.GrantedBy, &w.GrantedAt,
			&w.ExpiresAt, &v.RevokedBy, &v.RevokedAt, &v.RevokeReason, &v.RunNumber); err != nil {
			return nil, err
		}
		switch {
		case v.RevokedAt != 0:
			v.State = "revoked"
		case now >= w.ExpiresAt:
			v.State = "expired"
		default:
			v.State = "active"
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- reading decisions ----

// PolicyDecision is one policy.evaluated event of a run.
type PolicyDecision struct {
	Seq int64 `json:"seq"`
	policy.Evaluation
}

// RunPolicy lists a run's policy decisions, oldest first.
func (e *Engine) RunPolicy(runID string) ([]PolicyDecision, error) {
	out := []PolicyDecision{}
	if _, err := e.RunProject(runID); err != nil {
		return nil, err
	}
	if !e.knows(schemaPolicy) {
		return out, nil
	}
	rows, err := e.db.Query(`SELECT seq, data FROM events WHERE run_id = ? AND type = ? ORDER BY run_seq`, runID, PolicyEvaluated)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d PolicyDecision
		var data string
		if err := rows.Scan(&d.Seq, &data); err != nil {
			return nil, err
		}
		var p Payload
		if json.Unmarshal([]byte(data), &p) != nil || p.Policy == nil {
			continue
		}
		d.Evaluation = *p.Policy
		out = append(out, d)
	}
	return out, rows.Err()
}

// PolicySourceView is a source of policies, as the Policy page shows it.
type PolicySourceView struct {
	Name         string          `json:"name"`
	Kind         string          `json:"kind"` // config or git
	Library      string          `json:"library,omitempty"`
	Ref          string          `json:"ref,omitempty"`
	Path         string          `json:"path,omitempty"`
	Projects     []string        `json:"projects"`
	Version      string          `json:"version"`
	LoadedAt     int64           `json:"loaded_at,omitempty"`
	CheckedAt    int64           `json:"checked_at,omitempty"`
	FetchError   string          `json:"fetch_error,omitempty"`
	FetchErrorAt int64           `json:"fetch_error_at,omitempty"`
	Error        string          `json:"error,omitempty"` // set: it fails closed
	Policies     []policy.Policy `json:"policies"`
}

// PolicyVerdict is one policy's verdict in one decision.
type PolicyVerdict struct {
	Policy      string `json:"policy"`
	RunID       string `json:"run_id"`
	Project     string `json:"project"`
	RunNumber   int    `json:"run_number"`
	Point       string `json:"point"`
	Step        string `json:"step,omitempty"`
	Environment string `json:"environment,omitempty"`
	Version     string `json:"version"`
	Mode        string `json:"mode"`
	Verdict     string `json:"verdict"`
	At          int64  `json:"at"`
}

// PolicyOverview is everything the Policy page shows.
type PolicyOverview struct {
	Catalogue     []policy.RuleInfo  `json:"catalogue"`
	Sources       []PolicySourceView `json:"sources"`
	Recent        []PolicyVerdict    `json:"recent"`
	MaxWaiverDays int                `json:"max_waiver_days"`
}

// PolicyOverview lists the policies in force, their sources, and recent
// verdicts of runs of onlyProjects (nil: every project).
func (e *Engine) PolicyOverview(onlyProjects []string, recentPerPolicy int) (*PolicyOverview, error) {
	o := &PolicyOverview{Catalogue: policy.Catalogue, Sources: []PolicySourceView{}, Recent: []PolicyVerdict{}, MaxWaiverDays: e.live().pol.MaxWaiverDays}
	if !e.knows(schemaPolicy) {
		return o, nil
	}
	set := e.policySet(e.db, e.now().UnixMilli())
	cfg := map[string]PolicySourceConfig{}
	for _, sc := range e.live().pol.Sources {
		cfg[sc.Name] = sc
	}
	var names []string
	for _, src := range set.Sources {
		v := PolicySourceView{Name: src.Name, Kind: "git", Projects: src.Projects, Version: src.Version, Error: src.Error, Ref: src.Ref,
			Policies: src.Policies}
		if v.Projects == nil {
			v.Projects = []string{}
		}
		if src.Name == configSource {
			v.Kind = "config"
		} else {
			sc := cfg[src.Name]
			v.Library, v.Path = sc.Library, sc.Path
			if row, err := readSourceRow(e.db, src.Name); err == nil {
				v.LoadedAt, v.CheckedAt, v.FetchError, v.FetchErrorAt = row.LoadedAt, row.CheckedAt, row.FetchError, row.FetchErrorAt
				if v.Policies == nil && row.Policies != "" {
					// Show what was loaded even while it fails closed.
					_ = json.Unmarshal([]byte(row.Policies), &v.Policies)
				}
			}
		}
		if v.Policies == nil {
			v.Policies = []policy.Policy{}
		}
		for _, p := range v.Policies {
			names = append(names, p.Name)
		}
		o.Sources = append(o.Sources, v)
	}
	if onlyProjects != nil && len(onlyProjects) == 0 {
		return o, nil
	}
	if recentPerPolicy <= 0 {
		recentPerPolicy = 10
	}
	for _, name := range names {
		q := `SELECT policy, run_id, project, run_number, point, step_id, environment, version, mode, verdict, at FROM policy_evaluations WHERE policy = ?`
		args := []any{name}
		if onlyProjects != nil {
			q += ` AND project IN (` + strings.TrimSuffix(strings.Repeat("?,", len(onlyProjects)), ",") + `)`
			for _, p := range onlyProjects {
				args = append(args, p)
			}
		}
		q += ` ORDER BY at DESC, seq DESC LIMIT ?`
		args = append(args, recentPerPolicy)
		rows, err := e.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v PolicyVerdict
			if err := rows.Scan(&v.Policy, &v.RunID, &v.Project, &v.RunNumber, &v.Point, &v.Step, &v.Environment, &v.Version, &v.Mode, &v.Verdict, &v.At); err != nil {
				rows.Close()
				return nil, err
			}
			o.Recent = append(o.Recent, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return o, nil
}

// PolicyNames lists the policies in force with the rules each uses.
func (e *Engine) PolicyNames() map[string][]string {
	out := map[string][]string{}
	if !e.knows(schemaPolicy) {
		return out
	}
	for _, p := range e.policySet(e.db, e.now().UnixMilli()).Policies() {
		out[p.Name] = p.RuleList()
	}
	return out
}

// policySummary sums up a run's policy decisions: the latest at each
// point (the run, and each step).
func policySummary(st *RunState) *RunPolicySummary {
	if len(st.Policy) == 0 {
		return nil
	}
	latest := map[string]*policy.Evaluation{}
	for _, ev := range st.Policy {
		latest[ev.Point+"/"+ev.Step] = ev
	}
	sum := &RunPolicySummary{Decisions: len(st.Policy), Verdict: policy.VerdictPass}
	rank := map[string]int{policy.VerdictPass: 0, policy.VerdictWarn: 1, policy.VerdictBlock: 2}
	for _, ev := range latest {
		if rank[ev.Verdict] > rank[sum.Verdict] {
			sum.Verdict = ev.Verdict
		}
		for _, p := range ev.Policies {
			for _, r := range p.Rules {
				if r.Verdict == policy.ResultWaived {
					sum.Waived++
				}
			}
		}
	}
	return sum
}

// MaxWaiverDays is the longest a waiver may last.
func (e *Engine) MaxWaiverDays() int { return e.live().pol.MaxWaiverDays }
