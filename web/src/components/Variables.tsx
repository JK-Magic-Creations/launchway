import { useCallback, useEffect, useMemo, useState } from "react";
import { ApiError } from "../api";
import { useMe, canOperate } from "../auth";
import { Dialog } from "../kit/Dialog";
import { Chip } from "../kit/Status";
import { Button } from "../kit/layout";
import { terms } from "../terms";
import { variablesApi, VAR_TYPES, wireValue, type RecordedVar, type VarEntry, type VarLevel, type VarLink, type VarResolved, type VarSource, type VarType, type VarView } from "../variablesApi";
import { ErrorBox, Mono } from "./ui";
import { onLink } from "../router";
import "../variables.css";
import { prose } from "../prose";
const P = await prose("components.Variables");

/** The words for a level, in this installation's terms: "Wing", "Squadron payments", "Project cart-api". */
export function levelWord(level: VarLevel, scope?: string): string {
  const t = terms();
  switch (level) {
    case "installation": return t.label("installation");
    case "organization": return t.label("organization");
    case "unit": return `${t.label("unit")} ${scope ?? ""}`.trim();
    case "project": return `${t.label("project")} ${scope ?? ""}`.trim();
  }
}

/** Where a value comes from, in words. */
export function sourceWord(s: VarSource): string {
  switch (s.kind) {
    case "run": return terms().say("given when the {run} started");
    case "file": return "the pipeline file's with:";
    case "default": return "the template's own default";
  }
  if (!s.level) return "a built-in value";
  return levelWord(s.level, s.scope) + (s.environment ? ` for ${s.environment}` : "");
}

/** A value as shown: a secret is its reference and says so, never a value. */
function Value({ v }: { v: Pick<VarResolved, "type" | "display"> }) {
  return (
    <span className="vr-val">
      <Mono>{v.display === "" ? "(empty)" : v.display}</Mono>
      {v.type === "secret" && <span className="vr-ref" title={terms().say(P.t0)}>reference</span>}
    </span>
  );
}

function LockBadge({ e }: { e: VarResolved }) {
  if (!e.locked) return null;
  const by = levelWord(e.locked.level, e.locked.scope);
  return (
    <span className="vr-lock" title={`Only ${e.locked.who} can change it`}>
      <Chip tone="warn">Locked by {by}</Chip>
      {e.locked.reason && <span className="vr-why">“{e.locked.reason}”</span>}
      {e.locked.by && <span className="vr-who">set by {e.locked.by}</span>}
    </span>
  );
}

/** The chain behind a value: what it overrides, nearest first, struck through. */
export function Chain({ chain }: { chain: VarLink[] | null | undefined }) {
  // The server sends null, not an empty list, for a variable nothing overrides.
  if (!chain || chain.length < 2) return null;
  return (
    <ol className="vr-chain" aria-label={P.t1}>
      {chain.slice(1).map((l, i) => (
        <li key={i}>
          <s><Mono>{l.display === "" ? "(empty)" : l.display}</Mono></s>
          <span className="vr-from">{sourceWord(l)}</span>
          {l.ignored && <span className="vr-ignored">{l.ignored}{P.t2}</span>}
        </li>
      ))}
    </ol>
  );
}

/** What a beginner needs when there is nothing here yet. */
function FirstUse({ level, scope }: { level: VarLevel; scope?: string }) {
  const here = levelWord(level, scope);
  return (
    <div className="vr-first">
      <strong>No variables here yet.</strong>
      <p>{terms().say(P.t3)} {here} {P.t4}</p>
    </div>
  );
}

/** The rule, once, in the one sentence; the server owns the words. */
export function RuleNote({ rule }: { rule: string }) {
  return (
    <details className="vr-rule">
      <summary>How the nearest setting wins</summary>
      <p>{rule}</p>
      <p>{P.t5}<code>final</code>.</p>
    </details>
  );
}

interface Props {
  level: VarLevel;
  scope?: string;
  environment?: string;
  /** Read-only, short: the panel beside the editor and in the run-start form. */
  compact?: boolean;
  /** Only these names (what a pipeline takes). */
  names?: string[];
  onChanged?: () => void;
}

/** The hierarchy: every variable in effect at a level or for a project, where it comes from, what it overrides,
 * who locked it, and (unless compact) one-click override, change, lock and delete. The server decides who may;
 * what would be refused is shown disabled, with the reason. */
export function VariablesPanel({ level, scope, environment, compact, names, onChanged }: Props) {
  const me = useMe();
  const [view, setView] = useState<VarView | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [edit, setEdit] = useState<{ entry?: VarEntry; name?: string } | null>(null);
  const [locking, setLocking] = useState<VarEntry | null>(null);
  const [said, setSaid] = useState("");

  const load = useCallback((background: boolean) => {
    const p = level === "project" ? variablesApi.project(scope ?? "", environment, background) : variablesApi.at(level, scope, environment, background);
    p.then((v) => { setView(v); setError(null); }).catch(setError);
  }, [level, scope, environment]);
  useEffect(() => { load(false); const t = setInterval(() => load(true), 8000); return () => clearInterval(t); }, [load]);

  const shown = useMemo(() => (view?.variables ?? []).filter((v) => !names || names.includes(v.name)), [view, names]);
  const mayEdit = !!me && (level === "project" ? canOperate(me, scope ?? "") : me.admin || me.auth_mode === "none");
  const mayLock = !!me && (me.admin || me.auth_mode === "none" || (level === "project" && me.roles?.[scope ?? ""] === "admin"));

  const act = async (what: () => Promise<unknown>, done: string) => {
    setError(null); setSaid("");
    try { await what(); setSaid(done); load(true); onChanged?.(); } catch (e) { setError(e); }
  };

  if (error && !view) return <ErrorBox error={error} />;
  if (!view) return <p className="sub">Loading variables…</p>;
  return (
    <div className={"vr" + (compact ? " vr-compact" : "")}>
      {!compact && <RuleNote rule={view.rule} />}
      <ErrorBox error={error} />
      {said && <p className="vr-said" role="status">{said}</p>}
      {shown.length === 0 && (compact
        ? <p className="sub">No variables in effect{names ? " for this pipeline" : ""}. {level === "project" && <a href={`/projects/${encodeURIComponent(scope ?? "")}/variables`} onClick={onLink}>Add one</a>}</p>
        : <FirstUse level={level} scope={scope} />)}
      <ul className="vr-list">
        {shown.map((e) => (
          <li key={e.name} className="vr-item">
            <div className="vr-head">
              <strong className="vr-name">{e.name}</strong>
              <span className="vr-type">{VAR_TYPES.find((t) => t.id === e.type)?.label ?? e.type}{e.of ? ` of ${e.of}` : ""}</span>
              <Value v={e} />
              <span className="vr-from">from {sourceWord(e.from)}</span>
              <LockBadge e={e} />
            </div>
            {e.description && <div className="vr-desc">{e.description}</div>}
            <Chain chain={e.chain} />
            {!compact && (
              <div className="vr-actions">
                {!e.set_here && (
                  <span title={e.can_override ? "" : e.blocked}>
                    <Button sm disabled={!mayEdit || !e.can_override} onClick={() => setEdit({ entry: e })}>Override here</Button>
                  </span>
                )}
                {e.set_here && <Button sm disabled={!mayEdit || !e.can_override} onClick={() => setEdit({ entry: e })}>Change</Button>}
                {e.set_here && !e.locked && <Button sm disabled={!mayLock} onClick={() => setLocking(e)}>Lock…</Button>}
                {e.set_here && e.locked && e.locked.level === level && (
                  <Button sm disabled={!mayLock} onClick={() => act(() => variablesApi.unlock(e.name, level, view.scope, e.locked?.environment), `Unlocked ${e.name}.`)}>Unlock</Button>
                )}
                {e.set_here && <Button sm kind="bad" disabled={!mayEdit} onClick={() => act(() => variablesApi.remove(e.name, level, view.scope, e.from.environment), `Deleted ${e.name} here.`)}>Delete</Button>}
                {!e.can_override && !e.set_here && <span className="vr-blocked">{e.blocked}</span>}
              </div>
            )}
            {!compact && e.can_override && <div className="vr-reason">{e.why}</div>}
          </li>
        ))}
      </ul>
      {!compact && <div className="vr-add"><Button kind="pri" disabled={!mayEdit} onClick={() => setEdit({})}>Add a variable here</Button></div>}
      {edit && <EditVariable level={level} scope={view.scope} base={edit.entry} onClose={() => setEdit(null)}
        onSaved={(n) => { setEdit(null); setSaid(`Saved ${n}.`); load(true); onChanged?.(); }} />}
      {locking && <LockDialog entry={locking} onClose={() => setLocking(null)}
        onLock={(reason) => { const e = locking; setLocking(null); act(() => variablesApi.lock(e.name, level, reason, view.scope, e.from.environment), `Locked ${e.name}. Nearer settings are kept but ignored.`); }} />}
    </div>
  );
}

function EditVariable({ level, scope, base, onClose, onSaved }: { level: VarLevel; scope?: string; base?: VarEntry; onClose: () => void; onSaved: (name: string) => void }) {
  const [name, setName] = useState(base?.name ?? "");
  const [type, setType] = useState<VarType>(base?.type ?? "string");
  const [text, setText] = useState(base && base.set_here && base.type !== "secret" ? valueText(base) : base?.type === "secret" ? base.display : "");
  const [of, setOf] = useState(base?.of ?? "");
  const [env, setEnv] = useState(base?.from.environment ?? "");
  const [desc, setDesc] = useState(base?.description ?? "");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const typeLocked = !!base; // the first level to set a name fixes its type
  const save = async () => {
    setBusy(true); setError(null);
    try {
      await variablesApi.set({ name, level, scope, environment: env || undefined, type, of: of || undefined, value: wireValue(type, text), description: desc || undefined });
      onSaved(name);
    } catch (e) { setError(e instanceof SyntaxError ? new ApiError(400, P.t6 + e.message) : e); setBusy(false); }
  };
  return (
    <Dialog label={base ? `Set ${base.name}` : "Add a variable"} onClose={onClose}>
      <form className="vr-form" onSubmit={(e) => { e.preventDefault(); save(); }}>
        <h2>{base ? (base.set_here ? `Change ${base.name}` : `Override ${base.name}`) : "Add a variable"} at {levelWord(level, scope)}</h2>
        {base && !base.set_here && <p className="sub">Now it is {base.display} from {sourceWord(base.from)}{P.t7}</p>}
        <label>Name<input value={name} onChange={(e) => setName(e.target.value)} disabled={!!base} placeholder="registry" pattern="[a-z][a-z0-9_]{0,39}" required data-autofocus /></label>
        <label>Type
          <select value={type} onChange={(e) => setType(e.target.value as VarType)} disabled={typeLocked}>
            {VAR_TYPES.map((t) => <option key={t.id} value={t.id}>{t.label}</option>)}
          </select>
        </label>
        {type === "connection" && <label>Connection to<input value={of} onChange={(e) => setOf(e.target.value)} placeholder="registry" required /></label>}
        <label>{type === "secret" ? "Reference (store:key#field)" : type === "list" ? "Values, separated by commas" : type === "map" ? "Map as JSON" : "Value"}
          {type === "boolean"
            ? <select value={text || "true"} onChange={(e) => setText(e.target.value)}><option value="true">Yes</option><option value="false">No</option></select>
            : <input value={text} onChange={(e) => setText(e.target.value)} placeholder={type === "secret" ? "vault:apps/registry#token" : ""} required />}
        </label>
        {type === "secret" && <p className="sub">{terms().say(P.t8)}</p>}
        <label>{terms().say(P.t9)}<input value={env} onChange={(e) => setEnv(e.target.value)} placeholder="production" /></label>
        <label>What it is for (optional)<input value={desc} onChange={(e) => setDesc(e.target.value)} maxLength={200} /></label>
        <ErrorBox error={error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={busy}>{busy ? "Saving…" : "Save"}</button>
        </div>
      </form>
    </Dialog>
  );
}

function valueText(e: VarEntry): string {
  return e.type === "map" ? JSON.stringify(e.value) : Array.isArray(e.value) ? (e.value as unknown[]).join(", ") : String(e.value ?? "");
}

function LockDialog({ entry, onClose, onLock }: { entry: VarEntry; onClose: () => void; onLock: (reason: string) => void }) {
  const [reason, setReason] = useState("");
  return (
    <Dialog label={`Lock ${entry.name}`} onClose={onClose}>
      <form className="vr-form" onSubmit={(e) => { e.preventDefault(); onLock(reason); }}>
        <h2>Lock {entry.name}</h2>
        <p className="sub">{terms().say(P.t10)}</p>
        <label>{P.t11}<input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={300} required data-autofocus /></label>
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary">Lock</button>
        </div>
      </form>
    </Dialog>
  );
}

/** What a finished run used, as recorded when it was created. */
export function RunVariables({ runId }: { runId: string }) {
  const [rec, setRec] = useState<{ rerun_of?: string; variables: RecordedVar[] } | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { variablesApi.run(runId).then(setRec).catch(setError); }, [runId]);
  if (error) return null; // older servers have no record; the run page does not depend on it
  if (!rec) return null;
  // A variable with no value and no source (a template's built-in, such as launchway_project, that nothing set) says
  // nothing to a person: it is not listed.
  const shown = (rec.variables ?? []).filter((e) => e.display !== "" || e.value != null && e.value !== "" || ((e.from?.kind as string | undefined) ?? "") !== "");
  if (shown.length === 0) return null;
  return (
    <section className="card vr vr-compact" aria-labelledby="runvars-title">
      <h2 id="runvars-title">Variables in effect</h2>
      <p className="sub">{terms().say(P.t12)}</p>
      {rec.rerun_of && <p className="sub">{P.t13}</p>}
      <ul className="vr-list">
        {shown.map((e) => (
          <li key={e.name + (e.environment ?? "")} className="vr-item">
            <div className="vr-head">
              <strong className="vr-name">{e.name}</strong><Value v={e} />
              <span className="vr-from">from {sourceWord(e.from)}</span>{e.environment && <span className="vr-from">for {e.environment}</span>}
              <LockBadge e={e} />
            </div>
            <Chain chain={e.chain} />
          </li>
        ))}
      </ul>
    </section>
  );
}
