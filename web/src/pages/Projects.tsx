import { useEffect, useMemo, useRef, useState } from "react";
import { api, type Project, type Ref } from "../api";
import { ago, shortSha } from "../format";
import { navigate } from "../router";
import { useNow } from "../live";
import { BranchIcon, Empty, ErrorBox, Link, Mono, StatusPill } from "../components/ui";

export function ProjectsPage() {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [starting, setStarting] = useState<Project | null>(null);
  const now = useNow(5000);

  useEffect(() => {
    let alive = true;
    const load = () => api.projects().then((p) => { if (alive) { setProjects(p); setError(null); } }).catch((e) => alive && setError(e));
    load();
    const t = setInterval(load, 5000);
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
              <button type="button" className="btn btn-primary" onClick={() => setStarting(p)}>Start a run</button>
            </div>
            {p.error && <div className="callout callout-bad">Cannot reach the repository: {p.error}</div>}
            <div className="project-facts">
              <span className="meta"><BranchIcon />default {p.default_branch}</span>
              <span className="meta">checks every {p.poll_seconds}s</span>
              {p.last_fetch > 0 && <span className="meta">last checked {ago(p.last_fetch, now)}</span>}
            </div>
            {p.last_run && (
              <Link href={`/runs/${p.last_run.id}`} className="lastrun">
                <StatusPill status={p.last_run.status} />
                <span>#{p.last_run.number} {p.last_run.message || p.last_run.pipeline}</span>
                <span className="faint">{ago(p.last_run.created_at, now)}</span>
              </Link>
            )}
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
  const [refs, setRefs] = useState<Ref[] | null>(null);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [query, setQuery] = useState("");
  const [chosen, setChosen] = useState<string>("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => { dialog.current?.showModal(); }, []);
  useEffect(() => {
    api.refs(project.name).then((r) => {
      setRefs(r);
      setChosen((c) => c || r.find((x) => x.name === project.default_branch)?.name || r[0]?.name || "");
    }).catch(setLoadErr);
  }, [project.name, project.default_branch]);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (refs ?? []).filter((r) => !q || r.name.toLowerCase().includes(q) || r.subject.toLowerCase().includes(q)).slice(0, 50);
  }, [refs, query]);
  const pick = refs?.find((r) => r.name === chosen);

  const start = async () => {
    setBusy(true); setError(null);
    try {
      const run = await api.startRun(project.name, chosen);
      dialog.current?.close();
      onClose();
      navigate(`/runs/${run.id}`);
    } catch (e) { setError(e); setBusy(false); }
  };

  return (
    <dialog ref={dialog} className="dialog" onClose={onClose} aria-labelledby="start-title">
      <form method="dialog" onSubmit={(e) => { e.preventDefault(); if (chosen) start(); }}>
        <h2 id="start-title">Start a run of {project.name}</h2>
        <p className="sub">Branches and tags are read from the repository now. The pipeline file at the chosen commit is what runs.</p>
        <label className="field">
          <span>Branch or tag</span>
          <input type="search" placeholder="Search branches, tags and commit messages" value={query}
            onChange={(e) => setQuery(e.target.value)} autoFocus />
        </label>
        <ErrorBox error={loadErr} />
        {refs === null && !loadErr && <div className="loading">Reading the repository…</div>}
        <div className="reflist" role="listbox" aria-label="Branches and tags">
          {shown.map((r) => (
            <button key={r.kind + r.name} type="button" role="option" aria-selected={chosen === r.name}
              className={"refrow" + (chosen === r.name ? " refrow-on" : "")} onClick={() => setChosen(r.name)}>
              <span className={"refkind refkind-" + r.kind}>{r.kind}</span>
              <span className="refname mono">{r.name}</span>
              <span className="refsubj">{r.subject}</span>
              <span className="faint small">{r.author} · {ago(r.committed_at)}</span>
            </button>
          ))}
          {refs && shown.length === 0 && <div className="sub">Nothing matches.</div>}
        </div>
        {pick && (
          <div className="refpick">
            Will run <strong>{pick.name}</strong> at <Mono>{shortSha(pick.sha)}</Mono>, "{pick.subject}"
          </div>
        )}
        <ErrorBox error={error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={() => { dialog.current?.close(); }}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={!chosen || busy}>{busy ? "Starting…" : "Start run"}</button>
        </div>
      </form>
    </dialog>
  );
}
