import { useCallback, useEffect, useState } from "react";
import { api, verifyApi, type DeploymentView, type EnvironmentView, type LiveView, type UnitView, type VerificationSummary } from "../api";
import { ago, clock } from "../format";
import { useNow } from "../live";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { WizardButton, WizardEmpty } from "../components/WizardButton";
import { AppChips, DeployStatePill, FreshnessLine, freshness } from "../components/Deployment";
import { VerifyPill } from "../components/Verification";
import { PageHead } from "../kit/layout";
import { terms } from "../terms";

// EnvironmentsPage shows, for every service of every project, what each
// environment runs, what is being deployed there now, and who holds or waits
// for its lock. There is no live stream for this, so it asks every 5 seconds.
export function EnvironmentsPage() {
  const [envs, setEnvs] = useState<EnvironmentView[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [verifs, setVerifs] = useState<VerificationSummary[]>([]);
  const [tick, setTick] = useState(0);
  const now = useNow(1000);

  const load = useCallback((background: boolean) => {
    api.environments(background).then((e) => { setEnvs(e); setError(null); }).catch(setError);
    // A server that does not know verifications answers with none.
    verifyApi.verifications({ limit: 200 }, background).then(setVerifs).catch(() => setVerifs([]));
  }, []);
  useEffect(() => {
    load(false);
    const t = window.setInterval(() => { load(true); setTick((n) => n + 1); }, 5000);
    return () => window.clearInterval(t);
  }, [load]);

  const rows = envs ? unitRows(envs, verifs) : [];
  const unobserved = (envs ?? []).filter((e) => !observing(e, now));

  return (
    <div className="page page-wide">
      <PageHead eyebrow={terms().say("{Deploys}")} title={terms().say("{Environments}")}
        sentence={terms().say("What each {environment} runs, by service, and what is being {deploy:did} there now. Checked every 5 seconds.")}
        actions={envs && envs.length > 0 ? <WizardButton wizard="add-environment" label={terms().say("Add an {environment}")} /> : undefined} />
      <ErrorBox error={error} />
      {envs === null && !error && <div className="loading">Loading…</div>}
      {envs && envs.length === 0 && <NoEnvironments />}
      {envs && envs.length > 0 && (
        <>
          {unobserved.length > 0 && (
            <div className="callout callout-draft">
              {unobserved.map((e) => (
                <div key={e.name}><strong>{e.name}</strong>{terms().say(": no {deploy:do} {runner} observes target")} <Mono>{e.target}</Mono>, so deployments there cannot finish and nothing shown for it is current.</div>
              ))}
            </div>
          )}
          {rows.length === 0 ? (
            <Empty title={terms().say("Nothing has been {deploy:did} yet")}>
              {terms().say("{Environments} are set up:")} {envs.map((e) => e.name).join(", ")}{terms().say(". A {run} {deploy:does} to one when it reaches a step with")} <Mono>kind: deploy</Mono>.
              <Example />
            </Empty>
          ) : (
            <div className="envgrid-wrap">
              <table className="envgrid">
                <thead>
                  <tr>
                    <th className="envgrid-corner">Service</th>
                    {envs.map((e) => (
                      <th key={e.name} scope="col">
                        <div className="envgrid-env">{e.name}{e.protected && <> <ProtectedPill env={e.name} /></>}</div>
                        <div className="envgrid-target">target <Mono>{e.target}</Mono></div>
                        <ObserverLine env={e} now={now} />
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r) => (
                    <tr key={r.project + "/" + r.service}>
                      <th scope="row" className="envgrid-svc">
                        <span className="envgrid-svc-name">{r.service}</span>
                        {r.project !== r.service && <span className="faint"> of {r.project}</span>}
                        {r.project === r.service && <span className="faint envgrid-svc-proj">project {r.project}</span>}
                      </th>
                      {envs.map((e) => {
                        const u = e.services.find((x) => x.project === r.project && x.service === r.service);
                        const vs = verifs.filter((v) => v.environment === e.name && v.project === r.project && v.service === r.service);
                        return (
                          <td key={e.name} data-env={e.name}>
                            <span className="envgrid-cell-env">{e.name}{e.protected && <> <ProtectedPill env={e.name} /></>} <span className="faint">· {e.target}</span></span>
                            {u ? <UnitCell env={e} unit={u} now={now} tick={tick} />
                              : allows(e, r.project) ? <span className="faint">{terms().say("Nothing {deploy:did} here yet.")}</span>
                              : vs.length === 0 ? <span className="faint">{r.project} {terms().say("may not {deploy:do} here.")}</span> : null}
                            {vs.length > 0 && <Verifications list={vs} now={now} />}
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function ProtectedPill({ env }: { env: string }) {
  return (
    <span className="dpill dpill-lock env-protected"
      title={`${env} is protected: the person who started a run may not promote or retry its deployments here; someone else with the operator role must. Anyone with the operator role may abort.`}>
      protected
    </span>
  );
}

/** Idle services are observed about every 30 seconds; older than this, what is shown is out of date. */
const LIVE_STALE_MS = 120_000;

// LiveLine shows what a deploy runner last saw of a service nothing is
// deploying, and whether it differs from the last deployment.
function LiveLine({ live, now }: { live: LiveView; now: number }) {
  const old = now - live.observed_at > LIVE_STALE_MS;
  return (
    <div className="unit-live">
      {live.snapshot?.app && <div className="unit-line"><AppChips app={live.snapshot.app} muted={old} /></div>}
      {live.snapshot?.error && <div className="unit-line small dep-msg-bad">{terms().say("The {runner} cannot see the application:")} {live.snapshot.error}</div>}
      <div className={"unit-line small " + (old ? "dfresh-warn" : "faint")} data-case={old ? "live-stale" : "live"}>
        observed {ago(live.observed_at, now)}{live.observed_by ? ` by ${live.observed_by}` : ""}{old ? ": what is shown may be out of date" : ""}
      </div>
      {live.drift && (
        <div className={`callout unit-drift ${live.drift === "missing" ? "callout-bad" : "callout-draft"}${old ? " stale" : ""}`} role="status" aria-label="Drift" data-drift={live.drift}>
          {live.drift === "out_of_sync" ? <>OutOfSync: Argo CD reports that what runs differs from Git{live.drift_detail ? `: ${live.drift_detail}` : ""}.</>
            : live.drift === "differs" ? <>Live differs from the last deployment{live.drift_detail ? `: ${live.drift_detail}` : ""}.</>
            : live.drift === "missing" ? <>The application is gone{live.drift_detail ? `: ${live.drift_detail}` : ""}.</>
            : <>{live.drift}{live.drift_detail ? `: ${live.drift_detail}` : ""}</>}
        </div>
      )}
    </div>
  );
}

function observing(e: EnvironmentView, now: number): boolean {
  return !!e.observer && (!e.observer_expires || e.observer_expires > now);
}

function ObserverLine({ env, now }: { env: EnvironmentView; now: number }) {
  return observing(env, now)
    ? <div className="envgrid-observer"><span className="online online-on" />observed by {env.observer}</div>
    : <div className="envgrid-observer envgrid-observer-none"><span className="online" />{terms().say("no {deploy:do} {runner} observes target")} {env.target}</div>;
}

// allows reports whether a project may deploy to an environment, matching
// its patterns as the server does (* and ? only).
function allows(e: EnvironmentView, project: string): boolean {
  return e.projects.some((p) => new RegExp("^" + p.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*").replace(/\?/g, "[^/]") + "$").test(project));
}

function unitRows(envs: EnvironmentView[], verifs: VerificationSummary[]): { project: string; service: string }[] {
  const seen = new Map<string, { project: string; service: string }>();
  for (const e of envs) for (const u of e.services) seen.set(u.project + "/" + u.service, { project: u.project, service: u.service });
  // A service only verified, never deployed by Launchway, has a row too.
  for (const v of verifs) if (envs.some((e) => e.name === v.environment)) seen.set(v.project + "/" + v.service, { project: v.project, service: v.service });
  return [...seen.values()].sort((a, b) => a.project.localeCompare(b.project) || a.service.localeCompare(b.service));
}

function UnitCell({ env, unit, now, tick }: { env: EnvironmentView; unit: UnitView; now: number; tick: number }) {
  const cur = unit.current;
  const last = unit.last;
  // The newest deployment, when it is not simply what runs now.
  const showLast = last && (!cur || last.id !== cur.deployment || !last.verdict);
  const f = last ? freshness(last, now) : null;
  // Nothing deploying it: what a deploy runner sees of it while idle.
  const idle = !last || !!last.verdict;
  const live = idle ? unit.live : undefined;
  return (
    <div className="unit">
      {cur ? (
        <div className="unit-current">
          <span className="unit-version mono">{cur.version}</span>
          <span className="unit-since" title={clock(cur.since)}>since {ago(cur.since, now)}, by <Link href={`/runs/${cur.run}`}>{terms().say("{run} #")}{cur.run_number}</Link></span>
        </div>
      ) : (
        <div className="unit-current"><span className="faint">{terms().say("Nothing Launchway {deploy:did} is known to run here.")}</span></div>
      )}
      {last && (
        <div className="unit-last">
          {showLast && (
            <div className="unit-line">
              <Link href={`/runs/${last.run_id}?step=${encodeURIComponent(last.verdict === "rolled_back" || last.verdict === "rollback_failed" ? (last.rollback_step ?? last.step) : last.step)}`}>#{last.run_number}</Link>
              {!last.verdict && <span className="faint">deploying</span>}
              <Mono>{last.version}</Mono>
              <DeployStatePill d={last} now={now} />
            </div>
          )}
          {!live && (
            <div className="unit-line">
              <AppChips app={last.snapshot?.app} muted={f?.state !== "fresh"} />
            </div>
          )}
          {!last.verdict && <div className="unit-line small"><FreshnessLine d={last} now={now} compact /></div>}
          {!live && last.verdict && last.snapshot?.app && <div className="unit-line small faint">as last observed {last.observed_at ? ago(last.observed_at, now) : ""}</div>}
        </div>
      )}
      {live && <LiveLine live={live} now={now} />}
      {idle && !live && <div className="unit-line small faint">{terms().say("Not observed while idle (needs a {deploy:do} {runner} of this release)")}</div>}
      {(unit.lock || unit.waiting.length > 0) && (
        <div className="unit-lock">
          {unit.lock && (
            <div>Lock held by <Link href={`/runs/${unit.lock.run}?step=${encodeURIComponent(unit.lock.step ?? "")}`}>{terms().say("{run} #")}{unit.lock.number}</Link></div>
          )}
          {unit.waiting.length > 0 && (
            <div>
              {unit.waiting.length === 1 ? "1 run waits" : `${unit.waiting.length} runs wait`}:{" "}
              {unit.waiting.map((w, i) => (
                <span key={w.run + (w.step ?? "")}>{i > 0 && ", "}<Link href={`/runs/${w.run}?step=${encodeURIComponent(w.step ?? "")}`}>#{w.number}</Link></span>
              ))}
            </div>
          )}
        </div>
      )}
      <History env={env.name} project={unit.project} service={unit.service} tick={tick} now={now} />
    </div>
  );
}

// History lists the newest deployments of one service to one environment,
// loaded when opened.
function History({ env, project, service, tick, now }: { env: string; project: string; service: string; tick: number; now: number }) {
  const [open, setOpen] = useState(false);
  const [list, setList] = useState<DeploymentView[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (!open) return;
    api.deployments({ environment: env, project, service, limit: 10 }, true).then((l) => { setList(l); setError(null); }).catch(setError);
  }, [open, env, project, service, tick]);
  return (
    <details className="unit-history" onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary>History</summary>
      <ErrorBox error={error} />
      {open && list === null && !error && <div className="faint small">Loading…</div>}
      {list && list.length === 0 && <div className="faint small">No deployments.</div>}
      {list && list.length > 0 && (
        <ol className="unit-history-list">
          {list.map((d) => (
            <li key={d.id}>
              <Link href={`/runs/${d.run_id}?step=${encodeURIComponent(d.step)}`}>#{d.run_number}</Link>
              <Mono>{d.version}</Mono>
              <DeployStatePill d={d} now={now} />
              <span className="faint" title={clock(d.opened_at)}>{ago(d.opened_at, now)}</span>
            </li>
          ))}
        </ol>
      )}
    </details>
  );
}

// Verifications lists the newest verifications of one service in one
// environment: the latest in a line, the others when opened.
function Verifications({ list, now }: { list: VerificationSummary[]; now: number }) {
  const [latest, ...rest] = list;
  const row = (v: VerificationSummary) => (
    <>
      <Link href={`/runs/${v.run_id}?step=${encodeURIComponent(v.step)}`}>#{v.run_number}</Link>
      <VerifyPill v={{ outcome: v.outcome, override: v.overridden_by ? v.outcome : undefined, status: v.status || (v.outcome === "running" ? "running" : undefined) }} now={now} />
      {v.version && <Mono>{v.version}</Mono>}
      <span className="faint" title={clock(v.started_at)}>{ago(v.decided_at || v.started_at, now)}</span>
    </>
  );
  return (
    <div className="unit-verif" aria-label="Verifications">
      <div className="h-xs unit-verif-title">Verifications</div>
      <div className="unit-line">{row(latest)}</div>
      <div className="unit-verif-summary">
        {latest.outcome === "running" && !latest.summary ? `interval ${Math.min(latest.intervals + 1, latest.total)} of ${latest.total}` : latest.summary}
        {latest.overridden_by && <span className="faint"> (marked by {latest.overridden_by})</span>}
      </div>
      {rest.length > 0 && (
        <details className="unit-history">
          <summary>{rest.length} earlier</summary>
          <ol className="unit-history-list">
            {rest.slice(0, 10).map((v) => (
              <li key={v.run_id + v.step}>
                {row(v)}
                {v.summary && <span className="unit-verif-summary">{v.summary}</span>}
              </li>
            ))}
          </ol>
        </details>
      )}
    </div>
  );
}

function NoEnvironments() {
  return (
    <>
      <WizardEmpty title={terms().say("No {environments} yet")} wizard="add-environment" label={terms().say("Add an {environment}")}
        text={terms().say("An {environment} is a place runs {deploy:do} to, such as staging or prod: add one to see what runs where and to {deploy:do} with a protected, queued, recorded step.")} />
      <details className="card">
        <summary className="h-sm">{terms().say("How a pipeline {deploy:does} to an {environment}")}</summary>
        <p className="sub">{terms().say("A {run} {deploy:does} when it reaches a step with")} <Mono>kind: deploy</Mono>: the step commits the new version to your GitOps repository,
          asks Argo CD to sync, and waits until the application is healthy.</p>
        <Example />
      </details>
    </>
  );
}

function Example() {
  return (
    <div className="howto">
      <p className="sub">In a project's <Mono>.launchway/pipeline.yaml</Mono>:</p>
      <pre className="code">{terms().say(`  deploy-prod:
    kind: {deploy:do}
    needs: build
    {environment}: prod
    version: { output: build.tag }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - file: apps/shop.yaml
            image: team/shop
            tag: { output: build.tag }`)}</pre>
    </div>
  );
}
