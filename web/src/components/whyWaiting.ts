// Every wait says why. A step that is not running and not finished is waiting for something, and the
// server already knows what: the steps it needs, a retry's timer, the runner hints, an approval's
// message, a deployment's own explanation (its `detail`). This turns those into one plain sentence,
// so the page never shows a bare "waiting".

import type { RunDetail, StepView } from "../api";
import { clock, duration } from "../format";
import { deploymentOf, environmentOf } from "./Deployment";
import { terms } from "../terms";

export interface Why { text: string; /** The same in a few words, for a station of the line. */ short: string; /** What the wait is for: a person, a runner, another step, the environment, the timer. */ kind: "person" | "runner" | "step" | "environment" | "timer" | "engine" }

const done = (s: StepView | undefined) => !s || s.status === "succeeded" || s.status === "skipped" || s.status === "canceled" || (s.status === "failed" && !!s.allowed_failure);

/** Why a step is waiting, or null when it is not (running, or ended). */
export function whyWaiting(run: RunDetail, step: StepView, now: number): Why | null {
  switch (step.status) {
    case "pending": {
      if (run.lane?.state === "waiting") return { kind: "step", short: "Waiting for its lane", text: run.lane.reason || `The whole run waits its turn in the lane ${run.lane.label}.` };
      const open = (step.needs ?? []).filter((n) => !done(run.step_list.find((s) => s.id === n)));
      if (open.length > 0) return { kind: "step", short: `After ${open.join(" and ")}`, text: `Waiting for ${open.join(" and ")} to finish first.` };
      return { kind: "step", short: "Waiting for its turn", text: "Waiting for its turn: the steps before it have not finished." };
    }
    case "queued": {
      if ((step.not_before ?? 0) > now) {
        const cause = step.failure_cause && step.failure_cause !== "exit" ? ` after ${step.failure_cause}` : step.failure_cause === "exit" ? " after a failed exit" : "";
        return { kind: "timer", short: `Retrying in ${duration(step.not_before! - now)}`, text: `Trying again in ${duration(step.not_before! - now)} (attempt ${step.attempt} of ${step.retry + 1})${cause}. The wait is the step's own retry delay.` };
      }
      const hint = run.pool_hint || run.runner_hint;
      const for_ = step.queued_at > 0 ? ` It has waited ${duration(Math.max(0, now - step.queued_at))}.` : "";
      return { kind: "runner", short: "Waiting for a runner", text: hint ? `${hint}${for_}` : `Waiting for a runner to take it.${for_}` };
    }
    case "leased":
      return { kind: "runner", short: "Starting", text: `${step.runner ? step.runner : "A runner"} has taken it and is starting it.` };
    case "waiting":
      return waitingWhy(run, step);
    default:
      return null;
  }
}

function waitingWhy(run: RunDetail, step: StepView): Why {
  if (step.kind === "approval") {
    const by = step.deadline > 0 ? ` Approve or reject (${terms().label("go")} or ${terms().label("no_go")}) before ${clock(step.deadline)}, or it is refused.` : "";
    return { kind: "person", short: "Waiting for a person", text: `Waiting for a person to give ${terms().lower("clearance")}${step.message ? `: ${step.message}` : "."}${by}` };
  }
  if (step.kind === "verify" || step.wait_kind === "verify") {
    return { kind: "person", short: "Waiting for a person", text: "The checks could not decide whether the deployment is healthy, so a person must mark it passed or failed." };
  }
  const env = environmentOf(run, step);
  const d = deploymentOf(run, step);
  if (step.wait_kind === "lock") {
    const holder = d?.holding ? "" : " Another deployment holds it; this one goes when that one is done.";
    return { kind: "environment", short: `Waiting for ${env || "the"} lock`, text: `Waiting for ${env || "the environment"}'s lock, so two deployments never run at once.${holder}` };
  }
  if (d?.detail) return { kind: "engine", short: d.detail.length > 40 ? d.detail.slice(0, 38) + "…" : d.detail, text: d.detail };
  if (!d || d.phase === "unobserved" || d.phase === "") {
    const target = d?.target ? ` for ${d.target}` : "";
    return { kind: "engine", short: "No deploy runner yet", text: `The step is done on its side. No deploy runner has reported yet on what happened${target}, so Launchway cannot tell whether it worked. Check that a deploy runner serves this environment.` };
  }
  return { kind: "engine", short: "Settling", text: `Waiting for the deployment to settle (${d.phase.replace(/_/g, " ")}).` };
}

/** One sentence for the whole run: where it is, and what it waits for if it waits. */
export function runSentence(run: RunDetail, now: number): string {
  const n = `Run #${run.number}`;
  if (run.status === "canceled") return `${n} was canceled${run.cancel_reason ? `: ${run.cancel_reason}` : ""}.`;
  if (run.status === "failed") {
    const f = run.step_list.find((s) => s.status === "failed" && !s.allowed_failure);
    return f ? (f.cause?.summary ? `${n} stopped at ${f.id}. ${f.cause.summary}` : `${n} stopped at ${f.id}.`) : `${n} failed.`;
  }
  if (run.status === "succeeded") return (run.allowed_failures ?? 0) > 0 ? `${n} finished. ${run.allowed_failures} step${run.allowed_failures === 1 ? "" : "s"} failed and the pipeline allows that.` : `${n} finished. Everything passed.`;
  if (run.cancel_requested) return `${n} is being canceled.`;
  const live = run.step_list.filter((s) => s.status === "running");
  const waits = run.step_list.filter((s) => s.status === "waiting" || s.status === "queued" || s.status === "leased");
  const w = waits.length ? whyWaiting(run, waits[0], now) : null;
  const head = live.length ? `${n} is running ${live.map((s) => s.id).join(" and ")}.` : w ? `${n} is waiting.` : `${n} is not running anything right now.`;
  return w ? `${head} ${waits[0].id}: ${w.text}` : head;
}
