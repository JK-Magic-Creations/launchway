package source

// Triggers on another project's result: when a run or a launch of an upstream
// project ends, the triggers that wait for it are judged, and a run of the
// downstream project starts, with inputs mapped from the result.
//
// Every server reads the finished runs after a shared cursor; each
// (trigger, result) pair is claimed in the database, so exactly one server
// starts exactly one run. The result is described to the downstream project by
// reference: the upstream run's number, status, commit and address; its step
// outputs only when the upstream project allowed that. No secret, log or file
// crosses.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/eventexpr"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// CheckRunTriggerExpressions reports what is wrong with a trigger's
// expressions, so that a mistake is found when it is saved.
func CheckRunTriggerExpressions(t engine.RunTrigger) error {
	return CheckSourceExpressions(engine.EventSource{Filter: t.Filter, Mapping: t.Mapping})
}

func (m *Manager) runResultLoop(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		m.ProcessResults(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// ProcessResults judges the runs and launches that ended since the cursor.
// The manager does it every two seconds once started; tests call it.
func (m *Manager) ProcessResults(ctx context.Context) {
	cur, err := m.eng.ResultCursor()
	if err != nil {
		m.log.Warn("reading where result triggers stopped", "err", err)
		return
	}
	results, err := m.eng.ResultsAfter(cur, 200)
	if err != nil {
		m.log.Warn("looking for results that ended", "err", err)
		return
	}
	for _, r := range results {
		if err := m.judgeResult(ctx, r); err != nil {
			m.log.Warn("judging a result for the triggers waiting on it", "run", r.RunID, "err", err)
			return // tried again at the next tick
		}
		m.eng.MoveResultCursor(r.Seq)
	}
}

func (m *Manager) judgeResult(ctx context.Context, r engine.ResultEvent) error {
	sum, err := m.eng.Summary(r.RunID)
	if err != nil {
		return nil // the run is gone; nothing waits on it
	}
	triggers, err := m.eng.RunTriggersOn(sum.Project)
	if err != nil {
		return err
	}
	for _, t := range triggers {
		if t.Kind != r.Kind || (t.UpstreamPipeline != "" && t.UpstreamPipeline != sum.Pipeline) {
			continue
		}
		if err := m.judgeTrigger(ctx, r, sum, t); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) judgeTrigger(ctx context.Context, r engine.ResultEvent, sum *engine.RunSummary, t engine.RunTrigger) error {
	claimed, _, err := m.eng.ClaimKey(t.Project, "rt:"+t.ID, fmt.Sprint(r.Seq), "", 100*engine.DedupeMemory)
	if err != nil || !claimed {
		return err // another server judged it
	}
	source := engine.SourceRunResult
	what := fmt.Sprintf("Run #%d of %s ended: %s", sum.Number, sum.Project, r.Verdict)
	if r.Kind == engine.TriggerOnLaunch {
		source = engine.SourceLaunchResult
		what = fmt.Sprintf("Launch of %s to %s (run #%d of %s) ended: %s", r.Service, r.Environment, sum.Number, sum.Project, r.Verdict)
	}
	run := map[string]any{"id": sum.ID, "number": float64(sum.Number), "project": sum.Project, "pipeline": sum.Pipeline, "status": string(sum.Status),
		"ref": sum.Ref, "branch": sum.Branch, "tag": sum.Tag, "sha": sum.SHA, "event": sum.Event, "url": "/runs/" + sum.ID}
	vars := map[string]any{"run": run}
	if r.Kind == engine.TriggerOnLaunch {
		vars["launch"] = map[string]any{"verdict": r.Verdict, "environment": r.Environment, "service": r.Service, "version": r.Version}
	}
	payload, _ := json.Marshal(vars)
	ev := engine.EventRecord{Project: t.Project, Source: source, SourceName: sum.Project, Verified: true, Summary: what, Ref: sum.Ref, SHA: sum.SHA,
		Payload: engine.ScrubPayload(payload), DedupeKey: fmt.Sprint(r.Seq)}
	trig := "result:" + t.Name
	end := func(decision, reason string) error {
		_, err := m.eng.RecordEvent(ev, engine.EventDecision{Trigger: trig, Decision: decision, Reason: reason})
		return err
	}

	allow, err := m.eng.RunTriggerPermission(sum.Project, t.Project)
	if err != nil {
		return end(engine.DecRefused, fmt.Sprintf("the project %s has not allowed %s to trigger on its results (an operator of %s allows it)", sum.Project, t.Project, sum.Project))
	}
	if allow.ShareOutputs {
		if outs, err := m.eng.RunOutputs(sum.ID); err == nil {
			o := map[string]any{}
			for step, kv := range outs {
				m := map[string]any{}
				for k, v := range kv {
					m[k] = v
				}
				o[step] = m
			}
			vars["outputs"] = o
		}
	} else {
		vars["outputs"] = map[string]any{}
	}
	if t.When != "any" && t.When != r.Verdict {
		return end(engine.DecFiltered, fmt.Sprintf("the result was %s, and this trigger waits for %s", r.Verdict, t.When))
	}
	if t.Filter != "" {
		x, err := eventexpr.Parse(t.Filter)
		var ok bool
		if err == nil {
			ok, err = x.Bool(vars)
		}
		if err != nil {
			return end(engine.DecError, "the filter could not be judged: "+err.Error())
		}
		if !ok {
			return end(engine.DecFiltered, "the filter "+t.Filter+" was false for this result")
		}
	}
	given := map[string]string{}
	var problems []string
	names := make([]string, 0, len(t.Mapping))
	for k := range t.Mapping {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		x, err := eventexpr.Parse(t.Mapping[k])
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
		return end(engine.DecError, "the inputs could not be worked out from the result; "+strings.Join(problems, "; "))
	}
	ev.Replay = &engine.ReplayPlan{Ref: t.Ref, Inputs: given}
	id, err := m.eng.RecordEvent(ev)
	if err != nil {
		return err
	}
	_, err = m.startOnEvent(ctx, startSpec{Project: t.Project, Ref: t.Ref, EventID: id, Trigger: trig, Event: pipeline.EventManual,
		Actor: fmt.Sprintf("result:%s#%d", sum.Project, sum.Number), Given: given}, &SourceResult{})
	return err
}
