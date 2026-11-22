import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { ago } from "../format";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { mirrorsApi, type Mirror, type Repository } from "../mirrors";
import { RepositorySettings } from "./Repositories";
import { terms } from "../terms";
import { RepoTabs } from "../components/RepoTabs";

// The imported history is a chunk of its own: it loads when its tab is opened.
const ImportedHistory = lazy(() => import("./ImportedHistory"));

function span(s: number): string {
  if (s <= 0) return "none";
  if (s < 90) return `${s} s`;
  if (s < 5400) return `${Math.round(s / 60)} min`;
  if (s < 129600) return `${Math.round(s / 3600)} h`;
  return `${Math.round(s / 86400)} days`;
}

// RepositoryPage is a hosted repository: where to clone it from, and its
// mirrors. The tab is part of the address, so a link opens the right one.
export function RepositoryPage({ path, tab }: { path: string; tab: "settings" | "mirrors" | "history" }) {
  const [repo, setRepo] = useState<Repository | null>(null);
  const [error, setError] = useState<unknown>(null);
  const load = useCallback(() => { mirrorsApi.repository(path).then((r) => { setRepo(r); setError(null); }).catch(setError); }, [path]);
  useEffect(load, [load]);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{path}</h1>
          {repo && <p className="sub">{repo.visibility} repository, default branch {repo.default_branch ?? "main"}{repo.mirror ? ", a mirror: it takes no pushes" : ""}{repo.archived ? ", archived" : ""}.</p>}
        </div>
      </header>
      <RepoTabs path={path} on={tab} />
      <ErrorBox error={error} />
      {tab === "settings" && <RepositorySettings owner={path.split("/")[0]} name={path.split("/").slice(1).join("/")} />}
      {repo && tab === "mirrors" && <MirrorsTab repo={repo} reload={load} />}
      {repo && tab === "history" && <Suspense fallback={<div className="loading">Loading…</div>}><ImportedHistory path={path} /></Suspense>}
    </div>
  );
}

function MirrorsTab({ repo, reload }: { repo: Repository; reload: () => void }) {
  const [list, setList] = useState<Mirror[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState("");
  const load = useCallback((background = false) => {
    mirrorsApi.mirrors(repo.path, background).then((m) => { setList(m); setError(null); }).catch(setError);
  }, [repo.path]);
  useEffect(() => {
    load();
    const t = setInterval(() => load(true), 10000);
    return () => clearInterval(t);
  }, [load]);

  const act = async (id: string, what: string, fn: () => Promise<unknown>) => {
    setBusy(id + what); setError(null);
    try { await fn(); load(); reload(); } catch (e) { setError(e); load(); } finally { setBusy(""); }
  };
  const pull = list?.find((m) => m.direction === "pull");
  const stop = async () => {
    if (!window.confirm(`Stop mirroring ${repo.path}? It becomes the primary and takes pushes from now on; the link to ${pull?.url ?? "the other repository"} is removed. Run a final sync first to pick up the latest.`)) return;
    await act("stop", "", () => mirrorsApi.stop(repo.path, window.confirm("Run one more sync first, so nothing the other repository gained is lost? (OK = yes, sync first)")));
  };

  return (
    <>
      <ErrorBox error={error} />
      {list && list.length === 0 && (
        <Empty title="No mirrors">A pull mirror keeps this repository an exact copy of one on another server; a push mirror copies every update to another server.</Empty>
      )}
      {list && list.map((m) => (
        <section key={m.id} className="card mirror" aria-label={`${m.direction} mirror`}>
          <h2 className="h-sm">{m.direction === "pull" ? "Pull mirror of" : "Push mirror to"} <Mono>{m.url}</Mono></h2>
          <p className="sub">
            {!m.enabled ? "Paused. " : m.running ? "Running now. " : ""}
            {m.state === "ok" && <>Last synced {m.last_success ? ago(Date.parse(m.last_success)) : "never"}, {m.refs} refs. </>}
            {m.state === "pending" && "Not run yet. "}
            {m.direction === "push" ? "Behind by " : "Since the last sync: "}{span(m.lag_seconds)}.
            {m.behind && <strong> It has fallen behind.</strong>}
            {m.credential ? <> Credential read from <Mono>{m.credential}</Mono>.</> : " No credential."}
          </p>
          {m.last_error && <div className="callout callout-err" role="alert">{terms().say("Last {run} failed (")}{m.failures} in a row): {m.last_error}</div>}
          <div className="actions">
            <button type="button" className="btn" disabled={!!busy} onClick={() => act(m.id, "sync", () => mirrorsApi.sync(repo.path, m.id))}>{busy === m.id + "sync" ? "Syncing…" : "Sync now"}</button>
            <button type="button" className="btn" disabled={!!busy} onClick={() => act(m.id, "pause", () => mirrorsApi.pause(repo.path, m.id, !m.enabled))}>{m.enabled ? "Pause" : "Resume"}</button>
            {m.direction === "push" && <button type="button" className="btn" disabled={!!busy} onClick={() => act(m.id, "remove", () => mirrorsApi.remove(repo.path, m.id))}>Remove</button>}
            {m.direction === "pull" && <button type="button" className="btn btn-primary" disabled={!!busy} onClick={stop}>Stop mirroring, make this the primary</button>}
          </div>
        </section>
      ))}
      <AddMirror repo={repo} hasPull={!!pull} onAdded={() => { load(); reload(); }} />
    </>
  );
}

function AddMirror({ repo, hasPull, onAdded }: { repo: Repository; hasPull: boolean; onAdded: () => void }) {
  const [direction, setDirection] = useState<"pull" | "push">(hasPull ? "push" : "pull");
  const [url, setUrl] = useState("");
  const [username, setUsername] = useState("");
  const [credKind, setCredKind] = useState<"env" | "file" | "">("");
  const [cred, setCred] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await mirrorsApi.add(repo.path, { direction, url: url.trim(), username: username.trim() || undefined,
        credential: credKind && cred.trim() ? { [credKind]: cred.trim() } : undefined });
      setUrl(""); setUsername(""); setCred("");
      onAdded();
    } catch (err) { setError(err); } finally { setBusy(false); }
  };
  return (
    <section className="card">
      <h2 className="h-sm">Add a mirror</h2>
      <form onSubmit={submit} className="inputs-form" aria-label="Add a mirror">
        <label className="field"><span>Direction</span>
          <select value={direction} onChange={(e) => setDirection(e.target.value as "pull" | "push")}>
            <option value="pull" disabled={hasPull}>Pull: keep this repository a copy of another</option>
            <option value="push">Push: copy every update to another</option>
          </select></label>
        <label className="field"><span>Address</span>
          <input value={url} required placeholder="https://git.example.com/team/app.git" onChange={(e) => setUrl(e.target.value)} /></label>
        <label className="field"><span>User name (if the token needs one)</span>
          <input value={username} onChange={(e) => setUsername(e.target.value)} /></label>
        <label className="field"><span>Where the server reads the token from</span>
          <select value={credKind} onChange={(e) => setCredKind(e.target.value as "env" | "file" | "")}>
            <option value="">No credential (a public repository)</option>
            <option value="env">An environment variable of the server</option>
            <option value="file">A file on the server</option>
          </select></label>
        {credKind && (
          <label className="field"><span>{credKind === "env" ? "Variable name" : "File path"}</span>
            <input value={cred} placeholder={credKind === "env" ? "GITEA_TOKEN" : "/run/secrets/gitea-token"} onChange={(e) => setCred(e.target.value)} />
            <small>This is a reference. The token itself is never typed here or kept by Launchway.</small></label>
        )}
        <p className="sub">Only https and http addresses. An address on a private network is refused unless an administrator has listed it
          in <Mono>forge.mirror_allow</Mono>.{direction === "pull" && " A pull mirror needs a repository with no branches or tags of its own."}</p>
        <ErrorBox error={error} />
        <div><button type="submit" className="btn btn-primary" disabled={busy || !url.trim()}>{busy ? "Adding…" : "Add mirror"}</button></div>
      </form>
      <p className="sub"><Link href="/projects">{terms().say("{Projects}")}</Link> use repositories; a mirror keeps a copy current, it does not change who may push where.</p>
    </section>
  );
}
