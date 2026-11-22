// Building blocks of the configuration pages and wizards: fields with an
// explanation under each, the server's own validation as people type, the
// choice cards wizards ask with, the wizard frame, and readable summaries
// and diffs of specs.

import { PageLearn } from "../learn/LearnLink";
import { PageHead } from "../kit/layout";
import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { configApi, type ChangeRequest, type ConfigOverview, type Spec } from "../configApi";
import { navigate } from "../router";
import { terms } from "../terms";
import { sayField } from "../configKinds";
import { BtnPrimary, BtnSm, SpanFaint, SpanFaintSmall, SpanHidden } from "../kit/bits";

// ---- spec paths ----

/** Keys whose empty object means something (push: {} runs on every push). */
const KEEP_EMPTY = new Set(["push", "pull_request"]);

export function getPath(obj: unknown, path: string): unknown {
  let cur: unknown = obj;
  for (const k of path.split(".")) {
    if (cur == null || typeof cur !== "object") return undefined;
    cur = (cur as Record<string, unknown>)[k];
  }
  return cur;
}

function empty(v: unknown): boolean {
  return v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0);
}

/** setPath returns a copy of obj with path set; an empty value removes the key,
 * and objects left empty by that are removed too (except push and pull_request). */
export function setPath(obj: Spec, path: string, value: unknown): Spec {
  const keys = path.split(".");
  const out: Spec = { ...obj };
  let cur: Record<string, unknown> = out;
  const chain: [Record<string, unknown>, string][] = [];
  for (let i = 0; i < keys.length - 1; i++) {
    const k = keys[i];
    const next = cur[k];
    const copy = next && typeof next === "object" && !Array.isArray(next) ? { ...(next as Record<string, unknown>) } : {};
    cur[k] = copy;
    chain.push([cur, k]);
    cur = copy;
  }
  const last = keys[keys.length - 1];
  if (empty(value)) delete cur[last];
  else cur[last] = value;
  for (let i = chain.length - 1; i >= 0; i--) {
    const [parent, k] = chain[i];
    const v = parent[k] as Record<string, unknown>;
    if (Object.keys(v).length === 0 && !KEEP_EMPTY.has(k)) delete parent[k];
    else break;
  }
  return out;
}

export function splitList(s: string): string[] {
  return s.split(/[,\n]/).map((x) => x.trim()).filter(Boolean);
}

// ---- field definitions ----

export interface Option { value: string; label: string; help?: ReactNode }

export interface FieldDef {
  /** Where the value is in the spec: pipeline.on.push.branches. */
  path: string;
  label: string;
  /** What it is and what it does, in a sentence or two. */
  help: string;
  type: "text" | "number" | "bool" | "tri" | "list" | "select" | "map" | "secretref" | "presence" | "textarea" | "duration";
  options?: Option[];
  placeholder?: string;
  /** Words in the server's messages that mean this field; default: the last key. */
  match?: string[];
  /** A heading the field is shown under. */
  section?: string;
  showIf?: (spec: Spec) => boolean;
  /** tri: what unset means. */
  unsetMeans?: string;
  /** Takes the whole row (long values such as addresses). */
  wide?: boolean;
}

/** Puts each of the server's messages next to the field it is about; the
 * others are returned in rest. */
export function assignProblems(problems: string[], fields: FieldDef[]): { by: Record<string, string[]>; rest: string[] } {
  const by: Record<string, string[]> = {};
  const rest: string[] = [];
  // The field whose word in the message is the longest (the most specific) gets it.
  for (const p of problems) {
    let best: FieldDef | null = null;
    let bestLen = 0;
    for (const f of fields) {
      for (const t of f.match ?? [f.path, f.path.split(".").pop()!]) {
        if (t.length > bestLen && wordIn(p, t)) { best = f; bestLen = t.length; }
      }
    }
    if (best) (by[best.path] ??= []).push(p);
    else rest.push(p);
  }
  return { by, rest };
}

function wordIn(text: string, word: string): boolean {
  const esc = word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(^|[^A-Za-z0-9_])${esc}($|[^A-Za-z0-9_])`).test(text);
}

// ---- fields ----

/** Field lays out a label, the control, its explanation and its problems;
 * the control gets the ids to point at them. */
export function Field({ label, help, problems, children, wide, required }: {
  label: string; help?: ReactNode; problems?: string[]; wide?: boolean; required?: boolean;
  children: (ids: { id: string; describedBy: string; invalid: boolean }) => ReactNode;
}) {
  const id = useId();
  const helpId = id + "-help";
  const probId = id + "-prob";
  const invalid = !!problems?.length;
  const describedBy = [help ? helpId : "", invalid ? probId : ""].filter(Boolean).join(" ");
  return (
    <div className={"cf-field" + (wide ? " cf-wide" : "") + (invalid ? " cf-invalid" : "")}>
      <label htmlFor={id} className="cf-label">{label}{required && <span className="cf-req" aria-hidden="true"> *</span>}</label>
      {children({ id, describedBy, invalid })}
      {help && <div id={helpId} className="cf-help">{help}</div>}
      <div id={probId} className="cf-problems" aria-live="polite">
        {problems?.map((p) => <div key={p} className="cf-problem">{p}</div>)}
      </div>
    </div>
  );
}

export function TextInput({ label, help, value, onChange, problems, placeholder, required, mono, type = "text", focusOnMount, wide, list }: {
  label: string; help?: ReactNode; value: string; onChange: (v: string) => void; problems?: string[];
  placeholder?: string; required?: boolean; mono?: boolean; type?: string; focusOnMount?: boolean; wide?: boolean; list?: string;
}) {
  // Focus is moved once, when a wizard step or form opens, so the first question is ready to type into.
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { if (focusOnMount) ref.current?.focus(); }, []); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Field label={label} help={help} problems={problems} required={required} wide={wide}>
      {({ id, describedBy, invalid }) => (
        <input ref={ref} id={id} type={type} value={value} placeholder={placeholder} list={list} className={"cf-input" + (mono ? " mono" : "")}
          aria-describedby={describedBy || undefined} aria-invalid={invalid || undefined} aria-required={required || undefined}
          spellCheck={false} autoComplete="off" onChange={(e) => onChange(e.target.value)} />
      )}
    </Field>
  );
}

/** ListInput edits a list as text separated by commas or lines. */
export function ListInput({ label, help, value, onChange, problems, placeholder, wide }: {
  label: string; help?: ReactNode; value: string[]; onChange: (v: string[]) => void; problems?: string[]; placeholder?: string; wide?: boolean;
}) {
  const [text, setText] = useState(value.join(", "));
  const last = useRef(value);
  // Follow changes from elsewhere (the raw tab, a reload) without fighting the typist.
  useEffect(() => {
    if (JSON.stringify(last.current) !== JSON.stringify(value)) { setText(value.join(", ")); last.current = value; }
  }, [value]);
  return (
    <Field label={label} help={help} problems={problems} wide={wide}>
      {({ id, describedBy, invalid }) => (
        <input id={id} value={text} placeholder={placeholder} className="cf-input mono" spellCheck={false} autoComplete="off"
          aria-describedby={describedBy || undefined} aria-invalid={invalid || undefined}
          onChange={(e) => { setText(e.target.value); const l = splitList(e.target.value); last.current = l; onChange(l); }} />
      )}
    </Field>
  );
}

export function SelectInput({ label, help, value, onChange, options, problems }: {
  label: string; help?: ReactNode; value: string; onChange: (v: string) => void; options: Option[]; problems?: string[];
}) {
  return (
    <Field label={label} help={help} problems={problems}>
      {({ id, describedBy, invalid }) => (
        <select id={id} value={value} className="cf-input" aria-describedby={describedBy || undefined} aria-invalid={invalid || undefined}
          onChange={(e) => onChange(e.target.value)}>
          {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
      )}
    </Field>
  );
}

export function CheckInput({ label, help, checked, onChange, problems }: {
  label: string; help?: ReactNode; checked: boolean; onChange: (v: boolean) => void; problems?: string[];
}) {
  const id = useId();
  return (
    <div className={"cf-field cf-check" + (problems?.length ? " cf-invalid" : "")}>
      <label className="cf-checkline">
        <input type="checkbox" checked={checked} aria-describedby={help ? id + "-help" : undefined} onChange={(e) => onChange(e.target.checked)} />
        <span className="cf-label">{label}</span>
      </label>
      {help && <div id={id + "-help"} className="cf-help cf-help-indent">{help}</div>}
      <div className="cf-problems" aria-live="polite">{problems?.map((p) => <div key={p} className="cf-problem">{p}</div>)}</div>
    </div>
  );
}

/** Choices asks one question with radio cards: each option says what it
 * does and what follows from it. */
export function Choices({ legend, help, value, onChange, options, problems, name }: {
  legend: string; help?: ReactNode; value: string; onChange: (v: string) => void; options: (Option & { disabled?: boolean; badge?: string })[];
  problems?: string[]; name?: string;
}) {
  const id = useId();
  return (
    <fieldset className="cf-choices" aria-describedby={help ? id + "-help" : undefined}>
      <legend className="cf-legend">{legend}</legend>
      {help && <div id={id + "-help"} className="cf-help">{help}</div>}
      <div className="cf-cards">
        {options.map((o) => (
          <label key={o.value} className={"cf-card" + (value === o.value ? " cf-card-on" : "") + (o.disabled ? " cf-card-off" : "")}>
            <input type="radio" name={name ?? id} value={o.value} checked={value === o.value} disabled={o.disabled}
              aria-describedby={o.help ? `${id}-${o.value}` : undefined} onChange={() => onChange(o.value)} />
            <span className="cf-card-body">
              <span className="cf-card-title">{o.label}{o.badge && <span className="cf-badge">{o.badge}</span>}</span>
              {o.help && <span id={`${id}-${o.value}`} className="cf-card-help">{o.help}</span>}
            </span>
          </label>
        ))}
      </div>
      <div className="cf-problems" aria-live="polite">{problems?.map((p) => <div key={p} className="cf-problem">{p}</div>)}</div>
    </fieldset>
  );
}

/** One field of a spec, drawn by its definition. */
export function SpecField({ f: f0, spec, onChange, problems }: { f: FieldDef; spec: Spec; onChange: (s: Spec) => void; problems?: string[] }) {
  const f = sayField(f0);
  const v = getPath(spec, f.path);
  const set = (x: unknown) => onChange(setPath(spec, f.path, x));
  switch (f.type) {
    case "bool":
      return <CheckInput label={f.label} help={f.help} checked={v === true} onChange={(b) => set(b || undefined)} problems={problems} />;
    case "presence":
      return <CheckInput label={f.label} help={f.help} checked={v !== undefined} onChange={(b) => set(b ? {} : undefined)} problems={problems} />;
    case "tri":
      return (
        <SelectInput label={f.label} help={f.help} problems={problems} value={v === true ? "yes" : v === false ? "no" : ""}
          onChange={(x) => set(x === "yes" ? true : x === "no" ? false : undefined)}
          options={[{ value: "", label: f.unsetMeans ?? "Not set (the default)" }, { value: "yes", label: "Yes" }, { value: "no", label: "No" }]} />
      );
    case "select":
      return (
        <SelectInput label={f.label} help={f.help} problems={problems} value={typeof v === "string" ? v : ""} onChange={(x) => set(x || undefined)}
          options={f.options ?? []} />
      );
    case "list":
      return <ListInput label={f.label} help={f.help} problems={problems} value={Array.isArray(v) ? (v as string[]) : []} onChange={set} placeholder={f.placeholder} />;
    case "number":
      return (
        <TextInput label={f.label} help={f.help} problems={problems} type="number" placeholder={f.placeholder}
          value={typeof v === "number" ? String(v) : ""} onChange={(x) => set(x === "" ? undefined : Number(x))} />
      );
    case "map":
      return <MapInput label={f.label} help={f.help} problems={problems} value={(v as Record<string, string>) ?? {}} onChange={(m) => set(Object.keys(m).length ? m : undefined)} />;
    case "secretref":
      return <SecretRefInput label={f.label} help={f.help} problems={problems} value={(v as SecretRef) ?? null} onChange={(r) => set(r ?? undefined)} />;
    case "textarea":
      return (
        <Field label={f.label} help={f.help} problems={problems} wide>
          {({ id, describedBy }) => <textarea id={id} className="cf-input cf-textarea" value={typeof v === "string" ? v : ""}
            aria-describedby={describedBy || undefined} onChange={(e) => set(e.target.value)} />}
        </Field>
      );
    default:
      return <TextInput label={f.label} help={f.help} problems={problems} placeholder={f.placeholder} mono={f.type === "duration" || f.wide} wide={f.wide}
        value={typeof v === "string" ? v : v == null ? "" : String(v)} onChange={(x) => set(x)} />;
  }
}

/** SpecForm draws a kind's fields, grouped by section. */
export function SpecForm({ fields, spec, onChange, problems }: { fields: FieldDef[]; spec: Spec; onChange: (s: Spec) => void; problems: Record<string, string[]> }) {
  const shown = fields.filter((f) => !f.showIf || f.showIf(spec));
  const sections: { title: string; fields: FieldDef[] }[] = [];
  for (const f of shown) {
    const t = terms().say(f.section ?? "");
    let s = sections.find((x) => x.title === t);
    if (!s) sections.push((s = { title: t, fields: [] }));
    s.fields.push(f);
  }
  // Many sections (the installation's settings): each folds, the first and any with a problem open.
  const fold = sections.filter((x) => x.title).length > 3;
  return (
    <div className="cf-form">
      {sections.map((s, i) => {
        const grid = <div className="cf-grid">{s.fields.map((f) => <SpecField key={f.path} f={f} spec={spec} onChange={onChange} problems={problems[f.path]} />)}</div>;
        if (!s.title) return <div key="_">{grid}</div>;
        if (fold) {
          const bad = s.fields.some((f) => problems[f.path]?.length);
          return (
            <details key={s.title} className="cf-section cf-fold" open={i === 0 || bad || undefined}>
              <summary className="cf-section-title">{s.title}</summary>
              <fieldset className="cf-plain"><legend className="visually-hidden">{s.title}</legend>{grid}</fieldset>
            </details>
          );
        }
        return (
          <fieldset key={s.title} className="cf-section">
            <legend className="cf-section-title">{s.title}</legend>
            {grid}
          </fieldset>
        );
      })}
    </div>
  );
}

export function MapInput({ label, help, value, onChange, problems }: {
  label: string; help?: ReactNode; value: Record<string, string>; onChange: (m: Record<string, string>) => void; problems?: string[];
}) {
  const fmt = (m: Record<string, string>) => Object.entries(m).map(([k, v]) => `${k}=${v}`).join("\n");
  const [text, setText] = useState(fmt(value));
  const last = useRef(JSON.stringify(value));
  useEffect(() => {
    const j = JSON.stringify(value);
    if (j !== last.current) { setText(fmt(value)); last.current = j; }
  }, [value]);
  return (
    <Field label={label} help={help} problems={problems} wide>
      {({ id, describedBy }) => (
        <textarea id={id} className="cf-input cf-textarea mono" value={text} placeholder="name=value, one per line" spellCheck={false}
          aria-describedby={describedBy || undefined}
          onChange={(e) => {
            setText(e.target.value);
            const m: Record<string, string> = {};
            for (const line of e.target.value.split("\n")) {
              const i = line.indexOf("=");
              if (i > 0) m[line.slice(0, i).trim()] = line.slice(i + 1).trim();
            }
            last.current = JSON.stringify(m);
            onChange(m);
          }} />
      )}
    </Field>
  );
}

export interface SecretRef { store: string; key: string; field?: string }

/** A reference to a secret in a store the runners have: never the secret. */
export function SecretRefInput({ label, help, value, onChange, problems }: {
  label: string; help?: ReactNode; value: SecretRef | null; onChange: (r: SecretRef | null) => void; problems?: string[];
}) {
  const id = useId();
  const v = value ?? { store: "", key: "" };
  const set = (patch: Partial<SecretRef>) => {
    const n = { ...v, ...patch };
    if (!n.field) delete n.field;
    onChange(!n.store && !n.key && !n.field ? null : n);
  };
  return (
    <fieldset className={"cf-field cf-wide cf-subset" + (problems?.length ? " cf-invalid" : "")} aria-describedby={help ? id + "-help" : undefined}>
      <legend className="cf-label">{label}</legend>
      <div className="cf-ref">
        <label className="cf-mini"><span>Store</span><input className="cf-input mono" value={v.store} placeholder="vault" onChange={(e) => set({ store: e.target.value.trim() })} /></label>
        <label className="cf-mini"><span>Key</span><input className="cf-input mono" value={v.key} placeholder="ci/models" onChange={(e) => set({ key: e.target.value.trim() })} /></label>
        <label className="cf-mini"><span>Field (optional)</span><input className="cf-input mono" value={v.field ?? ""} placeholder="api_key" onChange={(e) => set({ field: e.target.value.trim() })} /></label>
      </div>
      {help && <div id={id + "-help"} className="cf-help">{help}</div>}
      <div className="cf-problems" aria-live="polite">{problems?.map((p) => <div key={p} className="cf-problem">{p}</div>)}</div>
    </fieldset>
  );
}

/** Where an object is defined: the organisation or one of its units.
 * Shown only when units are in use. */
export function DefinedAt({ overview, value, onChange, project, problems }: {
  overview: ConfigOverview | null; value: string; onChange: (u: string) => void; project?: boolean; problems?: string[];
}) {
  if (!overview?.units_in_use) return null;
  const lv = overview.levels;
  const org = overview.organisation?.display_name || lv.organisation.singular;
  return (
    <SelectInput label={project ? `${lv.unit.singular} it is in` : "Defined at"} value={value} onChange={onChange} problems={problems}
      help={project
        ? terms().say(`The ${lv.unit.singular.toLowerCase()}'s roles, libraries, policies, {environments}, {runner_groups} and secret stores apply to this ${lv.project.singular.toLowerCase()}.`)
        : `Defined at a ${lv.unit.singular.toLowerCase()}, it applies only to that ${lv.unit.singular.toLowerCase()}'s ${lv.project.plural.toLowerCase()}; at the ${lv.organisation.singular.toLowerCase()}, to all of them.`}
      options={[{ value: "", label: project ? `None (directly in ${org})` : `${lv.organisation.singular}: ${org}` },
        ...(overview.units ?? []).map((u) => ({ value: u.name, label: `${lv.unit.singular}: ${u.display_name || u.name}` }))]} />
  );
}

// ---- the server's validation, as people answer ----

export interface ServerCheck {
  problems: string[];
  checking: boolean;
  /** The server cannot check (an error that is not a problem). */
  failure: unknown;
  /** Checks now and returns the problems. */
  now: () => Promise<string[]>;
}

/** useServerCheck asks the server to validate the changes half a second
 * after they stop changing. null checks nothing. */
export function useServerCheck(changes: ChangeRequest[] | null, delay = 450): ServerCheck {
  const key = changes ? JSON.stringify(changes) : "";
  const [problems, setProblems] = useState<string[]>([]);
  const [checking, setChecking] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  const seq = useRef(0);
  const run = useCallback(async (k: string): Promise<string[]> => {
    if (!k) { setProblems([]); return []; }
    const n = ++seq.current;
    setChecking(true);
    try {
      const ps = await configApi.validate(JSON.parse(k));
      if (n === seq.current) { setProblems(ps); setFailure(null); }
      return ps;
    } catch (e) {
      if (n === seq.current) setFailure(e);
      throw e;
    } finally {
      if (n === seq.current) setChecking(false);
    }
  }, []);
  useEffect(() => {
    if (!key) return;
    const t = window.setTimeout(() => { run(key).catch(() => {}); }, delay);
    return () => window.clearTimeout(t);
  }, [key, delay, run]);
  const now = useCallback(() => run(key), [run, key]);
  return { problems: key ? problems : [], checking, failure, now };
}

/** CheckLine says whether the server's check passed, in a live region. */
export function CheckLine({ check, okText = "The server's checks pass." }: { check: ServerCheck; okText?: string }) {
  return (
    <div className="cf-checkstate" aria-live="polite">
      {check.checking ? <SpanFaint>Checking with the server…</SpanFaint>
        : check.failure ? <span className="cf-problem">The server could not check this: {check.failure instanceof Error ? check.failure.message : String(check.failure)}</span>
        : check.problems.length === 0 ? <span className="cf-ok">{okText}</span>
        : <span className="cf-problem">{check.problems.length === 1 ? "The server found a problem." : `The server found ${check.problems.length} problems.`}</span>}
    </div>
  );
}

export function ProblemList({ problems, title }: { problems: string[]; title?: string }) {
  if (!problems.length) return null;
  return (
    <div className="callout callout-bad cf-problemlist" role="alert">
      {title && <strong>{title}</strong>}
      <ul>{problems.map((p) => <li key={p}>{p}</li>)}</ul>
    </div>
  );
}

// ---- copying ----

export function CopyBlock({ title, text, note }: { title: string; text: string; note?: ReactNode }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try { await navigator.clipboard.writeText(text); setCopied(true); window.setTimeout(() => setCopied(false), 2000); } catch { setCopied(false); }
  };
  return (
    <div className="cf-copy">
      <div className="cf-copy-head">
        <span className="cf-copy-title">{title}</span>
        <BtnSm onClick={copy} aria-label={`Copy: ${title}`}>{copied ? "Copied" : "Copy"}</BtnSm>
      </div>
      {note && <div className="cf-help">{note}</div>}
      <pre className="code cf-code">{text}</pre>
      <SpanHidden aria-live="polite">{copied ? "Copied to the clipboard" : ""}</SpanHidden>
    </div>
  );
}

// ---- showing specs ----

/** toYAML writes plain JSON values the way the configuration file would. */
export function toYAML(v: unknown): string {
  if (!isComplex(v)) return inline(v);
  return yamlLines(v, 0).join("\n");
}

function isComplex(v: unknown): boolean {
  if (Array.isArray(v)) return v.some((x) => x !== null && typeof x === "object");
  return v !== null && typeof v === "object" && Object.keys(v as object).length > 0;
}

function inline(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(scalar).join(", ")}]`;
  if (v !== null && typeof v === "object") return "{}";
  if (v === null || v === undefined) return "null";
  return scalar(v);
}

function yamlLines(v: unknown, indent: number): string[] {
  const pad = "  ".repeat(indent);
  if (Array.isArray(v)) {
    const out: string[] = [];
    for (const x of v) {
      if (!isComplex(x)) { out.push(`${pad}- ${inline(x)}`); continue; }
      const inner = yamlLines(x, indent + 1);
      inner[0] = pad + "- " + inner[0].slice(pad.length + 2);
      out.push(...inner);
    }
    return out;
  }
  const out: string[] = [];
  for (const [k, x] of Object.entries(v as Record<string, unknown>)) {
    if (isComplex(x)) out.push(`${pad}${k}:`, ...yamlLines(x, indent + 1));
    else out.push(`${pad}${k}: ${inline(x)}`);
  }
  return out;
}

function scalar(v: unknown): string {
  if (typeof v === "string") return /^[A-Za-z0-9_./@:-][A-Za-z0-9_./@:\- ]*$/.test(v) && !/^(true|false|null|yes|no|on|off|\d.*)$/i.test(v) && !v.endsWith(" ") && !v.includes(": ") ? v : JSON.stringify(v);
  return String(v);
}

export function SpecYAML({ spec }: { spec: unknown }) {
  return <pre className="code cf-code">{toYAML(spec)}</pre>;
}

/** The differences between two specs, key by key. */
export function specDiff(before: unknown, after: unknown): { path: string; before?: string; after?: string }[] {
  const a = flatten(before), b = flatten(after);
  const keys = Array.from(new Set([...Object.keys(a), ...Object.keys(b)])).sort();
  return keys.filter((k) => a[k] !== b[k]).map((k) => ({ path: k, before: a[k], after: b[k] }));
}

function flatten(v: unknown, prefix = "", out: Record<string, string> = {}): Record<string, string> {
  if (v && typeof v === "object" && !Array.isArray(v)) {
    const e = Object.entries(v as Record<string, unknown>);
    if (e.length === 0 && prefix) out[prefix] = "{}";
    for (const [k, x] of e) flatten(x, prefix ? `${prefix}.${k}` : k, out);
  } else if (Array.isArray(v) && v.some((x) => x && typeof x === "object")) {
    v.forEach((x, i) => flatten(x, `${prefix}[${i}]`, out));
  } else if (prefix) {
    out[prefix] = Array.isArray(v) ? `[${v.map(scalar).join(", ")}]` : scalar(v);
  }
  return out;
}

export function DiffTable({ rows }: { rows: { path: string; before?: string; after?: string }[] }) {
  if (!rows.length) return <p className="faint small">No setting changed.</p>;
  return (
    <ul className="cf-diff">
      {rows.map((r) => (
        <li key={r.path}>
          <span className="mono cf-diff-key">{r.path}</span>
          <span className="cf-diff-vals">
            {r.before !== undefined && <del className="cf-del mono" aria-label={`was ${r.before}`}>{r.before}</del>}
            {r.before !== undefined && r.after !== undefined && <SpanFaint aria-hidden="true"> → </SpanFaint>}
            {r.after !== undefined ? <ins className="cf-ins mono" aria-label={`now ${r.after}`}>{r.after}</ins> : <SpanFaintSmall> (removed)</SpanFaintSmall>}
          </span>
        </li>
      ))}
    </ul>
  );
}

// ---- wizards ----

export interface WizardStep { id: string; title: string }

/** WizardFrame draws a wizard: where it stands, the current step, and Back,
 * Next and Cancel. Focus moves to the step's heading when the step changes. */
export function WizardFrame({ title, intro, steps, at, children, onBack, onNext, nextLabel = "Next", busy, nextDisabled, hideNav, cancelTo = "/config" }: {
  title: string; intro?: ReactNode; steps: WizardStep[]; at: number; children: ReactNode;
  onBack?: () => void; onNext?: () => void; nextLabel?: string; busy?: boolean; nextDisabled?: boolean; hideNav?: boolean; cancelTo?: string;
}) {
  const head = useRef<HTMLHeadingElement>(null);
  const first = useRef(true);
  useEffect(() => {
    if (first.current) { first.current = false; return; }
    head.current?.focus();
  }, [at]);
  const cancel = () => {
    if (window.confirm("Leave this wizard? Nothing has been created, and your answers will be lost.")) navigate(cancelTo);
  };
  const step = steps[at];
  return (
    <div className="page cf-wizard">
      <PageHead eyebrow="Settings · Set-up" title={title} sentence={intro} actions={<button type="button" className="btn btn-ghost" onClick={cancel}>Leave the set-up</button>} />
      <PageLearn className="cf-learn" />
      <nav aria-label="Steps" className="cf-steps-nav">
        <ol className="cf-steps">
          {steps.map((s, i) => (
            <li key={s.id} className={"cf-stepdot" + (i < at ? " cf-done" : i === at ? " cf-now" : "")} aria-current={i === at ? "step" : undefined}>
              <span className="cf-stepnum" aria-hidden="true">{i < at ? "✓" : i + 1}</span>
              <span className="cf-steptitle">{s.title}</span>
              <SpanHidden>{i < at ? " (done)" : i === at ? " (current step)" : ""}</SpanHidden>
            </li>
          ))}
        </ol>
        <div className="cf-stepcount">Step {at + 1} of {steps.length}</div>
      </nav>
      <section className="card cf-stepcard" aria-labelledby="cf-step-title">
        <h2 id="cf-step-title" ref={head} tabIndex={-1} className="cf-step-h">{step?.title}</h2>
        <form onSubmit={(e) => { e.preventDefault(); if (!busy && !nextDisabled) onNext?.(); }} noValidate>
          <div className="cf-stepbody">{children}</div>
          {!hideNav && (
            <div className="cf-wiz-actions">
              <button type="button" className="btn cf-cancel" onClick={cancel}>Cancel</button>
              <span className="cf-spacer" />
              {onBack && at > 0 && <button type="button" className="btn" onClick={onBack} disabled={busy}>Back</button>}
              {onNext && <BtnPrimary disabled={busy || nextDisabled}>{busy ? "Working…" : nextLabel}</BtnPrimary>}
            </div>
          )}
        </form>
      </section>
    </div>
  );
}

/** Summary lists what a wizard will create, each object readably. */
export function WillCreate({ changes, labels }: { changes: ChangeRequest[]; labels?: Record<string, string> }) {
  return (
    <div className="cf-summary">
      <p>Here is exactly what will be created. Nothing exists yet: <strong>Create</strong> makes all of it at once, or, if anything is refused, none of it.</p>
      <ol className="cf-willlist">
        {changes.map((c) => (
          <li key={c.kind + "/" + c.name} className="cf-will">
            <div className="cf-will-head"><span className="cf-kindtag">{labels?.[c.kind] ?? c.kind}</span> <strong className="mono">{c.name}</strong>
              {c.unit ? <SpanFaint> in {c.unit}</SpanFaint> : null}</div>
            <SpecYAML spec={c.spec ?? {}} />
          </li>
        ))}
      </ol>
    </div>
  );
}
