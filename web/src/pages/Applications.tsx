import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { EntityLink } from "../components/EntityLink";
import { Empty, Link, Mono } from "../components/ui";
import { ago } from "../format";
import { needsLook, plainHealth, plainSync, type AppRow } from "../guided/derive";
import { useInstallation } from "../guided/facts";
import { useCrumbLabel } from "../nav/crumbs";
import { useNow } from "../live";


function Pill({ tone, children, title }: { tone: string; children: ReactNode; title?: string }) {
  return <span className={`pill pill-${tone}`} title={title}>{children}</span>;
}

export function SyncPill({ app }: { app: AppRow }) {
  const s = plainSync(app.sync);
  return <Pill tone={s.tone} title={`Argo CD says: ${app.sync}`}>{s.label}</Pill>;
}

export function HealthPill({ app }: { app: AppRow }) {
  const h = plainHealth(app.health);
  return <Pill tone={h.tone} title={`Argo CD says: ${app.health}`}>{h.label}</Pill>;
}

/** Every Argo CD application the deploy runners have seen, with whether it matches Git and is healthy. */
export function ApplicationsPage() {
  const inst = useInstallation();
  const [filter, setFilter] = useState<"all" | "look">("all");
  const [q, setQ] = useState("");
  const now = useNow(5000);
  const shown = useMemo(() => inst.applications.filter((a) => (filter === "all" || needsLook(a)) && (!q || a.name.toLowerCase().includes(q.toLowerCase()))), [inst.applications, filter, q]);
  const look = inst.applications.filter(needsLook).length;
  return (
    <div className="page page-wide">
      <header className="page-head">
        <div>
          <h1>Applications</h1>
          <p className="sub">Argo CD keeps your cluster the same as what is in Git. Each application is one thing it keeps in step. These are the ones a deploy runner has looked at.</p>
        </div>
      </header>
      {!inst.loaded && <div className="loading">Loading…</div>}
      {inst.loaded && inst.applications.length === 0 && <Empty title="No applications seen yet">An application appears here once a pipeline has deployed it and a deploy runner has observed it.</Empty>}
      {inst.applications.length > 0 && (
        <>
          <div className="toolbar">
            <div className="chips" role="group" aria-label="Filter">
              <button type="button" className={"chip" + (filter === "all" ? " chip-on" : "")} aria-pressed={filter === "all"} onClick={() => setFilter("all")}>All ({inst.applications.length})</button>
              <button type="button" className={"chip" + (filter === "look" ? " chip-on" : "")} aria-pressed={filter === "look"} onClick={() => setFilter("look")}>Needs a look ({look})</button>
            </div>
            <label className="select"><span className="visually-hidden">Search applications</span>
              <input type="search" placeholder="Search applications" value={q} onChange={(e) => setQ(e.target.value)} />
            </label>
          </div>
          <div className="card card-flush tablewrap">
            <table className="guided-table">
              <thead><tr><th>Application</th><th>Environment</th><th>Matches Git?</th><th>Healthy?</th><th>Version</th><th>Deployed by</th></tr></thead>
              <tbody>
                {shown.map((a) => (
                  <tr key={a.name}>
                    <td><EntityLink kind="application" name={a.name} /></td>
                    <td><EntityLink kind="environment" name={a.environment} /></td>
                    <td><SyncPill app={a} /></td>
                    <td><HealthPill app={a} /></td>
                    <td className="mono">{a.version}</td>
                    <td>{a.run ? <><EntityLink kind="project" name={a.project} /> <EntityLink kind="run" name={`#${a.run.number}`} id={a.run.id} /></> : "Not by a pipeline"}</td>
                  </tr>
                ))}
                {shown.length === 0 && <tr><td colSpan={6} className="faint">Nothing matches.</td></tr>}
              </tbody>
            </table>
          </div>
          <p className="faint">Seen {inst.applications[0]?.observedAt ? ago(Math.max(...inst.applications.map((a) => a.observedAt)), now) : "never"}. Applications Argo CD runs that no pipeline has deployed are not listed: no deploy runner has been asked to look at them.</p>
        </>
      )}
    </div>
  );
}

/** One application, as the deploy runner last saw it. */
export function ApplicationPage({ name }: { name: string }) {
  const inst = useInstallation();
  const now = useNow(5000);
  useCrumbLabel(name);
  const app = inst.applications.find((a) => a.name === name);
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  if (!app) return <div className="page"><Empty title={`No application called ${name} has been seen`}><Link href="/deployments/applications">See the applications</Link></Empty></div>;
  const resources = inst.environments.flatMap((e) => e.services).find((u) => u.last?.application === name);
  const parts = (resources?.live?.snapshot ?? resources?.last?.snapshot)?.app?.resources ?? [];
  const sync = plainSync(app.sync);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{app.name}</h1>
          <p className="sub">An Argo CD application, deployed to <EntityLink kind="environment" name={app.environment} />.</p>
        </div>
      </header>
      <div className="guided-two">
        <section className="card"><h2 className="h-sm">Matches Git?</h2><p><SyncPill app={app} /></p>
          <p className="faint">{app.sync === "OutOfSync" ? "Someone changed the cluster, or Git moved on and nobody has applied it." : sync.tone === "ok" ? "The cluster is what Git says it should be." : "A deploy runner has not reported yet."}</p></section>
        <section className="card"><h2 className="h-sm">Healthy?</h2><p><HealthPill app={app} /></p>
          <p className="faint">{app.healthMessage || (app.error ? app.error : "As Argo CD reports it.")}</p></section>
      </div>
      <section className="card glance" aria-label="At a glance">
        <dl className="glance-grid">
          <div><dt>What is this?</dt><dd>An Argo CD application: one thing Argo CD keeps in step with Git. It deploys to <Mono>{app.target}</Mono>.</dd></div>
          <div><dt>Where does it come from?</dt><dd>{app.revision ? <>Git revision <Mono>{app.revision.slice(0, 8)}</Mono>{app.automated ? ", and it syncs by itself" : ", synced when a pipeline asks"}.</> : "Its revision is not known yet."}</dd></div>
          <div><dt>Who uses it?</dt><dd>The pipeline <EntityLink kind="pipeline" name={app.project} />, service {app.service}, environment <EntityLink kind="environment" name={app.environment} />.</dd></div>
          <div><dt>What happened to it recently?</dt><dd>{app.run ? <>Run <EntityLink kind="run" name={`#${app.run.number}`} id={app.run.id} /> deployed {app.version}. </> : ""}Last looked at {app.observedAt ? ago(app.observedAt, now) : "never"}{app.observedBy ? <> by <EntityLink kind="runner" name={app.observedBy} /></> : ""}.</dd></div>
          <div><dt>What can I do with it?</dt><dd className="glance-actions"><EntityLink kind="project" name={app.project}>Open the project</EntityLink>{app.run && <EntityLink kind="run" name="" id={app.run.id}>See the deploy run</EntityLink>}</dd></div>
        </dl>
      </section>
      <section className="card" aria-labelledby="app-parts-h">
        <h2 id="app-parts-h" className="h-sm">Parts</h2>
        {parts.length === 0 ? <p className="faint">The deploy runner has not listed the parts of this application.</p> : (
          <ul className="guided-list">
            {parts.map((r) => (
              <li key={`${r.kind}/${r.namespace}/${r.name}`}>
                <span className="mono">{r.kind}</span><span>{r.name}</span>
                <span className="guided-grow faint">{r.message ?? ""}</span>
                <span className="faint">{r.found ? [r.sync, r.health].filter(Boolean).join(", ") : "not found"}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
