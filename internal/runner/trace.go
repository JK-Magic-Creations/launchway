package runner

import (
	"context"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/trace"
)

// stationSpan records a span of what a Station did for a step, as a child of the step's span, when the
// server handed it a trace context and this Station exports traces (see trace.Configure). The name is one
// of the Station's phases; a command, an environment value or a path never goes into a span.
func stationSpan(job *engine.Job, phase string, start time.Time, failed bool) {
	p, ok := trace.Parse(job.Traceparent)
	if !ok || !trace.Enabled() || !p.Sampled {
		return
	}
	sp := trace.Span{TraceID: p.TraceID, SpanID: trace.NewSpanID(), ParentID: p.SpanID, Name: "station " + phase, Start: start, End: time.Now(), Failed: failed}
	sp.Set("launchway.kind", "station")
	sp.Set("launchway.phase", phase)
	sp.Set("launchway.step", job.StepID)
	trace.Record(sp)
}

// runStepTraced runs the step, recording a span for its commands and another for collecting its reports.
func (r *Runner) runStepTraced(ctx context.Context, job *engine.Job, sys func(string, ...any), out executor.Sink) (engine.Result, func(string) collected) {
	start := time.Now()
	res, collect := r.runStep(ctx, job, sys, out)
	stationSpan(job, "commands", start, res.Outcome != "succeeded")
	if collect == nil {
		return res, nil
	}
	return res, func(outcome string) collected {
		t := time.Now()
		got := collect(outcome)
		stationSpan(job, "report collection", t, false)
		return got
	}
}
