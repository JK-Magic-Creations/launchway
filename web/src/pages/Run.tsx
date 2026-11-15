import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type AttemptInputs, type DeploymentView, type InputVar, type LogLine, type RerunPlan, type RunDetail, type RunEvent, type RunPipelineText, type StepView } from "../api";
import { ago, clock, duration, elapsed, refLabel, shortSha, starterLabel } from "../format";
import { useNow, useStream } from "../live";
import { canOperate, useMe } from "../auth";
import { Graph } from "../components/Graph";
import { LogView } from "../components/LogView";
import { BranchIcon, CommitIcon, ErrorBox, Link, Mono, StatusPill, shownStatus, stepLabel } from "../components/ui";
import { deploymentOf, DeploymentPanel, DeploymentSummary, environmentOf, freshness, phaseLabel, shortPhase, verdictLabel } from "../components/Deployment";
import { LibraryList, TemplateFacts } from "../components/Libraries";
import { openInEditor } from "./EditPipeline";
import { navigate } from "../router";
import { TestsTab } from "./RunTests";
import { TestCounts, failing } from "../components/Tests";

// stepFromURL is the step named by ?step= in the address, if any.
function stepFromURL(): string | null {
  return new URLSearchParams(location.search).get("step");
}

type Tab = "graph" | "tests" | "events" | "definition";

// tabFromURL is the tab named by ?tab= in the address.
function tabFromURL(): Tab {
  const t = new URLSearchParams(location.search).get("tab");
  return t === "tests" || t === "events" || t === "definition" ? t : "graph";
}

// setQuery changes the address's query without a new history entry.
function setQuery(changes: Record<string, string>) {
  const q = new URLSearchParams(location.search);
  for (const [k, v] of Object.entries(changes)) {
    if (v) q.set(k, v); else q.delete(k);
  }
  const qs = q.toString();
  history.replaceState(null, "", location.pathname + (qs ? "?" + qs : ""));
}

export function RunPage({ id }: { id: string }) {
  const [run, setRun] = useState<RunDetail | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [tab, setTabState] = useState<Tab>(tabFromURL);
  const [testStep, setTestStepState] = useState(() => new URLSearchParams(location.search).get("tests_step") ?? "");
  const setTab = useCallback((t: Tab) => { setTabState(t); setQuery({ tab: t === "graph" ? "" : t }); }, []);
  const setTestStep = useCallback((s: string) => { setTestStepState(s); setQuery({ tests_step: s }); }, []);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);
  const logSink = useRef<((step: string, attempt: number, lines: LogLine[]) => void) | null>(null);

  // A fetch answers with the run as it was when the server read it, which
  // may be older than live updates that arrive while it is on its way. So
  // if an update arrives meanwhile, fetch again once the fetches in flight
  // are done, and drop answers for a run no longer shown.
  const current = useRef(id);
  current.current = id;
  const inflight = useRef(0);
  const stale = useRef(false);
  const load = useCallback((background = false) => {
    inflight.current++;
    api.run(id, background)
      .then((r) => { if (current.current === id) { setRun(r); setError(null); } })
      .catch((e) => { if (current.current === id) setError(e); })
      .finally(() => {
        inflight.current--;
        if (inflight.current === 0 && stale.current && current.current === id) {
          stale.current = false;
          load(true);
        }
      });
  }, [id]);
  const updated = () => { if (inflight.current > 0) stale.current = true; };
  useEffect(() => {
    setRun(null); setSelected(stepFromURL()); setTabState(tabFromURL());
    setTestStepState(new URLSearchParams(location.search).get("tests_step") ?? "");
    load();
  }, [id, load]);

  // Keep the selected step in the address, so it can be shared and reloaded.
  const select = useCallback((step: string) => {
    setSelected(step);
    setQuery({ step });
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
    run: (d) => {
      updated();
      setRun((prev) => (prev ? { ...prev, ...(d as Partial<RunDetail>), step_list: prev.step_list } : prev));
    },
    step: (d) => {
      updated();
      const s = d as StepView;
      setRun((prev) => {
        if (!prev) return prev;
        // Live step updates carry the step's state, not its definition.
        return { ...prev, step_list: prev.step_list.map((x) => (x.id === s.id ? { ...x, ...s, image: x.image, run: x.run, needs: x.needs, when: x.when, secrets: x.secrets, kind: x.kind, message: x.message, description: x.description, retry: x.retry, timeout: x.timeout, group: x.group } : x)) };
      });
    },
    deployment: (d) => {
      // What a deploy runner observes; it is not in the run's events, so a
      // fetch in flight need not be repeated for it.
      const v = d as DeploymentView;
      setRun((prev) => {
        if (!prev) return prev;
        const list = prev.deployments ?? [];
        const old = list.find((x) => x.id === v.id);
        const merged = { ...v, timeline: v.timeline ?? old?.timeline };
        return { ...prev, deployments: old ? list.map((x) => (x.id === v.id ? merged : x)) : [merged, ...list] };
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
  // Only approvals wait for someone to decide; deploy steps wait for their
  // environment's lock or for the deployment to finish.
  const waitingStep = done ? undefined : run.step_list.find((s) => s.status === "waiting" && s.kind === "approval");
  const deploying = done ? undefined : run.step_list.find((s) => s.status === "waiting" && (s.kind === "deploy" || s.kind === "rollback" || s.kind === "rollout"));
  const phases = deployPhases(run);
  const mayOperate = canOperate(me, run.project);
  // A finished run can be re-run from any step (the server says if not).
  const mayRerun = done && mayOperate && !run.needs_newer_server;
  const libraries = run.graph.libraries ?? [];
  const overridden = libraries.filter((l) => l.override);

  return (
    <div className="page page-wide">
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link href="/">Runs</Link><span>/</span><span>{run.project}</span><span>/</span><span>#{run.number}</span>
      </nav>

      <header className="run-head">
        <div className="run-head-main">
          <div className="run-title-row">
            {run.cancel_requested && !done ? <StatusPill status={run.status} label="Canceling" />
              : waitingStep ? <StatusPill status="waiting" label="Waiting for approval" />
              : deploying ? <StatusPill status={shownStatus(deploying)} label={stepLabel({ ...deploying, environment: environmentOf(run, deploying) }, deployingPhase(run, deploying))} />
              : <StatusPill status={run.status} />}
            {run.draft && <span className="badge-draft" title="Pipeline text from the editor, not from Git">Draft</span>}
            {overridden.length > 0 && <span className="badge-override" title="Started with another version of a library than the pipeline names">Library override</span>}
            <h1>{run.message || run.pipeline}</h1>
          </div>
          <div className="run-meta">
            <span className="meta"><strong>{run.project}</strong> #{run.number}</span>
            {refLabel(run) && <span className="meta" title={run.ref}><BranchIcon />{refLabel(run)}</span>}
            {run.sha && <span className="meta mono" title={run.sha}><CommitIcon />{shortSha(run.sha)}</span>}
            <span className="meta">{starterLabel(run)}</span>
            <span className="meta" title={clock(run.created_at)}>{ago(run.created_at, now)}</span>
            {run.started_at > 0 && <span className="meta">took {elapsed(run.started_at, run.finished_at, now)}</span>}
            <span className={"live live-sm " + (live ? "live-on" : "")}><span className="dot" />{live ? "live" : "…"}</span>
          </div>
          {(run.rerun_of || run.reruns.length > 0) && (
            <div className="run-meta">
              {run.rerun_of && (
                <span className="meta rerun-of">
                  Re-run of <Link href={`/runs/${run.rerun_of}?step=${encodeURIComponent(run.rerun_from ?? "")}`}>#{run.rerun_of_number}</Link> from <strong>{run.rerun_from}</strong>
                </span>
              )}
              {run.reruns.length > 0 && (
                <span className="meta">
                  Re-run as {run.reruns.map((r, i) => (
                    <span key={r.id}>{i > 0 && ", "}<Link href={`/runs/${r.id}`}>#{r.number}</Link> from {r.from}</span>
                  ))}
                </span>
              )}
            </div>
          )}
          {(libraries.length > 0 || run.graph.origin) && (
            <div className="run-meta">
              {libraries.length > 0 && <><span className="meta">Libraries</span><LibraryList libraries={libraries} /></>}
              {run.graph.origin && <span className="meta">pipeline configured on the server</span>}
            </div>
          )}
        </div>
        <div className="run-actions">
          {mayOperate && !done && !run.cancel_requested && (
            <button type="button" className="btn btn-danger-outline" disabled={busy} onClick={() => act(() => api.cancel(run.id))}>Cancel run</button>
          )}
          {mayOperate && !run.needs_newer_server && (run.status === "failed" || run.status === "canceled") && (
            <button type="button" className="btn btn-primary" disabled={busy}
              title="Runs again, in this run, only the steps that did not succeed. To start a new run from a step of your choice, open the step and choose Re-run from this step."
              onClick={() => act(async () => { await api.resume(run.id); if (firstFailed) select(firstFailed.id); })}>
              {firstFailed ? `Resume from ${firstFailed.id}` : "Resume"}
            </button>
          )}
        </div>
      </header>
      <ErrorBox error={actionErr} />

      {run.needs_newer_server && (
        <div className="callout callout-draft">
          <strong>Needs a newer server.</strong> A newer version of Launchway wrote parts of this run that this server does not understand
          {run.not_understood.length > 0 && <> ({run.not_understood.join("; ")})</>}. This server shows it but leaves it alone; a server of the newer version carries it on.
        </div>
      )}

      {run.cancel_requested && run.superseded_by && (
        <div className="callout callout-draft">
          <strong>Superseded.</strong> Run <Link href={`/runs/${run.superseded_by}`}>#{run.superseded_by_number}</Link> of the same {run.pull_request ? "pull request" : "branch or tag"} started
          {" "}with <Mono>supersede: true</Mono> in its pipeline, so this older run was canceled.
        </div>
      )}

      {run.draft && (
        <div className="callout callout-draft">
          <strong>Draft run.</strong> It ran pipeline text from the editor that is not in Git, against {run.branch || run.tag} at <Mono>{shortSha(run.sha)}</Mono>.
          {" "}See it on the <button type="button" className="linkbtn" onClick={() => setTab("definition")}>Pipeline</button> tab.
        </div>
      )}

      {overridden.length > 0 && (
        <div className="callout callout-draft">
          <strong>Library override.</strong> Started with {overridden.map((l, i) => (
            <span key={l.name}>{i > 0 && ", "}<Mono>{l.name}</Mono> at <Mono>{l.override}</Mono> ({l.commit ? <Mono>{shortSha(l.commit)}</Mono> : "?"}) instead of <Mono>{l.version}</Mono></span>
          ))}, the version{overridden.length > 1 ? "s" : ""} the pipeline names. Steps of its templates ran with those files.
        </div>
      )}

      {run.graph.origin && (
        <div className="callout callout-info">
          The repository has no <Mono>.launchway/pipeline.yaml</Mono> at this commit, so the run used the pipeline configured for {run.project} on the server.
          {" "}See it on the <button type="button" className="linkbtn" onClick={() => setTab("definition")}>Pipeline</button> tab.
        </div>
      )}

      {waitingStep && selected !== waitingStep.id && (
        <div className="callout callout-wait">
          <span><strong>{waitingStep.id}</strong> is waiting for approval: {waitingStep.message}</span>
          <button type="button" className="btn" onClick={() => select(waitingStep.id)}>{mayOperate ? "Decide" : "View"}</button>
        </div>
      )}

      {deploying && selected !== deploying.id && (
        <div className="callout callout-info callout-row">
          <span><strong>{deploying.id}</strong>: {stepLabel({ ...deploying, environment: environmentOf(run, deploying) }, deployingPhase(run, deploying)).replace(/^./, (c) => c.toLowerCase())}</span>
          <button type="button" className="btn btn-sm" onClick={() => select(deploying.id)}>View</button>
        </div>
      )}

      {run.status === "failed" && firstFailed && (
        <div className="callout callout-bad">
          <strong>{failedCount === 1 ? `${firstFailed.id} failed` : `${failedCount} steps failed`}.</strong>{" "}
          {firstFailed.error}. Steps that succeeded are kept: resuming runs only what did not finish, using the same commit.
          {firstFailed.excerpt && <Excerpt step={firstFailed} />}
        </div>
      )}

      <DeploymentSummary run={run} onSelect={(s) => { select(s); setTab("graph"); }} />

      <div className="tabs" role="tablist">
        {([["graph", "Steps and logs"], ["tests", "Tests"], ["events", "Event log"], ["definition", "Pipeline"]] as [Tab, string][]).map(([k, label]) => (
          <button key={k} role="tab" type="button" aria-selected={tab === k} className={"tab" + (tab === k ? " tab-on" : "")} onClick={() => setTab(k)}>
            {label}
            {k === "tests" && run.tests && (failing(run.tests) > 0
              ? <span className="tab-n tab-n-bad">{failing(run.tests)} failed</span>
              : <span className="tab-n">{run.tests.total}</span>)}
          </button>
        ))}
      </div>

      {tab === "graph" && (
        <>
          <section className="card card-flush">
            <Graph levels={run.levels} steps={run.step_list} groups={run.graph.groups} selected={selected} onSelect={select} now={now} phases={phases} />
          </section>
          {step && (step.kind === "approval"
            ? <ApprovalPanel run={run} step={step} now={now} mayDecide={mayOperate} rerun={mayRerun} />
            : <StepPanel run={run} step={step} now={now} sink={logSink} rerun={mayRerun}
              showTests={(s) => { setTestStep(s); setTab("tests"); }} />)}
        </>
      )}
      {tab === "tests" && <TestsTab run={run} step={testStep} onStep={setTestStep}
        version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "events" && <EventsTab id={run.id} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "definition" && <DefinitionTab run={run} />}
    </div>
  );
}

function StepPanel({ run, step, now, sink, rerun, showTests }: {
  run: RunDetail; step: StepView; now: number;
  sink: React.MutableRefObject<((step: string, attempt: number, lines: LogLine[]) => void) | null>;
  rerun: boolean;
  showTests: (step: string) => void;
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
  const deploys = step.kind === "deploy" || step.kind === "rollback" || step.kind === "rollout";
  const env = deploys ? environmentOf(run, step) : "";
  const reused = !!step.reused_from;
  const group = step.group ? run.graph.groups?.find((g) => g.id === step.group) : undefined;
  const outputs = Object.entries(step.outputs ?? {});
  const secrets = Object.entries(step.secrets ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const empty = !loaded ? "Loading…"
    : reused ? `Not run in this run: reused from #${step.reused_from_number}, where its log is.`
    : step.status === "pending" ? "Waiting for the steps it needs."
    : step.status === "queued" ? "Waiting for a runner."
    : step.status === "skipped" ? step.error || "Skipped."
    : deploys && step.status === "waiting" && step.wait_kind === "lock" ? `Waiting for ${env}'s lock: nothing is deployed yet.`
    : deploys && step.status === "waiting" ? "The deploy runner's part is done; the step waits for what deploy runners observe (above)."
    : step.status === "canceled" && step.attempt === 0 ? "Canceled before it started."
    : "No output.";

  return (
    <section className="steppanel">
      <div className="steppanel-head">
        <div>
          <h2>{step.id}</h2>
          {step.description && <p className="sub step-desc">{step.description}</p>}
          <div className="run-meta">
            <StatusPill status={shownStatus(step)} label={stepLabel({ ...step, environment: env || step.environment })} />
            {deploys && <span className="meta">{step.kind === "rollback" ? `rollback of ${step.deployment}` : step.kind === "rollout" ? `acts on the rollout of ${step.deployment}` : `deploy to ${env}`}</span>}
            {reused && <span className="badge-reused">Reused from #{step.reused_from_number}</span>}
            {step.started_at > 0 && <span className="meta">{elapsed(step.started_at, step.finished_at, now)}</span>}
            {step.exit_code !== null && step.exit_code !== undefined && <span className="meta">exit code {step.exit_code}</span>}
            {step.runner && <span className="meta">on {step.runner}</span>}
            {step.container && !step.container.startsWith("image-") && <span className="meta">in <strong>{step.container}</strong></span>}
            {step.image && <span className="meta mono">{step.image}</span>}
          </div>
        </div>
        <div className="steppanel-actions">
          {step.attempt > 1 && (
            <div className="chips" role="group" aria-label="Attempt">
              {Array.from({ length: step.attempt }, (_, i) => i + 1).map((a) => (
                <button key={a} type="button" className={"chip" + (attempt === a ? " chip-on" : "")} aria-pressed={attempt === a}
                  onClick={() => setAttempt(a)}>Attempt {a}</button>
              ))}
            </div>
          )}
          {rerun && <RerunButton run={run} step={step} />}
        </div>
      </div>
      {reused && <ReusedNote run={run} step={step} />}
      {deploys && <DeploymentPanel run={run} step={step} />}
      {step.tests && (
        <div className={"callout tstep-callout " + (failing(step.tests) > 0 ? "callout-bad" : "callout-info")}>
          <span>
            <strong>{failing(step.tests) > 0 ? `${failing(step.tests)} of ${step.tests.total} tests failed`
              : step.tests.total === 0 ? "Its reports held no tests" : `All ${step.tests.passed} tests that ran passed`}</strong>
            {step.tests.skipped > 0 && `, ${step.tests.skipped} skipped`}
          </span>
          <TestCounts t={step.tests} />
          <button type="button" className="btn btn-sm" onClick={() => showTests(step.id)}>
            {failing(step.tests) > 0 ? "See the failed tests" : "See the tests"}
          </button>
        </div>
      )}
      {step.status === "failed" && following && (step.excerpt || (step.error && !/^exit code \d+$/.test(step.error))) && (
        <div className="callout callout-bad">
          {step.error && !/^exit code \d+$/.test(step.error) && <div>{step.error}</div>}
          {step.excerpt && <Excerpt step={step} />}
        </div>
      )}
      {!(deploys && step.attempt === 0) && <LogView lines={lines} live={isLive} empty={empty} />}
      <div className="stepinfo">
        {step.run && (
          <details>
            <summary>Script</summary>
            <pre className="code">{step.run}</pre>
          </details>
        )}
        <dl className="kv">
          {group && <TemplateFacts group={group} libraries={run.graph.libraries} />}
          {step.needs && step.needs.length > 0 && <><dt>Needs</dt><dd>{step.needs.join(", ")}</dd></>}
          {step.when && <><dt>When</dt><dd className="mono">{JSON.stringify(step.when)}</dd></>}
          {secrets.length > 0 && <><dt>Secrets</dt><dd>{secrets.map(([name, r]) => <div key={name}><Mono>{name}</Mono> ← <Mono>{`${r.store}:${r.key}${r.field ? `#${r.field}` : ""}`}</Mono></div>)}</dd></>}
          <dt>Retry</dt><dd>{step.retry ? `up to ${step.retry} more time${step.retry > 1 ? "s" : ""}` : "no"}</dd>
          <dt>Timeout</dt><dd>{step.timeout}</dd>
          {!deploys && step.queued_at > 0 && step.started_at > 0 && <><dt>Waited</dt><dd>{duration(step.started_at - step.queued_at)} for a runner</dd></>}
          {outputs.length > 0 && <><dt>Outputs</dt><dd>{outputs.map(([k, v]) => <div key={k}><Mono>{k}</Mono> = <Mono>{v}</Mono></div>)}</dd></>}
        </dl>
        {!deploys && <InputsView inputs={step.inputs?.find((a) => a.attempt === attempt)} step={step} />}
      </div>
    </section>
  );
}

// ApprovalPanel shows an approval step: what it asks, the context to decide
// in, and the decision, or the buttons to make one.
function ApprovalPanel({ run, step, now, mayDecide, rerun }: { run: RunDetail; step: StepView; now: number; mayDecide: boolean; rerun: boolean }) {
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
  const starter = starterLabel(run);
  const said = step.comment && <blockquote className="approval-comment">{step.comment}</blockquote>;
  // A linked approval guards a deployment: it shows the live rollout.
  const guarded = step.deployment ? deploymentOf(run, step) : undefined;

  return (
    <section className="steppanel">
      <div className="steppanel-head">
        <div>
          <h2>{step.id}</h2>
          {step.description && <p className="sub step-desc">{step.description}</p>}
          <div className="run-meta">
            <StatusPill status={step.status} />
            <span className="meta">approval</span>
            {step.reused_from && <span className="badge-reused">Reused from #{step.reused_from_number}</span>}
            {step.attempt > 1 && <span className="meta">asked {step.attempt} times</span>}
          </div>
        </div>
        {rerun && <div className="steppanel-actions"><RerunButton run={run} step={step} /></div>}
      </div>
      <div className="card approval">
        <p className="approval-q">{step.message}</p>
        {step.deployment && (
          <p className="sub approval-guards">
            {guarded
              ? <>This approval guards the deployment of {guarded.service} to {guarded.environment}: it is rejected by itself if the rollout is aborted.</>
              : <>This approval guards the deployment made by {step.deployment}: it is rejected by itself if the rollout is aborted.</>}
          </p>
        )}
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
          {refLabel(run) && <><dt>{run.pull_request ? "Pull request" : run.branch ? "Branch" : "Tag"}</dt><dd><span className="meta"><BranchIcon />{refLabel(run)}</span></dd></>}
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
          <div className="callout callout-ok">
            <strong>Approved by {step.decided_by}</strong>, {clock(step.decided_at)}
            {step.reused_from && <> in <Link href={`/runs/${step.reused_from}?step=${encodeURIComponent(step.id)}`}>run #{step.reused_from_number}</Link>. This re-run reused that decision; nobody was asked again.</>}
            {said}
          </div>
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
      {guarded && <DeploymentPanel run={run} step={step} />}
    </section>
  );
}

const EVENT_TEXT: Record<string, (d: Record<string, unknown>) => string> = {
  "run.created": (d) => `Run created (${(d.trigger as { event?: string })?.event ?? "manual"})`,
  "run.cancel_requested": (d) => d.reason ? `Canceled: ${d.reason}` : `Cancel requested by ${d.actor ?? "someone"}`,
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
  "deploy.waiting_lock": (d) => {
    const x = deployOf(d);
    const h = x.holder as { number?: number; step?: string } | undefined;
    return `${d.step}: waiting for ${x.environment}'s lock${h ? `, held by run #${h.number}${h.step ? ` (${h.step})` : ""}` : ""}`;
  },
  "deploy.opened": (d) => {
    const x = deployOf(d);
    const prev = x.previous as { version?: string } | undefined;
    return `${d.step}: took ${x.environment}'s lock and started deploying ${x.service} ${x.version}${prev?.version ? ` (it ran ${prev.version})` : ""}`;
  },
  "deploy.action_done": (d) => {
    const x = deployOf(d);
    const f = (x.facts ?? {}) as { repo?: string; branch?: string; commit?: string; changed?: boolean; revert?: string; reverted?: boolean; sync?: string };
    const sync = f.sync ? `; sync: ${f.sync}` : "";
    if (x.outcome !== "succeeded") return `${d.step}: the deploy runner could not ${x.action === "revert" ? "revert the commit" : "commit the version"}`;
    if (x.action === "revert") return f.reverted ? `${d.step}: reverted with commit ${shortSha(f.revert ?? "")} on ${f.branch}${sync}` : `${d.step}: there was nothing to revert${sync}`;
    return f.changed ? `${d.step}: committed ${shortSha(f.commit ?? "")} to ${f.repo} on ${f.branch}${sync}` : `${d.step}: ${f.repo} on ${f.branch} already held this version${sync}`;
  },
  "deploy.observing": (d) => {
    const x = deployOf(d);
    return `${d.step}: waiting until Argo CD has synced ${shortSha(String(x.commit ?? ""))} and it is ${x.until ?? "healthy"}${x.deadline ? `, until ${clock(x.deadline as number)} at most` : ""}`;
  },
  "deploy.phase": (d) => {
    const x = deployOf(d);
    return `${d.step}: ${phaseLabel(String(x.phase ?? ""))}${x.detail ? ` (${x.detail})` : ""}`;
  },
  "deploy.closed": (d) => `${d.step}: deployment over, ${verdictLabel(String(deployOf(d).verdict ?? "")).toLowerCase()}; the lock is released`,
  "deploy.command": (d) => {
    const c = commandOf(d);
    const who = c.origin === "step" ? `step ${d.step}` : c.origin === "pauses" ? `step ${d.step} (pauses: promote)` : c.origin === "on_cancel" ? "canceling the run (on_cancel: abort)" : String(c.actor ?? "someone");
    return `${d.step}: ${c.action === "promote-full" ? "full promotion" : c.action} asked for by ${who}${c.comment ? `: ${c.comment}` : ""}`;
  },
  "deploy.command_taken": (d) => {
    const c = commandOf(d);
    return c.take ? `${d.step}: command taken by ${c.runner ?? "a deploy runner"}` : `${d.step}: command handed back${c.detail ? `: ${c.detail}` : ""}`;
  },
  "deploy.command_done": (d) => {
    const c = commandOf(d);
    return `${d.step}: command ${String(c.result ?? "done").replace(/_/g, " ")}${c.detail ? `: ${c.detail}` : ""}`;
  },
  "deploy.command_withdrawn": (d) => `${d.step}: command withdrawn by ${commandOf(d).actor ?? "someone"}`,
  "step.reused": (d) => `${d.step}: reused from run #${(d.reused as { number?: number })?.number ?? "?"} (${d.outcome})`,
};

function deployOf(d: Record<string, unknown>): Record<string, unknown> {
  return (d.deploy as Record<string, unknown> | undefined) ?? {};
}

function commandOf(d: Record<string, unknown>): Record<string, string | undefined> {
  return (deployOf(d).command as Record<string, string | undefined> | undefined) ?? {};
}

// deployPhases says, for each deploy or rollback step at work, where its
// deployment stands, in words. Out-of-date observations say so instead.
function deployPhases(run: RunDetail): Record<string, string> {
  const out: Record<string, string> = {};
  const now = Date.now();
  for (const s of run.step_list) {
    if (s.status === "waiting" && s.wait_kind === "lock") { out[s.id] = "waiting for lock"; continue; }
    if (s.status !== "waiting" || s.wait_kind !== "observe") continue;
    const d: DeploymentView | undefined = deploymentOf(run, s);
    if (!d) continue;
    const f = freshness(d, now);
    const where = s.kind === "rollback" ? "rollback" : s.kind === "rollout" ? "rollout" : environmentOf(run, s) || "deploy";
    out[s.id] = `${where}: ${f.state === "never" ? "not observed" : f.state === "error" ? "cannot see it" : f.state === "stale" ? "out of date" : shortPhase(d.phase)}`;
  }
  return out;
}

// deployingPhase is where a deploy step's deployment stands, in words, only
// if what was observed is current (the panel explains the other cases).
function deployingPhase(run: RunDetail, s: StepView): string | undefined {
  if (s.wait_kind !== "observe") return undefined;
  const d = deploymentOf(run, s);
  if (!d) return undefined;
  const f = freshness(d, Date.now());
  return f.state === "fresh" ? phaseLabel(d.phase) : undefined;
}

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

// ReusedNote explains a step a re-run did not run, and links to where it ran.
function ReusedNote({ run, step }: { run: RunDetail; step: StepView }) {
  const href = `/runs/${step.reused_from}?step=${encodeURIComponent(step.id)}`;
  return (
    <div className="callout callout-info">
      <strong>Reused from run #{step.reused_from_number}.</strong> This re-run did not run {step.id}: it kept the outcome{Object.keys(step.outputs ?? {}).length > 0 ? " and outputs" : ""} it had
      {" "}there{step.reused_attempt ? ` (attempt ${step.reused_attempt})` : ""}, and passed them on to the steps after it. Files it wrote to that run's workspace are not in this one.
      {" "}<Link href={href}>See its log in #{step.reused_from_number}</Link>{run.rerun_of !== step.reused_from && run.rerun_of_number ? `, which #${run.rerun_of_number} had reused too` : ""}.
    </div>
  );
}

// fromLabel says where an input came from, in a few words.
function fromLabel(v: InputVar, container?: string): string {
  switch (v.from) {
    case "needs": return `from ${v.step} · ${v.output}`;
    case "container": return container ? `container ${container}` : "container";
    case "step": return "the step's env";
    case "template": return `template parameter, ${v.step}`;
    case "launchway": return "Launchway";
    default: return v.from;
  }
}

// InputsView shows what one attempt of a step was given: its environment
// with where each value came from, the secrets it asked for (references,
// never values), and the image and library commit it ran with.
function InputsView({ inputs, step }: { inputs?: AttemptInputs; step: StepView }) {
  if (!inputs) {
    return (
      <section className="inputs">
        <h3 className="h-sm">Inputs</h3>
        <p className="sub">{step.reused_from ? `Recorded with the step in run #${step.reused_from_number}.` : step.attempt === 0 ? "Recorded when a runner takes the step." : "Not recorded for this attempt (it ran on an older server)."}</p>
      </section>
    );
  }
  const { inputs: inp } = inputs;
  const custom = inp.env.filter((v) => v.from !== "launchway");
  const standard = inp.env.filter((v) => v.from === "launchway");
  const row = (v: InputVar) => (
    <tr key={v.name}>
      <td className="mono">{v.name}</td>
      <td className="mono inputs-value">{v.value}{v.truncated && <span className="faint"> …(shortened in this record)</span>}</td>
      <td className="inputs-from">{fromLabel(v, inp.container)}</td>
    </tr>
  );
  return (
    <section className="inputs" aria-label={`Inputs of attempt ${inputs.attempt}`}>
      <h3 className="h-sm">Inputs <span className="faint">· attempt {inputs.attempt}</span></h3>
      <dl className="kv">
        {inp.image && <><dt>Image</dt><dd className="mono">{inp.image}{inp.container && !inp.container.startsWith("image-") ? ` (container ${inp.container})` : ""}</dd></>}
        {inp.library && <><dt>Library</dt><dd><Mono>{inp.library.name}</Mono> at <Mono>{shortSha(inp.library.commit)}</Mono></dd></>}
        {inp.secrets && inp.secrets.length > 0 && (
          <><dt>Secrets</dt><dd>{inp.secrets.map((x) => <div key={x.name}><Mono>{x.name}</Mono> ← <Mono>{x.ref}</Mono> <span className="faint">(the value is never recorded)</span></div>)}</dd></>
        )}
      </dl>
      {(custom.length > 0 || (inp.params?.length ?? 0) > 0) && (
        <div className="table-scroll">
          <table className="inputs-table">
            <thead><tr><th>Variable</th><th>Value</th><th>From</th></tr></thead>
            <tbody>{inp.params?.map(row)}{custom.map(row)}</tbody>
          </table>
        </div>
      )}
      <details>
        <summary>Standard values ({standard.length})</summary>
        <div className="table-scroll">
          <table className="inputs-table"><tbody>{standard.map(row)}</tbody></table>
        </div>
      </details>
      {inp.script && inp.script !== step.run && (
        <details>
          <summary>Script as given{inp.script_truncated ? " (shortened in this record)" : ""}</summary>
          <pre className="code">{inp.script}</pre>
        </details>
      )}
    </section>
  );
}

// RerunButton offers a new run of a finished run from the step shown, with
// a preview of what runs again and what is reused.
function RerunButton({ run, step }: { run: RunDetail; step: StepView }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" className="btn btn-sm" onClick={() => setOpen(true)}
        title={`A new run at the same commit: ${step.id} and the steps after it run again; the others are reused.`}>Re-run from this step</button>
      {open && <RerunDialog run={run} step={step} onClose={() => setOpen(false)} />}
    </>
  );
}

function RerunDialog({ run, step, onClose }: { run: RunDetail; step: StepView; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [plan, setPlan] = useState<RerunPlan | null>(null);
  const [refusal, setRefusal] = useState<unknown>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { dialog.current?.showModal(); }, []);
  useEffect(() => { api.rerunPlan(run.id, step.id).then(setPlan).catch(setRefusal); }, [run.id, step.id]);

  const go = async () => {
    setBusy(true); setError(null);
    try {
      const re = await api.rerun(run.id, step.id);
      dialog.current?.close();
      onClose();
      navigate(`/runs/${re.id}?step=${encodeURIComponent(step.id)}`);
    } catch (e) { setError(e); setBusy(false); }
  };

  return (
    <dialog ref={dialog} className="dialog" onClose={onClose} aria-labelledby="rerun-title">
      <form method="dialog" onSubmit={(e) => { e.preventDefault(); go(); }}>
        <h2 id="rerun-title">Re-run #{run.number} from {step.id}</h2>
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
        <p className="sub">To run again only the steps that did not succeed, in this same run, use Resume instead.</p>
        <ErrorBox error={error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={() => { dialog.current?.close(); }}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={!plan || busy}>{busy ? "Starting…" : "Re-run"}</button>
        </div>
      </form>
    </dialog>
  );
}

// Excerpt shows the last lines a failed step wrote, as the runner kept them
// (secrets masked).
function Excerpt({ step }: { step: StepView }) {
  return (
    <figure className="excerpt">
      <figcaption>The last lines {step.id} wrote{step.exit_code !== null && step.exit_code !== undefined ? ` before exit code ${step.exit_code}` : ""}:</figcaption>
      <pre>{step.excerpt}</pre>
    </figure>
  );
}

// DefinitionTab shows the pipeline text the run ran. For a draft run that is
// the text kept with the run, which is in no commit; otherwise the file in
// Git at the run's commit.
function DefinitionTab({ run }: { run: RunDetail }) {
  const [file, setFile] = useState<RunPipelineText | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (run.source.kind !== "git") { setFailed(true); return; }
    api.pipelineFile(run.id).then(setFile).catch(() => setFailed(true));
  }, [run.id, run.source.kind]);
  const graph = useMemo(() => JSON.stringify(run.graph, null, 2), [run.graph]);
  const branch = run.branch;
  const editHref = `/projects/${encodeURIComponent(run.project)}/pipeline?ref=${encodeURIComponent(branch)}`;
  return (
    <div className="split">
      <section className="card">
        <div className="card-head">
          <h2 className="h-sm">
            {file?.draft ? "The draft text this run ran"
              : file?.origin === "server" ? `The pipeline configured for ${run.project} on the server`
              : `.launchway/pipeline.yaml at ${shortSha(run.sha) || "this run"}`}
          </h2>
          {run.source.kind === "git" && branch && (file?.draft
            ? <button type="button" className="btn btn-sm" onClick={() => openInEditor(run.project, branch, run.sha, file.text)}>Edit this draft</button>
            : <Link href={editHref} className="btn btn-sm">Edit this pipeline</Link>)}
        </div>
        {file?.origin === "server" && (
          <p className="sub">The repository had no pipeline file at <Mono>{shortSha(run.sha)}</Mono>, so this is what ran: the project's configuration, kept with the run.
            A <Mono>.launchway/pipeline.yaml</Mono> in the repository takes its place.</p>
        )}
        {file?.draft && (
          <p className="sub">Not in Git: it was typed in the editor and is kept only with this run.
            It is named <Mono>{file.hash}</Mono> in the audit trail. The file on {branch || run.tag} at <Mono>{shortSha(run.sha)}</Mono> may say something else.</p>
        )}
        {file !== null ? <pre className="code">{file.text}</pre> : failed ? <p className="sub">The file is only kept for runs from a Git project.</p> : <div className="loading">Loading…</div>}
      </section>
      <section className="card">
        <h2 className="h-sm">The graph it produced</h2>
        <p className="sub">What the engine actually runs. Kept with the run, so it can be shown and resumed even if the repository changes.</p>
        <pre className="code">{graph}</pre>
      </section>
    </div>
  );
}
