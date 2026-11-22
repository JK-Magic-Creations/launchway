import type { DeployAnalysis, DeployMetric, StepCause } from "../api";
import { clock } from "../format";
import { Mono } from "./ui";
import { SpanFaint } from "../kit/bits";

// What a failed step says about itself: one plain sentence, the evidence
// behind it (for an analysis: each metric with its counts and the failing
// measurement, the job it ran and the end of its log) and what Launchway
// did next. The server writes the sentence; this only lays it out.

const BAD = new Set(["Failed", "Error", "Inconclusive"]);

/** What a metric checks, in words, from its provider and conditions. */
export function metricChecks(m: DeployMetric): string {
  const how = m.provider === "job" ? "a Kubernetes job" : m.provider ? `a ${m.provider} query` : "a check";
  const rule = m.success_condition ? `passes when ${m.success_condition}` : m.failure_condition ? `fails when ${m.failure_condition}` : "passes when the check succeeds";
  const every = m.interval ? `, every ${m.interval}` : "";
  return `${how}${every}; ${rule}; it tolerates ${m.failure_limit ?? 0} failure${(m.failure_limit ?? 0) === 1 ? "" : "s"}`;
}

/** One analysis run with its metrics: counts, each measurement and its time, the rule, and for a job its log. */
export function AnalysisEvidence({ run, failedOnly }: { run: DeployAnalysis; failedOnly?: boolean }) {
  const metrics = (run.metrics ?? []).filter((m) => !failedOnly || BAD.has(m.phase));
  return (
    <div className="cause-analysis">
      <p className="faint small">Analysis run <Mono>{run.name}</Mono>: {run.phase.toLowerCase()}{run.message ? `. ${run.message}` : ""}</p>
      {metrics.map((m) => (
        <div key={m.name} className="cause-metric" data-phase={m.phase}>
          <div className="cause-metric-head">
            <strong>{m.name}</strong>
            <span className={"dpill dpill-" + (BAD.has(m.phase) ? "bad" : m.phase === "Successful" ? "ok" : "run")}>{m.phase.toLowerCase()}</span>
            <SpanFaint>{m.successful} passed, {m.failed + m.error} failed{m.inconclusive ? `, ${m.inconclusive} inconclusive` : ""} of {m.count} measured</SpanFaint>
          </div>
          <p className="faint small">{metricChecks(m)}.</p>
          {(m.measurements ?? []).length > 0 && (
            <ol className="cause-measures" aria-label={`Measurements of ${m.name}`}>
              {m.measurements!.map((x, i) => (
                <li key={i} data-phase={x.phase}>
                  <span aria-hidden>{x.phase === "Successful" ? "✓" : x.phase === "Running" || x.phase === "Pending" ? "…" : "✗"}</span>
                  <span>{x.phase.toLowerCase()}</span>
                  {x.value && <Mono>{x.value}</Mono>}
                  {x.at ? <SpanFaint>{clock(x.at)}</SpanFaint> : null}
                  {x.message && <span className="cause-msg">{x.message}</span>}
                </li>
              ))}
            </ol>
          )}
          {m.job && <p className="small">Job <Mono>{`${m.job.namespace}/${m.job.name}`}</Mono></p>}
          {m.job_log?.note && <p className="note small" role="note">{m.job_log.note}</p>}
          {m.job_log?.lines && m.job_log.lines.length > 0 && (
            <figure className="excerpt"><figcaption>The end of the job's log:</figcaption><pre>{m.job_log.lines.join("\n")}</pre></figure>
          )}
        </div>
      ))}
    </div>
  );
}

/** StepCauseView shows a failed step's cause. */
export function StepCauseView({ cause, summary = true, evidence = true }: { cause: StepCause; summary?: boolean; evidence?: boolean }) {
  return (
    <div className="cause" data-kind={cause.kind}>
      {summary && <p className="cause-summary"><strong>{cause.summary}</strong></p>}
      {evidence && cause.analysis && <AnalysisEvidence run={cause.analysis} />}
      {cause.next && (
        <p className="cause-next" data-next={cause.next.kind}>
          <b>What Launchway did about it.</b> {cause.next.summary}
        </p>
      )}
    </div>
  );
}
