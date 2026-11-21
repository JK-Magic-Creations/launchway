import type { RunDetail } from "../api";
import { Link } from "./ui";

/**
 * What flow control says about a run, above its steps: where it waits in its
 * lane, that its lane gave it up, and which of its steps failed and were
 * allowed to (the run went on as if they had succeeded).
 */
export function RunFlowBanners({ run, done, onSelect }: { run: RunDetail; done: boolean; onSelect: (step: string) => void }) {
  const allowedFailed = run.step_list.filter((s) => s.status === "failed" && s.allowed_failure);
  return (
    <>
      {run.lane?.state === "waiting" && !done && (
        <div className="callout callout-wait lane-banner">
          <span>
            <strong>Waiting for lane {run.lane.label}</strong>
            {run.lane.behind && <>, behind <Link href={`/runs/${run.lane.behind.id}`}>run #{run.lane.behind.number}</Link></>}
            {" "}(number {run.lane.position ?? 1} in line). It starts when the run before it ends; none of its steps is queued until then.
          </span>
        </div>
      )}
      {run.lane && run.lane.state !== "waiting" && run.lane.state !== "holding" && (
        <div className="callout callout-info lane-banner">
          {run.lane.state === "replaced"
            ? <>Replaced in lane <strong>{run.lane.label}</strong> by {run.lane.behind ? <Link href={`/runs/${run.lane.behind.id}`}>run #{run.lane.behind.number}</Link> : "a newer run"}.</>
            : <>Skipped: lane <strong>{run.lane.label}</strong> was busy{run.lane.behind && <> (<Link href={`/runs/${run.lane.behind.id}`}>run #{run.lane.behind.number}</Link>)</>}. No step ran.</>}
        </div>
      )}

      {allowedFailed.length > 0 && (
        <div className="callout callout-allowed">
          <strong>{allowedFailed.length === 1 ? `${allowedFailed[0].id} failed, and the pipeline allows that` : `${allowedFailed.length} steps failed, and the pipeline allows that`}.</strong>{" "}
          Their steps have <code>on_failure: continue</code>, so the run goes on as if they had succeeded:{" "}
          {allowedFailed.map((s, i) => (
            <span key={s.id}>{i > 0 && ", "}<button type="button" className="linkbtn" onClick={() => onSelect(s.id)}>{s.id}</button></span>
          ))}.
        </div>
      )}
    </>
  );
}
