import { rollbackApi, type ReviewItem } from "../apiRollback";
import { RollbackControl } from "../components/Rollback";
import { HistoryNote, NeedsReview } from "../components/RollbackRun";
import { useCallback } from "react";
import { EnvironmentChanges } from "../components/ToggleChanges";
import { api, type DeploymentView } from "../api";
import { useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { DeployStatePill } from "../components/Deployment";
import { ErrorBox, Mono } from "../components/ui";
import { ago, clock } from "../format";
import { diagnose } from "../guided/diagnose";
import { useInstallation } from "../guided/facts";
import { useLoad } from "../guided/useLoad";
import { Status, type State } from "../kit/Status";
import { EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { useCrumbLabel } from "../nav/crumbs";
import { useNow } from "../live";
import { terms } from "../terms";

const STATE: Record<string, State> = { bad: "bad", wait: "wait", run: "run", ok: "ok", quiet: "skip" };

/** One environment: where it deploys, what runs in it, whether anything needs a look and what happened lately. */
export function EnvironmentViewPage({ name }: { name: string }) {
  const inst = useInstallation();
  const me = useMe();
  const now = useNow(5000);
  useCrumbLabel(name);
  const histFn = useCallback((bg: boolean) => api.deployments({ environment: name, limit: 20 }, bg), [name]);
  const hist = useLoad<DeploymentView[]>(histFn, 10000);
  const reviewFn = useCallback((bg: boolean) => rollbackApi.reviews(bg), []);
  const reviews = useLoad<ReviewItem[]>(reviewFn, 15000);
  const env = inst.environments.find((e) => e.name === name);
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  if (!env) return <div className="page"><PageHead eyebrow={terms().say("Deployments · {Environments}")} title={name} /><Sheet><EmptyState title={terms().say(`There is no {environment} called ${name}`)} action={<LinkButton href="/deployments/environments" icon="arrow">{terms().say("See the {environments}")}</LinkButton>}>It may have been removed, or you may not be allowed to see it.</EmptyState></Sheet></div>;
  const watched = !!env.observer && (!env.observer_expires || env.observer_expires > now);
  const rows = env.services.map((unit) => ({ unit, dg: diagnose(unit.last, unit.live, undefined, now) }));
  const trouble = rows.filter((r) => r.dg.tone === "bad" || r.dg.tone === "wait").length;
  return (
    <div className="page">
      <PageHead eyebrow={terms().say("Deployments · {Environments}")} title={env.name}
        sentence={rows.length === 0 ? terms().say("Nothing has been {deploy:did} here yet.") : trouble > 0 ? <><b>{trouble} of {rows.length} {rows.length === 1 ? "service needs" : "services need"} a look.</b></> : terms().say("Everything {deploy:did} here is running as expected.")}
        actions={<>{me?.admin && <LinkButton href={`/config/environments/${encodeURIComponent(env.name)}`} icon="edit">Change it</LinkButton>}<LinkButton href="/deployments/environments" icon="list">{terms().say("The matrix of all {environments}")}</LinkButton></>} />
      <NeedsReview items={reviews.data} environment={env.name} onChanged={reviews.reload} />
      <Sheet title="What runs here">
        {rows.length === 0 ? <EmptyState title={terms().say("Nothing {deploy:did} yet")}>{terms().say("A {run} {deploy:does} here when it reaches a deploy step for this {environment}.")}</EmptyState> : (
          <ul className="rowlist">
            {rows.map(({ unit: u, dg }) => (
              <li key={u.project + "/" + u.service} className={dg.tone === "bad" ? "bad" : dg.tone === "wait" ? "warn" : ""}>
                <div className="main">
                  <div className="nm">{u.last?.application ? <EntityLink kind="application" name={u.last.application}>{u.service}</EntityLink> : u.service} <span className="faint">of</span> <EntityLink kind="project" name={u.project} /></div>
                  <div className="sb">{dg.headline}</div>
                </div>
                <div className="cell"><Status state={STATE[dg.tone]} /><span className="mono">{u.current?.version ?? "not yet"}</span></div>
                <div className="cell faint">{u.current ? <><EntityLink kind="run" name={`#${u.current.run_number}`} id={u.current.run} /> {ago(u.current.since, now)}</> : ""}</div>
                {u.rollback && <div className="cell"><RollbackControl action={u.rollback} unit={{ project: u.project, service: u.service, environment: env.name }} compact /></div>}
                {(u.lock || u.waiting.length > 0) && <div className="cell small">{u.lock && <>lock held by <EntityLink kind="run" name={`#${u.lock.number}`} id={u.lock.run} /></>}{u.waiting.length > 0 && <>, {u.waiting.length} waiting</>}</div>}
              </li>
            ))}
          </ul>
        )}
      </Sheet>
      <Sheet title={terms().say("About this {environment}")}>
        <dl className="kvlist">
          <dt>Where</dt><dd>{terms().say("{Deploy:does} to")} <Mono>{env.target}</Mono>{env.protected ? "; protected: the person who started a run may not promote or retry its deployments here" : ""}.</dd>
          <dt>Watched by</dt><dd className="row"><Status state={watched ? "ok" : "warn"} />{watched ? <>{terms().say("The {deploy:do} {runner}")} <EntityLink kind="runner" name={env.observer ?? ""} /> watches it.</> : terms().say("No {deploy:do} {runner} watches it, so a deployment here cannot finish.")}</dd>
          <dt>Used by</dt><dd>{env.projects.length === 0 ? terms().say("No project {deploy:does} here yet.") : env.projects.map((p, i) => <span key={p}>{i > 0 && ", "}<EntityLink kind="project" name={p} /></span>)}</dd>
          <dt>Queue</dt><dd>{env.queue === "all" ? terms().say("Every {run} waiting for the lock is kept.") : terms().say("A {run} waiting for the lock is skipped when a newer run of the same service starts waiting.")}</dd>
        </dl>
      </Sheet>
      <Sheet title={terms().say("{Deploys}")} actions={<span className="faint small">newest first</span>}>
        <ErrorBox error={hist.error} />
        {hist.data && hist.data.length === 0 && <p className="sh-b faint">No deployments to {env.name} yet.</p>}
        {hist.data && hist.data.length > 0 && (
          <ul className="rowlist">
            {hist.data.map((d) => (
              <li key={d.id}>
                <div className="main"><div className="nm">{d.application ? <EntityLink kind="application" name={d.application}>{d.service}</EntityLink> : d.service} <span className="mono">{d.version}</span></div>
                  <div className="sb"><EntityLink kind="run" name={terms().say(`{run} #${d.run_number}`)} id={d.run_id} /> of <EntityLink kind="project" name={d.project} />{d.detail ? ` · ${d.detail}` : ""}</div><HistoryNote d={d} /></div>
                <DeployStatePill d={d} now={now} />
                <span className="faint small" title={clock(d.opened_at)}>{ago(d.opened_at, now)}</span>
              </li>
            ))}
          </ul>
        )}
      </Sheet>
      <EnvironmentChanges environment={env.name} projects={env.projects} deployments={hist.data ?? []} />
    </div>
  );
}
