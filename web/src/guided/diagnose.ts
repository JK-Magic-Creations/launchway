// Why a deployment failed or waits, in one plain sentence, composed from what the deploy runner really
// observed: the deployment's phase and detail, Argo CD's operation and health, the rollout and its failed
// analysis runs, and the tail of the deploy step's log. Nothing is invented: when the data is not enough
// the diagnosis says "Launchway cannot tell why" and points to the engine's own console.

import type { DeploymentView, DeployAnalysis, DeployApp, DeployRollout, LiveView, RolloutAction, StepView } from "../api";
import { terms } from "../terms";

export type DiagTone = "bad" | "wait" | "run" | "ok" | "quiet";

export interface Evidence {
  text: string;
  /** When it was observed or happened (Unix ms), if known. */
  at?: number;
  /** Where in Argo CD to look: an application resource, by kind and name. */
  resource?: { kind: string; name: string; namespace?: string; group?: string };
}

export interface Recommend {
  /** A rollout command that exists, or "open" to look in the engine first. */
  action: RolloutAction | "open";
  label: string;
  /** What it does, in words. */
  why: string;
}

export interface Diagnosis {
  tone: DiagTone;
  headline: string;
  detail?: string;
  evidence: Evidence[];
  /** False when Launchway cannot tell why: the data is not enough. */
  known: boolean;
  recommend: Recommend | null;
}

const failedAnalyses = (list: DeployAnalysis[] | undefined) => (list ?? []).filter((a) => a.phase === "Failed" || a.phase === "Error");

function analysisEvidence(a: DeployAnalysis): string {
  const bad = (a.metrics ?? []).filter((m) => m.failed > 0 || m.error > 0 || m.phase === "Failed" || m.phase === "Error");
  const m = bad[0];
  const what = m ? `${m.name} failed ${m.failed > 0 ? `${m.failed} of ${m.count} measurements` : "to measure"}` : "";
  return `The rollout's check ${a.name} ${a.phase === "Error" ? "could not run" : "failed"}${what ? `: ${what}` : ""}${a.message ? ` (${a.message})` : ""}.`;
}

/** The last few lines of a failed step's log, without blank lines. */
export function logTail(excerpt: string | undefined, n = 3): string[] {
  return (excerpt ?? "").split("\n").map((l) => l.trim()).filter(Boolean).slice(-n);
}

function unhealthy(app: DeployApp | undefined): Evidence[] {
  return (app?.resources ?? []).filter((r) => !r.found || (r.health && r.health !== "Healthy" && r.health !== "") || r.sync === "OutOfSync")
    .slice(0, 4).map((r) => ({
      text: `${r.kind} ${r.name} ${!r.found ? "was not found" : r.health && r.health !== "Healthy" ? `is ${r.health}` : "differs from Git"}${r.message ? `: ${r.message}` : ""}.`,
      resource: { kind: r.kind, name: r.name, namespace: r.namespace, group: r.group },
    }));
}

/** Whether a rollout action makes sense for the rollout as seen. */
function canDo(action: RolloutAction, ro?: DeployRollout): boolean {
  if (!ro || !ro.found) return false;
  const done = ro.phase === "Healthy" && !!ro.stable_rs && ro.stable_rs === ro.current_pod_hash && !ro.aborted;
  switch (action) {
    case "promote": return !ro.aborted && !done && (ro.paused || (ro.pause_reasons?.length ?? 0) > 0);
    case "abort": return !ro.aborted && !done;
    case "retry": return ro.aborted;
    default: return false;
  }
}

/** The diagnosis of a service's newest deployment. live is what the runner saw of the service while idle. */
export function diagnose(d: DeploymentView | undefined, live: LiveView | undefined, step: StepView | undefined, now: number): Diagnosis {
  if (!d) {
    if (live?.drift) return driftDiagnosis(live);
    return { tone: "quiet", headline: terms().say("Nothing is being {deploy:did}."), detail: "No deployment has been made to this application by a pipeline yet.", evidence: [], known: true, recommend: null };
  }
  const snap = d.snapshot;
  const app = snap?.app;
  const ro = snap?.rollout;
  const ev: Evidence[] = [];
  const add = (text: string, at?: number) => { if (text) ev.push({ text, at }); };
  const bad = failedAnalyses(snap?.analysis);
  for (const a of bad) add(analysisEvidence(a), d.observed_at);
  if (app?.operation && app.operation.phase !== "Succeeded") add(`The last sync ${app.operation.phase.toLowerCase()}${app.operation.message ? `: ${app.operation.message}` : ""}.`, app.operation.finished_at ?? app.operation.started_at);
  if (app?.source_error) add(`Argo CD cannot read the source: ${app.source_error}`, d.observed_at);
  if (ro?.message && (ro.phase === "Degraded" || ro.aborted)) add(`The rollout says: ${ro.message}`, d.observed_at);
  if (app?.health_message && app.health !== "Healthy") add(`Argo CD says: ${app.health_message}`, d.observed_at);
  for (const e of unhealthy(app)) ev.push({ ...e, at: d.observed_at });
  const tail = logTail(step?.excerpt);
  if (tail.length > 0) add(`The deploy step's last lines: ${tail.join(" / ")}`, step?.finished_at || undefined);
  if (d.detail) add(`Launchway's note: ${d.detail}`, d.observed_at);
  const uniq = ev.filter((e, i) => ev.findIndex((x) => x.text === e.text) === i);
  const timeline = (d.timeline ?? []).slice(-3).map((t) => ({ text: `${t.step}: ${t.detail || t.phase}`, at: t.at }));

  const ver = d.version;
  const out = (tone: DiagTone, headline: string, detail?: string, recommend: Recommend | null = null, known = true): Diagnosis =>
    ({ tone, headline, detail, evidence: uniq.length > 0 ? uniq : timeline, known, recommend });
  const cannot = (what: string, recommend: Recommend | null) => out("bad", `${what} Launchway cannot tell why.`,
    terms().say("The {deploy:do} {runner} has not reported a reason. Argo CD's own events, in the console, show what it found."), recommend ?? OPEN, false);

  switch (d.verdict) {
    case "deployed":
      return live?.drift ? driftDiagnosis(live, terms().say(`Version ${ver} was {deploy:did}, but it no longer matches.`)) : out("ok", `Version ${ver} is running.`, d.deployed_at ? terms().say("{Deploy:did} and healthy when last observed.") : undefined);
    case "rolled_back":
      return out("wait", `Version ${ver} did not hold, so the previous version was put back.`, d.detail || terms().say("The {rollback} step reverted the commit and the {environment} was healthy again."), { action: "open", label: "Look at what failed", why: terms().say("Fix the cause, then push again: a new {run} {deploy:does} the next attempt.") });
    case "rollback_failed":
      return out("bad", terms().say(`The {rollback} of ${ver} failed: the {environment} needs a person.`), d.detail || undefined, OPEN);
    case "abandoned":
      return out("quiet", terms().say(`{Deploy:doing} ${ver} was abandoned.`), terms().say("The run was canceled before the version was {deploy:did}."));
    case "failed":
      return uniq.length === 0 ? cannot(terms().say(`{Deploy:doing} ${ver} failed.`), null) : out("bad", terms().say(`{Deploy:doing} ${ver} failed.`), d.detail || terms().say("The deploy step failed and nothing rolled it back: the {environment} may run something in between."), OPEN);
  }

  switch (d.phase) {
    case "app_missing": return out("bad", `Argo CD has no application called ${d.application}.`, "The deployment waits for an application that does not exist in the engine.", OPEN);
    case "source_error": return out("bad", terms().say("Argo CD cannot read the repository it {deploy:does} from."), app?.source_error, OPEN);
    case "sync_failed": return out("bad", "The sync failed: Argo CD could not apply the change.", app?.operation?.message, OPEN);
    case "sync_blocked": return out("wait", "The sync is held back.", app?.window_blocked ? `A sync window holds it: ${app.window_blocked}` : app?.operation?.message ?? "Argo CD is not allowed to sync yet.", OPEN);
    case "degraded": {
      const r: Recommend | null = canDo("abort", ro) ? { action: "abort", label: terms().say("{Rollback:do}"), why: "Stops the rollout: all traffic goes back to the stable version." } : OPEN;
      return uniq.length === 0 ? cannot(`${ver} is not healthy.`, r) : out("bad", `Version ${ver} started, but it is not healthy.`, app?.health_message, r);
    }
    case "rollout_aborted": {
      const r: Recommend = canDo("retry", ro) ? { action: "retry", label: "Retry", why: "Starts the aborted rollout again from its first step." } : OPEN;
      return bad.length > 0 ? out("bad", `The rollout was stopped: its check ${bad[0].name} failed. Traffic is back on the stable version.`, ro?.message, r)
        : out("bad", "The rollout was aborted. Traffic is on the stable version.", ro?.message, r);
    }
    case "rollout_paused": {
      const reasons = ro?.pause_reasons?.length ? ro.pause_reasons.join(", ") : "a pause";
      const who = ro?.pause_indefinite ? "It waits for a person to promote it." : ro?.pause_until && ro.pause_until > now ? "It goes on by itself when the pause ends." : "It waits at the pause.";
      return out("wait", `Version ${ver} is paused at step ${(ro?.step ?? 0) + 1} of ${ro?.steps ?? "?"}. ${who}`, `Paused for: ${reasons}.`,
        canDo("promote", ro) ? { action: "promote", label: "Promote", why: "Lets the rollout go on past the pause it waits at." } : null);
    }
    case "rollout_progressing":
      return out("run", `Version ${ver} is rolling out${ro?.canary_weight !== undefined ? `: ${ro.canary_weight}% of traffic is on it` : ""}.`, ro ? `${ro.ready} of ${ro.replicas} copies ready${ro.step !== undefined ? `, step ${Math.min(ro.step + 1, ro.steps)} of ${ro.steps}` : ""}.` : undefined,
        bad.length > 0 && canDo("abort", ro) ? { action: "abort", label: terms().say("{Rollback:do}"), why: "Stops the rollout: all traffic goes back to the stable version." } : null);
    case "syncing": return out("run", `Argo CD is applying version ${ver}.`, app?.operation?.message);
    case "progressing": return out("run", `Version ${ver} is starting up.`, app?.health_message);
    case "command_pending": return out("run", terms().say("A command waits for a {deploy:do} {runner} to carry it out."), d.detail || undefined);
    case "healthy": return out("ok", terms().say(`Version ${ver} is {deploy:did} and healthy.`));
    case "unobserved": default: {
      const unserved = !d.observer;
      return out("wait", unserved ? terms().say(`No {deploy:do} {runner} serves target ${d.target}, so nothing is observed.`) : "Launchway has not seen this deployment yet.",
        unserved ? terms().say("The deployment cannot finish until a {deploy:do} {runner} for that target is running.") : terms().say(`The {runner} ${d.observer} will report soon.`), null);
    }
  }
}

const OPEN: Recommend = { action: "open", label: "Look at it in Argo CD", why: "The engine's own console shows the events, the live objects and the logs." };

function driftDiagnosis(live: LiveView, headline?: string): Diagnosis {
  const text = live.drift === "missing" ? "The application is gone." : live.drift === "out_of_sync" ? "What runs differs from Git." : "What runs differs from the last deployment.";
  return { tone: live.drift === "missing" ? "bad" : "wait", headline: headline ?? text, detail: live.drift_detail || text,
    evidence: [{ text: `${text}${live.drift_detail ? ` ${live.drift_detail}` : ""}`, at: live.observed_at }], known: true, recommend: OPEN };
}

/** One plain sentence about the state of a deployment, for a row in a list. */
export function oneLine(dg: Diagnosis): string { return dg.headline; }
