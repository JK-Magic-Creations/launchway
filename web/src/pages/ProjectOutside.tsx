import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api";
import { ProjectHead } from "./PipelineChrome";
import { CopyBlock } from "../components/ConfigForm";
import { Empty, ErrorBox, Link } from "../components/ui";
import { Loading, useLoad } from "../work/common";
import { FEATURE_WORDS, outsideApi, type Connection, type FlightOutside, type OutsideRepository } from "../outsideApi";

// The code host of a Flight: the outside repository it is attached to, the two report switches, the webhook to set
// up on the host, when a delivery last arrived, and what the host cannot do, with what to do instead.

export function ProjectOutsidePage({ name }: { name: string }) {
  const load = useCallback(async (): Promise<FlightOutside | null> => {
    try { return await outsideApi.flight(name); } catch (e) { if (e instanceof ApiError && e.status === 404) return null; throw e; }
  }, [name]);
  const { data, error, loading, reload } = useLoad(`outside:${name}`, load);
  return (
    <div className="page page-wide">
      <ProjectHead name={name} on="outside" title={<>Code host of {name}</>}
        sentence="Where this Flight's code lives when it is not hosted here, and how Launchway works with it." />
      <ErrorBox error={error} />
      {loading && data === undefined && <Loading what="the code host" />}
      {data === null && <Attach name={name} onDone={reload} />}
      {data && <Attached name={name} f={data} onChanged={reload} />}
    </div>
  );
}

function Attach({ name, onDone }: { name: string; onDone: () => void }) {
  const [conns, setConns] = useState<Connection[] | null>(null);
  const [conn, setConn] = useState("");
  const [q, setQ] = useState("");
  const [found, setFound] = useState<OutsideRepository[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { outsideApi.connections().then((c) => { setConns(c); if (c[0]) setConn(c[0].name); }).catch(setError); }, []);
  const search = async () => { setError(null); try { setFound((await outsideApi.repositories(conn, q)).items); } catch (e) { setError(e); } };
  const attach = async (r: OutsideRepository) => {
    setBusy(true); setError(null);
    try { await outsideApi.attach(name, { connection: conn, repository: r.path }); onDone(); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  if (conns && conns.length === 0) return <Empty title="This Flight keeps its code in a plain Git address">Add a connection to a Git host in <Link href="/settings/connections" className="a-u">Settings</Link> to keep it on that host.</Empty>;
  return (
    <section aria-labelledby="attach-h">
      <h2 id="attach-h" className="h-sm">Attach a repository</h2>
      <p>This Flight reads a plain Git address. To work with its repository through a connection (statuses, comments, releases), choose the repository below.</p>
      <ErrorBox error={error} />
      <div className="cf-row">
        <label>Connection <select value={conn} onChange={(e) => { setConn(e.target.value); setFound(null); }}>{(conns ?? []).map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}</select></label>
        <label>Search <input value={q} onChange={(e) => setQ(e.target.value)} /></label>
        <button type="button" className="btn" disabled={!conn} onClick={search}>Search the host</button>
      </div>
      {found && found.length === 0 && <p role="status">No repository matches.</p>}
      {found && found.length > 0 && (
        <ul aria-label="Repositories on the host">
          {found.map((r) => <li key={r.path}><button type="button" className="btn btn-sm" disabled={busy} onClick={() => attach(r)}>Attach {r.path}</button></li>)}
        </ul>
      )}
    </section>
  );
}

function Attached({ name, f, onChanged }: { name: string; f: FlightOutside; onChanged: () => void }) {
  const [error, setError] = useState<unknown>(null);
  const [confirm, setConfirm] = useState(false);
  const set = async (body: { report_statuses?: boolean; report_comment?: boolean }) => {
    setError(null);
    try { await outsideApi.attach(name, body); onChanged(); } catch (e) { setError(e); }
  };
  const detach = async () => { setError(null); try { await outsideApi.detach(name); onChanged(); } catch (e) { setError(e); } };
  const lacking = Object.entries(f.unsupported ?? {});
  return (
    <>
      <p>Attached to <strong>{f.repository}</strong> through the connection <strong>{f.connection}</strong>{f.kind ? ` (${f.kind})` : ""}.</p>
      <ErrorBox error={error} />
      <fieldset className="cf-fieldset">
        <legend>Report back to the host</legend>
        <label className="cf-checkline"><input type="checkbox" checked={f.report_statuses} onChange={(e) => set({ report_statuses: e.target.checked })} /> A commit status for each Sortie and each failed step</label>
        <label className="cf-checkline"><input type="checkbox" checked={f.report_comment} onChange={(e) => set({ report_comment: e.target.checked })} /> One summary comment on each pull request, updated in place</label>
      </fieldset>
      <section aria-labelledby="hook-h">
        <h2 id="hook-h" className="h-sm">Webhook</h2>
        {f.webhook_url ? <CopyBlock title="Address to call" text={f.webhook_url} note="Add this as a webhook on the repository (or its owner)." /> :
          <p>Launchway does not know its own public address, so it cannot show the webhook address. Set <code>forge.public_url</code>.</p>}
        <ol>
          <li>Content type <code>application/json</code>.</li>
          <li>The secret you stored for this connection.</li>
          <li>Events: pushes and pull requests (tag pushes arrive as pushes).</li>
        </ol>
        <p role="status">{f.last_delivery_at ? <>Last delivery received {new Date(f.last_delivery_at).toLocaleString()}.</> : "No delivery received yet. Until one arrives the Flight is polled."}</p>
      </section>
      <section aria-labelledby="lack-h">
        <h2 id="lack-h" className="h-sm">What this host cannot do</h2>
        {lacking.length === 0 ? <p>Nothing Launchway offers is missing on this host.</p> : (
          <ul>
            {lacking.map(([k, d]) => (
              <li key={k}><strong>This host cannot {FEATURE_WORDS[k] ?? k}</strong>: {d.reason ?? "it does not offer it"}.{d.workaround ? <> {d.workaround}</> : null}</li>
            ))}
          </ul>
        )}
      </section>
      {f.additions && f.additions.length > 0 && (
        <section aria-labelledby="adds-h">
          <h2 id="adds-h" className="h-sm">What {f.kind === "github" ? "GitHub" : "this host"} adds</h2>
          <ul>
            {f.additions.map((a) => (
              <li key={a.key}><strong>{a.title}</strong> <span className="faint">from {f.kind === "github" ? "GitHub" : "the host"}</span>: {a.what}.{a.drivable ? " Launchway can switch it on for you." : " Use it on the host."}</li>
            ))}
          </ul>
        </section>
      )}
      <div className="cf-row">
        {!confirm && <button type="button" className="btn" onClick={() => setConfirm(true)}>Detach from the host</button>}
        {confirm && (
          <>
            <span role="alert">Detach {name}? It goes back to a plain Git address and Launchway stops reporting to the host.</span>
            <button type="button" className="btn btn-danger" onClick={detach}>Yes, detach</button>
            <button type="button" className="btn" onClick={() => setConfirm(false)}>Keep it attached</button>
          </>
        )}
      </div>
    </>
  );
}
