import { EXPLAIN } from "../learn/links";
import { LearnLink } from "../learn/LearnLink";
import type { ReactNode } from "react";
import type { RunDetail, StepView } from "../api";
import { AllowedFailureBadge } from "./AllowedFailures";
import { EntityLink } from "./EntityLink";
import { LibraryList } from "./Libraries";
import { BranchIcon, CommitIcon, Link, StatusPill, shownStatus, stepLabel } from "./ui";
import { environmentOf } from "./Deployment";
import { runSentence } from "./whyWaiting";
import { ago, clock, duration, elapsed, refLabel, shortSha, starterLabel } from "../format";
import { Band } from "../kit/layout";
import { Icon } from "../kit/Icon";
import { failing } from "./Tests";

/** How much of a run's detail shows: the summary, or everything the page knows. Engineers set it once. */
export type Density = "summary" | "everything";

const KEY = "lw-density";
export function storedDensity(): Density {
  try { return localStorage.getItem(KEY) === "everything" ? "everything" : "summary"; } catch { return "summary"; }
}
export function saveDensity(d: Density): void {
  try { localStorage.setItem(KEY, d); } catch { /* storage may be blocked */ }
}

/** DensityToggle is the Summary / Everything switch. */
export function DensityToggle({ value, onChange }: { value: Density; onChange: (d: Density) => void }) {
  return (
    <div className="seg3" role="group" aria-label="How much to show">
      {(["summary", "everything"] as const).map((d) => (
        <button key={d} type="button" aria-pressed={value === d} onClick={() => onChange(d)}>{d === "summary" ? "Summary" : "Everything"}</button>
      ))}
    </div>
  );
}

export interface RunHeaderProps {
  run: RunDetail; now: number; done: boolean; live: boolean; density: Density; onDensity: (d: Density) => void;
  select: (step: string) => void; setTab: (t: "definition") => void;
  waitingStep?: StepView; verifyWaiting?: StepView; deploying?: StepView; deployingPhase?: string;
  actions: ReactNode;
}

/** The top of the run page: the status and the sentence, a band of the numbers that matter, and under
 * Everything the run's every field. */
export function RunHeader({ run, now, done, live, density, onDensity, select, waitingStep, verifyWaiting, deploying, deployingPhase, actions }: RunHeaderProps) {
  const libraries = run.graph.libraries ?? [];
  const overridden = libraries.filter((l) => l.override);
  const all = density === "everything";
  const ended = run.step_list.filter((s) => s.status === "succeeded" || s.status === "skipped").length;
  const badge = run.cancel_requested && !done ? <StatusPill status={run.status} label="Canceling" />
    : run.lane?.state === "waiting" && !done ? <StatusPill status="waiting" label="Waiting for lane" />
    : waitingStep ? <StatusPill status="waiting" label="Waiting for approval" />
    : verifyWaiting ? <StatusPill status="waiting" label="Waiting for a person" />
    : deploying ? <StatusPill status={shownStatus(deploying)} label={stepLabel({ ...deploying, environment: environmentOf(run, deploying) }, deployingPhase)} />
    : <StatusPill status={run.status} />;
  return (
    <>
      <header className="phead run-phead">
        <div className="t">
          <div className="eyebrow"><EntityLink kind="project" name={run.project} /> · run #{run.number}</div>
          <h1>{run.message || run.pipeline}</h1>
          <div className="row wrap run-badges">
            {badge}
            <AllowedFailureBadge run={run} onSelect={select} />
            {run.draft && <span className="badge-draft" title="Pipeline text from the editor, not from Git">Draft</span>}
            {overridden.length > 0 && <span className="badge-override" title="Started with another version of a library than the pipeline names">Library override</span>}
            <span className={"live live-sm " + (live ? "live-on" : "")}><span className="dot" />{live ? "live" : "…"}</span>
          </div>
          <p className="sentence" aria-live="polite">{runSentence(run, now)}{run.status === "failed" && <> <LearnLink to={EXPLAIN.stopped} /></>}</p>
        </div>
        <div className="acts">
          <DensityToggle value={density} onChange={onDensity} />
          {actions}
        </div>
      </header>
      <Band cells={[
        { key: "status", label: "Status", value: run.status === "succeeded" ? "Passed" : run.status === "failed" ? "Failed" : run.status === "canceled" ? "Canceled" : run.status === "queued" ? "Queued" : "Running", foot: ago(run.created_at, now), loud: run.status === "failed" ? "bad" : undefined },
        { key: "took", label: "Took", value: run.started_at > 0 ? elapsed(run.started_at, run.finished_at, now) : "–", foot: run.started_at > 0 ? (done ? "finished" : "so far") : "not started" },
        { key: "steps", label: "Steps", value: <>{ended}<small> of {run.step_list.length}</small></>, foot: "passed or skipped" },
        { key: "tests", label: "Tests", value: run.tests ? (failing(run.tests) > 0 ? `${failing(run.tests)} failed` : run.tests.total) : "–", foot: run.tests ? `${run.tests.total} in all` : "none reported", loud: run.tests && failing(run.tests) > 0 ? "bad" : undefined },
      ]} />
      {all && (
        <section className="sheet pad run-details" aria-label="Run details">
          <h2 className="h-sm">Run details</h2>
          <div className="run-meta">
            <span className="meta"><strong><EntityLink kind="project" name={run.project} /></strong> #{run.number}</span>
            {refLabel(run) && <span className="meta" title={run.ref}><BranchIcon />{refLabel(run)}</span>}
            {run.sha && <span className="meta mono" title={run.sha}><CommitIcon />{shortSha(run.sha)}</span>}
            <span className="meta">{starterLabel(run)}</span>
            {run.lane && <span className="meta" title="This run's pipeline has a lane: its runs take turns">lane {run.lane.label}</span>}
            <span className="meta" title={clock(run.created_at)}>{ago(run.created_at, now)}</span>
            {run.started_at > 0 && <span className="meta">took {elapsed(run.started_at, run.finished_at, now)}</span>}
            {!done && !run.cancel_requested && (run.deadline_at ?? 0) > 0 && (
              <span className="meta" title={`The pipeline's timeout cancels the run at ${clock(run.deadline_at!)}. Time spent waiting for an approval does not count.`}>
                times out in {duration(Math.max(0, run.deadline_at! - now))}</span>
            )}
            {run.cancel_requested && run.cancel_reason && <span className="meta">{run.cancel_reason}</span>}
            {(run.allowed_failures ?? 0) > 0 && <span className="meta">{run.allowed_failures} allowed failure{run.allowed_failures === 1 ? "" : "s"}</span>}
            {run.cancel_requested && <span className="meta">cancel requested</span>}
          </div>
          {(run.rerun_of || run.reruns.length > 0) && (
            <div className="run-meta">
              {run.rerun_of && (
                <span className="meta rerun-of">
                  Re-run of <Link href={`/runs/${run.rerun_of}?step=${encodeURIComponent(run.rerun_from ?? "")}`}>#{run.rerun_of_number}</Link> from <strong>{(run.rerun_froms?.length ? run.rerun_froms : [run.rerun_from]).join(", ")}</strong>
                </span>
              )}
              {run.reruns.length > 0 && (
                <span className="meta">
                  Re-run as {run.reruns.map((r, i) => (
                    <span key={r.id}>{i > 0 && ", "}<Link href={`/runs/${r.id}`}>#{r.number}</Link> from {(r.froms?.length ? r.froms : [r.from]).join(", ")}</span>
                  ))}
                </span>
              )}
            </div>
          )}
          {(libraries.length > 0 || run.graph.origin) && (
            <div className="run-meta">
              {libraries.length > 0 && <><span className="meta">Libraries</span><LibraryList libraries={libraries} /></>}
              {run.graph.origin && <span className="meta">pipeline configured on the server</span>}
            </div>
          )}
        </section>
      )}
      {!all && <p className="faint small density-note"><Icon name="eye" size="s" /> The summary hides the run's fields, the dependency graph and each step's settings. Choose Everything to see them.</p>}
    </>
  );
}
