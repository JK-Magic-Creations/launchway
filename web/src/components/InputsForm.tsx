import { useEffect, useId, useRef } from "react";
import { checkValue, type InputValue, type RunInput } from "../triggers";
import "../triggers.css";

interface Props {
  inputs: RunInput[];
  values: Record<string, InputValue>;
  onChange: (name: string, v: InputValue) => void;
  /** What the server said is wrong, by input name. */
  errors?: Record<string, string>;
  /** Show what the browser can tell is wrong before the server is asked. */
  touched: Record<string, boolean>;
  onTouch: (name: string) => void;
}

// InputsForm renders a pipeline's inputs as a form: a choice as a select,
// a boolean as a switch, a number with its bounds, a string with its
// pattern, a list as values separated by commas. The server checks every
// value again; what it says is shown next to the field.
export function InputsForm({ inputs, values, onChange, errors, touched, onTouch }: Props) {
  const box = useRef<HTMLDivElement>(null);
  // When the server refuses values, the first field it named gets focus.
  useEffect(() => {
    if (errors && Object.keys(errors).length) box.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus();
  }, [errors]);
  if (inputs.length === 0) return null;
  return (
    <div className="inputs-form" ref={box}>
      <fieldset>
        <legend>Inputs</legend>
        {inputs.map((i) => (
          <InputField key={i.name} input={i} value={values[i.name]} onChange={(v) => onChange(i.name, v)}
            error={errors?.[i.name] || (touched[i.name] ? checkValue(i, values[i.name]) : "")} onBlur={() => onTouch(i.name)} />
        ))}
      </fieldset>
    </div>
  );
}

function InputField({ input: i, value, onChange, error, onBlur }: {
  input: RunInput; value: InputValue; onChange: (v: InputValue) => void; error: string; onBlur: () => void;
}) {
  const id = useId();
  const help = id + "-help", err = id + "-err";
  const described = [i.description || hint(i) ? help : "", error ? err : ""].filter(Boolean).join(" ") || undefined;
  const common = { id, "aria-describedby": described, "aria-invalid": error ? true : undefined, "aria-required": i.required || undefined, onBlur };
  let control;
  switch (i.type) {
    case "choice":
      control = (
        <select {...common} value={String(value)} onChange={(e) => onChange(e.target.value)}>
          {!i.required && i.default === undefined && <option value="">(none)</option>}
          {i.required && i.default === undefined && value === "" && <option value="" disabled>Choose…</option>}
          {i.options?.map((o) => <option key={o} value={o}>{o}</option>)}
        </select>
      );
      break;
    case "boolean":
      control = (
        <button {...common} type="button" role="switch" aria-checked={value === true} className="switch" onClick={() => onChange(!(value === true))}>
          <span className="switch-track" aria-hidden="true" />
          <span>{value === true ? "Yes" : "No"}</span>
        </button>
      );
      break;
    case "number":
      control = <input {...common} type="number" inputMode="decimal" step="any" min={i.min} max={i.max} value={String(value)}
        onChange={(e) => onChange(e.target.value)} />;
      break;
    case "list": {
      const items = value as string[];
      control = (
        <>
          <input {...common} type="text" value={items.join(", ")} placeholder="values separated by commas"
            onChange={(e) => onChange(e.target.value.split(",").map((s) => s.trim()).filter((s, n, all) => s !== "" || n < all.length - 1))} />
          {items.filter(Boolean).length > 0 && (
            <div className="list-items" aria-label={`${items.filter(Boolean).length} items`}>
              {items.filter(Boolean).map((s, n) => <span key={n} className="list-item">{s}</span>)}
            </div>
          )}
        </>
      );
      break;
    }
    default:
      control = <input {...common} type="text" value={String(value)} spellCheck={false} autoComplete="off"
        onChange={(e) => onChange(e.target.value)} />;
  }
  return (
    <div className="input-field">
      <label className="input-label" htmlFor={id}>
        <span className="mono">{i.name}</span>
        {i.required && <span className="input-req" aria-hidden="true" title="required">required</span>}
        <span className="input-type">{typeLabel(i)}</span>
      </label>
      {control}
      {(i.description || hint(i)) && <div className="input-help" id={help}>{[i.description, hint(i)].filter(Boolean).join(" · ")}</div>}
      {error && <div className="input-err" id={err} role="alert">{error}</div>}
    </div>
  );
}

function typeLabel(i: RunInput): string {
  return i.type === "list" ? "list of values" : i.type;
}

function hint(i: RunInput): string {
  const parts: string[] = [];
  if (i.type === "number" && (i.min !== undefined || i.max !== undefined)) {
    parts.push(i.min !== undefined && i.max !== undefined ? `from ${i.min} to ${i.max}` : i.min !== undefined ? `at least ${i.min}` : `at most ${i.max}`);
  }
  if (i.pattern) parts.push(`must match ${i.pattern}`);
  if (i.default !== undefined && i.type !== "boolean") parts.push(`default ${i.type === "list" ? listText(i.default) : i.default || "(empty)"}`);
  return parts.join(", ");
}

function listText(json: string): string {
  try { return (JSON.parse(json) as string[]).join(", ") || "(empty)"; } catch { return json; }
}
