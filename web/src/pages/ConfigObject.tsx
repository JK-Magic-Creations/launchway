import { useCallback, useEffect, useMemo, useState, type KeyboardEvent } from "react";
import { ApiError, api, type RuleInfo } from "../api";
import { configApi, kindLabel, kindOf, levelOf, loadOverview, useOverview, type ChangeRequest, type ConfigChange, type ConfigKind,
  type ConfigObject, type ConfigOverview, type Effective, type Spec } from "../configApi";
import { FIELDS, levelWords, nameHelp, withLevelNames } from "../configKinds";
import { ago, clock } from "../format";
import { navigate, usePath } from "../router";
import { CheckLine, DefinedAt, DiffTable, ProblemList, SpecForm, TextInput, assignProblems, specDiff, useServerCheck } from "../components/ConfigForm";
import { ConfigNav, wizardFor } from "../components/ConfigNav";
import { PolicyRules } from "../components/PolicyRules";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import "../config.css";

// ConfigObjectPage shows one configuration object: a form for its settings
// with an explanation under each, a raw tab for anything without a field,
// the server's validation as you type, save with the version you loaded,
// delete and restore, and its history. With name "" it makes a new one.
export function ConfigObjectPage({ kind, name }: { kind: string; name: string }) {
  const path = usePath();
  const { overview, error: ovError } = useOverview();
  const k = kindOf(overview, kind);
  if (ovError) return <div className="page"><ErrorBox error={ovError} /></div>;
  if (!overview) return <div className="page"><div className="loading">Loading…</div></div>;
  if (!k) return <div className="page"><ConfigNav overview={overview} path={path} /><Empty title="No such kind of configuration"><Link href="/config">Back to configuration</Link></Empty></div>;
  return <Editor key={k.name + "/" + name} k={k} name={name} overview={overview} path={path} />;
}

type Loaded = { state: "loading" } | { state: "new" } | { state: "ok"; obj: ConfigObject } | { state: "error"; error: unknown };

function Editor({ k, name, overview, path }: { k: ConfigKind; name: string; overview: ConfigOverview; path: string }) {
  const [loaded, setLoaded] = useState<Loaded>(name ? { state: "loading" } : { state: "new" });
  const [spec, setSpec] = useState<Spec>({});
  const [unit, setUnit] = useState("");
  const [newName, setNewName] = useState("");
  const [comment, setComment] = useState("");
  const [tab, setTab] = useState<"form" | "raw">("form");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [conflict, setConflict] = useState(false);
  const [saved, setSaved] = useState("");
  const [history, setHistory] = useState<ConfigChange[] | null>(null);
  const [catalogue, setCatalogue] = useState<RuleInfo[]>([]);

  const load = useCallback(() => {
    if (!name) return;
    configApi.get(k.path, name).then((o) => {
      setLoaded({ state: "ok", obj: o }); setSpec(o.spec ?? {}); setUnit(o.unit ?? ""); setConflict(false);
    }).catch((e) => {
      // A singleton nobody has set yet is made on the first save.
      if (e instanceof ApiError && e.status === 404 && k.singleton) setLoaded({ state: "new" });
      else setLoaded({ state: "error", error: e });
    });
    configApi.history(k.path, name).then((h) => setHistory(h.changes)).catch(() => setHistory([]));
  }, [k, name]);
  useEffect(load, [load]);
  useEffect(() => { if (k.name === "policy") api.policies(true).then((p) => setCatalogue(p.catalogue)).catch(() => {}); }, [k.name]);

  const obj = loaded.state === "ok" ? loaded.obj : null;
  const isNew = loaded.state === "new";
  const objName = name || newName.trim();
  const dirty = isNew ? (objName !== "" || Object.keys(spec).length > 0) : !!obj && (JSON.stringify(obj.spec ?? {}) !== JSON.stringify(spec) || (obj.unit ?? "") !== unit);
  const change: ChangeRequest[] | null = dirty && objName
    ? [{ kind: k.name, name: objName, unit: unit || undefined, spec, version: isNew ? -1 : (obj?.version ?? -1) }] : null;
  const check = useServerCheck(change);
  const fields = useMemo(() => withLevelNames(FIELDS[k.name] ?? [], overview.levels), [k.name, overview.levels]);
  const nameField = { path: "__name", label: "Name", help: "", type: "text" as const, match: ["name", "names", "exists", "already"] };
  const { by, rest } = useMemo(() => {
    const ruleFields = k.name === "policy" ? catalogue.map((r) => ({ path: r.name, label: r.name, help: "", type: "text" as const })) : [];
    return assignProblems(check.problems, [...fields, ...ruleFields, ...(isNew ? [nameField] : [])]);
  }, [check.problems, fields, catalogue, isNew]); // eslint-disable-line react-hooks/exhaustive-deps

  // Someone else saved since this page loaded: the server's check says so before Save.
  const moved = check.problems.some((p) => /is at version \d+, not \d+/.test(p));
  const restShown = rest.filter((p) => !/is at version \d+, not \d+/.test(p));
  const one = kindLabel(overview, k, false);
  const title = k.singleton ? kindLabel(overview, k) : isNew ? `New ${one.toLowerCase()}` : name;

  // Arrow keys move between the two tabs, as in the tabs pattern.
  const tabKeys = (e: KeyboardEvent<HTMLElement>) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    const t = tab === "form" ? "raw" : "form";
    setTab(t);
    document.getElementById(t === "form" ? "cf-tab-form" : "cf-tab-raw")?.focus();
  };

  const save = async () => {
    setBusy(true); setError(null); setSaved(""); setConflict(false);
    try {
      if (isNew) {
        const r = await configApi.create(k.path, { name: k.singleton ?? objName, unit: unit || undefined, spec, comment: comment || undefined });
        setComment("");
        if (["installation", "organisation", "unit"].includes(k.name)) loadOverview(true).catch(() => {});
        navigate(`/config/${k.path}/${encodeURIComponent(r.object.name)}`);
        return;
      }
      const take = await takeOwnership();
      if (take === null) { setBusy(false); return; }
      const r = await configApi.put(k.path, name, { unit: unit || undefined, spec, comment: comment || undefined, version: obj!.version, take_ownership: take || undefined });
      setComment("");
      setSaved(r.changed ? `Saved: version ${r.object.version}. Every server uses it within seconds.` : "Nothing changed, so nothing was saved.");
      if (["installation", "organisation", "unit"].includes(k.name) || unit !== obj?.unit) loadOverview(true).catch(() => {});
      load();
    } catch (e) {
      if (e instanceof ApiError && e.status === 412) setConflict(true);
      else setError(e);
    } finally { setBusy(false); }
  };
  // An object Terraform or Git manages is read-only here unless the person
  // takes it over, which is recorded: ask, and say what it means. null: they
  // declined.
  const takeOwnership = async (): Promise<boolean | null> => {
    const by = obj?.managed_by;
    if (by !== "terraform" && by !== "git") return false;
    const who = by === "terraform" ? "Terraform" : "Git";
    return window.confirm(`${who} manages this. A change made here would be set back by its next run, so changing it here means taking it over from ${who}: it is then managed here, and the change is recorded. Take it over and save?`) ? true : null;
  };
  const remove = async () => {
    if (!obj) return;
    const what = k.name === "project" ? " Its runs and history are kept; it stops being watched." : "";
    if (!window.confirm(`Delete ${one.toLowerCase()} ${name}? It stops applying at once.${what} You can restore it from this page.`)) return;
    setBusy(true); setError(null);
    const take = await takeOwnership();
    if (take === null) { setBusy(false); return; }
    try { await configApi.remove(k.path, name, obj.version, comment || undefined, take || undefined); setComment(""); if (k.name === "unit") loadOverview(true).catch(() => {}); load(); }
    catch (e) { if (e instanceof ApiError && e.status === 412) setConflict(true); else setError(e); }
    finally { setBusy(false); }
  };
  const restore = async () => {
    if (!window.confirm(`Restore ${one.toLowerCase()} ${name}? It applies again at once, as it was when it was deleted.`)) return;
    setBusy(true); setError(null);
    try { await configApi.restore(k.path, name, obj?.version ?? -1, comment || undefined); setComment(""); if (k.name === "unit") loadOverview(true).catch(() => {}); load(); }
    catch (e) { setError(e); } finally { setBusy(false); }
  };

  if (loaded.state === "loading") return <div className="page"><ConfigNav overview={overview} path={path} /><div className="loading">Loading…</div></div>;
  if (loaded.state === "error") {
    return (
      <div className="page"><ConfigNav overview={overview} path={path} />
        {loaded.error instanceof ApiError && loaded.error.status === 404
          ? <Empty title={`No ${one.toLowerCase()} ${name}`}>It does not exist. <Link href={`/config/${k.path}`}>See them all</Link>.</Empty>
          : <ErrorBox error={loaded.error} />}
      </div>
    );
  }
  const wiz = isNew ? wizardFor(k.name) : undefined;
  const deleted = !!obj?.deleted;
  return (
    <div className="page cf-page">
      <ConfigNav overview={overview} path={path} />
      <header className="page-head">
        <div>
          <div className="crumbs"><Link href="/config">Configuration</Link><span aria-hidden="true">/</span>
            {!k.singleton && <><Link href={`/config/${k.path}`}>{kindLabel(overview, k)}</Link><span aria-hidden="true">/</span></>}</div>
          <h1 className={isNew || k.singleton ? "" : "mono cf-h1-mono"}>{title}</h1>
          <p className="sub">{levelWords(k.doc, overview.levels)}</p>
        </div>
        {obj && (
          <dl className="cf-meta">
            <div><dt>Version</dt><dd>{obj.version}</dd></div>
            {obj.updated_at > 0 && <div><dt>Changed</dt><dd title={clock(obj.updated_at)}>{ago(obj.updated_at)}{obj.updated_by ? ` by ${obj.updated_by}` : ""}</dd></div>}
            <div><dt>Managed by</dt><dd>{obj.version === 0 ? "nobody yet: the built-in defaults" : managerLabel(obj.managed_by)}</dd></div>
            {k.levels && overview.units_in_use && <div><dt>Defined at</dt><dd>{levelOf(overview, obj.unit)}</dd></div>}
          </dl>
        )}
      </header>

      {wiz && <div className="callout callout-info">Not sure what to put? <Link href={`/config/wizard/${wiz.id}`}>{wiz.title}</Link> asks a few questions instead.</div>}
      {obj?.managed_by === "terraform" && (
        <div className="callout callout-draft" role="note"><strong>Managed by Terraform.</strong> It is read-only here: change it in Terraform. If you must change it here, saving asks you to take it over from Terraform, which is recorded.</div>
      )}
      {obj?.managed_by === "import" && (
        <div className="callout callout-info" role="note">This was imported from a configuration file. The file's copy is now ignored: changes made here are the ones that count.</div>
      )}
      {deleted && (
        <div className="callout callout-bad cf-row" role="status">
          <span><strong>Deleted</strong> {ago(obj!.deleted_at ?? 0)}{obj!.deleted_by ? ` by ${obj!.deleted_by}` : ""}: it does not apply. Restoring it brings it back as it was.</span>
          <button type="button" className="btn" disabled={busy} onClick={restore}>Restore</button>
        </div>
      )}
      {(conflict || moved) && (
        <div className="callout callout-bad cf-row" role="alert">
          <span><strong>Someone else changed this since you opened it.</strong> Your change was not saved, so theirs is not lost. Reload to see their version, then make your change again (copy it from the Raw tab first if it is long).</span>
          <button type="button" className="btn" onClick={() => { setLoaded({ state: "loading" }); load(); }}>Reload</button>
        </div>
      )}
      <ErrorBox error={error} />

      <section className="card cf-editor" aria-label="Settings">
        <div className="tabs" role="tablist" aria-label="How to edit">
          <button type="button" role="tab" id="cf-tab-form" aria-selected={tab === "form"} tabIndex={tab === "form" ? 0 : -1} aria-controls="cf-panel" className={"tab" + (tab === "form" ? " tab-on" : "")} onClick={() => setTab("form")} onKeyDown={tabKeys}>Settings</button>
          <button type="button" role="tab" id="cf-tab-raw" aria-selected={tab === "raw"} tabIndex={tab === "raw" ? 0 : -1} aria-controls="cf-panel" className={"tab" + (tab === "raw" ? " tab-on" : "")} onClick={() => setTab("raw")} onKeyDown={tabKeys}>Raw (JSON)</button>
        </div>
        <div id="cf-panel" role="tabpanel" aria-labelledby={tab === "form" ? "cf-tab-form" : "cf-tab-raw"} className="cf-panel">
          <fieldset disabled={deleted} className="cf-plain">
            {isNew && !k.singleton && (
              <div className="cf-grid">
                <TextInput label="Name" required value={newName} onChange={setNewName} help={nameHelp(k.name, overview.levels)} mono focusOnMount problems={by.__name} />
              </div>
            )}
            {k.levels && (
              <div className="cf-grid">
                <DefinedAt overview={overview} value={unit} onChange={setUnit} project={k.name === "project"} />
              </div>
            )}
            {tab === "form" ? (
              <>
                {fields.length > 0 && <SpecForm fields={fields} spec={spec} onChange={setSpec} problems={by} />}
                {k.name === "policy" && (
                  catalogue.length ? <PolicyRules catalogue={catalogue} rules={(spec.rules as Record<string, unknown>) ?? {}} problems={by}
                    onChange={(r) => setSpec({ ...spec, rules: Object.keys(r).length ? r : undefined } as Spec)} />
                    : <p className="faint">Loading the rule catalogue…</p>
                )}
                <Unformed kind={k.name} spec={spec} />
              </>
            ) : <RawEditor spec={spec} onChange={setSpec} />}
          </fieldset>
          <ProblemList problems={restShown} title="The server says:" />
        </div>
        {!deleted && (
          <div className="cf-savebar">
            <TextInput label="Why (kept in the history, optional)" value={comment} onChange={setComment} placeholder="for example: the team moved to the new cluster" />
            <div className="cf-savebar-row">
              {dirty ? <CheckLine check={check} okText="The server's checks pass: you can save." /> : <span className="faint cf-checkstate">{isNew ? "Fill in the settings." : "No changes yet."}</span>}
              <span className="cf-spacer" />
              {obj && !k.singleton && <button type="button" className="btn btn-danger-outline" disabled={busy} onClick={remove}>Delete</button>}
              {obj && dirty && <button type="button" className="btn" disabled={busy} onClick={() => { setSpec(obj.spec ?? {}); setUnit(obj.unit ?? ""); }}>Undo changes</button>}
              <button type="button" className="btn btn-primary" disabled={busy || !dirty || check.checking || check.problems.length > 0 || (isNew && !k.singleton && !objName)} onClick={save}>
                {busy ? "Saving…" : isNew ? "Create" : "Save"}
              </button>
            </div>
            <div aria-live="polite" className="cf-ok">{dirty ? "" : saved}</div>
          </div>
        )}
      </section>

      {k.name === "project" && obj && !deleted && <EffectiveCard key={obj.version} project={name} overview={overview} />}
      {!isNew && <History changes={history} overview={overview} />}
    </div>
  );
}

function managerLabel(m: string): string {
  switch (m) {
    case "api": return "this page, the API or the command line";
    case "terraform": return "Terraform";
    case "import": return "an import of a configuration file";
    default: return m || "unknown";
  }
}

/** Unformed lists the spec's keys that have no field, kept as they are. */
function Unformed({ kind, spec }: { kind: string; spec: Spec }) {
  const known = new Set((FIELDS[kind] ?? []).map((f) => f.path.split(".")[0]));
  if (kind === "policy") known.add("rules");
  const rest = Object.keys(spec).filter((k) => !known.has(k));
  if (!rest.length) return null;
  return <p className="cf-help">Also set, without a field here: {rest.map((r, i) => <span key={r}>{i ? ", " : ""}<Mono>{r}</Mono></span>)}. They are kept as they are; edit them on the Raw tab.</p>;
}

function RawEditor({ spec, onChange }: { spec: Spec; onChange: (s: Spec) => void }) {
  const [text, setText] = useState(() => JSON.stringify(spec, null, 2));
  const [bad, setBad] = useState("");
  return (
    <div className="cf-field cf-wide">
      <label htmlFor="cf-raw" className="cf-label">The settings as JSON, with the configuration file's keys</label>
      <textarea id="cf-raw" className="cf-input cf-textarea cf-raw mono" value={text} spellCheck={false} aria-describedby="cf-raw-help cf-raw-bad"
        onChange={(e) => {
          setText(e.target.value);
          try {
            const v = JSON.parse(e.target.value);
            if (!v || typeof v !== "object" || Array.isArray(v)) throw new Error("the settings must be an object: { … }");
            setBad(""); onChange(v);
          } catch (err) { setBad(err instanceof Error ? err.message : String(err)); }
        }} />
      <div id="cf-raw-help" className="cf-help">Every key the configuration file has for this kind works here, including those without a field on the Settings tab. The server checks it as you type.</div>
      <div id="cf-raw-bad" className="cf-problems" aria-live="polite">{bad && <div className="cf-problem">Not valid JSON yet: {bad}</div>}</div>
    </div>
  );
}

const FROM: Record<string, string> = { default: "built-in default", installation: "installation setting", project: "this project" };

/** EffectiveCard says what applies to a project and where each comes from. */
function EffectiveCard({ project, overview }: { project: string; overview: ConfigOverview }) {
  const [ef, setEf] = useState<Effective | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { configApi.effective(project).then(setEf).catch(setError); }, [project]);
  const from = (f: string, unit?: string) => f === "organisation" ? `${overview.levels.organisation.singular}`
    : f === "unit" ? levelOf(overview, unit ?? "") : f === "installation" ? `${overview.levels.installation.singular} setting` : FROM[f] ?? f;
  return (
    <section className="card" aria-labelledby="cf-eff-h">
      <h2 id="cf-eff-h" className="cf-h">What applies to this {overview.levels.project.singular.toLowerCase()}</h2>
      <p className="cf-help">Settings it has itself, and what it gets from the levels above it. Change a value where it comes from.</p>
      <ErrorBox error={error} />
      {ef && (
        <div className="cf-two">
          <div>
            <h3 className="h-sm">Settings</h3>
            <table className="cf-table">
              <thead><tr><th scope="col">Setting</th><th scope="col">Value</th><th scope="col">From</th></tr></thead>
              <tbody>{ef.settings.map((s) => (
                <tr key={s.key}><td className="mono">{s.key}</td><td className="mono">{fmt(s.value)}</td><td><span className={`cf-from cf-from-${s.from}`}>{from(s.from)}</span></td></tr>
              ))}</tbody>
            </table>
          </div>
          <div>
            <h3 className="h-sm">Shared objects that apply</h3>
            {ef.objects.length === 0 ? <p className="faint">None.</p> : (
              <table className="cf-table">
                <thead><tr><th scope="col">What</th><th scope="col">Name</th><th scope="col">From</th></tr></thead>
                <tbody>{ef.objects.map((o) => {
                  const kk = overview.kinds.find((x) => x.name === o.kind);
                  return (
                    <tr key={o.kind + "/" + o.name}>
                      <td>{kk ? kindLabel(overview, kk, false) : o.kind}</td>
                      <td>{kk ? <Link href={`/config/${kk.path}/${encodeURIComponent(o.name)}`}><Mono>{o.name}</Mono></Link> : <Mono>{o.name}</Mono>}</td>
                      <td><span className={`cf-from cf-from-${o.from}`}>{o.from === "project" ? "names this project" : from(o.from, o.unit)}</span></td>
                    </tr>
                  );
                })}</tbody>
              </table>
            )}
          </div>
        </div>
      )}
    </section>
  );
}

function fmt(v: unknown): string {
  if (v === null || v === undefined) return "not set";
  if (typeof v === "string") return v;
  return JSON.stringify(v);
}

/** History lists every change, newest first, with what changed. */
function History({ changes, overview }: { changes: ConfigChange[] | null; overview: ConfigOverview }) {
  return (
    <section className="card" aria-labelledby="cf-hist-h">
      <h2 id="cf-hist-h" className="cf-h">History</h2>
      {changes === null ? <p className="faint">Loading…</p> : changes.length === 0 ? <p className="faint">No changes recorded.</p> : (
        <ol className="cf-history">
          {changes.map((c, i) => (
            <li key={c.id} className="cf-hist">
              <div className="cf-hist-head">
                <span className={`cf-action cf-action-${c.action}`}>{c.action}</span>
                <strong>version {c.version}</strong>
                <span>by {c.actor || "someone"}</span>
                <span className="faint" title={clock(c.at)}>{ago(c.at)}</span>
                {c.managed_by && c.managed_by !== "api" && <span className="cf-pill">{managerLabel(c.managed_by)}</span>}
              </div>
              {c.comment && <div className="cf-hist-comment">“{c.comment}”</div>}
              {c.unit_before !== c.unit_after && (
                <div className="small">Moved from {levelOf(overview, c.unit_before)} to {levelOf(overview, c.unit_after)}.</div>
              )}
              {c.action === "delete" || c.action === "restore" ? null : i < 6 ? <DiffTable rows={specDiff(c.before ?? {}, c.after ?? {})} /> : (
                <details><summary className="small">What changed</summary><DiffTable rows={specDiff(c.before ?? {}, c.after ?? {})} /></details>
              )}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
