import { useCallback, useEffect, useState } from "react";
import { releasesApi, type ChecklistAnswer, type ChecklistItem } from "../releasesApi";
import { Dialog } from "../kit/Dialog";
import { navigate } from "../router";
import { terms } from "../terms";
import { ErrorBox } from "./ui";
import "../releases.css";

const MARK: Record<ChecklistItem["status"], string> = { pass: "✓", fail: "✗", waiting: "…", skipped: "–" };
const WORD: Record<ChecklistItem["status"], string> = { pass: "met", fail: "not met", waiting: "waiting", skipped: "not asked" };

/** The checklist of an Airfield, line by line, with what each line found. */
export function ChecklistView({ items }: { items: ChecklistItem[] }) {
  return (
    <ul className="rel-checks" aria-label={`${terms().label("checklist")} lines`}>
      {items.map((i) => (
        <li key={i.id} className={i.status}>
          <span className="mark" aria-hidden="true">{MARK[i.status]}</span>
          <span><b>{i.label}</b> <span className="faint">({WORD[i.status]})</span>{i.detail && <span className="d">{i.detail}</span>}</span>
        </li>
      ))}
    </ul>
  );
}

/** Promote a release to an Airfield: the checklist as the server judges it now, what an administrator may override and why it is
 * asked, and the one button that starts the Sortie with this release's commit and image digest. */
export function PromoteDialog({ project, version, airfield, onClose, onDone }: { project: string; version: string; airfield: string; onClose: () => void; onDone?: () => void }) {
  const [ans, setAns] = useState<ChecklistAnswer | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [override, setOverride] = useState("");
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => releasesApi.checklist(project, version, airfield).then(setAns).catch(setError), [project, version, airfield]);
  useEffect(() => { load(); }, [load]);
  const label = `Promote ${project} ${version} to ${airfield}`;
  const ck = ans?.checklist;
  const needsClearance = ck?.items.some((i) => i.id === "clearance" && i.status === "waiting");
  const run = async () => {
    setBusy(true); setError(null);
    try {
      const r = await releasesApi.promote(project, version, airfield, override.trim() || undefined);
      onClose(); onDone?.();
      navigate(`/runs/${r.run.id}`);
    } catch (e) { setError(e); setBusy(false); load(); }
  };
  const clear = async () => {
    setBusy(true); setError(null);
    try { await releasesApi.clearance(project, version, airfield, comment.trim()); setComment(""); await load(); } catch (e) { setError(e); }
    setBusy(false);
  };
  const mayStart = !!ans?.may_promote && !!ck && (ck.ready || (ans.may_override && override.trim() !== "")) && !ck.already_there;
  return (
    <Dialog label={label} onClose={onClose}>
      <form className="rel-form" onSubmit={(e) => { e.preventDefault(); if (mayStart && !busy) run(); }}>
        <h2>{label}</h2>
        {!ck && !error && <div className="loading">Checking the {terms().lower("checklist")}…</div>}
        {ck && (
          <>
            <p className="rel-hint">Nothing is built again: the Sortie starts at this release's tag with its version and, where it has one, its image digest.</p>
            <ChecklistView items={ck.items} />
            {ck.already_there && <div className="callout callout-wait">This release already runs in {airfield} and is healthy.</div>}
            {ans?.environment_refusal && <div className="callout callout-wait">{ans.environment_refusal}.</div>}
            {needsClearance && ans?.may_promote && (
              <label className="field"><span>Give your {terms().lower("clearance")} for {airfield} (a comment helps the next person)</span>
                <input type="text" value={comment} onChange={(e) => setComment(e.target.value)} maxLength={500} />
                <span><button type="button" className="btn btn-sm" onClick={clear} disabled={busy}>Give {terms().lower("clearance")}</button></span>
              </label>
            )}
            {!ck.ready && !ck.already_there && ck.can_override && ans?.may_override && (
              <label className="field"><span>Override what stops it, and say why (an administrator's decision, recorded in the {terms().lower("logbook")})</span>
                <textarea value={override} onChange={(e) => setOverride(e.target.value)} maxLength={500} placeholder="For example: customer outage, fix approved by the on-call lead" /></label>
            )}
            {!ck.ready && !ck.already_there && !(ck.can_override && ans?.may_override) && (
              <p className="rel-hint">{ck.can_override ? "An administrator of the project may override what stops it, with a reason." : "What stops it cannot be overridden: it has to be met."}</p>
            )}
          </>
        )}
        <ErrorBox error={error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          {ck && <button type="submit" className="btn btn-primary" disabled={busy || !mayStart} title={!mayStart ? "The checklist is not met yet" : undefined}>{busy ? "Starting…" : `Promote to ${airfield}`}</button>}
        </div>
      </form>
    </Dialog>
  );
}

/** Return an Airfield to base: the release before the one it runs, by the rollback the product already has. */
export function ReturnDialog({ project, airfield, version, onClose, onDone }: { project: string; airfield: string; version?: string; onClose: () => void; onDone?: () => void }) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const label = `Return ${project} in ${airfield} to base`;
  const go = async () => {
    setBusy(true); setError(null);
    try { const r = await releasesApi.returnToBase(project, airfield, reason.trim()); onClose(); onDone?.(); navigate(`/runs/${r.id}`); } catch (e) { setError(e); setBusy(false); }
  };
  return (
    <Dialog label={label} onClose={onClose}>
      <form className="rel-form" onSubmit={(e) => { e.preventDefault(); if (reason.trim()) go(); }}>
        <h2>{label}</h2>
        <p className="rel-hint">{version ? `${airfield} runs ${version}. ` : ""}{terms().say("It goes back to the release it ran before, by a {rollback}: the same rules, the same lock, and the reason is kept with it. A {rollback} never restores a database or configuration.")}</p>
        <label className="field"><span>Why? (required)</span><textarea value={reason} onChange={(e) => setReason(e.target.value)} maxLength={1000} required data-autofocus /></label>
        <ErrorBox error={error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-danger-outline" disabled={busy || !reason.trim()}>{busy ? "Starting…" : "Return to base"}</button>
        </div>
      </form>
    </Dialog>
  );
}
