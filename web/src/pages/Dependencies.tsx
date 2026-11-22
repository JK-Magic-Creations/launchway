import { useEffect, useMemo, useState } from "react";
import { bool, useUrlState } from "../urlState";
import { ErrorBox, Link, Mono } from "../components/ui";
import { PageHead, Sheet } from "../kit/layout";
import "../styles/deps.css";
import { depsApi, type DependencyReport, type DepUpdate, type Decision } from "../depsApi";
import { terms } from "../terms";
import { RepoTabs } from "../components/RepoTabs";
import { prose } from "../prose";
import { BtnSm, LabelField, PSub, ThCol, ThRow } from "../kit/bits";
const P = await prose("pages.Dependencies");

const STATUS_WORDS: Record<string, string> = {
  "up-to-date": "Up to date", update: "Update available", security: "Security fix available", held: P.t0,
  ignored: "Ignored", manual: "Needs a person", vulnerable: "Vulnerable, no fix yet", transitive: "Needed by another package", unknown: "Not checked",
};
const ACTION_WORDS: Record<string, string> = { open: "Would open", update: "Would update in place", keep: "Open, unchanged", queued: "Waiting", close: "Would close" };

function age(published?: string): string {
  if (!published) return "unknown";
  const days = Math.floor((Date.now() - Date.parse(published)) / 86_400_000);
  return days <= 0 ? "today" : days === 1 ? "1 day" : `${days} days`;
}

function Header({ path, current }: { path: string; current: "dependencies" | "updates" }) {
  return (
    <>
      <PageHead eyebrow={<Link href={`/repositories/${path}`}>{path}</Link>}
        title={current === "dependencies" ? "Dependencies" : terms().say("{Dependency_updatess}")}
        sentence={current === "dependencies"
          ? P.t1
          : P.t2} />
      <RepoTabs path={path} on="dependencies" />
      <nav className="tabs" aria-label="Dependency pages">
        <a href={`/repositories/${path}/dependencies`} className={"tab" + (current === "dependencies" ? " tab-on" : "")} aria-current={current === "dependencies" ? "page" : undefined}>Dependencies</a>
        <a href={`/repositories/${path}/updates`} className={"tab" + (current === "updates" ? " tab-on" : "")} aria-current={current === "updates" ? "page" : undefined}>Updates and settings</a>
      </nav>
    </>
  );
}

function useReport(path: string, preview: boolean) {
  const [report, setReport] = useState<DependencyReport | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    (preview ? depsApi.preview(path) : depsApi.report(path)).then((r) => { setReport(r); setError(null); }).catch(setError);
  }, [path, preview]);
  return { report, error };
}

/** The dependencies of one hosted repository, as a table with filters. */
export function DependenciesPage({ path }: { path: string }) {
  const { report, error } = useReport(path, false);
  // The filters are in the address (?eco=npm&status=outdated&q=lodash&direct=0&vulnerable=1).
  const [eco, setEco] = useUrlState("eco", "");
  const [status, setStatus] = useUrlState("status", "");
  const [text, setText] = useUrlState("q", "");
  const [direct, setDirect] = useUrlState("direct", true, bool);
  const [justVulnerable, setJustVulnerable] = useUrlState("vulnerable", false, bool);
  const prOf = useMemo(() => {
    const m = new Map<string, number[]>();
    for (const d of report?.decisions ?? []) if (d.number) for (const u of d.updates) m.set(u.dep.ecosystem + "|" + u.dep.name, [...(m.get(u.dep.ecosystem + "|" + u.dep.name) ?? []), d.number]);
    return m;
  }, [report]);
  const rows = useMemo(() => (report?.updates ?? []).filter((u) =>
    (!direct || u.dep.direct) && (!eco || u.dep.ecosystem === eco) && (!status || u.status === status) &&
    (!justVulnerable || (u.advisories?.length ?? 0) > 0) && (!text || u.dep.name.toLowerCase().includes(text.toLowerCase()))), [report, eco, status, text, direct, justVulnerable]);
  const ecosystems = useMemo(() => [...new Set((report?.updates ?? []).map((u) => u.dep.ecosystem))].sort(), [report]);
  const vulnerable = (report?.updates ?? []).filter((u) => (u.advisories?.length ?? 0) > 0).length;
  const behind = (report?.updates ?? []).filter((u) => u.dep.direct && (u.target || u.major)).length;
  return (
    <div className="page deps">
      <Header path={path} current="dependencies" />
      <ErrorBox error={error} />
      {!report && !error && <p className="faint" role="status">Reading the repository…</p>}
      {report && (
        <>
          <PSub role="status">
            {report.updates.filter((u) => u.dep.direct).length} direct dependencies in {report.files.length} files of <Mono>{report.branch}</Mono> at <Mono>{report.commit.slice(0, 7)}</Mono>:{" "}
            {behind} behind, {vulnerable} with a known vulnerability.
          </PSub>
          {report.vulnerability_error && <p className="errorbox" role="alert">{P.t3}{report.vulnerability_error}</p>}
          <Sheet title="Filter" pad>
            <form className="deps-filter" aria-label="Filter dependencies" onSubmit={(e) => e.preventDefault()}>
              <LabelField><span>Name contains</span><input value={text} onChange={(e) => setText(e.target.value)} /></LabelField>
              <LabelField><span>Ecosystem</span>
                <select value={eco} onChange={(e) => setEco(e.target.value)}><option value="">All</option>{ecosystems.map((x) => <option key={x} value={x}>{x}</option>)}</select></LabelField>
              <LabelField><span>Status</span>
                <select value={status} onChange={(e) => setStatus(e.target.value)}><option value="">All</option>{Object.entries(STATUS_WORDS).map(([k, v]) => <option key={k} value={k}>{v}</option>)}</select></LabelField>
              <label className="check"><input type="checkbox" checked={direct} onChange={(e) => setDirect(e.target.checked)} /> {P.t4}</label>
              <label className="check"><input type="checkbox" checked={justVulnerable} onChange={(e) => setJustVulnerable(e.target.checked)} /> {P.t5}</label>
            </form>
          </Sheet>
          <Sheet title={`Dependencies (${rows.length})`}>
            {rows.length === 0 ? <p className="faint pad">{P.t6}</p> : (
              <div className="deps-scroll"><table className="table deps-table">
                <caption className="sr-only">Dependencies of {path}</caption>
                <thead><tr><ThCol>Dependency</ThCol><ThCol>Ecosystem</ThCol><ThCol>In use</ThCol><ThCol>Newest</ThCol><ThCol>Proposed</ThCol><ThCol>Age of proposed</ThCol><ThCol>Known vulnerabilities</ThCol><ThCol>Pull request</ThCol><ThCol>Status</ThCol></tr></thead>
                <tbody>{rows.map((u) => <Row key={u.dep.ecosystem + u.dep.file + u.dep.name} u={u} path={path} pr={prOf.get(u.dep.ecosystem + "|" + u.dep.name)} />)}</tbody>
              </table></div>
            )}
          </Sheet>
          {report.notes && report.notes.length > 0 && (
            <Sheet title="Found, not updated" pad><ul>{report.notes.map((n) => <li key={n.file + n.message}><Mono>{n.file}</Mono>: {n.message}</li>)}</ul></Sheet>
          )}
          <PSub>Coming, not available yet: {report.coming.join(", ")}.</PSub>
        </>
      )}
    </div>
  );
}

function Row({ u, path, pr }: { u: DepUpdate; path: string; pr?: number[] }) {
  return (
    <tr>
      <ThRow><Mono>{u.dep.name}</Mono><br /><small className="faint">{u.dep.file}{u.dep.line ? `:${u.dep.line}` : ""}{u.dep.group ? `, ${u.dep.group}` : ""}</small></ThRow>
      <td>{u.dep.ecosystem}</td>
      <td><Mono>{u.dep.current || u.dep.constraint || "unknown"}</Mono></td>
      <td>{u.latest ? <Mono>{u.latest}</Mono> : "unknown"}</td>
      <td>{u.target ? <><Mono>{u.target}</Mono> <small>({u.kind})</small></> : "none"}{u.major && <><br /><small>major: <Mono>{u.major.target}</Mono>, its own pull request</small></>}</td>
      <td>{u.target ? age(u.published) : "—"}</td>
      <td>{(u.advisories?.length ?? 0) === 0 ? "none known" : <ul className="plain">{u.advisories!.map((a) => <li key={a.id}><strong>{a.severity}</strong> {a.id}{a.fixed_in ? `, fixed in ${a.fixed_in}` : ", no fix yet"}</li>)}</ul>}</td>
      <td>{pr && pr.length > 0 ? pr.map((n, i) => <span key={n}>{i > 0 && ", "}<Link href={`/repositories/${path}/pulls/${n}`}>#{n}</Link></span>) : "none open"}</td>
      <td>{STATUS_WORDS[u.status] ?? u.status}{(u.reason || u.note) && <><br /><small className="faint">{u.reason || u.note}</small></>}</td>
    </tr>
  );
}

/** The settings that apply, and a preview of what the updater would open now. */
export function UpdatesPage({ path }: { path: string }) {
  const [preview, setPreview] = useState(false);
  const { report, error } = useReport(path, preview);
  const p = report?.policy;
  return (
    <div className="page deps">
      <Header path={path} current="updates" />
      <ErrorBox error={error} />
      {!report && !error && <p className="faint" role="status">{P.t7}</p>}
      {report && p && (
        <>
          <Sheet title="Settings that apply" pad>
            <PSub>{P.t8}<Mono>.launchway/updates.yaml</Mono> {P.t9}</PSub>
            <dl className="kv">
              <dt>Updates</dt><dd>{p.enabled ? "on" : "off"}; security updates {p.security ? "on" : "off"}; routine updates {p.routine ? "on" : "off"}</dd>
              <dt>Schedule</dt><dd>{p.schedule.interval}{p.schedule.day ? ` on ${p.schedule.day}` : ""} at {String(p.schedule.hour).padStart(2, "0")}:00 {p.schedule.timezone ?? "UTC"}{P.t10}</dd>
              <dt>Wait for new releases</dt><dd>{p.cooldown_days} {P.t11}</dd>
              <dt>Limits</dt><dd>{p.limits.max_open} open at most, {p.limits.max_per_run} {terms().say("opened per {run}")}{p.limits.max_open_per_ecosystem ? `, ${p.limits.max_open_per_ecosystem} per ecosystem` : ""}</dd>
              <dt>Merging</dt><dd>{p.auto_merge.enabled ? P.t12 : "never automatic"}</dd>
              <dt>Ecosystems</dt><dd>{Object.entries(p.ecosystems).filter(([, v]) => v.enabled).map(([k]) => k).sort().join(", ")}</dd>
              {p.locked && Object.keys(p.locked).length > 0 && (<><dt>Locked</dt><dd>{Object.entries(p.locked).map(([k, v]) => `${k} (by the ${v} level)`).join(", ")}</dd></>)}
            </dl>
            {report.settings_file_error && <p className="errorbox" role="alert">{P.t13}{report.settings_file_error}</p>}
            {p.ignored && p.ignored.length > 0 && <ul>{p.ignored.map((i) => <li key={i}>Not applied: {i}</li>)}</ul>}
          </Sheet>
          <Sheet title="What would be opened" actions={<BtnSm onClick={() => setPreview(true)} disabled={preview}>{preview ? "Showing the preview" : "Preview what would be opened"}</BtnSm>} pad={report.decisions.length === 0}>
            {!preview && <p className="sub pad">{P.t14}</p>}
            {preview && report.decisions.length === 0 && <p className="faint">{P.t15}</p>}
            {preview && report.decisions.length > 0 && <Decisions decisions={report.decisions} />}
          </Sheet>
          <PSub>{terms().say(P.t16)}<Mono>launchway updates run</Mono>{P.t17}</PSub>
        </>
      )}
    </div>
  );
}

function Decisions({ decisions }: { decisions: Decision[] }) {
  return (
    <div className="deps-scroll"><table className="table deps-table">
      <caption className="sr-only">What the updater would do</caption>
      <thead><tr><ThCol>Pull request</ThCol><ThCol>What</ThCol><ThCol>Updates</ThCol><ThCol>Why</ThCol></tr></thead>
      <tbody>{decisions.map((d) => (
        <tr key={d.key}>
          <ThRow>{d.title || d.key}{d.security && <> <strong>security</strong></>}{d.number ? ` (#${d.number})` : ""}</ThRow>
          <td>{ACTION_WORDS[d.action] ?? d.action}</td>
          <td>{d.updates.map((u) => `${u.dep.name} ${u.dep.current || ""} → ${u.target ?? "?"}`).join("; ")}</td>
          <td>{d.why}</td>
        </tr>))}</tbody>
    </table></div>
  );
}
