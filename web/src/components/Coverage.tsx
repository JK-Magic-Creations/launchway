import { useEffect, useRef, useState } from "react";
import type { CoverageCounter, CoverageTotals, CoverageTrend } from "../api";
import { shortSha } from "../format";
import { onLink } from "../router";
import { terms } from "../terms";
import { PSub, SpanHidden, SpanMono } from "../kit/bits";

// Pieces shared by a run's Coverage tab and a project's coverage trend.

export type Measure = "lines" | "branches" | "functions";
export const MEASURES: Measure[] = ["lines", "branches", "functions"];

/** The name of a measure; Go counts statements, not lines. */
export function measureLabel(m: Measure, unit?: string): string {
  if (m === "lines") return unit === "statements" ? "Statements" : "Lines";
  return m === "branches" ? "Branches" : "Functions";
}

/** Covered as a percentage, or null when the measure was not taken. */
export function ratio(c?: CoverageCounter): number | null {
  if (!c || c.total <= 0) return null;
  return (c.covered / c.total) * 100;
}

/** 87.5%, 100%, 0%; — when not measured. Never rounds up to 100% or down to 0%. */
export function pct(c?: CoverageCounter | number | null): string {
  const r = typeof c === "number" ? c : c === null ? null : ratio(c ?? undefined);
  if (r === null || r === undefined) return "—";
  if (r >= 100) return "100%";
  if (r <= 0) return "0%";
  const v = r >= 99.95 ? 99.9 : r < 0.05 ? 0.1 : r;
  return `${v.toFixed(1)}%`;
}

/** A level for colour, always shown with its number: low under 50%, fair under 80%. */
export function level(r: number | null, threshold?: number): "none" | "low" | "fair" | "good" {
  if (r === null) return "none";
  if (threshold && r < threshold) return "low";
  if (r < 50) return "low";
  if (r < 80) return "fair";
  return "good";
}

/** A change in points, with an arrow and words, not colour alone. */
export function Delta({ now, before, label, href }: { now: number | null; before: number | null; label: string; href?: string }) {
  if (now === null || before === null) return null;
  const d = Math.round((now - before) * 10) / 10;
  const kind = d > 0 ? "up" : d < 0 ? "down" : "same";
  const text = d === 0 ? "no change" : `${d > 0 ? "+" : "−"}${Math.abs(d).toFixed(1)} pts`;
  const words = d === 0 ? `no change against ${label}` : `${d > 0 ? "up" : "down"} ${Math.abs(d).toFixed(1)} points against ${label}`;
  const body = (
    <>
      <svg width="11" height="11" viewBox="0 0 12 12" aria-hidden="true" className="cdelta-ic">
        {kind === "up" ? <path d="M6 2l4 6H2z" /> : kind === "down" ? <path d="M6 10L2 4h8z" /> : <path d="M2 4.5h8M2 7.5h8" fill="none" strokeWidth="1.6" />}
      </svg>
      <span aria-hidden="true">{text}</span><span aria-hidden="true" className="cdelta-vs"> vs {label}</span>
      <SpanHidden>{words}</SpanHidden>
    </>
  );
  return href
    ? <a href={href} onClick={onLink} className={"cdelta cdelta-" + kind}>{body}</a>
    : <span className={"cdelta cdelta-" + kind}>{body}</span>;
}

/** A horizontal meter, with a marker where a step's fail_under is. */
export function Meter({ c, threshold, thin, label }: { c?: CoverageCounter; threshold?: number; thin?: boolean; label: string }) {
  const r = ratio(c);
  const lv = level(r, threshold);
  return (
    <div className={"cmeter" + (thin ? " cmeter-thin" : "")} role="img"
      aria-label={r === null ? `${label}: not measured` : `${label}: ${pct(r)} covered${threshold ? `, at least ${threshold}% required` : ""}`}>
      {r !== null && <span className={"cmeter-fill cfill-" + lv} style={{ width: `${Math.max(r, 0.8)}%` }} />}
      {threshold ? <span className="cmeter-mark" style={{ left: `${threshold}%` }} /> : null}
    </div>
  );
}

/** A small bar beside a number, for tables. */
export function MiniBar({ c, label }: { c?: CoverageCounter; label: string }) {
  const r = ratio(c);
  return (
    <span className="cmini">
      <span className="cmini-num">{pct(r)}</span>
      <span className="cmini-bar" aria-hidden="true">{r !== null && <span className={"cfill-" + level(r)} style={{ width: `${Math.max(r, 2)}%` }} />}</span>
      <SpanHidden>{label}</SpanHidden>
    </span>
  );
}

/** How to report coverage, for empty states. */
export function CoverageHowTo({ compact }: { compact?: boolean }) {
  return (
    <div className="howto">
      {!compact && <p>Name the coverage report a step writes, and Launchway reads it when the step ends. With <SpanMono>fail_under</SpanMono> the step fails when coverage is lower:</p>}
      <pre className="code">{`steps:
  test:
    image: node:22
    run: npx jest --coverage
    reports:
      coverage:
        paths: coverage/lcov.info
        fail_under: 80`}</pre>
      <PSub>Cobertura XML (coverage.py, Istanbul, .NET coverlet), JaCoCo XML, Clover XML, LCOV, Go coverprofiles (<SpanMono>go test -coverprofile</SpanMono>) and Istanbul's json-summary are read. <SpanMono>fail_under</SpanMono> also takes <SpanMono>{"{ lines: 80, branches: 60, functions: 70 }"}</SpanMono>.</PSub>
    </div>
  );
}

// ---- trend ----

const H = 170, PAD_L = 40, PAD_B = 22, PAD_T = 10, PAD_R = 10;

function useWidth(): [React.RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(800);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    setW(el.clientWidth);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

/** Line (or statement) coverage over runs as an area, branch coverage as a dashed line; each run is a link. */
export function CoverageTrendChart({ trend }: { trend: CoverageTrend }) {
  const [box, width] = useWidth();
  const [tip, setTip] = useState<number | null>(null);
  const runs = trend.runs;
  const unit = runs[runs.length - 1]?.totals.unit;
  const w = Math.max(width, 260);
  const n = runs.length;
  const plotW = w - PAD_L - PAD_R;
  const plotH = H - PAD_T - PAD_B;
  const x = (i: number) => PAD_L + (n === 1 ? plotW / 2 : (i * plotW) / (n - 1));
  const y = (v: number) => PAD_T + plotH * (1 - v / 100);
  const pts = (m: Measure) => runs.map((r, i) => [i, ratio(r.totals[m])] as const).filter(([, v]) => v !== null) as [number, number][];
  const line = (p: [number, number][]) => p.map(([i, v], k) => `${k ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join("");
  const lines = pts("lines");
  const branches = pts("branches");
  const area = lines.length > 1 ? `${line(lines)}L${x(lines[lines.length - 1][0]).toFixed(1)},${y(0)}L${x(lines[0][0]).toFixed(1)},${y(0)}Z` : "";
  const every = Math.ceil(n / Math.max(Math.floor(plotW / 44), 1));
  const step = n > 1 ? plotW / (n - 1) : plotW;
  const t = tip !== null ? runs[tip] : null;
  return (
    <>
      <div className="chart" ref={box}>
        <svg viewBox={`0 0 ${w} ${H}`} width={w} height={H} role="group"
          aria-label={`${measureLabel("lines", unit)} and branch coverage over the last ${n} runs, oldest first`}>
          {[0, 50, 100].map((v) => (
            <g key={v}>
              <line x1={PAD_L} x2={w - PAD_R} y1={y(v)} y2={y(v)} className={v === 0 ? "chart-base" : "chart-grid"} />
              <text x={PAD_L - 6} y={y(v) + 4} className="chart-tick" textAnchor="end">{v}%</text>
            </g>
          ))}
          {runs.map((r, i) => (i % every === 0 || i === n - 1) && (
            <text key={r.id} x={x(i)} y={H - 6} className="chart-tick" textAnchor="middle">#{r.number}</text>
          ))}
          {area && <path d={area} className="ctrend-area" />}
          {lines.length > 1 && <path d={line(lines)} className="ctrend-line" />}
          {branches.length > 1 && <path d={line(branches)} className="ctrend-branch" />}
          {runs.map((r, i) => {
            const lv = ratio(r.totals.lines);
            const bv = ratio(r.totals.branches);
            const label = terms().say(`{Run} ${r.number}: ${measureLabel("lines", unit).toLowerCase()} ${pct(lv)}, branches ${pct(bv)}`);
            return (
              <a key={r.id} href={`/runs/${r.id}?tab=coverage`} onClick={onLink as never} aria-label={label} className="chart-bar"
                onMouseEnter={() => setTip(i)} onFocus={() => setTip(i)} onMouseLeave={() => setTip(null)} onBlur={() => setTip(null)}>
                <rect x={x(i) - step / 2} y={0} width={step} height={H - PAD_B} className="chart-hit" />
                {lv !== null && <circle cx={x(i)} cy={y(lv)} r={tip === i ? 5 : 3.5} className="ctrend-dot" />}
                {bv !== null && <rect x={x(i) - 3} y={y(bv) - 3} width={6} height={6} className="ctrend-bdot" transform={`rotate(45 ${x(i)} ${y(bv)})`} />}
              </a>
            );
          })}
        </svg>
        {t && tip !== null && (
          <div className="chart-tip" style={{ left: Math.min(x(tip), w - 170) }} role="status">
            <strong>#{t.number}</strong> <span className="mono faint">{shortSha(t.sha)}</span>
            <div>{measureLabel("lines", unit)}: {pct(t.totals.lines)}</div>
            <div>Branches: {pct(t.totals.branches)}</div>
            <div>Functions: {pct(t.totals.functions)}</div>
          </div>
        )}
      </div>
      <ul className="chart-legend" aria-label="Legend">
        <li><span className="sw sw-cline" />{measureLabel("lines", unit)}</li>
        <li><span className="sw sw-cbranch" />Branches</li>
      </ul>
    </>
  );
}

/** The totals of the latest of a trend's runs, or null. */
export function latestTotals(t: CoverageTrend | null): CoverageTotals | null {
  return t?.runs[t.runs.length - 1]?.totals ?? null;
}
