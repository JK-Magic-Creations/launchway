import { useEffect, useState } from "react";
import { api, type Project } from "../api";
import { useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { ErrorBox, Link, StatusPill } from "../components/ui";
import { WizardButton, WizardEmpty } from "../components/WizardButton";
import { ago } from "../format";
import { useNow } from "../live";

/** The projects: each row is a link to the project's own page, which answers what it is,
 * where it comes from, who uses it, what happened to it and what can be done with it. */
export function ProjectsPage() {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const now = useNow(5000);
  const me = useMe();

  useEffect(() => {
    let alive = true;
    const load = (background: boolean) => api.projects(background).then((p) => { if (alive) { setProjects(p); setError(null); } }).catch((e) => alive && setError(e));
    load(false);
    const t = setInterval(() => load(true), 5000);
    return () => { alive = false; clearInterval(t); };
  }, []);

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Projects</h1>
          <p className="sub">Your applications. Each is a Git repository whose pipeline Launchway runs when you push.</p>
        </div>
        <div className="head-actions">
          {me?.runners && <Link href="/playground" className="btn">Try a pipeline without saving</Link>}
          {projects && projects.length > 0 && <WizardButton wizard="connect-repository" label="Add a project" />}
        </div>
      </header>
      <ErrorBox error={error} />
      {projects === null && !error && <div className="loading">Loading…</div>}
      {projects && projects.length === 0 && (
        <WizardEmpty title="No projects yet" wizard="connect-repository" label="Add a project"
          text="A project is a Git repository. Pick one and Launchway reads its pipeline from Git and runs it each time you push." />
      )}
      {projects && projects.length > 0 && (
        <ul className="guided-rows">
          {projects.map((p) => (
            <li key={p.name} className="guided-row">
              <div className="guided-row-main">
                <EntityLink kind="project" name={p.name} className="guided-row-name" />
                <span className="faint mono small">{p.url}</span>
                {p.error && <span className="guided-warn">Cannot reach the repository: {p.error}</span>}
              </div>
              <div className="guided-row-side">
                {p.last_run ? (
                  <>
                    <StatusPill status={p.last_run.status} />
                    <EntityLink kind="run" name={`#${p.last_run.number}`} id={p.last_run.id} />
                    <span className="faint">{ago(p.last_run.created_at, now)}</span>
                  </>
                ) : <span className="faint">No runs yet</span>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
