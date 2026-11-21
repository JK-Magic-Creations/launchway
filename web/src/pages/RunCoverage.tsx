import { useEffect, useRef, useState } from "react";
import { api, type CoverageFiles, type CoverageSort, type CoverageStep, type CoverageTotals, type RunCoverage, type RunDetail } from "../api";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { bytes } from "../components/Security";
import { CoverageHowTo, Delta, MEASURES, Meter, MiniBar, level, measureLabel, pct, ratio, type Measure } from "../components/Coverage";
import { terms } from "../terms";

const ENDED = ["succeeded", "failed", "skipped", "canceled"];
const PAGE = 50;

const FORMAT: Record<string, string> = {
  "cobertura": "Cobertura XML", "jacoco": "JaCoCo XML", "lcov": "LCOV", "go-coverprofile": "Go coverprofile",
  "clover": "Clover XML", "istanbul-json-summary": "Istanbul json-summary", "mixed": "several formats",
};

function refName(ref: string): string { return ref.replace(/^refs\/(heads|tags)\//, ""); }

// CoverageTab shows how much of the code the run's tests ran: the totals
// against the previous run and the default branch, each step against its
// fail_under, then packages and files.
export function CoverageTab({ run, version }: { run: RunDetail; version: string }) {
  const [cov, setCov] = useState<RunCoverage | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    api.runCoverage(run.id, true).then((c) => { if (alive) { setCov(c); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [run.id, version]);

  if (error) return <ErrorBox error={error} />;
  if (!cov) return <div className="loading">Loading…</div>;
  if (!cov.totals) {
    if (cov.declared.length > 0) {
      const going = run.step_list.filter((s) => cov.declared.includes(s.id) && !ENDED.includes(s.status));
      return (
        <Empty title={going.length ? "No coverage yet" : "No coverage collected"}>
          {going.length
            ? <>Coverage appears when {going.map((s) => s.id).join(", ")} {going.length > 1 ? "end" : "ends"}.</>
            : <>{cov.declared.join(", ")} {cov.declared.length > 1 ? "declare" : "declares"} coverage reports, but none were read. The step's log says why, for example a path that matched no files.</>}
        </Empty>
      );
    }
    return <Empty title="This run has no coverage"><CoverageHowTo /></Empty>;
  }
  return <CoverageView run={run} cov={cov} totals={cov.totals} version={version} />;
}

function CoverageView({ run, cov, totals, version }: { run: RunDetail; cov: RunCoverage; totals: CoverageTotals; version: string }) {
  const failed = cov.steps.filter((s) => s.failed_under);
  return (
    <div className="cov">
      <section className="card csum" aria-label="Coverage totals">
        <div className="csum-head">
          <h2 className="h-sm">{terms().say("Coverage of this {run}")}</h2>
          <span className="faint small">
            {cov.steps.reduce((n, s) => n + s.source_files, 0).toLocaleString()} source files from {cov.steps.length} step{cov.steps.length === 1 ? "" : "s"}
          </span>
        </div>
        <div className="cmeters">
          {MEASURES.map((m) => <TotalMeter key={m} m={m} totals={totals} cov={cov} />)}
        </div>
        {failed.length > 0 && (
          <div className="callout callout-bad cfail" role="note">
            <strong>{failed.length === 1 ? `${failed[0].step} failed its coverage threshold.` : `${failed.length} steps failed their coverage thresholds.`}</strong>
            {failed.map((s) => <div key={s.step}>{failed.length > 1 && <strong>{s.step}: </strong>}{s.failed_under}</div>)}
          </div>
        )}
      </section>

      {cov.steps.length > 0 && (
        <section className="card" aria-label="Coverage by step">
          <h2 className="h-sm">By step</h2>
          <ul className="covsteps">
            {cov.steps.map((s) => <StepCoverage key={s.step} s={s} run={run} />)}
          </ul>
        </section>
      )}

      <FilesTable run={run} steps={cov.steps} unit={totals.unit} version={version} />
    </div>
  );
}

function TotalMeter({ m, totals, cov }: { m: Measure; totals: CoverageTotals; cov: RunCoverage }) {
  const c = totals[m];
  const r = ratio(c);
  const label = measureLabel(m, totals.unit);
  // A threshold is shown on the totals only when every step that measured asks for the same one.
  const ts = [...new Set(cov.steps.map((s) => s.threshold?.[m]).filter((t): t is number => !!t))];
  const threshold = ts.length === 1 ? ts[0] : undefined;
  return (
    <div className={"cmeter-box cbox-" + level(r, threshold)}>
      <div className="cmeter-top">
        <span className="cmeter-label">{label}</span>
        <span className="cmeter-pct">{pct(r)}</span>
      </div>
      <Meter c={c} threshold={threshold} label={label} />
      <div className="cmeter-foot">
        {r === null ? <span className="faint">Not measured by these reports</span>
          : <span className="faint">{c.covered.toLocaleString()} of {c.total.toLocaleString()} {m === "lines" ? (totals.unit === "statements" ? "statements" : "lines") : m}{threshold ? `, needs ${threshold}%` : ""}</span>}
      </div>
      <div className="cmeter-deltas">
        {cov.previous && <Delta now={r} before={ratio(cov.previous.totals[m])} label={`#${cov.previous.run.number}`} href={`/runs/${cov.previous.run.id}?tab=coverage`} />}
        {cov.base && <Delta now={r} before={ratio(cov.base.totals[m])} label={refName(cov.base.ref)} href={`/runs/${cov.base.run.id}?tab=coverage`} />}
      </div>
    </div>
  );
}

function StepCoverage({ s, run }: { s: CoverageStep; run: RunDetail }) {
  return (
    <li className={"covstep" + (s.failed_under ? " covstep-bad" : "")}>
      <div className="covstep-head">
        <Link href={`/runs/${run.id}?step=${encodeURIComponent(s.step)}`} className="covstep-name">{s.step}</Link>
        {s.failed_under && <span className="pill pill-bad"><span aria-hidden="true">✕</span> Below threshold</span>}
        {!s.failed_under && s.threshold && <span className="pill pill-ok"><span aria-hidden="true">✓</span> Meets threshold</span>}
        {s.reused_from && <span className="badge-reused">Reused from <Link href={`/runs/${s.reused_from}?tab=coverage`}>#{s.reused_from_number}</Link></span>}
        <span className="faint small">{s.source_files.toLocaleString()} file{s.source_files === 1 ? "" : "s"}{s.attempt > 1 ? `, attempt ${s.attempt}` : ""}</span>
      </div>
      <div className="covstep-meters">
        {MEASURES.map((m) => {
          const label = measureLabel(m, s.unit);
          return (
            <div key={m} className="covstep-meter">
              <span className="covstep-mlabel">{label}</span>
              <Meter c={s[m]} threshold={s.threshold?.[m]} thin label={`${s.step} ${label}`} />
              <span className="covstep-pct">{pct(s[m])}{s.threshold?.[m] ? <span className="faint"> / {s.threshold[m]}%</span> : null}</span>
            </div>
          );
        })}
      </div>
      {s.failed_under && <p className="covstep-why">{s.failed_under}</p>}
      <ul className="treport-files">
        {s.reports.map((f) => (
          <li key={f.path} className={f.error ? "tfile-bad" : undefined}>
            <Mono>{f.path}</Mono>
            {f.error ? <span className="tfile-err">{f.error}</span>
              : <span className="faint">{FORMAT[f.format ?? ""] ?? f.format ?? "coverage"}, {f.files} file{f.files === 1 ? "" : "s"}, {bytes(f.bytes)}</span>}
            {f.warning && <span className="tfile-warn">{f.warning}</span>}
          </li>
        ))}
        {s.notes?.map((n, i) => <li key={"n" + i} className="tfile-note">{n}</li>)}
        {s.stored < s.source_files && <li className="tfile-note">{s.stored.toLocaleString()} of {s.source_files.toLocaleString()} files were kept for the table below; the totals count them all.</li>}
        {s.pruned && <li className="tfile-note">The files were removed to keep the project's storage within its limit; the totals remain.</li>}
      </ul>
    </li>
  );
}

// ---- packages and files ----

function FilesTable({ run, steps, unit, version }: { run: RunDetail; steps: CoverageStep[]; unit?: string; version: string }) {
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<CoverageSort>("lines");
  const [step, setStep] = useState("");
  const [offset, setOffset] = useState(0);
  const [data, setData] = useState<CoverageFiles | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    const id = window.setTimeout(() => { setQuery(search.trim()); setOffset(0); }, 200);
    return () => window.clearTimeout(id);
  }, [search]);
  const ask = useRef(0);
  useEffect(() => {
    const n = ++ask.current;
    api.coverageFiles(run.id, { q: query, sort, step, limit: PAGE, offset }, true)
      .then((d) => { if (ask.current === n) { setData(d); setError(null); } })
      .catch((e) => ask.current === n && setError(e));
  }, [run.id, query, sort, step, offset, version]);
  const lines = measureLabel("lines", unit);
  const many = steps.length > 1;
  return (
    <>
      {data && data.packages.length > 1 && (
        <section className="card" aria-label="Coverage by package">
          <details className="cpkgs" open={data.packages.length <= 12}>
            <summary><h2 className="h-sm cinline">By package or directory</h2> <span className="faint small">{data.packages.length}</span></summary>
            <div className="table-scroll">
              <table className="inputs-table ctable">
                <thead><tr><th>Package</th><th className="cnum">Files</th><th>{lines}</th><th>Branches</th><th>Functions</th></tr></thead>
                <tbody>
                  {data.packages.map((p) => (
                    <tr key={p.package}>
                      <td className="mono cpath">
                        <button type="button" className="linkbtn mono" onClick={() => { setSearch(p.package); }}>{p.package || "(top level)"}</button>
                      </td>
                      <td className="cnum">{p.files}</td>
                      <td><MiniBar c={p.lines} label={lines} /></td>
                      <td><MiniBar c={p.branches} label="branches" /></td>
                      <td><MiniBar c={p.functions} label="functions" /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        </section>
      )}

      <section className="card cfiles" aria-label="Coverage by file">
        <div className="cfiles-head">
          <h2 className="h-sm">Files {data && <span className="faint">{data.matching.toLocaleString()}</span>}</h2>
          <div className="cfiles-tools">
            <label className="search">
              <span className="visually-hidden">Search files</span>
              <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true" className="ic-line"><circle cx="7" cy="7" r="4.6" /><path d="M10.5 10.5L14 14" /></svg>
              <input type="search" placeholder="Search paths and packages" value={search} onChange={(e) => setSearch(e.target.value)} />
            </label>
            {many && (
              <label className="select">
                <span className="visually-hidden">Step</span>
                <select value={step} onChange={(e) => { setStep(e.target.value); setOffset(0); }}>
                  <option value="">Every step</option>
                  {steps.map((s) => <option key={s.step} value={s.step}>{s.step}</option>)}
                </select>
              </label>
            )}
            <div className="seg" role="group" aria-label="Order">
              {([["lines", "Least covered"], ["missed", "Most missed"], ["path", "Path"]] as [CoverageSort, string][]).map(([k, l]) => (
                <button key={k} type="button" className={"seg-btn" + (sort === k ? " seg-on" : "")} aria-pressed={sort === k}
                  onClick={() => { setSort(k); setOffset(0); }}>{l}</button>
              ))}
            </div>
          </div>
        </div>
        <ErrorBox error={error} />
        {!data ? <div className="loading">Loading…</div> : data.files.length === 0 ? (
          <div className="empty empty-sm">{query ? <>No files match <strong>{query}</strong>.</> : terms().say("No per-file coverage was kept for this {run}.")}</div>
        ) : (
          <>
            <div className="table-scroll">
              <table className="inputs-table ctable cfile-table">
                <thead>
                  <tr>
                    <th>File</th>{many && <th>Step</th>}<th>{lines}</th><th className="cnum">Missed</th><th>Branches</th><th>Functions</th><th className="cnum">Change</th>
                  </tr>
                </thead>
                <tbody>
                  {data.files.map((f) => (
                    <tr key={f.step + f.path} className={"cfile cfile-" + level(ratio(f.lines))}>
                      <td className="mono cpath" data-label="File">{f.path}</td>
                      {many && <td data-label="Step">{f.step}</td>}
                      <td data-label={lines}><MiniBar c={f.lines} label={lines} /></td>
                      <td className="cnum" data-label="Missed">{f.lines.total ? (f.lines.total - f.lines.covered).toLocaleString() : "—"}</td>
                      <td data-label="Branches"><MiniBar c={f.branches} label="branches" /></td>
                      <td data-label="Functions"><MiniBar c={f.functions} label="functions" /></td>
                      <td className="cnum" data-label="Change"><Change v={f.change} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {data.matching > PAGE && (
              <div className="cpager">
                <button type="button" className="btn btn-sm" disabled={offset === 0} onClick={() => setOffset(Math.max(offset - PAGE, 0))}>Previous</button>
                <span className="faint small">{(offset + 1).toLocaleString()}–{Math.min(offset + PAGE, data.matching).toLocaleString()} of {data.matching.toLocaleString()}</span>
                <button type="button" className="btn btn-sm" disabled={offset + PAGE >= data.matching} onClick={() => setOffset(offset + PAGE)}>Next</button>
              </div>
            )}
          </>
        )}
      </section>
    </>
  );
}

function Change({ v }: { v?: number | null }) {
  if (v === undefined || v === null) return <span className="faint" title={terms().say("Not in the previous {run}")}>new</span>;
  const d = Math.round(v * 10) / 10;
  if (d === 0) return <span className="faint">±0</span>;
  return (
    <span className={"cchange " + (d > 0 ? "cchange-up" : "cchange-down")}>
      <span aria-hidden="true">{d > 0 ? "▲" : "▼"} </span>{d > 0 ? "+" : "−"}{Math.abs(d).toFixed(1)}
      <span className="visually-hidden"> points</span>
    </span>
  );
}
