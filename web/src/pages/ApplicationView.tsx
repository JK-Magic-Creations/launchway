import { RollbackControl } from "../components/Rollback";
import { HistoryNote } from "../components/RollbackRun";
import { useCallback, useMemo, useState } from "react";
import { api, type DeploymentControls, type DeploymentView, type RolloutAction, type RunDetail, type UnitView } from "../api";
import { EntityLink } from "../components/EntityLink";
import { OpenInArgo } from "../components/ArgoLink";
import { DeploymentControls as Controls } from "../components/DeploymentControls";
import { DeployStatePill, FreshnessLine, freshness } from "../components/Deployment";
import { Runway } from "../components/Runway";
import { ErrorBox, Mono } from "../components/ui";
import { ago, clock, shortSha } from "../format";
import { diagnose, type Diagnosis, type Recommend } from "../guided/diagnose";
import { plainHealth, plainSync, type AppRow } from "../guided/derive";
import { useInstallation } from "../guided/facts";
import { useLoad } from "../guided/useLoad";
import { Button, EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { Icon } from "../kit/Icon";
import { Chip, type Tone } from "../kit/Status";
import { useCrumbLabel } from "../nav/crumbs";
import { useNow } from "../live";
import { terms } from "../terms";
import { prose } from "../prose";
import { DivFaintSmall, DivLoading, DivPage, SpanFaint, SpanFaintSmall, SpanMono } from "../kit/bits";
const P = await prose("pages.ApplicationView");

const TONE: Record<string, Tone | undefined> = { ok: "ok", warn: "warn", bad: "bad", run: "run", idle: undefined };

/** The application page: one Argo CD application as Launchway's deploy runner last saw it. It opens with what is
 * wrong or waiting in one sentence, the evidence, one recommended action and the way into the engine's console;
 * under it the rollout, the current and previous deployment, the parts and the history. */
export function ApplicationViewPage({ name }: { name: string }) {
  const inst = useInstallation();
  const now = useNow(1000);
  useCrumbLabel(name);
  const app = inst.applications.find((a) => a.name === name);
  const unit = useMemo(() => {
    for (const e of inst.environments) for (const u of e.services) if (u.last?.application === name) return u;
    return undefined;
  }, [inst.environments, name]);
  if (!inst.loaded) return <DivPage><DivLoading>Loading…</DivLoading></DivPage>;
  if (!app || !unit) {
    return (
      <DivPage>
        <PageHead eyebrow="Deployments · Applications" title={name} />
        <Sheet><EmptyState title={P.t0} action={<LinkButton href="/deployments/applications" icon="arrow">See the applications</LinkButton>}>
          {terms().say(P.t1)}
          {" "}<OpenInArgo application={name} sm />
        </EmptyState></Sheet>
      </DivPage>
    );
  }
  return <Loaded app={app} unit={unit} now={now} />;
}

function Loaded({ app, unit, now }: { app: AppRow; unit: UnitView; now: number }) {
  const d = unit.last;
  const live = unit.live;
  const runId = d?.run_id ?? "";
  const runFn = useCallback((bg: boolean) => (runId ? api.run(runId, bg) : Promise.reject(new Error("no deployment"))), [runId]);
  const run = useLoad<RunDetail>(runFn, d && !d.verdict ? 5000 : 0);
  const histFn = useCallback((bg: boolean) => api.deployments({ project: unit.project, service: unit.service, limit: 20 }, bg), [unit.project, unit.service]);
  const hist = useLoad<DeploymentView[]>(histFn, 10000);
  const step = run.data?.step_list.find((s) => s.id === (d?.verdict === "rolled_back" ? d.rollback_step : d?.step) || s.id === d?.step);
  const dg = diagnose(d, live, d?.verdict === "failed" ? run.data?.step_list.find((s) => s.id === d.step) ?? step : step, now);
  const sync = plainSync(app.sync);
  const health = plainHealth(app.health);
  const snap = d && (!d.verdict || !live?.snapshot) ? d.snapshot : live?.snapshot ?? d?.snapshot;
  const muted = d ? freshness(d, now).state !== "fresh" && !d.verdict : false;
  const resources = snap?.app?.resources ?? [];
  const current = hist.data?.find((x) => x.id === unit.current?.deployment);
  const prev = hist.data?.find((x) => x.id !== current?.id && x.version !== current?.version && x.opened_at < (current?.opened_at ?? Infinity));
  return (
    <DivPage>
      <PageHead eyebrow={<>Deployments · Applications · <EntityLink kind="environment" name={app.environment} /></>} title={app.name}
        sentence={<>{dg.headline} <SpanFaint>Service {unit.service} of <EntityLink kind="project" name={unit.project} />.</SpanFaint></>}
        actions={<OpenInArgo application={app.name} kind="primary" />} />

      <Diagnose dg={dg} d={d} run={run.data} app={app.name} />

      {unit.rollback && (
        <Sheet title={terms().say("{Rollback:do}")} actions={<SpanFaintSmall>after a deployment succeeded</SpanFaintSmall>}>
          <div className="sh-b"><RollbackControl action={unit.rollback} unit={{ project: unit.project, service: unit.service, environment: app.environment }} /></div>
        </Sheet>
      )}

      <Sheet>
        <div className="engine-strip">
          <Icon name="deploy" />
          <div className="grow">
            <b>Argo CD application <Mono>{app.name}</Mono></b>
            <DivFaintSmall>{P.t2}</DivFaintSmall>
          </div>
          <Chip tone={TONE[sync.tone]} icon={sync.tone === "ok" ? "check" : undefined}>{sync.label}</Chip>
          <Chip tone={TONE[health.tone]}>{health.label}</Chip>
          {app.revision && <Chip icon="commit">at {shortSha(app.revision)}</Chip>}
          <Chip>{app.automated ? "syncs by itself" : "syncs when asked"}</Chip>
          <OpenInArgo application={app.name} sm />
        </div>
        <div className="sh-b faint small" style={{ paddingTop: 0 }}>
          {d && <FreshnessLine d={d} now={now} />}
          {!d && app.observedAt ? <>Observed {ago(app.observedAt, now)}{app.observedBy ? ` by ${app.observedBy}` : ""}.</> : null}
        </div>
      </Sheet>

      {snap && (snap.rollout || (snap.analysis?.length ?? 0) > 0) && (
        <Sheet title="Rollout" actions={<Chip>{d?.verdict ? "as it was when it ended" : "live"}</Chip>}>
          <div className="runway-block"><Runway snapshot={snap} muted={muted} now={now} /></div>
        </Sheet>
      )}
      {d && run.data && !d.verdict && <Sheet title="Actions on the rollout"><div className="sh-b"><Controls run={run.data} d={d} now={now} /></div></Sheet>}

      <div className="grid g2">
        <Sheet title="Now"><DeployFacts d={current ?? (d?.verdict === "deployed" ? d : undefined)} unit={unit} now={now} empty={terms().say(P.t3)} /></Sheet>
        <Sheet title="Before"><DeployFacts d={prev} unit={unit} now={now} empty={d?.previous_version ? `The previous version was ${d.previous_version}; its deployment is not in the history.` : "What ran before is not known."} /></Sheet>
      </div>

      <Sheet title="Parts" actions={<SpanFaintSmall>{resources.length} {resources.length === 1 ? "resource" : "resources"} Argo CD manages</SpanFaintSmall>}>
        {resources.length === 0 ? <p className="sh-b faint">{terms().say(P.t4)}</p> : (
          <ul className="rowlist">
            {resources.map((r) => (
              <li key={`${r.kind}/${r.namespace}/${r.name}`} className={!r.found || (r.health && r.health !== "Healthy") ? "warn" : ""}>
                <div className="main"><div className="nm"><SpanMono>{r.kind}</SpanMono> {r.name}</div><div className="sb">{r.namespace ?? ""}{r.message ? ` · ${r.message}` : ""}</div></div>
                <div className="cell">{r.found ? [r.sync, r.health].filter(Boolean).join(", ") || "no state reported" : "not found"}</div>
                <OpenInArgo application={app.name} resource={{ kind: r.kind, name: r.name, namespace: r.namespace, group: r.group }} sm />
              </li>
            ))}
          </ul>
        )}
      </Sheet>

      <Sheet title="History" actions={<SpanFaintSmall>deployments of {unit.service} to {app.environment}, newest first</SpanFaintSmall>}>
        <ErrorBox error={hist.error} />
        {hist.data && hist.data.length === 0 && <p className="sh-b faint">No deployments yet.</p>}
        {hist.data && hist.data.length > 0 && (
          <ul className="rowlist">
            {hist.data.map((x) => (
              <li key={x.id}>
                <div className="main"><div className="nm mono">{x.version}</div>
                  <div className="sb"><EntityLink kind="run" name={terms().say(`{run} #${x.run_number}`)} id={x.run_id} />{x.commit ? ` · ${shortSha(x.commit)}` : ""} · {x.environment}{x.detail ? ` · ${x.detail}` : ""}</div><HistoryNote d={x} /></div>
                <DeployStatePill d={x} now={now} />
                <SpanFaintSmall title={clock(x.opened_at)}>{ago(x.opened_at, now)}</SpanFaintSmall>
              </li>
            ))}
          </ul>
        )}
      </Sheet>
    </DivPage>
  );
}

function DeployFacts({ d, unit, now, empty }: { d?: DeploymentView; unit: UnitView; now: number; empty: string }) {
  if (!d) return <p className="sh-b faint">{empty}</p>;
  return (
    <dl className="kvlist">
      <dt>Version</dt><dd><Mono>{d.version}</Mono></dd>
      <dt>{terms().say("{Deploy:did} by")}</dt><dd><EntityLink kind="run" name={terms().say(`{run} #${d.run_number}`)} id={d.run_id} /> of <EntityLink kind="project" name={unit.project} /></dd>
      {d.commit && <><dt>Commit</dt><dd><Mono>{shortSha(d.commit)}</Mono>{d.branch ? <> on <Mono>{d.branch}</Mono></> : null}</dd></>}
      <dt>When</dt><dd title={clock(d.deployed_at ?? d.opened_at)}>{ago(d.deployed_at ?? d.opened_at, now)}</dd>
      <dt>Result</dt><dd><DeployStatePill d={d} now={now} /></dd>
    </dl>
  );
}

const WORD: Record<string, string> = { bad: "Something is wrong", wait: "Waiting", run: "In motion", ok: "All is well", quiet: "Nothing to do" };

/** The diagnosis: the sentence, the evidence in order and one recommended action. */
export function Diagnose({ dg, d, run, app }: { dg: Diagnosis; d?: DeploymentView; run: RunDetail | null; app: string }) {
  return (
    <section className={`diag ${dg.tone}`} aria-label="Diagnosis" data-tone={dg.tone}>
      <div className="diag-h">
        <div className={`ico-tile ${dg.tone === "bad" ? "bad" : dg.tone === "wait" ? "warn" : dg.tone === "ok" ? "ok" : dg.tone === "run" ? "run" : ""}`}><Icon name={dg.tone === "ok" ? "check" : dg.tone === "bad" ? "alert" : dg.tone === "wait" ? "pause" : "info"} /></div>
        <div><div className="eyebrow">{WORD[dg.tone]}</div><h2>{dg.headline}</h2>{dg.detail && <p>{dg.detail}</p>}</div>
      </div>
      <div className="diag-b">
        <div>
          <div className="eyebrow">What Launchway saw</div>
          {dg.evidence.length === 0
            ? <p className="faint">{dg.known ? "Nothing to report." : terms().say(P.t5)}</p>
            : <ol className="evlist">{dg.evidence.map((e, i) => <li key={i}><div>{e.text}{e.at ? <div className="at">{clock(e.at)}</div> : null}</div></li>)}</ol>}
        </div>
        <div>
          <div className="eyebrow">What to do</div>
          <Recommendation rec={dg.recommend} d={d} run={run} app={app} />
        </div>
      </div>
    </section>
  );
}

function Recommendation({ rec, d, run, app }: { rec: Recommend | null; d?: DeploymentView; run: RunDetail | null; app: string }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [sent, setSent] = useState(false);
  const ctlFn = useCallback(() => (rec && rec.action !== "open" && d && run ? api.deploymentControls(run.id, d.id) : Promise.resolve<DeploymentControls | null>(null)), [rec, d, run]);
  const controls = useLoad<DeploymentControls | null>(ctlFn);
  if (!rec) return <p className="faint">{P.t6}</p>;
  if (rec.action === "open") {
    return <div className="stack" style={{ gap: 10 }}><p style={{ margin: 0 }}>{rec.why}</p><div><OpenInArgo application={app} kind="primary" /></div></div>;
  }
  const action: RolloutAction = rec.action;
  const c = controls.data?.controls.find((x) => x.action === action);
  const may = c?.available === true;
  const give = async () => {
    if (!d || !run) return;
    setBusy(true); setError(null);
    try { await api.command(run.id, d.id, action, "from the application page"); setSent(true); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  return (
    <div className="stack" style={{ gap: 10 }}>
      <p style={{ margin: 0 }}><b>{rec.label}.</b> {rec.why}</p>
      {sent ? <div className="note" role="status"><Icon name="check" />{terms().say(P.t7)}</div> : (
        <div><Button kind="pri" disabled={busy || !may} onClick={give} title={may ? undefined : c?.reason ?? "Not available now"}>{rec.label}</Button></div>
      )}
      {!sent && c && !may && c.reason && <p className="faint small" style={{ margin: 0 }}>{c.reason.replace(/^./, (x) => x.toUpperCase())}{/[.!?)]$/.test(c.reason) ? "" : "."}</p>}
      <ErrorBox error={error} />
    </div>
  );
}

