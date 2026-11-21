// What a person deciding an approval needs to be told, worked out from real fields: why they may not
// approve, what approving and rejecting do, and a link to the change itself. Plain functions, tested.

import type { Approval, Me, Project, RunDetail, RunSummary, StepView } from "../api";
import type { ApprovalSettings } from "../api/v2b";
import { terms } from "../terms";
import { canOperate } from "../auth";
import { hostBase } from "./code";

export interface Refusal {
  /** The reason in plain words: who, what, and what to do. */
  text: string;
  /** A link that fixes it, when there is one. */
  link?: { href: string; label: string };
  /** The person may still reject. */
  mayReject: boolean;
}

/** Why this person may not approve, or null when they may (the server decides in the end). */
export function approvalRefusal(a: Pick<Approval, "project" | "started_by">, me: Me | null, s: ApprovalSettings | null): Refusal | null {
  if (!me) return null;
  if (!canOperate(me, a.project)) {
    const role = me.roles[a.project];
    return {
      text: terms().say(`You are signed in as ${me.user || "a guest"}, ${role ? `with the ${role} role` : "without a role"} on ${a.project}. Giving {clearance} needs the operator role there. An administrator can give it in Settings → People and access.`),
      link: { href: "/settings/access", label: "People and access" }, mayReject: false,
    };
  }
  const starter = a.started_by !== "" && a.started_by === me.user;
  if (starter && s && !s.allow_self) {
    return {
      text: terms().say(`You started this {run}. On this {installation} the person who starts a {run} cannot give {clearance} for it, so someone else must. You may still say {No_go}. An administrator can change this in Settings → {Clearances}.`),
      link: { href: "/settings/approvals", label: terms().say("Settings → {Clearances}") }, mayReject: true,
    };
  }
  return null;
}

/** A link to the commit on the repository's host, when the repository has an address on the web. */
export function commitLink(p: Pick<Project, "url"> | undefined, sha: string): string | null {
  const base = p && sha ? hostBase(p.url) : null;
  return base ? `${base}/commit/${sha}` : null;
}

/** What approving and rejecting do, from the steps that wait on this one. */
export function consequences(run: RunDetail | null, stepId: string): { approve: string; reject: string } {
  const after = run ? run.step_list.filter((s) => s.needs?.includes(stepId)).map((s) => s.id) : [];
  const approve = after.length > 0
    ? `${list(after)} ${after.length === 1 ? "goes" : "go"} ahead.`
    : terms().say("The {run} goes on.");
  return { approve, reject: terms().say("The {run} fails here and the steps after it are skipped. Resuming the {run} asks again.") };
}

function list(xs: string[]): string {
  if (xs.length <= 1) return xs.join("");
  return xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1];
}

/** Steps that had succeeded when the approval asked. */
export function succeededBefore(run: RunDetail, step: StepView | undefined): string[] {
  return run.step_list.filter((s) => s.status === "succeeded" && s.id !== step?.id && (!step?.started_at || s.finished_at <= step.started_at)).map((s) => s.id);
}

// ---- runs in a list ----

export type Tick = "ok" | "bad" | "run" | "wait" | "skip";

/** One run as a tick of a trend. */
export function tickOf(r: Pick<RunSummary, "status" | "waiting">): Tick {
  if (r.status === "succeeded") return "ok";
  if (r.status === "failed") return "bad";
  if (r.status === "canceled") return "skip";
  return (r.waiting?.length ?? 0) > 0 ? "wait" : "run";
}

export interface Trend { ticks: Tick[]; passed: number; failed: number; finished: number; rate: number | null; median: number | null }

/** The last runs, oldest first, as ticks, with the pass rate of the finished ones and their middle duration. */
export function trendOf(runs: Pick<RunSummary, "status" | "waiting" | "started_at" | "created_at" | "finished_at">[], keep = 20): Trend {
  const last = runs.slice(0, keep).reverse();
  const done = last.filter((r) => r.status === "succeeded" || r.status === "failed");
  const passed = done.filter((r) => r.status === "succeeded").length;
  const times = done.filter((r) => r.finished_at > 0).map((r) => r.finished_at - (r.started_at || r.created_at)).filter((x) => x > 0).sort((a, b) => a - b);
  return {
    ticks: last.map(tickOf), passed, failed: done.length - passed, finished: done.length,
    rate: done.length ? Math.round((passed / done.length) * 100) : null, median: times.length ? times[Math.floor(times.length / 2)] : null,
  };
}

/** The run's state in one plain sentence, from its real steps. */
export function runSentence(r: Pick<RunSummary, "status" | "steps" | "waiting" | "cancel_requested" | "cancel_reason">, dur: string): string {
  const failed = r.steps.find((s) => s.status === "failed");
  switch (r.status) {
    case "succeeded": return dur ? `Passed in ${dur}.` : "Passed.";
    case "failed": return failed ? `Stopped at ${failed.id}${dur ? ` after ${dur}` : ""}.` : "Failed.";
    case "canceled": return r.cancel_reason ? `Canceled: ${r.cancel_reason}.` : "Canceled before it finished.";
    case "queued": return terms().say("Waiting for a {runner} to pick it up.");
    default: {
      if (r.cancel_requested) return "Stopping: cancel was asked for.";
      if ((r.waiting?.length ?? 0) > 0) return `Waiting for a person to decide: ${r.waiting.join(", ")}.`;
      const now = r.steps.find((s) => s.status === "running");
      return now ? `Running ${now.id}.` : "Running.";
    }
  }
}
