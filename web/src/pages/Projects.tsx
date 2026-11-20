import { useEffect, useRef, useState } from "react";
import { api, ApiError, type LibraryOverrides, type Project, type Ref } from "../api";
import { ago, shortSha } from "../format";
import { navigate } from "../router";
import { useNow } from "../live";
import { canOperate, useMe } from "../auth";
import { BranchIcon, Empty, ErrorBox, Link, Mono, StatusPill } from "../components/ui";
import { RefPicker } from "../components/RefPicker";
import { OverridesEditor } from "../components/Libraries";
import { TestCounts } from "../components/Tests";
import { StartInputs, useStartInputs } from "../components/StartInputs";
import { triggersApi } from "../triggers";
import { ProjectTriggers } from "../components/ProjectTriggers";
import { ArtifactCount, FindingCountBadge } from "../components/Security";

export function ProjectsPage() {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [starting, setStarting] = useState<Project | null>(null);
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
          <p className="sub">Repositories Launchway watches. A push that matches a pipeline's trigger starts a run; every push is explained below.</p>
        </div>
      </header>
      <ErrorBox error={error} />
      {projects === null && !error && <div className="loading">Loading…</div>}
      {projects && projects.length === 0 && (
        <Empty title="No projects yet">
          Add repositories to <Mono>launchway.yaml</Mono> and restart the server:
          <pre className="code">{`projects:
  - name: my-service
    repo: https://git.example.com/me/my-service.git
    branch: main`}</pre>
          Any Git host works, or a local path.
        </Empty>
      )}
      <div className="projects">
        {projects?.map((p) => (
          <section key={p.name} className="card project">
            <div className="project-head">
              <div>
                <h2>{p.name}</h2>
                <div className="faint mono small">{p.url}</div>
              </div>
              <div className="project-actions">
                <Link href={`/projects/${encodeURIComponent(p.name)}/tests`} className="btn">Tests and coverage</Link>
                <Link href={`/projects/${encodeURIComponent(p.name)}/security`} className="btn">Security</Link>
                <Link href={`/projects/${encodeURIComponent(p.name)}/caches`} className="btn">Caches</Link>
                <Link href={`/projects/${encodeURIComponent(p.name)}/pipeline?ref=${encodeURIComponent(p.default_branch)}`} className="btn">Edit pipeline</Link>
                {canOperate(me, p.name) && <button type="button" className="btn btn-primary" onClick={() => setStarting(p)}>Start a run</button>}
              </div>
            </div>
            {p.error && <div className="callout callout-bad">Cannot reach the repository: {p.error}</div>}
            <div className="project-facts">
              <span className="meta"><BranchIcon />default {p.default_branch}</span>
              <span className="meta">checks every {p.poll_seconds}s</span>
              {p.last_fetch > 0 && <span className="meta">last checked {ago(p.last_fetch, now)}</span>}
            </div>
            {p.pipeline && (
              <p className="sub project-pipeline">
                Pipeline from the library <Mono>{p.pipeline.library}@{p.pipeline.version}</Mono>, template <Mono>{p.pipeline.template}</Mono>,
                configured on the server{p.pipeline.on.push?.branches?.length ? <>, on pushes to {p.pipeline.on.push.branches.join(", ")}</> : null}{p.pipeline.on.pull_request ? <>, on pull requests</> : null}.
                {" "}A <Mono>.launchway/pipeline.yaml</Mono> in the repository takes its place.
              </p>
            )}
            {p.last_run && (
              <Link href={`/runs/${p.last_run.id}`} className="lastrun">
                <StatusPill status={p.last_run.status} />
                <span>#{p.last_run.number} {p.last_run.message || p.last_run.pipeline}</span>
                {p.last_run.tests && <TestCounts t={p.last_run.tests} />}
                {p.last_run.findings && <FindingCountBadge c={p.last_run.findings} />}
                {!!p.last_run.artifacts && <ArtifactCount n={p.last_run.artifacts} />}
                <span className="faint">{ago(p.last_run.created_at, now)}</span>
              </Link>
            )}
            <ProjectTriggers project={p.name} />
            <details className="decisions" open={p.decisions.length > 0 && p.decisions.length <= 3}>
              <summary>Recent pushes ({p.decisions.length})</summary>
              {p.decisions.length === 0 && <p className="sub">No pushes seen since the server started. Launchway records where every branch is the first time it looks, and reacts to changes after that.</p>}
              <ul>
                {p.decisions.map((d, i) => (
                  <li key={i} className={"decision d-" + d.outcome}>
                    <span className="d-outcome">{d.outcome === "started" ? "Started" : d.outcome === "ignored" ? "Ignored" : "Problem"}</span>
                    <span className="mono">{d.ref.replace(/^refs\/(heads|tags)\//, "")}</span>
                    <span className="mono faint">{shortSha(d.sha)}</span>
                    <span className="d-detail">
                      {d.run_id ? <Link href={`/runs/${d.run_id}`}>{d.detail}</Link> : <span style={{ whiteSpace: "pre-wrap" }}>{d.detail}</span>}
                    </span>
                    <span className="faint">{ago(d.at, now)}</span>
                  </li>
                ))}
              </ul>
            </details>
          </section>
        ))}
      </div>
      {starting && <StartDialog project={starting} onClose={() => setStarting(null)} />}
    </div>
  );
}

function StartDialog({ project, onClose }: { project: Project; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [pick, setPick] = useState<Ref | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [overrides, setOverrides] = useState<LibraryOverrides>({});
  const inputs = useStartInputs(project.name, pick?.name ?? "");

  useEffect(() => { dialog.current?.showModal(); }, []);

  const start = async () => {
    if (!pick || !inputs.ready) return;
    if (!inputs.checkAll()) return;
    setBusy(true); setError(null); inputs.setServerErrors(undefined);
    try {
      const run = await triggersApi.startRun(project.name, pick.name, inputs.wire(), Object.keys(overrides).length ? overrides : undefined);
      dialog.current?.close();
      onClose();
      navigate(`/runs/${run.id}`);
    } catch (e) {
      if (e instanceof ApiError && e.fields) inputs.setServerErrors(e.fields);
      setError(e); setBusy(false);
    }
  };

  return (
    <dialog ref={dialog} className="dialog" onClose={onClose} aria-labelledby="start-title">
      <form method="dialog" onSubmit={(e) => { e.preventDefault(); start(); }}>
        <h2 id="start-title">Start a run of {project.name}</h2>
        <p className="sub">Branches and tags are read from the repository now. The pipeline file at the chosen commit is what runs{project.pipeline ? ", or, if it has none, the pipeline configured on the server" : ""}.</p>
        <RefPicker project={project.name} defaultBranch={project.default_branch} chosen={pick?.name ?? ""} onChoose={setPick} />
        {pick && (
          <div className="refpick">
            Will run <strong>{pick.name}</strong> at <Mono>{shortSha(pick.sha)}</Mono>, "{pick.subject}"
          </div>
        )}
        <StartInputs state={inputs} />
        <details className="advanced">
          <summary>Advanced{Object.keys(overrides).length > 0 && `: ${Object.keys(overrides).length} library override${Object.keys(overrides).length > 1 ? "s" : ""}`}</summary>
          <OverridesEditor value={overrides} onChange={setOverrides} names={project.pipeline ? [project.pipeline.library] : undefined} />
        </details>
        <ErrorBox error={error instanceof ApiError && error.problems
          ? error.message + ":\n" + error.problems.map((p) => (p.file ? `${p.library}: ${p.file}` : "") + (p.line ? ` line ${p.line}` : "") + ": " + p.message).join("\n")
          : error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={() => { dialog.current?.close(); }}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={!pick || busy || !inputs.ready}>{busy ? "Starting…" : "Start run"}</button>
        </div>
      </form>
    </dialog>
  );
}
