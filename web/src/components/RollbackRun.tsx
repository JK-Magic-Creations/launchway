import { useEffect, useState } from "react";
import type { DeploymentView, RunDetail } from "../api";
import { rollbackApi, type ReviewItem, type RollbackRunView } from "../apiRollback";
import { ago, clock } from "../format";
import { Dialog } from "../kit/Dialog";
import { Button, Sheet } from "../kit/layout";
import { useNow } from "../live";
import { terms } from "../terms";
import { EntityLink } from "./EntityLink";
import { ErrorBox } from "./ui";
import "../rollback.css";
import { BtnPrimary, DivActions, LabelField, SpanFaint, SpanFaintSmall } from "../kit/bits";

/** The page of a rollback run opens with what it is, in one sentence, who asked and why, what it undoes, and
 * whether an approver has reviewed it. */
export function RollbackBanner({ run, onChanged }: { run: RunDetail; onChanged: () => void }) {
  const r = run.rollback;
  const now = useNow(30000);
  const [mayReview, setMay] = useState(false);
  const waiting = !!r && r.review_required && !r.review;
  useEffect(() => {
    if (!waiting) return;
    let alive = true;
    rollbackApi.reviews(true).then((l) => { if (alive) setMay(!!l.find((x) => x.run === run.id)?.acknowledge.available); }).catch(() => { if (alive) setMay(false); });
    return () => { alive = false; };
  }, [waiting, run.id]);
  if (!r) return null;
  return (
    <section className={"rb-banner" + (r.emergency ? "" : " plain")} aria-label={terms().say("{Rollback}")}>
      <h2>{r.headline}</h2>
      <dl>
        <dt>Restores</dt><dd><b>{r.to.version}</b>{terms().say(", {deploy:did} by")} <EntityLink kind="run" name={terms().say(`{run} #${r.to.run_number}`)} id={r.to.run} />{r.to.at ? ` ${ago(r.to.at, now)}` : ""}</dd>
        <dt>Instead of</dt><dd><b>{r.from.version}</b>, from <EntityLink kind="run" name={terms().say(`{run} #${r.from.run_number}`)} id={r.from.run} /></dd>
        <dt>Why</dt><dd>{r.reason} <SpanFaint>(asked by {r.actor})</SpanFaint></dd>
        <dt>Undoes</dt><dd>{r.reverts.length === 0 ? "No commit: the deployments after it changed nothing in Git."
          : r.reverts.map((x, i) => <span key={x.deployment}>{i > 0 && ", "}{x.version} (<EntityLink kind="run" name={terms().say(`{run} #${x.run_number}`)} id={x.run} />)</span>)}
          {r.reverts.length > 1 ? <SpanFaint>, newest first</SpanFaint> : null}</dd>
        {r.emergency && <><dt>Review</dt><dd><ReviewState r={r} run={run} mayReview={waiting && mayReview} now={now} onChanged={onChanged} /></dd></>}
      </dl>
      <p className="rb-note">{r.note}</p>
    </section>
  );
}

function ReviewState({ r, run, mayReview, now, onChanged }: { r: RollbackRunView; run: RunDetail; mayReview: boolean; now: number; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  if (!r.review_required) return <>An emergency action, but an approval came first, so no review is needed.</>;
  if (r.review) return <>Acknowledged by <b>{r.review.by}</b> {ago(r.review.at, now)} ({clock(r.review.at)}): {r.review.comment}</>;
  return (
    <>
      <b>Needs review.</b> {terms().say("It went ahead as an emergency on a protected {environment}, without an approval first; an approver acknowledges it afterwards.")}
      {mayReview && <> <Button sm onClick={() => setOpen(true)}>Acknowledge…</Button></>}
      {open && <ReviewDialog items={[{ run: run.id, number: run.number, project: run.project, environment: r.environment, service: r.service, from_version: r.from.version,
        to_version: r.to.version, reason: r.reason, actor: r.actor, at: run.created_at, acknowledge: { name: "acknowledge", available: true, who: "operator" } }]}
        onClose={() => setOpen(false)} onDone={() => { setOpen(false); onChanged(); }} />}
    </>
  );
}

/** "Rolled back later by run #N": on a run that made a deployment a rollback undid. */
export function RolledBackLine({ run }: { run: RunDetail }) {
  const by = run.rolled_back_by ?? [];
  if (by.length === 0) return null;
  return (
    <p className="rb-link" role="status">
      <b>{terms().say("{Rollback:did} later")}</b> by {by.map((b, i) => <span key={b.id}>{i > 0 && ", "}<EntityLink kind="run" name={terms().say(`{run} #${b.number}`)} id={b.id} />{b.actor ? ` (${b.actor}${b.reason ? `: ${b.reason}` : ""})` : ""}</span>)}{terms().say(". This {run} itself is as it ended.")}
    </p>
  );
}

/** One line in a deployment history: what a rollback restored and who asked, or that a rollback undid it. */
export function HistoryNote({ d }: { d: DeploymentView }) {
  const by = d.rolled_back_by ?? [];
  if (!d.rollback && by.length === 0) return null;
  return (
    <div className="rb-hist">
      {d.rollback && <>{d.rollback.headline}{d.rollback.needs_review ? " · needs review" : d.rollback.review ? ` · reviewed by ${d.rollback.review.by}` : ""}</>}
      {by.length > 0 && <>{terms().say("{rollback:did} later by")} {by.map((b, i) => <span key={b.id}>{i > 0 && ", "}<EntityLink kind="run" name={terms().say(`{run} #${b.number}`)} id={b.id} /></span>)}</>}
    </div>
  );
}

/** ReviewDialog asks an approver for the comment that goes with acknowledging an emergency rollback. */
export function ReviewDialog({ items, onClose, onDone }: { items: ReviewItem[]; onClose: () => void; onDone: () => void }) {
  const it = items[0];
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const go = async () => {
    setBusy(true); setError(null);
    try { await rollbackApi.review(it.run, comment.trim()); onDone(); } catch (e) { setError(e); setBusy(false); }
  };
  return (
    <Dialog label={terms().say(`Review the {rollback} of ${it.service} in ${it.environment}`)} onClose={onClose}>
      <form className="rb-form" onSubmit={(e) => { e.preventDefault(); if (comment.trim()) go(); }}>
        <h2>{terms().say("Review the {rollback} of")} {it.service} in {it.environment}</h2>
        <p className="rb-then">{it.actor} rolled it back from {it.from_version} to {it.to_version} as an emergency: “{it.reason}”. It went ahead without an approval first.</p>
        <LabelField><span>What did you check? (required, kept in the {terms().lower("logbook")})</span>
          <textarea value={comment} onChange={(e) => setComment(e.target.value)} maxLength={1000} required data-autofocus placeholder="For example: the incident is real and the revert matches it" /></LabelField>
        <ErrorBox error={error} />
        <DivActions>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <BtnPrimary disabled={busy || !comment.trim()}>{busy ? "Saving…" : "Acknowledge"}</BtnPrimary>
        </DivActions>
      </form>
    </Dialog>
  );
}

/** The emergency rollbacks no approver has acknowledged yet. Only those the person may acknowledge have a button;
 * for the rest it says why not. */
export function NeedsReview({ items, environment, onChanged }: { items: ReviewItem[] | null; environment?: string; onChanged: () => void }) {
  const [open, setOpen] = useState<ReviewItem | null>(null);
  const now = useNow(30000);
  const list = (items ?? []).filter((i) => !environment || i.environment === environment);
  if (list.length === 0) return null;
  return (
    <Sheet title="Needs review" actions={<SpanFaintSmall>{list.length} {terms().say(list.length === 1 ? "{emergency_rollback}" : "{emergency_rollbacks}")} without an approver's acknowledgment</SpanFaintSmall>}>
      {list.map((i) => (
        <div key={i.run} className="need warn">
          <div className="ico-tile warn"><span aria-hidden>!</span></div>
          <div className="tx">
            <div className="ti">{i.service} in {i.environment}{terms().say(": {rollback:did} from")} {i.from_version} to {i.to_version} by {i.actor}</div>
            <div className="ds">“{i.reason}” · <EntityLink kind="run" name={terms().say(`{run} #${i.number}`)} id={i.run} /> of <EntityLink kind="project" name={i.project} /> · {ago(i.at, now)}
              {!i.acknowledge.available && i.acknowledge.reason ? <> · <span>you cannot acknowledge it: {i.acknowledge.reason}</span></> : null}</div>
          </div>
          {i.acknowledge.available && <div className="acts"><Button sm kind="pri" onClick={() => setOpen(i)}>Acknowledge…</Button></div>}
        </div>
      ))}
      {open && <ReviewDialog items={[open]} onClose={() => setOpen(null)} onDone={() => { setOpen(null); onChanged(); }} />}
    </Sheet>
  );
}

