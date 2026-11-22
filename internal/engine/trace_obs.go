package engine

import (
	"time"

	"github.com/jk-magic-creations/launchway/internal/trace"
)

// Traces of runs and deploys, worked out from the same changes the metrics count. Ids come from
// the run's id (trace.RunTraceID, trace.SpanIDOf), so every server, every Station and the run page
// agree on them with nothing stored. Spans carry only what trace.Allowed lets them.

// runTraceparent is the trace context handed to a Station for a step: its spans are children of the step's span.
func runTraceparent(runID, stepID string, attempt int) string {
	if !trace.Enabled() {
		return ""
	}
	id := trace.RunTraceID(runID)
	return trace.Format(id, trace.SpanIDOf(runID, stepID, itoa(attempt)), trace.Sampled(id, trace.Ratio()))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

func tms(t int64) time.Time { return time.UnixMilli(t) }

// traceTransitions records spans for what changed: the run's own span when it ends, a span for each
// time a step waited in the queue, and one for each step that ended; a deploy step also ends the deployment's trace.
func traceTransitions(before *runSnapshot, st *RunState, now int64) {
	if !trace.Enabled() || st == nil {
		return
	}
	id := trace.RunTraceID(st.ID)
	root := trace.SpanIDOf(st.ID)
	if before != nil && !isEnded(before.status) && isEnded(st.Status) {
		sp := trace.Span{TraceID: id, SpanID: root, Name: "run", Start: tms(st.CreatedAt), End: tms(tnz(st.FinishedAt, now)), Failed: st.Status == RunFailed}
		sp.Set("launchway.kind", "run")
		sp.Set("launchway.outcome", string(st.Status))
		trace.Record(sp)
	}
	if before == nil {
		return
	}
	for sid, s := range st.Steps {
		b, known := before.steps[sid]
		if !known || (s.Status == b.status && s.Attempt == b.attempt) {
			continue
		}
		attempt := itoa(s.Attempt)
		if b.status == StepQueuedS && (s.Status == StepLeasedS || s.Status == StepRunning) && b.queuedAt > 0 {
			q := trace.Span{TraceID: id, SpanID: trace.SpanIDOf(st.ID, sid, attempt, "queue"), ParentID: root, Name: "queue wait", Start: tms(b.queuedAt), End: tms(now)}
			q.Set("launchway.kind", "queue")
			q.Set("launchway.step", sid)
			q.Set("launchway.pool", poolName(s.Runner))
			trace.Record(q)
		}
		if s.Status.Terminal() && !b.status.Terminal() {
			start := tnz(s.StartedAt, tnz(b.queuedAt, now))
			sp := trace.Span{TraceID: id, SpanID: trace.SpanIDOf(st.ID, sid, attempt), ParentID: root, Name: "step", Start: tms(start), End: tms(tnz(s.FinishedAt, now)), Failed: s.Status == StepFailed}
			sp.Set("launchway.kind", "step")
			sp.Set("launchway.step", sid)
			sp.Set("launchway.attempt", attempt)
			sp.Set("launchway.outcome", string(s.Status))
			trace.Record(sp)
			if s.Dep != nil && s.Dep.ID != "" {
				did := trace.RunTraceID("deploy:" + s.Dep.ID)
				d := trace.Span{TraceID: did, SpanID: trace.SpanIDOf(s.Dep.ID), Name: "deploy", Start: tms(start), End: tms(tnz(s.FinishedAt, now)), Failed: s.Status == StepFailed}
				d.Set("launchway.kind", "deploy")
				d.Set("launchway.step", sid)
				d.Set("launchway.outcome", tnz2(s.Dep.Verdict, string(s.Status)))
				trace.Record(d)
			}
		}
	}
}

func isEnded(s RunStatus) bool { return s == RunSucceeded || s == RunFailed || s == RunCanceled }

func tnz(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func tnz2(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
