import { useEffect, useState } from "react";
import { api, type DeploymentView } from "../api";
import { rollbackApi, type RollbackPlan } from "../apiRollback";
import { ErrorBox } from "../components/ui";
import { Dialog } from "../kit/Dialog";
import { announce } from "../kit/announce";
import { navigate } from "../router";
import { launchApi, type LAction, type TreeView } from "../launchApi";

const WORDS: Record<LAction["action"], { label: string; what: string }> = {
  pause: { label: "Pause", what: "Holds the rollout where it is: no new step starts until it is resumed." },
  resume: { label: "Resume", what: "Lets a rollout a person paused carry on." },
  restart: { label: "Restart the pods", what: "Replaces the workload's pods one by one (a rolling restart): the service keeps answering." },
  scale: { label: "Scale", what: "Changes the number of replicas, within what this Airfield allows." },
};

/** The workload the delivery made: the rollout, or else the Deployment. */
export function workloadOf(v: TreeView): string {
  const roots = v.tree.nodes.filter((n) => !n.parent && n.change !== "leftover" && (n.kind === "Rollout" || n.kind === "Deployment" || n.kind === "StatefulSet"));
  return (roots.find((n) => n.kind === "Rollout") ?? roots[0])?.id ?? "";
}

/** Pause, resume, restart and scale: each a real operation through the deploy runner, shown only when it can be done, with the reason when it cannot. */
export function WorkloadControls({ v, reload }: { v: TreeView; reload: () => void }) {
  const [open, setOpen] = useState<LAction | null>(null);
  const [done, setDone] = useState("");
  const object = workloadOf(v);
  if (!object || v.actions.length === 0) return null;
  const ok = v.actions.filter((a) => a.ok);
  const no = v.actions.filter((a) => !a.ok);
  return (
    <div className="lw-controls" role="group" aria-label="Control the workload">
      <h3 className="h-xs">Control the workload <span className="faint">({object.split("/").pop()})</span></h3>
      <div className="lw-btns">
        {ok.map((a) => <button key={a.action} type="button" className="btn" title={WORDS[a.action].what} onClick={() => setOpen(a)}>{WORDS[a.action].label}</button>)}
      </div>
      {no.length > 0 && (
        <ul className="lw-nope">
          {no.map((a) => <li key={a.action}><strong>{WORDS[a.action].label}</strong> is not offered: {a.why}</li>)}
        </ul>
      )}
      {done && <p className="lw-note" role="status">{done}</p>}
      {open && <ControlDialog v={v} action={open} object={object} onClose={() => setOpen(null)} onDone={(m) => { setOpen(null); setDone(m); announce(m); reload(); }} />}
    </div>
  );
}

function ControlDialog({ v, action, object, onClose, onDone }: { v: TreeView; action: LAction; object: string; onClose: () => void; onDone: (m: string) => void }) {
  const [reason, setReason] = useState("");
  const [replicas, setReplicas] = useState(() => v.tree.nodes.find((n) => n.id === object)?.replicas?.desired ?? 1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const w = WORDS[action.action];
  const submit = async () => {
    setBusy(true); setError(null);
    try {
      const r = await launchApi.control(v.run, v.deployment, { action: action.action, object, replicas: action.action === "scale" ? replicas : undefined, reason });
      onDone(`${w.label}: ${r.result.replace(/_/g, " ")}. ${r.detail}`);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  return (
    <Dialog label={`${w.label}: ${object.split("/").pop()}`} onClose={onClose}>
      <form className="lw-form" onSubmit={(e) => { e.preventDefault(); if (reason.trim()) void submit(); }}>
        <h2>{w.label} {object.split("/").pop()}?</h2>
        <p>{w.what} Through the deploy runner of {v.target}; kept in the audit trail with your reason.</p>
        {action.action === "scale" && (
          <label className="field"><span>Replicas</span><input type="number" min={1} value={replicas} onChange={(e) => setReplicas(Number(e.target.value))} /></label>
        )}
        <label className="field"><span>Why <span className="faint">(required)</span></span>
          <textarea rows={2} required value={reason} maxLength={300} onChange={(e) => setReason(e.target.value)} />
        </label>
        <ErrorBox error={error} />
        <div className="lw-btns">
          <button type="submit" className="btn btn-primary" disabled={busy || !reason.trim()}>{busy ? "Working…" : w.label}</button>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
        </div>
      </form>
    </Dialog>
  );
}

/** The emergency way out: abort what is rolling out and return to the last good release. The service's name must be typed. */
export function EmergencyReturn({ v, d, reload }: { v: TreeView; d: DeploymentView; reload: () => void }) {
  const [plan, setPlan] = useState<RollbackPlan | null>(null);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!v.can_control) return;
    rollbackApi.plan(v.project, v.environment, d.service).then(setPlan).catch(() => setPlan(null));
  }, [v.project, v.environment, d.service, v.can_control]);
  if (!v.can_control || !plan || !(plan.action?.available || plan.action?.queueable)) return null;
  const target = plan.action.targets?.find((t) => t.previous) ?? plan.action.targets?.[0];
  return (
    <div className="lw-emergency">
      <button type="button" className="btn btn-danger" onClick={() => setOpen(true)}>Abort and return to the last good release</button>
      <span className="faint"> {target ? `Goes back to ${target.version}.` : ""}{plan.action.emergency ? " Protected environment: no approval first, an approver reviews it afterwards." : ""}</span>
      {open && <EmergencyDialog v={v} d={d} plan={plan} onClose={() => setOpen(false)} reload={reload} />}
    </div>
  );
}

function EmergencyDialog({ v, d, plan, onClose, reload }: { v: TreeView; d: DeploymentView; plan: RollbackPlan; onClose: () => void; reload: () => void }) {
  const [typed, setTyped] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const target = plan.action.targets?.find((t) => t.previous) ?? plan.action.targets?.[0];
  const rolling = d.snapshot?.rollout && !d.verdict;
  const go = async () => {
    setBusy(true); setError(null);
    try {
      // The rollout stops first, so that nothing goes on while the revert is made; a refusal there does not stop the return.
      if (rolling) { try { await api.command(v.run, v.deployment, "abort", `emergency return: ${reason}`); } catch { /* the return is what counts */ } }
      const r = await rollbackApi.request(v.project, { environment: v.environment, service: plan.service, to: target?.deployment, reason: `Emergency: ${reason}`, queue: true });
      announce(`Return to base started: run ${r.number}.`);
      reload();
      navigate(`/runs/${r.id}`);
    } catch (e) { setError(e); setBusy(false); }
  };
  const ok = typed.trim() === plan.service && reason.trim().length > 0;
  return (
    <Dialog label="Abort and return to the last good release" onClose={onClose}>
      <form className="lw-form" onSubmit={(e) => { e.preventDefault(); if (ok) void go(); }}>
        <h2>Abort and return to {target?.version ?? "the last good release"}</h2>
        <p>{rolling ? "The rollout is aborted first, so all traffic goes back to the stable version. " : ""}Then {plan.service} in {plan.environment} is returned to {target?.version ?? "the previous release"} through Git, as Return to base does. A database or configuration is never restored.</p>
        <label className="field"><span>Why <span className="faint">(required, kept with the return)</span></span>
          <textarea rows={2} value={reason} maxLength={300} onChange={(e) => setReason(e.target.value)} />
        </label>
        <label className="field"><span>Type <strong>{plan.service}</strong> to confirm</span>
          <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
        </label>
        <ErrorBox error={error} />
        <div className="lw-btns">
          <button type="submit" className="btn btn-danger" disabled={!ok || busy}>{busy ? "Working…" : "Abort and return"}</button>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
        </div>
      </form>
    </Dialog>
  );
}
