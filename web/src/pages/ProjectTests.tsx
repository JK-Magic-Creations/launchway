import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { ProjectHead } from "./PipelineChrome";
import { api, ApiError, type CoverageTrend, type OverviewRun, type TestHistory, type TestOverview, type TestStat } from "../api";
import { ago, shortSha } from "../format";
import { navigate, onLink } from "../router";
import { BranchIcon, Empty, ErrorBox, Link } from "../components/ui";
import { CoverageTrendChart, Delta, MEASURES, measureLabel, pct, ratio } from "../components/Coverage";
import { Badge, HistoryStrip, ReportsHowTo, TestCounts, TestIcon, failing, passRate, testDuration, testStatusLabel, VariantPill } from "../components/Tests";
import { terms } from "../terms";

function params() { return new URLSearchParams(location.search); }

function refName(ref: string): string { return ref.replace(/^refs\/(heads|tags)\//, ""); }

// ProjectTestsPage shows a project's tests over its last runs of a branch:
// how the totals and the time they take move, the tests that fail most,
// flake or are slowest, and one test's history.
function ProjectResultsPage({ name }: { name: string }) {
  const [ref, setRef] = useState(() => params().get("ref") ?? "");
  const [test, setTest] = useState(() => params().get("test") ?? "");
  const [data, setData] = useState<TestOverview | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    const on = () => { setRef(params().get("ref") ?? ""); setTest(params().get("test") ?? ""); };
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  useEffect(() => {
    let alive = true;
    setData(null);
    api.projectTests(name, ref || undefined).then((d) => { if (alive) { setData(d); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [name, ref]);

  const [cov, setCov] = useState<CoverageTrend | null>(null);
  useEffect(() => {
    let alive = true;
    setCov(null);
    api.projectCoverage(name, ref || undefined).then((d) => alive && setCov(d)).catch(() => { /* a server without coverage: no chart */ });
    return () => { alive = false; };
  }, [name, ref]);

  const go = (changes: { ref?: string; test?: string }) => {
    const q = params();
    for (const [k, v] of Object.entries(changes)) { if (v) q.set(k, v); else q.delete(k); }
    navigate(`/projects/${encodeURIComponent(name)}/tests${q.toString() ? "?" + q.toString() : ""}`);
  };

  const withTests = data?.runs.filter((r) => r.tests) ?? [];
  const latest = withTests[withTests.length - 1];

  return (
    <div className="page page-wide">
      <ProjectHead name={name} on="tests" title={<>Tests and coverage of {name}</>} sentence={data?.ref ? <>The last {data.runs.length} runs of <BranchIcon /> <strong>{refName(data.ref)}</strong>: how the results move, and the tests that stand out.</> : "Results of the tests its steps report."}
        actions={data && data.refs.length > 0 && (
          <div className="chips" role="group" aria-label="Branch">
            {(data.refs.includes(data.ref) || !data.ref ? data.refs : [data.ref, ...data.refs]).slice(0, 8).map((r) => (
              <button key={r} type="button" className={"chip" + (r === data.ref ? " chip-on" : "")} aria-pressed={r === data.ref}
                onClick={() => go({ ref: r.startsWith("refs/heads/") ? refName(r) : r, test })}>{refName(r)}</button>
            ))}
          </div>
        )} />

      <ErrorBox error={error instanceof ApiError && error.status === 404 ? `There is no project called ${name}, or you may not see it.` : error} />
      {test && <HistoryPanel project={name} testKey={test} gref={data?.ref ?? ""} onClose={() => go({ test: "" })} />}
      {!data && !error && <div className="loading">Loading…</div>}
      {data && withTests.length === 0 && (
        <Empty title={data.ref ? `No test results on ${refName(data.ref)} yet` : "No test results yet"}>
          {data.refs.length > 0 && <p>Other branches have some: choose one above.</p>}
          <ReportsHowTo />
        </Empty>
      )}
      {data && !latest?.tests && cov && cov.runs.length > 0 && <CoverageCard trend={cov} />}
      {data && latest?.tests && (
        <>
          <section className="card ptrend" aria-label="Results over time">
            <div className="ptrend-head">
              <div>
                <h2 className="h-sm">Results, run by run</h2>
                <p className="sub small">Latest: <Link href={`/runs/${latest.id}?tab=tests`}>#{latest.number}</Link>, {passRate(latest.tests)} of {latest.tests.total - latest.tests.skipped} passed, {ago(latest.created_at)}</p>
              </div>
              <TestCounts t={latest.tests} />
            </div>
            <ResultsChart runs={data.runs} />
            <h2 className="h-sm ptrend-sub">Time the tests took</h2>
            <DurationChart runs={data.runs} />
            <details className="ptable">
              <summary>Show as a table</summary>
              <div className="table-scroll">
                <table className="inputs-table">
                  <thead><tr><th>{terms().say("{Run}")}</th><th>Commit</th><th>Passed</th><th>Failed</th><th>Skipped</th><th>Time</th></tr></thead>
                  <tbody>{[...data.runs].reverse().map((r) => (
                    <tr key={r.id}>
                      <td><Link href={`/runs/${r.id}?tab=tests`}>#{r.number}</Link></td>
                      <td className="mono">{shortSha(r.sha)}</td>
                      {r.tests ? <><td>{r.tests.passed}</td><td>{failing(r.tests)}</td><td>{r.tests.skipped}</td><td>{testDuration(r.tests.duration_ms)}</td></>
                        : <td colSpan={4} className="faint">no test results</td>}
                    </tr>
                  ))}</tbody>
                </table>
              </div>
            </details>
          </section>
          {cov && cov.runs.length > 0 && <CoverageCard trend={cov} />}
          <div className="pgrid">
            <StatList title="Failing most" empty={terms().say("Nothing failed in these {runs}.")} stats={data.failing} name={name} go={go}
              detail={(s) => <><strong>{s.failures}</strong> of {s.runs} runs{s.last_failed ? <>, last in #{s.last_failed}</> : null}</>} tone="bad" />
            <StatList title="Flaky" empty="No test both passed and failed on the same commit." stats={data.flaky} name={name} go={go}
              detail={(s) => <>{s.commits ? <>passed and failed on <strong>{s.commits}</strong> commit{s.commits > 1 ? "s" : ""}</> : null}
                {s.tool_retry ? <>{s.commits ? "; " : ""}passed only when the test tool ran it again</> : null}</>} tone="flaky" />
            <StatList title="Slowest" empty="No timings yet." stats={data.slowest} name={name} go={go}
              detail={(s) => <><strong>{testDuration(s.avg_ms ?? 0)}</strong> on average{(s.max_ms ?? 0) > (s.avg_ms ?? 0) * 1.05 ? <>, up to {testDuration(s.max_ms ?? 0)}</> : null} over {s.runs} run{s.runs > 1 ? "s" : ""}</>} />
          </div>
        </>
      )}
    </div>
  );
}

// CoverageCard shows the latest coverage and how line and branch coverage
// moved over the runs.
function CoverageCard({ trend }: { trend: CoverageTrend }) {
  const runs = trend.runs;
  const last = runs[runs.length - 1];
  const first = runs[0];
  const t = last.totals;
  return (
    <section className="card ptrend" aria-label="Coverage over time">
      <div className="ptrend-head">
        <div>
          <h2 className="h-sm">Coverage, run by run</h2>
          <p className="sub small">Latest: <Link href={`/runs/${last.id}?tab=coverage`}>#{last.number}</Link>, {ago(last.created_at)}{runs.length > 1 ? <>; since #{first.number}:</> : null}
            {runs.length > 1 && <> <Delta now={ratio(t.lines)} before={ratio(first.totals.lines)} label={`#${first.number}`} /></>}
          </p>
        </div>
        <dl className="pcov-stats">
          {MEASURES.map((m) => (
            <div key={m} className="tstat">
              <dt>{measureLabel(m, t.unit)}</dt>
              <dd>{pct(t[m])}</dd>
            </div>
          ))}
        </dl>
      </div>
      {runs.length > 1 ? <CoverageTrendChart trend={trend} /> : <p className="sub small">{terms().say("The chart appears once two {runs} have reported coverage.")}</p>}
      <details className="ptable">
        <summary>Show as a table</summary>
        <div className="table-scroll">
          <table className="inputs-table">
            <thead><tr><th>{terms().say("{Run}")}</th><th>Commit</th>{MEASURES.map((m) => <th key={m}>{measureLabel(m, t.unit)}</th>)}</tr></thead>
            <tbody>{[...runs].reverse().map((r) => (
              <tr key={r.id}>
                <td><Link href={`/runs/${r.id}?tab=coverage`}>#{r.number}</Link></td>
                <td className="mono">{shortSha(r.sha)}</td>
                {MEASURES.map((m) => <td key={m}>{pct(r.totals[m])}</td>)}
              </tr>
            ))}</tbody>
          </table>
        </div>
      </details>
    </section>
  );
}

function StatList({ title, empty, stats, detail, name, go, tone }: {
  title: string; empty: string; stats: TestStat[]; name: string; tone?: "bad" | "flaky";
  detail: (s: TestStat) => React.ReactNode; go: (c: { test: string }) => void;
}) {
  const [all, setAll] = useState(false);
  const shown = all ? stats : stats.slice(0, 5);
  return (
    <section className="card pstat" aria-label={title}>
      <h2 className="h-sm">{title}</h2>
      {stats.length === 0 ? <p className="sub">{empty}</p> : (
        <ol className="pstat-list">
          {shown.map((s) => (
            <li key={s.key}>
              <a href={`/projects/${encodeURIComponent(name)}/tests?test=${s.key}`} className="pstat-item"
                onClick={(e) => { if (e.button === 0 && !e.metaKey && !e.ctrlKey) { e.preventDefault(); go({ test: s.key }); } }}>
                <span className="pstat-name">
                  {tone === "flaky" && <Badge kind="flaky">Flaky</Badge>}
                  <span>{s.name}</span>
                  {s.variant && <VariantPill variant={s.variant} />}
                </span>
                <span className="pstat-suite">{s.suite}</span>
                <span className={"pstat-detail" + (tone === "bad" ? " pstat-bad" : "")}>{detail(s)}</span>
              </a>
            </li>
          ))}
        </ol>
      )}
      {stats.length > 5 && (
        <button type="button" className="linkbtn pstat-more" onClick={() => setAll(!all)}>{all ? "Show fewer" : `Show all ${stats.length}`}</button>
      )}
    </section>
  );
}

// Charts. One measure per chart: results as stacked bars, time as bars of
// its own. Each bar is a link to its run, with its numbers on hover and
// focus.

const H = 160, PAD_L = 44, PAD_B = 22, PAD_T = 8;

// The bars share the width there is, from 6 to 40 pixels each; with many
// runs the chart scrolls instead.
interface Layout { w: number; bar: number; step: number }

function layout(n: number, width: number): Layout {
  const step = Math.max(Math.min((width - PAD_L - 8) / Math.max(n, 1), 110), 10);
  const bar = Math.max(Math.min(step * 0.62, 40), 6);
  return { w: PAD_L + n * step + 8, bar, step };
}

function barX(l: Layout, i: number) { return PAD_L + i * l.step + (l.step - l.bar) / 2; }

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

function niceMax(v: number): number {
  if (v <= 4) return Math.max(Math.ceil(v / 2) * 2, 2);
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.2, 1.6, 2, 3, 4, 5, 6, 8, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

type Tip = { x: number; run: OverviewRun; text: string[] } | null;

function ChartFrame({ runs, max, label, ticks, children, tip, describe, l, boxRef }: {
  runs: OverviewRun[]; max: number; label: string; ticks: (v: number) => string; tip: Tip;
  children: React.ReactNode; describe: string; l: Layout; boxRef: React.RefObject<HTMLDivElement | null>;
}) {
  const w = l.w;
  const plot = H - PAD_B - PAD_T;
  const every = Math.ceil(runs.length / Math.max(Math.floor((w - PAD_L) / 44), 1));
  return (
    <div className="chart" ref={boxRef}>
      <svg viewBox={`0 0 ${w} ${H}`} width={w} height={H} role="group" aria-label={describe}>
        {[0, 0.5, 1].map((f) => (
          <g key={f}>
            <line x1={PAD_L} x2={w - 4} y1={PAD_T + plot * (1 - f)} y2={PAD_T + plot * (1 - f)} className={f === 0 ? "chart-base" : "chart-grid"} />
            <text x={PAD_L - 6} y={PAD_T + plot * (1 - f) + 4} className="chart-tick" textAnchor="end">{ticks(max * f)}</text>
          </g>
        ))}
        {runs.map((r, i) => i % every === 0 || i === runs.length - 1 ? (
          <text key={r.id} x={barX(l, i) + l.bar / 2} y={H - 6} className="chart-tick" textAnchor="middle">#{r.number}</text>
        ) : null)}
        {children}
        <title>{label}</title>
      </svg>
      {tip && (
        <div className="chart-tip" style={{ left: Math.min(tip.x, w - 160) }} role="status">
          <strong>#{tip.run.number}</strong> <span className="mono faint">{shortSha(tip.run.sha)}</span>
          {tip.text.map((t) => <div key={t}>{t}</div>)}
        </div>
      )}
    </div>
  );
}

function barLink(l: Layout, r: OverviewRun, i: number, setTip: (t: Tip) => void, text: string[], children: React.ReactNode) {
  const x = barX(l, i);
  const show = () => setTip({ x: x + l.bar, run: r, text });
  return (
    <a key={r.id} href={`/runs/${r.id}?tab=tests`} onClick={onLink as never} aria-label={terms().say(`{Run} ${r.number}: ${text.join(", ")}`)}
      onMouseEnter={show} onFocus={show} onMouseLeave={() => setTip(null)} onBlur={() => setTip(null)} className="chart-bar">
      {/* A wide, invisible target, easier to hover than the bar. */}
      <rect x={PAD_L + i * l.step} y={0} width={l.step} height={H - PAD_B} className="chart-hit" />
      {children}
    </a>
  );
}

/** A bar segment with a rounded end on top. */
function seg(x: number, y: number, h: number, bar: number, cls: string, top: boolean) {
  if (h <= 0) return null;
  const r = top ? Math.min(4, h, bar / 2) : 0;
  const d = `M${x},${y + h}V${y + r}${r ? `Q${x},${y} ${x + r},${y}` : ""}H${x + bar - r}${r ? `Q${x + bar},${y} ${x + bar},${y + r}` : ""}V${y + h}Z`;
  return <path d={d} className={cls} />;
}

function ResultsChart({ runs }: { runs: OverviewRun[] }) {
  const [tip, setTip] = useState<Tip>(null);
  const [box, width] = useWidth();
  const l = layout(runs.length, width);
  const max = niceMax(Math.max(...runs.map((r) => r.tests?.total ?? 0), 1));
  const plot = H - PAD_B - PAD_T;
  return (
    <>
      <ChartFrame runs={runs} max={max} tip={tip} l={l} boxRef={box} label={terms().say("Tests per {run}: passed, failed and skipped")} ticks={(v) => String(Math.round(v * 10) / 10)}
        describe={terms().say(`Tests per {run}, the last ${runs.length} runs, oldest first`)}>
        {runs.map((r, i) => {
          const x = barX(l, i);
          const t = r.tests;
          if (!t) {
            return barLink(l, r, i, setTip, ["no test results"],
              <rect x={x} y={H - PAD_B - 3} width={l.bar} height={3} rx={1.5} className="chart-none" />);
          }
          const scale = plot / max;
          const parts: [number, string][] = [[t.passed, "chart-ok"], [failing(t), "chart-bad"], [t.skipped, "chart-skip"]];
          const shown = parts.filter(([n]) => n > 0);
          let y = H - PAD_B;
          const out = shown.map(([n, cls], k) => {
            const h = Math.max(n * scale, 3);
            y -= h;
            // A 2px gap of surface between stacked parts.
            const el = seg(x, y, k > 0 ? h - 2 : h, l.bar, cls, k === shown.length - 1);
            return <g key={cls}>{el}</g>;
          });
          return barLink(l, r, i, setTip, [`${t.passed} passed`, `${failing(t)} failed`, `${t.skipped} skipped`], out);
        })}
      </ChartFrame>
      <ul className="chart-legend" aria-label="Legend">
        <li><span className="sw sw-ok" />Passed</li>
        <li><span className="sw sw-bad" />Failed or error</li>
        <li><span className="sw sw-skip" />Skipped</li>
        <li><span className="sw sw-none" />No results</li>
      </ul>
    </>
  );
}

function DurationChart({ runs }: { runs: OverviewRun[] }) {
  const [tip, setTip] = useState<Tip>(null);
  const [box, width] = useWidth();
  const l = layout(runs.length, width);
  const max = Math.max(...runs.map((r) => r.tests?.duration_ms ?? 0), 1);
  const plot = H - PAD_B - PAD_T;
  return (
    <ChartFrame runs={runs} max={max} tip={tip} l={l} boxRef={box} label={terms().say("Time the tests took per {run}")} ticks={(v) => (v === 0 ? "0" : testDuration(v))}
      describe={`Time the tests took, the last ${runs.length} runs, oldest first`}>
      {runs.map((r, i) => {
        const x = barX(l, i);
        if (!r.tests) {
          return barLink(l, r, i, setTip, ["no test results"], <rect x={x} y={H - PAD_B - 3} width={l.bar} height={3} rx={1.5} className="chart-none" />);
        }
        const h = Math.max((r.tests.duration_ms / max) * plot, 1.5);
        return barLink(l, r, i, setTip, [testDuration(r.tests.duration_ms)], seg(x, H - PAD_B - h, h, l.bar, "chart-time", true));
      })}
    </ChartFrame>
  );
}

// HistoryPanel shows one test's last results, as a strip of links to runs.
function HistoryPanel({ project, testKey, gref, onClose }: { project: string; testKey: string; gref: string; onClose: () => void }) {
  const [h, setH] = useState<TestHistory | null>(null);
  const [everyRef, setEveryRef] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    setH(null);
    api.testHistory(project, testKey, everyRef ? undefined : gref || undefined)
      .then((x) => { if (alive) { setH(x); setError(null); } })
      .catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [project, testKey, gref, everyRef]);
  const latest = h?.results[0];
  const counts = { passed: 0, failed: 0, errored: 0, skipped: 0 };
  for (const r of h?.results ?? []) counts[r.status] = (counts[r.status] ?? 0) + 1;
  return (
    <section className="card phist" aria-label="History of a test">
      <div className="phist-head">
        <div className="phist-title">
          {latest && <TestIcon status={latest.status} size={20} />}
          <div>
            <h2>{h?.name ?? "Test history"}</h2>
            {h && <div className="faint mono small">{h.suite}{h.file ? ` · ${h.file}` : ""}</div>}
          </div>
        </div>
        <div className="phist-actions">
          {gref && (
            <label className="checkline">
              <input type="checkbox" checked={everyRef} onChange={(e) => setEveryRef(e.target.checked)} />
              Every branch
            </label>
          )}
          <button type="button" className="btn btn-sm" onClick={onClose}>Close</button>
        </div>
      </div>
      {error ? <ErrorBox error={error instanceof ApiError && error.status === 404
        ? (everyRef || !gref ? terms().say("This test has no results in the {runs} kept.") : `This test has no results on ${refName(gref)}; try every branch.`) : error} />
        : !h ? <div className="loading">Loading…</div> : (
          <>
            <HistoryStrip results={h.results} />
            <p className="sub small">
              The last {h.results.length} runs{h.ref ? <> of {refName(h.ref)}</> : " on every branch"}, oldest on the left:{" "}
              {counts.passed} passed{h.results.some((r) => r.flaky) ? ` (${h.results.filter((r) => r.flaky).length} only on a retry)` : ""},
              {" "}{counts.failed + counts.errored} failed{counts.skipped ? `, ${counts.skipped} skipped` : ""}.
              {" "}{terms().say("Each square opens its {run}.")}
            </p>
            <ol className="phist-list">
              {h.results.slice(0, 10).map((r) => (
                <li key={r.run_id + r.step}>
                  <TestIcon status={r.status} size={14} />
                  <Link href={`/runs/${r.run_id}?tab=tests`}>#{r.number}</Link>
                  <span className="mono faint">{shortSha(r.sha)}</span>
                  {!h.ref && <span className="faint">{refName(r.ref)}</span>}
                  <span>{testStatusLabel(r.status)}</span>
                  {r.flaky && <Badge kind="flaky" title="Failed on an earlier attempt of the step">Passed on retry</Badge>}
                  <span className="faint">{testDuration(r.duration_ms)}</span>
                  {r.message && (r.status === "failed" || r.status === "errored") && <span className="phist-msg">{r.message.split("\n")[0]}</span>}
                  <span className="faint phist-ago">{ago(r.created_at)}</span>
                </li>
              ))}
            </ol>
          </>
        )}
    </section>
  );
}

const ProjectFlaky = lazy(() => import("./ProjectFlaky").then((m) => ({ default: m.ProjectFlaky })));

/** ProjectTestsPage is a project's tests; ?view=flaky shows its flaky tests (a lazy page of its own). */
export function ProjectTestsPage({ name }: { name: string }) {
  const [view, setView] = useState(() => params().get("view") ?? "");
  useEffect(() => {
    const on = () => setView(params().get("view") ?? "");
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  if (view === "flaky") {
    return <Suspense fallback={<div className="loading">Loading…</div>}><ProjectFlaky name={name} /></Suspense>;
  }
  return <ProjectResultsPage name={name} />;
}
