import type { ReactNode } from "react";
import type { RunStatus, StepStatus } from "../api";
import { onLink } from "../router";
import { terms, useTerms } from "../terms";
import { SpanHidden, SpanMono } from "../kit/bits";

type AnyStatus = RunStatus | StepStatus;

const LABEL: Record<AnyStatus, string> = {
  queued: "Queued", pending: "Waiting", leased: "Starting", running: "Running", waiting: "Waiting",
  succeeded: "Succeeded", failed: "Failed", skipped: "Skipped", canceled: "Canceled",
};

export function tone(s: AnyStatus): string {
  switch (s) {
    case "succeeded": return "ok";
    case "failed": return "bad";
    case "running": case "leased": return "run";
    case "canceled": return "warn";
    case "waiting": return "wait";
    default: return "idle";
  }
}

export function StatusIcon({ status, size = 16 }: { status: AnyStatus; size?: number }) {
  const common = { width: size, height: size, viewBox: "0 0 16 16", "aria-hidden": true } as const;
  switch (status) {
    case "succeeded":
      return <svg {...common} className="ic ic-ok"><circle cx="8" cy="8" r="7" /><path d="M4.8 8.2l2.1 2.1 4.3-4.4" fill="none" /></svg>;
    case "failed":
      return <svg {...common} className="ic ic-bad"><circle cx="8" cy="8" r="7" /><path d="M5.5 5.5l5 5M10.5 5.5l-5 5" fill="none" /></svg>;
    case "running": case "leased":
      return <svg {...common} className="ic ic-run spin"><circle cx="8" cy="8" r="6" fill="none" strokeOpacity=".25" /><path d="M8 2a6 6 0 0 1 6 6" fill="none" /></svg>;
    case "canceled":
      return <svg {...common} className="ic ic-warn"><circle cx="8" cy="8" r="7" /><path d="M5 8h6" fill="none" /></svg>;
    case "waiting":
      return <svg {...common} className="ic ic-wait"><circle cx="8" cy="8" r="7" /><path d="M6.3 5.2v5.6M9.7 5.2v5.6" fill="none" /></svg>;
    case "skipped":
      return <svg {...common} className="ic ic-idle"><circle cx="8" cy="8" r="6.5" fill="none" /><path d="M5 11L11 5" fill="none" /></svg>;
    default:
      return <svg {...common} className="ic ic-idle"><circle cx="8" cy="8" r="6.5" fill="none" strokeDasharray="2.5 2" /></svg>;
  }
}

/** Which statuses have a word in the vocabulary (the others are shown as they are: Starting, Skipped). */
const STATUS_KEY: Partial<Record<string, string>> = { queued: "queued", running: "running", succeeded: "succeeded", failed: "failed", canceled: "canceled", waiting: "waiting", rolled_back: "rolled_back" };

/** A status in words: the themed word and the plain word, which are the same word in the plain set and for Failed. */
export function statusParts(s: string): { themed: string; plain: string } {
  const t = terms();
  const k = STATUS_KEY[s];
  const plain = LABEL[s as AnyStatus] ?? (k ? t.plain(k) : s);
  if (!k) return { themed: plain, plain };
  return { themed: t.word(k), plain: t.plain(k) };
}

/** StatusPill is THE way a run, step or rollback status is shown: an icon, the themed word and, beside it, the plain
 * word ("Airborne · running"), so it never relies on color or on knowing the theme. In the business set, and where the
 * two words are the same, the word appears once. The plain word is always in the text for assistive technology. */
export function StatusPill({ status, label }: { status: AnyStatus | "rolled_back"; label?: string }) {
  useTerms();
  const p = label ? null : statusParts(status);
  return (
    <span className={`pill pill-${tone(status as AnyStatus)}`} data-status={status}>
      <StatusIcon status={status as AnyStatus} size={14} />
      {label ?? p!.themed}
      {p && p.themed.toLowerCase() !== p.plain.toLowerCase() && <span className="pill-plain"><span aria-hidden="true">{" · "}</span><SpanHidden>, </SpanHidden>{p.plain.toLowerCase()}</span>}
    </span>
  );
}

/** The status as one phrase, for an accessible name or a sentence. */
export function statusLabel(s: AnyStatus | string): string {
  const p = statusParts(s);
  return p.themed.toLowerCase() === p.plain.toLowerCase() ? p.plain : `${p.themed} (${p.plain.toLowerCase()})`;
}

/** The status a step is drawn with: a deploy step waiting for its
 * deployment to finish is at work, so it is drawn as running. */
export function shownStatus(s: { status: StepStatus; wait_kind?: string }): StepStatus {
  return s.status === "waiting" && s.wait_kind === "observe" ? "running" : s.status;
}

/** What a step does, in the words its status pill uses: an approval waits
 * for a decision, a deploy step waits for its environment's lock or for the
 * deployment to finish. phase, if given, is where a deployment stands. */
export function stepLabel(s: {
  status: StepStatus; kind?: string; wait_kind?: string; environment?: string; skip_kind?: string; policy_blocked?: boolean;
  allowed_failure?: boolean;
}, phase?: string): string {
  if (s.status === "failed" && s.policy_blocked) return "Blocked by policy";
  if (s.status === "failed" && s.allowed_failure) return "Failed (allowed)";
  if (s.status === "waiting") {
    // A verification that was inconclusive waits for someone to mark it.
    if (s.kind === "verify" || s.wait_kind === "verify") return "Waiting for a person";
    if (s.kind === "approval" || (!s.kind && !s.wait_kind)) return terms().say("Waiting for {clearance}");
    const env = s.environment;
    if (s.wait_kind === "lock") return env ? `Waiting for ${env}'s lock` : terms().say("Waiting for the {environment}'s lock");
    const what = s.kind === "rollback" ? terms().say("{Rollback:doing}") : s.kind === "rollout" ? (env ? `Acting on the rollout in ${env}` : "Acting on the rollout") : env ? terms().say(`{Deploy:doing} to ${env}`) : "Deploying";
    return phase ? `${what}: ${phase}` : what;
  }
  if (s.status === "skipped") {
    if (s.skip_kind === "not_needed") return "Not needed";
    if (s.skip_kind === "superseded") return "Superseded by a newer run";
    if (s.skip_kind === "condition") return "Condition not met";
    if (s.skip_kind === "paths") return "Skipped: no matching file changed";
  }
  return statusLabel(s.status);
}

export function StepStrip({ steps }: { steps: { id: string; status: StepStatus }[] }) {
  return (
    <span className="strip" role="img" aria-label={steps.map((s) => `${s.id} ${statusLabel(s.status)}`).join(", ")}>
      {steps.map((s) => <span key={s.id} className={`strip-cell t-${tone(s.status)}`} title={`${s.id}: ${statusLabel(s.status)}`} />)}
    </span>
  );
}

export function Link({ href, children, className }: { href: string; children: ReactNode; className?: string }) {
  return <a href={href} onClick={onLink} className={className}>{children}</a>;
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <div className="empty-title">{title}</div>
      {children && <div className="empty-body">{children}</div>}
    </div>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  return <div className="errorbox" role="alert">{error instanceof Error ? error.message : String(error)}</div>;
}

export function Mono({ children }: { children: ReactNode }) {
  return <SpanMono>{children}</SpanMono>;
}

export function BranchIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <circle cx="4.5" cy="3.5" r="1.6" /><circle cx="4.5" cy="12.5" r="1.6" /><circle cx="11.5" cy="5.5" r="1.6" />
      <path d="M4.5 5.1v5.8M11.5 7.1c0 2.4-2 3-7 3.8" fill="none" />
    </svg>
  );
}

export function CommitIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <circle cx="8" cy="8" r="2.6" /><path d="M1 8h4.4M10.6 8H15" fill="none" />
    </svg>
  );
}
