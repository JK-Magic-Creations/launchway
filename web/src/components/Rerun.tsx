import { useEffect, useMemo, useRef, useState } from "react";
import { api, type RerunPlan, type RunDetail, type StepView } from "../api";
import type { RerunFrom } from "../api/flow";
import { shortSha } from "../format";
import { navigate } from "../router";
import { ErrorBox, Mono } from "./ui";
import { RerunInputsNote } from "./RunTrigger";

// RerunButton offers a new run of a finished run from the step shown, with
// a preview of what runs again and what is reused.
export function RerunButton({ run, step, label = "Re-run from this step" }: { run: RunDetail; step: StepView; label?: string }) {
  const [open, setOpen] = useState(false);
  const from = useMemo<RerunFrom>(() => ({ from: [step.id] }), [step.id]);
  return (
    <>
      <button type="button" className="btn btn-sm" onClick={() => setOpen(true)}
        title={`A new run at the same commit: ${step.id} and the steps after it run again; the others are reused.`}>{label}</button>
      {open && <RerunDialog run={run} from={from} onClose={() => setOpen(false)} />}
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
        title="A new run at the same commit that runs again the failed and canceled steps and what comes after them, reusing everything else. Resume instead continues this run.">Re-run failed steps</button>
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
        <p className="sub">A new run of {run.project} at the same commit, <Mono>{shortSha(run.sha)}</Mono>, with the same pipeline{run.graph.libraries?.length ? " and library commits" : ""}{run.draft ? " (a draft run of the same text)" : ""}.</p>
        <div className="callout callout-draft">Outputs are kept. Files earlier steps wrote to the workspace are not; steps that need them should be included in the re-run.</div>
        {refusal ? <ErrorBox error={refusal} /> : !plan ? <div className="loading">Working out what runs…</div> : (
          <div className="rerun-plan">
            <div><h3 className="h-sm">Will run</h3><ul>{plan.run.map((id) => <li key={id}><Mono>{id}</Mono></li>)}</ul></div>
            <div><h3 className="h-sm">Will be reused from #{run.number}</h3>
              {plan.reuse.length ? <ul>{plan.reuse.map((id) => {
                const s = run.step_list.find((x) => x.id === id);
                return <li key={id}><Mono>{id}</Mono>{s?.kind === "approval" && s.decided_by ? <span className="faint"> (approved by {s.decided_by})</span> : s?.status === "skipped" ? <span className="faint"> (skipped)</span> : null}</li>;
              })}</ul> : <p className="sub">Nothing: every step runs again.</p>}
            </div>
          </div>
        )}
        <RerunInputsNote run={run} />
        <p className="sub"><strong>Resume</strong> continues this run instead: only the steps that did not succeed run again, in the same run and the same workspace, and it is refused once a deployment opened. A <strong>re-run</strong> is a new run from a fresh checkout: outputs of reused steps are kept, files they wrote are not, and deploy steps always run again.</p>
        <ErrorBox error={error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={() => { dialog.current?.close(); }}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={!plan || busy}>{busy ? "Starting…" : "Re-run"}</button>
        </div>
      </form>
    </dialog>
  );
}
