import type { RunEstimate, StepEstimate, StepView } from "./api";
import { duration } from "./format";
import { terms } from "./terms";

/** approx writes a time the rounded way estimates are shown: "40s", "2 min", "1 h 10 min". It never claims
 * a precision the history does not have. */
export function approx(ms: number): string {
  const s = Math.max(0, ms) / 1000;
  if (s < 45) return `${Math.max(1, Math.round(s))}s`;
  const min = Math.round(s / 60);
  if (s < 90 * 60) return `${Math.max(1, min)} min`;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 300) * 5;
  return m === 0 ? `${h} h` : `${h} h ${m} min`;
}

export type StepTimingKind = "none" | "unknown" | "normal" | "slow" | "done";
export interface StepTiming {
  kind: StepTimingKind;
  /** The one sentence: "1m 12s of about 2 min". */
  text: string;
  /** 0..1 for the small indicator; undefined when there is nothing to measure against. */
  fraction?: number;
  /** What it usually takes, for the title of the indicator. */
  usual?: string;
}

/** usualText says what a step usually takes. */
export function usualText(e: StepEstimate): string {
  if (e.bimodal) return `about ${approx(e.typical_ms ?? 0)} (${approx(e.bimodal.slow_ms)} when the faster case does not apply)`;
  return `about ${approx(e.typical_ms ?? 0)}`;
}

/** stepTiming compares a running step with what it usually takes. */
export function stepTiming(s: Pick<StepView, "status" | "started_at" | "finished_at" | "estimate">, now: number): StepTiming {
  const e = s.estimate;
  const running = s.status === "running" || s.status === "leased";
  if (!e || e.person) return { kind: "none", text: "" };
  if (!e.enough) {
    if (!running && s.status !== "pending" && s.status !== "queued") return { kind: "none", text: "" };
    return { kind: "unknown", text: e.samples === 0 ? "No estimate yet: this step has not run before." : `No estimate yet: ${e.samples} earlier run${e.samples === 1 ? "" : "s"}, three are needed.` };
  }
  const typical = e.typical_ms ?? 0;
  if (s.status === "succeeded" && s.started_at > 0 && s.finished_at > 0) {
    const took = s.finished_at - s.started_at;
    return { kind: "done", text: `took ${duration(took)}, usually ${usualText(e)}`, usual: usualText(e) };
  }
  if (!running || s.started_at <= 0) return { kind: "normal", text: `usually ${usualText(e)}`, usual: usualText(e) };
  const el = Math.max(0, now - s.started_at);
  if (e.slow_ms && el > e.slow_ms) {
    return { kind: "slow", text: `taking longer than usual (usually ~${approx(typical)}, now ${approx(el)})`, fraction: 1, usual: usualText(e) };
  }
  return { kind: "normal", text: `${duration(el)} of ${usualText(e)}`, fraction: typical > 0 ? Math.min(1, el / typical) : undefined, usual: usualText(e) };
}

export interface RunTiming { kind: "left" | "none" | "took" | "waiting" | "none-final"; text: string; detail?: string }

/** runTiming is the line at the top of a run: the time left, why there is no estimate, or how long it took
 * against what is usual. now is the browser's clock; the server's remaining time was worked out at as_of. */
export function runTiming(e: RunEstimate | undefined, status: string, now: number): RunTiming {
  if (!e) return { kind: "none-final", text: "" };
  if (status === "succeeded" && e.took_ms && e.known) {
    const d = e.diff_percent ?? 0;
    const how = d <= -5 ? `${-d}% faster than usual` : d >= 5 ? `${d}% slower than usual` : "about as long as usual";
    return { kind: "took", text: `took ${duration(e.took_ms)}, ${how}` };
  }
  if (status === "succeeded" || status === "failed" || status === "canceled") return { kind: "none-final", text: "" };
  if (!e.known) {
    return { kind: "none", text: e.reason === "no_history" ? "No estimate yet: these steps have not run before." : "No estimate yet: a step needs three earlier runs." };
  }
  const waiting = (e.waiting_for_person?.length ?? 0) > 0;
  const gone = Math.max(0, now - e.as_of);
  const left = Math.max(0, (e.remaining_ms ?? 0) - gone);
  let text: string;
  if (left <= 0 && !waiting) text = e.overrun ? "taking longer than usual" : "about to finish";
  else if (left <= 0) text = terms().say("waiting for {clearance}");
  else text = `about ${approx(left)} left`;
  if (waiting && left > 0) text += terms().say(", after {clearance}");
  const detail: string[] = [];
  if (waiting) detail.push(terms().say("Waiting for {clearance}") + `: ${e.waiting_for_person!.join(", ")}. That wait is not counted.`);
  if (e.partial) detail.push(`At least that: ${e.unknown_steps!.join(", ")} ${e.unknown_steps!.length === 1 ? "has" : "have"} no history yet.`);
  if (e.overrun) detail.push("A step is taking longer than usual, so this is less certain.");
  if ((e.queued?.length ?? 0) > 0) detail.push(`Queued: ${e.queued!.map((q) => q.reason ? `${q.step} (${q.reason})` : q.step).join("; ")}. Queue time is not counted.`);
  detail.push("From the same steps of earlier runs of this pipeline; steps that run side by side count once.");
  return { kind: waiting && left <= 0 ? "waiting" : "left", text, detail: detail.join(" ") };
}
