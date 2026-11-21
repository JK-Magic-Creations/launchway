import { EXPLAIN } from "../learn/links";
import { LearnLink } from "../learn/LearnLink";
import { RunHeader, storedDensity, saveDensity, type Density } from "../components/RunHeader";
import { RunLine } from "../components/RunLine";
import { whyWaiting } from "../components/whyWaiting";
import { RerunButton, RerunFailedButton, RerunPicker } from "../components/Rerun";
import { StepCauseView } from "../components/StepCause";
import { RunFlowBanners } from "../components/RunFlow";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type AttemptInputs, type DeploymentView, type InputVar, type LogLine, type RunDetail, type RunEvent, type RunPipelineText, type StepView } from "../api";
import { ago, clock, duration, elapsed, refLabel, shortSha, starterLabel } from "../format";
import { Icon } from "../kit/Icon";
import { useNow, useStream } from "../live";
import { canOperate, useMe } from "../auth";
import { Graph } from "../components/Graph";
import { LogView } from "../components/LogView";
import { BranchIcon, CommitIcon, ErrorBox, Link, Mono, StatusPill, shownStatus, stepLabel } from "../components/ui";
import { deploymentOf, DeploymentPanel, DeploymentSummary, environmentOf, phaseLabel, verdictLabel } from "../components/Deployment";
import { TemplateFacts } from "../components/Libraries";
import { openInEditor } from "./EditPipeline";
import { TestsTab } from "./RunTests";
import { TestCounts, failing } from "../components/Tests";
import { RunTrigger } from "../components/RunTrigger";
import { RunVariables } from "../components/Variables";
import { progressText, whoMayDecide, type ApprovalProgress } from "../api/approvals";
import { collapseLoops, LoopPanel, loopById, loopFor } from "../components/Loops";
import { ArtifactsTab } from "./RunArtifacts";
import { CoverageTab } from "./RunCoverage";
import { ReportsTab } from "./RunReports";
import { pct } from "../components/Coverage";
import { SecurityTab } from "./RunSecurity";
import { CopyValue, serious, seriousLabel } from "../components/Security";
import { ImagesTab, imageSteps } from "./RunImages";
import { PolicyTab } from "./RunPolicy";
import { useRunVerifications, VerificationPanel, VerificationsTab, VerificationSummaryRows } from "../components/Verification";
import { FailurePanel } from "../components/FailurePanel";
import { explainFailure } from "../guided/derive";
import { useCrumbLabel } from "../nav/crumbs";
import { AIStepResult, ExplainFailure } from "../components/Explain";
import { DIGEST_KEEP, ImageBuildFacts, ImageIcon, shortImageRef } from "../components/Images";
import { CacheResults } from "../components/CacheResults";
import { Excerpt } from "../components/Excerpt";
import { terms } from "../terms";
import { commandOf, deployOf, deployPhases, deployingPhase } from "./runDeploy";

// Stands for the run until it is loaded, so hooks run in the same order.
const EMPTY_RUN = { id: "", step_list: [] } as unknown as RunDetail;

// stepFromURL is the step named by ?step= in the address, if any.
function stepFromURL(): string | null {
  return new URLSearchParams(location.search).get("step");
}

type Tab = "graph" | "tests" | "coverage" | "reports" | "images" | "verification" | "artifacts" | "security" | "policy" | "events" | "definition";

// tabFromURL is the tab named by ?tab= in the address.
function tabFromURL(): Tab {
  const t = new URLSearchParams(location.search).get("tab");
  return t === "tests" || t === "coverage" || t === "reports" || t === "images" || t === "verification" || t === "artifacts" || t === "security" || t === "policy" || t === "events" || t === "definition" ? t : "graph";
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
  const [density, setDensity] = useState<Density>(storedDensity);
  const changeDensity = useCallback((d: Density) => { setDensity(d); saveDensity(d); }, []);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);
  const logSink = useRef<((step: string, attempt: number, lines: LogLine[]) => void) | null>(null);
  const [covBadge, setCovBadge] = useState<{ run: string; text: string; bad: boolean } | null>(null);
  const [htmlCount, setHtmlCount] = useState<{ run: string; n: number } | null>(null);

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
    if (!run || (selected && (run.step_list.some((x) => x.id === selected) || loopById(run, selected)))) return;
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
        return { ...prev, step_list: prev.step_list.map((x) => (x.id === s.id ? { ...x, ...s, not_before: s.not_before, failure_cause: s.failure_cause, allowed_failure: s.allowed_failure, image: x.image, run: x.run, needs: x.needs, when: x.when, secrets: x.secrets, kind: x.kind, message: x.message, description: x.description, retry: x.retry, timeout: x.timeout, group: x.group } : x)) };
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

  // What the Coverage and Reports tabs hold, for their badges: asked for
  // only when the pipeline declares them, again as steps end.
  const declaresCoverage = !!run?.graph.steps.some((s) => (s.reports?.coverage?.length ?? 0) > 0);
  const declaresHTML = !!run?.graph.steps.some((s) => (s.reports?.html?.length ?? 0) > 0);
  const ended = run ? run.step_list.filter((s) => ["succeeded", "failed", "skipped", "canceled"].includes(s.status)).length : 0;
  useEffect(() => {
    if (!run || !declaresCoverage) return;
    let alive = true;
    api.runCoverage(run.id, true).then((c) => {
      if (!alive) return;
      setCovBadge(c.totals ? { run: run.id, text: pct(c.totals.lines), bad: c.steps.some((s) => s.failed_under) } : null);
    }).catch(() => { /* the tab says what went wrong */ });
    return () => { alive = false; };
  }, [run?.id, declaresCoverage, ended]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!run || !declaresHTML) return;
    let alive = true;
    api.runReports(run.id, true).then((r) => { if (alive) setHtmlCount({ run: run.id, n: r.reports.length }); }).catch(() => { /* shown on the tab */ });
    return () => { alive = false; };
  }, [run?.id, declaresHTML, ended]); // eslint-disable-line react-hooks/exhaustive-deps

  const done = run ? ["succeeded", "failed", "canceled"].includes(run.status) : false;
  const now = useNow(1000, !done);
  const me = useMe();
  const verifications = useRunVerifications(run ?? EMPTY_RUN);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true); setActionErr(null);
    try { await fn(); } catch (e) { setActionErr(e); } finally { setBusy(false); }
  };

  useCrumbLabel(run ? `Run #${run.number} of ${run.project}` : undefined);
  if (error) {
    return <div className="page"><ErrorBox error={error instanceof ApiError && error.status === 404 ? "This run does not exist." : error} /><Link href="/activity">Back to runs</Link></div>;
  }
  if (!run) return <div className="page"><div className="loading">Loading…</div></div>;

  const step = run.step_list.find((s) => s.id === selected) ?? null;
  const loopView = collapseLoops(run);
  const loop = loopFor(run, selected);
  // A step that failed and is allowed to (on_failure: continue) does not fail the run.
  const firstFailed = run.step_list.find((s) => s.status === "failed" && !s.allowed_failure);
  const failure = explainFailure(run.step_list);
  // A run that made a deployment is never resumed: re-running from the step
  // the server names (the failed one, or the deploy step it belongs to) is.
  const act0 = (name: string) => run.actions?.find((a) => a.name === name);
  const resumeBlocked = run.resume?.allowed === false || act0("resume")?.available === false;
  const mayCancel = act0("cancel")?.available !== false;
  const rerunTarget = run.step_list.find((s) => s.id === (run.resume?.rerun_from || firstFailed?.id));
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
  const images = imageSteps(run);
  const builtImages = images.filter((s) => s.outputs?.digest).length;
  const verifySteps = run.step_list.filter((s) => s.kind === "verify");
  const verifyFailed = verifySteps.filter((s) => s.status === "failed").length;
  const verifyWaiting = done ? undefined : verifySteps.find((s) => s.status === "waiting" && s.wait_kind === "verify");
  const tabs: [Tab, string][] = [["graph", "Steps and logs"], ["tests", "Tests"],
    ...(declaresCoverage || tab === "coverage" ? [["coverage", "Coverage"] as [Tab, string]] : []),
    ...(declaresHTML || tab === "reports" ? [["reports", "Reports"] as [Tab, string]] : []),
    ...(images.length ? [["images", "Images"] as [Tab, string]] : []),
    ...(verifySteps.length ? [["verification", "Verification"] as [Tab, string]] : []),
    ["security", "Security"], ["artifacts", "Artifacts"], ...(run.policy ? [["policy", "Policy"] as [Tab, string]] : []),
    ["events", "Event log"], ["definition", "Pipeline"]];
  const shownTab: Tab = (tab === "images" && !images.length) || (tab === "verification" && !verifySteps.length) ? "graph" : tab;

  return (
    <div className={"page page-wide run-page dens-" + density}>

      <RunHeader run={run} now={now} done={done} live={live} density={density} onDensity={changeDensity} select={select} setTab={setTab}
        waitingStep={waitingStep} verifyWaiting={verifyWaiting} deploying={deploying} deployingPhase={deploying ? deployingPhase(run, deploying) : undefined}
        actions={<>
          {mayOperate && !done && !run.cancel_requested && mayCancel && (
            <button type="button" className="btn btn-danger-outline" disabled={busy} onClick={() => act(() => api.cancel(run.id))}>Cancel run</button>
          )}
          {mayOperate && !run.needs_newer_server && run.status === "canceled" && !run.draft && <RerunFailedButton run={run} />}
          {mayOperate && !run.needs_newer_server && run.status === "canceled" && run.resume?.allowed !== false && (
            <button type="button" className="btn btn-primary" disabled={busy}
              title="Runs again, in this run, only the steps that did not succeed. To start a new run from a step of your choice, open the step and choose Re-run from this step."
              onClick={() => act(async () => { await api.resume(run.id); if (firstFailed) select(firstFailed.id); })}>
              {firstFailed ? `Resume from ${firstFailed.id}` : "Resume"}
            </button>
          )}
        </>} />
      <ErrorBox error={actionErr} />
      {actionErr instanceof Error && /Re-run from/.test(actionErr.message) && rerunTarget && mayRerun && (
        <p className="refusal-action"><RerunButton run={run} step={rerunTarget} label={`Re-run from ${rerunTarget.id}`} primary /></p>
      )}

      {run.needs_newer_server && (
        <div className="callout callout-draft">
          <strong>Needs a newer server.</strong> A newer version of Launchway wrote parts of this run that this server does not understand
          {run.not_understood.length > 0 && <> ({run.not_understood.join("; ")})</>}. This server shows it but leaves it alone; a server of the newer version carries it on.
        </div>
      )}

      {run.runner_hint && (
        <div className="callout callout-draft" role="status">
          <strong>Waiting for a runner that builds images.</strong> {run.runner_hint}
        </div>
      )}

      {run.pool_hint && (
        <div className="callout callout-draft" role="status">
          <strong>Waiting for a runner that may take this run.</strong> {run.pool_hint} <Link href="/settings/runners">Runners and pools</Link>
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

      <RunTrigger run={run} />
      <RunVariables runId={run.id} />

      {waitingStep && selected !== waitingStep.id && (
        <div className="callout callout-wait">
          <span><strong>{waitingStep.id}</strong> is waiting for {terms().lower("clearance")}: {waitingStep.message}</span>
          <button type="button" className="btn" onClick={() => select(waitingStep.id)}>{mayOperate ? "Decide" : "View"}</button>
        </div>
      )}

      {verifyWaiting && selected !== verifyWaiting.id && (
        <div className="callout callout-wait">
          <span><strong>{verifyWaiting.id}</strong> is waiting for a person: the verification was inconclusive, and someone must mark it passed or failed.</span>
          <button type="button" className="btn" onClick={() => { select(verifyWaiting.id); setTab("graph"); }}>{mayOperate ? "Decide" : "View"}</button>
        </div>
      )}

      {deploying && selected !== deploying.id && (
        <div className="callout callout-info callout-row">
          <span><strong>{deploying.id}</strong>: {stepLabel({ ...deploying, environment: environmentOf(run, deploying) }, deployingPhase(run, deploying)).replace(/^./, (c) => c.toLowerCase())}</span>
          <button type="button" className="btn btn-sm" onClick={() => select(deploying.id)}>View</button>
        </div>
      )}

      <RunFlowBanners run={run} done={done} onSelect={select} />

      {resumeBlocked && mayOperate && !run.needs_newer_server && (
        <div className="callout callout-info callout-row" role="status">
          <span><strong>This run cannot be resumed.</strong> {run.resume?.reason}. Re-run from <Mono>{rerunTarget?.id ?? ""}</Mono> instead: it makes a new
            deployment attempt of the same version and reuses every step before it, so nothing is rebuilt.</span>
          {rerunTarget && <RerunButton run={run} step={rerunTarget} label={`Re-run from ${rerunTarget.id}`} primary />}
        </div>
      )}

      {run.status === "failed" && failure && firstFailed && (
        <FailurePanel failure={failure} actions={<>
          {mayOperate && !run.needs_newer_server && !resumeBlocked && (
            <button type="button" className="btn btn-primary" disabled={busy}
              title="Runs again, in this same run, only the steps that did not succeed. The steps that passed are kept."
              onClick={() => act(async () => { await api.resume(run.id); select(firstFailed.id); })}>Resume this run from the failed step</button>
          )}
          {mayRerun && rerunTarget && <RerunButton run={run} step={rerunTarget} label={`Re-run from ${rerunTarget.id}`} primary={resumeBlocked} />}
          {mayRerun && <RerunPicker run={run} />}
          {mayOperate && !run.needs_newer_server && !run.draft && !rerunTarget && <RerunFailedButton run={run} />}
          {mayRerun && run.step_list.length > 1 && <RerunButton run={run} step={run.step_list[0]} label="Re-run everything" />}
          <button type="button" className="btn btn-sm" onClick={() => { select(firstFailed.id); setTab("graph"); }}>Open the log of {firstFailed.id}</button>
          {firstFailed.policy_blocked && <button type="button" className="btn btn-sm" onClick={() => setTab("policy")}>See the policy decision</button>}
        </>} />
      )}

      <DeploymentSummary run={run} onSelect={(s) => { select(s); setTab("graph"); }} />
      <VerificationSummaryRows list={verifications.list} onSelect={(s) => { select(s); setTab("graph"); }} />

      <div className="tabs" role="tablist">
        {tabs.map(([k, label]) => (
          <button key={k} role="tab" type="button" aria-selected={shownTab === k} className={"tab" + (shownTab === k ? " tab-on" : "")} onClick={() => setTab(k)}>
            {label}
            {k === "tests" && run.tests && (failing(run.tests) > 0
              ? <span className="tab-n tab-n-bad">{failing(run.tests)} failed</span>
              : <span className="tab-n">{run.tests.total}</span>)}
            {k === "coverage" && covBadge?.run === run.id && <span className={"tab-n" + (covBadge.bad ? " tab-n-bad" : "")} title="Lines covered">{covBadge.text}</span>}
            {k === "reports" && htmlCount?.run === run.id && htmlCount.n > 0 && <span className="tab-n">{htmlCount.n}</span>}
            {k === "images" && builtImages > 0 && <span className="tab-n">{builtImages}</span>}
            {k === "verification" && (verifyFailed > 0 ? <span className="tab-n tab-n-bad">{verifyFailed} failed</span> : <span className="tab-n">{verifySteps.length}</span>)}
            {k === "artifacts" && !!run.artifacts && <span className="tab-n">{run.artifacts}</span>}
            {k === "policy" && run.policy && (run.policy.verdict === "block"
              ? <span className="tab-n tab-n-bad">blocked</span>
              : run.policy.verdict === "warn" ? <span className="tab-n tab-n-warn">warning</span>
              : <span className="tab-n">{run.policy.waived ? `${run.policy.waived} waived` : "passed"}</span>)}
            {k === "security" && run.findings && (serious(run.findings) > 0
              ? <span className="tab-n tab-n-bad">{seriousLabel(run.findings)}</span>
              : <span className="tab-n">{run.findings.total}</span>)}
          </button>
        ))}
      </div>

      {shownTab === "graph" && (
        <>
          <section className="sheet runline-sheet" aria-label="The pipeline of this run">
            <div className="sh-h"><h2>The pipeline</h2><div className="r"><span className="faint small">Choose a step to see it below</span></div></div>
            <div className="sh-b"><RunLine run={run} selected={selected} onSelect={select} now={now} /></div>
          </section>
          {density === "everything" && <section className="card card-flush only-everything" aria-label="Dependency graph">
            <Graph levels={loopView.levels} steps={loopView.steps} groups={run.graph.groups} selected={loopView.nodeOf(selected)} onSelect={select} now={now} phases={phases} />
          </section>}
          {loop && <LoopPanel run={run} loop={loop} selected={selected} onSelect={select} />}
          {step && (step.kind === "approval"
            ? <ApprovalPanel run={run} step={step} now={now} mayDecide={mayOperate} rerun={mayRerun} />
            : <StepPanel run={run} step={step} now={now} sink={logSink} rerun={mayRerun} verifications={verifications}
              showTests={(s) => { setTestStep(s); setTab("tests"); }} showImages={() => setTab("images")} showPolicy={() => setTab("policy")} />)}
        </>
      )}
      {tab === "tests" && <TestsTab run={run} step={testStep} onStep={setTestStep}
        version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "coverage" && <CoverageTab run={run} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "reports" && <ReportsTab run={run} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {shownTab === "images" && <ImagesTab run={run} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()}
        onStep={(s) => { select(s); setTab("graph"); }} onShow={setTab} />}
      {shownTab === "verification" && <VerificationsTab run={run} list={verifications.list} error={verifications.error} reload={verifications.reload}
        onStep={(s) => { select(s); setTab("graph"); }} />}
      {tab === "artifacts" && <ArtifactsTab run={run} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "security" && <SecurityTab run={run} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "policy" && <PolicyTab run={run} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "events" && <EventsTab id={run.id} version={run.status + run.step_list.map((s) => s.status + s.attempt).join()} />}
      {tab === "definition" && <DefinitionTab run={run} />}
    </div>
  );
}

function StepPanel({ run, step, now, sink, rerun, showTests, showImages, showPolicy, verifications }: {
  run: RunDetail; step: StepView; now: number;
  verifications: ReturnType<typeof useRunVerifications>;
  sink: React.MutableRefObject<((step: string, attempt: number, lines: LogLine[]) => void) | null>;
  rerun: boolean;
  showTests: (step: string) => void;
  showImages: () => void;
  showPolicy: () => void;
}) {
  const [attempt, setAttempt] = useState(step.attempt);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [loaded, setLoaded] = useState(false);
  const following = attempt === step.attempt;
  // The what-went-wrong panel above shows the last lines of the first failed step.
  const excerptInPanel = run.status === "failed" && run.step_list.find((x) => x.status === "failed")?.id === step.id;

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

  const why = whyWaiting(run, step, now);
  const isLive = following && (step.status === "running" || step.status === "leased");
  const deploys = step.kind === "deploy" || step.kind === "rollback" || step.kind === "rollout";
  const build = step.kind === "image" ? run.graph.steps.find((g) => g.id === step.id)?.image_build : undefined;
  const env = deploys ? environmentOf(run, step) : "";
  const reused = !!step.reused_from;
  // A verification taken over by another deploy runner keeps its window:
  // it has lasted since it started, not since the latest attempt.
  const vstart = step.kind === "verify" ? verifications.list?.find((v) => v.step === step.id)?.started_at ?? 0 : 0;
  const since = step.attempt > 1 && vstart > 0 && vstart < step.started_at ? vstart : step.started_at;
  const group = step.group ? run.graph.groups?.find((g) => g.id === step.group) : undefined;
  const outputs = Object.entries(step.outputs ?? {});
  const secrets = Object.entries(step.secrets ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const empty = !loaded ? "Loading…"
    : reused ? `Not run in this run: reused from #${step.reused_from_number}, where its log is.`
    : step.status === "pending" || step.status === "queued" || step.status === "leased" ? why?.text ?? "Waiting."
    : step.status === "skipped" ? step.error || "Skipped."
    : deploys && step.status === "waiting" ? why?.text ?? "Waiting."
    : step.kind === "ai" && step.status === "succeeded" ? "No output: the answer is above."
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
            {step.allowed_failure && <span className="badge-allowed" title="The step has on_failure: continue, so its failure does not fail the run and the steps that need it run as if it had succeeded">Allowed to fail</span>}
            {step.kind === "image" && <span className="meta step-kind"><ImageIcon />builds a container image</span>}
            {step.kind === "verify" && <span className="meta">verifies {step.environment || environmentOf(run, step) || "an environment"}</span>}
            {step.kind === "ai" && <span className="meta">asks a model</span>}
            {deploys && <span className="meta">{step.kind === "rollback" ? `rollback of ${step.deployment}` : step.kind === "rollout" ? `acts on the rollout of ${step.deployment}` : `deploy to ${env}`}</span>}
            {reused && <span className="badge-reused">Reused from #{step.reused_from_number}</span>}
            {step.started_at > 0 && <span className="meta" title={since < step.started_at ? `since the verification started; this attempt started ${clock(step.started_at)}` : undefined}>
              {elapsed(since, step.finished_at, now)}</span>}
            {step.status === "queued" && (step.not_before ?? 0) > now && (
              <span className="meta wait" title={`It is handed to a runner at ${clock(step.not_before!)}`}>
                retrying in {duration(step.not_before! - now)} (attempt {step.attempt} of {step.retry + 1})
                {step.failure_cause && <>, after {step.failure_cause === "exit" ? "a failed exit" : step.failure_cause}</>}
              </span>
            )}
            {step.status === "failed" && step.failure_cause && step.failure_cause !== "exit" && (
              <span className="meta" title="Why the last attempt failed">cause: {step.failure_cause}</span>
            )}
            {step.exit_code !== null && step.exit_code !== undefined && <span className="meta">exit code {step.exit_code}</span>}
            {step.runner && <span className="meta only-everything">on {step.runner}</span>}
            {(step as { workspace?: string }).workspace && <span className="meta only-everything" title="a fresh checkout shared with no other step">own workspace</span>}
            {(step as { waiting_for?: string }).waiting_for && <span className="meta wait">{(step as { waiting_for?: string }).waiting_for}</span>}
            {step.container && !step.container.startsWith("image-") && <span className="meta only-everything">in <strong>{step.container}</strong></span>}
            {step.image && <span className="meta mono only-everything">{step.image}</span>}
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
          {rerun && step.status !== "failed" && <RerunButton run={run} step={step} />}
        </div>
      </div>
      {why && <div className={"note why " + (why.kind === "person" ? "warn" : "")} role="status"><Icon name={why.kind === "person" ? "pause" : "clock"} size="s" /><span><b>Why it waits.</b> {why.text} <LearnLink to={why.kind === "environment" ? EXPLAIN.lock : why.kind === "person" ? EXPLAIN.clearance : EXPLAIN.waits} /></span></div>}
      {reused && <ReusedNote run={run} step={step} />}
      {deploys && <DeploymentPanel run={run} step={step} />}
      {step.kind === "verify" && <VerificationPanel run={run} step={step} list={verifications.list} reload={verifications.reload} />}
      {step.kind === "ai" && <AIStepResult run={run} step={step} />}
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
      {step.caches && step.caches.length > 0 && <CacheResults project={run.project} caches={step.caches} />}
      {step.policy_blocked && (
        <div className="callout callout-bad pol-step-callout">
          <span><strong>Blocked by policy.</strong> Nothing of this step reached a runner or the environment.</span>
          <button type="button" className="btn btn-sm" onClick={showPolicy}>See the decision</button>
        </div>
      )}
      {step.status === "failed" && following && !reused && step.cause && step.cause.kind !== "script_exit" && step.cause.kind !== "other" && (
        <div className="callout callout-bad"><StepCauseView cause={step.cause} /></div>
      )}
      {step.status === "failed" && following && step.kind !== "verify" && !(step.cause && step.cause.kind !== "script_exit" && step.cause.kind !== "other") && ((step.excerpt && !excerptInPanel) || (step.error && !/^exit code \d+$/.test(step.error))) && (
        <div className="callout callout-bad">
          {step.error && !/^exit code \d+$/.test(step.error) && <div>{step.error}</div>}
          {step.excerpt && !excerptInPanel && <Excerpt step={step} />}
        </div>
      )}
      {step.status === "failed" && following && !reused && <ExplainFailure run={run} step={step} />}
      {step.kind === "image" && <ImageResult run={run} step={step} showImages={showImages} />}
      {!(deploys && step.attempt === 0) && step.kind !== "verify" && <LogView lines={lines} live={isLive} empty={empty} />}
      <div className="stepinfo only-everything">
        {step.run && (
          <details>
            <summary>Script</summary>
            <pre className="code">{step.run}</pre>
          </details>
        )}
        {build && (
          <section className="imgbuild" aria-label="What it builds">
            <h3 className="h-sm">What it builds</h3>
            <dl className="kv"><ImageBuildFacts b={build} /></dl>
          </section>
        )}
        <dl className="kv">
          {group && <TemplateFacts group={group} libraries={run.graph.libraries} />}
          {step.needs && step.needs.length > 0 && <><dt>Needs</dt><dd>{step.needs.join(", ")}</dd></>}
          {step.when && <><dt>When</dt><dd className="mono">{JSON.stringify(step.when)}</dd></>}
          {step.on_failure === "continue" && <><dt>On failure</dt><dd className="mono">continue</dd></>}
          {step.retry_wait && <><dt>Retry</dt><dd className="mono">{step.retry} times, wait {step.retry_wait}{step.retry_backoff && step.retry_backoff > 1 ? `, backoff ${step.retry_backoff}` : ""}{step.retry_on?.length ? `, on ${step.retry_on.join(", ")}` : ""}</dd></>}
          {secrets.length > 0 && <><dt>Secrets</dt><dd>{secrets.map(([name, r]) => <div key={name}><Mono>{name}</Mono> ← <Mono>{`${r.store}:${r.key}${r.field ? `#${r.field}` : ""}`}</Mono></div>)}</dd></>}
          <dt>Retry</dt><dd>{step.retry ? `up to ${step.retry} more time${step.retry > 1 ? "s" : ""}` : "no"}</dd>
          <dt>Timeout</dt><dd>{step.timeout}</dd>
          {!deploys && step.kind !== "verify" && step.queued_at > 0 && step.started_at > 0 && <><dt>Waited</dt><dd>{duration(step.started_at - step.queued_at)} for a runner</dd></>}
          {outputs.length > 0 && <><dt>Outputs</dt><dd>{outputs.map(([k, v]) => <div key={k}><Mono>{k}</Mono> = <Mono>{v}</Mono></div>)}</dd></>}
        </dl>
        {!deploys && step.kind !== "verify" && <InputsView inputs={step.inputs?.find((a) => a.attempt === attempt)} step={step} />}
      </div>
    </section>
  );
}

// ImageResult sums up what an image step built, from its outputs, with
// the way to the Images tab, where the rest is.
function ImageResult({ run, step, showImages }: { run: RunDetail; step: StepView; showImages: () => void }) {
  const [signed, setSigned] = useState<boolean | null>(null);
  const o = step.outputs ?? {};
  const digest = o.digest;
  useEffect(() => {
    if (!digest) return;
    let alive = true;
    api.runImages(run.id, true).then((d) => alive && setSigned(d.images.find((i) => i.step === step.id)?.signed ?? null)).catch(() => {});
    return () => { alive = false; };
  }, [run.id, step.id, digest]);
  if (!digest) return null;
  const pushed = o.pushed === "true";
  return (
    <div className="callout callout-info imgresult">
      <div className="imgresult-main">
        <strong>Built</strong>
        <CopyValue value={digest} label={`digest of ${step.id}`} keep={DIGEST_KEEP} />
        {pushed && o.image
          ? <span>pushed as <span className="mono imgref" title={o.image}>{shortImageRef(o.image)}</span></span>
          : <span>not pushed</span>}
        {signed !== null && <span className={"artst" + (signed ? " artst-kept" : "")}>{signed ? "Signed" : "Not signed"}</span>}
      </div>
      <button type="button" className="btn btn-sm" onClick={showImages}>See the image</button>
    </div>
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
            <span className="meta">{terms().lower("clearance")}</span>
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
              ? <>This {terms().lower("clearance")} guards the deployment of {guarded.service} to {guarded.environment}: it is rejected by itself if the rollout is aborted.</>
              : <>This {terms().lower("clearance")} guards the deployment made by {step.deployment}: it is refused by itself ({terms().label("no_go")}) if the rollout is aborted.</>}
          </p>
        )}
        {(progressText(step as ApprovalProgress) || whoMayDecide(step as ApprovalProgress)) && (
          <p className="sub approval-progress">
            {progressText(step as ApprovalProgress)}
            {whoMayDecide(step as ApprovalProgress) && <> Only {whoMayDecide(step as ApprovalProgress)} may decide.</>}
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
              <button type="button" className="btn btn-primary" disabled={busy} onClick={() => decide(true)} title={`${terms().label("go")}: ${terms().lower("clearance")} is given and the run goes on`}>{terms().label("go")}</button>
              <button type="button" className="btn btn-danger-outline" disabled={busy} onClick={() => decide(false)} title={`${terms().label("no_go")}: ${terms().lower("clearance")} is refused and the run stops here`}>{terms().label("no_go")}</button>
            </div>
          </form>
        )}
        {step.status === "succeeded" && (
          <div className="callout callout-ok">
            <strong>{terms().label("go")} from {step.decided_by}</strong>, {clock(step.decided_at)}
            {step.reused_from && <> in <Link href={`/runs/${step.reused_from}?step=${encodeURIComponent(step.id)}`}>run #{step.reused_from_number}</Link>. This re-run reused that decision; nobody was asked again.</>}
            {said}
          </div>
        )}
        {step.status === "failed" && (
          <div className="callout callout-bad">
            {step.decided_by
              ? <><strong>{terms().label("no_go")} from {step.decided_by}</strong>, {clock(step.decided_at)}{said}</>
              : <><strong>{step.error}</strong>, so it was refused ({terms().label("no_go")}) at {clock(step.decided_at)}.</>}
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
  "step.waiting": (d) => `${d.step}: waiting for ${terms().lower("clearance")} until ${clock(d.deadline as number)}`,
  "step.approved": (d) => `${d.step}: ${terms().label("go")} from ${d.actor}${d.comment ? `: ${d.comment}` : ""}`,
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
    if (c.result === "failed" || c.result === "not_applied") {
      return `${d.step}: the command did NOT take effect (${String(c.result).replace(/_/g, " ")})${c.detail ? `: ${c.detail}` : ""}`;
    }
    return `${d.step}: command ${String(c.result ?? "done").replace(/_/g, " ")}${c.detail ? `: ${c.detail}` : ""}`;
  },
  "deploy.command_withdrawn": (d) => `${d.step}: command withdrawn by ${commandOf(d).actor ?? "someone"}`,
  "step.reused": (d) => `${d.step}: reused from run #${(d.reused as { number?: number })?.number ?? "?"} (${d.outcome})`,
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
        <p className="sub">{step.reused_from ? `Recorded with the step in run #${step.reused_from_number}.` : step.attempt === 0 || step.status === "pending" || step.status === "queued" ? "Recorded when a runner takes the step." : "Not recorded for this attempt (it ran on an older server)."}</p>
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
  const editHref = `/projects/${encodeURIComponent(run.project)}/pipeline/edit?ref=${encodeURIComponent(branch)}`;
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
            It is named <Mono>{file.hash}</Mono> in the {terms().lower("logbook")}. The file on {branch || run.tag} at <Mono>{shortSha(run.sha)}</Mono> may say something else.</p>
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
