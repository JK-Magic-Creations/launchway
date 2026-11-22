// The wizards: guided set-ups that ask plain questions, check the answers
// with the server as people give them, show exactly what will be created,
// and create all of it with one all-or-none apply at the end. Nothing is
// created before the final confirmation, so cancelling leaves nothing behind.

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ApiError } from "../../api";
import { configApi, kindLabel, loadOverview, useOverview, type ChangeRequest, type ConfigObject, type ConfigOverview } from "../../configApi";
import { Problems, assignProblems, ProblemList, useServerCheck, WillCreate, WizardFrame, CheckLine } from "../../components/ConfigForm";
import { Empty, ErrorBox, Link } from "../../components/ui";
import { ConnectRepoWizard } from "./ConnectRepo";
import { FirstRunWizard } from "./FirstRun";
import { AddRunnerWizard } from "./AddRunner";
import { AddEnvironmentWizard } from "./AddEnvironment";
import { AddPolicyWizard } from "./AddPolicy";
import { AddHealthSourceWizard, AddModelWizard } from "./AddVerify";
import "../../config.css";
import { prose } from "../../prose";
import { DivLoading, DivPage, SpanFaint, SpanMono } from "../../kit/bits";
const P = await prose("pages.wizards.Wizards");

export function WizardPage({ id }: { id: string }) {
  const { overview, error } = useOverview();
  if (error) return <DivPage><ErrorBox error={error} /></DivPage>;
  if (!overview) return <DivPage><DivLoading>Loading…</DivLoading></DivPage>;
  if (!overview.available) return <DivPage><Empty title={P.t0}>{P.t1}<Link href="/config">Back</Link></Empty></DivPage>;
  if (!overview.admin) return <DivPage><Empty title={P.t2}>{P.t3}</Empty></DivPage>;
  switch (id) {
    case "first-run": return <FirstRunWizard overview={overview} />;
    case "connect-repository": return <ConnectRepoWizard overview={overview} />;
    case "add-runner": return <AddRunnerWizard overview={overview} />;
    case "add-environment": return <AddEnvironmentWizard overview={overview} />;
    case "add-policy": return <AddPolicyWizard overview={overview} />;
    case "add-health-source": return <AddHealthSourceWizard overview={overview} />;
    case "add-model": return <AddModelWizard overview={overview} />;
  }
  return <DivPage><Empty title="No such wizard"><Link href="/config">Back to configuration</Link></Empty></DivPage>;
}

/** A field of a step: where the server's messages about it go, and its
 * value (problems of a field with a value show as people type; the others
 * once they press Next). */
export interface StepField { path: string; match?: string[]; value?: unknown }

export interface StepDef {
  id: string;
  title: string;
  fields?: StepField[];
  /** The step's questions; by holds the server's messages per field. */
  render: (by: Record<string, string[]>) => ReactNode;
  /** A problem to fix before going on, found without the server. */
  local?: () => string | null;
  /** Work to do before the next step (a check); false stays. */
  onNext?: () => Promise<boolean>;
}

function hasValue(v: unknown): boolean {
  return !(v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0));
}

/** WizardRunner runs a wizard's steps, then a summary of the changes with
 * Create, then what to do next. */
export function WizardRunner({ title, intro, steps, changes, overview, done, summaryExtra, createLabel = "Create" }: {
  title: string; intro?: ReactNode; steps: StepDef[]; changes: ChangeRequest[]; overview: ConfigOverview;
  done: (created: ConfigObject[]) => ReactNode; summaryExtra?: ReactNode; createLabel?: string;
}) {
  // ?step= opens a wizard at one of its steps (the setup checklist links to them).
  const [at, setAt] = useState(() => {
    const i = steps.findIndex((x) => x.id === new URLSearchParams(location.search).get("step"));
    return i > 0 ? i : 0;
  });
  const [tried, setTried] = useState<Record<string, boolean>>({});
  const [busy, setBusy] = useState(false);
  // The message belongs to the step it was raised on: moving to another step hides it.
  const [localErrAt, setLocalErrAt] = useState({ at: 0, msg: "" });
  const localErr = localErrAt.at === at ? localErrAt.msg : "";
  const setLocalErr = (msg: string) => setLocalErrAt({ at, msg });
  const [applyError, setApplyError] = useState<unknown>(null);
  const [created, setCreated] = useState<ConfigObject[] | null>(null);
  const check = useServerCheck(changes.length && !created ? changes : null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const all = useMemo(() => [...steps, { id: "summary", title: "Check and create", render: () => null } as StepDef], [steps]);
  const step = all[at];
  const fields = steps.flatMap((s) => s.fields ?? []);
  const { by, rest } = assignProblems(check.problems, fields.map((f) => ({ path: f.path, match: f.match, label: "", help: "", type: "text" as const })));
  const shown: Record<string, string[]> = {};
  for (const f of step.fields ?? []) {
    if (by[f.path] && (tried[step.id] || hasValue(f.value))) shown[f.path] = by[f.path];
  }

  const labels: Record<string, string> = {};
  for (const k of overview.kinds) labels[k.name] = kindLabel(overview, k, false);

  const focusInvalid = () => window.setTimeout(() => {
    const el = bodyRef.current?.querySelector<HTMLElement>(P.t4);
    el?.focus();
  }, 30);

  const next = async () => {
    setLocalErr("");
    const l = step.local?.();
    if (l) { setLocalErr(l); setTried((t) => ({ ...t, [step.id]: true })); focusInvalid(); return; }
    setBusy(true);
    try {
      let ps: string[] = [];
      try { ps = changes.length ? await check.now() : []; } catch (e) { setLocalErr(`The server could not check your answers: ${e instanceof Error ? e.message : String(e)}`); return; }
      const now = assignProblems(ps, fields.map((f) => ({ path: f.path, match: f.match, label: "", help: "", type: "text" as const })));
      if ((step.fields ?? []).some((f) => now.by[f.path])) {
        setTried((t) => ({ ...t, [step.id]: true }));
        focusInvalid();
        return;
      }
      if (step.onNext && !(await step.onNext())) return;
      setAt(at + 1);
    } finally { setBusy(false); }
  };

  const create = async () => {
    if (!changes.length) { setCreated([]); return; }
    setBusy(true); setApplyError(null);
    try {
      const r = await configApi.apply(changes);
      setCreated(r.results.map((x) => x.object));
      if (changes.some((c) => ["installation", "organisation", "unit"].includes(c.kind))) loadOverview(true).catch(() => {});
    } catch (e) { setApplyError(e); } finally { setBusy(false); }
  };

  if (created) {
    return (
      <WizardFrame title={title} steps={all.concat([{ id: "done", title: "Done", render: () => null }])} at={all.length} hideNav>
        <div className="cf-donepanel">
          {created.length > 0 && <div className="callout callout-ok" role="status"><strong>Created.</strong> {P.t5}</div>}
          {done(created)}
        </div>
      </WizardFrame>
    );
  }
  const isSummary = step.id === "summary";
  const applyProblems = applyError instanceof ApiError ? ((applyError.problems as unknown as string[] | undefined) ?? []).map((p) => typeof p === "string" ? p : (p as { message: string }).message) : [];
  return (
    <WizardFrame title={title} intro={intro} steps={all.concat([{ id: "done", title: "Done", render: () => null }])} at={at}
      onBack={() => setAt(Math.max(0, at - 1))} onNext={isSummary ? create : next} nextLabel={isSummary ? createLabel : "Next"} busy={busy}
      nextDisabled={isSummary && (check.checking || check.problems.length > 0)}>
      <div ref={bodyRef} className="cf-stepinner">
        {isSummary ? (
          <>
            {changes.length > 0 ? <WillCreate changes={changes} labels={labels} /> : <p>{P.t6}</p>}
            {summaryExtra}
            {changes.length > 0 && <CheckLine check={check} okText={P.t7} />}
            <ProblemList problems={check.problems} title={P.t8} />
            {applyError ? (applyProblems.length ? <ProblemList problems={applyProblems} title={P.t9} /> : <ErrorBox error={applyError} />) : null}
          </>
        ) : (
          <>
            {step.render(shown)}
            {tried[step.id] && rest.length > 0 && at === steps.length - 1 && <ProblemList problems={rest} title="The server also says:" />}
          </>
        )}
        <div aria-live="assertive" className="cf-problem">{localErr}</div>
      </div>
    </WizardFrame>
  );
}

/** Created lists what a wizard made, each linking to its page. */
export function CreatedList({ objects, overview }: { objects: ConfigObject[]; overview: ConfigOverview }) {
  return (
    <ul className="cf-created">
      {objects.map((o) => {
        const k = overview.kinds.find((x) => x.name === o.kind);
        return (
          <li key={o.kind + "/" + o.name}>
            <span className="cf-kindtag">{k ? kindLabel(overview, k, false) : o.kind}</span>{" "}
            {k ? <Link href={`/config/${k.path}/${encodeURIComponent(o.name)}`}><SpanMono>{o.name}</SpanMono></Link> : <SpanMono>{o.name}</SpanMono>}
            <SpanFaint> · version {o.version}</SpanFaint>
          </li>
        );
      })}
    </ul>
  );
}

/** Lists the names of the objects of a kind, for pickers. */
export function useNames(kind: string): string[] {
  const [names, setNames] = useState<string[]>([]);
  useEffect(() => { configApi.list(kind).then((r) => setNames(r.objects.map((o) => o.name))).catch(() => setNames([])); }, [kind]);
  return names;
}

/** Lists the objects of a kind. */
export function useObjects(kind: string): ConfigObject[] | null {
  const [objs, setObjs] = useState<ConfigObject[] | null>(null);
  useEffect(() => { configApi.list(kind).then((r) => setObjs(r.objects)).catch(() => setObjs([])); }, [kind]);
  return objs;
}

/** PickNames lets people tick existing names and add patterns. */
export function PickNames({ legend, help, names, value, onChange, placeholder = "or patterns, such as web-*", problems }: {
  legend: string; help?: ReactNode; names: string[]; value: string[]; onChange: (v: string[]) => void; placeholder?: string; problems?: string[];
}) {
  const extra = value.filter((v) => !names.includes(v));
  const [text, setText] = useState(extra.join(", "));
  const toggle = (n: string, on: boolean) => onChange(on ? [...value, n] : value.filter((x) => x !== n));
  return (
    <fieldset className={"cf-choices" + (problems?.length ? " cf-invalid" : "")}>
      <legend className="cf-legend">{legend}</legend>
      {help && <div className="cf-help">{help}</div>}
      {names.length > 0 && (
        <div className="cf-ticks">
          {names.map((n) => (
            <label key={n} className="cf-tick"><input type="checkbox" checked={value.includes(n)} onChange={(e) => toggle(n, e.target.checked)} /> <SpanMono>{n}</SpanMono></label>
          ))}
        </div>
      )}
      <label className="cf-mini cf-wide"><span>{names.length ? P.t10 : P.t11}</span>
        <input className="cf-input mono" value={text} placeholder={placeholder} aria-invalid={problems?.length ? true : undefined}
          onChange={(e) => {
            setText(e.target.value);
            const typed = e.target.value.split(",").map((s) => s.trim()).filter(Boolean);
            onChange([...value.filter((v) => names.includes(v)), ...typed]);
          }} />
      </label>
      <Problems problems={problems} />
    </fieldset>
  );
}
