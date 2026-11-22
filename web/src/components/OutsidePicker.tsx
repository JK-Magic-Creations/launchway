import { useEffect, useState } from "react";
import { ApiError } from "../api";
import { outsideApi, type Connection, type OutsideRepository } from "../outsideApi";
import { Link } from "./ui";

export interface OutsidePick { connection: string; repository: string; address: string }

/** OutsidePicker chooses a repository through a connection to a Git host, searching the host. It draws nothing when the
 * server has no connections to hosts (or none is offered), so a person who does not use one never sees it. */
export function OutsidePicker({ picked, onPick }: { picked: OutsidePick | null; onPick: (p: OutsidePick | null) => void }) {
  const [conns, setConns] = useState<Connection[] | null>(null);
  const [conn, setConn] = useState("");
  const [q, setQ] = useState("");
  const [found, setFound] = useState<OutsideRepository[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    outsideApi.connections().then((c) => { const on = c.filter((x) => x.enabled); setConns(on); if (on[0]) setConn(on[0].name); }).catch(() => setConns([]));
  }, []);
  if (!conns || conns.length === 0) return null;
  const search = async () => {
    setBusy(true); setError("");
    try { setFound((await outsideApi.repositories(conn, q)).items); }
    catch (e) { setError(e instanceof ApiError || e instanceof Error ? e.message : String(e)); setFound(null); }
    finally { setBusy(false); }
  };
  return (
    <fieldset className="cf-fieldset outside-picker">
      <legend>Or choose a repository on a connected Git host</legend>
      <p className="cf-help">The connection's token reads it and reports back to it; its address is filled in for you. <Link href="/settings/connections" className="a-u">Manage connections</Link>.</p>
      {picked && (
        <p role="status">Chosen: <strong>{picked.repository}</strong> on {picked.connection}. <button type="button" className="btn btn-sm" onClick={() => onPick(null)}>Choose another</button></p>
      )}
      <div className="cf-row">
        <label>Connection <select value={conn} onChange={(e) => { setConn(e.target.value); setFound(null); }}>{conns.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}</select></label>
        <label>Search the host <input value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); search(); } }} /></label>
        <button type="button" className="btn" disabled={busy || !conn} onClick={search}>{busy ? "Searching…" : "Search"}</button>
      </div>
      {error && <p role="alert" className="cf-problem">{error}</p>}
      {found && found.length === 0 && <p role="status">No repository matches.</p>}
      {found && found.length > 0 && (
        <ul aria-label="Repositories on the host">
          {found.map((r) => (
            <li key={r.path}>
              <button type="button" className="btn btn-sm" onClick={() => onPick({ connection: conn, repository: r.path, address: r.clone.https ?? "" })} disabled={!r.clone.https}>
                Use {r.path}
              </button> <span className="faint">{r.visibility}{r.default_branch ? `, ${r.default_branch}` : ""}</span>
            </li>
          ))}
        </ul>
      )}
    </fieldset>
  );
}

/** AttachAfterCreate attaches the new project to the repository that was chosen on the host, as soon as the servers know
 * the project, and says what happened. */
export function AttachAfterCreate({ project, pick }: { project: string; pick: OutsidePick }) {
  const [state, setState] = useState<{ phase: "busy" | "done" | "error"; msg?: string }>({ phase: "busy" });
  const [again, setAgain] = useState(0);
  useEffect(() => {
    let alive = true;
    (async () => {
      for (let i = 0; i < 8; i++) {
        try {
          await outsideApi.attach(project, { connection: pick.connection, repository: pick.repository });
          if (alive) setState({ phase: "done" });
          return;
        } catch (e) {
          if (e instanceof ApiError && e.status === 404 && i < 7) { await new Promise((ok) => window.setTimeout(ok, 1000)); continue; }
          if (alive) setState({ phase: "error", msg: e instanceof Error ? e.message : String(e) });
          return;
        }
      }
    })();
    return () => { alive = false; };
  }, [project, pick, again]);
  if (state.phase === "busy") return <p role="status">Attaching to {pick.repository} on {pick.connection}…</p>;
  if (state.phase === "done") return <p role="status" className="cf-ok">Attached to {pick.repository} on {pick.connection}. <Link href={`/projects/${encodeURIComponent(project)}/outside`} className="a-u">See its code host</Link>.</p>;
  return (
    <p role="alert" className="cf-problem">It was not attached to the host: {state.msg}. <button type="button" className="btn btn-sm" onClick={() => { setState({ phase: "busy" }); setAgain(again + 1); }}>Try again</button></p>
  );
}
