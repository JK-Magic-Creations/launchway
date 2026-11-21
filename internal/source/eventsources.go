package source

// Event sources: what happens to a call after the API has proved who sent it
// (internal/api/event_sources.go). The body is checked against the source's
// schema, filtered, deduplicated, mapped to inputs, and then starts a run,
// waits in a debounce or batch window, or is recorded with the reason it did
// none of these.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/eventexpr"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// SourceCall is a call to an event source that is verified, within its size
// and rate limits, and read.
type SourceCall struct {
	Source  engine.EventSource
	Body    []byte
	Headers map[string]string // lower-case names; credentials already left out
}

// SourceResult says what became of a call.
type SourceResult struct {
	// Status is one of the event decisions: started, held, waiting, filtered,
	// duplicate, refused or error.
	Status   string              `json:"status"`
	EventID  string              `json:"event_id,omitempty"`
	Reason   string              `json:"reason"`
	Problems []string            `json:"problems,omitempty"`
	Inputs   map[string]string   `json:"inputs,omitempty"`
	Run      *engine.RunSummary  `json:"run,omitempty"`
	Filter   *bool               `json:"filter,omitempty"`
	Key      string              `json:"dedupe_key,omitempty"`
	Window   *engine.EventWindow `json:"-"`
	Dry      bool                `json:"dry_run,omitempty"`
}

func triggerOf(s engine.EventSource) string { return "source:" + s.Name }

func (c SourceCall) vars(body any) map[string]any {
	h := make(map[string]any, len(c.Headers))
	for k, v := range c.Headers {
		h[k] = v
	}
	return map[string]any{"body": body, "headers": h, "source": c.Source.Name}
}

// CheckSourceExpressions reports what is wrong with the expressions of a
// source, so that a mistake is found when it is saved, not when a call comes.
func CheckSourceExpressions(s engine.EventSource) error {
	check := func(what, src string) error {
		if strings.TrimSpace(src) == "" {
			return nil
		}
		if _, err := eventexpr.Parse(src); err != nil {
			return fmt.Errorf("%w: %s: %v", engine.ErrInvalid, what, err)
		}
		return nil
	}
	if err := check("the filter", s.Filter); err != nil {
		return err
	}
	if err := check("the dedupe key", s.DedupeKey); err != nil {
		return err
	}
	names := make([]string, 0, len(s.Mapping))
	for k := range s.Mapping {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if strings.TrimSpace(s.Mapping[k]) == "" {
			return fmt.Errorf("%w: the mapping of input %s is empty", engine.ErrInvalid, k)
		}
		if err := check("the mapping of input "+k, s.Mapping[k]); err != nil {
			return err
		}
	}
	if _, err := eventexpr.CompileSchema(s.Schema); err != nil {
		return fmt.Errorf("%w: %v", engine.ErrInvalid, err)
	}
	return nil
}

// ReceiveEvent judges a call to an event source. With dry true it records
// nothing and starts nothing: it says what would happen (a test delivery).
func (m *Manager) ReceiveEvent(ctx context.Context, project string, c SourceCall, dry bool) (*SourceResult, error) {
	src := c.Source
	res := &SourceResult{Dry: dry}
	ev := engine.EventRecord{Project: project, Source: engine.SourceEventSource, SourceName: src.Name, Verified: true,
		Summary: "Call to event source " + src.Name, Payload: engine.ScrubPayload(c.Body)}
	trig := triggerOf(src)
	// end records an event that starts nothing, and answers.
	end := func(decision, reason string, problems ...string) (*SourceResult, error) {
		res.Status, res.Reason, res.Problems = decision, reason, problems
		if !dry {
			id, err := m.eng.RecordEvent(ev, engine.EventDecision{Trigger: trig, Decision: decision, Reason: reason + strings.Join(prefixed(problems), "")})
			if err != nil {
				return nil, err
			}
			res.EventID = id
		}
		return res, nil
	}

	body, err := eventexpr.ParseBody(c.Body)
	if err != nil {
		return end(engine.DecError, "the body is not JSON: "+err.Error())
	}
	schema, err := eventexpr.CompileSchema(src.Schema)
	if err != nil {
		return end(engine.DecError, "the source's schema cannot be used: "+err.Error())
	}
	if problems := schema.Check(c.Body); len(problems) > 0 {
		return end(engine.DecRefused, "the body does not match the source's schema", problems...)
	}
	vars := c.vars(body)

	if src.Filter != "" {
		f, err := eventexpr.Parse(src.Filter)
		if err != nil {
			return end(engine.DecError, "the filter cannot be read: "+err.Error())
		}
		ok, err := f.Bool(vars)
		if err != nil {
			return end(engine.DecError, "the filter could not be judged: "+err.Error())
		}
		res.Filter = &ok
		if !ok {
			return end(engine.DecFiltered, "the filter "+src.Filter+" was false for this body")
		}
	}

	given := map[string]string{}
	var problems []string
	names := make([]string, 0, len(src.Mapping))
	for k := range src.Mapping {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		x, err := eventexpr.Parse(src.Mapping[k])
		if err == nil {
			var text string
			if text, err = x.Text(vars); err == nil {
				given[k] = text
				continue
			}
		}
		problems = append(problems, fmt.Sprintf("input %s: %v", k, err))
	}
	if len(problems) > 0 {
		return end(engine.DecError, "the inputs could not be worked out from the body", problems...)
	}
	res.Inputs = given
	ev.Replay = &engine.ReplayPlan{Ref: src.Ref, Inputs: given}

	key := ""
	if src.DedupeKey != "" {
		x, err := eventexpr.Parse(src.DedupeKey)
		if err == nil {
			key, err = x.Text(vars)
		}
		if err != nil {
			return end(engine.DecError, "the dedupe key could not be worked out: "+err.Error())
		}
		if key == "" {
			return end(engine.DecError, "the dedupe key came out empty for this body, so it cannot tell events apart")
		}
		ev.DedupeKey, res.Key = key, key
	}
	if dry {
		res.Status, res.Reason = "ready", "this body would be accepted; the run's inputs are shown"
		if src.DebounceSecs > 0 || src.BatchSecs > 0 {
			res.Reason += ", after the source's window closes"
		}
		return m.dryRun(ctx, project, src, given, res)
	}

	scope := "src:" + src.ID
	if key != "" {
		claimed, first, err := m.eng.ClaimKey(project, scope, key, "", engine.DedupeMemory)
		if err != nil {
			return nil, err
		}
		if !claimed {
			return end(engine.DecDuplicate, fmt.Sprintf("the key %s was seen before, in event %s", key, first))
		}
	}
	id, err := m.eng.RecordEvent(ev)
	if err != nil {
		return nil, err
	}
	res.EventID = id
	if key != "" {
		m.eng.SetClaimEvent(project, scope, key, id)
	}

	if window, mode := windowOf(src); window > 0 {
		w, prev, err := m.eng.AddToWindow(project, src.Name, mode, window, id, given)
		if err != nil {
			return nil, err
		}
		if prev != "" {
			_ = m.eng.AddDecision(prev, engine.EventDecision{Trigger: trig, Decision: engine.DecSuperseded,
				Reason: fmt.Sprintf("folded into event %s, which arrived in the same %s window; the run uses the newest", id, mode)})
		}
		res.Window = w
		res.Status = engine.DecWaiting
		res.Reason = fmt.Sprintf("held in a %s window of %s: %d event(s) so far, the run starts at %s", mode, window, w.Count, time.UnixMilli(w.DueAt).UTC().Format("15:04:05Z"))
		return res, m.eng.AddDecision(id, engine.EventDecision{Trigger: trig, Decision: engine.DecWaiting, Reason: res.Reason})
	}
	return m.startFromSource(ctx, project, src, id, given, key, res)
}

func prefixed(problems []string) []string {
	out := make([]string, len(problems))
	for i, p := range problems {
		out[i] = "; " + p
	}
	return out
}

func windowOf(s engine.EventSource) (time.Duration, string) {
	switch {
	case s.DebounceSecs > 0:
		return time.Duration(s.DebounceSecs) * time.Second, "debounce"
	case s.BatchSecs > 0:
		return time.Duration(s.BatchSecs) * time.Second, "batch"
	}
	return 0, ""
}

// preparedFor reads the pipeline a source's run would use and checks the
// inputs against it, as a person's start would.
func (m *Manager) preparedFor(ctx context.Context, p *project, src engine.EventSource, given map[string]string) (Ref, *pipeline.Pipeline, []engine.LibrarySource, map[string]string, *varRecord, error) {
	r, err := p.resolve(ctx, src.Ref, false)
	if err != nil {
		return Ref{}, nil, nil, nil, nil, err
	}
	pl, _, err := p.parsedAt(ctx, r.SHA)
	if err != nil {
		return r, nil, nil, nil, nil, startProblem(r, err)
	}
	pl, libs, vals, rec, err := m.prepare(ctx, p.Name, pl, given, nil)
	if err != nil {
		return r, nil, nil, nil, nil, startProblem(r, err)
	}
	return r, pl, libs, vals, rec, nil
}

func inputProblems(ie pipeline.InputErrors) []string {
	out := make([]string, 0, len(ie))
	for k, v := range ie {
		out = append(out, k+": "+v)
	}
	sort.Strings(out)
	return out
}

// dryRun checks a test delivery's inputs against the project's pipeline.
func (m *Manager) dryRun(ctx context.Context, project string, src engine.EventSource, given map[string]string, res *SourceResult) (*SourceResult, error) {
	pp := m.c().projects[project]
	if pp == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, project)
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()
	_, _, _, vals, _, err := m.preparedFor(ctx, pp, src, given)
	if err != nil {
		var ie pipeline.InputErrors
		if errors.As(err, &ie) {
			res.Status, res.Reason = engine.DecError, "the inputs do not fit the pipeline's inputs"
			res.Problems = inputProblems(ie)
			return res, nil
		}
		res.Status, res.Reason, res.Problems = engine.DecError, "the pipeline could not be prepared: "+redact.URLs(err.Error()), nil
		return res, nil
	}
	res.Inputs = vals
	return res, nil
}

// startFromSource creates the run for an event already recorded as eventID.
func (m *Manager) startFromSource(ctx context.Context, project string, src engine.EventSource, eventID string, given map[string]string, key string, res *SourceResult) (*SourceResult, error) {
	var forget func()
	if key != "" {
		forget = func() { m.eng.ForgetKey(project, "src:"+src.ID, key) }
	}
	return m.startOnEvent(ctx, startSpec{Project: project, Ref: src.Ref, EventID: eventID, Trigger: triggerOf(src), Event: pipeline.EventWebhook,
		Actor: "source:" + src.Name, Webhook: src.Name, Given: given, Forget: forget}, res)
}

// startSpec says what run an already recorded event starts.
type startSpec struct {
	Project, Ref, EventID string
	Trigger               string // the decision's trigger name
	Event                 string // the run's event: webhook or manual
	Actor, Webhook        string
	Given                 map[string]string
	Forget                func() // called when no run could be made
}

// startOnEvent creates the run for an event already recorded: the decision
// "started" (or the error) is added to that event, in the run's transaction
// when it starts.
func (m *Manager) startOnEvent(ctx context.Context, sp startSpec, res *SourceResult) (*SourceResult, error) {
	fail := func(decision, reason string, problems ...string) (*SourceResult, error) {
		if sp.Forget != nil {
			sp.Forget()
		}
		res.Status, res.Reason, res.Problems = decision, reason, problems
		return res, m.eng.AddDecision(sp.EventID, engine.EventDecision{Trigger: sp.Trigger, Decision: decision, Reason: reason + strings.Join(prefixed(problems), "")})
	}
	pp := m.c().projects[sp.Project]
	if pp == nil {
		return fail(engine.DecError, "the project is not watched by this server")
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()
	r, pl, libs, vals, rec, err := m.preparedFor(ctx, pp, engine.EventSource{Ref: sp.Ref}, sp.Given)
	if err != nil {
		var ie pipeline.InputErrors
		if errors.As(err, &ie) {
			return fail(engine.DecError, "the inputs do not fit the pipeline's inputs", inputProblems(ie)...)
		}
		return fail(engine.DecError, "no run could be made: "+redact.URLs(err.Error()))
	}
	t := trigger(r, sp.Event, sp.Actor)
	t.Inputs, t.Webhook = vals, sp.Webhook
	nr := engine.NewRun{Project: sp.Project, Pipeline: pl, Trigger: t, Source: engine.Source{Kind: "git", URL: pp.URL, Libraries: libs}}
	nr.EventID = sp.EventID
	run, err := m.createRun(ctx, nr, rec)
	if err != nil {
		return fail(engine.DecError, "the run could not be created: "+redact.URLs(err.Error()))
	}
	res.Run, res.Status, res.Reason = run, engine.DecStarted, fmt.Sprintf("started run #%d", run.Number)
	return res, nil
}

// runWindows closes debounce and batch windows that are due, once each.
func (m *Manager) runWindows(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		m.closeWindows(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (m *Manager) closeWindows(ctx context.Context) {
	due, err := m.eng.DueWindows()
	if err != nil {
		m.log.Warn("looking for event windows that closed", "err", err)
		return
	}
	for _, w := range due {
		ok, err := m.eng.ClaimWindow(w)
		if err != nil || !ok {
			continue // another server closed it
		}
		src, _, err := m.eng.EventSourceNamed(w.Project, w.Source)
		res := &SourceResult{}
		if err != nil {
			_ = m.eng.AddDecision(w.EventID, engine.EventDecision{Trigger: "source:" + w.Source, Decision: engine.DecError, Reason: "the source was removed before its window closed"})
			continue
		}
		if _, err := m.startFromSource(ctx, w.Project, *src, w.EventID, w.Inputs, "", res); err != nil {
			m.log.Warn("starting the run of a closed event window", "project", w.Project, "source", w.Source, "err", err)
		}
	}
}

// CloseWindows closes the event windows that are due now. The manager does it
// every second once started; tests and tools call it directly.
func (m *Manager) CloseWindows(ctx context.Context) { m.closeWindows(ctx) }
