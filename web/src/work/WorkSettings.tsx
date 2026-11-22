import { useId, useState } from "react";
import { Button, Sheet } from "../kit/layout";
import { announce } from "../kit/announce";
import { csv, ErrorPanel, Loading, scopeLabel, useLoad, WorkPage } from "./common";
import { workApi, type Autonomy, type FieldDef, type ReviewPolicy, type Settings, type SettingsAnswer, type StateDef, type TypeDef } from "./workApi";
import { prose } from "../prose";
const P = await prose("work.WorkSettings");

// How work is set up in a project: the workflow, who may move work, types, fields, the close rule, and the rules for agents,
// including the autonomy policy and the review policy. A level the server does not act on yet is shown, disabled, and says so.

const CATEGORIES = ["todo", "doing", "review", "done", "cancelled"];
const LEVELS: { level: number; name: string; text: string }[] = [
  { level: 0, name: "Propose only", text: P.t0 },
  { level: 1, name: "A person approves and merges", text: P.t1 },
  { level: 2, name: "Merge small, safe changes", text: P.t2 },
  { level: 3, name: "Level 3", text: "" },
  { level: 4, name: "Level 4", text: "" },
];
const WHO: [string, string][] = [["anyone", P.t3], ["human", "A person, never an agent"], ["maintainer", P.t4]];
const CLASSES = ["small", "medium", "large"];

function sourceText(a: SettingsAnswer, scope: string): string {
  const src = a.autonomy.source;
  if (src === "default") return P.t5;
  if (src === scope) return `Set here, for ${scopeLabel(scope)}.`;
  return `Inherited from ${scopeLabel(src)}. Saving a level here overrides it for ${scopeLabel(scope)}.`;
}

export default function WorkSettings({ scope }: { scope: string }) {
  const ans = useLoad<SettingsAnswer>("settings|" + scope, () => workApi.settings(scope));
  if (ans.error) return <WorkPage scope={scope} on="settings" title="Settings"><ErrorPanel error={ans.error} onRetry={ans.reload} /></WorkPage>;
  if (!ans.data) return <WorkPage scope={scope} on="settings" title="Settings"><Loading what="settings" /></WorkPage>;
  return <Form key={JSON.stringify(ans.data.own) + ans.data.autonomy.level} scope={scope} a={ans.data} onSaved={ans.reload} />;
}

function Form({ scope, a, onSaved }: { scope: string; a: SettingsAnswer; onSaved: () => void }) {
  const eff = a.effective;
  const [prefix, setPrefix] = useState(a.own.prefix ?? "");
  const [unit, setUnit] = useState(a.own.estimate_unit ?? eff.estimate_unit ?? "points");
  const [states, setStates] = useState<StateDef[]>(a.own.states ?? eff.states ?? []);
  const [statesDirty, setStatesDirty] = useState(false);
  const [moveWho, setMoveWho] = useState<Record<string, string>>(() => {
    const m: Record<string, string> = {};
    for (const t of eff.transitions ?? []) if (!t.from?.length) m[t.to] = t.who;
    return m;
  });
  const [whoDirty, setWhoDirty] = useState(false);
  const [types, setTypes] = useState<TypeDef[]>(a.own.types ?? eff.types ?? []);
  const [typesDirty, setTypesDirty] = useState(false);
  const [fields, setFields] = useState<FieldDef[]>(a.own.fields ?? eff.fields ?? []);
  const [fieldsDirty, setFieldsDirty] = useState(false);
  const [close, setClose] = useState({ on: a.own.close?.on ?? eff.close?.on ?? "merge", state: a.own.close?.state ?? eff.close?.state ?? "done", verbs: (a.own.close?.verbs ?? eff.close?.verbs ?? []).join(", ") });
  const [closeDirty, setCloseDirty] = useState(false);
  const [rules, setRules] = useState({ enabled: a.own.agents?.enabled ?? eff.agents?.enabled ?? true, human: a.own.agents?.human_approval ?? eff.agents?.human_approval ?? true, merge: a.own.agents?.allow_merge ?? eff.agents?.allow_merge ?? false, reason: a.own.agents?.reason ?? "" });
  const [aut, setAut] = useState<Autonomy | undefined>(a.own.agents?.autonomy);
  const [rev, setRev] = useState<ReviewPolicy | undefined>(a.own.agents?.review);
  const [classes, setClasses] = useState<Record<string, string[]>>(a.own.classes ?? eff.classes ?? {});
  const [classesDirty, setClassesDirty] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);

  const level = aut?.level ?? a.autonomy.level;
  const editable = a.can_change;
  const loosened = !rules.human || rules.merge;

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaved(false);
    if (level > 1 && !(aut?.reason ?? "").trim()) { setError(new Error(P.t6)); return; }
    if (level >= 2 && !(aut?.paths ?? []).length) { setError(new Error(P.t7)); return; }
    setBusy(true);
    const out: Settings = { ...a.own };
    out.prefix = prefix.trim() || undefined;
    out.estimate_unit = unit;
    if (statesDirty) out.states = states;
    if (whoDirty || statesDirty) {
      const keep = (eff.transitions ?? []).filter((t) => t.from?.length);
      out.transitions = [...keep, ...states.filter((s) => (moveWho[s.key] ?? "anyone") !== "anyone" || whoDirty).map((s) => ({ to: s.key, who: moveWho[s.key] ?? "anyone" }))];
    }
    if (typesDirty) out.types = types;
    if (fieldsDirty) out.fields = fields;
    if (closeDirty) out.close = { on: close.on, state: close.state, verbs: csv(close.verbs) };
    if (classesDirty) out.classes = classes;
    out.agents = { ...a.own.agents, enabled: rules.enabled, human_approval: rules.human, allow_merge: rules.merge, reason: rules.reason.trim() || undefined, autonomy: aut, review: rev };
    try {
      await workApi.putSettings(scope, out);
      setError(null); setSaved(true); announce("Settings saved."); onSaved();
    } catch (x) { setError(x); } finally { setBusy(false); }
  };

  return (
    <WorkPage scope={scope} on="settings" title="Settings" sentence={`How work is set up in ${scopeLabel(scope)}. What is not set here is inherited from the levels above.`}>
      {!editable && <p className="callout callout-wait" role="note">{P.t8}{scopeLabel(scope)} can.</p>}
      <form onSubmit={save} className="wk-settings">
        <fieldset disabled={!editable} className="wk-fs">
          <Sheet title="General" pad>
            <div className="wk-grid">
              <TextField label="Item prefix" value={prefix} onChange={setPrefix} hint={P.t9} />
              <div className="wk-f"><label htmlFor="wk-unit">Estimates are in</label><select id="wk-unit" value={unit} onChange={(e) => setUnit(e.target.value)}><option value="points">Points</option><option value="hours">Hours</option></select></div>
            </div>
          </Sheet>

          <Sheet title="Workflow" pad>
            <p className="sub">{P.t10}</p>
            <div className="tablewrap"><table className="table wk-table">
              <caption className="visually-hidden">Workflow states</caption>
              <thead><tr><th scope="col">Key</th><th scope="col">Name</th><th scope="col">Meaning</th><th scope="col">Limit</th><th scope="col">Who may move work here</th><th scope="col"><span className="visually-hidden">Remove</span></th></tr></thead>
              <tbody>
                {states.map((s, i) => (
                  <tr key={i}>
                    <td><input aria-label={`Key of state ${i + 1}`} value={s.key} onChange={(e) => { setStates(states.map((x, j) => (j === i ? { ...x, key: e.target.value.replace(/\s+/g, "_").toLowerCase() } : x))); setStatesDirty(true); }} /></td>
                    <td><input aria-label={`Name of state ${i + 1}`} value={s.name} onChange={(e) => { setStates(states.map((x, j) => (j === i ? { ...x, name: e.target.value } : x))); setStatesDirty(true); }} /></td>
                    <td><select aria-label={`Meaning of ${s.name || "state " + (i + 1)}`} value={s.category} onChange={(e) => { setStates(states.map((x, j) => (j === i ? { ...x, category: e.target.value } : x))); setStatesDirty(true); }}>{CATEGORIES.map((c) => <option key={c}>{c}</option>)}</select></td>
                    <td><input type="number" min="0" className="narrow" aria-label={`Work-in-progress limit of ${s.name || "state " + (i + 1)}`} value={s.wip ?? ""} onChange={(e) => { setStates(states.map((x, j) => (j === i ? { ...x, wip: e.target.value ? Number(e.target.value) : undefined } : x))); setStatesDirty(true); }} /></td>
                    <td><select aria-label={`Who may move work to ${s.name || "state " + (i + 1)}`} value={moveWho[s.key] ?? "anyone"} onChange={(e) => { setMoveWho({ ...moveWho, [s.key]: e.target.value }); setWhoDirty(true); }}>{WHO.map(([k, n]) => <option key={k} value={k}>{n}</option>)}</select></td>
                    <td><Button sm kind="ghost" aria-label={`Remove state ${s.name || i + 1}`} disabled={states.length <= 2} onClick={() => { setStates(states.filter((_, j) => j !== i)); setStatesDirty(true); }}>Remove</Button></td>
                  </tr>
                ))}
              </tbody>
            </table></div>
            <Button sm onClick={() => { setStates([...states, { key: "", name: "", category: "doing" }]); setStatesDirty(true); }}>Add a state</Button>
          </Sheet>

          <Sheet title="Types" pad>
            <div className="tablewrap"><table className="table wk-table">
              <caption className="visually-hidden">Item types</caption>
              <thead><tr><th scope="col">Key</th><th scope="col">Name</th><th scope="col">Starting text</th><th scope="col">Off</th></tr></thead>
              <tbody>{types.map((t, i) => (
                <tr key={i}>
                  <td><input aria-label={`Key of type ${i + 1}`} value={t.key} onChange={(e) => { setTypes(types.map((x, j) => (j === i ? { ...x, key: e.target.value } : x))); setTypesDirty(true); }} /></td>
                  <td><input aria-label={`Name of type ${t.key || i + 1}`} value={t.name} onChange={(e) => { setTypes(types.map((x, j) => (j === i ? { ...x, name: e.target.value } : x))); setTypesDirty(true); }} /></td>
                  <td><input aria-label={`Starting text of ${t.name || t.key || "type " + (i + 1)}`} value={t.template ?? ""} onChange={(e) => { setTypes(types.map((x, j) => (j === i ? { ...x, template: e.target.value } : x))); setTypesDirty(true); }} /></td>
                  <td><input type="checkbox" aria-label={`Turn off ${t.name || t.key}`} checked={!!t.disabled} onChange={(e) => { setTypes(types.map((x, j) => (j === i ? { ...x, disabled: e.target.checked } : x))); setTypesDirty(true); }} /></td>
                </tr>
              ))}</tbody>
            </table></div>
          </Sheet>

          <Sheet title="Custom fields" pad>
            {fields.length === 0 && <p className="faint">{P.t11}</p>}
            {fields.map((f, i) => (
              <div className="wk-grid wk-fieldrow" key={i}>
                <div className="wk-f"><label htmlFor={`cf-k-${i}`}>Key</label><input id={`cf-k-${i}`} value={f.key} onChange={(e) => { setFields(fields.map((x, j) => (j === i ? { ...x, key: e.target.value } : x))); setFieldsDirty(true); }} /></div>
                <div className="wk-f"><label htmlFor={`cf-n-${i}`}>Name</label><input id={`cf-n-${i}`} value={f.name} onChange={(e) => { setFields(fields.map((x, j) => (j === i ? { ...x, name: e.target.value } : x))); setFieldsDirty(true); }} /></div>
                <div className="wk-f"><label htmlFor={`cf-t-${i}`}>Kind</label><select id={`cf-t-${i}`} value={f.type} onChange={(e) => { setFields(fields.map((x, j) => (j === i ? { ...x, type: e.target.value } : x))); setFieldsDirty(true); }}>{["text", "number", "date", "select", "boolean"].map((t) => <option key={t}>{t}</option>)}</select></div>
                {f.type === "select" && <div className="wk-f"><label htmlFor={`cf-o-${i}`}>Choices</label><input id={`cf-o-${i}`} value={(f.options ?? []).join(", ")} onChange={(e) => { setFields(fields.map((x, j) => (j === i ? { ...x, options: csv(e.target.value) } : x))); setFieldsDirty(true); }} /></div>}
                <label className="wk-check"><input type="checkbox" checked={!!f.required} onChange={(e) => { setFields(fields.map((x, j) => (j === i ? { ...x, required: e.target.checked } : x))); setFieldsDirty(true); }} />Required</label>
                <Button sm kind="ghost" aria-label={`Remove field ${f.name || i + 1}`} onClick={() => { setFields(fields.filter((_, j) => j !== i)); setFieldsDirty(true); }}>Remove</Button>
              </div>
            ))}
            <Button sm onClick={() => { setFields([...fields, { key: "", name: "", type: "text" }]); setFieldsDirty(true); }}>Add a field</Button>
          </Sheet>

          <Sheet title="When a pull request merges" pad>
            <div className="wk-grid">
              <div className="wk-f"><label htmlFor="cl-on">Close rule</label>
                <select id="cl-on" value={close.on} onChange={(e) => { setClose({ ...close, on: e.target.value }); setCloseDirty(true); }}>
                  <option value="merge">{P.t12}</option>
                  <option value="review">{P.t13}</option>
                  <option value="never">{P.t14}</option>
                </select></div>
              <div className="wk-f"><label htmlFor="cl-st">State it moves to</label>
                <select id="cl-st" value={close.state} onChange={(e) => { setClose({ ...close, state: e.target.value }); setCloseDirty(true); }}>{states.map((s) => <option key={s.key} value={s.key}>{s.name || s.key}</option>)}</select></div>
              <div className="wk-f span"><label htmlFor="cl-v">Words that close an item</label><input id="cl-v" value={close.verbs} onChange={(e) => { setClose({ ...close, verbs: e.target.value }); setCloseDirty(true); }} /><span className="faint small">{P.t15}</span></div>
            </div>
          </Sheet>

          <Sheet title="Agent rules" pad>
            <label className="wk-check big"><input type="checkbox" checked={rules.enabled} onChange={(e) => setRules({ ...rules, enabled: e.target.checked })} />Agents may work here</label>
            <p className="faint small">{P.t16}{scopeLabel(scope)} {P.t17}</p>
            {!rules.enabled && <p className="callout callout-bad" role="note">{P.t18}</p>}
            <label className="wk-check"><input type="checkbox" checked={rules.human} onChange={(e) => setRules({ ...rules, human: e.target.checked })} />{P.t19}</label>
            <label className="wk-check"><input type="checkbox" checked={rules.merge} onChange={(e) => setRules({ ...rules, merge: e.target.checked })} />Agents may merge</label>
            {loosened && <TextField label={P.t20} value={rules.reason} onChange={(v) => setRules({ ...rules, reason: v })} required />}
          </Sheet>

          <AutonomySection scope={scope} a={a} level={level} aut={aut} setAut={setAut} editable={editable} onResumed={onSaved} />
          <ReviewSection rev={rev} setRev={setRev} />

          <Sheet title="Difficulty and agent classes" pad>
            <p className="sub">{P.t21}</p>
            {CLASSES.map((d) => (
              <fieldset key={d} className="wk-radios inline"><legend>{d[0]!.toUpperCase() + d.slice(1)} items may be taken by</legend>
                {CLASSES.map((c) => (
                  <label key={c} className="wk-check"><input type="checkbox" checked={(classes[d] ?? []).includes(c)}
                    onChange={(e) => { const cur = classes[d] ?? []; setClasses({ ...classes, [d]: e.target.checked ? [...cur, c] : cur.filter((x) => x !== c) }); setClassesDirty(true); }} />{c}</label>
                ))}
              </fieldset>
            ))}
          </Sheet>
        </fieldset>
        <ErrorPanel error={error} />
        {saved && <p className="callout callout-ok" role="status">Saved.</p>}
        {editable && <div className="acts wk-savebar"><Button type="submit" kind="pri" disabled={busy}>Save settings</Button></div>}
      </form>
    </WorkPage>
  );
}

function TextField({ label, value, onChange, hint, required, type }: { label: string; value: string; onChange: (v: string) => void; hint?: string; required?: boolean; type?: string }) {
  const id = useId();
  return <div className="wk-f"><label htmlFor={id}>{label}</label><input id={id} type={type ?? "text"} value={value} onChange={(e) => onChange(e.target.value)} required={required} />{hint && <span className="faint small">{hint}</span>}</div>;
}

function AutonomySection({ scope, a, level, aut, setAut, editable, onResumed }: {
  scope: string; a: SettingsAnswer; level: number; aut: Autonomy | undefined; setAut: (v: Autonomy | undefined) => void; editable: boolean; onResumed: () => void;
}) {
  const st = a.autonomy;
  const [resumeWhy, setResumeWhy] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const cur: Autonomy = aut ?? { level: st.level };
  const patch = (p: Partial<Autonomy>) => setAut({ ...cur, ...p });
  const raised = level > 1;
  const resume = async () => {
    try { await workApi.clearPause(scope, resumeWhy); announce("Autonomy resumed."); onResumed(); } catch (e) { setErr(e); }
  };
  return (
    <Sheet title="Autonomy policy" pad>
      <dl className="wk-dl">
        <dt>In force now</dt><dd>Level {st.level}: {LEVELS[st.level]?.name}{st.enforced_level !== st.level && <> (the server acts on level {st.enforced_level})</>}</dd>
        <dt>Where it comes from</dt><dd>{sourceText(a, scope)}</dd>
        <dt>Agents</dt><dd>{st.agents_enabled ? "May work here" : "Switched off here"}</dd>
      </dl>
      {st.paused && (
        <div className="callout callout-wait" role="alert">
          <span>Autonomy is paused: {st.failures} of {st.pause_after} {P.t22}</span>
          {editable && (
            <span className="wk-inline">
              <input aria-label="Reason for resuming" value={resumeWhy} onChange={(e) => setResumeWhy(e.target.value)} placeholder="Why it is safe to resume" />
              <Button sm onClick={resume} disabled={!resumeWhy.trim()}>Resume</Button>
            </span>
          )}
        </div>
      )}
      <ErrorPanel error={err} />
      <fieldset className="wk-radios">
        <legend>{P.t23}</legend>
        {LEVELS.map((l) => {
          const off = l.level > 2;
          return (
            <label key={l.level} className={"wk-radio" + (off ? " off" : "")}>
              <input type="radio" name="autonomy-level" value={l.level} checked={level === l.level} disabled={off} onChange={() => patch({ level: l.level })} />
              <span><strong>Level {l.level}{off ? "" : `: ${l.name}`}</strong>{off ? <span className="faint"> Not yet available</span> : <span className="faint small block">{l.text}</span>}</span>
            </label>
          );
        })}
      </fieldset>
      {level >= 2 && (
        <div className="wk-grid">
          <TextField label={P.t24} value={(cur.paths ?? []).join(", ")} onChange={(v) => patch({ paths: csv(v) })} hint={P.t25} required />
          <TextField label="Item types" value={(cur.types ?? []).join(", ")} onChange={(v) => patch({ types: csv(v) })} hint="Empty means any type." />
          <TextField label="Most files in a change" type="number" value={String(cur.max_files ?? "")} onChange={(v) => patch({ max_files: v ? Number(v) : undefined })} hint="Empty means 10." />
          <TextField label="Most lines in a change" type="number" value={String(cur.max_lines ?? "")} onChange={(v) => patch({ max_lines: v ? Number(v) : undefined })} hint="Empty means 200." />
          <TextField label={P.t26} type="number" value={String(cur.pause_after ?? "")} onChange={(v) => patch({ pause_after: v ? Number(v) : undefined })} hint="Empty means 3." />
          <TextField label="Counted over this many days" type="number" value={String(cur.pause_days ?? "")} onChange={(v) => patch({ pause_days: v ? Number(v) : undefined })} hint="Empty means 14." />
        </div>
      )}
      {raised && <TextField label={P.t27} value={cur.reason ?? ""} onChange={(v) => patch({ reason: v })} required />}
    </Sheet>
  );
}

function ReviewSection({ rev, setRev }: { rev: ReviewPolicy | undefined; setRev: (v: ReviewPolicy | undefined) => void }) {
  const cur: ReviewPolicy = rev ?? { mode: "human_only" };
  const patch = (p: Partial<ReviewPolicy>) => setRev({ ...cur, ...p });
  const indep = cur.mode === "independent_agent_allowed";
  return (
    <Sheet title="Agent review policy" pad>
      <fieldset className="wk-radios">
        <legend>{P.t28}</legend>
        <label className="wk-radio"><input type="radio" name="review-mode" checked={!indep} onChange={() => patch({ mode: "human_only" })} /><span>Only a person's review counts<span className="faint small block">{P.t29}</span></span></label>
        <label className="wk-radio"><input type="radio" name="review-mode" checked={indep} onChange={() => patch({ mode: "independent_agent_allowed" })} /><span>{P.t30}<span className="faint small block">{P.t31}</span></span></label>
      </fieldset>
      {indep && (
        <div className="wk-grid">
          <TextField label="Paths it covers" value={(cur.paths ?? []).join(", ")} onChange={(v) => patch({ paths: csv(v) })} hint={P.t32} required />
          <TextField label="Item types" value={(cur.types ?? []).join(", ")} onChange={(v) => patch({ types: csv(v) })} />
          <TextField label="Most files" type="number" value={String(cur.max_files ?? "")} onChange={(v) => patch({ max_files: v ? Number(v) : undefined })} />
          <TextField label="Most lines" type="number" value={String(cur.max_lines ?? "")} onChange={(v) => patch({ max_lines: v ? Number(v) : undefined })} />
          <TextField label={P.t33} type="number" value={String(cur.sample_percent ?? "")} onChange={(v) => patch({ sample_percent: v ? Number(v) : undefined })} hint={P.t34} />
          <label className="wk-check"><input type="checkbox" checked={!!cur.require_different_model} onChange={(e) => patch({ require_different_model: e.target.checked })} />{P.t35}</label>
          <TextField label="Why (required, recorded)" value={cur.reason ?? ""} onChange={(v) => patch({ reason: v })} required />
        </div>
      )}
    </Sheet>
  );
}
