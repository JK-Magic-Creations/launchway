import { useEffect, useMemo, useState, type ReactNode } from "react";
import { debriefApi, EXPORT_FORMATS, exportHref, type Debrief, type DebriefDiff, type DebriefFailure, type DebriefTrendPoint } from "../debriefApi";
import { ago, shortSha } from "../format";
import { Empty, ErrorBox, Link } from "../components/ui";
import { Badge, TestIcon, testDuration } from "../components/Tests";
import "../debrief.css";
import { terms } from "../terms";
import { refName } from "../refs";
import { prose } from "../prose";
import { DivLoading, PSub, SpanHidden, TdNum, ThCol, ThRow } from "../kit/bits";
const P = await prose("pages.Debrief");

// The Debrief of a run: what its tests say, in the order a person needs
// them: the summary, what changed, the failures with what each expected and
// got, flaky and quarantined tests, the slowest, the suites, coverage and the
// trend. Every chart has its numbers in a table beside it, and no state is
// shown by color alone.

const pct = (n: number) => (Number.isInteger(n) ? `${n}%` : `${n.toFixed(1)}%`);
const sign = (n: number) => (n > 0 ? `+${n}` : String(n));

export function DebriefView({ runId, version, openCase }: { runId: string; version: string; openCase: (id: number) => void }) {
  const [d, setD] = useState<Debrief | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    debriefApi.debrief(runId, true).then((x) => { if (alive) { setD(x); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [runId, version]);
  if (error) return <ErrorBox error={error} />;
  if (!d) return <DivLoading>Loading the Debrief…</DivLoading>;
  if (d.summary.total === 0) return <Empty title="No test results to report">{d.notes[0] ?? terms().say(P.t0)}</Empty>;
  return (
    <div className="debrief">
      <Summary d={d} />
      <Exports runId={runId} />
      <Failures d={d} runId={runId} openCase={openCase} />
      <Flaky d={d} />
      <Quarantined d={d} />
      <Slowest d={d} />
      <Suites d={d} />
      <Matrix d={d} />
      <CoverageSection d={d} />
      <Trend d={d} />
      {d.notes.length > 0 && (
        <section className="card" aria-labelledby="dn-h"><h2 className="h-sm" id="dn-h">What this report leaves out</h2>
          <ul className="dnotes">{d.notes.map((n) => <li key={n}>{n}</li>)}</ul></section>
      )}
    </div>
  );
}

function headline(d: Debrief): string {
  const s = d.summary, bad = s.failed + s.errored;
  if (bad === 0 && s.flaky === 0 && s.quarantined === 0) return `All ${s.passed} tests passed.`;
  if (bad === 0) return `${s.total} tests ran with no failures, with ${s.flaky > 0 ? `${s.flaky} flaky` : ""}${s.flaky > 0 && s.quarantined > 0 ? " and " : ""}${s.quarantined > 0 ? `${s.quarantined} quarantined` : ""}.`;
  const base = d.change && d.change.newly_failing > 0 ? `; ${d.change.newly_failing} of them pass on ${refName(d.change.base.branch) || "the default branch"}` : "";
  return `${bad} of ${s.total} tests failed${base}.`;
}

function Stat({ label, value, tone, icon }: { label: string; value: ReactNode; tone?: "bad" | "warn" | "ok"; icon?: ReactNode }) {
  return <div className={"dstat" + (tone ? " dstat-" + tone : "")}><dt>{label}</dt><dd>{icon}{value}</dd></div>;
}

function ResultBar({ d }: { d: Debrief }) {
  const s = d.summary, bad = s.failed + s.errored;
  const parts = [{ k: "pass", label: "Passed", n: s.passed }, { k: "fail", label: "Failed", n: bad }, { k: "skip", label: "Skipped", n: s.skipped }];
  let x = 0;
  const id = "dbar" + d.run.number;
  return (
    <figure className="dchart">
      <svg role="img" aria-labelledby={id + "t " + id + "d"} width="100%" height="26" viewBox="0 0 1000 26" preserveAspectRatio="none">
        <title id={id + "t"}>Results of {s.total} tests</title>
        <desc id={id + "d"}>{s.passed} passed, {bad} failed, {s.skipped} {P.t1}</desc>
        <defs>
          <pattern id={id + "p"} width="6" height="6" patternUnits="userSpaceOnUse"><rect width="6" height="6" className="dp-pass" /></pattern>
          <pattern id={id + "f"} width="8" height="8" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="8" height="8" className="dp-fail" /><rect width="3" height="8" fill="#fff" opacity=".6" /></pattern>
          <pattern id={id + "s"} width="6" height="6" patternUnits="userSpaceOnUse"><rect width="6" height="6" className="dp-skip" /><circle cx="3" cy="3" r="1.3" fill="#fff" /></pattern>
        </defs>
        {parts.map((p) => {
          if (p.n === 0) return null;
          const w = Math.max((p.n * 1000) / Math.max(s.total, 1), 3);
          const r = <rect key={p.k} x={x} y="1" width={w} height="24" fill={`url(#${id}${p.k[0]})`} className="dbar-seg" vectorEffect="non-scaling-stroke" />;
          x += w;
          return r;
        })}
      </svg>
      <figcaption>
        <table className="dtable dtable-slim"><caption className="visually-hidden">Results</caption>
          <thead><tr><ThCol>Result</ThCol><ThCol className="num">Tests</ThCol><ThCol className="num">Share</ThCol></tr></thead>
          <tbody>{parts.map((p) => (
            <tr key={p.k}><ThRow><span className={"dkey dkey-" + p.k} aria-hidden="true" />{p.label}</ThRow><TdNum>{p.n}</TdNum><TdNum>{pct((p.n * 100) / Math.max(s.total, 1))}</TdNum></tr>
          ))}</tbody></table>
      </figcaption>
    </figure>
  );
}

function Summary({ d }: { d: Debrief }) {
  const s = d.summary, bad = s.failed + s.errored, c = d.change;
  return (
    <section className="card dsummary" aria-labelledby="ds-h">
      <h2 className="h-sm" id="ds-h">Summary</h2>
      <p className="dheadline">{headline(d)}</p>
      <dl className="dstats">
        <Stat label="Tests" value={s.total} />
        <Stat label="Passed" value={s.passed} tone="ok" icon={<TestIcon status="passed" />} />
        <Stat label="Failed" value={bad} tone={bad > 0 ? "bad" : undefined} icon={<TestIcon status="failed" />} />
        <Stat label="Skipped" value={s.skipped} icon={<TestIcon status="skipped" />} />
        <Stat label="Flaky" value={s.flaky} tone={s.flaky > 0 ? "warn" : undefined} />
        {s.quarantined > 0 && <Stat label="Quarantined" value={s.quarantined} tone="warn" />}
        <Stat label="Duration" value={testDuration(s.duration_ms)} />
        <Stat label="Pass rate" value={pct(Math.round(s.pass_rate * 10) / 10)} />
      </dl>
      <ResultBar d={d} />
      {c && (
        <div className="dchange">
          <h3 className="h-xs">Against {refName(c.base.branch) || "the default branch"}, <Link href={`/runs/${c.base.id}?tab=tests`}>{terms().say("{run} #")}{c.base.number}</Link> ({shortSha(c.base.sha)})</h3>
          <ul>
            <li><strong className={c.newly_failing > 0 ? "t-bad" : ""}>{c.newly_failing}</strong> newly failing</li>
            <li><strong>{c.newly_passing}</strong> newly passing</li>
            <li>{sign(c.total_delta)} tests; duration {c.duration_delta_ms >= 0 ? "+" : "−"}{testDuration(Math.abs(c.duration_delta_ms))} {c.duration_delta_ms >= 0 ? "slower" : "faster"}</li>
            {c.slower.length > 0 && <li><strong>{c.slower.length}</strong> slower: {c.slower.slice(0, 4).map((x) => `${x.name} (${testDuration(x.base_ms)} to ${testDuration(x.ms)})`).join("; ")}{c.slower.length > 4 ? " and more" : ""}</li>}
          </ul>
        </div>
      )}
    </section>
  );
}

function Exports({ runId }: { runId: string }) {
  return (
    <section className="card dexports" aria-labelledby="de-h">
      <h2 className="h-sm" id="de-h">Take it with you</h2>
      <ul>
        {EXPORT_FORMATS.map((f) => (
          <li key={f.format}>
            <a className="btn btn-sm" href={exportHref(runId, f.format)} download>{`Download ${f.label}`}</a>
            <span className="sub small">{f.what}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function DiffView({ diff }: { diff: DebriefDiff }) {
  const multi = diff.expected.includes("\n") || diff.actual.includes("\n");
  if (!multi) {
    return (
      <div className="ddiff" role="group" aria-label="Expected against actual">
        <div className="ddiff-del"><span className="op" aria-hidden="true">−</span><span><SpanHidden>Expected: </SpanHidden>{diff.expected}</span></div>
        <div className="ddiff-add"><span className="op" aria-hidden="true">+</span><span><SpanHidden>Actual: </SpanHidden>{diff.actual}</span></div>
      </div>
    );
  }
  return (
    <div className="ddiff" role="group" aria-label={P.t2}>
      {(diff.lines ?? []).map((l, i) => (
        <div key={i} className={l.op === "-" ? "ddiff-del" : l.op === "+" ? "ddiff-add" : ""}>
          <span className="op" aria-hidden="true">{l.op === "-" ? "−" : l.op === "+" ? "+" : " "}</span>
          {l.op !== " " && <SpanHidden>{l.op === "-" ? "Expected: " : "Actual: "}</SpanHidden>}{l.text}
        </div>
      ))}
    </div>
  );
}

function FailureCard({ f, runId, openCase, open }: { f: DebriefFailure; runId: string; openCase: (id: number) => void; open: boolean }) {
  const base = f.failing_on_base;
  return (
    <details className="dfail" open={open}>
      <summary>
        <TestIcon status={f.status} />
        <span className="dfail-name">{f.name}</span>
        {f.suite && <span className="dfail-suite">in {f.suite}</span>}
        {f.new_here && <Badge kind="new">New here</Badge>}
        {(f.flaky_score ?? 0) > 0 && <Badge kind="flaky" title={P.t3}>Flaky history</Badge>}
      </summary>
      <div className="dfail-body">
        <ul className="dfacts">
          {f.file && <li>File <strong>{f.file}{f.line ? `:${f.line}` : ""}</strong></li>}
          <li>Took <strong>{testDuration(f.duration_ms)}</strong></li>
          {f.variant && <li>Variant <strong>{f.variant}</strong></li>}
          {f.owner && <li>{terms().say("{Owner}")} <strong>{f.owner}</strong></li>}
          {f.first_failed_in && <li>First failed in <Link href={`/runs/${f.first_failed_in.id}?tab=tests`}>{terms().say("{run} #")}{f.first_failed_in.number}</Link> ({shortSha(f.first_failed_in.sha)}, {ago(f.first_failed_in.created_at)})</li>}
          {base === "yes" && <li>{P.t4}<strong>yes</strong>{P.t5}</li>}
          {base === "no" && <li>{P.t4}<strong>no</strong>, so this change broke it</li>}
          {base === "unknown" && <li>{P.t4}<strong>not known</strong>, it did not run there</li>}
        </ul>
        {f.message && <pre className="dpre">{f.message}</pre>}
        {f.diff && <DiffView diff={f.diff} />}
        {f.details && <details className="dmore"><summary>Stack or trace</summary><pre className="dpre">{f.details}</pre></details>}
        {f.output && <details className="dmore"><summary>Captured output</summary><pre className="dpre">{f.output}</pre></details>}
        {f.attachments?.map((a, i) => (
          a.kind === "image" && f.case_id
            ? <figure key={i} className="dshot"><img src={`/api/v1/runs/${runId}/tests/cases/${f.case_id}/attachments/${i}`} alt={`${a.name}, attached to ${f.name}`} loading="lazy" /><figcaption>{a.name}</figcaption></figure>
            : <p key={i} className="sub small">Attachment: <a href={`/api/v1/runs/${runId}/tests/cases/${f.case_id}/attachments/${i}`} download>{a.name}</a> ({a.kind})</p>
        ))}
        {f.case_id ? <p><button type="button" className="btn btn-sm" onClick={() => openCase(f.case_id!)}>Open the full result</button></p> : null}
      </div>
    </details>
  );
}

function Failures({ d, runId, openCase }: { d: Debrief; runId: string; openCase: (id: number) => void }) {
  const [q, setQ] = useState("");
  const [only, setOnly] = useState<"all" | "new">("all");
  const list = useMemo(() => d.failures.filter((f) => (only === "all" || f.new_here) && (!q || `${f.suite} ${f.name} ${f.message ?? ""}`.toLowerCase().includes(q.toLowerCase()))), [d, q, only]);
  const newCount = d.failures.filter((f) => f.new_here).length;
  return (
    <section className="card" aria-labelledby="df-h">
      <h2 className="h-sm" id="df-h">Failures {d.failures.length > 0 && <span className="dcount">{d.failures.length}</span>}</h2>
      {d.failures.length === 0 ? <PSub>No test failed.</PSub> : (
        <>
          <div className="dtools">
            <label className="search"><SpanHidden>Search the failures</SpanHidden>
              <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search failures" /></label>
            {newCount > 0 && (
              <div className="chips" role="group" aria-label="Which failures">
                <button type="button" className={"chip" + (only === "all" ? " chip-on" : "")} aria-pressed={only === "all"} onClick={() => setOnly("all")}>All {d.failures.length}</button>
                <button type="button" className={"chip" + (only === "new" ? " chip-on" : "")} aria-pressed={only === "new"} onClick={() => setOnly("new")}>New here {newCount}</button>
              </div>
            )}
          </div>
          {list.length === 0 ? <PSub>No failure matches.</PSub> : list.map((f, i) => <FailureCard key={f.key + f.step} f={f} runId={runId} openCase={openCase} open={i < 5} />)}
        </>
      )}
    </section>
  );
}

function Flaky({ d }: { d: Debrief }) {
  return (
    <section className="card" aria-labelledby="dfl-h">
      <h2 className="h-sm" id="dfl-h">Flaky tests {d.flaky.length > 0 && <span className="dcount">{d.flaky.length}</span>}</h2>
      {d.flaky.length === 0 ? <PSub>{P.t6}</PSub> : (
        <div className="dscroll"><table className="dtable">
          <caption className="sub small">{P.t7}</caption>
          <thead><tr><ThCol>Test</ThCol><ThCol>{terms().say("In this {run}")}</ThCol><ThCol className="num">Score</ThCol><ThCol>Why</ThCol><ThCol>First seen</ThCol></tr></thead>
          <tbody>{d.flaky.map((f) => (
            <tr key={f.key}><ThRow><span className="dname">{f.name}</span><span className="dsuite">{f.suite}</span></ThRow>
              <td><Badge kind="flaky">{f.this_run}</Badge></td><TdNum>{f.score}</TdNum><td>{f.reason}</td><td>{f.first_seen ? ago(f.first_seen) : "—"}</td></tr>
          ))}</tbody></table></div>
      )}
      <p className="small"><Link href={`/projects/${encodeURIComponent(d.project)}/tests?view=flaky`}>All flaky tests of {d.project}</Link></p>
    </section>
  );
}

function Quarantined({ d }: { d: Debrief }) {
  if (d.quarantined.length === 0) return null;
  return (
    <section className="card" aria-labelledby="dq-h">
      <h2 className="h-sm" id="dq-h">Quarantined tests <span className="dcount">{d.quarantined.length}</span></h2>
      <PSub>{terms().say(P.t8)}</PSub>
      <div className="dscroll"><table className="dtable">
        <thead><tr><ThCol>Test</ThCol><ThCol>{terms().say("This {run}")}</ThCol><ThCol>{terms().say("{Owner}")}</ThCol><ThCol>Expires</ThCol><ThCol>Why</ThCol></tr></thead>
        <tbody>{d.quarantined.map((q) => (
          <tr key={q.key}><ThRow><span className="dname">{q.name}</span><span className="dsuite">{q.suite}</span></ThRow>
            <td><TestIcon status={q.status === "passed" ? "passed" : "failed"} size={14} /> {q.status}</td><td>{q.owner}</td>
            <td>{q.expires}{q.expired && <> <Badge kind="new">expired: counts as a failure</Badge></>}</td><td>{q.reason}</td></tr>
        ))}</tbody></table></div>
    </section>
  );
}

function Slowest({ d }: { d: Debrief }) {
  const top = d.slowest[0]?.duration_ms ?? 0;
  return (
    <section className="card" aria-labelledby="dsl-h">
      <h2 className="h-sm" id="dsl-h">Slowest tests</h2>
      {d.slowest.length === 0 ? <PSub>No durations were reported.</PSub> : (
        <div className="dscroll"><table className="dtable">
          <thead><tr><ThCol>Test</ThCol><ThCol className="num">Took</ThCol><ThCol className="num">Usually</ThCol><ThCol><SpanHidden>Relative to the slowest</SpanHidden></ThCol></tr></thead>
          <tbody>{d.slowest.map((s) => (
            <tr key={s.key}><ThRow><span className="dname">{s.name}</span><span className="dsuite">{s.suite}</span></ThRow>
              <TdNum>{testDuration(s.duration_ms)}</TdNum><TdNum>{s.avg_ms ? testDuration(s.avg_ms) : "—"}</TdNum>
              <td><span className="dmeter" aria-hidden="true"><i style={{ width: `${top ? Math.round((s.duration_ms * 100) / top) : 0}%` }} /></span></td></tr>
          ))}</tbody></table></div>
      )}
    </section>
  );
}

function Suites({ d }: { d: Debrief }) {
  const [q, setQ] = useState("");
  const [failingOnly, setFailingOnly] = useState(false);
  const rows = d.suites.filter((s) => (!q || s.name.toLowerCase().includes(q.toLowerCase())) && (!failingOnly || s.failed > 0));
  return (
    <section className="card" aria-labelledby="dsu-h">
      <h2 className="h-sm" id="dsu-h">Suites</h2>
      <div className="dtools">
        <label className="search"><SpanHidden>Filter suites</SpanHidden><input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter suites" /></label>
        <div className="chips" role="group" aria-label="Which suites">
          <button type="button" className={"chip" + (!failingOnly ? " chip-on" : "")} aria-pressed={!failingOnly} onClick={() => setFailingOnly(false)}>All</button>
          <button type="button" className={"chip" + (failingOnly ? " chip-on" : "")} aria-pressed={failingOnly} onClick={() => setFailingOnly(true)}>With failures</button>
        </div>
      </div>
      {rows.length === 0 ? <PSub>No suite matches.</PSub> : (
        <div className="dscroll"><table className="dtable">
          <thead><tr><ThCol>Suite</ThCol><ThCol className="num">Tests</ThCol><ThCol className="num">Passed</ThCol><ThCol className="num">Failed</ThCol><ThCol className="num">Skipped</ThCol><ThCol className="num">Time</ThCol></tr></thead>
          <tbody>{rows.map((s) => (
            <tr key={s.name}><ThRow style={{ paddingLeft: 10 + s.depth * 18 }}>{s.name}</ThRow><TdNum>{s.total}</TdNum><TdNum>{s.passed}</TdNum>
              <td className={"num" + (s.failed > 0 ? " t-bad" : "")}>{s.failed > 0 && <TestIcon status="failed" size={12} />} {s.failed}</td><TdNum>{s.skipped}</TdNum><TdNum>{testDuration(s.duration_ms)}</TdNum></tr>
          ))}</tbody></table></div>
      )}
    </section>
  );
}

function Matrix({ d }: { d: Debrief }) {
  const m = d.matrix;
  if (!m || m.rows.length === 0) return null;
  const label: Record<string, string> = { passed: "passed", failed: "failed", errored: "errored", skipped: "skipped" };
  return (
    <section className="card" aria-labelledby="dm-h">
      <h2 className="h-sm" id="dm-h">Combinations</h2>
      <div className="dscroll"><table className="dtable dmatrix">
        <caption className="sub small">{P.t9}</caption>
        <thead><tr><ThCol>Test</ThCol>{m.columns.map((c) => <ThCol key={c}>{c}</ThCol>)}</tr></thead>
        <tbody>{m.rows.map((r) => (
          <tr key={r.suite + r.name}><ThRow><span className="dname">{r.name}</span><span className="dsuite">{r.suite}</span></ThRow>
            {r.cells.map((c, i) => <td key={i}>{c ? <><TestIcon status={c as "passed"} size={14} /><SpanHidden>{label[c] ?? c}</SpanHidden></> : <SpanHidden>did not run</SpanHidden>}</td>)}</tr>
        ))}</tbody></table></div>
      {m.cut && <p className="sub small">The grid shows the first {m.rows.length} tests.</p>}
    </section>
  );
}

function CoverageSection({ d }: { d: Debrief }) {
  const c = d.coverage;
  return (
    <section className="card" aria-labelledby="dc-h">
      <h2 className="h-sm" id="dc-h">Coverage</h2>
      {!c ? <PSub>{terms().say(P.t10)}</PSub> : (
        <>
          <dl className="dstats">
            <Stat label="Lines covered" value={pct(Math.round(c.lines.percent * 10) / 10)} />
            {c.branches.total > 0 && <Stat label="Branches covered" value={pct(Math.round(c.branches.percent * 10) / 10)} />}
            {c.functions.total > 0 && <Stat label="Functions covered" value={pct(Math.round(c.functions.percent * 10) / 10)} />}
            {c.base_lines != null && <Stat label="Default branch" value={pct(Math.round(c.base_lines * 10) / 10)} />}
            {c.change && <Stat label="Of the changed lines" value={pct(Math.round(c.change.percent * 10) / 10)} tone={c.change.advisory ? "warn" : "ok"} />}
          </dl>
          {c.change && (
            <div className="dchangecov">
              <p><strong>Coverage of the change:</strong> {c.change.covered} of {c.change.total} {P.t11}</p>
              {c.change.advisory && <p className="dadvice"><Badge kind="flaky">Advice</Badge> {c.change.advisory}</p>}
              {c.change.files.length > 0 && <FileTable title="Changed files" files={c.change.files} />}
            </div>
          )}
          {c.least.length > 0 && <FileTable title={`Least covered files (of ${c.files})`} files={c.least} />}
        </>
      )}
    </section>
  );
}

function FileTable({ title, files }: { title: string; files: { path: string; covered: number; total: number; percent: number; missed?: string }[] }) {
  return (
    <div className="dscroll"><table className="dtable">
      <caption className="dcap">{title}</caption>
      <thead><tr><ThCol>File</ThCol><ThCol className="num">Lines</ThCol><ThCol className="num">Covered</ThCol><ThCol>Not covered</ThCol></tr></thead>
      <tbody>{files.map((f) => (
        <tr key={f.path}><ThRow className="dname">{f.path}</ThRow><TdNum>{f.covered}/{f.total}</TdNum><TdNum>{pct(Math.round(f.percent * 10) / 10)}</TdNum><td>{f.missed ?? ""}</td></tr>
      ))}</tbody></table></div>
  );
}

function floorOf(t: DebriefTrendPoint[]): number {
  const lo = Math.min(100, ...t.map((p) => p.pass_rate));
  return Math.min(Math.max(Math.floor(lo / 5) * 5 - 5, 0), 90);
}

function Trend({ d }: { d: Debrief }) {
  const t = d.trend;
  if (t.length < 2) return (
    <section className="card" aria-labelledby="dt-h"><h2 className="h-sm" id="dt-h">Trend</h2><PSub>{P.t12}</PSub></section>
  );
  const W = 640, H = 220, L = 40, R = 16, T = 14, B = 28, lo = floorOf(t);
  const y = (v: number) => T + (H - T - B) * (1 - (v - lo) / (100 - lo));
  const x = (i: number) => L + ((W - L - R) * i) / (t.length - 1);
  const step = Math.max(1, Math.ceil(t.length / 12));
  return (
    <section className="card" aria-labelledby="dt-h">
      <h2 className="h-sm" id="dt-h">Trend</h2>
      <figure className="dchart">
        <svg role="img" aria-labelledby="dtr-t dtr-d" viewBox={`0 0 ${W} ${H}`} width="100%" style={{ maxWidth: W }}>
          <title id="dtr-t">Pass rate over the last {t.length} runs</title>
          <desc id="dtr-d">From {pct(Math.round(t[0].pass_rate * 10) / 10)} to {pct(Math.round(t[t.length - 1].pass_rate * 10) / 10)}; the table has every value.</desc>
          {[lo, (lo + 100) / 2, 100].map((g) => <g key={g}><line x1={L} x2={W - R} y1={y(g)} y2={y(g)} className="dgrid" /><text x={L - 6} y={y(g) + 4} textAnchor="end">{Math.round(g)}%</text></g>)}
          <polyline fill="none" className="dline" points={t.map((p, i) => `${x(i)},${y(p.pass_rate)}`).join(" ")} />
          {t.map((p, i) => (
            <g key={p.number}>
              {p.pass_rate < 100
                ? <path d={`M${x(i)} ${y(p.pass_rate) - 6} l5 9 h-10 z`} className="dmark" />
                : <circle cx={x(i)} cy={y(p.pass_rate)} r="4" className="dmark-ok" />}
              {(i % step === 0 || i === t.length - 1) && <text x={x(i)} y={H - 10} textAnchor="middle">#{p.number}</text>}
            </g>
          ))}
        </svg>
        <figcaption className="sub small">{terms().say(P.t13)} {lo}{terms().say(P.t14)}</figcaption>
      </figure>
      <details className="dmore"><summary>Show the trend as a table</summary>
        <div className="dscroll"><table className="dtable"><thead><tr><ThCol>{terms().say("{Run}")}</ThCol><ThCol>Commit</ThCol><ThCol className="num">Tests</ThCol><ThCol className="num">Pass rate</ThCol><ThCol className="num">Duration</ThCol><ThCol className="num">Flaky</ThCol></tr></thead>
          <tbody>{t.map((p) => <tr key={p.number}><ThRow>#{p.number}</ThRow><td>{shortSha(p.sha)}</td><TdNum>{p.total}</TdNum><TdNum>{pct(Math.round(p.pass_rate * 10) / 10)}</TdNum><TdNum>{testDuration(p.duration_ms)}</TdNum><TdNum>{p.flaky}</TdNum></tr>)}</tbody></table></div>
      </details>
    </section>
  );
}
