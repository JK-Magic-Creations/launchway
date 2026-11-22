import { useEffect, useState } from "react";
import { ErrorBox } from "./ui";
import { InputsForm } from "./InputsForm";
import { checkValue, initialValue, triggersApi, type InputsInfo, type InputValue } from "../triggers";
import { DivLoading } from "../kit/bits";

export interface StartInputsState {
  info: InputsInfo | null;
  loadErr: unknown;
  values: Record<string, InputValue>;
  touched: Record<string, boolean>;
  serverErrors?: Record<string, string>;
  /** The inputs are read and the pipeline at the ref has no problems. */
  ready: boolean;
  set: (name: string, v: InputValue) => void;
  touch: (name: string) => void;
  setServerErrors: (e?: Record<string, string>) => void;
  /** Marks every field touched and reports whether the browser sees no problem. */
  checkAll: () => boolean;
  /** The values to send. */
  wire: () => Record<string, InputValue>;
}

// useStartInputs reads the inputs of the pipeline at a ref, as the run
// would, and keeps the form's values. When the ref changes, values already
// typed are kept for inputs of the same name and type.
export function useStartInputs(project: string, ref: string): StartInputsState {
  const [info, setInfo] = useState<InputsInfo | null>(null);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [values, setValues] = useState<Record<string, InputValue>>({});
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [serverErrors, setServerErrors] = useState<Record<string, string> | undefined>();

  useEffect(() => {
    if (!ref) return;
    let alive = true;
    setInfo(null); setLoadErr(null); setServerErrors(undefined);
    triggersApi.inputs(project, ref).then((i) => {
      if (!alive) return;
      setInfo(i);
      setValues((old) => {
        const next: Record<string, InputValue> = {};
        for (const input of i.inputs) {
          const keep = old[input.name];
          next[input.name] = keep !== undefined && typeof keep === typeof initialValue(input) && Array.isArray(keep) === (input.type === "list")
            ? keep : initialValue(input);
        }
        return next;
      });
    }).catch((e) => alive && setLoadErr(e));
    return () => { alive = false; };
  }, [project, ref]);

  const ready = !!info && !info.problems?.length;
  return {
    info, loadErr, values, touched, serverErrors, ready,
    set: (name, v) => {
      setValues((old) => ({ ...old, [name]: v }));
      setServerErrors((e) => { if (!e?.[name]) return e; const n = { ...e }; delete n[name]; return n; });
    },
    touch: (name) => setTouched((t) => (t[name] ? t : { ...t, [name]: true })),
    setServerErrors,
    checkAll: () => {
      if (!info) return false;
      setTouched(Object.fromEntries(info.inputs.map((i) => [i.name, true])));
      return info.inputs.every((i) => checkValue(i, values[i.name]) === "");
    },
    wire: () => {
      const out: Record<string, InputValue> = {};
      for (const i of info?.inputs ?? []) {
        const v = values[i.name];
        out[i.name] = Array.isArray(v) ? v.filter((s) => s !== "") : v;
      }
      return out;
    },
  };
}

// StartInputs shows the inputs form for a ref, or why the pipeline there
// cannot run.
export function StartInputs({ state }: { state: StartInputsState }) {
  const { info, loadErr } = state;
  if (loadErr) return <ErrorBox error={loadErr} />;
  if (!info) return <DivLoading>Reading the pipeline's inputs…</DivLoading>;
  if (info.problems?.length) {
    return (
      <div className="callout callout-bad" role="alert">
        The pipeline at this ref cannot run:{"\n"}
        {info.problems.map((p) => (p.line ? `line ${p.line}: ` : "") + (p.path ? p.path + ": " : "") + p.message).join("\n")}
      </div>
    );
  }
  return <InputsForm inputs={info.inputs} values={state.values} onChange={state.set} errors={state.serverErrors}
    touched={state.touched} onTouch={state.touch} />;
}
