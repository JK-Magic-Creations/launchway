import { useEffect, useMemo, useRef, useState } from "react";
import { api, type RerunPlan, type RunDetail, type StepView } from "../api";
import type { RerunFrom } from "../api/flow";
import { clock, shortSha } from "../format";
import { navigate } from "../router";
import { ErrorBox, Mono } from "./ui";
import { RerunInputsNote } from "./RunTrigger";
import { terms } from "../terms";
import { prose } from "../prose";
import { BtnPrimary, DivActions, DivLoading, LabelField, PSub, SpanFaint } from "../kit/bits";
const P = await prose("components.Rerun");

// RerunButton offers a new run of a finished run from the step shown, with
// a preview of what runs again and what is reused.
export function RerunButton({ run, step, label = "Re-run from this step", primary }: { run: RunDetail; step: StepView; label?: string; primary?: boolean }) {
  const [open, setOpen] = useState(false);
  const from = useMemo<RerunFrom>(() => ({ from: [step.id] }), [step.id]);
  return (
    <>
      <button type="button" className={primary ? "btn btn-primary" : "btn btn-sm"} onClick={() => setOpen(true)}
        title={terms().say(`A new {run} at the same commit: ${step.id} and the steps after it run again; the others are reused.`)}>{label}</button>
      {open && <RerunDialog run={run} from={from} onClose={() => setOpen(false)} />}
    </>
  );
}

// RerunPicker offers "Re-run from…": choose any step, and see what would be
// reused and what would run again before starting.
export function RerunPicker({ run }: { run: RunDetail }) {
  const [choosing, setChoosing] = useState(false);
  const [from, setFrom] = useState<RerunFrom | null>(null);
  const first = run.resume?.rerun_from ?? run.step_list.find((s) => s.status === "failed")?.id ?? run.step_list[0]?.id ?? "";
  const [picked, setPick] = useState("");
  const pick = picked || first;
  if (run.step_list.length < 2) return null;
  return (
    <>
      <button type="button" className="btn" onClick={() => setChoosing(true)}
        title={terms().say(P.t0)}>Re-run from…</button>
      {choosing && !from && (
        <dialog className="dialog" ref={(d) => { if (d && !d.open) d.showModal(); }} onClose={() => setChoosing(false)} aria-labelledby="rerun-pick-title">
          <form method="dialog" onSubmit={(e) => { e.preventDefault(); setFrom({ from: [pick] }); }}>
            <h2 id="rerun-pick-title">Re-run #{run.number} from which step?</h2>
            <PSub>{P.t1}</PSub>
            <LabelField><span>Start again from</span>
              <select value={pick} onChange={(e) => setPick(e.target.value)} aria-label="Step to re-run from">
                {run.step_list.map((s) => <option key={s.id} value={s.id}>{s.id} ({s.status})</option>)}
              </select>
            </LabelField>
            <DivActions>
              <button type="button" className="btn" onClick={() => setChoosing(false)}>Cancel</button>
              <BtnPrimary>Show what would run</BtnPrimary>
            </DivActions>
          </form>
        </dialog>
      )}
      {from && <RerunDialog run={run} from={from} onClose={() => { setFrom(null); setChoosing(false); }} />}
    </>
  );
}

// RerunFailedButton offers a new run that runs again only what failed or was
// canceled, when the plan allows it (it asks the server first); the dialog
// shows what runs and what is reused.
const FAILED_ONLY: RerunFrom = { failed: true };
export function RerunFailedButton({ run }: { run: RunDetail }) {
  const [open, setOpen] = useState(false);
  const [ok, setOk] = useState(false);
  useEffect(() => { api.rerunPlan(run.id, FAILED_ONLY).then(() => setOk(true)).catch(() => setOk(false)); }, [run.id, run.status]);
  if (!ok) return null;
  return (
    <>
      <button type="button" className="btn" onClick={() => setOpen(true)}
        title={terms().say(P.t2)}>Re-run failed steps</button>
      {open && <RerunDialog run={run} from={FAILED_ONLY} onClose={() => setOpen(false)} />}
    </>
  );
}

function RerunDialog({ run, from, onClose }: { run: RunDetail; from: RerunFrom; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [plan, setPlan] = useState<RerunPlan | null>(null);
  const [refusal, setRefusal] = useState<unknown>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { dialog.current?.showModal(); }, []);
  useEffect(() => { api.rerunPlan(run.id, from).then(setPlan).catch(setRefusal); }, [run.id, from]);
  const starts = plan?.from?.length ? plan.from : from.from ?? [];
  const label = from.failed ? "the failed steps" : starts.join(", ");

  const go = async () => {
    setBusy(true); setError(null);
    try {
      const re = await api.rerun(run.id, from);
      dialog.current?.close();
      onClose();
      navigate(`/runs/${re.id}?step=${encodeURIComponent(re.rerun_from ?? starts[0] ?? "")}`);
    } catch (e) { setError(e); setBusy(false); }
  };

  return (
    <dialog ref={dialog} className="dialog" onClose={onClose} aria-labelledby="rerun-title">
      <form method="dialog" onSubmit={(e) => { e.preventDefault(); go(); }}>
        <h2 id="rerun-title">Re-run #{run.number} from {label}</h2>
        <PSub>A new run of {run.project} at the same commit, <Mono>{shortSha(run.sha)}</Mono>, with the same pipeline{run.graph.libraries?.length ? " and library commits" : ""}{run.draft ? P.t3 : ""}.</PSub>
        <div className="callout callout-draft">{P.t4}</div>
        {refusal ? <ErrorBox error={refusal} /> : !plan ? <DivLoading>Working out what runs…</DivLoading> : (
          <div className="rerun-plan">
            <div><h3 className="h-sm">Will run</h3><ul>{plan.run.map((id) => <li key={id}><Mono>{id}</Mono></li>)}</ul></div>
            <div><h3 className="h-sm">Will be reused from #{run.number}</h3>
              {plan.reuse.length ? <ul>{plan.reuse.map((id) => <ReusedItem key={id} id={id} plan={plan} run={run} />)}</ul> : <PSub>{P.t5}</PSub>}
            </div>
          </div>
        )}
        {plan && <PlanFacts plan={plan} run={run} />}
        <RerunInputsNote run={run} />
        <PSub><strong>Resume</strong> {terms().say(P.t6)} <strong>re-run</strong> {terms().say(P.t7)}</PSub>
        <ErrorBox error={error} />
        <DivActions>
          <button type="button" className="btn" onClick={() => { dialog.current?.close(); }}>Cancel</button>
          <BtnPrimary disabled={!plan || busy}>{busy ? "Starting…" : "Re-run"}</BtnPrimary>
        </DivActions>
      </form>
    </dialog>
  );
}

const shortDigest = (d: string) => (d.startsWith("sha256:") ? d.slice(0, 19) + "…" : d.length > 24 ? d.slice(0, 24) + "…" : d);

// ReusedItem says how a reused step ended, and for an approval whether its
// decision still counts.
function ReusedItem({ id, plan, run }: { id: string; plan: RerunPlan; run: RunDetail }) {
  const info = plan.reused?.find((x) => x.id === id);
  const s = run.step_list.find((x) => x.id === id);
  const who = info?.decided_by || s?.decided_by;
  const at = info?.decided_at || s?.decided_at;
  return (
    <li><Mono>{id}</Mono>
      {s?.kind === "approval" && who
        ? <SpanFaint> approved by {who}{at ? ` at ${clock(at)}` : ""} {P.t8}</SpanFaint>
        : s?.status === "skipped" ? <SpanFaint> (skipped)</SpanFaint> : null}
      {info?.outputs?.digest && <SpanFaint> image digest <Mono>{shortDigest(info.outputs.digest)}</Mono>, the same one</SpanFaint>}
    </li>
  );
}

// PlanFacts states what a re-run does that people ask about: nothing is
// rebuilt, which approvals ask again, and whether a deployment is made.
function PlanFacts({ plan, run }: { plan: RerunPlan; run: RunDetail }) {
  const rebuilds = plan.run.some((id) => { const s = run.step_list.find((x) => x.id === id); return s?.kind === "image" || !!s?.outputs?.digest; });
  return (
    <ul className="rerun-facts">
      {!rebuilds && <li>{P.t9}</li>}
      {(plan.approvals_again ?? []).length > 0 && <li>Asks again: {plan.approvals_again!.map((id) => <Mono key={id}>{id}</Mono>)} {P.t10}</li>}
      {plan.deployment && <li>{P.t11}</li>}
    </ul>
  );
}
