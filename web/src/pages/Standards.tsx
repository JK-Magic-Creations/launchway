import { useCallback, useEffect, useMemo, useState } from "react";
import { ProjectHead } from "./PipelineChrome";
import { canOperate, useMe } from "../auth";
import { useNow } from "../live";
import { FindingCard, fromTracked, useProfile } from "../components/Inspection";
import { SEVERITIES, severityLabel } from "../components/Security";
import { Empty, ErrorBox, Link } from "../components/ui";
import { PageHead } from "../kit/layout";
import {
  standardsApi, type BacklogList, type Coverage, type Effective, type ProjectStandards, type EffectiveCheck, type LevelSettings, type Series, type Settings, type Sev, type Source, type State, type Value,
} from "../standardsApi";
import { terms } from "../terms";
import "../standards.css";

type ProjectStandings = ProjectStandards;

function words(level: string | undefined, scope: string | undefined, w: Record<string, string>): string {
  if (!level) return "the default";
  if (level === "file") return "the repository's own file";
  const name = w[level] ?? level;
  return scope && level !== "installation" && level !== "organization" ? `the ${name} ${scope}` : `the ${name}`;
}

function sourceText(s: Source, w: Record<string, string>): string {
  return s.kind === "default" ? "the default" : words(s.level, s.scope, w);
}

function LockNote({ v, w }: { v: Value<unknown>; w: Record<string, string> }) {
  if (!v.locked) return null;
  return <span className="st-lock"><span aria-hidden="true">🔒</span> Locked by {words(v.locked.level, v.locked.scope, w)}: {v.locked.reason}</span>;
}

const STATE_WORD: Record<State, string> = { off: "Off", advise: "Advises", block: "Blocks" };

/** Where each value comes from, and what it overrides. */
function Chain({ v, w }: { v: Value<unknown>; w: Record<string, string> }) {
  if (!v.chain || v.chain.length < 2) return null;
  return (
    <details className="st-chain">
      <summary>Where this comes from</summary>
      <ul>
        {v.chain.map((l, i) => (
          <li key={i} className={l.ignored ? "st-ignored" : l.overridden ? "st-over" : "st-won"}>
            {sourceText({ ...l, kind: l.kind as Source["kind"] }, w)}: <strong>{l.display}</strong>
            {l.ignored ? <> — does not count: {l.ignored}</> : l.overridden ? <> — overridden</> : null}
          </li>
        ))}
      </ul>
    </details>
  );
}

function CheckRow({ c, w }: { c: EffectiveCheck; w: Record<string, string> }) {
  return (
    <tr className={c.active ? "" : "st-dim"}>
      <th scope="row">
        <span className="st-ct">{c.title}</span>
        <span className="st-why">{c.why}</span>
        <span className="faint small">Tool {c.tool} ({c.license}); up to {Math.round(c.budget_seconds / 60)} minutes{c.network ? "; " + c.network : "; needs no network"}</span>
      </th>
      {c.unavailable ? (
        // An unavailable check has no state to show and nothing to set: only the plain fact.
        <><td><span className="faint">Not available yet</span></td><td><span className="faint">–</span></td></>
      ) : (
        <>
          <td>
            <span className={"st-state st-state-" + c.state.value}>{STATE_WORD[c.state.value]}</span>
            {c.gates && c.state.value !== "block" && c.applies ? <span className="faint small"> can block</span> : null}
            <LockNote v={c.state} w={w} />
            <Chain v={c.state} w={w} />
          </td>
          <td>{sourceText(c.state.from, w)}</td>
        </>
      )}
      <td>{c.active ? <span className="st-on">{terms().say("Runs on every {run}")}</span> : <span className="faint">{c.reason}</span>}{c.applies && <div className="faint small">Because {c.because}</div>}</td>
    </tr>
  );
}

function Gate({ e, w }: { e: Effective; w: Record<string, string> }) {
  const rows: { label: string; v: Value<unknown>; text: string; hint: string }[] = [
    { label: "A change is blocked at", v: e.fail_on, text: `${severityLabel(e.fail_on.value)} and above`, hint: "Only for checks set to block, and only for findings the change introduces." },
    { label: "A vulnerability needs a fix to block", v: e.require_fix, text: e.require_fix.value ? "yes" : "no", hint: "Where no fixed version exists yet, it advises." },
    { label: "Findings on the default branch", v: e.default_branch, text: e.default_branch.value === "block" ? terms().say("fail the {run}") : "are shown, never fail it", hint: "The backlog is yours to work down; nothing old blocks a change." },
    { label: "A check that did not run", v: e.incomplete, text: e.incomplete.value === "fail" ? "fails the verdict" : "is shown, and the verdict stands", hint: "It is never taken for a clean result." },
    { label: "Licenses denied", v: e.licenses_deny, text: e.licenses_deny.value.join(", ") || "none", hint: "A dependency under one of these is a finding." },
    { label: "Licenses allowed", v: e.licenses_allow, text: e.licenses_allow.value.length ? e.licenses_allow.value.join(", ") : "any not denied", hint: "With a list, a license outside it is flagged; one that cannot be identified is always flagged." },
  ];
  return (
    <table className="table st-gate">
      <caption className="visually-hidden">The gates and license lists in effect</caption>
      <thead><tr><th scope="col">Setting</th><th scope="col">Value</th><th scope="col">From</th></tr></thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.label}>
            <th scope="row">{r.label}<span className="faint small st-hint">{r.hint}</span></th>
            <td><strong>{r.text}</strong><LockNote v={r.v} w={w} /><Chain v={r.v} w={w} /></td>
            <td>{sourceText(r.v.from, w)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** The names of the levels as this installation reads them. */
function levelWords(): Record<string, string> {
  const t = terms();
  return { installation: t.label("installation"), organization: t.label("organization"), unit: t.label("unit"), project: t.label("project") };
}

/** The open findings by day as a line, with the same numbers in a table. */
function History({ s }: { s: Series }) {
  const pts = s.points;
  if (pts.length < 2) return <p className="faint">The history starts when the default branch has been inspected on two different days.</p>;
  const max = Math.max(1, ...pts.map((p) => p.total));
  const W2 = 560, H = 120, pad = 4;
  const x = (i: number) => pad + (i * (W2 - 2 * pad)) / (pts.length - 1);
  const y = (n: number) => H - pad - (n * (H - 2 * pad)) / max;
  const line = (f: (p: (typeof pts)[number]) => number) => pts.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)} ${y(f(p)).toFixed(1)}`).join(" ");
  return (
    <figure className="st-hist">
      <svg viewBox={`0 0 ${W2} ${H}`} role="img" aria-label={`Open findings from ${pts[0].day} to ${pts[pts.length - 1].day}: ${pts[0].total} at first, ${pts[pts.length - 1].total} now`} preserveAspectRatio="none">
        <path d={line((p) => p.total)} className="st-line st-line-total" fill="none" />
        <path d={line((p) => p.critical + p.high)} className="st-line st-line-high" fill="none" strokeDasharray="4 3" />
      </svg>
      <figcaption className="faint small"><span className="st-key st-key-total" /> all open findings <span className="st-key st-key-high" /> critical and high. Highest {max}.</figcaption>
      <details>
        <summary>The same as a table</summary>
        <div className="table-scroll"><table className="table">
          <thead><tr><th scope="col">Day</th><th scope="col" className="num">Open</th><th scope="col" className="num">Critical</th><th scope="col" className="num">High</th><th scope="col" className="num">Medium</th><th scope="col" className="num">Low</th></tr></thead>
          <tbody>{[...pts].reverse().slice(0, 30).map((p) => <tr key={p.day}><th scope="row">{p.day}</th><td className="num">{p.total}</td><td className="num">{p.critical}</td><td className="num">{p.high}</td><td className="num">{p.medium}</td><td className="num">{p.low}</td></tr>)}</tbody>
        </table></div>
      </details>
    </figure>
  );
}

// ProjectStandardsPage is what runs on a repository and why, how to change
// it, and the backlog of what the checks found.
export function ProjectStandardsPage({ name }: { name: string }) {
  const [data, setData] = useState<ProjectStandings | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [list, setList] = useState<BacklogList | null>(null);
  const [series, setSeries] = useState<Series | null>(null);
  const [status, setStatus] = useState("open");
  const [sev, setSev] = useState<Sev | "">("");
  const [check, setCheck] = useState("");
  const [oldest, setOldest] = useState(true);
  const [file, setFile] = useState<{ from_settings: string } | null>(null);
  const [tick, setTick] = useState(0);
  const profile = useProfile();
  const me = useMe();
  const now = useNow(60000);
  const reload = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => { let on = true; standardsApi.project(name).then((d) => on && setData(d)).catch((e) => on && setError(e)); return () => { on = false; }; }, [name, tick]);
  useEffect(() => {
    let on = true;
    standardsApi.backlog(name, { status, severity: sev, check, sort: oldest ? "oldest" : "", limit: 100 }).then((l) => on && setList(l)).catch((e) => on && setError(e));
    return () => { on = false; };
  }, [name, status, sev, check, oldest, tick]);
  useEffect(() => { let on = true; standardsApi.history(name).then((s) => on && setSeries(s)).catch(() => undefined); return () => { on = false; }; }, [name, tick]);

  const e = data?.effective;
  const w = useMemo(() => profile?.words ?? levelWords(), [profile]);
  const checksByEco = useMemo(() => {
    const out = new Map<string, EffectiveCheck[]>();
    for (const c of e?.checks ?? []) out.set(c.ecosystem, [...(out.get(c.ecosystem) ?? []), c]);
    return out;
  }, [e]);
  const enc = encodeURIComponent(name);
  return (
    <div className="page page-wide sto">
      <ProjectHead name={name} on="standards" title={<>Standing orders of {name}</>}
        sentence={<>The checks every repository gets, on by default: what runs on <strong>{name}</strong>, why, and how to change it. Nothing here needs a change to the repository.</>} />
      <ErrorBox error={error} />
      {!data && !error && <div className="loading">Loading…</div>}
      {e && e.off.value && (
        <div className="callout callout-draft st-off" role="status">
          <strong>Standing orders are switched off</strong> by {sourceText(e.off.from, w)}{e.off_reason ? <>: “{e.off_reason}”</> : null}. No check runs on this repository and no finding is recorded.
          {" "}Whoever administers that level switches them back on in <Link href="/settings/standards">Settings, Standing orders</Link>.
        </div>
      )}
      {e && e.notices.length > 0 && (
        <div className="callout callout-info" role="status">
          <strong>Worth knowing</strong>
          <ul>{e.notices.map((n) => <li key={n}>{n}</li>)}</ul>
        </div>
      )}
      {data?.empty && <p className="faint">The repository has no commit on its default branch yet, so only what applies to every repository is shown.</p>}

      {e && (
        <>
          <section className="card" aria-labelledby="st-found">
            <h2 id="st-found" className="h-sm">What was found in the repository</h2>
            <ul className="chips st-eco">{e.detected.map((d) => <li key={d.ecosystem} className="chip">{d.title}<span className="visually-hidden"> ({d.because})</span></li>)}</ul>
            <p className="faint small">The profile (version {e.profile_version}) adds the checks that fit what is in the repository. Open a check to see where its setting comes from.</p>
          </section>

          <section aria-labelledby="st-checks">
            <h2 id="st-checks" className="h-sm st-h">Checks</h2>
            <div className="card card-flush table-scroll">
              <table className="table st-table">
                <caption className="visually-hidden">The checks of the profile and how they apply to this repository</caption>
                <thead><tr><th scope="col">Check</th><th scope="col">State</th><th scope="col">Set by</th><th scope="col">On this repository</th></tr></thead>
                {[...checksByEco.entries()].map(([eco, cs]) => (
                  <tbody key={eco} className="st-group">
                    <tr><th colSpan={4} scope="colgroup" className="st-eco-h">{profile?.ecosystems.find((x) => x.id === eco)?.title ?? eco}</th></tr>
                    {cs.map((c) => <CheckRow key={c.id} c={c} w={w} />)}
                  </tbody>
                ))}
              </table>
            </div>
          </section>

          <section aria-labelledby="st-gates">
            <h2 id="st-gates" className="h-sm st-h">Gates and licenses</h2>
            <div className="card card-flush table-scroll"><Gate e={e} w={w} /></div>
            {e.exemptions.length > 0 && (
              <>
                <h3 className="h-xs st-h">Exemptions in force</h3>
                <ul className="st-ex">
                  {e.exemptions.map((x, i) => (
                    <li key={i} className={x.expired ? "st-ignored" : ""}>
                      <strong>{[x.check && "check " + x.check, x.rule && "rule " + x.rule, x.package && "package " + x.package, x.path && x.path].filter(Boolean).join(", ")}</strong> until {x.expires}{x.expired ? " (ended)" : ""} — {x.reason}
                      <span className="faint small"> From {sourceText(x.from, w)}.</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </section>

          <section className="card" aria-labelledby="st-file">
            <h2 id="st-file" className="h-sm">The repository’s own settings</h2>
            <p>
              {data?.file ? <>The repository has <span className="mono">.launchway/standards.yaml</span>; it is read from the default branch only, so a pull request cannot switch its own checks off.</>
                : <>The repository has no <span className="mono">.launchway/standards.yaml</span>. A team can keep its choices in Git there: checks on or off, thresholds, license lists and exemptions with a reason and an end date. What a level above locked is shown as locked and cannot be weakened.</>}
            </p>
            {data?.file_problems && data.file_problems.length > 0 && (
              <div className="callout callout-draft" role="alert"><strong>The file has problems</strong>, so what they name is left out until they are fixed:<ul>{data.file_problems.map((p) => <li key={p}>{p}</li>)}</ul></div>
            )}
            <button type="button" className="btn btn-sm" onClick={() => standardsApi.file(name).then(setFile).catch(setError)}>Show this project’s settings as a file</button>
            {file && <textarea className="code st-file" readOnly rows={Math.min(24, file.from_settings.split("\n").length + 1)} aria-label="This project’s settings as a repository file" value={file.from_settings} />}
          </section>
        </>
      )}

      <section aria-labelledby="st-back">
        <h2 id="st-back" className="h-sm st-h">Findings on the default branch</h2>
        {series && list && (
          <div className="st-stats">
            <div><span className="st-n">{list.open}</span><span>open</span></div>
            {SEVERITIES.filter((s) => s !== "unknown").map((s) => <div key={s}><span className={"st-n sec-" + s}>{list.by_severity[s] ?? 0}</span><span>{severityLabel(s)}</span></div>)}
            <div><span className="st-n">{series.fixed_count ? series.mean_days_to_fix.toFixed(1) : "—"}</span><span>mean days to fix{series.fixed_count ? ` (${series.fixed_count} fixed)` : ""}</span></div>
            <div><span className="st-n">{series.oldest_open ? Math.max(0, Math.round((now - series.oldest_open) / 86400000)) : "—"}</span><span>days, oldest open</span></div>
          </div>
        )}
        {series && <History s={series} />}
        {list && Object.keys(list.by_check).length > 0 && (
          <div className="chips" role="group" aria-label="Show findings of a check">
            <button type="button" className={"chip" + (check === "" ? " chip-on" : "")} aria-pressed={check === ""} onClick={() => setCheck("")}>All checks</button>
            {Object.entries(list.by_check).map(([c, n]) => <button key={c} type="button" className={"chip" + (check === c ? " chip-on" : "")} aria-pressed={check === c} onClick={() => setCheck(c)}>{c || "other"} ({n})</button>)}
          </div>
        )}
        <div className="toolbar">
          <label>Show <select value={status} onChange={(ev) => setStatus(ev.target.value)}><option value="open">open</option><option value="accepted">accepted</option><option value="fixed">fixed</option><option value="all">all</option></select></label>
          <label>Severity <select value={sev} onChange={(ev) => setSev(ev.target.value as Sev | "")}><option value="">any</option>{SEVERITIES.map((s) => <option key={s} value={s}>{severityLabel(s)}</option>)}</select></label>
          <label><input type="checkbox" checked={oldest} onChange={(ev) => setOldest(ev.target.checked)} /> Oldest first</label>
          <a className="btn btn-sm" href={`/api/v1/projects/${enc}/findings/export?format=csv&status=${status}`}>CSV</a>
          <a className="btn btn-sm" href={`/api/v1/projects/${enc}/findings/export?format=json&status=${status}`}>JSON</a>
          <a className="btn btn-sm" href={`/api/v1/projects/${enc}/findings/export?format=sarif&status=${status}`}>SARIF</a>
        </div>
        {list && list.findings.length === 0 && (
          <Empty title={status === "open" ? "Nothing open" : "Nothing here"}>
            {series && series.points.length === 0
              ? <>The default branch has not been inspected yet. The first run on it records what it has; after that, a change is blocked only by what it adds.</>
              : <>No findings match.</>}
          </Empty>
        )}
        {list && list.findings.length > 0 && (
          <ul className="sf-list">{list.findings.map((f) => <FindingCard key={f.fingerprint} f={fromTracked(f)} project={name} profile={profile} onChanged={reload} />)}</ul>
        )}
        {list && list.matched > list.findings.length && <p className="faint small">{list.findings.length} of {list.matched} shown; narrow it with the filters or export all.</p>}
        {!canOperate(me, name) && <p className="faint small">You can read these. Accepting a risk needs the operator role on this project.</p>}
        <p className="faint small">When the pipeline is a run on the default branch, the backlog updates; <Link href={`/projects/${enc}/security`}>the Security page</Link> {terms().say("shows every {run}’s findings, including the tools you added yourself.")}</p>
      </section>
    </div>
  );
}

// ---- the level pages ----

const LEVELS = ["installation", "organization", "unit", "project"] as const;

const STATE_OPTIONS: State[] = ["off", "advise", "block"];

/** SettingsStandardsPage changes a level’s Standing orders and shows which repositories have which checks. */
export function SettingsStandardsPage() {
  const profile = useProfile();
  const [level, setLevel] = useState<(typeof LEVELS)[number]>("installation");
  const [scope, setScope] = useState("");
  const [cov, setCov] = useState<Coverage | null>(null);
  const [cur, setCur] = useState<LevelSettings | null>(null);
  const [draft, setDraft] = useState<Settings>({});
  const [error, setError] = useState<unknown>(null);
  const [said, setSaid] = useState("");
  const [busy, setBusy] = useState(false);
  const need = level === "unit" || level === "project";
  const ready = !need || scope !== "";
  const effScope = need ? scope : level === "organization" ? "default" : "";
  const w = profile?.words ?? levelWords();

  useEffect(() => {
    if (need && !scope) return;
    let on = true;
    standardsApi.level(level, effScope).then((l) => { if (on) { setCur(l); setDraft(l.settings); setSaid(""); setError(null); } }).catch((e) => on && setError(e));
    standardsApi.coverage(level, effScope).then((c) => on && setCov(c)).catch(() => undefined);
    return () => { on = false; };
  }, [level, effScope, need, scope]);

  const set = (f: (s: Settings) => Settings) => setDraft((d) => f(structuredClone(d)));
  const lock = (key: string, on: boolean) => set((s) => { s.locks = { ...(s.locks ?? {}) }; if (on) s.locks[key] = s.locks[key] || "Required by " + (w[level] ?? level); else delete s.locks[key]; return s; });
  const save = async () => {
    setBusy(true); setError(null); setSaid("");
    try {
      const r = await standardsApi.setLevel(level, effScope, draft, cur?.version ?? 0);
      setSaid(`Saved as version ${r.version}. Runs made from now on use these settings.`);
      const l = await standardsApi.level(level, effScope);
      setCur(l); setDraft(l.settings);
      standardsApi.coverage(level, effScope).then(setCov).catch(() => undefined);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const off = !!draft.off;
  const lockOf = (key: string) => draft.locks?.[key] !== undefined;
  return (
    <div className="page page-wide sto">
      <PageHead eyebrow="Settings" title="Standing orders" sentence="The same checks for every repository, on by default. Choose a level to see which repositories have which checks, and to change what the level says. A level can lock a value so nothing below it weakens it." />
      <div className="toolbar st-level" role="group" aria-label="Level">
        {LEVELS.map((l) => <button key={l} type="button" className={"chip" + (level === l ? " chip-on" : "")} aria-pressed={level === l} onClick={() => { setLevel(l); setScope(""); }}>{w[l] ?? l}</button>)}
        {need && <label>Name of the {(w[level] ?? level).toLowerCase()} <input value={scope} onChange={(e) => setScope(e.target.value.trim())} placeholder={level === "unit" ? "squadron name" : "project name"} /></label>}
      </div>
      <ErrorBox error={error} />
      {said && <div className="callout callout-info" role="status">{said}</div>}
      {need && !scope && <p className="faint">Name the {(w[level] ?? level).toLowerCase()} to see and change its settings.</p>}

      {cov && ready && (
        <section aria-labelledby="st-cov">
          <h2 id="st-cov" className="h-sm st-h">Which repositories have which checks</h2>
          {cov.any_off && <div className="callout callout-draft" role="status"><strong>Standing orders are switched off for some repositories below.</strong> They are listed as off in the table.</div>}
          {cov.projects.length === 0 ? <p className="faint">No repository here that you may see.</p> : (
            <div className="card card-flush table-scroll">
              <table className="table st-cov">
                <caption className="visually-hidden">Checks in effect for each repository</caption>
                <thead><tr><th scope="col">Repository</th>{(profile?.checks ?? []).filter((c) => c.available).map((c) => <th key={c.id} scope="col" className="st-covh"><span title={c.title}>{c.id}</span></th>)}<th scope="col">Open findings</th></tr></thead>
                <tbody>
                  {cov.projects.map((p) => (
                    <tr key={p.project}>
                      <th scope="row"><Link href={`/projects/${encodeURIComponent(p.project)}/standards`}>{p.project}</Link>{p.unit ? <span className="faint small"> · {p.unit}</span> : null}</th>
                      {(profile?.checks ?? []).filter((c) => c.available).map((c) => {
                        const cell = p.checks[c.id];
                        if (p.off) return <td key={c.id} className="st-c st-c-off">off</td>;
                        if (!cell) return <td key={c.id} className="st-c st-c-na"><span className="visually-hidden">does not apply</span>–</td>;
                        return <td key={c.id} className={"st-c st-c-" + (cell.active ? cell.state : "off")}>{cell.active ? (cell.state === "block" ? "blocks" : "advises") : "off"}{cell.locked ? <span aria-label="locked"> 🔒</span> : null}</td>;
                      })}
                      <td>{p.off ? <span className="faint">not recorded</span> : <>{p.open["critical"] ?? 0} critical, {p.open["high"] ?? 0} high, {p.open["medium"] ?? 0} medium</>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}

      {cur && profile && ready && (
        <section className="card st-edit" aria-labelledby="st-edit">
          <h2 id="st-edit" className="h-sm">What {w[level] ?? level}{need ? " " + scope : ""} says</h2>
          <p className="faint small">Leave a value on “inherit” to take it from the level above. A lock keeps every level below, and every repository’s own file, from weakening the value.</p>
          <fieldset>
            <legend>The switch</legend>
            <label className="st-switch"><input type="checkbox" checked={off} onChange={(e) => set((s) => { s.off = e.target.checked; if (!e.target.checked) s.off_reason = ""; return s; })} /> Switch Standing orders off for everything below {w[level] ?? level}</label>
            {off && (
              <div className="callout callout-draft">
                Nothing will be scanned and no finding recorded. This is written to the {terms().say("{Logbook}")}, and a reminder is shown on the Security pages while it is off.
                <label htmlFor="st-offr">Why? (at least 10 characters)</label>
                <input id="st-offr" value={draft.off_reason ?? ""} onChange={(e) => set((s) => { s.off_reason = e.target.value; return s; })} />
              </div>
            )}
            <label className="st-lockl"><input type="checkbox" checked={lockOf("off")} onChange={(e) => lock("off", e.target.checked)} /> Lock: nothing below may switch them off</label>
          </fieldset>
          <fieldset>
            <legend>Checks</legend>
            <table className="table st-edit-table">
              <thead><tr><th scope="col">Check</th><th scope="col">State</th><th scope="col">Lock</th></tr></thead>
              <tbody>
                {profile.checks.filter((c) => c.available).map((c) => (
                  <tr key={c.id}>
                    <th scope="row">{c.title}<span className="faint small st-hint">{c.id}, by default {c.default}</span></th>
                    <td>
                      <select aria-label={`State of ${c.title}`} value={draft.checks?.[c.id] ?? ""} onChange={(e) => set((s) => { s.checks = { ...(s.checks ?? {}) }; if (e.target.value) s.checks[c.id] = e.target.value as State; else delete s.checks[c.id]; return s; })}>
                        <option value="">inherit</option>{STATE_OPTIONS.filter((o) => o !== "block" || c.gates).map((o) => <option key={o} value={o}>{o}</option>)}
                      </select>
                    </td>
                    <td><label><input type="checkbox" checked={lockOf("check." + c.id)} onChange={(e) => lock("check." + c.id, e.target.checked)} /> <span className="visually-hidden">Lock {c.title}</span>lock</label></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </fieldset>
          <fieldset>
            <legend>Gates</legend>
            <div className="st-form">
              <label>Block at <select value={draft.gate?.fail_on ?? ""} onChange={(e) => set((s) => { s.gate = { ...(s.gate ?? {}), fail_on: (e.target.value || undefined) as Sev | undefined }; return s; })}><option value="">inherit</option>{["critical", "high", "medium", "low"].map((o) => <option key={o}>{o}</option>)}</select></label>
              <label><input type="checkbox" checked={lockOf("gate.fail_on")} onChange={(e) => lock("gate.fail_on", e.target.checked)} /> lock</label>
              <label>Fix required <select value={draft.gate?.require_fix === undefined ? "" : String(draft.gate.require_fix)} onChange={(e) => set((s) => { s.gate = { ...(s.gate ?? {}), require_fix: e.target.value === "" ? undefined : e.target.value === "true" }; return s; })}><option value="">inherit</option><option value="true">yes</option><option value="false">no</option></select></label>
              <label><input type="checkbox" checked={lockOf("gate.require_fix")} onChange={(e) => lock("gate.require_fix", e.target.checked)} /> lock</label>
              <label>Default branch <select value={draft.gate?.default_branch ?? ""} onChange={(e) => set((s) => { s.gate = { ...(s.gate ?? {}), default_branch: (e.target.value || undefined) as State | undefined }; return s; })}><option value="">inherit</option><option>advise</option><option>block</option></select></label>
              <label><input type="checkbox" checked={lockOf("gate.default_branch")} onChange={(e) => lock("gate.default_branch", e.target.checked)} /> lock</label>
              <label>A check that did not run <select value={draft.gate?.incomplete ?? ""} onChange={(e) => set((s) => { s.gate = { ...(s.gate ?? {}), incomplete: e.target.value || undefined }; return s; })}><option value="">inherit</option><option>pass</option><option>fail</option></select></label>
            </div>
          </fieldset>
          <fieldset>
            <legend>Licenses</legend>
            <div className="st-form">
              <label>Denied (comma-separated) <input value={(draft.licenses?.deny ?? []).join(", ")} placeholder="inherit" onChange={(e) => set((s) => { s.licenses = { ...(s.licenses ?? {}), deny: e.target.value.trim() === "" ? undefined : e.target.value.split(",").map((x) => x.trim()).filter(Boolean) }; return s; })} /></label>
              <label><input type="checkbox" checked={lockOf("licenses.deny")} onChange={(e) => lock("licenses.deny", e.target.checked)} /> lock</label>
              <label>Allowed (comma-separated) <input value={(draft.licenses?.allow ?? []).join(", ")} placeholder="inherit" onChange={(e) => set((s) => { s.licenses = { ...(s.licenses ?? {}), allow: e.target.value.trim() === "" ? undefined : e.target.value.split(",").map((x) => x.trim()).filter(Boolean) }; return s; })} /></label>
              <label><input type="checkbox" checked={lockOf("licenses.allow")} onChange={(e) => lock("licenses.allow", e.target.checked)} /> lock</label>
            </div>
          </fieldset>
          <fieldset>
            <legend>Exemptions</legend>
            <p className="faint small">Findings this level accepts, with a reason and an end date. Recorded decisions about single findings are made on the finding itself.</p>
            {(draft.ignore ?? []).length === 0 ? <p className="faint">None.</p> : (
              <ul className="st-ex">{(draft.ignore ?? []).map((x, i) => (
                <li key={i}><strong>{[x.check && "check " + x.check, x.rule && "rule " + x.rule, x.package && "package " + x.package, x.path].filter(Boolean).join(", ")}</strong> until {x.expires} — {x.reason}{" "}
                  <button type="button" className="btn btn-sm" onClick={() => set((s) => { s.ignore = (s.ignore ?? []).filter((_, j) => j !== i); return s; })}>Remove</button></li>
              ))}</ul>
            )}
            <label className="st-lockl"><input type="checkbox" checked={lockOf("ignore")} onChange={(e) => lock("ignore", e.target.checked)} /> Lock: levels below and repository files may not add exemptions</label>
          </fieldset>
          <p className="faint small">Version {cur.version}.</p>
          <button type="button" className="btn" disabled={busy} onClick={save}>Save these settings</button>
        </section>
      )}
    </div>
  );
}
