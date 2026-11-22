import { DeploymentRollback } from "./Rollback";
import { useEffect, useState } from "react";
import { api, type DeployApp, type DeployPhase, type DeploySnapshot, type DeploymentView, type RunDetail, type RunEvent, type StepView, type Verdict } from "../api";
import { ago, clock, duration, shortSha } from "../format";
import { useNow } from "../live";
import { Link, Mono } from "./ui";
import { DeploymentControls } from "./DeploymentControls";
import { Runway } from "./Runway";
import { terms } from "../terms";

// What a deploy step did and what deploy runners last observed of it. This
// is read-only: it shows the deployment, it never changes it.

/** Observed data older than this is out of date. Deploy runners report at
 * least every ten seconds while they watch. */
export const STALE_MS = 30_000;

const PHASE: Record<DeployPhase, string> = {
  unobserved: "not observed",
  app_missing: "the application does not exist",
  source_error: "the application does not deploy from the repository",
  syncing: "waiting for Argo CD to sync",
  sync_blocked: "a sync window does not allow syncing",
  sync_failed: "the sync failed",
  progressing: "synced, waiting to be healthy",
  degraded: "degraded",
  rollout_progressing: "the rollout is in progress",
  rollout_paused: "the rollout is paused",
  rollout_aborted: "the rollout was aborted",
  healthy: "healthy",
  command_pending: "waiting for a deploy runner to apply a command",
};

const SHORT: Partial<Record<DeployPhase, string>> = {
  unobserved: "not observed", app_missing: "no application", source_error: "wrong source", syncing: "syncing",
  sync_blocked: "sync window", sync_failed: "sync failed", progressing: "getting healthy", degraded: "degraded",
  rollout_progressing: "rolling out", rollout_paused: "rollout paused", rollout_aborted: "rollout aborted", healthy: "healthy",
  command_pending: "command waiting",
};

/** A phase in a word or two, for small spaces. */
export function shortPhase(p: string): string { return SHORT[p as DeployPhase] ?? phaseLabel(p); }

/** Where a deployment stands, in plain words. A phase a newer server added is shown as it is called. */
export function phaseLabel(p: string): string {
  if (!p) return "not observed yet";
  return PHASE[p as DeployPhase] ?? p.replace(/_/g, " ");
}

export function phaseTone(p: string): string {
  switch (p) {
    case "healthy": return "ok";
    case "degraded": case "sync_failed": case "rollout_aborted": case "app_missing": case "source_error": return "bad";
    case "sync_blocked": case "rollout_paused": case "command_pending": return "wait";
    case "syncing": case "progressing": case "rollout_progressing": return "run";
    default: return "idle";
  }
}

const VERDICT: Record<Verdict, string> = {
  deployed: "Deployed",
  failed: "Failed",
  rolled_back: "Rolled back",
  rollback_failed: "Rollback failed",
  abandoned: "Abandoned",
};

export function verdictLabel(v: string): string { return VERDICT[v as Verdict] ?? v.replace(/_/g, " "); }

export function verdictTone(v: string): string {
  switch (v) {
    case "deployed": return "ok";
    case "failed": case "rollback_failed": return "bad";
    case "rolled_back": return "warn";
    default: return "idle";
  }
}

const VERDICT_TEXT: Record<Verdict, string> = {
  deployed: "The version was deployed and nothing rolled it back.",
  failed: "The deploy step failed and nothing rolled it back: the environment may run something in between.",
  rolled_back: "The rollback step reverted the commit, and the environment was healthy again afterwards.",
  rollback_failed: "The rollback step failed. The environment needs a person to look at it.",
  abandoned: "The run was canceled before the version was deployed.",
};

/** How fresh what was observed is. */
export type Freshness =
  | { state: "closed" } // the deployment is over: what is shown is how it was when it ended
  | { state: "never" } // no deploy runner has observed it
  | { state: "error"; age: number; error: string } // the runner reported it cannot see the application
  | { state: "stale"; age: number }
  | { state: "fresh"; age: number };

export function freshness(d: DeploymentView, now: number): Freshness {
  if (d.verdict) return { state: "closed" };
  if (!d.observed_at) return { state: "never" };
  const age = Math.max(0, now - d.observed_at);
  if (d.snapshot?.error) return { state: "error", age, error: d.snapshot.error };
  return age > STALE_MS ? { state: "stale", age } : { state: "fresh", age };
}

/** "4s ago", counting seconds up to a minute. */
export function secondsAgo(at: number, now: number): string {
  const s = Math.max(0, Math.floor((now - at) / 1000));
  return s < 60 ? `${s}s ago` : ago(at, now);
}

function Chip({ tone, label, value, title }: { tone: string; label: string; value: string; title?: string }) {
  return <span className={`dchip dchip-${tone}`} title={title}><span className="dchip-label">{label}</span>{value}</span>;
}

function syncTone(s: string): string {
  return s === "Synced" ? "ok" : s === "OutOfSync" ? "warn" : "idle";
}

function healthTone(h: string): string {
  switch (h) {
    case "Healthy": return "ok";
    case "Progressing": return "run";
    case "Degraded": case "Missing": return "bad";
    case "Suspended": return "warn";
    default: return "idle";
  }
}

/** The Argo CD application's sync and health. Muted: the data is out of
 * date, so nothing is shown as good. An application not yet at the
 * deployment's commit is not shown as good either: its health is the old
 * version's. */
export function AppChips({ app, muted }: { app?: DeployApp; muted: boolean }) {
  if (!app) return null;
  if (!app.found) return <Chip tone={muted ? "idle" : "bad"} label="Argo CD" value="application not found" />;
  const behind = app.contains === false;
  const behindNote = behind ? `Argo CD is at ${shortSha(app.revision ?? "")}, which does not include this deployment's commit yet` : undefined;
  const t = (tone: string) => (muted || (behind && tone === "ok") ? "idle" : tone);
  return (
    <>
      <Chip tone={t(syncTone(app.sync))} label="sync" value={app.sync || "Unknown"} title={behindNote} />
      <Chip tone={t(healthTone(app.health))} label="health" value={app.health || "Unknown"} title={app.health_message || behindNote} />
    </>
  );
}

/** The pill for where a deployment stands: its verdict once it is over,
 * else its phase (grey when what was observed is out of date). */
export function DeployStatePill({ d, now }: { d: DeploymentView; now: number }) {
  if (d.verdict) return <span className={`dpill dpill-${verdictTone(d.verdict)}`}>{verdictLabel(d.verdict)}</span>;
  const f = freshness(d, now);
  const tone = f.state === "fresh" ? phaseTone(d.phase) : f.state === "error" ? "bad" : "idle";
  return <span className={`dpill dpill-${tone}`} title={d.detail || undefined}>{d.phase ? phaseLabel(d.phase) : "not observed yet"}</span>;
}

/** Whether no deploy runner observes the deployment's target now: nothing
 * is observed, and commands wait until one does. */
export function unserved(d: DeploymentView, now: number): boolean {
  return !d.verdict && (!d.observer || (d.observer_expires !== undefined && d.observer_expires < now));
}

/** How long Argo CD has not compared the application while the deployment
 * waits for it to sync, when that is more than three minutes. */
export const RECONCILE_MS = 3 * 60_000;
export function notReconciling(d: DeploymentView, now: number): number {
  const at = d.snapshot?.app?.reconciled_at;
  if (d.verdict || d.phase !== "syncing" || !at) return 0;
  return now - at > RECONCILE_MS ? now - at : 0;
}

/** Says how fresh the observed data is, naming the case. */
export function FreshnessLine({ d, now, compact }: { d: DeploymentView; now: number; compact?: boolean }) {
  const f = freshness(d, now);
  const by = d.observed_by ? ` by ${d.observed_by}` : "";
  // No deploy runner serves the target: what was observed (if anything) is
  // as it was when the last one stopped.
  if ((f.state === "never" || f.state === "stale") && unserved(d, now)) {
    return (
      <span className="dfresh dfresh-warn" data-case="unserved">
        {compact ? terms().say(`no {deploy:do} {runner} serves target ${d.target}`)
          : <>{terms().say("No {deploy:do} {runner} serves target")} <Mono>{d.target}</Mono>: nothing is observed, and commands wait until one does.</>}
        {f.state === "stale" && <span className="faint"> (last observed {ago(d.observed_at!, now)}{by})</span>}
      </span>
    );
  }
  switch (f.state) {
    case "closed":
      return d.observed_at
        ? <span className="dfresh dfresh-closed">last observed {ago(d.observed_at, now)}{by}, before it ended</span>
        : null;
    case "never":
      return <span className="dfresh dfresh-warn">{compact ? terms().say(`no {deploy:do} {runner} has observed this yet (target ${d.target})`)
        : d.observer ? <>Not observed yet; {d.observer} serves target <Mono>{d.target}</Mono>.</>
        : <>{terms().say("No {deploy:do} {runner} has observed this yet: is one running for target")} <Mono>{d.target}</Mono>?</>}</span>;
    case "error":
      return <span className="dfresh dfresh-bad">{terms().say("The {runner} cannot see the application:")} {f.error} <span className="faint">({secondsAgo(d.observed_at!, now)}{by})</span></span>;
    case "stale":
      return <span className="dfresh dfresh-warn">Last observed {ago(d.observed_at!, now)}{by}{compact ? "" : ": what is shown may be out of date"}</span>;
    case "fresh":
      return <span className="dfresh">observed {secondsAgo(d.observed_at!, now)}{by}</span>;
  }
}

/** The deployment a deploy, rollback or rollout step (or a linked approval) works on, if it has one. */
export function deploymentOf(run: RunDetail, step: StepView): DeploymentView | undefined {
  const list = run.deployments ?? [];
  if (step.kind === "rollback") return list.find((d) => d.rollback_step === step.id) ?? list.find((d) => d.step === (step.deployment || step.id));
  if (step.kind === "rollout" || step.kind === "approval") return step.deployment ? list.find((d) => d.step === step.deployment) : undefined;
  return list.find((d) => d.step === step.id);
}

/** The environment a deploy or rollback step deploys to. */
export function environmentOf(run: RunDetail, step: StepView): string {
  if (step.environment) return step.environment;
  if ((step.kind === "rollback" || step.kind === "rollout" || step.kind === "approval") && step.deployment) return run.step_list.find((s) => s.id === step.deployment)?.environment ?? "";
  return "";
}

/** What a rollout step does to the rollout, from the pipeline. */
function rolloutAction(run: RunDetail, step: StepView): string {
  return run.graph.steps.find((s) => s.id === step.id)?.action ?? "";
}

/** The panel's title for a rollout step or a linked approval. */
function controlTitle(run: RunDetail, step: StepView, d: DeploymentView): string {
  if (step.kind === "approval") return `The rollout this ${terms().lower("clearance")} guards: ${d.service} in ${d.environment}`;
  const where = `${d.service} in ${d.environment}`;
  switch (rolloutAction(run, step)) {
    case "promote": return `Promoting ${where}`;
    case "promote-full": return `Fully promoting ${where}`;
    case "abort": return `Aborting ${where}`;
    case "retry": return `Retrying ${where}`;
    default: return `Acting on the rollout of ${where}`;
  }
}

// DeploymentPanel is shown with a deploy, rollback or rollout step, and
// with an approval that guards a deployment.
export function DeploymentPanel({ run, step }: { run: RunDetail; step: StepView }) {
  const d = deploymentOf(run, step);
  const live = !!d && !d.verdict;
  const now = useNow(1000, live || step.status === "waiting");
  const env = environmentOf(run, step);

  if (!d) return <NoDeployment run={run} step={step} env={env} now={now} />;
  return (
    <section className="card dep" aria-label={`Deployment to ${d.environment}`}>
      <div className="dep-head">
        <h3 className="h-sm">
          {step.kind === "rollout" || step.kind === "approval" ? controlTitle(run, step, d)
            : step.kind !== "rollback" ? terms().say(`{Deploy:doing} ${d.service} to ${d.environment}`)
            : step.status === "skipped" ? terms().say(`No {rollback} needed: ${d.service} in ${d.environment}`)
            : step.status === "pending" ? terms().say(`Can {rollback:do} ${d.service} in ${d.environment}`)
            : terms().say(`{Rollback:doing} ${d.service} in ${d.environment}`)}
        </h3>
        <div className="dep-pills">
          <DeployStatePill d={d} now={now} />
          {d.holding && <span className="dpill dpill-lock" title={terms().say(`While it holds the lock, no other {run} {deploy:does} ${d.service} to ${d.environment}`)}>holds {d.environment}'s lock</span>}
        </div>
      </div>
      {step.kind === "rollback" && step.status === "skipped" && (
        <p className="sub">Not needed{step.error ? `: ${step.error}` : ""}. The deployment it guards is shown below.</p>
      )}
      {step.kind === "rollback" && step.status === "pending" && (
        <p className="sub">Runs only if a step guarding this deployment fails: it then reverts the commit below.</p>
      )}
      {d.verdict && d.mode !== "rollback" && (
        <div className={`callout dep-verdict callout-${d.verdict === "deployed" ? "ok" : d.verdict === "rolled_back" ? "draft" : d.verdict === "abandoned" ? "info" : "bad"}`}>
          <strong>{verdictLabel(d.verdict)}.</strong> {VERDICT_TEXT[d.verdict] ?? ""}
          {d.closed_at ? <span className="faint-in"> {clock(d.closed_at)}.</span> : null}
        </div>
      )}
      <dl className="kv dep-facts">
        <dt>{terms().say("{Environment}")}</dt><dd><strong>{d.environment}</strong> <span className="faint">on target</span> <Mono>{d.target}</Mono></dd>
        <dt>Service</dt><dd>{d.project === d.service ? d.service : <>{d.service} <span className="faint">of {d.project}</span></>}</dd>
        <dt>Application</dt><dd><Mono>{d.application}</Mono> <span className="faint">in Argo CD</span></dd>
        <dt>Version</dt>
        <dd>
          <Mono>{d.version}</Mono>
          {d.previous_version ? <span className="faint"> (it ran <Mono>{d.previous_version}</Mono> before)</span> : <span className="faint"> (what ran before is not known)</span>}
        </dd>
        {d.commit && (
          <>
            <dt>Commit</dt>
            <dd>
              <Mono><span title={d.commit}>{shortSha(d.commit)}</span></Mono>
              {d.repo && <> <span className="faint">to</span> <Mono>{d.repo}</Mono></>}
              {d.branch && <> <span className="faint">on</span> <Mono>{d.branch}</Mono></>}
              {!d.changed && <span className="faint"> (Git already held this version; that is the branch's tip)</span>}
            </dd>
          </>
        )}
        {d.revert && <><dt>Reverted by</dt><dd><Mono><span title={d.revert}>{shortSha(d.revert)}</span></Mono> <span className="faint">on</span> <Mono>{d.branch}</Mono></dd></>}
        <dt>Lock</dt>
        <dd>{d.holding ? terms().say(`Holds ${d.environment}'s lock for ${d.service}: other {runs} wait until it is done.`) : `Does not hold ${d.environment}'s lock${d.verdict ? " (released when it ended)" : ""}.`}</dd>
        <dt>Opened</dt><dd>{clock(d.opened_at)}{d.deployed_at ? <span className="faint">, deployed {clock(d.deployed_at)}</span> : null}</dd>
      </dl>
      <p className="dep-launch"><Link href={`/runs/${run.id}/launch/${d.id}`}>Open this Launch</Link> <span className="faint">to see every object it made, their logs and tests, and to steer it.</span></p>
      <Observed d={d} now={now} />
      <DeploymentControls run={run} d={d} now={now} />
      <DeploymentRollback d={d} />
      {d.timeline && d.timeline.length > 0 && <Timeline d={d} />}
    </section>
  );
}

// Observed shows what deploy runners last saw: the phase, the application,
// the rollout and its analyses. Out of date, it is greyed.
function Observed({ d, now }: { d: DeploymentView; now: number }) {
  const f = freshness(d, now);
  const muted = f.state !== "fresh";
  const snap = d.snapshot;
  return (
    <div className="dep-observed">
      <div className="dep-observed-head">
        <h4 className="h-xs">{d.verdict ? "As last observed" : "What is observed now"}</h4>
        <FreshnessLine d={d} now={now} />
      </div>
      {notReconciling(d, now) > 0 && (
        <div className="dep-msg dep-msg-warn" data-case="not-reconciling">
          Argo CD has not compared the application for {duration(notReconciling(d, now))}: is its application controller running?
        </div>
      )}
      <div className={"dep-observed-body" + (muted ? " stale" : "")}>
        {(d.phase || d.detail) && (
          <div className="dep-phase">
            <span className={`dpill dpill-${muted ? (f.state === "error" ? "bad" : "idle") : phaseTone(d.phase)}`}>{phaseLabel(d.phase)}</span>
            {d.detail && <span className="dep-detail">{d.detail}</span>}
          </div>
        )}
        {snap && <Snapshot snap={snap} muted={muted} now={now} />}
      </div>
    </div>
  );
}

function Snapshot({ snap, muted, now }: { snap: DeploySnapshot; muted: boolean; now: number }) {
  const app = snap.app;
  return (
    <>
      {app && (
        <div className="dep-block">
          <div className="dep-chips"><span className="dep-block-title">Argo CD</span><AppChips app={app} muted={muted} /></div>
          <div className="dep-notes">
            {app.revision && (
              <span>at <Mono><span title={app.revision}>{shortSha(app.revision)}</span></Mono>
                {app.contains === false && " (not yet the deployment's commit)"}{app.contains === true && " (includes the deployment's commit)"}</span>
            )}
            {app.automated ? <span>syncs by itself</span> : <span>syncs when asked</span>}
            {app.reconciled_at ? <span>compared {ago(app.reconciled_at, now)}</span> : null}
            {app.operation && <span>last sync {app.operation.phase.toLowerCase()}{app.operation.message ? `: ${app.operation.message}` : ""}</span>}
          </div>
          {app.health_message && <div className="dep-msg">{app.health_message}</div>}
          {app.source_error && <div className="dep-msg dep-msg-bad">{app.source_error}</div>}
          {app.window_blocked && <div className="faint">Held by a sync window: {app.window_blocked}</div>}
          {app.notes && app.notes.length > 0 && (
            <ul className="dep-conds faint">{app.notes.map((c, i) => <li key={"n" + i}>{c}</li>)}</ul>
          )}
          {app.conditions && app.conditions.length > 0 && (
            <ul className="dep-conds">{app.conditions.map((c, i) => <li key={i}>{c}</li>)}</ul>
          )}
          {app.resources && app.resources.length > 0 && (
            <ul className="dep-resources">
              {app.resources.map((r) => (
                <li key={`${r.kind}/${r.namespace}/${r.name}`}>
                  <Mono>{r.kind}/{r.namespace ? `${r.namespace}/` : ""}{r.name}</Mono>
                  {!r.found ? <Chip tone={muted ? "idle" : "bad"} label="" value="not found" />
                    : <>{r.sync && <Chip tone={muted ? "idle" : syncTone(r.sync)} label="sync" value={r.sync} />}
                      {r.health && <Chip tone={muted ? "idle" : healthTone(r.health)} label="health" value={r.health} />}</>}
                  {r.message && <span className="faint">{r.message}</span>}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      <Runway snapshot={snap} muted={muted} now={now} />
    </>
  );
}

export function RolloutView({ r, muted, now }: { r: NonNullable<DeploySnapshot["rollout"]>; muted: boolean; now: number }) {
  if (!r.found) return <div className="dep-block"><span className="dep-block-title">Rollout</span> <span className="faint">not found</span></div>;
  const tone = muted ? "idle" : r.aborted ? "bad" : r.phase === "Healthy" ? "ok" : r.phase === "Degraded" ? "bad" : r.paused || r.phase === "Paused" ? "wait" : "run";
  const steps = Array.from({ length: r.steps }, (_, i) => r.step_names?.[i] || `step ${i + 1}`);
  return (
    <div className="dep-block">
      <div className="dep-chips">
        <span className="dep-block-title">Rollout</span>
        <span className={`dpill dpill-${tone}`}>{r.aborted ? "Aborted" : r.phase || "Unknown"}</span>
        {r.strategy && <span className="faint">{r.strategy === "blueGreen" ? "blue-green" : r.strategy}</span>}
        {r.canary_weight !== undefined && <span className="dep-weight">{r.canary_weight}% of traffic to the new version</span>}
      </div>
      {r.message && <div className="dep-msg">{r.message}</div>}
      {steps.length > 0 && (
        <ol className="cstrip" aria-label={terms().say(`{Canary} steps: ${r.step !== undefined ? `at ${Math.min(r.step + 1, r.steps)} of ${r.steps}` : `${r.steps} steps`}`)}>
          {steps.map((name, i) => {
            const state = r.step === undefined ? "todo" : i < r.step ? "done" : i === r.step ? "now" : "todo";
            return (
              <li key={i} className={`cstep cstep-${state}`} aria-current={state === "now" ? "step" : undefined}>
                <span className="cstep-n">{i + 1}</span><span className="cstep-name">{name}</span>
              </li>
            );
          })}
          {r.step !== undefined && r.step >= r.steps && <li className="cstep cstep-end">all steps done</li>}
        </ol>
      )}
      {(r.paused || (r.pause_reasons?.length ?? 0) > 0) && (
        <div className="dep-notes dep-pause">
          <span><strong>Paused</strong>{r.pause_reasons?.length ? `: ${r.pause_reasons.join(", ")}` : ""}</span>
          {r.pause_until ? <span title={clock(r.pause_until)}>{r.pause_until > now ? `goes on in ${duration(r.pause_until - now)}` : "the pause time is up"}</span> : null}
        </div>
      )}
      <div className="dep-notes">
        <span>{r.ready} of {r.replicas} ready</span>
        <span>{r.updated} updated</span>
        <span>{r.available} available</span>
        {!r.observed && <span>the controller has not seen the newest change yet</span>}
      </div>
    </div>
  );
}

function analysisTone(p: string, muted: boolean): string {
  if (muted) return "idle";
  switch (p) {
    case "Successful": return "ok";
    case "Failed": case "Error": return "bad";
    case "Inconclusive": return "wait";
    case "Running": return "run";
    default: return "idle";
  }
}

export function Analyses({ list, muted }: { list: NonNullable<DeploySnapshot["analysis"]>; muted: boolean }) {
  return (
    <div className="dep-block">
      <div className="dep-chips"><span className="dep-block-title">Analysis</span><span className="faint">the rollout's own checks</span></div>
      {list.map((a) => (
        <div key={a.name} className="dep-analysis">
          <div className="dep-chips">
            <Mono>{a.name}</Mono>
            <span className={`dpill dpill-${analysisTone(a.phase, muted)}`}>{a.phase}</span>
            {a.kind && <span className="faint">{a.kind === "Background" ? "runs throughout" : a.step_index !== undefined ? terms().say(`at {canary} step ${a.step_index + 1}`) : a.kind}</span>}
          </div>
          {a.message && <div className="dep-msg">{a.message}</div>}
          {a.metrics && a.metrics.length > 0 && (
            <div className="table-scroll">
              <table className="inputs-table dep-metrics">
                <thead><tr><th>Metric</th><th>State</th><th>Measured</th><th>Successful</th><th>Failed</th><th>Inconclusive</th><th>Error</th></tr></thead>
                <tbody>
                  {a.metrics.map((m) => (
                    <tr key={m.name}>
                      <td className="mono" data-label="Metric">{m.name}{m.message && <div className="faint dep-metric-msg">{m.message}</div>}</td>
                      <td data-label="State"><span className={`dpill dpill-${analysisTone(m.phase, muted)}`}>{m.phase}</span></td>
                      <td data-label="Measured">{m.count}</td>
                      <td data-label="Successful">{m.successful}</td>
                      <td data-label="Failed" className={m.failed > 0 && !muted ? "bad-ink" : undefined}>{m.failed}</td>
                      <td data-label="Inconclusive">{m.inconclusive}</td>
                      <td data-label="Error" className={m.error > 0 && !muted ? "bad-ink" : undefined}>{m.error}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function Timeline({ d }: { d: DeploymentView }) {
  return (
    <details className="dep-timeline" open>
      <summary>What was observed, step by step ({d.timeline!.length})</summary>
      <ol className="events">
        {d.timeline!.map((t, i) => (
          <li key={i}>
            <span className="ev-time mono">{clock(t.at)}</span>
            <span className="dep-tl-step mono">{t.step}</span>
            <span className={`dpill dpill-${phaseTone(t.phase)}`}>{phaseLabel(t.phase)}</span>
            <span className="ev-text">{t.detail}</span>
          </li>
        ))}
      </ol>
    </details>
  );
}

// NoDeployment explains a deploy or rollback step that has no deployment
// (yet): waiting for its environment's lock, not reached, skipped.
function NoDeployment({ run, step, env, now }: { run: RunDetail; step: StepView; env: string; now: number }) {
  const lockWait = step.status === "waiting" && step.wait_kind === "lock";
  const holder = useLockHolder(run.id, step.id, lockWait);
  let body;
  if (lockWait) {
    body = (
      <div className="callout callout-wait dep-lockwait">
        <span>
          <strong>Waiting for {env}'s lock</strong>{step.started_at ? <> since {ago(step.started_at, now)}</> : null}.{" "}
          {holder
            ? <>{terms().say("{Run}")} <Link href={`/runs/${holder.run}?step=${encodeURIComponent(holder.step ?? "")}`}>#{holder.number}</Link> holds it{holder.step ? <> with {holder.step}</> : null}{holder.version ? <>, deploying <Mono>{holder.version}</Mono></> : null}. </>
            : <>{terms().say("Another run is {deploy:doing} to")} {env}. </>}
          {terms().say("Only one {run} {deploy:does} a service to an {environment} at a time; this one goes ahead when that deployment is done.")}
          {" "}See <Link href="/deployments/environments">{terms().say("{Environments}")}</Link>.
        </span>
      </div>
    );
  } else if (step.status === "skipped") {
    body = (
      <p className="sub">
        {step.skip_kind === "not_needed" ? <>Not needed: {step.error || terms().say("there was nothing to {rollback:do}")}.</>
          : step.skip_kind === "superseded" ? <>Superseded by a newer run: a newer run of the same service started waiting for {env}{terms().say("'s lock, so this older one did not {deploy:do}.")}{step.error ? ` ${step.error}.` : ""}</>
          : <>{step.error || "Skipped"}{terms().say(". Nothing was {deploy:did}.")}</>}
      </p>
    );
  } else if (step.status === "pending" || step.status === "queued") {
    body = <p className="sub">{step.kind === "rollback"
      ? "Runs only if a step guarding the deployment fails: it then reverts the deployment's commit."
      : step.kind === "rollout" ? `Acts on the rollout of ${step.deployment || "its deployment"} once ${step.needs?.length ? step.needs.join(", ") : terms().say("the {run}")} ${step.needs && step.needs.length > 1 ? "have" : "has"} succeeded.`
      : terms().say(`{Deploy:does} to ${env || terms().say("its {environment}")} once ${step.needs?.length ? step.needs.join(", ") : terms().say("the {run}")} ${step.needs && step.needs.length > 1 ? "have" : "has"} succeeded.`)}</p>;
  } else if (step.status === "canceled") {
    body = <p className="sub">{terms().say("Canceled before it {deploy:did} anything.")}</p>;
  } else {
    body = <p className="sub">This server has no deployment recorded for this step.</p>;
  }
  return (
    <section className="card dep" aria-label="Deployment">
      <div className="dep-head">
        <h3 className="h-sm">{step.kind === "rollback" ? terms().say("{Rollback}") : step.kind === "rollout" ? `Rollout of ${step.deployment ?? "a deployment"}` : env ? terms().say(`{Deploy:doing} to ${env}`) : "Deployment"}</h3>
      </div>
      {body}
    </section>
  );
}

// useLockHolder finds, in the run's events, which deployment holds the lock
// a step waits for.
function useLockHolder(runId: string, stepId: string, active: boolean) {
  const [holder, setHolder] = useState<{ run: string; number: number; step?: string; version?: string } | null>(null);
  useEffect(() => {
    if (!active) { setHolder(null); return; }
    let alive = true;
    api.events(runId).then((evs: RunEvent[]) => {
      if (!alive) return;
      const last = [...evs].reverse().find((e) => e.type === "deploy.waiting_lock" && e.data.step === stepId);
      const h = (last?.data.deploy as { holder?: { run: string; number: number; step?: string; version?: string } } | undefined)?.holder;
      setHolder(h ?? null);
    }).catch(() => {});
    return () => { alive = false; };
  }, [runId, stepId, active]);
  return holder;
}

// DeploymentSummary lists a run's deployments in a line each, near the top
// of the run page; choosing one shows its step.
export function DeploymentSummary({ run, onSelect }: { run: RunDetail; onSelect: (step: string) => void }) {
  const list = run.deployments ?? [];
  const live = list.some((d) => !d.verdict);
  const now = useNow(1000, live);
  if (list.length === 0) return null;
  return (
    <section className="depsum" aria-label={terms().say("Deployments of this {run}")}>
      {list.map((d) => (
        <button key={d.id} type="button" className="depsum-row" onClick={() => onSelect(d.verdict === "rolled_back" || d.verdict === "rollback_failed" ? (d.rollback_step ?? d.step) : d.step)}>
          <span className="depsum-env">{d.environment}</span>
          <span className="depsum-what">
            {d.service} <Mono>{d.previous_version || "?"}</Mono> → <Mono>{d.version}</Mono>
          </span>
          <DeployStatePill d={d} now={now} />
          {d.holding && <span className="dpill dpill-lock">holds the lock</span>}
          {!d.verdict && <span className="depsum-fresh"><FreshnessLine d={d} now={now} compact /></span>}
        </button>
      ))}
    </section>
  );
}
