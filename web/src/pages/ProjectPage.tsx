import { useEffect, useState } from "react";
import { api, type Project, type RunSummary } from "../api";
import { canOperate, useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { StartDialog } from "../components/StartRun";
import { ProjectTriggers } from "../components/ProjectTriggers";
import { BranchIcon, ErrorBox, Link, Mono, StatusPill } from "../components/ui";
import { ago, refLabel, shortSha } from "../format";
import { useInstallation } from "../guided/facts";
import { useCrumbLabel } from "../nav/crumbs";
import { useNow } from "../live";

/** A project's own page. At the top, five answers: what it is, where it comes from, who uses it,
 * what happened to it lately, and what can be done with it. */
export function ProjectPage({ name }: { name: string }) {
  const [project, setProject] = useState<Project | null>(null);
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [starting, setStarting] = useState(false);
  const me = useMe();
  const inst = useInstallation();
  const now = useNow(5000);
  useCrumbLabel(name);

  useEffect(() => {
    let alive = true;
    const load = (bg: boolean) => {
      api.project(name).then((p) => { if (alive) { setProject(p); setError(null); } }).catch((e) => alive && setError(e));
      api.runs({ project: name, limit: 8 }, bg).then((r) => alive && setRuns(r)).catch(() => {});
    };
    load(false);
    const t = setInterval(() => load(true), 5000);
    return () => { alive = false; clearInterval(t); };
  }, [name]);

  if (error) return <div className="page"><ErrorBox error={error} /></div>;
  if (!project) return <div className="page"><div className="loading">Loading…</div></div>;

  const enc = encodeURIComponent(project.name);
  const envs = inst.environments.filter((e) => e.projects.includes(project.name));
  const people = [...new Set((runs ?? []).map((r) => r.author || r.actor).filter(Boolean))];
  const last = runs?.[0] ?? project.last_run;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{project.name}</h1>
          <p className="sub">A project: a Git repository whose pipeline Launchway runs.</p>
        </div>
        <div className="head-actions">
          {canOperate(me, project.name) && <button type="button" className="btn btn-primary" onClick={() => setStarting(true)}>Start a run</button>}
          <Link href={`/projects/${enc}/pipeline`} className="btn">See the pipeline</Link>
        </div>
      </header>

      {project.error && <div className="callout callout-bad">Cannot reach the repository: {project.error}</div>}

      <section className="card glance" aria-label="At a glance">
        <dl className="glance-grid">
          <div>
            <dt>What is this?</dt>
            <dd>A project built and tested from <Mono>{project.url}</Mono>, on the branch <span className="meta"><BranchIcon />{project.default_branch}</span>.</dd>
          </div>
          <div>
            <dt>Where does it come from?</dt>
            <dd>
              {project.pipeline
                ? <>Its pipeline is the template <Mono>{project.pipeline.template}</Mono> of the library <Mono>{project.pipeline.library}@{project.pipeline.version}</Mono>, unless the repository has its own.</>
                : <>Its pipeline is a file in the repository, so it is reviewed and versioned like code.</>}
              {" "}Launchway checks it every {project.poll_seconds}s{project.last_fetch > 0 ? `, last ${ago(project.last_fetch, now)}` : ""}.
            </dd>
          </div>
          <div>
            <dt>Who uses it?</dt>
            <dd>
              {envs.length > 0 ? <>Deployed to {envs.map((e, i) => <span key={e.name}>{i > 0 && ", "}<EntityLink kind="environment" name={e.name} /></span>)}. </> : "It is not deployed anywhere yet. "}
              {people.length > 0 ? <>Recent runs by {people.slice(0, 4).join(", ")}.</> : "Nobody has run it yet."}
            </dd>
          </div>
          <div>
            <dt>What happened to it recently?</dt>
            <dd>
              {last ? <><StatusPill status={last.status} /> <EntityLink kind="run" name={`Run #${last.number}`} id={last.id} /> {ago(last.created_at, now)}.</> : "Nothing yet. Start a run to see it work."}
            </dd>
          </div>
          <div>
            <dt>What can I do with it?</dt>
            <dd className="glance-actions">
              {canOperate(me, project.name) && <button type="button" className="linkbtn" onClick={() => setStarting(true)}>Start a run</button>}
              <Link href={`/projects/${enc}/pipeline/edit?ref=${encodeURIComponent(project.default_branch)}`}>Edit the pipeline</Link>
              <Link href={`/projects/${enc}/tests`}>See tests and coverage</Link>
              <Link href={`/projects/${enc}/security`}>See security findings</Link>
              {me?.admin && <Link href={`/config/projects/${enc}`}>Change its settings</Link>}
            </dd>
          </div>
        </dl>
      </section>

      <section className="card" aria-labelledby="proj-runs-h">
        <div className="card-head"><h2 id="proj-runs-h" className="h-sm">Recent runs</h2><Link href="/activity">All activity</Link></div>
        {runs === null ? <p className="faint">Loading…</p> : runs.length === 0 ? (
          <p className="faint">No runs yet. Press Start a run, or push to the repository: a push to a branch the pipeline watches starts one by itself.</p>
        ) : (
          <ul className="guided-list">
            {runs.map((r) => (
              <li key={r.id}>
                <StatusPill status={r.status} />
                <EntityLink kind="run" name={`#${r.number}`} id={r.id} />
                <span className="guided-grow">{r.message || r.pipeline}</span>
                <span className="faint">{refLabel(r)}{r.sha ? ` · ${shortSha(r.sha)}` : ""}</span>
                <span className="faint">{ago(r.created_at, now)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <ProjectTriggers project={project.name} />
      <section className="card" aria-labelledby="proj-pushes-h">
        <h2 id="proj-pushes-h" className="h-sm">Recent pushes ({project.decisions.length})</h2>
        {project.decisions.length === 0 ? (
          <p className="faint">No pushes seen since the server started. Launchway notes where every branch is the first time it looks, and reacts to changes after that.</p>
        ) : (
          <ul className="guided-list">
            {project.decisions.map((d, i) => (
              <li key={i}>
                <span className={"d-outcome d-" + d.outcome}>{d.outcome === "started" ? "Started" : d.outcome === "ignored" ? "Ignored" : "Problem"}</span>
                <span className="mono">{d.ref.replace(/^refs\/(heads|tags)\//, "")}</span>
                <span className="mono faint">{shortSha(d.sha)}</span>
                <span className="guided-grow">{d.run_id ? <Link href={`/runs/${d.run_id}`}>{d.detail}</Link> : d.detail}</span>
                <span className="faint">{ago(d.at, now)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
      {starting && <StartDialog project={project} onClose={() => setStarting(false)} />}
    </div>
  );
}
