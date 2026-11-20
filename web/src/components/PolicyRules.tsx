// PolicyRules edits a policy's rules: each rule of the server's catalogue,
// with what it checks and when, and its options.

import { useId } from "react";
import type { RuleInfo } from "../api";
import { POINT, RULES, type RuleField } from "../configKinds";
import { splitList } from "./ConfigForm";

type Rules = Record<string, unknown>;

export function PolicyRules({ catalogue, rules, onChange, problems }: {
  catalogue: RuleInfo[]; rules: Rules; onChange: (r: Rules) => void; problems?: Record<string, string[]>;
}) {
  const toggle = (name: string, on: boolean) => {
    const next = { ...rules };
    if (on) next[name] = structuredClone(RULES[name]?.example ?? {});
    else delete next[name];
    onChange(next);
  };
  const known = new Set(catalogue.map((r) => r.name));
  const extra = Object.keys(rules).filter((r) => !known.has(r));
  return (
    <fieldset className="cf-section">
      <legend className="cf-section-title">Rules</legend>
      <p className="cf-help">A policy passes when every rule it has passes. Each rule is judged at one point of a run; tick the rules this policy has.</p>
      <ul className="cf-rules">
        {catalogue.map((r) => (
          <RuleRow key={r.name} info={r} on={r.name in rules} value={rules[r.name]} onToggle={(b) => toggle(r.name, b)}
            onValue={(v) => onChange({ ...rules, [r.name]: v })} problems={problems?.[r.name]} />
        ))}
      </ul>
      {extra.length > 0 && <p className="cf-help">Also: {extra.join(", ")} (not in this server's catalogue; see the raw tab).</p>}
    </fieldset>
  );
}

function RuleRow({ info, on, value, onToggle, onValue, problems }: {
  info: RuleInfo; on: boolean; value: unknown; onToggle: (b: boolean) => void; onValue: (v: unknown) => void; problems?: string[];
}) {
  const id = useId();
  const def = RULES[info.name];
  return (
    <li className={"cf-rule" + (on ? " cf-rule-on" : "")}>
      <label className="cf-checkline">
        <input type="checkbox" checked={on} onChange={(e) => onToggle(e.target.checked)} aria-describedby={id} />
        <span><span className="mono cf-rule-name">{info.name}</span> <span className="cf-badge">{POINT[info.point] ?? info.point}</span></span>
      </label>
      <div id={id} className="cf-help cf-help-indent">Passes when {info.summary}.</div>
      {on && (
        <div className="cf-rule-opts">
          {def?.scalar ? (
            <label className="cf-mini"><span>Longest step timeout</span>
              <input className="cf-input mono" value={typeof value === "string" ? value : ""} placeholder="2h" onChange={(e) => onValue(e.target.value)} /></label>
          ) : def?.templates ? (
            <TemplatesValue value={Array.isArray(value) ? value as Tmpl[] : []} onChange={onValue} />
          ) : def?.fields ? (
            def.fields.map((f) => <RuleOption key={f.key} f={f} value={(value as Rules | undefined)?.[f.key]}
              onChange={(v) => {
                const next = { ...((value as Rules) ?? {}) };
                if (v === undefined || v === "" || (Array.isArray(v) && !v.length) || v === false) delete next[f.key]; else next[f.key] = v;
                onValue(next);
              }} />)
          ) : (
            <label className="cf-mini cf-wide"><span>Value (JSON)</span>
              <textarea className="cf-input cf-textarea mono" defaultValue={JSON.stringify(value ?? {}, null, 1)}
                onChange={(e) => { try { onValue(JSON.parse(e.target.value)); } catch { /* finish typing */ } }} /></label>
          )}
          <div className="cf-problems" aria-live="polite">{problems?.map((p) => <div key={p} className="cf-problem">{p}</div>)}</div>
        </div>
      )}
    </li>
  );
}

function RuleOption({ f, value, onChange }: { f: RuleField; value: unknown; onChange: (v: unknown) => void }) {
  const id = useId();
  const help = f.help ? <span id={id} className="cf-help">{f.help}</span> : null;
  switch (f.type) {
    case "bool":
      return <label className="cf-checkline cf-mini-check"><input type="checkbox" checked={value === true} aria-describedby={f.help ? id : undefined}
        onChange={(e) => onChange(e.target.checked)} /><span>{f.label}</span>{help}</label>;
    case "select":
      return <label className="cf-mini"><span>{f.label}</span>
        <select className="cf-input" value={typeof value === "string" ? value : ""} aria-describedby={f.help ? id : undefined} onChange={(e) => onChange(e.target.value)}>
          {(f.options ?? []).map((o) => <option key={o} value={o}>{o || "not set"}</option>)}</select>{help}</label>;
    case "number":
      return <label className="cf-mini"><span>{f.label}</span>
        <input type="number" className="cf-input" value={typeof value === "number" ? value : ""} aria-describedby={f.help ? id : undefined}
          onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))} />{help}</label>;
    case "list":
      return <label className="cf-mini cf-wide"><span>{f.label}</span>
        <input className="cf-input mono" defaultValue={Array.isArray(value) ? value.join(", ") : ""} aria-describedby={f.help ? id : undefined}
          placeholder="separated by commas" onChange={(e) => onChange(splitList(e.target.value))} />{help}</label>;
    default:
      return <label className="cf-mini"><span>{f.label}</span>
        <input className="cf-input" value={typeof value === "string" ? value : ""} onChange={(e) => onChange(e.target.value)} />{help}</label>;
  }
}

interface Tmpl { library: string; template: string; versions?: string[] }

/** One template per line: library/template, then optionally the version patterns. */
function TemplatesValue({ value, onChange }: { value: Tmpl[]; onChange: (v: Tmpl[]) => void }) {
  const id = useId();
  const text = value.map((t) => `${t.library}/${t.template}${t.versions?.length ? " " + t.versions.join(", ") : ""}`).join("\n");
  return (
    <label className="cf-mini cf-wide"><span>Templates every run must use</span>
      <textarea className="cf-input cf-textarea mono" defaultValue={text} aria-describedby={id} spellCheck={false}
        onChange={(e) => onChange(e.target.value.split("\n").map((l) => l.trim()).filter(Boolean).map((l) => {
          const [lt, ...rest] = l.split(/\s+/);
          const [library, template = ""] = lt.split("/");
          const versions = splitList(rest.join(" "));
          return versions.length ? { library, template, versions } : { library, template };
        }))} />
      <span id={id} className="cf-help">One per line: library/template, then the versions allowed, such as <span className="mono">ci-templates/security-scan v3, v3.*</span>.</span>
    </label>
  );
}
