import { useEffect, useState } from "react";
import { api, type EnvironmentView } from "../api";
import { useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { ErrorBox, Link, Mono } from "../components/ui";
import { ago } from "../format";
import { useCrumbLabel } from "../nav/crumbs";
import { useNow } from "../live";

/** One environment: where it deploys, what runs in it now, and what happened last. */
export function EnvironmentPage({ name }: { name: string }) {
  const [env, setEnv] = useState<EnvironmentView | null | undefined>(undefined);
  const [error, setError] = useState<unknown>(null);
  const me = useMe();
  const now = useNow(5000);
  useCrumbLabel(name);
  useEffect(() => {
    let alive = true;
    const load = (bg: boolean) => api.environments(bg).then((l) => alive && (setEnv(l.find((e) => e.name === name) ?? null), setError(null))).catch((e) => alive && setError(e));
    load(false);
    const t = setInterval(() => load(true), 5000);
    return () => { alive = false; clearInterval(t); };
  }, [name]);

  if (error) return <div className="page"><ErrorBox error={error} /></div>;
  if (env === undefined) return <div className="page"><div className="loading">Loading…</div></div>;
  if (env === null) return <div className="page"><div className="empty"><div className="empty-title">There is no environment called {name}</div><div className="empty-body"><Link href="/deployments/environments">See the environments</Link></div></div></div>;
  const last = env.services.map((u) => u.last).filter((d) => !!d).sort((a, b) => (b!.opened_at - a!.opened_at))[0];
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{env.name}</h1>
          <p className="sub">An environment: a named place software goes.</p>
        </div>
        <div className="head-actions">
          {me?.admin && <Link href={`/config/environments/${encodeURIComponent(env.name)}`} className="btn">Change it</Link>}
          <Link href="/deployments/environments" className="btn">All environments</Link>
        </div>
      </header>
      <section className="card glance" aria-label="At a glance">
        <dl className="glance-grid">
          <div><dt>What is this?</dt><dd>{env.name}, deployed to <Mono>{env.target}</Mono>{env.protected ? "; the person who started a run may not promote it here" : ""}.</dd></div>
          <div><dt>Where does it come from?</dt><dd>It is part of the configuration{me?.admin ? <>, which you can change under <Link href={`/config/environments/${encodeURIComponent(env.name)}`}>Configuration</Link></> : ""}. {env.observer ? <>The deploy runner <EntityLink kind="runner" name={env.observer} /> watches it.</> : "No deploy runner watches it yet, so a deployment there cannot finish."}</dd></div>
          <div><dt>Who uses it?</dt><dd>{env.projects.length === 0 ? "No project deploys here yet." : <>Deployed to by {env.projects.map((p, i) => <span key={p}>{i > 0 && ", "}<EntityLink kind="project" name={p} /></span>)}.</>}</dd></div>
          <div><dt>What happened to it recently?</dt><dd>{last ? <>Run <EntityLink kind="run" name={`#${last.run_number}`} id={last.run_id} /> of {last.project} deployed {last.version}{last.opened_at ? `, ${ago(last.opened_at, now)}` : ""}.</> : "Nothing has been deployed here yet."}</dd></div>
          <div><dt>What can I do with it?</dt><dd className="glance-actions"><Link href="/activity">See the runs</Link>{me?.admin && <Link href="/config/wizard/add-environment">Add another environment</Link>}</dd></div>
        </dl>
      </section>
      <section className="card" aria-labelledby="env-run-h">
        <h2 id="env-run-h" className="h-sm">What runs here now</h2>
        {env.services.length === 0 ? <p className="faint">Nothing yet. A run deploys here when it reaches a deploy step for this environment.</p> : (
          <div className="tablewrap"><table className="guided-table">
            <thead><tr><th>Service</th><th>Project</th><th>Version</th><th>Deployed by</th></tr></thead>
            <tbody>
              {env.services.map((u) => (
                <tr key={u.project + "/" + u.service}>
                  <td>{u.service}</td>
                  <td><EntityLink kind="project" name={u.project} /></td>
                  <td>{u.current?.version ?? <span className="faint">not yet</span>}</td>
                  <td>{u.current ? <><EntityLink kind="run" name={`#${u.current.run_number}`} id={u.current.run} /> <span className="faint">{ago(u.current.since, now)}</span></> : ""}</td>
                </tr>
              ))}
            </tbody>
          </table></div>
        )}
      </section>
    </div>
  );
}
