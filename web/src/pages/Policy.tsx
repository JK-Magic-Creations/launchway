import { PageHead } from "../kit/layout";
import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type PolicyDef, type PolicyOverview, type PolicyVerdictRow, type Waiver } from "../api";
import { ago, clock } from "../format";
import { useNow } from "../live";
import { ErrorBox, Link, Mono } from "../components/ui";
import { WizardButton, WizardEmpty } from "../components/WizardButton";
import { ModeBadge, ResultBadge, ScopeText, ShieldIcon, WaiverForm, WaiverList, type WaiverDraft } from "../components/Policy";
import { terms } from "../terms";

const POINT_WORDS: Record<string, string> = { run: "run start", deploy: "before deploying", image: "before an image push" };

// PolicyPage lists the policies in force: where each comes from and at
// which version, its scope, mode and rules, and the verdicts of recent runs
// (of projects the person may see). Below, the waivers, and a form to
// grant one for those who may.
export function PolicyPage() {
  const now = useNow(30000);
  const [o, setO] = useState<PolicyOverview | null>(null);
  const [waivers, setWaivers] = useState<{ waivers: Waiver[]; waive_projects: string[]; rules: Record<string, string[]> } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [active, setActive] = useState(true);
  const [draft, setDraft] = useState<WaiverDraft | null>(null);
  const load = useCallback(() => {
    api.policies().then((x) => { setO(x); setError(null); }).catch(setError);
    api.waivers({ active }).then(setWaivers).catch(setError);
  }, [active]);
  useEffect(load, [load]);
  const recent = useMemo(() => {
    const by: Record<string, PolicyVerdictRow[]> = {};
    for (const r of o?.recent ?? []) (by[r.policy] ??= []).push(r);
    return by;
  }, [o]);

  if (error && !o) return <div className="page"><ErrorBox error={error} /></div>;
  if (!o || !waivers) return <div className="page"><div className="loading">Loading…</div></div>;
  const total = o.sources.reduce((n, s) => n + s.policies.length, 0);
  const failing = o.sources.filter((s) => s.error);
  return (
    <div className="page">
      <PageHead eyebrow="Settings" title={<>Policy</>} sentence={<>Rules every pipeline is held to, set on the server or in a reviewed Git repository. Each decision is recorded in the run
            it judged, with the evidence; <strong>enforce</strong> stops a run or a step, <strong>report</strong> only records.</>} actions={<>{o.sources.length > 0 && <WizardButton wizard="add-policy" label="Add a policy" />}</>} />
      <ErrorBox error={error} />

      {failing.map((s) => (
        <div key={s.name} className="callout callout-bad" role="alert">
          <strong>Policy source {s.name} is failing closed.</strong> {s.error}. Until it loads, every run of {s.projects.length ? s.projects.join(", ") : "every project"} is refused at each policy point.
        </div>
      ))}

      {!o.sources.length && (
        <WizardEmpty title="No policies" wizard="add-policy" label="Add a policy"
          text={`A policy is a set of rules every run must meet before it deploys or finishes, such as tests passing or a second person's ${terms().lower("clearance")}; pipelines cannot switch it off.`} />
      )}

      {o.sources.map((s) => (
        <section key={s.name} className="card pol-source" aria-label={`Source ${s.name}`}>
          <div className="pol-source-head">
            <div>
              <h2 className="h-sm"><ShieldIcon size={15} /> {s.kind === "config" ? "Policies set in Launchway" : <>Source <Mono>{s.name}</Mono></>}</h2>
              <div className="faint small pol-source-meta">
                {s.kind === "git" ? <>library <Mono>{s.library}</Mono> at <Mono>{s.ref}</Mono>, <Mono>{s.path}/</Mono> · </> : null}
                version <Mono>{s.version ? s.version.slice(0, 19) : "none"}</Mono>
                {s.loaded_at ? <> · loaded {ago(s.loaded_at, now)}</> : null}
                {s.checked_at ? <> · confirmed {ago(s.checked_at, now)}</> : null}
                {s.projects.length > 0 && <> · governs {s.projects.join(", ")}</>}
              </div>
            </div>
            <span className={"pol-badge " + (s.error ? "pol-bad" : "pol-ok")}>{s.error ? "failing closed" : `${s.policies.length} polic${s.policies.length === 1 ? "y" : "ies"}`}</span>
          </div>
          {s.fetch_error && !s.error && <div className="pol-note">The last check failed {s.fetch_error_at ? ago(s.fetch_error_at, now) : ""}: {s.fetch_error}. The version loaded before stays in use for a while.</div>}
          <ul className="pol-list">
            {s.policies.map((p) => <PolicyItem key={p.name} p={p} recent={recent[p.name] ?? []} now={now} />)}
          </ul>
        </section>
      ))}

      <section className="card" aria-label="Waivers">
        <div className="pol-section-head">
          <h2 className="h-sm">Waivers</h2>
          <div className="chips" role="group" aria-label="Which waivers">
            <button type="button" className={"chip" + (active ? " chip-on" : "")} aria-pressed={active} onClick={() => setActive(true)}>In force</button>
            <button type="button" className={"chip" + (!active ? " chip-on" : "")} aria-pressed={!active} onClick={() => setActive(false)}>All</button>
          </div>
        </div>
        <p className="sub small">A waiver lets one rule of one policy pass for a project (or one run, environment or finding) until it expires.
          Every waiver is in the {terms().lower("logbook")} and shown wherever the verdict it changed is.</p>
        {waivers.waivers.length
          ? <WaiverList waivers={waivers.waivers} mayRevoke={(w) => waivers.waive_projects.includes(w.project)} onChange={load} />
          : <p className="faint">{active ? "No waiver is in force." : "No waivers."}</p>}
        {waivers.waive_projects.length > 0 && total > 0 && (
          draft
            ? <WaiverForm draft={draft} maxDays={o.max_waiver_days} onCancel={() => setDraft(null)} onDone={() => { setDraft(null); load(); }} />
            : <GrantPicker projects={waivers.waive_projects} rules={waivers.rules} onPick={setDraft} />
        )}
      </section>

      <section className="card" aria-label="Rule catalogue">
        <h2 className="h-sm">The rules a policy can use</h2>
        <div className="table-scroll">
          <table className="inputs-table pol-catalogue">
            <thead><tr><th>Rule</th><th>Judged</th><th>What it asks for</th></tr></thead>
            <tbody>
              {o.catalogue.map((r) => (
                <tr key={r.name}><td><Mono>{r.name}</Mono></td><td>{POINT_WORDS[r.point] ?? r.point}</td><td>{r.summary}</td></tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

function PolicyItem({ p, recent, now }: { p: PolicyDef; recent: PolicyVerdictRow[]; now: number }) {
  return (
    <li className="pol-item">
      <div className="pol-item-head">
        <strong className="pol-name">{p.name}</strong>
        <ModeBadge mode={p.mode} />
        <span className="pol-rulelist">{Object.keys(p.rules).map((r) => <Mono key={r}>{r}</Mono>)}</span>
      </div>
      {p.description && <p className="sub small">{p.description}</p>}
      <div className="small"><span className="faint">Applies to:</span> <ScopeText p={p} /></div>
      {p.file && <div className="faint small">{p.file}{p.line ? `:${p.line}` : ""}</div>}
      <details className="pol-ev-details">
        <summary>Rules as written</summary>
        <pre className="code">{JSON.stringify(p.rules, null, 2)}</pre>
      </details>
      <div className="pol-recent" aria-label={`Recent verdicts of ${p.name}`}>
        <span className="faint small">Recent:</span>
        {recent.length === 0 && <span className="faint small">no run judged yet</span>}
        {recent.map((r) => (
          <Link key={r.run_id + r.at + (r.step ?? "")} href={`/runs/${r.run_id}?tab=policy`} className="pol-recent-item">
            <ResultBadge verdict={r.verdict} mode={r.mode} />
            <span>{r.project} #{r.run_number}</span>
            <span className="faint">{r.step ? `${r.step}${r.environment ? ` → ${r.environment}` : ""}` : POINT_WORDS[r.point]}</span>
            <span className="faint" title={clock(r.at)}>{ago(r.at, now)}</span>
          </Link>
        ))}
      </div>
    </li>
  );
}

/** Choose what to waive: a project, a policy and one of its rules. */
function GrantPicker({ projects, rules, onPick }: { projects: string[]; rules: Record<string, string[]>; onPick: (d: WaiverDraft) => void }) {
  const names = Object.keys(rules).sort();
  const [project, setProject] = useState(projects[0] ?? "");
  const [policy, setPolicy] = useState(names[0] ?? "");
  const [rule, setRule] = useState((rules[names[0]] ?? [])[0] ?? "");
  const deployRules = ["tests", "findings", "sbom", "deployed_images", "approvals", "allowed_failures", "refs"];
  return (
    <form className="pol-grant" aria-label="Grant a waiver" onSubmit={(e) => {
      e.preventDefault();
      onPick({ project, policy, rule, deployRule: deployRules.includes(rule) });
    }}>
      <label className="field"><span>Project</span>
        <select value={project} onChange={(e) => setProject(e.target.value)}>{projects.map((p) => <option key={p}>{p}</option>)}</select>
      </label>
      <label className="field"><span>Policy</span>
        <select value={policy} onChange={(e) => { setPolicy(e.target.value); setRule((rules[e.target.value] ?? [])[0] ?? ""); }}>
          {names.map((n) => <option key={n}>{n}</option>)}
        </select>
      </label>
      <label className="field"><span>Rule</span>
        <select value={rule} onChange={(e) => setRule(e.target.value)}>{(rules[policy] ?? []).map((r) => <option key={r}>{r}</option>)}</select>
      </label>
      <button type="submit" className="btn" disabled={!project || !policy || !rule}>Waive a rule…</button>
    </form>
  );
}
