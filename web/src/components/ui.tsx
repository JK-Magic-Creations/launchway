import type { ReactNode } from "react";
import type { RunStatus, StepStatus } from "../api";
import { onLink } from "../router";

type AnyStatus = RunStatus | StepStatus;

const LABEL: Record<AnyStatus, string> = {
  queued: "Queued", pending: "Waiting", leased: "Starting", running: "Running", waiting: "Waiting for approval",
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

export function StatusPill({ status, label }: { status: AnyStatus; label?: string }) {
  return (
    <span className={`pill pill-${tone(status)}`}>
      <StatusIcon status={status} size={14} />
      {label ?? LABEL[status]}
    </span>
  );
}

export function statusLabel(s: AnyStatus): string { return LABEL[s]; }

export function StepStrip({ steps }: { steps: { id: string; status: StepStatus }[] }) {
  return (
    <span className="strip" role="img" aria-label={steps.map((s) => `${s.id} ${LABEL[s.status]}`).join(", ")}>
      {steps.map((s) => <span key={s.id} className={`strip-cell t-${tone(s.status)}`} title={`${s.id}: ${LABEL[s.status]}`} />)}
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
  return <span className="mono">{children}</span>;
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
