import { useId, useState } from "react";
import { OPERATORS, showValue, valueOf, type Clause, type Rule, type Serve, type ToggleState, type ToggleType } from "../togglesApi";
import { Button } from "../kit/layout";
import "../toggles.css";

// The editor of what a toggle does in one environment: on or off, what it serves while off, prerequisites,
// rules in order (conditions on the context, then a value or a percentage), and what everyone else gets. Every
// control is a labelled form control and every step (add, remove, move) is a button, so it works by keyboard alone.

/** A value in the toggle's type: a choice for a boolean, a field for the rest (JSON is checked as it is typed). */
function ValueInput({ type, value, onChange, label }: { type: ToggleType; value: unknown; onChange: (v: unknown) => void; label: string }) {
  const [text, setText] = useState(showValue(value));
  const [bad, setBad] = useState("");
  const id = useId();
  if (type === "boolean") {
    return <select aria-label={label} value={value === true ? "true" : "false"} onChange={(e) => onChange(e.target.value === "true")}><option value="true">true</option><option value="false">false</option></select>;
  }
  return (
    <span className="tg-valfield">
      <input aria-label={label} aria-invalid={bad ? true : undefined} aria-describedby={bad ? id : undefined} value={text} inputMode={type === "number" ? "decimal" : undefined}
        onChange={(e) => {
          setText(e.target.value);
          try { const v = valueOf(type, e.target.value); if (type === "number" && Number.isNaN(v)) throw new Error("a number"); setBad(""); onChange(v); }
          catch { setBad(type === "number" ? "That is not a number." : "That is not valid JSON."); }
        }} />
      {bad && <span id={id} className="tg-bad" role="alert">{bad}</span>}
    </span>
  );
}

/** What a rule or the fallthrough serves: a value, or a percentage of keys that get a value (the rest get another). */
function ServeEditor({ type, serve, onChange, label }: { type: ToggleType; serve: Serve; onChange: (s: Serve) => void; label: string }) {
  const buckets = serve.rollout?.buckets ?? [];
  const many = buckets.length > 2;
  const mode = serve.rollout ? "rollout" : "value";
  const first = buckets[0], second = buckets[1];
  const setPct = (pct: number) => {
    const w = Math.max(0, Math.min(10000, Math.round(pct * 100)));
    onChange({ rollout: { by: serve.rollout?.by, buckets: [{ value: first?.value ?? defaultOn(type), weight: w }, { value: second?.value ?? defaultOff(type), weight: 10000 - w }] } });
  };
  return (
    <fieldset className="tg-serve">
      <legend>{label}</legend>
      <div className="tg-row">
        <label><input type="radio" name={label} checked={mode === "value"} onChange={() => onChange({ value: first?.value ?? serve.value ?? defaultOn(type) })} /> One value for everyone</label>
        <label><input type="radio" name={label} checked={mode === "rollout"} onChange={() => setPct(25)} /> A percentage of keys</label>
      </div>
      {mode === "value" && <div className="tg-row"><span>Serve</span><ValueInput type={type} label={`${label}: the value`} value={serve.value} onChange={(v) => onChange({ value: v })} /></div>}
      {mode === "rollout" && !many && first && second && (
        <div className="tg-row">
          <label>Percent of keys <input type="number" min={0} max={100} step={0.01} value={first.weight / 100} onChange={(e) => setPct(Number(e.target.value))} /></label>
          <span>get</span><ValueInput type={type} label={`${label}: the value for that percent`} value={first.value} onChange={(v) => onChange({ rollout: { ...serve.rollout!, buckets: [{ ...first, value: v }, second] } })} />
          <span>and the rest get</span><ValueInput type={type} label={`${label}: the value for the rest`} value={second.value} onChange={(v) => onChange({ rollout: { ...serve.rollout!, buckets: [first, { ...second, value: v }] } })} />
          <label>taken over <input value={serve.rollout?.by ?? ""} placeholder="key" onChange={(e) => onChange({ rollout: { ...serve.rollout!, by: e.target.value || undefined } })} /></label>
        </div>
      )}
      {mode === "rollout" && many && <p className="sub">This rollout has {buckets.length} shares. Edit it in the toggles file or with <code>launchway toggles rules</code>; it is kept as it is here.</p>}
      {mode === "rollout" && <p className="sub tg-help">The same key always lands in the same share, so raising the percentage keeps everyone who already has it.</p>}
    </fieldset>
  );
}

const defaultOn = (t: ToggleType): unknown => (t === "boolean" ? true : t === "number" ? 1 : t === "json" ? {} : "on");
const defaultOff = (t: ToggleType): unknown => (t === "boolean" ? false : t === "number" ? 0 : t === "json" ? null : "off");

function ClauseRow({ c, i, onChange, onRemove, segments }: { c: Clause; i: number; onChange: (c: Clause) => void; onRemove: () => void; segments: string[] }) {
  const op = OPERATORS.find((o) => o.id === c.op) ?? OPERATORS[0]!;
  const valuesText = (c.values ?? []).map((v) => (typeof v === "string" ? v : JSON.stringify(v))).join(", ");
  const parse = (text: string): unknown[] => text.split(",").map((s) => s.trim()).filter(Boolean).map((s) => (c.op.startsWith("number_") ? Number(s) : s === "true" ? true : s === "false" ? false : /^-?\d+(\.\d+)?$/.test(s) && !c.op.startsWith("semver_") && c.op !== "prefix" && c.op !== "suffix" && c.op !== "contains" ? Number(s) : s));
  return (
    <li className="tg-clause">
      {c.op !== "in_segment" && <label>Attribute <input value={c.attribute ?? ""} aria-label={`Condition ${i + 1}: the context attribute`} placeholder="plan" onChange={(e) => onChange({ ...c, attribute: e.target.value })} /></label>}
      <select aria-label={`Condition ${i + 1}: the test`} value={c.op} onChange={(e) => onChange({ ...c, op: e.target.value })}>{OPERATORS.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}</select>
      {op.needs !== "none" && (c.op === "in_segment" && segments.length > 0
        ? <select aria-label={`Condition ${i + 1}: the segment`} value={String(c.values?.[0] ?? "")} onChange={(e) => onChange({ ...c, values: [e.target.value] })}><option value="">choose…</option>{segments.map((s) => <option key={s} value={s}>{s}</option>)}</select>
        : <input aria-label={`Condition ${i + 1}: the value${op.needs === "many" ? "s, separated by commas" : ""}`} defaultValue={valuesText} placeholder={op.needs === "many" ? "a, b, c" : "value"} onBlur={(e) => onChange({ ...c, values: parse(e.target.value) })} />)}
      <label className="tg-check"><input type="checkbox" checked={!!c.negate} onChange={(e) => onChange({ ...c, negate: e.target.checked })} /> not</label>
      <Button sm type="button" onClick={onRemove} aria-label={`Remove condition ${i + 1}`}>Remove</Button>
    </li>
  );
}

function RuleEditor({ r, n, count, type, segments, onChange, onMove, onRemove }: { r: Rule; n: number; count: number; type: ToggleType; segments: string[]; onChange: (r: Rule) => void; onMove: (d: -1 | 1) => void; onRemove: () => void }) {
  const clauses = r.clauses ?? [];
  return (
    <li className="tg-rule">
      <fieldset>
        <legend>Rule {n + 1}{r.id ? <span className="faint"> ({r.id})</span> : null}: the first rule that matches decides</legend>
        <p className="sub">{clauses.length === 0 ? "No conditions: this rule matches everyone." : "When all of these are true:"}</p>
        <ul className="tg-clauses">{clauses.map((c, i) => <ClauseRow key={i} c={c} i={i} segments={segments} onChange={(x) => onChange({ ...r, clauses: clauses.map((y, j) => (j === i ? x : y)) })} onRemove={() => onChange({ ...r, clauses: clauses.filter((_, j) => j !== i) })} />)}</ul>
        <Button sm type="button" onClick={() => onChange({ ...r, clauses: [...clauses, { attribute: "", op: "equals", values: [] }] })}>Add a condition</Button>
        <ServeEditor type={type} label={`Rule ${n + 1} serves`} serve={r.serve} onChange={(serve) => onChange({ ...r, serve })} />
        <div className="tg-row">
          <Button sm type="button" disabled={n === 0} onClick={() => onMove(-1)} aria-label={`Move rule ${n + 1} up`}>Move up</Button>
          <Button sm type="button" disabled={n === count - 1} onClick={() => onMove(1)} aria-label={`Move rule ${n + 1} down`}>Move down</Button>
          <Button sm type="button" onClick={onRemove} aria-label={`Remove rule ${n + 1}`}>Remove the rule</Button>
        </div>
      </fieldset>
    </li>
  );
}

export function ToggleRules({ type, state, onChange, segments, otherToggles }: { type: ToggleType; state: ToggleState; onChange: (s: ToggleState) => void; segments: string[]; otherToggles: string[] }) {
  const rules = state.rules ?? [];
  const prereqs = state.prerequisites ?? [];
  const setRules = (rs: Rule[]) => onChange({ ...state, rules: rs });
  return (
    <div className="tg-editor">
      <label className="tg-check"><input type="checkbox" checked={state.on} onChange={(e) => onChange({ ...state, on: e.target.checked })} /> On in this environment</label>
      <div className="tg-row"><span>While off, serve</span><ValueInput type={type} label="The value while off" value={state.off_value} onChange={(v) => onChange({ ...state, off_value: v })} /></div>
      {prereqs.length > 0 && <h3>Only when these toggles serve</h3>}
      <ul className="tg-clauses">
        {prereqs.map((p, i) => (
          <li key={i} className="tg-clause">
            <select aria-label={`Prerequisite ${i + 1}: the toggle`} value={p.toggle} onChange={(e) => onChange({ ...state, prerequisites: prereqs.map((x, j) => (j === i ? { ...x, toggle: e.target.value } : x)) })}>
              <option value="">choose…</option>{otherToggles.map((k) => <option key={k} value={k}>{k}</option>)}
            </select>
            <span>serves</span>
            <select aria-label={`Prerequisite ${i + 1}: the value`} value={p.value === true ? "true" : p.value === false ? "false" : String(p.value)} onChange={(e) => onChange({ ...state, prerequisites: prereqs.map((x, j) => (j === i ? { ...x, value: e.target.value === "true" ? true : e.target.value === "false" ? false : e.target.value } : x)) })}>
              <option value="true">true</option><option value="false">false</option>
            </select>
            <Button sm type="button" onClick={() => onChange({ ...state, prerequisites: prereqs.filter((_, j) => j !== i) })} aria-label={`Remove prerequisite ${i + 1}`}>Remove</Button>
          </li>
        ))}
      </ul>
      {otherToggles.length > 0 && <div><Button sm type="button" onClick={() => onChange({ ...state, prerequisites: [...prereqs, { toggle: "", value: true }] })}>Add a prerequisite</Button></div>}
      <h3>Rules</h3>
      {rules.length === 0 && <p className="sub">No rules: everyone gets what is below.</p>}
      <ol className="tg-rules">
        {rules.map((r, i) => (
          <RuleEditor key={r.id ?? i} r={r} n={i} count={rules.length} type={type} segments={segments}
            onChange={(x) => setRules(rules.map((y, j) => (j === i ? x : y)))}
            onMove={(d) => { const out = [...rules]; const [m] = out.splice(i, 1); out.splice(i + d, 0, m!); setRules(out); }}
            onRemove={() => setRules(rules.filter((_, j) => j !== i))} />
        ))}
      </ol>
      <div><Button sm type="button" onClick={() => setRules([...rules, { clauses: [{ attribute: "", op: "equals", values: [] }], serve: { value: defaultOn(type) } }])}>Add a rule</Button></div>
      <ServeEditor type={type} label="Everyone else gets" serve={state.fallthrough} onChange={(f) => onChange({ ...state, fallthrough: f })} />
    </div>
  );
}
