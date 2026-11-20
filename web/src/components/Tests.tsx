import { useState, type ReactNode } from "react";
import type { HistoryResult, TestStatus, TestTotals } from "../api";
import { Link } from "./ui";

// Pieces shared by the run's Tests tab, the run list and a project's tests.

const STATUS_LABEL: Record<TestStatus, string> = { passed: "Passed", failed: "Failed", errored: "Error", skipped: "Skipped" };

export function testStatusLabel(s: TestStatus): string { return STATUS_LABEL[s] ?? s; }

/** How long a test took: 4ms, 1.2s, 3m 4s. */
export function testDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`;
  if (ms < 60_000) return `${(Math.round(ms / 100) / 10).toFixed(1)}s`;
  const s = Math.round(ms / 1000);
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** Failed counts errors too: both mean the test did not pass. */
export function failing(t: TestTotals): number { return t.failed + t.errored; }

export function passRate(t: TestTotals): string {
  const ran = t.total - t.skipped;
  if (ran <= 0) return "—";
  const r = (t.passed / ran) * 100;
  if (r === 100 || r === 0) return `${r}%`;
  return `${r >= 99.95 ? 99.9 : r.toFixed(1)}%`;
}

/** An icon for each result, distinct in shape as well as colour. */
export function TestIcon({ status, size = 16 }: { status: TestStatus; size?: number }) {
  const common = { width: size, height: size, viewBox: "0 0 16 16", "aria-hidden": true } as const;
  switch (status) {
    case "passed":
      return <svg {...common} className="ic ic-ok"><circle cx="8" cy="8" r="7" /><path d="M4.8 8.2l2.1 2.1 4.3-4.4" fill="none" /></svg>;
    case "failed":
      return <svg {...common} className="ic ic-bad"><circle cx="8" cy="8" r="7" /><path d="M5.5 5.5l5 5M10.5 5.5l-5 5" fill="none" /></svg>;
    case "errored":
      return <svg {...common} className="ic ic-bad"><path d="M8 1.2l7 12.6H1z" strokeLinejoin="round" /><path d="M8 6v3.6M8 11.6v.2" fill="none" /></svg>;
    default:
      return <svg {...common} className="ic ic-idle"><circle cx="8" cy="8" r="6.5" fill="none" /><path d="M5 11L11 5" fill="none" /></svg>;
  }
}

/** A bar split in proportion to the results. Text says the same. */
export function TestBar({ t, thin }: { t: TestTotals; thin?: boolean }) {
  const parts: [string, number, string][] = [
    ["ok", t.passed, "passed"], ["bad", t.failed, "failed"], ["err", t.errored, "errors"], ["idle", t.skipped, "skipped"],
  ];
  const total = Math.max(t.total, 1);
  return (
    <div className={"tbar" + (thin ? " tbar-thin" : "")} role="img"
      aria-label={parts.filter(([, n]) => n > 0).map(([, n, l]) => `${n} ${l}`).join(", ") || "no tests"}>
      {parts.map(([k, n]) => n > 0 && <span key={k} className={"tbar-" + k} style={{ flexGrow: n / total }} />)}
    </div>
  );
}

/** Compact counts for a run in a list: failures first, then passes. */
export function TestCounts({ t }: { t: TestTotals }) {
  const bad = failing(t);
  return (
    <span className="tcounts" title={`Tests: ${t.passed} passed, ${t.failed} failed, ${t.errored} errors, ${t.skipped} skipped`}>
      {bad > 0 && <span className="tcount tcount-bad"><TestIcon status="failed" size={13} />{bad}<span className="visually-hidden"> failed</span></span>}
      <span className="tcount tcount-ok"><TestIcon status="passed" size={13} />{t.passed}<span className="visually-hidden"> passed</span></span>
      {t.skipped > 0 && <span className="tcount tcount-idle"><TestIcon status="skipped" size={13} />{t.skipped}<span className="visually-hidden"> skipped</span></span>}
    </span>
  );
}

export function Badge({ kind, children, title }: { kind: "flaky" | "new" | "info"; children: ReactNode; title?: string }) {
  return <span className={"tbadge tbadge-" + kind} title={title}>{children}</span>;
}

/** A block of monospace text with a button that copies it. */
export function CopyBlock({ label, text, tone }: { label: string; text: string; tone?: "bad" }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
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
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };
  return (
    <figure className="copyblock">
      <figcaption>
        <span>{label}</span>
        <button type="button" className="btn btn-sm copybtn" onClick={copy} aria-label={`Copy ${label.toLowerCase()}`}>
          {copied ? "Copied" : "Copy"}
        </button>
      </figcaption>
      <pre className={tone === "bad" ? "copyblock-bad" : undefined}>{text}</pre>
    </figure>
  );
}

/** A test's last results, oldest on the left, each a link to its run. */
export function HistoryStrip({ results, current }: { results: HistoryResult[]; current?: string }) {
  const list = [...results].reverse();
  return (
    <ol className="hstrip" aria-label="Results, oldest first">
      {list.map((r) => (
        <li key={r.run_id + r.step}>
          <Link href={`/runs/${r.run_id}?tab=tests`} className={"hcell hcell-" + r.status + (r.flaky ? " hcell-flaky" : "") + (r.run_id === current ? " hcell-current" : "")}>
            <TestIcon status={r.status} size={12} />
            <span className="visually-hidden">#{r.number} {testStatusLabel(r.status)}{r.flaky ? ", flaky" : ""}</span>
            <span className="hcell-tip" aria-hidden="true">#{r.number} · {testStatusLabel(r.status)} · {testDuration(r.duration_ms)}{r.flaky ? " · passed on retry" : ""}</span>
          </Link>
        </li>
      ))}
    </ol>
  );
}

/** How to add test reports to a step, for empty states. */
export function ReportsHowTo({ compact }: { compact?: boolean }) {
  return (
    <div className="howto">
      {!compact && <p>Tell a step which files its tests write, and Launchway reads them when the step ends, whether it passed or failed:</p>}
      <pre className="code">{`steps:
  test:
    image: python:3.12
    run: pytest --junitxml=reports/unit.xml
    reports:
      tests: reports/*.xml`}</pre>
      <p className="sub">The format is worked out from each file. Launchway reads {READ_FORMATS}.</p>
      {!compact && (
        <>
          <p className="sub">Screenshots, videos and traces the tests attach are shown with each result when the step names the directory they are written to:</p>
          <pre className="code">{`    reports:
      tests:
        paths: test-results/results.json
        attachments: test-results`}</pre>
        </>
      )}
    </div>
  );
}

const READ_FORMATS = "JUnit XML (pytest, Maven Surefire and Failsafe, Gradle, Jest, Vitest, PHPUnit, RSpec, Mocha, Playwright, Cypress), " +
  "go test -json, Cucumber JSON (Karate, cucumber-js, Cucumber-JVM), Karate JSON, Playwright JSON, mochawesome (Cypress), CTRF, " +
  "TestNG, NUnit 3, xUnit.net v2, .NET TRX, TAP and Robot Framework output.xml";

const FORMAT_LABEL: Record<string, string> = {
  "junit": "JUnit XML", "go-test-json": "go test -json", "cucumber-json": "Cucumber JSON", "karate-json": "Karate JSON",
  "playwright-json": "Playwright JSON", "mochawesome": "mochawesome", "ctrf": "CTRF", "testng": "TestNG XML",
  "nunit3": "NUnit 3 XML", "xunit2": "xUnit.net v2 XML", "trx": ".NET TRX", "tap": "TAP", "robot": "Robot Framework XML",
};

/** The name of a test report format, as people know it. */
export function formatLabel(f?: string): string {
  if (!f) return "JUnit XML";
  return FORMAT_LABEL[f] ?? f;
}

/** Tags of a test, as small pills. */
export function TagPills({ tags, max = 4 }: { tags?: string[]; max?: number }) {
  if (!tags?.length) return null;
  const shown = tags.slice(0, max);
  return (
    <span className="ttags">
      <span className="visually-hidden">Tags: </span>
      {shown.map((t) => <span key={t} className="ttag">{t.replace(/^@/, "")}</span>)}
      {tags.length > max && <span className="ttag ttag-more" title={tags.slice(max).join(", ")}>+{tags.length - max}</span>}
    </span>
  );
}

const BROWSERS = ["chromium", "chrome", "firefox", "webkit", "safari", "edge", "msedge", "mobile", "android", "ios"];

/** Whether a variant names a browser or device. */
export function isBrowser(v: string): boolean {
  const l = v.toLowerCase();
  return BROWSERS.some((b) => l.includes(b));
}

/** What a test ran against: a browser gets a window icon, anything else a layers icon. */
export function VariantPill({ variant }: { variant?: string }) {
  if (!variant) return null;
  return (
    <span className="tvariant" title={isBrowser(variant) ? `Browser: ${variant}` : `Variant: ${variant}`}>
      {isBrowser(variant) ? <BrowserIcon /> : <LayersIcon />}
      <span className="visually-hidden">{isBrowser(variant) ? "Browser " : "Variant "}</span>
      {variant}
    </span>
  );
}

export function BrowserIcon({ size = 13 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <rect x="1.5" y="2.5" width="13" height="11" rx="2" /><path d="M1.5 5.5h13" /><circle cx="3.6" cy="4" r=".4" /><circle cx="5.2" cy="4" r=".4" />
    </svg>
  );
}

export function LayersIcon({ size = 13 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <path d="M8 2l6 3-6 3-6-3z" strokeLinejoin="round" /><path d="M2 8.2l6 3 6-3M2 11.2l6 3 6-3" strokeLinejoin="round" />
    </svg>
  );
}

/** A line of values as a small chart, oldest on the left; the last point is marked. */
export function Sparkline({ values, width = 160, height = 36, label, format }: {
  values: number[]; width?: number; height?: number; label: string; format: (v: number) => string;
}) {
  if (values.length < 2) return null;
  const max = Math.max(...values, 1);
  const min = 0;
  const pad = 3;
  const x = (i: number) => pad + (i * (width - pad * 2)) / (values.length - 1);
  const y = (v: number) => height - pad - ((v - min) / (max - min || 1)) * (height - pad * 2);
  const d = values.map((v, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join("");
  const area = `${d}L${x(values.length - 1).toFixed(1)},${height - pad}L${x(0).toFixed(1)},${height - pad}Z`;
  const last = values[values.length - 1];
  const avg = values.reduce((a, b) => a + b, 0) / values.length;
  return (
    <span className="spark">
      <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img"
        aria-label={`${label}: the last ${values.length}, oldest first, from ${format(values[0])} to ${format(last)}; at most ${format(max)}, ${format(avg)} on average`}>
        <path d={area} className="spark-area" />
        <path d={d} className="spark-line" />
        <circle cx={x(values.length - 1)} cy={y(last)} r="3" className="spark-dot" />
      </svg>
      <span className="spark-text"><strong>{format(last)}</strong> <span className="faint">last · {format(max)} max</span></span>
    </span>
  );
}
