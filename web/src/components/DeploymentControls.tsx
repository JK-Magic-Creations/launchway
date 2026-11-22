import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type CommandView, type DeploymentControls as Controls, type DeployRollout, type DeploymentView, type RolloutAction, type RunDetail } from "../api";
import { ago, clock } from "../format";
import { ErrorBox, Mono } from "./ui";
import { terms } from "../terms";
import { freshness } from "./Deployment";
import { RolloutOverview } from "./RolloutOverview";

// Controlling a deployment's Argo Rollouts rollout: Promote, Promote full,
// Abort and Retry, and the commands given so far. A command is applied by
// a deploy runner, and only if the rollout is still where it was shown.

const LABEL: Record<RolloutAction, string> = {
  promote: "Promote",
  "promote-full": "Promote full",
  abort: "Abort",
  retry: "Retry",
};

const VERB: Record<RolloutAction, string> = {
  promote: "promote",
  "promote-full": "fully promote",
  abort: "abort",
  retry: "retry",
};

/** What each action does, for the button's title. */
const WHAT: Record<RolloutAction, string> = {
  promote: "Lets the rollout go on past the pause it waits at.",
  "promote-full": "Skips the remaining canary steps and analyses: the new version gets all traffic.",
  abort: "Stops the rollout: all traffic goes back to the stable version.",
  retry: "Starts the aborted rollout again from its first step.",
};

const ACTIONS: RolloutAction[] = ["promote", "promote-full", "abort", "retry"];

/** A command's action as a noun phrase, for the list. */
export function actionLabel(a: string): string {
  return a === "promote-full" ? "Full promotion" : a ? a[0].toUpperCase() + a.slice(1) : a;
}

/** Whether the rollout, as shown, is at a point where the action makes sense. */
function meaningful(action: RolloutAction, ro?: DeployRollout): boolean {
  if (!ro || !ro.found) return false;
  const done = ro.phase === "Healthy" && !!ro.stable_rs && ro.stable_rs === ro.current_pod_hash && !ro.aborted;
  const paused = ro.paused || (ro.pause_reasons?.length ?? 0) > 0;
  switch (action) {
    case "promote": return !ro.aborted && !done && paused;
    case "promote-full": return !ro.aborted && !done && !ro.promote_full;
    case "abort": return !ro.aborted && !done;
    case "retry": return ro.aborted;
  }
}

/** Fetches what the caller may do to a deployment now: on mount, and again
 * when the deployment changes, at most once every 2 seconds. A server that
 * does not know commands, or a deployment that is over, has none (null). */
function useControls(run: string, d: DeploymentView, enabled: boolean) {
  const [controls, setControls] = useState<Controls | null>(null);
  const last = useRef(0);
  const timer = useRef<number | undefined>(undefined);
  const alive = useRef(true);
  const current = useRef("");
  useEffect(() => { alive.current = true; return () => { alive.current = false; window.clearTimeout(timer.current); }; }, []);

  const fetchNow = useCallback(() => {
    window.clearTimeout(timer.current);
    timer.current = undefined;
    last.current = Date.now();
    const asked = run + "/" + d.id;
    current.current = asked;
    api.deploymentControls(run, d.id).then((c) => { if (alive.current && current.current === asked) setControls(c); }).catch((e) => {
      if (!alive.current || current.current !== asked) return;
      if (e instanceof ApiError && (e.status === 404 || e.status === 409)) setControls(null);
    });
  }, [run, d.id]);

  const refetch = useCallback(() => {
    if (!enabled) return;
    const wait = last.current + 2000 - Date.now();
    if (wait <= 0) fetchNow();
    else if (timer.current === undefined) timer.current = window.setTimeout(fetchNow, wait);
  }, [fetchNow, enabled]);

  // The deployment changes when a live update or a fetch brings a newer view.
  const version = `${d.observed_at ?? 0}|${d.phase}|${d.verdict ?? ""}|${(d.commands ?? []).map((c) => c.id + c.state).join()}`;
  // Another deployment: start over (this runs before the effect below).
  useEffect(() => {
    setControls(null); last.current = 0;
    window.clearTimeout(timer.current); timer.current = undefined;
  }, [fetchNow]);
  useEffect(() => { refetch(); }, [version, refetch]);
  return { controls, refetch: fetchNow };
}

/** The protected-environment rule, in the asker's words, or the server's reason. */
function forbiddenText(reasons: { action: RolloutAction; reason: string }[], env: string, protectedEnv: boolean, abortOK: boolean): string[] {
  const own = reasons.filter((r) => protectedEnv && r.action !== "abort" && /started this run/.test(r.reason));
  const out: string[] = [];
  if (own.length > 0) {
    const verbs = own.map((r) => VERB[r.action]);
    const list = verbs.length > 1 ? `${verbs.slice(0, -1).join(", ")} or ${verbs[verbs.length - 1]}` : verbs[0];
    out.push(`You started this run; on protected environment ${env} someone else must ${list} it.${abortOK ? " You may still abort it." : ""}`);
  }
  for (const r of reasons) {
    if (own.includes(r)) continue;
    const text = r.reason.replace(/^./, (c) => c.toUpperCase()) + (/[.!?)]$/.test(r.reason) ? "" : ".");
    if (!out.includes(text)) out.push(text);
  }
  return out;
}

export function DeploymentControls({ run, d, now }: { run: RunDetail; d: DeploymentView; now: number }) {
  // Only a deployment that is not over and has a rollout has controls.
  const { controls, refetch } = useControls(run.id, d, !d.verdict && (!!d.snapshot?.rollout || (d.commands?.length ?? 0) > 0));
  const [posted, setPosted] = useState<CommandView[]>([]);
  useEffect(() => { setPosted([]); }, [d.id]);
  const known = d.commands ?? [];
  const commands = [...known, ...posted.filter((p) => !known.some((c) => c.id === p.id))];
  const hasRollout = !!d.snapshot?.rollout || commands.length > 0;
  if (!hasRollout) return null;
  const open = !d.verdict;
  return (
    <div className="dctl">
      <RolloutOverview d={d} now={now} />
      {open && controls && <ControlForm run={run} d={d} now={now} controls={controls} latest={commands[commands.length - 1]}
        onGiven={(c) => { setPosted((p) => [...p, c]); refetch(); }} />}
      {commands.length > 0 && <CommandList run={run} d={d} now={now} commands={commands} controls={controls} onChange={refetch} />}
    </div>
  );
}

function ControlForm({ run, d, now, controls, latest, onGiven }: {
  run: RunDetail; d: DeploymentView; now: number; controls: Controls; latest?: CommandView; onGiven: (c: CommandView) => void;
}) {
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [confirmAbort, setConfirmAbort] = useState(false);
  const [confirmFull, setConfirmFull] = useState(false);
  useEffect(() => { setComment(""); setError(null); setConfirmAbort(false); setConfirmFull(false); }, [d.id]);

  const ro = d.snapshot?.rollout;
  const shown = ACTIONS.flatMap((a) => {
    const c = controls.controls.find((x) => x.action === a);
    if (!c) return [];
    if (c.available) return [{ action: a, enabled: true, reason: "" }];
    if ((c.forbidden || c.runner_cannot) && meaningful(a, ro)) return [{ action: a, enabled: false, reason: c.reason ?? "" }];
    return [];
  });
  if (shown.length === 0) return null;
  const abortOK = shown.some((s) => s.action === "abort" && s.enabled);
  const reasons = forbiddenText(shown.filter((s) => !s.enabled).map((s) => ({ action: s.action, reason: s.reason })),
    controls.environment || d.environment, controls.protected, abortOK);
  const anyEnabled = shown.some((s) => s.enabled);
  const f = freshness(d, now);
  const uncertain = f.state === "never" || f.state === "stale" || f.state === "error";
  const reasonId = `dctl-why-${d.id}`;

  const give = async (action: RolloutAction) => {
    setBusy(true); setError(null);
    try {
      const c = await api.command(run.id, d.id, action, comment);
      setComment(""); setConfirmAbort(false);
      onGiven(c);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };

  return (
    <form className="dctl-form" aria-label="Control the rollout" onSubmit={(e) => e.preventDefault()}>
      <h4 className="h-xs">Control the rollout</h4>
      {uncertain && anyEnabled && (
        <p className="dctl-uncertain" role="note">
          {terms().say("What is shown may be out of date. The command will be applied when a {deploy:do} {runner} reaches the cluster, and only if the rollout is still where it is shown; you can withdraw it until then.")}
        </p>
      )}
      {anyEnabled && (
        <label className="field">
          <span>Comment <span className="faint">(optional, kept with the command)</span></span>
          <textarea className="dctl-comment" aria-label="Comment on the command" value={comment} maxLength={2000} rows={2}
            onChange={(e) => setComment(e.target.value)} />
        </label>
      )}
      <ErrorBox error={error} />
      {latest && latest.state !== "withdrawn" && (() => {
        const st = stateOf(latest);
        return (
          <p className={`dctl-last dcmd-${st.tone}`} role={st.tone === "bad" ? "alert" : "status"}>
            Last command, {actionLabel(latest.action).toLowerCase()} by {whoGave(latest)}: {latest.state === "done" && latest.result === "failed" ? "FAILED, it did not take effect (the reason is below)" : st.text}.
          </p>
        );
      })()}
      <div className="approval-actions dctl-actions">
        {shown.map((s) => {
          if (s.action === "abort" && s.enabled && confirmAbort) return null;
          const cls = s.action === "abort" ? "btn btn-danger-outline" : s.action === "promote" ? "btn btn-primary" : "btn";
          return (
            <button key={s.action} type="button" className={cls} data-action={s.action}
              disabled={!s.enabled || busy} title={s.enabled ? WHAT[s.action] : undefined}
              aria-describedby={!s.enabled ? reasonId : undefined}
              onClick={() => (s.action === "abort" ? setConfirmAbort(true) : s.action === "promote-full" ? setConfirmFull(true) : give(s.action))}>
              {LABEL[s.action]}
            </button>
          );
        })}
      </div>
      {confirmFull && (
        <div className="callout callout-bad dctl-confirm" role="alertdialog" aria-label="Confirm full promotion">
          <span>
            <strong>Promote {d.service} in {d.environment} fully?</strong> The new version gets all traffic now, and the remaining steps and analyses are skipped.
            {(d.snapshot?.analysis ?? []).some((a) => a.phase === "Failed" || a.phase === "Error")
              ? " An analysis of this version has failed: promoting past it ships a version a check rejected."
              : (d.snapshot?.analysis ?? []).some((a) => a.phase === "Running") ? " An analysis is still running, and promoting stops waiting for it." : ""}
            {" "}{terms().say("Suggestion: let the analysis finish, or promote one step at a time. You can still abort afterwards, or {rollback:do}.")}
            {!comment.trim() && <strong> Say why in the comment above first: promoting fully needs a reason, which is kept in the {terms().lower("logbook")}.</strong>}
          </span>
          <span className="dctl-confirm-actions">
            <button type="button" className="btn btn-danger dctl-full-confirm" disabled={busy || !comment.trim()} onClick={() => { setConfirmFull(false); give("promote-full"); }}>Yes, promote fully</button>
            <button type="button" className="btn" disabled={busy} onClick={() => setConfirmFull(false)}>Not yet</button>
          </span>
        </div>
      )}
      {confirmAbort && (
        <div className="callout callout-bad dctl-confirm" role="alertdialog" aria-label="Confirm abort">
          <span>Abort the rollout of {d.service} in {d.environment}? All traffic goes back to the stable version.</span>
          <span className="dctl-confirm-actions">
            <button type="button" className="btn btn-danger dctl-abort-confirm" disabled={busy} onClick={() => give("abort")}>Yes, abort</button>
            <button type="button" className="btn" disabled={busy} onClick={() => setConfirmAbort(false)}>Keep it going</button>
          </span>
        </div>
      )}
      {reasons.length > 0 && (
        <div id={reasonId} className="dctl-why">
          {reasons.map((r) => <p key={r}>{r}</p>)}
        </div>
      )}
    </form>
  );
}

/** Who gave a command, in words. */
function whoGave(c: CommandView): string {
  switch (c.origin) {
    case "step": return `step ${c.step}`;
    case "pauses": return `step ${c.step} (pauses: promote)`;
    case "on_cancel": return terms().say("canceling the {run} (on_cancel: abort)");
    default: return c.actor;
  }
}

/** A command's state in words, and its tone. */
function stateOf(c: CommandView): { text: string; tone: string } {
  switch (c.state) {
    case "pending":
      return c.detail
        ? { text: `handed back: ${c.detail}; it will be tried again`, tone: "warn" }
        : { text: terms().say("waiting for a {deploy:do} {runner} to take it"), tone: "wait" };
    case "taken":
      return { text: `taken by ${c.taken_by || terms().say("a {deploy:do} {runner}")}`, tone: "run" };
    case "withdrawn":
      return { text: `withdrawn${c.withdrawn_by ? ` by ${c.withdrawn_by}` : ""}`, tone: "idle" };
    case "done":
      switch (c.result) {
        case "applied": return { text: "applied", tone: "ok" };
        case "already_applied": return { text: `already applied${c.detail ? `: ${c.detail}` : ""}`, tone: "ok" };
        case "not_applied": return { text: `not applied, the rollout is unchanged${c.detail ? `: ${c.detail}` : ""}`, tone: "warn" };
        case "failed": return { text: `FAILED, it did not take effect${c.detail ? `: ${c.detail}` : ""}`, tone: "bad" };
      }
      return { text: String(c.result ?? "done").replace(/_/g, " "), tone: "idle" };
  }
  return { text: String(c.state), tone: "idle" };
}

function CommandList({ run, d, now, commands, controls, onChange }: {
  run: RunDetail; d: DeploymentView; now: number; commands: CommandView[]; controls: Controls | null; onChange: () => void;
}) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<{ id: string; e: unknown } | null>(null);
  const withdraw = async (c: CommandView) => {
    setBusy(c.id); setError(null);
    try { await api.withdrawCommand(run.id, d.id, c.id); onChange(); } catch (e) { setError({ id: c.id, e }); } finally { setBusy(""); }
  };
  return (
    <div className="dctl-commands">
      <h4 className="h-xs">Commands</h4>
      <ol className="dcmd-list" aria-label="Commands given to the rollout">
        {[...commands].reverse().map((c) => {
          const st = stateOf(c);
          return (
            <li key={c.id} className="dcmd" data-state={c.state} data-result={c.result ?? ""}>
              <div className="dcmd-head">
                <strong>{actionLabel(c.action)}</strong>
                <span>by {whoGave(c)}</span>
                <span className="faint" title={clock(c.at)}>{ago(c.at, now)}</span>
                <span className={`dpill dpill-${st.tone}`}>{c.state === "done" ? (c.result ?? "done").replace(/_/g, " ") : c.state}</span>
              </div>
              {c.comment && <blockquote className="approval-comment">{c.comment}</blockquote>}
              <div className={`dcmd-state dcmd-${st.tone}`} role={st.tone === "bad" ? "alert" : undefined}>{st.text.replace(/^./, (x) => x.toUpperCase())}{c.done_at && c.state !== "pending" && c.state !== "taken" ? <span className="faint">, {clock(c.done_at)}</span> : null}.</div>
              <div className="dep-notes">
                <span>for the rollout {c.expect.step !== undefined ? <>{terms().say("at {canary} step")} {c.expect.step + 1}{c.expect.paused ? ", paused" : ""}</> : c.expect.paused ? "paused" : "as it was"}</span>
                {c.expect.pod_hash && <span>pod template <Mono>{c.expect.pod_hash}</Mono></span>}
                {c.tries > 1 && <span>taken {c.tries} times</span>}
              </div>
              {c.state === "pending" && controls?.withdraw && (
                <div>
                  <button type="button" className="btn btn-sm" disabled={busy === c.id} onClick={() => withdraw(c)}>Withdraw</button>
                </div>
              )}
              {error?.id === c.id && <ErrorBox error={error.e} />}
            </li>
          );
        })}
      </ol>
    </div>
  );
}
