import { useState } from "react";
import { ErrorBox, Mono } from "../components/ui";
import { launchApi, type LNode, type TreeView } from "../launchApi";
import { ago, clock } from "../format";
import { ChangeTag, HealthTag } from "./Tree";
import { BtnSm, SpanFaint, SpanHidden, ThCol, ThRow } from "../kit/bits";

/** One object: its status, what the controller says, its events, what it holds, and its manifest. */
export function Details({ v, node, now, onLogs, onSelect }: { v: TreeView; node: LNode; now: number; onLogs: (q: Record<string, string>) => void; onSelect: (id: string) => void }) {
  const [yaml, setYaml] = useState<{ id: string; text: string } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const showManifest = async () => {
    setBusy(true); setError(null);
    try { setYaml({ id: node.id, text: (await launchApi.manifest(v.run, v.deployment, node.id)).yaml }); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const nodes = v.tree.nodes;
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const link = (id: string) => {
    const n = byId.get(id);
    return <button type="button" className="linkish" onClick={() => onSelect(id)}>{n ? `${n.kind} ${n.name}` : id}</button>;
  };
  const isPod = node.kind === "Pod";
  const logsFor: Record<string, string> | null = node.kind === "ReplicaSet" ? { replica_set: node.id } : node.kind === "Job" ? { job: node.id } : null;
  return (
    <section className="lw-details" aria-labelledby="lw-det-h">
      <h2 id="lw-det-h" className="lw-det-h"><span className="lw-kind">{node.kind}</span> <Mono>{node.name}</Mono></h2>
      <p className="lw-det-sub">
        <HealthTag h={node.health} /> <ChangeTag c={node.change} />
        {node.role && <> <span className={`lw-role lw-role-${node.role}`}>{node.role}</span></>}
        {node.created ? <SpanFaint> · created {ago(node.created, now)}<SpanHidden> ({clock(node.created)})</SpanHidden></SpanFaint> : null}
        {node.namespace && <SpanFaint> · namespace <Mono>{node.namespace}</Mono></SpanFaint>}
      </p>
      {node.message && <p className="lw-msg"><SpanFaint>The controller says: </SpanFaint>{node.message}</p>}
      {node.not_ready && (
        <div className="callout callout-bad lw-why" role="note">
          <strong>Why this is not ready.</strong> {node.not_ready.why}
          {node.not_ready.last_logs && node.not_ready.last_logs.length > 0 && <pre className="lw-pre">{node.not_ready.last_logs.join("\n")}</pre>}
        </div>
      )}
      {node.replicas && <p>Replicas: <strong>{node.replicas.ready}</strong> ready of <strong>{node.replicas.desired}</strong> wanted ({node.replicas.available} available, {node.replicas.updated} updated).</p>}
      {node.image && !isPod && <p>Image <Mono>{node.image}</Mono></p>}
      {node.revision && <p>Revision <Mono>{node.revision}</Mono>{node.hash ? <> · pod template <Mono>{node.hash}</Mono></> : null}</p>}

      {isPod && node.containers && (
        <>
          <h3 className="h-xs">Containers</h3>
          <table className="lw-table">
            <caption className="visually-hidden">Containers of {node.name}</caption>
            <thead><tr><ThCol>Container</ThCol><ThCol>State</ThCol><ThCol>Restarts</ThCol><ThCol>Logs</ThCol></tr></thead>
            <tbody>
              {node.containers.map((c) => (
                <tr key={c.name + (c.init ? "i" : "")}>
                  <ThRow><Mono>{c.name}</Mono>{c.init && <SpanFaint> (init)</SpanFaint>}<div className="faint lw-small"><Mono>{c.image}</Mono></div></ThRow>
                  <td>{c.state}{c.reason ? `: ${c.reason}` : ""}{c.exit_code !== undefined ? ` (exit ${c.exit_code})` : ""}{c.state === "running" && !c.ready ? ", not ready" : ""}</td>
                  <td>{c.restarts}</td>
                  <td>
                    {v.can_logs ? (
                      <>
                        <BtnSm onClick={() => onLogs({ pod: node.name, container: c.name })}>Read<SpanHidden> logs of {c.name}</SpanHidden></BtnSm>
                        {c.previous && c.restarts > 0 && <> <BtnSm onClick={() => onLogs({ pod: node.name, container: c.name, previous: "1" })}>Earlier run<SpanHidden> of {c.name}</SpanHidden></BtnSm></>}
                      </>
                    ) : <SpanFaint>needs the operator role</SpanFaint>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
      {logsFor && (v.can_logs
        ? <p><button type="button" className="btn" onClick={() => onLogs(logsFor)}>Read the logs{node.kind === "Job" ? " (what the test sent and got back)" : " of all its pods"}</button></p>
        : <p className="faint">Logs need the operator role on {v.project}.</p>)}

      {node.kind === "Service" && (
        <>
          <p>Ports: {node.ports?.length ? node.ports.join(", ") : "none"}.</p>
          <p>Selects: {node.selects?.length ? node.selects.map((s, i) => <span key={s}>{i > 0 && ", "}{link(s)}</span>) : <SpanFaint>no running ReplicaSet of this release</SpanFaint>}.</p>
        </>
      )}
      {node.weights && node.weights.length > 0 && (
        <>
          <h3 className="h-xs">Where requests go</h3>
          <table className="lw-table"><caption className="visually-hidden">Backends of {node.name} and their weights</caption>
            <thead><tr><ThCol>Backend</ThCol><ThCol>Weight</ThCol><ThCol>Only when</ThCol></tr></thead>
            <tbody>{node.weights.map((w, i) => <tr key={i}><ThRow><Mono>{w.backend}</Mono></ThRow><td>{w.weight}</td><td>{w.match || "always"}</td></tr>)}</tbody>
          </table>
        </>
      )}
      {node.kind === "ConfigMap" && node.data && (
        <>
          <h3 className="h-xs">Data <SpanFaint>(anything that looks like a secret is hidden: <Mono>***</Mono>)</SpanFaint></h3>
          <dl className="lw-data">
            {Object.entries(node.data).map(([k, val]) => <div key={k}><dt><Mono>{k}</Mono></dt><dd><pre className="lw-pre">{val}</pre></dd></div>)}
          </dl>
        </>
      )}
      {node.kind === "Secret" && (
        <>
          <h3 className="h-xs">Keys</h3>
          <p className="faint">Launchway shows the names of a Secret's keys, never their values.</p>
          <ul className="lw-keys">{(node.keys ?? []).map((k) => <li key={k}><Mono>{k}</Mono></li>)}</ul>
        </>
      )}
      {node.bound && node.bound.length > 0 && <ul className="lw-keys">{node.bound.map((b) => <li key={b}>{b}</li>)}</ul>}
      {node.used_by && node.used_by.length > 0 && <p>Read by: {node.used_by.map((s, i) => <span key={s}>{i > 0 && ", "}{link(s)}</span>)}.</p>}

      <h3 className="h-xs">Events</h3>
      {node.events && node.events.length > 0 ? (
        <ul className="lw-events">
          {node.events.map((e, i) => (
            <li key={i} className={e.type === "Warning" ? "lw-warn" : ""}>
              <strong>{e.type === "Warning" ? "Warning" : "Normal"}</strong> {e.reason}: {e.message}
              <SpanFaint> · {e.count > 1 ? `${e.count} times, ` : ""}last {ago(e.last, now)}</SpanFaint>
            </li>
          ))}
        </ul>
      ) : <p className="faint">No events kept for this object.</p>}

      <h3 className="h-xs">Manifest</h3>
      {yaml?.id === node.id ? <pre className="lw-pre lw-yaml" aria-label={`Manifest of ${node.name}`}>{yaml.text}</pre> : (
        <p>
          <button type="button" className="btn" disabled={busy} onClick={showManifest}>{busy ? "Reading…" : "Show the live manifest"}</button>
          <SpanFaint> Secret values and anything that looks like one are hidden.</SpanFaint>
        </p>
      )}
      <ErrorBox error={error} />
    </section>
  );
}
