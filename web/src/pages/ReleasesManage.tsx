import { useCallback, useState } from "react";
import { canOperate, useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { PromoteDialog, ReturnDialog } from "../components/ReleasePromote";
import { ReleasesNav } from "../components/ReleasesNav";
import { ErrorBox } from "../components/ui";
import { ago, clock } from "../format";
import { useLoad } from "../guided/useLoad";
import { Chip, type Tone } from "../kit/Status";
import { Button, EmptyState, PageHead, Sheet } from "../kit/layout";
import { useNow } from "../live";
import { onLink } from "../router";
import { useCrumbLabel } from "../nav/crumbs";
import { terms } from "../terms";
import { releasesApi, type Cell, type Formation, type Measures, type Report } from "../releasesApi";
import "../releases.css";

const relHref = (project: string, version: string) => `/deployments/releases/${encodeURIComponent(project)}/${encodeURIComponent(version)}`;
const HEALTH: Record<string, { tone?: Tone; word: string }> = { healthy: { tone: "ok", word: "healthy" }, failed: { tone: "bad", word: "failed" }, returned: { tone: "warn", word: "returned to base" } };

/** Release by Airfield: which version is where, since when, by whom and how it is, with what to promote next. */
export function ReleaseMatrixPage() {
  useCrumbLabel("Where each release is");
  const me = useMe();
  const now = useNow(15000);
  const data = useLoad(useCallback((b: boolean) => releasesApi.matrix(b), []), 10000);
  const [promote, setPromote] = useState<null | { project: string; version: string; airfield: string }>(null);
  const [back, setBack] = useState<null | { project: string; airfield: string; version?: string }>(null);
  const m = data.data;
  return (
    <div className="page">
      <PageHead eyebrow="Deployments · Releases" title="Where each release is"
        sentence="Each row is a project, each column an Airfield. What runs in an Airfield is what was last launched there; a release is what gets promoted from one to the next." />
      <ReleasesNav path="/deployments/releases/matrix" />
      <ErrorBox error={data.error} />
      {data.loading && !m && <div className="loading">Loading…</div>}
      {m && m.rows.length === 0 && <Sheet><EmptyState title="Nothing to show yet">Once a project has a release, or a pipeline has launched something into an Airfield, it appears here.</EmptyState></Sheet>}
      {m && m.rows.length > 0 && (
        <Sheet>
          <div className="rel-table-wrap">
            <table className="rel-table">
              <caption className="visually-hidden">Which release runs in which Airfield</caption>
              <thead><tr><th scope="col">Project</th>{m.airfields.map((a) => <th key={a} scope="col">{a}</th>)}</tr></thead>
              <tbody>
                {m.rows.map((row) => (
                  <tr key={row.project}>
                    <th scope="row" style={{ background: "transparent", textTransform: "none", letterSpacing: 0, fontSize: 14, color: "inherit" }}>
                      <EntityLink kind="project" name={row.project} />{row.latest && <div className="faint small">latest {row.latest}</div>}
                    </th>
                    {row.cells.map((c) => <td key={c.airfield}><CellView c={c} project={row.project} now={now} latest={row.latest}
                      mayPromote={!!m.can_promote.find((p) => p.project === row.project && p.airfield === c.airfield)}
                      mayReturn={canOperate(me, row.project)} onPromote={(version) => setPromote({ project: row.project, version, airfield: c.airfield })}
                      onReturn={() => setBack({ project: row.project, airfield: c.airfield, version: c.version })} /></td>)}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Sheet>
      )}
      {promote && <PromoteDialog {...promote} onClose={() => setPromote(null)} onDone={data.reload} />}
      {back && <ReturnDialog {...back} onClose={() => setBack(null)} onDone={data.reload} />}
    </div>
  );
}

function CellView({ c, project, now, latest, mayPromote, mayReturn, onPromote, onReturn }: { c: Cell; project: string; now: number; latest?: string; mayPromote: boolean; mayReturn: boolean; onPromote: (version: string) => void; onReturn: () => void }) {
  const h = HEALTH[c.health];
  if (!c.version && !c.launching) {
    return <div className="rel-cell"><span className="faint">nothing launched</span>{mayPromote && latest && <Button sm onClick={() => onPromote(latest)}>Promote {latest}<span className="visually-hidden"> to {c.airfield}</span></Button>}</div>;
  }
  return (
    <div className="rel-cell">
      {c.version && <span className="v">{c.release_id ? <a href={relHref(project, c.version)} onClick={onLink}>{c.version}</a> : c.version}{" "}{h && <Chip tone={h.tone}>{h.word}</Chip>}</span>}
      {c.version && <span className="m">since {ago(c.since ?? 0, now)} <span className="faint">({clock(c.since ?? 0)})</span>{c.by ? ` by ${c.by}` : ""}{c.run && <> · <EntityLink kind="run" name={`Sortie #${c.run_number}`} id={c.run} /></>}</span>}
      {!c.release_id && c.version && <span className="m">not a release of this project</span>}
      {c.launching && <span className="m"><Chip tone="run">launching {c.launching.version}</Chip> since {ago(c.launching.since, now)}</span>}
      <span style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
        {mayPromote && latest && c.version !== latest && !c.launching && <Button sm onClick={() => onPromote(latest)}>Promote {latest}<span className="visually-hidden"> to {c.airfield}</span></Button>}
        {mayReturn && c.version && !c.launching && <Button sm onClick={onReturn}>Return to base<span className="visually-hidden"> {project} in {c.airfield}</span></Button>}
      </span>
    </div>
  );
}

const kindWord: Record<string, string> = { planned: "Planned", in_progress: "In progress", done: "Done", freeze: "Freeze" };

/** The calendar: what is planned, what is going on, what was done, and the freezes. */
export function ReleaseCalendarPage() {
  useCrumbLabel("Calendar");
  const me = useMe();
  const now = useNow(30000);
  const data = useLoad(useCallback((b: boolean) => releasesApi.calendar({}, b), []), 15000);
  const freezes = useLoad(useCallback((b: boolean) => releasesApi.freezes(b), []), 15000);
  const [to, setTo] = useState("");
  const [reason, setReason] = useState("");
  const [project, setProject] = useState("");
  const [airfield, setAirfield] = useState("");
  const [error, setError] = useState<unknown>(null);
  const hold = terms().lower("hold");
  const projects = Object.keys(me?.roles ?? {}).filter((p) => me?.roles[p] === "admin");
  const mayFreezeAll = !!me?.admin;
  const make = async () => {
    setError(null);
    try { await releasesApi.freeze({ project: project || undefined, airfield: airfield || undefined, to: new Date(to).toISOString(), reason: reason.trim() }); setReason(""); setTo(""); data.reload(); freezes.reload(); } catch (e) { setError(e); }
  };
  const entries = data.data?.entries ?? [];
  return (
    <div className="page">
      <PageHead eyebrow="Deployments · Releases" title="Release calendar"
        sentence={`What is planned, in progress and done, and the periods when nothing is promoted (a ${hold}). A plan is a reminder: promoting stays a person's action.`} />
      <ReleasesNav path="/deployments/releases/calendar" />
      <ErrorBox error={data.error} />
      {data.loading && !data.data && <div className="loading">Loading…</div>}
      {data.data && entries.length === 0 && <Sheet><EmptyState title="Nothing on the calendar">Promotions and freezes from two weeks ago to a month ahead appear here.</EmptyState></Sheet>}
      {entries.length > 0 && (
        <Sheet title="Two weeks back to a month ahead">
          <ul className="rel-cal" aria-label="Calendar entries">
            {entries.map((e, i) => (
              <li key={i} className={e.kind === "freeze" ? "freeze" : ""}>
                <span className="when">{clock(e.at)}{e.end ? <> to {clock(e.end)}</> : null}</span>
                <span>{e.kind === "freeze" ? e.title : e.release ? <a href={relHref(e.project ?? "", e.version ?? "")} onClick={onLink}>{e.title}</a> : e.title}{e.by ? <span className="faint"> · {e.by}</span> : null}</span>
                <span><Chip tone={e.kind === "freeze" ? (e.state === "in force" ? "bad" : undefined) : e.kind === "in_progress" ? "run" : e.kind === "done" ? "ok" : "warn"}>{kindWord[e.kind]}{e.state && e.kind === "freeze" ? `: ${e.state}` : ""}</Chip></span>
              </li>
            ))}
          </ul>
          <p className="rel-hint" style={{ padding: "0 16px 12px" }}>Now is {clock(now)}.</p>
        </Sheet>
      )}
      {(mayFreezeAll || projects.length > 0) && (
        <Sheet title={`Start a ${hold}`}>
          <form className="rel-inline" onSubmit={(e) => { e.preventDefault(); if (to && reason.trim()) make(); }}>
            <label>For<select value={project} onChange={(e) => setProject(e.target.value)}>{mayFreezeAll && <option value="">Every project</option>}{projects.map((p) => <option key={p}>{p}</option>)}</select></label>
            <label>In<input type="text" value={airfield} onChange={(e) => setAirfield(e.target.value)} placeholder="every Airfield" /></label>
            <label>Until<input type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} required /></label>
            <label>Why<input type="text" value={reason} onChange={(e) => setReason(e.target.value)} required maxLength={500} /></label>
            <Button kind="pri" type="submit" disabled={!to || !reason.trim()}>Start the {hold}</Button>
          </form>
          <p className="rel-hint" style={{ padding: "0 16px 12px" }}>In a {hold} nothing is promoted unless an administrator of the project overrides, with a reason that is kept in the {terms().lower("logbook")}.</p>
          <ErrorBox error={error} />
        </Sheet>
      )}
      {(freezes.data ?? []).filter((f) => !f.canceled && f.to > now).length > 0 && (
        <Sheet title={`Current and coming ${terms().plural("hold").toLowerCase()}`}>
          <ul className="rowlist">{(freezes.data ?? []).filter((f) => !f.canceled && f.to > now).map((f) => (
            <li key={f.id}><div className="main"><div className="nm">{f.project || "Every project"} · {f.airfield || "every Airfield"}</div><div className="sb">{f.reason} · until {clock(f.to)}</div></div>
              {(f.project ? me?.roles[f.project] === "admin" || me?.admin : me?.admin) && <div className="cell"><Button sm onClick={async () => { try { await releasesApi.cancelFreeze(f.id); freezes.reload(); data.reload(); } catch (e) { setError(e); } }}>End it<span className="visually-hidden"> ({f.reason})</span></Button></div>}</li>))}</ul>
        </Sheet>
      )}
    </div>
  );
}

const MEMBER: Record<string, { tone?: Tone; word: string }> = { waiting: { word: "waiting" }, started: { tone: "run", word: "launching" }, launched: { tone: "ok", word: "launched" }, failed: { tone: "bad", word: "failed" }, not_started: { tone: "warn", word: "not started" } };

/** Formations: releases of several projects promoted together in an explicit order. */
export function ReleaseFormationsPage() {
  useCrumbLabel("Formations");
  const me = useMe();
  const data = useLoad<Formation[]>(useCallback((b: boolean) => releasesApi.formations(b), []), 5000);
  const mx = useLoad(useCallback((b: boolean) => releasesApi.matrix(b), []), 0);
  const [name, setName] = useState("");
  const [members, setMembers] = useState("");
  const [byDeps, setByDeps] = useState(false);
  const [airfield, setAirfield] = useState<Record<string, string>>({});
  const [error, setError] = useState<unknown>(null);
  const any = Object.values(me?.roles ?? {}).some((r) => r === "operator" || r === "admin");
  const make = async () => {
    setError(null);
    try { await releasesApi.createFormation(name.trim(), "", members.split(/[\s,]+/).filter(Boolean), byDeps ? "dependencies" : undefined); setName(""); setMembers(""); data.reload(); } catch (e) { setError(e); }
  };
  return (
    <div className="page">
      <PageHead eyebrow="Deployments · Releases" title="Formations"
        sentence="A Formation groups releases of several projects that go out together, in an order you write, or the order of their dependencies. Nothing starts unless every release passes the Airfield's checklist; then they are launched one after another, and the report says which launched, which failed and which were not started." />
      <ReleasesNav path="/deployments/releases/formations" />
      <ErrorBox error={data.error} />
      {data.loading && !data.data && <div className="loading">Loading…</div>}
      {data.data && data.data.length === 0 && <Sheet><EmptyState title="No formations yet">Write one below: the releases, in the order they should go out. The order is yours, or tick "Order by dependencies" and the software graph puts what the others depend on first.</EmptyState></Sheet>}
      {(data.data ?? []).map((f) => (
        <Sheet key={f.id} title={<>{f.name} <Chip tone={f.state === "done" ? "ok" : f.state === "partial" || f.state === "failed" ? "bad" : f.state === "promoting" ? "run" : undefined}>{f.state}</Chip></>}>
          <ol className="rowlist" style={{ listStyle: "decimal inside" }} aria-label={`Members of ${f.name}, in order`}>
            {f.members.map((m) => (
              <li key={m.release_id}><div className="main rel-member"><a href={relHref(m.project, m.version)} onClick={onLink}>{m.project} {m.version}</a><Chip tone={MEMBER[m.state]?.tone}>{MEMBER[m.state]?.word ?? m.state}</Chip>{m.detail && <span className="faint">{m.detail}</span>}</div></li>
            ))}
          </ol>
          {f.order_source === "dependencies" && <p className="faint small">Ordered by dependencies: the software graph put what the others depend on first.</p>}
          {f.state === "planned" && any && (
            <div className="rel-inline">
              <label>Promote to<select value={airfield[f.id] ?? ""} onChange={(e) => setAirfield({ ...airfield, [f.id]: e.target.value })}><option value="">Choose an Airfield</option>{(mx.data?.airfields ?? []).map((a) => <option key={a}>{a}</option>)}</select></label>
              <Button kind="pri" disabled={!airfield[f.id]} onClick={async () => { setError(null); try { await releasesApi.promoteFormation(f.name, airfield[f.id]); data.reload(); } catch (e) { setError(e); } }}>Promote the formation</Button>
            </div>
          )}
        </Sheet>
      ))}
      {any && (
        <Sheet title="Make a formation">
          <form className="rel-inline" onSubmit={(e) => { e.preventDefault(); if (name.trim() && members.trim()) make(); }}>
            <label>Name<input type="text" value={name} onChange={(e) => setName(e.target.value)} required maxLength={64} /></label>
            <label>Releases, in order, as project@version<input type="text" value={members} onChange={(e) => setMembers(e.target.value)} required placeholder="api@2.0.0 web@5.1.0" style={{ minWidth: 280 }} /></label>
            <label><input type="checkbox" checked={byDeps} onChange={(e) => setByDeps(e.target.checked)} /> Order by dependencies (the software graph puts what others depend on first)</label>
            <Button kind="pri" type="submit" disabled={!name.trim() || !members.trim()}>Make the formation</Button>
          </form>
          <ErrorBox error={error} />
        </Sheet>
      )}
    </div>
  );
}

const hours = (s: number) => (s <= 0 ? "none" : s < 5400 ? `${Math.round(s / 60)} min` : s < 48 * 3600 ? `${(s / 3600).toFixed(1)} h` : `${(s / 86400).toFixed(1)} days`);
const pct = (x: number) => `${Math.round(x * 100)}%`;
const day = (ms: number) => new Date(ms).toISOString().slice(0, 10);

/** What shipped and how delivery went: the four measures, a chart of launches with its table, and each release's journey. */
export function ReleaseReportPage() {
  useCrumbLabel("Report");
  const [project, setProject] = useState("");
  const [days, setDays] = useState("90");
  const [t0] = useState(() => Date.now());
  const from = new Date(t0 - Number(days) * 86400000).toISOString();
  const fn = useCallback((b: boolean) => releasesApi.report({ project: project || undefined, from }, b), [project, from]);
  const data = useLoad<Report>(fn, 60000);
  const list = useLoad(useCallback((b: boolean) => releasesApi.list({}, b), []), 0);
  const projects = [...new Set((list.data ?? []).map((r) => r.project))];
  const rep = data.data;
  return (
    <div className="page">
      <PageHead eyebrow="Deployments · Releases" title="Release report" sentence="What shipped, when it reached each Airfield, and the four measures of delivery worked out from what Launchway already records." />
      <ReleasesNav path="/deployments/releases/report" />
      <div className="toolbar2">
        <label className="select"><span className="visually-hidden">Project</span><select value={project} onChange={(e) => setProject(e.target.value)}><option value="">Every project</option>{projects.map((p) => <option key={p}>{p}</option>)}</select></label>
        <label className="select"><span className="visually-hidden">Period</span><select value={days} onChange={(e) => setDays(e.target.value)}><option value="30">Last 30 days</option><option value="90">Last 90 days</option><option value="365">Last year</option></select></label>
        <a className="btn" href={releasesApi.reportUrl({ project: project || undefined, from, format: "html" })}>Export as HTML</a>
        <a className="btn" href={releasesApi.reportUrl({ project: project || undefined, from })} target="_blank" rel="noreferrer">Open the JSON</a>
      </div>
      <ErrorBox error={data.error} />
      {data.loading && !rep && <div className="loading">Loading…</div>}
      {rep && <ReportBody rep={rep} />}
    </div>
  );
}

function ReportBody({ rep }: { rep: Report }) {
  const m = rep.measures;
  const series = rep.series;
  const max = Math.max(1, ...series.map((s) => s.launches));
  const w = 640, h = 130, bw = Math.max(4, Math.floor((w - 30) / Math.max(1, series.length)) - 3);
  return (
    <>
      <Sheet title="The four delivery measures">
        <div className="rel-measures">
          <div><div className="k">Frequency</div><div className="v">{m.frequency_per_week.toFixed(2)}</div><div className="f">successful launches a week; {m.launches} launches in all</div></div>
          <div><div className="k">Lead time (median)</div><div className="v">{hours(m.lead_time_median_s)}</div><div className="f">from the commit to the Airfield, over {m.lead_time_count} launches</div></div>
          <div><div className="k">Change failure rate</div><div className="v">{pct(m.change_failure_rate)}</div><div className="f">{m.failures} of {m.launches} launches failed or were returned to base</div></div>
          <div><div className="k">Time to restore (median)</div><div className="v">{hours(m.time_to_restore_median_s)}</div><div className="f">{m.restore_count} restored, {m.unrestored} not yet</div></div>
        </div>
      </Sheet>
      {series.length > 0 && (
        <Sheet title="Launches over time">
          <div className="rel-chart">
            <svg viewBox={`0 0 ${w} ${h + 22}`} role="img" aria-label={`Launches per period; the same numbers are in the table below. The busiest period had ${max}.`}>
              <line className="axis" x1="10" x2={w - 10} y1={h} y2={h} />
              {series.map((s, i) => {
                const ok = ((s.launches - s.failures) * h) / max, bad = (s.failures * h) / max, x = 10 + i * (bw + 3);
                return <g key={i}><rect className="ok-bar" x={x} y={h - ok} width={bw} height={ok} /><rect className="bad-bar" x={x} y={h - ok - bad} width={bw} height={bad} /></g>;
              })}
              <text x="10" y={h + 16}>{day(rep.from)}</text><text x={w - 10} y={h + 16} textAnchor="end">{day(rep.to)}</text>
            </svg>
            <p className="rel-hint">Launches that succeeded in the accent color; launches that failed or were returned to base in red.</p>
          </div>
          <div className="rel-table-wrap"><table className="rel-table">
            <caption>The same numbers as a table</caption>
            <thead><tr><th scope="col">From</th><th scope="col" className="n">Launches</th><th scope="col" className="n">Failed</th><th scope="col" className="n">Median lead time</th></tr></thead>
            <tbody>{series.map((s: Measures, i) => <tr key={i}><td>{day(rep.from + i * rep.step_seconds * 1000)}</td><td className="n">{s.launches}</td><td className="n">{s.failures}</td><td className="n">{hours(s.lead_time_median_s)}</td></tr>)}</tbody></table></div>
        </Sheet>
      )}
      {Object.keys(rep.by_airfield).length > 0 && (
        <Sheet title="By Airfield">
          <div className="rel-table-wrap"><table className="rel-table">
            <caption className="visually-hidden">Measures by Airfield</caption>
            <thead><tr><th scope="col">Airfield</th><th scope="col" className="n">Launches</th><th scope="col" className="n">Failed</th><th scope="col" className="n">Failure rate</th><th scope="col" className="n">Median lead time</th><th scope="col" className="n">Median time to restore</th></tr></thead>
            <tbody>{Object.entries(rep.by_airfield).sort().map(([k, x]) => <tr key={k}><td>{k}</td><td className="n">{x.launches}</td><td className="n">{x.failures}</td><td className="n">{pct(x.change_failure_rate)}</td><td className="n">{hours(x.lead_time_median_s)}</td><td className="n">{hours(x.time_to_restore_median_s)}</td></tr>)}</tbody></table></div>
        </Sheet>
      )}
      <Sheet title="What shipped">
        {rep.shipped.length === 0 ? <p className="rel-hint" style={{ padding: "14px 16px" }}>No release was published in this period.</p> : (
          <div className="rel-table-wrap"><table className="rel-table">
            <caption className="visually-hidden">Releases published in the period and when each reached each Airfield</caption>
            <thead><tr><th scope="col">Release</th><th scope="col">Published</th><th scope="col">Reached</th><th scope="col" className="n">Failures</th><th scope="col" className="n">Returns</th></tr></thead>
            <tbody>{rep.shipped.map((s) => (
              <tr key={s.release.id}><td><a href={relHref(s.release.project, s.release.version)} onClick={onLink}>{s.release.project} {s.release.version}</a></td><td>{day(s.release.published_at ?? 0)} by {s.release.published_by}</td>
                <td>{s.reached.length === 0 ? "no Airfield yet" : s.reached.map((a) => `${a.airfield} ${day(a.at)}${a.lead_seconds ? ` (lead ${hours(a.lead_seconds)})` : ""}`).join("; ")}</td><td className="n">{s.failures}</td><td className="n">{s.returns}</td></tr>))}</tbody></table></div>
        )}
      </Sheet>
    </>
  );
}
