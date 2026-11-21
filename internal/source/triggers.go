package source

// Run inputs, schedules and webhooks on the source side: binding a
// pipeline to a run's values before it is expanded and created, firing the
// default branch's schedules, starting runs from webhook triggers, and
// polling at once when a Git host says something changed.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// SecretSource says where the server reads a secret: an environment
// variable or a file (such as a mounted Kubernetes Secret). It is read
// each time it is needed, so a rotated file takes effect at once. The value
// itself is never in the configuration or the database.
type SecretSource struct {
	Env  string
	File string
}

// Read returns the secret, without surrounding white space.
func (s SecretSource) Read() ([]byte, error) {
	var v string
	switch {
	case s.Env != "":
		v = os.Getenv(s.Env)
	case s.File != "":
		b, err := os.ReadFile(s.File)
		if err != nil {
			return nil, err
		}
		v = string(b)
	default:
		return nil, errors.New("no secret is configured")
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, errors.New("the secret is empty")
	}
	return []byte(v), nil
}

// GitHook is how a project's Git host proves its webhook calls: the host's
// kind (github, gitea, forgejo or gitlab) and the secret.
type GitHook struct {
	Kind   string
	Secret SecretSource
}

// TriggerAuth is what callers of one webhook trigger must prove: a bearer
// token, or an HMAC signature of the body.
type TriggerAuth struct {
	Token SecretSource
	HMAC  SecretSource
}

// UsesHMAC reports whether callers sign their requests.
func (a *TriggerAuth) UsesHMAC() bool { return a.HMAC.Env != "" || a.HMAC.File != "" }

// triggerState is what the manager keeps for schedules and webhooks.
type triggerState struct {
	mu        sync.Mutex
	loopLimit int
	schedules map[string]*projectSchedules // by project
	wake      map[string]chan struct{}     // by project: poll at once
}

// projectSchedules are a project's schedules as last read from its default
// branch.
type projectSchedules struct {
	sha       string
	list      []*pipeline.Schedule
	problem   string
	readAt    time.Time
	triggers  []*pipeline.WebhookTrigger
	hasInputs bool
}

func (m *Manager) ts() *triggerState {
	m.trigOnce.Do(func() {
		m.trig = &triggerState{schedules: map[string]*projectSchedules{}, wake: map[string]chan struct{}{}}
		for _, name := range m.c().order {
			m.trig.wake[name] = make(chan struct{}, 1)
		}
	})
	return m.trig
}

// SetLoopLimit sets the most combinations one loop may have; 0 is the
// default.
func (m *Manager) SetLoopLimit(n int) {
	t := m.ts()
	t.mu.Lock()
	t.loopLimit = n
	t.mu.Unlock()
}

func (m *Manager) bindOptions() pipeline.BindOptions {
	t := m.ts()
	t.mu.Lock()
	defer t.mu.Unlock()
	return pipeline.BindOptions{MaxLoopMembers: t.loopLimit}
}

// prepare makes a run's graph from a parsed pipeline: it checks the given
// input values and completes them with defaults, binds the pipeline to
// them and expands its templates. Problems with the values are
// pipeline.InputErrors; with the pipeline, pipeline.Problems.
func (m *Manager) prepare(ctx context.Context, project string, pl *pipeline.Pipeline, given, overrides map[string]string) (*pipeline.Pipeline, []engine.LibrarySource, map[string]string, *varRecord, error) {
	vars, env, err := m.bindVariables(ctx, project, pl, given)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	vals, used, err := pipeline.ResolveInputsWith(pl, given, vars)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	b, err := pipeline.Bind(pl, vals, m.bindOptions())
	if err != nil {
		return nil, nil, nil, nil, err
	}
	x, libs, used2, err := m.expandVars(ctx, b, overrides, 0, vars)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(vals) == 0 {
		vals = nil
	}
	return x, libs, vals, &varRecord{env: env, used: mergeUsed(used, used2)}, nil
}

// preview binds a pipeline for showing it: with the defaults, loops whose
// values are not known standing aside.
func (m *Manager) preview(pl *pipeline.Pipeline) (*pipeline.Pipeline, error) {
	return pipeline.Preview(pl, m.bindOptions())
}

// StartRequest is a run started by a person.
type StartRequest struct {
	Project string
	Ref     string // a branch or tag; empty is the default branch
	Actor   string
	// Libraries overrides the versions of libraries, as for StartRun.
	Libraries map[string]string
	// Inputs are the values given for the pipeline's inputs, as text (see
	// pipeline.Input.Canon); those not given take their defaults.
	Inputs map[string]string
}

// StartRunWith starts a run by hand, with input values.
func (m *Manager) StartRunWith(ctx context.Context, rq StartRequest) (*engine.RunSummary, error) {
	p := m.c().projects[rq.Project]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, rq.Project)
	}
	if err := CheckOverrides(rq.Libraries); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, err := p.resolve(ctx, rq.Ref, false)
	if err != nil {
		return nil, err
	}
	pl, _, err := p.parsedAt(ctx, r.SHA)
	if err != nil {
		return nil, startProblem(r, err)
	}
	pl, libs, vals, rec, err := m.prepare(ctx, rq.Project, pl, rq.Inputs, rq.Libraries)
	if err != nil {
		return nil, startProblem(r, err)
	}
	t := trigger(r, pipeline.EventManual, rq.Actor)
	t.Inputs = vals
	return m.createRun(ctx, engine.NewRun{Project: rq.Project, Pipeline: pl, Trigger: t,
		Source: engine.Source{Kind: "git", URL: p.URL, Libraries: libs}}, rec)
}

// startProblem passes on problems a caller can fix, and words others.
func startProblem(r Ref, err error) error {
	var problems pipeline.Problems
	var inputs pipeline.InputErrors
	switch {
	case errors.As(err, &problems), errors.As(err, &inputs), errors.Is(err, engine.ErrInvalid), errors.Is(err, engine.ErrNotFound):
		return err
	case errors.Is(err, ErrNoPipeline):
		return fmt.Errorf("%w: %s at %s has no .launchway/pipeline.yaml", engine.ErrNotFound, r.Name, short(r.SHA))
	}
	return fmt.Errorf("%w: %s at %s: %v", engine.ErrConflict, r.Name, short(r.SHA), err)
}

// InputsInfo is what a form for starting a run needs: the pipeline's
// inputs at a ref, or its problems.
type InputsInfo struct {
	Ref      string            `json:"ref"`
	SHA      string            `json:"sha"`
	Inputs   []*pipeline.Input `json:"inputs"`
	Problems pipeline.Problems `json:"problems,omitempty"`
	// Libraries are the libraries the pipeline uses, which may be
	// overridden.
	Libraries []string `json:"libraries"`
}

// InputsAt reads the inputs of the pipeline at a branch or tag.
func (m *Manager) InputsAt(ctx context.Context, name, ref string) (*InputsInfo, error) {
	p := m.c().projects[name]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, err := p.resolve(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	info := &InputsInfo{Ref: r.Name, SHA: r.SHA, Inputs: []*pipeline.Input{}, Libraries: []string{}}
	pl, _, err := p.parsedAt(ctx, r.SHA)
	var problems pipeline.Problems
	switch {
	case errors.As(err, &problems):
		info.Problems = problems
		return info, nil
	case errors.Is(err, ErrNoPipeline):
		info.Problems = pipeline.Problems{{Message: "there is no .launchway/pipeline.yaml at this commit"}}
		return info, nil
	case err != nil:
		return nil, err
	}
	if pl.Inputs != nil {
		info.Inputs = pl.Inputs
	}
	for _, l := range pl.Libraries {
		info.Libraries = append(info.Libraries, l.Name)
	}
	return info, nil
}

// ---- polling at once ----

func (m *Manager) wakeChan(name string) <-chan struct{} {
	t := m.ts()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.wake[name]
}

// PollNow asks for a project to be polled at once, as when its Git host
// says something changed. Requests while one is pending are one request.
// It reports false for a project the server does not have.
func (m *Manager) PollNow(name string) bool {
	t := m.ts()
	t.mu.Lock()
	ch := t.wake[name]
	t.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- struct{}{}:
	default:
	}
	return true
}

// Hook returns how a project's Git host proves its webhook calls; nil if
// the project takes none (or does not exist).
func (m *Manager) Hook(name string) *GitHook {
	if p := m.c().projects[name]; p != nil {
		return p.Hook
	}
	return nil
}

// TriggerAuth returns what callers of a project's webhook trigger must
// prove; nil if the server's configuration has nothing for it, in which
// case the trigger cannot be called.
func (m *Manager) TriggerAuth(project, trigger string) *TriggerAuth {
	if p := m.c().projects[project]; p != nil {
		return p.Triggers[trigger]
	}
	return nil
}

// ---- schedules ----

// scheduleTick is how often schedules are looked at.
var scheduleTick = 10 * time.Second

// refreshSchedules reads the schedules and webhook triggers of the
// pipeline at the tip of the default branch. The caller holds p.mu.
func (m *Manager) refreshSchedules(ctx context.Context, p *project, refs []Ref) {
	ps := &projectSchedules{readAt: time.Now()}
	found := false
	for _, r := range refs {
		if r.Kind != "branch" || r.Name != p.DefaultBranch {
			continue
		}
		found = true
		ps.sha = r.SHA
		pl, _, err := p.parsedAt(ctx, r.SHA)
		switch {
		case errors.Is(err, ErrNoPipeline):
		case err != nil:
			ps.problem = "the pipeline on the default branch has problems, so its schedules and webhooks are not read:\n" + redact.URLs(err.Error())
		default:
			ps.list, ps.triggers, ps.hasInputs = pl.On.Schedule, pl.On.Webhook, len(pl.Inputs) > 0
		}
	}
	if !found {
		ps.problem = "the default branch, " + p.DefaultBranch + ", does not exist"
	}
	t := m.ts()
	t.mu.Lock()
	if old := t.schedules[p.Name]; old != nil && ps.problem != "" && old.problem == "" && old.sha == ps.sha {
		// A passing failure to read: keep what was read.
		ps = old
	}
	t.schedules[p.Name] = ps
	t.mu.Unlock()
}

func (m *Manager) schedulesOf(name string) *projectSchedules {
	t := m.ts()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.schedules[name]
}

// runSchedules looks at every project's schedules every scheduleTick until
// ctx ends, and fires those that are due.
func (m *Manager) runSchedules(ctx context.Context) {
	if !m.eng.Triggers() {
		return
	}
	tick := time.NewTicker(scheduleTick)
	defer tick.Stop()
	for {
		cat := m.c()
		for _, name := range cat.order {
			if ctx.Err() != nil {
				return
			}
			m.fireDue(ctx, cat.projects[name], time.Now())
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// fireDue fires a project's schedules that are due at now.
//
// The rule: a schedule fires for a time T if T is after the moment any
// server first saw the schedule (so adding or changing one never fires for
// the past) and after the last time it fired for. If several such times
// have passed, because no server was running then, it fires once, for the
// latest of them, and the earlier ones are skipped. Each firing is claimed
// in the database, so exactly one server acts on it. While a project's
// schedules are turned off in the server's configuration, its firings are
// claimed as skipped, so turning them on again does not catch up.
func (m *Manager) fireDue(ctx context.Context, p *project, now time.Time) {
	ps := m.schedulesOf(p.Name)
	if ps == nil {
		return
	}
	for _, s := range ps.list {
		key := s.Key()
		first, last, err := m.eng.ScheduleSeen(p.Name, key, now.UnixMilli())
		if err != nil {
			m.log.Warn("reading a schedule's state", "project", p.Name, "err", err)
			return
		}
		from := time.UnixMilli(max(first, last))
		due := s.Latest(from, now)
		if due.IsZero() {
			continue
		}
		f := engine.Firing{Key: key, At: due.UnixMilli()}
		if p.SchedulesOff {
			if _, err := m.eng.RecordFire(p.Name, f, "skipped", "schedules are turned off for this project in the server's configuration"); err != nil {
				m.log.Warn("recording a skipped firing", "project", p.Name, "err", err)
			}
			continue
		}
		if err := m.fire(ctx, p, s, f, due); err != nil {
			if _, rerr := m.eng.RecordFire(p.Name, f, "error", redact.URLs(err.Error())); rerr != nil {
				m.log.Warn("recording a failed firing", "project", p.Name, "err", rerr)
			}
		}
	}
}

// fire starts the run of one firing. ErrAlreadyClaimed is not an error:
// another server started it.
func (m *Manager) fire(ctx context.Context, p *project, s *pipeline.Schedule, f engine.Firing, due time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, err := p.resolve(ctx, s.Ref, false)
	if err != nil {
		return err
	}
	pl, _, err := p.parsedAt(ctx, r.SHA)
	if err != nil {
		return startProblem(r, err)
	}
	pl, libs, vals, rec, err := m.prepare(ctx, p.Name, pl, s.Inputs, nil)
	if err != nil {
		return startProblem(r, err)
	}
	t := trigger(r, pipeline.EventSchedule, "schedule")
	t.Inputs, t.Schedule, t.ScheduledFor = vals, describeSchedule(s), f.At
	run, err := m.createRun(ctx, engine.NewRun{Project: p.Name, Pipeline: pl, Trigger: t, Fire: &f,
		Source: engine.Source{Kind: "git", URL: p.URL, Libraries: libs}}, rec)
	switch {
	case errors.Is(err, engine.ErrAlreadyClaimed):
		return nil
	case err != nil:
		return err
	}
	m.log.Info("a schedule started a run", "project", p.Name, "schedule", t.Schedule, "for", due.UTC().Format(time.RFC3339), "run", run.Number)
	return nil
}

func describeSchedule(s *pipeline.Schedule) string {
	tz := s.Timezone
	if tz == "" {
		tz = "UTC"
	}
	return s.Cron + " (" + tz + ")"
}

// ScheduleView is one schedule as the UI shows it.
type ScheduleView struct {
	Key      string            `json:"key"`
	Cron     string            `json:"cron"`
	Timezone string            `json:"timezone"`
	Ref      string            `json:"ref"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Next     int64             `json:"next"` // Unix ms; 0 if never
	// Last is the latest firing recorded; nil if none yet.
	Last *engine.ScheduleFire `json:"last,omitempty"`
}

// SchedulesView is a project's schedules and their recent firings.
type SchedulesView struct {
	Enabled bool   `json:"enabled"`
	Branch  string `json:"branch"`        // the default branch, which they are read from
	SHA     string `json:"sha,omitempty"` // its commit when they were read
	Problem string `json:"problem,omitempty"`
	// ReadAt is when this server last read them (Unix ms); 0 if it has
	// not yet.
	ReadAt    int64                 `json:"read_at"`
	Schedules []ScheduleView        `json:"schedules"`
	Fires     []engine.ScheduleFire `json:"fires"`
	// Webhooks are the names of the webhook triggers the default branch
	// declares, with whether the server's configuration lets them be
	// called.
	Webhooks []WebhookView `json:"webhooks"`
	// GitHook says whether the project takes its Git host's webhooks, and
	// of which kind.
	GitHook string `json:"git_hook,omitempty"`
}

// WebhookView is a webhook trigger as the UI shows it.
type WebhookView struct {
	Name       string            `json:"name"`
	Ref        string            `json:"ref"`
	Inputs     map[string]string `json:"inputs,omitempty"`
	Configured bool              `json:"configured"`
	Auth       string            `json:"auth,omitempty"` // token or hmac
}

// Schedules lists a project's schedules, as read from its default branch,
// with when each fires next and the recent firings.
func (m *Manager) Schedules(name string) (*SchedulesView, error) {
	p := m.c().projects[name]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	v := &SchedulesView{Enabled: !p.SchedulesOff, Branch: p.DefaultBranch, Schedules: []ScheduleView{}, Webhooks: []WebhookView{}}
	if p.Hook != nil {
		v.GitHook = p.Hook.Kind
	}
	fires, err := m.eng.ScheduleFires(name)
	if err != nil {
		return nil, err
	}
	v.Fires = fires
	ps := m.schedulesOf(name)
	if ps == nil {
		return v, nil
	}
	v.SHA, v.Problem, v.ReadAt = ps.sha, ps.problem, ps.readAt.UnixMilli()
	now := time.Now()
	for _, s := range ps.list {
		sv := ScheduleView{Key: s.Key(), Cron: s.Cron, Timezone: s.Timezone, Ref: s.Ref, Inputs: s.Inputs}
		if sv.Timezone == "" {
			sv.Timezone = "UTC"
		}
		if sv.Ref == "" {
			sv.Ref = p.DefaultBranch
		}
		if n := s.Next(now); !n.IsZero() && v.Enabled {
			sv.Next = n.UnixMilli()
		}
		for i := range fires {
			if fires[i].Key == sv.Key {
				f := fires[i]
				sv.Last = &f
				break
			}
		}
		v.Schedules = append(v.Schedules, sv)
	}
	for _, w := range ps.triggers {
		wv := WebhookView{Name: w.Name, Ref: w.Ref, Inputs: w.Inputs}
		if wv.Ref == "" {
			wv.Ref = p.DefaultBranch
		}
		if a := p.Triggers[w.Name]; a != nil {
			wv.Configured, wv.Auth = true, "token"
			if a.UsesHMAC() {
				wv.Auth = "hmac"
			}
		}
		v.Webhooks = append(v.Webhooks, wv)
	}
	sort.SliceStable(v.Webhooks, func(i, j int) bool { return v.Webhooks[i].Name < v.Webhooks[j].Name })
	return v, nil
}

// ---- webhook triggers ----

// WebhookRequest is a call to a webhook trigger, already authenticated.
type WebhookRequest struct {
	Project, Name string
	Body          []byte
	// Key is the caller's idempotency key, if it sent one, and BodyHash a
	// hash of the body, to tell a repeat from a different call that reuses
	// the key.
	Key, BodyHash string
}

// ErrKeyReused means an idempotency key came again with a different body.
var ErrKeyReused = errors.New("the idempotency key was used before with a different body")

// StartWebhook starts the run a webhook trigger asks for. The trigger is
// read from the pipeline at the tip of the default branch; the pipeline at
// its ref is what runs. With an idempotency key that was used before (by
// any server), it returns the run the first call started, and repeat true.
func (m *Manager) StartWebhook(ctx context.Context, rq WebhookRequest) (run *engine.RunSummary, repeat bool, err error) {
	p := m.c().projects[rq.Project]
	if p == nil {
		return nil, false, fmt.Errorf("%w: project %s", engine.ErrNotFound, rq.Project)
	}
	if rq.Key != "" {
		if run, err := m.repeated(rq); run != nil || err != nil {
			return run, run != nil, err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	def, err := p.resolve(ctx, "", true)
	if err != nil {
		return nil, false, err
	}
	dpl, _, err := p.parsedAt(ctx, def.SHA)
	if err != nil {
		return nil, false, fmt.Errorf("%w: the pipeline on the default branch, which declares the webhooks, cannot be read: %v", engine.ErrConflict, err)
	}
	w := dpl.On.WebhookNamed(rq.Name)
	if w == nil {
		return nil, false, fmt.Errorf("%w: the pipeline on the default branch has no webhook called %s", engine.ErrNotFound, rq.Name)
	}
	given, err := w.Values(rq.Body)
	if err != nil {
		var ie pipeline.InputErrors
		if errors.As(err, &ie) {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("%w: %v", engine.ErrInvalid, err)
	}
	r := def
	if w.Ref != "" {
		if r, err = p.resolve(ctx, w.Ref, false); err != nil {
			return nil, false, err
		}
	}
	pl := dpl
	if r.SHA != def.SHA {
		if pl, _, err = p.parsedAt(ctx, r.SHA); err != nil {
			return nil, false, startProblem(r, err)
		}
	}
	pl, libs, vals, rec, err := m.prepare(ctx, rq.Project, pl, given, nil)
	if err != nil {
		return nil, false, startProblem(r, err)
	}
	t := trigger(r, pipeline.EventWebhook, "webhook:"+rq.Name)
	t.Inputs, t.Webhook = vals, rq.Name
	nr := engine.NewRun{Project: rq.Project, Pipeline: pl, Trigger: t, Source: engine.Source{Kind: "git", URL: p.URL, Libraries: libs}}
	if rq.Key != "" {
		nr.Delivery = &engine.Delivery{Hook: rq.Name, Key: rq.Key, BodyHash: rq.BodyHash}
	}
	run, err = m.createRun(ctx, nr, rec)
	if errors.Is(err, engine.ErrAlreadyClaimed) {
		// Another call with the key got there first, a moment ago.
		run, err = m.repeated(rq)
		if run == nil && err == nil {
			err = fmt.Errorf("%w: the idempotency key is being used by another call", engine.ErrConflict)
		}
		return run, run != nil, err
	}
	return run, false, err
}

// repeated returns the run an idempotency key started, if it was used.
func (m *Manager) repeated(rq WebhookRequest) (*engine.RunSummary, error) {
	runID, hash, err := m.eng.DeliveryRun(rq.Project, rq.Name, rq.Key)
	if errors.Is(err, engine.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hash != rq.BodyHash {
		return nil, fmt.Errorf("%w: %w", engine.ErrConflict, ErrKeyReused)
	}
	return m.eng.Summary(runID)
}
