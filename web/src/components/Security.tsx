import { useState } from "react";
import type { ArtifactView, FindingCounts, FindingStatus, Severity } from "../api";

// Pieces shared by the run's Security and Artifacts tabs, the run lists, a
// project's security page and the component search.

export const SEVERITIES: Severity[] = ["critical", "high", "medium", "low", "unknown"];

const SEVERITY_LABEL: Record<Severity, string> = {
  critical: "Critical", high: "High", medium: "Medium", low: "Low", unknown: "Unknown",
};

export function severityLabel(s: Severity): string { return SEVERITY_LABEL[s] ?? s; }

export function severityRank(s: Severity): number {
  const i = SEVERITIES.indexOf(s);
  return i < 0 ? SEVERITIES.length : i;
}

/** Critical and high together: the findings that most need a look. */
export function serious(c: FindingCounts): number { return c.critical + c.high; }

/** "3 high", "1 critical", or "4 critical or high". */
export function seriousLabel(c: FindingCounts): string {
  if (c.critical && c.high) return `${serious(c)} critical or high`;
  if (c.critical) return `${c.critical} critical`;
  return `${c.high} high`;
}

/** A finding's severity in words, with a mark of its own shape for each. */
export function SeverityBadge({ s }: { s: Severity }) {
  return (
    <span className={"sevbadge sev-" + s}>
      <SeverityMark s={s} />
      {severityLabel(s)}
    </span>
  );
}

function SeverityMark({ s }: { s: Severity }) {
  const common = { width: 10, height: 10, viewBox: "0 0 10 10", "aria-hidden": true, className: "sevmark" } as const;
  switch (s) {
    case "critical": return <svg {...common}><path d="M5 .6l4.4 4.4L5 9.4.6 5z" /></svg>;
    case "high": return <svg {...common}><path d="M5 1l4.4 8H.6z" /></svg>;
    case "medium": return <svg {...common}><rect x="1.2" y="1.2" width="7.6" height="7.6" rx="1" /></svg>;
    case "low": return <svg {...common}><circle cx="5" cy="5" r="3.8" /></svg>;
    default: return <svg {...common}><circle cx="5" cy="5" r="3.4" fill="none" strokeWidth="1.6" /></svg>;
  }
}

/** A bar split by severity. The text beside it says the same. */
export function SeverityBar({ c, thin }: { c: FindingCounts; thin?: boolean }) {
  const total = Math.max(c.total, 1);
  return (
    <div className={"sevbar" + (thin ? " sevbar-thin" : "")} role="img"
      aria-label={SEVERITIES.filter((s) => c[s] > 0).map((s) => `${c[s]} ${s}`).join(", ") || "no findings"}>
      {SEVERITIES.map((s) => c[s] > 0 && <span key={s} className={"sevfill-" + s} style={{ flexGrow: c[s] / total }} />)}
    </div>
  );
}

/** The bar's legend: each severity with its count. */
export function SeverityLegend({ c, all }: { c: FindingCounts; all?: boolean }) {
  return (
    <ul className="chart-legend sevlegend" aria-label="Findings by severity">
      {SEVERITIES.filter((s) => all || c[s] > 0 || s !== "unknown").map((s) => (
        <li key={s} className={c[s] ? undefined : "faint"}><span className={"sw sevfill-" + s} />{severityLabel(s)} <strong>{c[s]}</strong></li>
      ))}
    </ul>
  );
}

/** Compact counts for a run in a list: the serious ones if any, else the total. */
export function FindingCountBadge({ c }: { c: FindingCounts }) {
  const n = serious(c);
  const title = `Security findings: ${SEVERITIES.filter((s) => c[s] > 0).map((s) => `${c[s]} ${s}`).join(", ") || "none"}`;
  return n > 0
    ? <span className="fcount fcount-bad" title={title}><ShieldIcon />{seriousLabel(c)}</span>
    : <span className="fcount" title={title}><ShieldIcon />{c.total}<span className="visually-hidden"> findings</span></span>;
}

export function ArtifactCount({ n }: { n: number }) {
  return (
    <span className="fcount" title={`${n} artifact${n === 1 ? "" : "s"} kept`}>
      <BoxIcon />{n}<span className="visually-hidden"> artifact{n === 1 ? "" : "s"}</span>
    </span>
  );
}

export function ShieldIcon({ size = 13 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <path d="M8 1.5l5.5 2v4.2c0 3.3-2.3 5.7-5.5 6.8-3.2-1.1-5.5-3.5-5.5-6.8V3.5z" />
    </svg>
  );
}

export function BoxIcon({ size = 13 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <path d="M2 4.8L8 2l6 2.8v6.4L8 14l-6-2.8z" /><path d="M2 4.8L8 7.6l6-2.8M8 7.6V14" />
    </svg>
  );
}

export function LockIcon({ size = 13 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <rect x="3" y="7" width="10" height="7" rx="1.5" /><path d="M5.2 7V5a2.8 2.8 0 0 1 5.6 0v2" />
    </svg>
  );
}

const STATUS_LABEL: Record<FindingStatus, string> = { new: "New", existing: "Existing", fixed: "Fixed" };

export function FindingStatusBadge({ s }: { s?: FindingStatus }) {
  if (s === "new") return <span className="tbadge tbadge-new" title="Not in the previous run of this branch">New</span>;
  if (s === "fixed") return <span className="tbadge fbadge-fixed" title="In the previous run of this branch, gone in this one">Fixed</span>;
  return null;
}

export function findingStatusLabel(s: FindingStatus): string { return STATUS_LABEL[s] ?? s; }

/** A location as file:line. */
export function where(f: { file?: string; start_line?: number; end_line?: number }): string {
  if (!f.file) return "";
  if (!f.start_line) return f.file;
  return f.end_line && f.end_line !== f.start_line ? `${f.file}:${f.start_line}-${f.end_line}` : `${f.file}:${f.start_line}`;
}

/** Only an http or https address is offered as a link. */
export function safeURL(u?: string): string | null {
  if (!u) return null;
  return /^https?:\/\//i.test(u) ? u : null;
}

/** Bytes in words: 812 B, 1.7 KB, 3.4 MB. */
export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/** Where an artifact's files download from: the run the artifact belongs to, which for a reused step is an earlier one. */
export function artifactFileURL(a: Pick<ArtifactView, "run_id" | "id">, path: string): string {
  return `/api/v1/runs/${encodeURIComponent(a.run_id)}/artifacts/${encodeURIComponent(a.id)}/file?path=${encodeURIComponent(path)}`;
}

export function artifactArchiveURL(a: Pick<ArtifactView, "run_id" | "id">): string {
  return `/api/v1/runs/${encodeURIComponent(a.run_id)}/artifacts/${encodeURIComponent(a.id)}/archive`;
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    ta.remove();
  }
}

/** A long value shown short, with a button that copies all of it. */
export function CopyValue({ value, label, keep = 12 }: { value: string; label: string; keep?: number }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await copyText(value);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };
  return (
    <span className="copyval">
      <span className="mono" title={value}>{value.length > keep ? value.slice(0, keep) + "…" : value}</span>
      <button type="button" className="linkbtn copyval-btn" onClick={copy} aria-label={`Copy ${label}`}>{copied ? "Copied" : "Copy"}</button>
    </span>
  );
}

/** How to report findings and an SBOM from a step, for empty states. */
export function FindingsHowTo({ compact }: { compact?: boolean }) {
  return (
    <div className="howto">
      {!compact && <p>Tell a step which files its scanners write, and Launchway reads them when the step ends, whether it passed or failed:</p>}
      <pre className="code">{`steps:
  scan:
    image: alpine:3.22
    run: sh scripts/scan.sh
    reports:
      findings: reports/security/*.sarif
      sbom: reports/sbom.cdx.json`}</pre>
      <p className="sub">Findings are read from SARIF 2.1.0 files, which Semgrep, Trivy, gitleaks, CodeQL and most other scanners write. The SBOM is read from a CycloneDX JSON file, which Launchway also keeps as an artifact to download.</p>
    </div>
  );
}

/** How to keep files from a step, and fetch them in a later one. */
export function ArtifactsHowTo({ compact }: { compact?: boolean }) {
  return (
    <div className="howto">
      {!compact && <p>Name the files a step makes, and Launchway keeps them when the step succeeds. A later step can fetch them:</p>}
      <pre className="code">{`steps:
  build:
    image: golang:1.23
    run: go build -o dist/app ./cmd/app
    artifacts:
      - name: binary
        paths: [dist/app]
        retain: 14d
  smoke-test:
    needs: [build]
    fetch:
      - { from: build, name: binary }
    run: ./dist/app --version`}</pre>
    </div>
  );
}
