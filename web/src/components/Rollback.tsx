import { useEffect, useState } from "react";
import type { DeploymentView } from "../api";
import type { RollbackTarget, ServerAction } from "../apiDeploy";
import { rollbackApi, type RollbackToggles } from "../apiRollback";
import { ago, clock } from "../format";
import { Dialog } from "../kit/Dialog";
import { Button } from "../kit/layout";
import { useNow } from "../live";
import { navigate } from "../router";
import { terms } from "../terms";
import { EntityLink } from "./EntityLink";
import { ErrorBox } from "./ui";
import "../rollback.css";
import { prose } from "../prose";
import { DivActions, DivFaintSmall, LabelField, SpanFaint, SpanFaintSmall } from "../kit/bits";
const P = await prose("components.Rollback");

const RB_NOTE = P.t0;

/** A sentence from the server, ended with a full stop if it has none. */
const stop = (s?: string) => (s ? s[0].toUpperCase() + s.slice(1) + (/[.!?)]$/.test(s) ? "" : ".") : "");

interface Unit { project: string; service: string; environment: string }

/** RollbackControl offers "Roll back to <version>" (and "Roll back to…" for an older one) where the server says it
 * will work. Where it says it will not, it shows why and what would help instead of a button that fails; where the
 * only thing in the way is another deployment holding the lock, it offers to queue behind it. */
export function RollbackControl({ action, unit, compact }: { action?: ServerAction; unit: Unit; compact?: boolean }) {
  const [open, setOpen] = useState<null | { pick: boolean; queue: boolean }>(null);
  if (!action) return null;
  const targets = action.targets ?? [];
  const first = targets[0];
  const where = `${unit.service === unit.project ? "" : unit.service + " in "}${unit.environment}`;
  const sm = compact ? { sm: true } : {};
  if (action.available && first) {
    return (
      <div className="rb-control">
        <div className="row">
          <Button kind="bad" icon="rollback" {...sm} onClick={() => setOpen({ pick: false, queue: false })} title={`${terms().say("{Rollback:do}")} (${terms().say("{rollback:plain}")}): ${first.consequence}`}>{terms().say("{Rollback:do}")}: {first.version}</Button>
          {targets.length > 1 && <Button {...sm} onClick={() => setOpen({ pick: true, queue: false })}title={terms().say(P.t1)}>{terms().say("{Rollback:do}")}: another version…</Button>}
        </div>
        {!compact && <p className="rb-why">{action.emergency ? terms().say(P.t2) : P.t3}</p>}
        {open && <RollbackDialog action={action} unit={unit} pick={open.pick} queue={open.queue} onClose={() => setOpen(null)} />}
      </div>
    );
  }
  if (action.queueable && first) {
    const holder = /run #(\d+)/.exec(action.reason ?? "")?.[1];
    return (
      <div className="rb-control">
        <p className="rb-why"><b>Cannot start yet.</b> {stop(action.reason)}</p>
        <div className="row">
          <Button kind="bad" icon="rollback" {...sm} onClick={() => setOpen({ pick: false, queue: true })}>{terms().say("Queue the {rollback}")}{holder ? ` behind run #${holder}` : ""}</Button>
          {action.instead && <SpanFaintSmall>or cancel that run first</SpanFaintSmall>}
        </div>
        {open && <RollbackDialog action={action} unit={unit} pick={false} queue={open.queue} onClose={() => setOpen(null)} />}
      </div>
    );
  }
  return (
    <div className="rb-control">
      <Button {...sm} disabled title={action.reason}>{terms().say("{Rollback:do}")}</Button>
      <p className="rb-why">{terms().say("Cannot {rollback:do}")} {where}: {stop(action.reason).replace(/^./, (c) => c.toLowerCase())}{action.would_help ? <> <SpanFaint>To change that: {stop(action.would_help)}</SpanFaint></> : null}</p>
    </div>
  );
}

function RollbackDialog({ action, unit, pick, queue, onClose }: { action: ServerAction; unit: Unit; pick: boolean; queue: boolean; onClose: () => void }) {
  const targets = action.targets ?? [];
  const now = useNow(30000);
  const [chosen, setChosen] = useState(targets[0]?.deployment ?? "");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [alsoToggles, setAlsoToggles] = useState(false);
  const [toggles, setToggles] = useState<RollbackToggles | undefined>();
  const t: RollbackTarget | undefined = targets.find((x) => x.deployment === chosen) ?? targets[0];
  const service = unit.service === unit.project ? undefined : unit.service;
  const target = t?.deployment;
  useEffect(() => { // the toggle changes made since the version being restored went live
    let alive = true;
    rollbackApi.plan(unit.project, unit.environment, service, target).then((p) => { if (alive) { setToggles(p.toggles); } }).catch(() => undefined);
    return () => { alive = false; };
  }, [unit.project, unit.environment, service, target]);
  if (!t) return null;
  const noReason = reason.trim() === "";
  const go = async () => {
    setBusy(true); setError(null);
    try {
      const run = await rollbackApi.request(unit.project, { environment: unit.environment, service: unit.service === unit.project ? undefined : unit.service, to: t.deployment, reason: reason.trim(), queue, ...(alsoToggles && (toggles?.changes.length ?? 0) > 0 ? { revert_toggles: true } : {}) });
      onClose();
      navigate(`/runs/${run.id}`);
    } catch (e) { setError(e); setBusy(false); }
  };
  const label = terms().say(`{Rollback:do}: ${unit.service} in ${unit.environment} to ${t.version}`);
  return (
    <Dialog label={label} onClose={onClose}>
      <form className="rb-form" onSubmit={(e) => { e.preventDefault(); if (!noReason) go(); }}>
        <h2>{label}</h2>
        {(pick || targets.length > 1) && (
          <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
            <legend className="faint small" style={{ marginBottom: 6 }}>Go back to</legend>
            <ul className="rb-targets">
              {targets.map((x) => (
                <li key={x.deployment}><label>
                  <input type="radio" name="rb-target" checked={x.deployment === t.deployment} onChange={() => setChosen(x.deployment)} />
                  <span><span className="v">{x.version}</span>{x.previous ? " (the previous one)" : ""}
                    <span className="m"> · deployed {ago(x.at, now)} ({clock(x.at)}) · run #{x.run_number} · reverts {x.reverts.length === 1 ? "1 deployment" : `${x.reverts.length} deployments`}</span></span>
                </label></li>
              ))}
            </ul>
          </fieldset>
        )}
        <p className="rb-then"><b>What happens.</b> {t.consequence}.
          {t.reverts.length > 0 && <ul>{t.reverts.map((r) => <li key={r.deployment}>revert {r.version} from <EntityLink kind="run" name={terms().say(`{run} #${r.run_number}`)} id={r.run} /></li>)}</ul>}
        </p>
        <p className="rb-note">{RB_NOTE} {terms().say(P.t4)} {terms().lower("logbook")}.</p>
        {(t.suggestions ?? []).length > 0 && <div><DivFaintSmall>Before you go ahead</DivFaintSmall><ul className="rb-suggest">{t.suggestions!.map((s) => <li key={s}>{s}</li>)}</ul></div>}
        {action.emergency && !action.requires_approval && <div className="callout callout-wait">{terms().say(P.t5)}</div>}
        {action.emergency && action.requires_approval && <div className="callout callout-wait">{terms().say(P.t6)}</div>}
        {toggles && toggles.changes.length > 0 && (
          <fieldset className="rb-toggles">
            <legend>Toggle changes since {t.version} went live</legend>
            <p className="rb-note">{toggles.note}</p>
            <ul className="rb-suggest">{toggles.changes.map((c) => <li key={c.id}><b>{c.toggle}</b> by {c.actor}: {c.reason}</li>)}</ul>
            <label className="row"><input type="checkbox" checked={alsoToggles} onChange={(e) => setAlsoToggles(e.target.checked)} /> {P.t7}</label>
          </fieldset>
        )}
        {queue && <div className="callout callout-wait">{terms().say(P.t8)}</div>}
        <LabelField><span>{terms().say(P.t9)}</span>
          <textarea value={reason} onChange={(e) => setReason(e.target.value)} maxLength={1000} data-autofocus required placeholder={P.t10} /></LabelField>
        <ErrorBox error={error} />
        <DivActions>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-danger-outline" disabled={busy || noReason} title={noReason ? "Say why first" : undefined}>{busy ? "Starting…" : terms().say(`{Rollback:do}: ${t.version}`)}</button>
        </DivActions>
      </form>
    </Dialog>
  );
}

/** On a finished, successful deployment's panel: the rollback action as the server reports it, or what a rollback
 * run's own deployment restored. */
export function DeploymentRollback({ d }: { d: DeploymentView }) {
  if (d.mode === "rollback") {
    return d.rollback ? <p className="rb-note" style={{ margin: "8px 0 0" }}>{d.rollback.headline}. {RB_NOTE}</p> : null;
  }
  const a = d.actions?.[0];
  if (!a || d.verdict !== "deployed") return null;
  return (
    <div style={{ marginTop: 10 }}>
      <DivFaintSmall style={{ marginBottom: 4 }}>{terms().say("{Rollback} after success")}</DivFaintSmall>
      <RollbackControl action={a} unit={{ project: d.project, service: d.service, environment: d.environment }} compact />
    </div>
  );
}
