import { Fragment, useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import {
  ApiError, verifyApi, type ClusterVerdict, type GraphVerify, type IntervalCheck, type LogVerdict, type MetricVerdict, type RunDetail,
  type StepView, type VerificationView, type VerifyControls, type VerifyExplanation, type VerifyInterval, type VerifyOutcome, type VerifyStats,
} from "../api";
import { clock, duration } from "../format";
import { useNow } from "../live";
import { ErrorBox, Link, Mono } from "./ui";

// A verify step's verification: how it stands, why, what it read, and the
// way for a person to mark it passed or failed. The verdict comes from the
// statistics on the server; a model's explanation, when there is one, is
// shown as what it is: text a model wrote, which may be wrong.

/** How many standard deviations from the reference a reading may be, by sensitivity (as the server's Thresholds). */
export function sensitivityK(s: string): number {
  return s === "high" ? 2 : s === "low" ? 4.5 : 3;
}

const SENSITIVITY_TEXT: Record<string, string> = {
  high: "high: a reading 2 standard deviations from the reference fails",
  medium: "medium: a reading 3 standard deviations from the reference fails",
  low: "low: a reading 4.5 standard deviations from the reference fails",
};

/** A step that is still being verified, or may be. */
export function verifyActive(status: string): boolean {
  return status === "queued" || status === "leased" || status === "running" || status === "waiting";
}

/** A number as a person reads it: three significant digits, no exponent for everyday sizes. */
export function num(v: number | undefined): string {
  if (v === undefined || Number.isNaN(v)) return "?";
  if (v === 0) return "0";
  const a = Math.abs(v);
  if (a >= 1000) return Math.round(v).toLocaleString();
  if (a >= 100) return v.toFixed(0);
  if (a < 0.0001) return v.toExponential(1);
  return String(Number(v.toPrecision(3)));
}

/** The pill for how a verification stands, in words and a tone. */
export function verifyState(v: { status?: string; outcome?: VerifyOutcome; override?: string; start?: number }, now: number): { label: string; tone: string } {
  if (v.override === "passed") return { label: "Marked passed", tone: "ok" };
  if (v.override === "failed") return { label: "Marked failed", tone: "bad" };
  if (v.status === "waiting") return { label: "Waiting for a person", tone: "wait" };
  switch (v.outcome) {
    case "passed": return { label: "Passed", tone: "ok" };
    case "failed": return { label: "Failed", tone: "bad" };
    case "inconclusive": return { label: "Inconclusive", tone: "warn" };
  }
  if (v.status === "queued") return { label: "Waiting for a deploy runner", tone: "idle" };
  if (v.status === "canceled") return { label: "Canceled", tone: "idle" };
  if (v.start && v.start > now) return { label: "Starts soon", tone: "run" };
  return { label: "Watching", tone: "run" };
}

export function VerifyPill({ v, now }: { v: Parameters<typeof verifyState>[0]; now: number }) {
  const s = verifyState(v, now);
  return <span className={`dpill dpill-${s.tone}`}><OutcomeMark tone={s.tone} />{s.label}</span>;
}

/** A small shape for an outcome, so it is never told by colour alone. */
function OutcomeMark({ tone }: { tone: string }) {
  const p = { width: 10, height: 10, viewBox: "0 0 10 10", "aria-hidden": true, className: "vmark" } as const;
  switch (tone) {
    case "ok": return <svg {...p}><path d="M1.5 5.2l2.3 2.3 4.7-4.9" /></svg>;
    case "bad": return <svg {...p}><path d="M2 2l6 6M8 2l-6 6" /></svg>;
    case "warn": case "wait": return <svg {...p}><path d="M5 1.5v4.2M5 8.2v.3" /></svg>;
    case "run": return <svg {...p}><circle cx="5" cy="5" r="3" /></svg>;
    default: return <svg {...p}><path d="M2 5h6" /></svg>;
  }
}

/** The run's verifications, kept up to date while any is going on. */
export function useRunVerifications(run: RunDetail): { list: VerificationView[] | null; error: unknown; reload: () => void } {
  const [list, setList] = useState<VerificationView[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const steps = run.step_list.filter((s) => s.kind === "verify");
  const active = steps.some((s) => verifyActive(s.status));
  // A step's status or attempt changing (a live update) asks again at once.
  const version = steps.map((s) => s.id + s.status + s.attempt).join();
  const alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const load = useCallback(() => {
    verifyApi.runVerifications(run.id).then((l) => { if (alive.current) { setList(l); setError(null); } })
      .catch((e) => alive.current && setError(e));
  }, [run.id]);
  useEffect(() => { setList(null); }, [run.id]);
  useEffect(() => {
    if (steps.length === 0) return;
    load();
    if (!active) return;
    // Intervals are reported by a deploy runner, not streamed: ask every few seconds.
    const t = window.setInterval(load, 3000);
    return () => window.clearInterval(t);
  }, [load, version, active, steps.length]);
  return { list, error, reload: load };
}

/** What the pipeline says about a verify step. */
function graphVerify(run: RunDetail, step: string): GraphVerify | undefined {
  return run.graph.steps.find((s) => s.id === step)?.verify;
}

// VerificationPanel is shown with a verify step.
export function VerificationPanel({ run, step, list, reload }: { run: RunDetail; step: StepView; list: VerificationView[] | null; reload: () => void }) {
  const v = list?.find((x) => x.step === step.id);
  const now = useNow(1000, verifyActive(step.status));
  if (!v) return <NoVerification run={run} step={step} loading={list === null} />;
  return <Verification run={run} step={step} v={v} now={now} reload={reload} />;
}

function NoVerification({ run, step, loading }: { run: RunDetail; step: StepView; loading: boolean }) {
  const g = graphVerify(run, step.id);
  const env = step.environment || run.step_list.find((s) => s.id === step.deployment)?.environment || "its environment";
  let body;
  if (loading && step.attempt > 0) body = <div className="loading">Loading…</div>;
  else if (step.status === "pending") {
    body = <p className="sub">Watches {env} {g ? `for ${g.duration}, in intervals of ${g.interval},` : ""} once {step.needs?.length ? step.needs.join(", ") : "the run"} {step.needs && step.needs.length > 1 ? "have" : "has"} succeeded.</p>;
  } else if (step.status === "skipped") body = <p className="sub">{step.error || "Skipped"}. Nothing was watched.</p>;
  else if (step.status === "canceled") body = <p className="sub">Canceled before the verification started.</p>;
  else if (step.status === "failed") body = <p className="sub">It failed before the verification started{step.error ? `: ${step.error}` : "."}</p>;
  else body = <p className="sub">This server has no verification recorded for this step.</p>;
  return (
    <section className="card dep" aria-label="Verification">
      <div className="dep-head"><h3 className="h-sm">Verifying {env}</h3></div>
      {body}
    </section>
  );
}

function Verification({ run, step, v, now, reload }: { run: RunDetail; step: StepView; v: VerificationView; now: number; reload: () => void }) {
  const g = graphVerify(run, step.id);
  const vd = v.verdict;
  const st = verifyState({ status: v.status, outcome: vd.outcome, override: v.override, start: v.start }, now);
  const k = sensitivityK(v.sensitivity);
  return (
    <section className="card dep verify" aria-label={`Verification of ${v.service} in ${v.environment}`}>
      <div className="dep-head">
        <h3 className="h-sm">Verifying {v.service} in {v.environment}</h3>
        <div className="dep-pills"><VerifyPill v={{ status: v.status, outcome: vd.outcome, override: v.override, start: v.start }} now={now} /></div>
      </div>

      <div className="vfacts">
        <span><span className="faint">version</span> <Mono>{v.version || "?"}</Mono></span>
        <span><span className="faint">on target</span> <Mono>{v.target}</Mono></span>
        <span title={SENSITIVITY_TEXT[v.sensitivity] ?? v.sensitivity}><span className="faint">sensitivity</span> {v.sensitivity} <span className="faint">({k} standard deviations)</span></span>
        {v.deployment && <span><span className="faint">watches the deployment of</span> {step.deployment || v.deployment}</span>}
      </div>
      <Progress v={v} now={now} />
      <BaselineLine run={run} v={v} g={g} />

      {v.status === "waiting" && !v.override && (
        <div className="callout callout-wait vwait" role="status">
          <span>
            <strong>Waiting for a person.</strong> The verification was inconclusive, and this step is set to pause
            {" "}(<Mono>on_inconclusive: pause</Mono>): someone must mark it passed or failed below. The run waits until then
            {v.deadline ? <>; if nobody decides by {clock(v.deadline)} ({v.deadline > now ? `in ${duration(v.deadline - now)}` : "now"}), it fails</> : null}.
          </span>
        </div>
      )}

      <VerdictBox v={v} st={st} />
      {v.override && (
        <div className={`callout ${v.override === "passed" ? "callout-ok" : "callout-bad"} vwhy`}>
          <strong>Marked {v.override} by {v.override_by}.</strong>
          {v.override_comment && <blockquote className="approval-comment">{v.override_comment}</blockquote>}
        </div>
      )}
      {v.explanation && <ModelExplanation x={v.explanation} />}
      {!v.explanation && v.model && v.decided && (
        <p className="sub small">No explanation from model {v.model} was recorded.</p>
      )}

      <IntervalStrip v={v} now={now} />

      {(vd.metrics?.length ?? 0) > 0 && (
        <div className="vsection">
          <h4 className="h-xs">Metrics</h4>
          {vd.metrics!.map((m) => <MetricBlock key={m.name} v={v} m={m} g={g} />)}
        </div>
      )}
      {(vd.logs?.length ?? 0) > 0 && (
        <div className="vsection">
          <h4 className="h-xs">Logs</h4>
          {vd.logs!.map((l) => <LogBlock key={l.name} l={l} v={v} />)}
        </div>
      )}

      {verifyActive(step.status) && <VerifyForm run={run} step={step} v={v} onDone={reload} />}
      <WhatWasRead v={v} />
    </section>
  );
}

// Progress says where the window stands: which interval, and when the next is due.
function Progress({ v, now }: { v: VerificationView; now: number }) {
  const n = v.intervals.length;
  const ims = v.interval_ms;
  const window = `${v.total} interval${v.total === 1 ? "" : "s"} of ${duration(ims)}`;
  let text: string;
  if (v.decided || v.override) {
    text = v.verdict.intervals >= v.total ? `All ${v.total} intervals judged (${window}).` : `Decided after ${v.verdict.intervals} of ${v.total} intervals (${window}).`;
  } else if (v.status === "queued") {
    text = `${window}, waiting for a deploy runner serving target ${v.target} to take it.`;
  } else if (v.start > now) {
    text = `${window}; the first starts in ${duration(v.start - now)}.`;
  } else if (n >= v.total) {
    text = `All ${v.total} intervals read; waiting for the decision.`;
  } else {
    const end = v.start + (n + 1) * ims;
    text = end > now ? `Interval ${n + 1} of ${v.total}, next in ${Math.ceil((end - now) / 1000)}s.`
      : `Interval ${n + 1} of ${v.total} is over; waiting for the deploy runner to report it (${duration(now - end)}).`;
  }
  return <p className="vprogress">{text}</p>;
}

function BaselineLine({ run, v, g }: { run: RunDetail; v: VerificationView; g?: GraphVerify }) {
  const runs = v.baseline ?? [];
  if (runs.length > 0) {
    return (
      <p className="vbaseline">
        Compared with {runs.length === 1 ? "run" : "runs"}{" "}
        {runs.map((b, i) => (
          <span key={b.run + b.step}>{i > 0 && (i === runs.length - 1 ? " and " : ", ")}
            <Link href={`/runs/${b.run}?step=${encodeURIComponent(b.step)}`}>#{b.number}</Link>
          </span>
        ))}
        <span className="faint">: earlier verifications of {v.service} in {v.environment} that passed.</span>
      </p>
    );
  }
  if (g && g.baseline === 0) return <p className="vbaseline faint">Not compared with earlier verifications (<Mono>baseline: 0</Mono>).</p>;
  return (
    <p className="vbaseline faint">
      No earlier passed verification of {v.service} in {v.environment} to compare with{run.number > 1 ? " yet" : ""}: judged
      {" "}{v.queries.some((q) => q.stable_query) ? "against the stable version and the fixed bounds" : "against the fixed bounds"} only.
    </p>
  );
}

// VerdictBox states the verdict in words, prominently.
function VerdictBox({ v, st }: { v: VerificationView; st: { label: string; tone: string } }) {
  const vd = v.verdict;
  // Once a person decided, the box shows what the statistics said, in their tone.
  if (v.override) st = { label: "", tone: vd.outcome === "passed" ? "ok" : vd.outcome === "failed" ? "bad" : vd.outcome === "inconclusive" ? "warn" : "run" };
  const tone = st.tone === "ok" ? "ok" : st.tone === "bad" ? "bad" : st.tone === "warn" || st.tone === "wait" ? "draft" : "info";
  const lead = v.override ? "The statistics said" : vd.outcome === "running" ? "So far" : vd.final ? "Verdict" : "So far";
  return (
    <div className={`callout callout-${tone} vverdict`} data-outcome={vd.outcome}>
      <span className="vverdict-lead">{lead}{v.override ? ` (${vd.outcome})` : ""}</span>
      <p className="vverdict-text">{vd.summary || (vd.outcome === "running" ? "Nothing read yet." : "No summary.")}</p>
      {v.on_fail !== "fail" && vd.outcome === "failed" && !v.override && (
        <p className="vverdict-note">With <Mono>on_fail: {v.on_fail}</Mono>, a failure also aborts the deployment's rollout.</p>
      )}
      {vd.outcome === "inconclusive" && v.on_inconclusive !== "pause" && !v.override && (
        <p className="vverdict-note">With <Mono>on_inconclusive: {v.on_inconclusive}</Mono>, an inconclusive verification {v.on_inconclusive === "pass" ? "lets the run go on" : "fails the step"}.</p>
      )}
    </div>
  );
}

/** Tokens and cost in a few words. */
export function usageText(u?: { input_tokens: number; output_tokens: number; cost_usd?: number; duration_ms?: number; calls?: number }): string {
  if (!u) return "";
  const parts = [`${u.input_tokens.toLocaleString()} tokens in, ${u.output_tokens.toLocaleString()} out`];
  if (u.cost_usd) parts.push(`about ${costText(u.cost_usd)}`);
  if (u.duration_ms) parts.push(duration(u.duration_ms));
  if (u.calls && u.calls > 1) parts.push(`${u.calls} calls`);
  return parts.join(" · ");
}

export function costText(usd: number): string {
  if (usd === 0) return "$0";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
}

// ModelExplanation shows what a model said about the verdict, labelled as
// model output. Everything in it is shown as text.
function ModelExplanation({ x }: { x: VerifyExplanation }) {
  return (
    <aside className="vexpl" aria-label={`Model output from ${x.model}`}>
      <div className="vexpl-head">
        <span className="vexpl-badge">Model output</span>
        <span>Suggested by model <strong>{x.model}</strong>, which may be wrong</span>
      </div>
      {x.error ? (
        <p className="vexpl-err">No explanation: {x.error}</p>
      ) : (
        <>
          {x.changed && (
            <p className="vexpl-changed">
              <strong>The model changed the outcome</strong> to {x.outcome}: the statistics passed it, and this step lets the model turn a pass
              into {x.outcome === "failed" ? "a failure" : "inconclusive"} (<Mono>decide: model-assisted</Mono>).
            </p>
          )}
          {x.summary && <p className="vexpl-text">{x.summary}</p>}
          <dl className="kv vexpl-kv">
            {x.cause && <><dt>Likely cause</dt><dd className="vexpl-text">{x.cause}</dd></>}
            {x.suggestion && <><dt>What to try</dt><dd className="vexpl-text">{x.suggestion}</dd></>}
            {x.outcome && !x.changed && <><dt>Its view</dt><dd>{x.outcome}</dd></>}
          </dl>
        </>
      )}
      {x.usage && <div className="vexpl-usage faint">{usageText(x.usage)}</div>}
    </aside>
  );
}

// ---- intervals ----

type CellState = "passed" | "failed" | "no_data" | "pending" | "now" | "unneeded";

const CELL_TEXT: Record<CellState, string> = {
  passed: "passed", failed: "failed", no_data: "no data", pending: "not read yet", now: "being read now", unneeded: "not read: decided before it",
};

/** How one interval went, from the metrics' checks. */
function cellOf(v: VerificationView, index: number, now: number): { state: CellState; checks: { name: string; c: IntervalCheck; log?: boolean }[] } {
  const iv = v.intervals.find((x) => x.index === index);
  const checks: { name: string; c: IntervalCheck; log?: boolean }[] = [
    ...(v.verdict.metrics ?? []).flatMap((m) => (m.intervals ?? []).filter((c) => c.index === index).map((c) => ({ name: m.name, c }))),
    ...(v.verdict.logs ?? []).flatMap((l) => (l.intervals ?? []).filter((c) => c.index === index).map((c) => ({ name: l.name, c, log: true }))),
  ];
  if (!iv) {
    const s = v.start + index * v.interval_ms;
    const e = s + v.interval_ms;
    const live = !v.decided && !v.override && (v.status === "running" || v.status === "leased");
    if (v.decided || v.override || !verifyActive(v.status)) return { state: "unneeded", checks };
    return { state: live && s <= now && now < e + 15_000 && index === v.intervals.length ? "now" : "pending", checks };
  }
  if (checks.some((x) => x.c.outcome === "failed")) return { state: "failed", checks };
  const logsErr = (iv.logs ?? []).length > 0 && (iv.logs ?? []).every((l) => l.error);
  if (checks.length > 0 ? checks.every((x) => x.c.outcome === "no_data") : logsErr) return { state: "no_data", checks };
  return { state: "passed", checks };
}

function CellIcon({ state }: { state: CellState }) {
  const p = { width: 14, height: 14, viewBox: "0 0 14 14", "aria-hidden": true } as const;
  switch (state) {
    case "passed": return <svg {...p}><path d="M3 7.3l2.6 2.6 5.4-5.6" /></svg>;
    case "failed": return <svg {...p}><path d="M3.5 3.5l7 7M10.5 3.5l-7 7" /></svg>;
    case "no_data": return <svg {...p}><path d="M3.5 7h7" /></svg>;
    case "now": return <svg {...p}><circle cx="7" cy="7" r="2.6" /></svg>;
    default: return null;
  }
}

function cellTitle(v: VerificationView, index: number, cell: ReturnType<typeof cellOf>): string {
  const s = v.start + index * v.interval_ms;
  const head = `Interval ${index + 1} of ${v.total}, ${clock(s)} to ${new Date(s + v.interval_ms).toLocaleTimeString()}: ${CELL_TEXT[cell.state]}`;
  const lines = cell.checks.map(({ name, c, log }) =>
    `${name}: ${c.outcome === "no_data" ? "no data" : log ? `${c.value} log line${c.value === 1 ? "" : "s"}` : num(c.value)}${c.reason ? ` (${c.reason})` : ""}`);
  return [head, ...lines].join("\n");
}

function IntervalStrip({ v, now }: { v: VerificationView; now: number }) {
  const cells = Array.from({ length: v.total }, (_, i) => ({ i, cell: cellOf(v, i, now) }));
  const count = (s: CellState) => cells.filter((c) => c.cell.state === s).length;
  return (
    <div className="vsection">
      <h4 className="h-xs">Intervals</h4>
      <ol className="vtl" aria-label={`${v.total} intervals: ${count("passed")} passed, ${count("failed")} failed, ${count("no_data")} without data, ${count("pending") + count("now")} not read yet${count("unneeded") ? `, ${count("unneeded")} not needed` : ""}`}>
        {cells.map(({ i, cell }) => (
          <li key={i} className={`vcell vcell-${cell.state}`} title={cellTitle(v, i, cell)} tabIndex={0}
            aria-label={cellTitle(v, i, cell).replace(/\n/g, "; ")}>
            <CellIcon state={cell.state} />
          </li>
        ))}
      </ol>
      <ul className="chart-legend vtl-legend" aria-hidden="true">
        <li><span className="vcell vcell-sm vcell-passed"><CellIcon state="passed" /></span>passed</li>
        <li><span className="vcell vcell-sm vcell-failed"><CellIcon state="failed" /></span>failed</li>
        <li><span className="vcell vcell-sm vcell-no_data"><CellIcon state="no_data" /></span>no data</li>
        {cells.some((c) => c.cell.state === "unneeded")
          ? <li><span className="vcell vcell-sm vcell-unneeded" />not needed: decided early</li>
          : <li><span className="vcell vcell-sm vcell-pending" />not read yet</li>}
      </ul>
    </div>
  );
}

// ---- metrics ----

function outcomeTone(o: string): string {
  return o === "passed" ? "ok" : o === "failed" ? "bad" : o === "inconclusive" ? "warn" : o === "running" ? "run" : "idle";
}

function OutcomePill({ o, decided }: { o: string; decided?: boolean }) {
  // A check still open when another one decided the verification was never finished.
  const label = o === "running" ? (decided ? "not decided: stopped early" : "so far fine") : o === "unexpected_frequency" ? "more often" : o.replace(/_/g, " ");
  const tone = o === "running" && decided ? "idle" : outcomeTone(o);
  return <span className={`dpill dpill-${tone}`}><OutcomeMark tone={tone} />{label}</span>;
}

function MetricBlock({ v, m, g }: { v: VerificationView; m: MetricVerdict; g?: GraphVerify }) {
  const def = g?.metrics?.find((x) => x.name === m.name);
  const baseline = m.baseline?.n ? m.baseline : v.baseline_metrics?.[m.name];
  const checks = m.intervals ?? [];
  return (
    <div className="vmetric">
      <div className="vmetric-head">
        <strong className="mono">{m.name}</strong>
        <OutcomePill o={m.outcome} decided={v.decided || !!v.override} />
        {def?.direction && def.direction !== "higher-is-worse" && <span className="faint small">{def.direction === "either" ? "either way is worse" : "lower is worse"}</span>}
      </div>
      {m.reason && <p className="vmetric-reason">{m.reason}</p>}
      <MetricChart v={v} m={m} baseline={baseline} max={def?.max} min={def?.min} />
      {checks.length > 0 && (
        <details className="vreadings">
          <summary>Readings by interval</summary>
          <div className="table-scroll">
            <table className="inputs-table vreadings-table">
              <thead><tr><th>Interval</th><th>Mean</th><th>Versus stable</th><th>Versus baseline</th><th>Result</th></tr></thead>
              <tbody>
                {checks.map((c) => (
                  <tr key={c.index} className={c.outcome === "failed" ? "vrow-bad" : undefined}>
                    <td data-label="Interval">{c.index + 1}</td>
                    <td data-label="Mean" className="mono">{c.outcome === "no_data" ? "–" : num(c.value)}</td>
                    <td data-label="Versus stable">{c.stable_z !== undefined ? zText(c.stable_z) : "–"}</td>
                    <td data-label="Versus baseline">{c.baseline_z !== undefined ? zText(c.baseline_z) : "–"}</td>
                    <td data-label="Result">{c.outcome === "no_data" ? "no data" : c.outcome}{c.reason ? <span className="faint">: {c.reason}</span> : null}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </details>
      )}
    </div>
  );
}

function zText(z: number): string {
  if (!Number.isFinite(z)) return z > 0 ? "far above" : "far below";
  const a = Math.abs(z);
  return `${a.toFixed(1)} sd ${z >= 0 ? "above" : "below"}`;
}

/** Follows an element's width, for charts drawn at their real size. */
function useWidth<T extends HTMLElement>(): [React.RefObject<T | null>, number] {
  const ref = useRef<T | null>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

function niceTicks(lo: number, hi: number, n = 4): number[] {
  const span = hi - lo || 1;
  const raw = span / n;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((f) => f * mag).find((s) => s >= raw) ?? raw;
  const out: number[] = [];
  for (let t = Math.ceil(lo / step) * step; t <= hi + step * 1e-9; t += step) out.push(Number(t.toPrecision(6)));
  return out;
}

// MetricChart draws one metric over the window: the new version's readings
// as a line, the stable version's as a dashed line, the baseline as a band,
// the fixed bounds as lines, and failed intervals shaded.
function MetricChart({ v, m, baseline, max, min }: { v: VerificationView; m: MetricVerdict; baseline?: VerifyStats; max?: number; min?: number }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const id = useId();
  const series = (side: "canary" | "stable") => v.intervals.flatMap((iv: VerifyInterval) =>
    (iv.metrics ?? []).filter((x) => x.name === m.name).flatMap((x) => (x[side] ?? []).map((p) => ({ ...p, index: iv.index }))))
    .sort((a, b) => a.t - b.t);
  const canary = series("canary");
  const stable = series("stable");
  const checks = m.intervals ?? [];
  const failed = new Set(checks.filter((c) => c.outcome === "failed").map((c) => c.index));
  const noData = new Set(checks.filter((c) => c.outcome === "no_data").map((c) => c.index));
  const k = sensitivityK(v.sensitivity);
  const band = baseline && baseline.n > 1 ? { lo: baseline.mean - k * baseline.sd, hi: baseline.mean + k * baseline.sd, mean: baseline.mean } : undefined;

  const values = [...canary.map((p) => p.v), ...stable.map((p) => p.v)];
  const dataMax = values.length ? Math.max(...values) : 0;
  const dataMin = values.length ? Math.min(...values) : 0;
  let hi = Math.max(dataMax, band?.hi ?? -Infinity);
  let lo = Math.min(dataMin, band ? Math.max(band.lo, 0) : Infinity);
  if (!Number.isFinite(hi)) hi = 1;
  if (!Number.isFinite(lo)) lo = 0;
  // A bound far beyond the readings would flatten them: it is named, not drawn.
  const maxShown = max !== undefined && (max <= hi * 1.6 || values.length === 0);
  const minShown = min !== undefined && (min >= lo - (hi - lo) * 0.6 || values.length === 0);
  if (maxShown) hi = Math.max(hi, max!);
  if (minShown) lo = Math.min(lo, min!);
  if (lo >= 0 && dataMin >= 0) lo = 0;
  if (hi === lo) hi = lo + 1;
  hi += (hi - lo) * 0.08;

  const W = Math.max(width, 280);
  const H = 180;
  const pad = { l: 48, r: 10, t: 12, b: 24 };
  const x0 = v.start;
  const x1 = v.start + v.total * v.interval_ms;
  const X = (t: number) => pad.l + ((t - x0) / (x1 - x0 || 1)) * (W - pad.l - pad.r);
  const Y = (y: number) => pad.t + (1 - (y - lo) / (hi - lo)) * (H - pad.t - pad.b);
  const ticks = niceTicks(lo, hi);
  const line = (ps: { t: number; v: number }[]) => ps.map((p, i) => `${i ? "L" : "M"}${X(p.t).toFixed(1)},${Y(p.v).toFixed(1)}`).join(" ");
  const ivW = (W - pad.l - pad.r) / v.total;
  const labelEvery = Math.max(1, Math.ceil(v.total / Math.max(1, Math.floor((W - pad.l - pad.r) / 28))));

  const failedList = [...failed].sort((a, b) => a - b).map((i) => i + 1);
  const desc = `${m.name} over ${v.total} intervals. ${canary.length ? `The new version read between ${num(dataMin)} and ${num(Math.max(...canary.map((p) => p.v)))}.` : "No readings of the new version."}`
    + (stable.length ? ` The stable version averaged ${num(m.stable.mean)}.` : "")
    + (band ? ` The baseline is ${num(band.mean)}, allowed ${num(Math.max(band.lo, 0))} to ${num(band.hi)}.` : "")
    + (max !== undefined ? ` Upper bound ${num(max)}.` : "") + (min !== undefined ? ` Lower bound ${num(min)}.` : "")
    + (failedList.length ? ` Failed in interval${failedList.length > 1 ? "s" : ""} ${failedList.join(", ")}.` : "");

  return (
    <div className="vchart" ref={ref}>
      {width > 0 && (
        <svg width={W} height={H} role="img" aria-labelledby={`${id}-t ${id}-d`}>
          <title id={`${id}-t`}>{m.name}</title>
          <desc id={`${id}-d`}>{desc}</desc>
          <defs>
            <pattern id={`${id}-hatch`} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
              <line x1="0" y1="0" x2="0" y2="6" className="vc-hatch" />
            </pattern>
          </defs>
          {Array.from({ length: v.total }, (_, i) => i).map((i) => (failed.has(i) || noData.has(i)) && (
            <g key={"iv" + i}>
              <rect x={pad.l + i * ivW} y={pad.t} width={ivW} height={H - pad.t - pad.b} className={failed.has(i) ? "vc-failed" : "vc-nodata"} />
              <rect x={pad.l + i * ivW} y={pad.t} width={ivW} height={H - pad.t - pad.b} fill={`url(#${id}-hatch)`} className={failed.has(i) ? "vc-failed-hatch" : "vc-nodata-hatch"} />
            </g>
          ))}
          {ticks.map((t) => (
            <g key={"y" + t}>
              <line x1={pad.l} x2={W - pad.r} y1={Y(t)} y2={Y(t)} className="chart-grid" />
              <text x={pad.l - 6} y={Y(t) + 4} textAnchor="end" className="chart-tick">{num(t)}</text>
            </g>
          ))}
          {Array.from({ length: v.total + 1 }, (_, i) => (
            <line key={"x" + i} x1={pad.l + i * ivW} x2={pad.l + i * ivW} y1={pad.t} y2={H - pad.b} className="vc-ivline" />
          ))}
          {Array.from({ length: v.total }, (_, i) => i).filter((i) => i % labelEvery === 0).map((i) => (
            <text key={"xl" + i} x={pad.l + (i + 0.5) * ivW} y={H - 7} textAnchor="middle" className="chart-tick">{i + 1}</text>
          ))}
          {band && (
            <g>
              <rect x={pad.l} width={W - pad.l - pad.r} y={Y(Math.min(band.hi, hi))} height={Math.max(0, Y(Math.max(band.lo, lo)) - Y(Math.min(band.hi, hi)))} className="vc-band" />
              <line x1={pad.l} x2={W - pad.r} y1={Y(band.mean)} y2={Y(band.mean)} className="vc-band-mean" />
            </g>
          )}
          {maxShown && (
            <g>
              <line x1={pad.l} x2={W - pad.r} y1={Y(max!)} y2={Y(max!)} className="vc-bound" />
              <text x={W - pad.r - 2} y={Y(max!) - 4} textAnchor="end" className="vc-bound-label">max {num(max)}</text>
            </g>
          )}
          {minShown && (
            <g>
              <line x1={pad.l} x2={W - pad.r} y1={Y(min!)} y2={Y(min!)} className="vc-bound" />
              <text x={W - pad.r - 2} y={Y(min!) + 12} textAnchor="end" className="vc-bound-label">min {num(min)}</text>
            </g>
          )}
          {stable.length > 0 && <path d={line(stable)} className="vc-stable" />}
          {stable.map((p) => <rect key={"s" + p.t} x={X(p.t) - 2.5} y={Y(p.v) - 2.5} width="5" height="5" className="vc-stable-pt"><title>{`stable, ${new Date(p.t).toLocaleTimeString()}: ${num(p.v)}`}</title></rect>)}
          {canary.length > 0 && <path d={line(canary)} className="vc-canary" />}
          {canary.map((p) => failed.has(p.index)
            ? <g key={"c" + p.t} className="vc-fail-pt"><path d={`M${X(p.t) - 4},${Y(p.v) - 4}l8,8M${X(p.t) + 4},${Y(p.v) - 4}l-8,8`} /><title>{`new version, ${new Date(p.t).toLocaleTimeString()}: ${num(p.v)} (interval ${p.index + 1} failed)`}</title></g>
            : <circle key={"c" + p.t} cx={X(p.t)} cy={Y(p.v)} r="3" className="vc-canary-pt"><title>{`new version, ${new Date(p.t).toLocaleTimeString()}: ${num(p.v)}`}</title></circle>)}
          <line x1={pad.l} x2={W - pad.r} y1={H - pad.b} y2={H - pad.b} className="chart-base" />
        </svg>
      )}
      <ul className="chart-legend vchart-legend">
        <li><svg width="22" height="10" aria-hidden="true"><path d="M1 5h20" className="vc-canary" /><circle cx="11" cy="5" r="3" className="vc-canary-pt" /></svg>new version{m.canary.n ? `, mean ${num(m.canary.mean)}` : ": no readings"}</li>
        {stable.length > 0 && <li><svg width="22" height="10" aria-hidden="true"><path d="M1 5h20" className="vc-stable" /><rect x="8.5" y="2.5" width="5" height="5" className="vc-stable-pt" /></svg>stable version, mean {num(m.stable.mean)}</li>}
        {band && <li><svg width="22" height="10" aria-hidden="true"><rect x="1" y="1" width="20" height="8" className="vc-band" /><path d="M1 5h20" className="vc-band-mean" /></svg>baseline {num(band.mean)} ± {k} sd</li>}
        {max !== undefined && <li><svg width="22" height="10" aria-hidden="true"><path d="M1 5h20" className="vc-bound" /></svg>max {num(max)}{!maxShown && " (above the chart)"}</li>}
        {min !== undefined && <li><svg width="22" height="10" aria-hidden="true"><path d="M1 5h20" className="vc-bound" /></svg>min {num(min)}{!minShown && " (below the chart)"}</li>}
        {failed.size > 0 && <li><svg width="22" height="10" aria-hidden="true"><rect x="1" y="1" width="20" height="8" className="vc-failed" /><path d="M7 1l8 8M15 1l-8 8" className="vc-fail-pt" /></svg>failed interval</li>}
        {noData.size > 0 && <li><svg width="22" height="10" aria-hidden="true"><rect x="1" y="1" width="20" height="8" className="vc-nodata" /></svg>no data</li>}
      </ul>
    </div>
  );
}

// ---- logs ----

const CLASS_LABEL: Record<string, string> = {
  unknown: "New", unexpected_frequency: "More often", known: "Known", no_reference: "No reference",
};
const CLASS_TITLE: Record<string, string> = {
  unknown: "Not seen in the reference (the baseline or the stable version): new with this version",
  unexpected_frequency: "Seen in the reference, but much more often now",
  known: "Seen in the reference at a similar rate",
  no_reference: "There is no reference to compare with",
};

function LogBlock({ l, v }: { l: LogVerdict; v: VerificationView }) {
  const clusters = l.clusters ?? [];
  const errors = v.intervals.flatMap((iv) => (iv.logs ?? []).filter((x) => x.name === l.name && x.error).map((x) => ({ i: iv.index, e: x.error! })));
  const truncated = v.intervals.some((iv) => (iv.logs ?? []).some((x) => x.name === l.name && x.truncated));
  return (
    <div className="vmetric">
      <div className="vmetric-head">
        <strong className="mono">{l.name}</strong>
        <OutcomePill o={l.outcome} decided={v.decided || !!v.override} />
        <span className="faint small">{l.lines.toLocaleString()} lines read, {clusters.length} pattern{clusters.length === 1 ? "" : "s"}</span>
      </div>
      {l.reason && <p className="vmetric-reason">{l.reason}</p>}
      {truncated && <p className="sub small">The source had more lines than the step reads in an interval (<Mono>max_lines</Mono>); the rest were not read.</p>}
      {errors.length > 0 && (
        <details className="vreadings">
          <summary className="tfile-warn">Could not be read in {errors.length} interval{errors.length === 1 ? "" : "s"}</summary>
          <ul className="vlog-errors">{errors.map((x) => <li key={x.i}>Interval {x.i + 1}: <span className="mono">{x.e}</span></li>)}</ul>
        </details>
      )}
      {clusters.length > 0 && (
        <div className="table-scroll">
          <table className="inputs-table vlog-table">
            <thead>
              <tr><th>Class</th><th>Error</th><th className="vnum">Count</th><th className="vnum">Before</th><th className="vnum">Rate</th><th>Pattern</th></tr>
            </thead>
            <tbody>
              {clusters.map((c, i) => <ClusterRow key={i + c.template} c={c} />)}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function ClusterRow({ c }: { c: ClusterVerdict }) {
  const [open, setOpen] = useState(false);
  const samples = c.samples ?? [];
  const tone = c.failing ? "bad" : c.class === "unknown" || c.class === "unexpected_frequency" ? "warn" : "idle";
  return (
    <>
      <tr className={"vlog-row" + (c.failing ? " vrow-bad" : "")}>
        <td data-label="Class">
          <span className={`dpill dpill-${tone}`} title={CLASS_TITLE[c.class] ?? c.class}>{CLASS_LABEL[c.class] ?? c.class.replace(/_/g, " ")}</span>
          {c.failing && <span className="vfailing">failing</span>}
        </td>
        <td data-label="Error">{c.error ? <strong className="bad-ink">error</strong> : <span className="faint">no</span>}</td>
        <td data-label="Count" className="vnum">{c.count.toLocaleString()}</td>
        <td data-label="Before" className="vnum">{c.reference !== undefined ? c.reference.toLocaleString() : <span className="faint">0</span>}</td>
        <td data-label="Rate" className="vnum">{c.rate_ratio ? `${num(c.rate_ratio)}×` : <span className="faint">–</span>}</td>
        <td data-label="Pattern" className="vlog-template">
          <span className="mono">{c.template}</span>
          {samples.length > 0 && (
            <button type="button" className="linkbtn vlog-more" aria-expanded={open} onClick={() => setOpen(!open)}>
              {open ? "Hide" : "Show"} {samples.length} sample{samples.length === 1 ? "" : "s"}
            </button>
          )}
        </td>
      </tr>
      {open && samples.length > 0 && (
        <tr className="vlog-samples">
          <td colSpan={6}>
            <pre aria-label="Sample lines">{samples.join("\n")}</pre>
          </td>
        </tr>
      )}
    </>
  );
}

// ---- marking passed or failed ----

function VerifyForm({ run, step, v, onDone }: { run: RunDetail; step: StepView; v: VerificationView; onDone: () => void }) {
  const [controls, setControls] = useState<VerifyControls | null>(null);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [confirm, setConfirm] = useState(false);
  const hintId = useId();
  useEffect(() => {
    let alive = true;
    verifyApi.controls(run.id, step.id).then((c) => alive && setControls(c)).catch((e) => {
      if (alive && !(e instanceof ApiError && (e.status === 404 || e.status === 409))) setError(e);
    });
    return () => { alive = false; };
  }, [run.id, step.id, step.status, step.attempt]);
  if (!controls) return error ? <ErrorBox error={error} /> : null;
  const pass = controls.controls.find((c) => c.action === "mark-passed");
  const fail = controls.controls.find((c) => c.action === "mark-failed");
  const empty = reason.trim() === "";
  const aborts = v.on_fail === "abort" || v.on_fail === "rollback";
  const mark = async (outcome: "passed" | "failed") => {
    setBusy(true); setError(null);
    try {
      await verifyApi.override(run.id, step.id, outcome, reason.trim());
      setReason(""); setConfirm(false);
      onDone();
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const why = [pass, fail].filter((c) => c && !c.available && c.reason).map((c) => ({ action: c!.action, reason: c!.reason! }));
  const both = why.length === 2 && why[0].reason === why[1].reason;
  return (
    <form className="dctl vctl" aria-label="Mark the verification" onSubmit={(e) => e.preventDefault()}>
      <h4 className="h-xs">Decide by hand</h4>
      <p className="sub small">
        {v.status === "waiting" ? "The statistics could not decide. " : "You may decide before the window is over. "}
        Marking it passed lets the run go on; marking it failed acts as a failed verification does
        {aborts ? <> (<Mono>on_fail: {v.on_fail}</Mono>: the deployment's rollout is aborted)</> : null}.
        {" "}A pass marked by a person does not teach later verifications.
      </p>
      {(pass?.available || fail?.available) && (
        <label className="field">
          <span>Reason <span className="faint">(required, kept with the decision)</span></span>
          <textarea value={reason} maxLength={2000} rows={2} required aria-describedby={hintId} onChange={(e) => setReason(e.target.value)} />
        </label>
      )}
      <ErrorBox error={error} />
      <div className="approval-actions">
        <button type="button" className="btn btn-primary" disabled={!pass?.available || empty || busy} aria-describedby={hintId}
          onClick={() => mark("passed")}>Mark passed</button>
        {!confirm && (
          <button type="button" className="btn btn-danger-outline" disabled={!fail?.available || empty || busy} aria-describedby={hintId}
            onClick={() => (aborts ? setConfirm(true) : mark("failed"))}>Mark failed</button>
        )}
      </div>
      {confirm && (
        <div className="callout callout-bad dctl-confirm" role="alertdialog" aria-label="Confirm marking failed">
          <span>Mark the verification failed? The rollout of {v.service} in {v.environment} is aborted: all traffic goes back to the stable version.</span>
          <span className="dctl-confirm-actions">
            <button type="button" className="btn btn-danger" disabled={busy} onClick={() => mark("failed")}>Yes, mark failed</button>
            <button type="button" className="btn" disabled={busy} onClick={() => setConfirm(false)}>Keep watching</button>
          </span>
        </div>
      )}
      <div id={hintId} className="dctl-why">
        {both ? <p>{cap(why[0].reason)}</p> : why.map((w) => <p key={w.action}>{w.action === "mark-passed" ? "Mark passed" : "Mark failed"}: {w.reason}{/[.!?)]$/.test(w.reason) ? "" : "."}</p>)}
        {empty && (pass?.available || fail?.available) && <p>Write a reason first: it is kept with the decision and in the audit trail.</p>}
      </div>
    </form>
  );
}

function cap(s: string): string {
  const t = s.replace(/^./, (c) => c.toUpperCase());
  return /[.!?)]$/.test(t) ? t : t + ".";
}

// WhatWasRead lists the queries, collapsed.
function WhatWasRead({ v }: { v: VerificationView }) {
  const params = Object.entries(v.params ?? {});
  if (v.queries.length === 0 && params.length === 0) return null;
  return (
    <details className="dep-timeline vread">
      <summary>What was read ({v.queries.length} quer{v.queries.length === 1 ? "y" : "ies"})</summary>
      <ul className="vqueries">
        {v.queries.map((q) => (
          <li key={q.name}>
            <div className="vq-head"><strong className="mono">{q.name}</strong> <span className="faint">from</span> <Mono>{q.source}</Mono>{q.index && <> <span className="faint">index</span> <Mono>{q.index}</Mono></>}</div>
            {q.query && <pre className="code vq-code" aria-label={`Query of ${q.name}`}>{q.query}</pre>}
            {q.stable_query && <><div className="faint small">the stable version's</div><pre className="code vq-code">{q.stable_query}</pre></>}
            {q.statement && <div className="small">the statement <Mono>{q.statement}</Mono>, written by the source's operator</div>}
          </li>
        ))}
      </ul>
      {params.length > 0 && (
        <dl className="kv vparams">
          {params.map(([k, val]) => <Fragment key={k}><dt className="mono">{`{{${k}}}`}</dt><dd className="mono">{val}</dd></Fragment>)}
        </dl>
      )}
      <p className="sub small">Read by a deploy runner serving target <Mono>{v.target}</Mono>, with the credentials its operator configured; queries run once each interval is over.</p>
    </details>
  );
}

// ---- in the run's summary ----

// VerificationSummary lists a run's verifications in a line each, near the
// top of the run page; choosing one shows its step.
export function VerificationSummaryRows({ list, onSelect }: { list: VerificationView[] | null; onSelect: (step: string) => void }) {
  const live = !!list?.some((v) => verifyActive(v.status));
  const now = useNow(1000, live);
  if (!list || list.length === 0) return null;
  return (
    <section className="depsum" aria-label="Verifications of this run">
      {list.map((v) => (
        <button key={v.step} type="button" className="depsum-row" onClick={() => onSelect(v.step)}>
          <span className="depsum-env">{v.environment}</span>
          <span className="depsum-what">verifying {v.service} <Mono>{v.version || ""}</Mono></span>
          <VerifyPill v={{ status: v.status, outcome: v.verdict.outcome, override: v.override, start: v.start }} now={now} />
          <span className="vsum-text">{v.verdict.summary || (verifyActive(v.status) ? `interval ${Math.min(v.intervals.length + 1, v.total)} of ${v.total}` : "")}</span>
        </button>
      ))}
    </section>
  );
}

// VerificationsTab shows every verification of the run.
export function VerificationsTab({ run, list, error, reload, onStep }: {
  run: RunDetail; list: VerificationView[] | null; error: unknown; reload: () => void; onStep: (s: string) => void;
}) {
  const steps = run.step_list.filter((s) => s.kind === "verify");
  if (error && !list) return <ErrorBox error={error} />;
  return (
    <div className="imgs">
      <p className="sub imgs-intro">
        {steps.length === 1 ? "1 verify step" : `${steps.length} verify steps`}: each watches an environment for a window and decides from metrics and logs
        whether the new version is healthy. The statistics decide; a model, if one is named, only explains.
      </p>
      {steps.map((s) => (
        <div key={s.id} className="vtab-step">
          <h3 className="imgcard-title"><button type="button" className="linkbtn" onClick={() => onStep(s.id)}>{s.id}</button></h3>
          <VerificationPanel run={run} step={s} list={list} reload={reload} />
        </div>
      ))}
    </div>
  );
}
