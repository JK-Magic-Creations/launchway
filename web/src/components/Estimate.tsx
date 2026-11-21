import type { RunDetail, StepView } from "../api";
import "../estimateApi";
import { runTiming, stepTiming } from "../estimate";
import { Link } from "./ui";
import "../estimate.css";
import { terms } from "../terms";

/** The line under a run's sentence: the time left, why there is no estimate, or how long it took against
 * what is usual. The words change at most once a minute; they are not announced as they change. */
export function RunEstimateLine({ run, now }: { run: RunDetail; now: number }) {
  const t = runTiming(run.estimate, run.status, now);
  if (!t.text) return null;
  return (
    <div className="est-line" data-kind={t.kind}>
      <b>{t.text.charAt(0).toUpperCase() + t.text.slice(1)}</b>
      {t.detail && <details><summary>About this estimate</summary><p className="est-detail">{t.detail}</p></details>}
    </div>
  );
}

/** StepTiming says how far a step is against what it usually takes, with a small indicator that is never
 * the only carrier of the message, and a calm notice past the 90th percentile. */
export function StepTimingLine({ run, step, now }: { run: RunDetail; step: StepView; now: number }) {
  const t = stepTiming(step, now);
  if (t.kind === "none") return null;
  const last = step.estimate?.last_run;
  return (
    <>
      <div className="est-step" data-kind={t.kind} title={t.usual ? terms().say(`Usually ${t.usual}, from earlier {runs} of this step`) : undefined}>
        {t.fraction !== undefined && (
          <span className={"est-bar" + (t.kind === "slow" ? " slow" : "")} aria-hidden="true"><span style={{ width: `${Math.round(t.fraction * 100)}%` }} /></span>
        )}
        <span>{t.text.charAt(0).toUpperCase() + t.text.slice(1)}</span>
      </div>
      {t.kind === "slow" && (
        <p className="est-slow" role="note">
          This step is taking longer than it usually does. That is not a failure; it may only be a busy machine or a cold cache.
          {last && last !== run.id && <> {" "}<Link href={`/runs/${last}?step=${encodeURIComponent(step.id)}`}>Compare with its last good run (#{step.estimate?.last_run_number})</Link>.</>}
        </p>
      )}
    </>
  );
}
