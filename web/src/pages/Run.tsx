import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type LogLine, type RunDetail, type RunEvent, type StepView } from "../api";
import { ago, clock, duration, elapsed, shortSha } from "../format";
import { useNow, useStream } from "../live";
import { canOperate, useMe } from "../auth";
import { Graph } from "../components/Graph";
import { LogView } from "../components/LogView";
import { BranchIcon, CommitIcon, ErrorBox, Link, Mono, StatusPill } from "../components/ui";

// stepFromURL is the step named by ?step= in the address, if any.
function stepFromURL(): string | null {
  return new URLSearchParams(location.search).get("step");
}

type Tab = "graph" | "events" | "definition";

export function RunPage({ id }: { id: string }) {
  const [run, setRun] = useState<RunDetail | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("graph");
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);
  const logSink = useRef<((step: string, attempt: number, lines: LogLine[]) => void) | null>(null);

  const load = useCallback((background = false) => {
    api.run(id, background).then((r) => { setRun(r); setError(null); }).catch(setError);
  }, [id]);
  useEffect(() => { setRun(null); setSelected(stepFromURL()); setTab("graph"); load(); }, [id, load]);

  // Keep the selected step in the address, so it can be shared and reloaded.
  const select = useCallback((step: string) => {
    setSelected(step);
    history.replaceState(null, "", `${location.pathname}?step=${encodeURIComponent(step)}`);
  }, []);

  // Pick a sensible step to show: one waiting for approval, else the first
  // failed one, else a running one, else the last that ran.
  useEffect(() => {
    if (!run || (selected && run.step_list.some((x) => x.id === selected))) return;
    const s = run.step_list.find((x) => x.status === "waiting")
      ?? run.step_list.find((x) => x.status === "failed")
      ?? run.step_list.find((x) => x.status === "running")
      ?? [...run.step_list].reverse().find((x) => x.attempt > 0)
      ?? run.step_list[0];
    if (s) setSelected(s.id);
  }, [run, selected]);

  const live = useStream(`/api/v1/stream?run=${encodeURIComponent(id)}`, {
    run: (d) => setRun((prev) => (prev ? { ...prev, ...(d as Partial<RunDetail>), step_list: prev.step_list } : prev)),
    step: (d) => {
      const s = d as StepView;
      setRun((prev) => {
        if (!prev) return prev;
        // Live step updates carry the step's state, not its definition.
        return { ...prev, step_list: prev.step_list.map((x) => (x.id === s.id ? { ...x, ...s, image: x.image, run: x.run, needs: x.needs, when: x.when, secrets: x.secrets, kind: x.kind, message: x.message } : x)) };
      });
    },
    log: (d) => {
      const b = d as { step: string; attempt: number; lines: LogLine[] };
      logSink.current?.(b.step, b.attempt, b.lines);
    },
  }, () => load(true));

  const done = run ? ["succeeded", "failed", "canceled"].includes(run.status) : false;
  const now = useNow(1000, !done);
  const me = useMe();

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true); setActionErr(null);
    try { await fn(); } catch (e) { setActionErr(e); } finally { setBusy(false); }
  };

  if (error) {
    return <div className="page"><ErrorBox error={error instanceof ApiError && error.status === 404 ? "This run does not exist." : error} /><Link href="/">Back to runs</Link></div>;
  }
  if (!run) return <div className="page"><div className="loading">Loading…</div></div>;

  const step = run.step_list.find((s) => s.id === selected) ?? null;
  const failedCount = run.step_list.filter((s) => s.status === "failed").length;
  const firstFailed = run.step_list.find((s) => s.status === "failed");
  const waitingStep = done ? undefined : run.step_list.find((s) => s.status === "waiting");
  const mayOperate = canOperate(me, run.project);

  return (
    <div className="page page-wide">
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link href="/">Runs</Link><span>/</span><span>{run.project}</span><span>/</span><span>#{run.number}</span>
      </nav>

      <header className="run-head">
        <div className="run-head-main">
          <div className="run-title-row">
            {run.cancel_requested && !done ? <StatusPill status={run.status} label="Canceling" />
              : waitingStep ? <StatusPill status="waiting" />
              : <StatusPill status={run.status} />}
            <h1>{run.message || run.pipeline}</h1>
          </div>
          <div className="run-meta">
            <span className="meta"><strong>{run.project}</strong> #{run.number}</span>
            {(run.branch || run.tag) && <span className="meta"><BranchIcon />{run.branch || run.tag}</span>}
            {run.sha && <span className="meta mono" title={run.sha}><CommitIcon />{shortSha(run.sha)}</span>}
            <span className="meta">{run.event === "push" ? `pushed by ${run.author || run.actor}` : `started by ${run.actor}`}</span>
            <span className="meta" title={clock(run.created_at)}>{ago(run.created_at, now)}</span>
            {run.started_at > 0 && <span className="meta">took {elapsed(run.started_at, run.finished_at, now)}</span>}
            <span className={"live live-sm " + (live ? "live-on" : "")}><span className="dot" />{live ? "live" : "…"}</span>
          </div>
        </div>
        <div className="run-actions">
          {mayOperate && !done && !run.cancel_requested && (
            <button type="button" className="btn btn-danger-outline" disabled={busy} onClick={() => act(() => api.cancel(run.id))}>Cancel run</button>
          )}
          {mayOperate && (run.status === "failed" || run.status === "canceled") && (
            <button type="button" className="btn btn-primary" disabled={busy}
              onClick={() => act(async () => { await api.resume(run.id); if (firstFailed) select(firstFailed.id); })}>
              {firstFailed ? `Resume from ${firstFailed.id}` : "Resume"}
            </button>
          )}
        </div>
      </header>
      <ErrorBox error={actionErr} />

      {waitingStep && selected !== waitingStep.id && (
        <div className="callout callout-wait">
          <span><strong>{waitingStep.id}</strong> is waiting for approval: {waitingStep.message}</span>
          <button type="button" className="btn" onClick={() => select(waitingStep.id)}>{mayOperate ? "Decide" : "View"}</button>
        </div>
      )}

      {run.status === "failed" && firstFailed && (
        <div className="callout callout-bad">
          <strong>{failedCount === 1 ? `${firstFailed.id} failed` : `${failedCount} steps failed`}.</strong>{" "}
          {firstFailed.error}. Steps that succeeded are kept: resuming runs only what did not finish, using the same commit.
        </div>
      )}

      <div className="tabs" role="tablist">
        {([["graph", "Steps and logs"], ["events", "Event log"], ["definition", "Pipeline"]] as [Tab, string][]).map(([k, label]) => (
          <button key={k} role="tab" type="button" aria-selected={tab === k} className={"tab" + (tab === k ? " tab-on" : "")} onClick={() => setTab(k)}>{label}</button>
        ))}
      </div>

      {tab === "graph" && (
        <>
          <section className="card card-flush">
            <Graph levels={run.levels} steps={run.step_list} selected={selected} onSelect={select} now={now} />
          </section>
          {step && (step.kind === "approval"
            ? <ApprovalPanel run={run} step={step} now={now} mayDecide={mayOperate} />
            : <StepPanel run={run} step={step} now={now} sink={logSink} />)}
        </>
      )}
      {tab === "events" && <EventsTab id={run.id} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "definition" && <DefinitionTab run={run} />}
    </div>
  );
}

function StepPanel({ run, step, now, sink }: {
  run: RunDetail; step: StepView; now: number;
  sink: React.MutableRefObject<((step: string, attempt: number, lines: LogLine[]) => void) | null>;
}) {
  const [attempt, setAttempt] = useState(step.attempt);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [loaded, setLoaded] = useState(false);
  const following = attempt === step.attempt;

  // Follow the newest attempt when a retry starts.
  useEffect(() => { setAttempt(step.attempt); }, [step.id, step.attempt]);

  useEffect(() => {
    let alive = true;
    setLines([]); setLoaded(false);
    if (attempt < 1) { setLoaded(true); return; }
    (async () => {
      let after = 0;
      let all: LogLine[] = [];
      for (;;) {
        const r = await api.logs(run.id, step.id, attempt, after).catch(() => null);
        if (!alive || !r) break;
        all = all.concat(r.lines);
        if (r.lines.length < 5000) break;
        after = r.lines[r.lines.length - 1].seq;
      }
      if (alive) { setLines(all); setLoaded(true); }
    })();
    return () => { alive = false; };
  }, [run.id, step.id, attempt]);

  useEffect(() => {
    sink.current = (s, a, incoming) => {
      if (s !== step.id || a !== attempt) return;
      setLines((prev) => {
        const last = prev.length ? prev[prev.length - 1].seq : 0;
        const fresh = incoming.filter((l) => l.seq > last);
        return fresh.length ? prev.concat(fresh) : prev;
      });
    };
    return () => { sink.current = null; };
  }, [step.id, attempt, sink]);

  const isLive = following && (step.status === "running" || step.status === "leased");
  const outputs = Object.entries(step.outputs ?? {});
  const secrets = Object.entries(step.secrets ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const empty = !loaded ? "Loading…"
    : step.status === "pending" ? "Waiting for the steps it needs."
    : step.status === "queued" ? "Waiting for a runner."
    : step.status === "skipped" ? step.error || "Skipped."
    : step.status === "canceled" && step.attempt === 0 ? "Canceled before it started."
    : "No output.";

  return (
    <section className="steppanel">
      <div className="steppanel-head">
        <div>
          <h2>{step.id}</h2>
          <div className="run-meta">
            <StatusPill status={step.status} />
            {step.started_at > 0 && <span className="meta">{elapsed(step.started_at, step.finished_at, now)}</span>}
            {step.exit_code !== null && step.exit_code !== undefined && <span className="meta">exit code {step.exit_code}</span>}
            {step.runner && <span className="meta">on {step.runner}</span>}
            {step.container && !step.container.startsWith("image-") && <span className="meta">in <strong>{step.container}</strong></span>}
            {step.image && <span className="meta mono">{step.image}</span>}
          </div>
        </div>
        {step.attempt > 1 && (
          <div className="chips" role="group" aria-label="Attempt">
            {Array.from({ length: step.attempt }, (_, i) => i + 1).map((a) => (
              <button key={a} type="button" className={"chip" + (attempt === a ? " chip-on" : "")} aria-pressed={attempt === a}
                onClick={() => setAttempt(a)}>Attempt {a}</button>
            ))}
          </div>
        )}
      </div>
      {step.status === "failed" && step.error && following && !/^exit code \d+$/.test(step.error) && <div className="callout callout-bad">{step.error}</div>}
      <LogView lines={lines} live={isLive} empty={empty} />
      <div className="stepinfo">
        <details>
          <summary>Script</summary>
          <pre className="code">{step.run}</pre>
        </details>
        <dl className="kv">
          {step.needs && step.needs.length > 0 && <><dt>Needs</dt><dd>{step.needs.join(", ")}</dd></>}
          {step.when && <><dt>When</dt><dd className="mono">{JSON.stringify(step.when)}</dd></>}
          {secrets.length > 0 && <><dt>Secrets</dt><dd>{secrets.map(([name, r]) => <div key={name}><Mono>{name}</Mono> ← <Mono>{`${r.store}:${r.key}${r.field ? `#${r.field}` : ""}`}</Mono></div>)}</dd></>}
          <dt>Retry</dt><dd>{step.retry ? `up to ${step.retry} more time${step.retry > 1 ? "s" : ""}` : "no"}</dd>
          <dt>Timeout</dt><dd>{step.timeout}</dd>
          {step.queued_at > 0 && step.started_at > 0 && <><dt>Waited</dt><dd>{duration(step.started_at - step.queued_at)} for a runner</dd></>}
          {outputs.length > 0 && <><dt>Outputs</dt><dd>{outputs.map(([k, v]) => <div key={k}><Mono>{k}</Mono> = <Mono>{v}</Mono></div>)}</dd></>}
        </dl>
      </div>
    </section>
  );
}

// ApprovalPanel shows an approval step: what it asks, the context to decide
// in, and the decision, or the buttons to make one.
function ApprovalPanel({ run, step, now, mayDecide }: { run: RunDetail; step: StepView; now: number; mayDecide: boolean }) {
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { setComment(""); setError(null); }, [step.id, step.attempt]);

  const decide = async (approve: boolean) => {
    setBusy(true); setError(null);
    try {
      await (approve ? api.approve : api.reject)(run.id, step.id, comment);
      setComment("");
    } catch (e) { setError(e); } finally { setBusy(false); }
  };

  const waiting = step.status === "waiting";
  // What had succeeded when it asked, not what ran after a decision.
  const succeeded = run.step_list.filter((s) => s.status === "succeeded" && s.id !== step.id &&
    (!step.started_at || s.finished_at <= step.started_at));
  const starter = run.event === "push" ? `pushed by ${run.author || run.actor}` : `started by ${run.actor || "someone"}`;
  const said = step.comment && <blockquote className="approval-comment">{step.comment}</blockquote>;

  return (
    <section className="steppanel">
      <div className="steppanel-head">
        <div>
          <h2>{step.id}</h2>
          <div className="run-meta">
            <StatusPill status={step.status} />
            <span className="meta">approval</span>
            {step.attempt > 1 && <span className="meta">asked {step.attempt} times</span>}
          </div>
        </div>
      </div>
      <div className="card approval">
        <p className="approval-q">{step.message}</p>
        <div className="run-meta">
          {step.started_at > 0 && <span className="meta" title={clock(step.started_at)}>asked {ago(step.started_at, now)}</span>}
          <span className="meta">run {starter}</span>
          {waiting && (
            <span className="meta wait" title={clock(step.deadline)}>
              {step.deadline > now ? `expires in ${duration(step.deadline - now)}, ${clock(step.deadline)}` : "expiring now"}
            </span>
          )}
        </div>
        <dl className="kv approval-ctx">
          {run.message && <><dt>Commit</dt><dd>{run.message}</dd></>}
          {(run.branch || run.tag) && <><dt>{run.branch ? "Branch" : "Tag"}</dt><dd><span className="meta"><BranchIcon />{run.branch || run.tag}</span></dd></>}
          {run.sha && <><dt>Commit id</dt><dd><span className="meta mono" title={run.sha}><CommitIcon />{shortSha(run.sha)}</span></dd></>}
          <dt>Succeeded so far</dt><dd>{succeeded.length ? succeeded.map((s) => s.id).join(", ") : "nothing yet"}</dd>
        </dl>

        {waiting && !mayDecide && (
          <p className="sub">Someone with the operator role on {run.project} must decide this.</p>
        )}
        {waiting && mayDecide && (
          <form className="approval-form" onSubmit={(e) => e.preventDefault()}>
            <label className="field">
              <span>Comment <span className="faint">(optional, kept with the decision)</span></span>
              <textarea value={comment} maxLength={2000} rows={3} onChange={(e) => setComment(e.target.value)} />
            </label>
            <ErrorBox error={error} />
            <div className="approval-actions">
              <button type="button" className="btn btn-primary" disabled={busy} onClick={() => decide(true)}>Approve</button>
              <button type="button" className="btn btn-danger-outline" disabled={busy} onClick={() => decide(false)}>Reject</button>
            </div>
          </form>
        )}
        {step.status === "succeeded" && (
          <div className="callout callout-ok"><strong>Approved by {step.decided_by}</strong>, {clock(step.decided_at)}{said}</div>
        )}
        {step.status === "failed" && (
          <div className="callout callout-bad">
            {step.decided_by
              ? <><strong>Rejected by {step.decided_by}</strong>, {clock(step.decided_at)}{said}</>
              : <><strong>{step.error}</strong>, so it was rejected at {clock(step.decided_at)}.</>}
            {run.status === "failed" && <div>Resuming the run asks again.</div>}
          </div>
        )}
        {step.status === "canceled" && <p className="sub">The run was canceled before anyone decided.</p>}
        {step.status === "skipped" && <p className="sub">{step.error || "Skipped."} Nobody was asked.</p>}
        {step.status === "pending" && (
          <p className="sub">Asks once {step.needs?.length ? step.needs.join(", ") : "the run"} {step.needs && step.needs.length > 1 ? "have" : "has"} succeeded.</p>
        )}
      </div>
    </section>
  );
}

const EVENT_TEXT: Record<string, (d: Record<string, unknown>) => string> = {
  "run.created": (d) => `Run created (${(d.trigger as { event?: string })?.event ?? "manual"})`,
  "run.cancel_requested": (d) => `Cancel requested by ${d.actor ?? "someone"}`,
  "run.resumed": (d) => `Resumed by ${d.actor ?? "someone"}`,
  "run.finished": (d) => `Run ${d.status}`,
  "step.queued": (d) => `${d.step}: queued, attempt ${d.attempt}${d.reason ? ` (${d.reason})` : ""}`,
  "step.skipped": (d) => `${d.step}: skipped, ${d.reason}`,
  "step.leased": (d) => `${d.step}: taken by runner ${d.runner}`,
  "step.started": (d) => `${d.step}: started`,
  "step.finished": (d) => `${d.step}: ${d.outcome}${d.reason ? `, ${d.reason}` : ""}`,
  "step.lost": (d) => `${d.step}: ${d.reason}`,
  "step.canceled": (d) => `${d.step}: canceled`,
  "step.reset": (d) => `${d.step}: reset to run again`,
  "step.waiting": (d) => `${d.step}: waiting for approval until ${clock(d.deadline as number)}`,
  "step.approved": (d) => `${d.step}: approved by ${d.actor}${d.comment ? `: ${d.comment}` : ""}`,
  "step.rejected": (d) => `${d.step}: ${d.reason}`,
};

function EventsTab({ id, version }: { id: string; version: string }) {
  const [events, setEvents] = useState<RunEvent[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { api.events(id).then(setEvents).catch(setError); }, [id, version]);
  return (
    <section className="card">
      <p className="sub">This is the run's permanent record. Everything else on this page is worked out from it, which is also why a run can be resumed after the server restarts.</p>
      <ErrorBox error={error} />
      {events && (
        <ol className="events">
          {events.map((e) => (
            <li key={e.seq}>
              <span className="ev-time mono">{clock(e.at)}</span>
              <span className={"ev-type mono ev-" + e.type.split(".")[0]}>{e.type}</span>
              <span className="ev-text">{(EVENT_TEXT[e.type] ?? (() => ""))(e.data)}</span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function DefinitionTab({ run }: { run: RunDetail }) {
  const [text, setText] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (run.source.kind !== "git") { setFailed(true); return; }
    api.pipelineFile(run.id).then((r) => setText(r.text)).catch(() => setFailed(true));
  }, [run.id, run.source.kind]);
  const graph = useMemo(() => JSON.stringify(run.graph, null, 2), [run.graph]);
  return (
    <div className="split">
      <section className="card">
        <h2 className="h-sm">.launchway/pipeline.yaml at {shortSha(run.sha) || "this run"}</h2>
        {text !== null ? <pre className="code">{text}</pre> : failed ? <p className="sub">The file is only kept for runs from a Git project.</p> : <div className="loading">Loading…</div>}
      </section>
      <section className="card">
        <h2 className="h-sm">The graph it produced</h2>
        <p className="sub">What the engine actually runs. Kept with the run, so it can be shown and resumed even if the repository changes.</p>
        <pre className="code">{graph}</pre>
      </section>
    </div>
  );
}
