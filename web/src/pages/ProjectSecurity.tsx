import { useEffect, useMemo, useRef, useState } from "react";
import { ProjectHead } from "./PipelineChrome";
import { api, ApiError, type FindingView, type ProjectSecurity, type SecurityRun, type Severity } from "../api";
import { ago, shortSha } from "../format";
import { navigate, onLink } from "../router";
import { BranchIcon, Empty, ErrorBox, Link } from "../components/ui";
import { FindingsHowTo, SEVERITIES, SeverityLegend, serious, seriousLabel, severityLabel, severityRank } from "../components/Security";
import { FindingRow } from "./RunSecurity";
import { terms } from "../terms";

function params() { return new URLSearchParams(location.search); }

function refName(ref: string): string { return ref.replace(/^refs\/(heads|tags)\//, ""); }

// ProjectSecurityPage shows a project's findings over its last runs of a
// branch, and the findings still open in the latest, oldest first within
// each severity.
export function ProjectSecurityPage({ name }: { name: string }) {
  const [ref, setRef] = useState(() => params().get("ref") ?? "");
  const [data, setData] = useState<ProjectSecurity | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    const on = () => setRef(params().get("ref") ?? "");
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  useEffect(() => {
    let alive = true;
    setData(null);
    api.projectSecurity(name, ref || undefined, 30).then((d) => { if (alive) { setData(d); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [name, ref]);

  const go = (r: string) => {
    const q = params();
    if (r) q.set("ref", r); else q.delete("ref");
    navigate(`/projects/${encodeURIComponent(name)}/security${q.toString() ? "?" + q.toString() : ""}`);
  };

  const withFindings = data?.runs.filter((r) => r.findings) ?? [];
  const c = data?.counts;

  return (
    <div className="page page-wide">
      <ProjectHead name={name} on="security" title={<>Security of {name}</>} sentence={data?.ref ? <>The last {data.runs.length} runs of <BranchIcon /> <strong>{refName(data.ref)}</strong>: how the findings move, and those still open.</> : "What the scanners its steps run have found."}
        actions={data && data.refs.length > 0 && (
          <div className="chips" role="group" aria-label="Branch">
            {(data.refs.includes(data.ref) || !data.ref ? data.refs : [data.ref, ...data.refs]).slice(0, 8).map((r) => (
              <button key={r} type="button" className={"chip" + (r === data.ref ? " chip-on" : "")} aria-pressed={r === data.ref}
                onClick={() => go(r.startsWith("refs/heads/") ? refName(r) : r)}>{refName(r)}</button>
            ))}
          </div>
        )} />

      <ErrorBox error={error instanceof ApiError && error.status === 404 ? `There is no project called ${name}, or you may not see it.` : error} />
      {!data && !error && <div className="loading">Loading…</div>}
      {data && withFindings.length === 0 && (
        <Empty title={data.ref ? `No findings on ${refName(data.ref)} yet` : "No findings yet"}>
          {data.refs.length > 0 && <p>Other branches have some: choose one above.</p>}
          <FindingsHowTo />
        </Empty>
      )}
      {data && withFindings.length > 0 && c && data.latest && (
        <>
          <section className="card ptrend" aria-label="Findings over time">
            <div className="ptrend-head">
              <div>
                <h2 className="h-sm">Findings, run by run</h2>
                <p className="sub small">
                  Latest: <Link href={`/runs/${data.latest.id}?tab=security`}>#{data.latest.number}</Link>, {c.total} finding{c.total === 1 ? "" : "s"}
                  {serious(c) > 0 ? <>, <strong className="sec-bad">{seriousLabel(c)}</strong></> : null}.
                  {" "}<strong className={data.statuses.new ? "sec-new" : undefined}>{data.statuses.new} new</strong>, {data.statuses.existing} existing and{" "}
                  <strong className={data.statuses.fixed ? "sec-fixed" : undefined}>{data.statuses.fixed} fixed</strong> since the run before it.
                </p>
              </div>
            </div>
            <SeverityChart runs={data.runs} />
            <SeverityLegend c={c} />
            <details className="ptable">
              <summary>Show as a table</summary>
              <div className="table-scroll">
                <table className="inputs-table">
                  <thead><tr><th>{terms().say("{Run}")}</th><th>Commit</th>{SEVERITIES.map((s) => <th key={s}>{severityLabel(s)}</th>)}<th>All</th></tr></thead>
                  <tbody>{[...data.runs].reverse().map((r) => (
                    <tr key={r.id}>
                      <td><Link href={`/runs/${r.id}?tab=security`}>#{r.number}</Link></td>
                      <td className="mono">{shortSha(r.sha)}</td>
                      {r.findings ? <>{SEVERITIES.map((s) => <td key={s}>{r.findings![s]}</td>)}<td>{r.findings.total}</td></>
                        : <td colSpan={6} className="faint">no findings reported</td>}
                    </tr>
                  ))}</tbody>
                </table>
              </div>
            </details>
          </section>
          <OpenFindings data={data} />
        </>
      )}
    </div>
  );
}

// OpenFindings lists the latest run's findings, most severe first and,
// within a severity, open the longest first; filtered here, in the page.
function OpenFindings({ data }: { data: ProjectSecurity }) {
  const [severities, setSeverities] = useState<Severity[]>([]);
  const [tool, setTool] = useState("");
  const [search, setSearch] = useState("");
  const [all, setAll] = useState(false);
  const created = useMemo(() => new Map(data.runs.map((r) => [r.id, r.created_at])), [data.runs]);
  const multiStep = useMemo(() => new Set(data.open.map((f) => f.step)).size > 1, [data.open]);
  const tools = useMemo(() => [...new Set(data.open.map((f) => f.tool))].sort(), [data.open]);
  const sorted = useMemo(() => [...data.open].sort((a, b) =>
    severityRank(a.severity) - severityRank(b.severity) || (a.first_seen?.number ?? 0) - (b.first_seen?.number ?? 0)), [data.open]);
  const counts = useMemo(() => {
    const m: Record<string, number> = {};
    for (const f of data.open) m[f.severity] = (m[f.severity] ?? 0) + 1;
    return m;
  }, [data.open]);
  const q = search.trim().toLowerCase();
  const shown = sorted.filter((f) => (severities.length === 0 || severities.includes(f.severity)) && (!tool || f.tool === tool)
    && (!q || [f.rule_id, f.rule?.short, f.message, f.file].some((x) => x?.toLowerCase().includes(q))));
  const listed = all ? shown : shown.slice(0, 100);
  const toggle = (s: Severity) => setSeverities((cur) => cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]);

  return (
    <section className="secopen" aria-label="Open findings">
      <div className="card-head">
        <h2 className="h-sm">Open findings in #{data.latest?.number}</h2>
        {data.open_matched > data.open.length && <span className="faint small">The {data.open.length} most severe of {data.open_matched} are listed.</span>}
      </div>
      <div className="toolbar ttools">
        <div className="chips" role="group" aria-label="Severity">
          {SEVERITIES.map((s) => (s !== "unknown" || counts.unknown) && (
            <button key={s} type="button" aria-pressed={severities.includes(s)} className={"chip sevchip" + (severities.includes(s) ? " chip-on" : "")} onClick={() => toggle(s)}>
              <span className={"sevdot sevfill-" + s} aria-hidden="true" />{severityLabel(s)} <span className="chip-n">{counts[s] ?? 0}</span>
            </button>
          ))}
        </div>
        <div className="ttools-right">
          <label className="search">
            <span className="visually-hidden">Search open findings</span>
            <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true" className="ic-line"><circle cx="7" cy="7" r="4.6" /><path d="M10.5 10.5L14 14" /></svg>
            <input type="search" placeholder="Search rules, messages, files" value={search} onChange={(e) => setSearch(e.target.value)} />
          </label>
          {tools.length > 1 && (
            <label className="select">
              <span className="visually-hidden">Tool</span>
              <select value={tool} onChange={(e) => setTool(e.target.value)}>
                <option value="">Every tool</option>
                {tools.map((t) => <option key={t} value={t}>{t}</option>)}
              </select>
            </label>
          )}
        </div>
      </div>
      {shown.length === 0 ? (
        <div className="empty empty-sm">{data.open.length === 0 ? terms().say("Nothing is open: the latest {run} found nothing.") : "No open findings match these filters."}</div>
      ) : (
        <div className="tgroups">
          <ul className="tlist">
            {listed.map((f) => <FindingRow key={f.id} f={f} open={false} multiStep={multiStep} extra={<OpenSince f={f} at={f.first_seen ? created.get(f.first_seen.id) : undefined} />} />)}
          </ul>
          {shown.length > listed.length && (
            <button type="button" className="btn tmore" onClick={() => setAll(true)}>Show {shown.length - listed.length} more</button>
          )}
        </div>
      )}
    </section>
  );
}

function OpenSince({ f, at }: { f: FindingView; at?: number }) {
  if (!f.first_seen) return null;
  return <span className={f.status === "new" ? "sec-new" : undefined}>open since #{f.first_seen.number}{at ? `, ${ago(at)}` : ""}</span>;
}

// The chart: stacked bars by severity, one per run, each a link to its run,
// with its numbers on hover and focus. Laid out as on the tests page.

const H = 170, PAD_L = 40, PAD_B = 22, PAD_T = 8;

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

/** A bar segment with a rounded end on top. */
function seg(x: number, y: number, h: number, bar: number, cls: string, top: boolean) {
  if (h <= 0) return null;
  const r = top ? Math.min(4, h, bar / 2) : 0;
  const d = `M${x},${y + h}V${y + r}${r ? `Q${x},${y} ${x + r},${y}` : ""}H${x + bar - r}${r ? `Q${x + bar},${y} ${x + bar},${y + r}` : ""}V${y + h}Z`;
  return <path d={d} className={cls} />;
}

type Tip = { x: number; run: SecurityRun; text: string[] } | null;

function SeverityChart({ runs }: { runs: SecurityRun[] }) {
  const [tip, setTip] = useState<Tip>(null);
  const [box, width] = useWidth();
  const l = layout(runs.length, width);
  const max = niceMax(Math.max(...runs.map((r) => r.findings?.total ?? 0), 1));
  const plot = H - PAD_B - PAD_T;
  const w = l.w;
  const every = Math.ceil(runs.length / Math.max(Math.floor((w - PAD_L) / 44), 1));
  return (
    <div className="chart" ref={box}>
      <svg viewBox={`0 0 ${w} ${H}`} width={w} height={H} role="group" aria-label={`Findings per run by severity, the last ${runs.length} runs, oldest first`}>
        {[0, 0.5, 1].map((f) => (
          <g key={f}>
            <line x1={PAD_L} x2={w - 4} y1={PAD_T + plot * (1 - f)} y2={PAD_T + plot * (1 - f)} className={f === 0 ? "chart-base" : "chart-grid"} />
            <text x={PAD_L - 6} y={PAD_T + plot * (1 - f) + 4} className="chart-tick" textAnchor="end">{Math.round(max * f * 10) / 10}</text>
          </g>
        ))}
        {runs.map((r, i) => i % every === 0 || i === runs.length - 1 ? (
          <text key={r.id} x={barX(l, i) + l.bar / 2} y={H - 6} className="chart-tick" textAnchor="middle">#{r.number}</text>
        ) : null)}
        {runs.map((r, i) => {
          const x = barX(l, i);
          const c = r.findings;
          const text = c ? SEVERITIES.filter((s) => c[s] > 0).map((s) => `${c[s]} ${s}`) : ["no findings reported"];
          if (c && c.total === 0) text.push("no findings");
          const show = () => setTip({ x: x + l.bar, run: r, text });
          let marks: React.ReactNode;
          if (!c || c.total === 0) {
            marks = <rect x={x} y={H - PAD_B - 3} width={l.bar} height={3} rx={1.5} className={c ? "chart-zero" : "chart-none"} />;
          } else {
            const scale = plot / max;
            // Most severe at the bottom, where the eye starts.
            const shown = SEVERITIES.filter((s) => c[s] > 0);
            let y = H - PAD_B;
            marks = shown.map((s, k) => {
              const h = Math.max(c[s] * scale, 3);
              y -= h;
              return <g key={s}>{seg(x, y, k > 0 ? h - 2 : h, l.bar, "sevfill-" + s, k === shown.length - 1)}</g>;
            });
          }
          return (
            <a key={r.id} href={`/runs/${r.id}?tab=security`} onClick={onLink as never} aria-label={terms().say(`{Run} ${r.number}: ${text.join(", ")}`)}
              onMouseEnter={show} onFocus={show} onMouseLeave={() => setTip(null)} onBlur={() => setTip(null)} className="chart-bar">
              <rect x={PAD_L + i * l.step} y={0} width={l.step} height={H - PAD_B} className="chart-hit" />
              {marks}
            </a>
          );
        })}
        <title>Findings per run by severity</title>
      </svg>
      {tip && (
        <div className="chart-tip" style={{ left: Math.min(tip.x, w - 160) }} role="status">
          <strong>#{tip.run.number}</strong> <span className="mono faint">{shortSha(tip.run.sha)}</span>
          {tip.text.map((t) => <div key={t}>{t}</div>)}
          <div className="faint">{ago(tip.run.created_at)}</div>
        </div>
      )}
    </div>
  );
}
