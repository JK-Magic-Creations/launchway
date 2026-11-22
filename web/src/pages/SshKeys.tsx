import { useCallback, useEffect, useState } from "react";
import { ago, clock } from "../format";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { PageHead, Sheet } from "../kit/layout";
import { forgeApi, type SshInfo, type SshKey } from "../forgeApi";
import "../triggers.css";
import { prose } from "../prose";
const P = await prose("pages.SshKeys");

/** Whether this server hosts repositories: the calls answer feature_off (404) when it does not. */
export function useHosting(): "on" | "off" | null {
  const [state, setState] = useState<"on" | "off" | null>(null);
  useEffect(() => {
    let live = true;
    forgeApi.repositories().then(() => live && setState("on"), () => live && setState("off"));
    return () => { live = false; };
  }, []);
  return state;
}

/** The server's address for Git over SSH, as people type it. */
export function sshAddress(info: SshInfo): string {
  return info.port && info.port !== 22 ? `${info.host}:${info.port}` : `${info.host}`;
}

/** SSH keys: the keys that let this person clone and push over SSH, and the server's own fingerprints to verify on first connect. */
export function SshKeysPage() {
  const hosting = useHosting();
  const [keys, setKeys] = useState<SshKey[] | null>(null);
  const [info, setInfo] = useState<SshInfo | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [pub, setPub] = useState("");
  const [expires, setExpires] = useState("");
  const [busy, setBusy] = useState(false);
  const [now] = useState(() => Date.now());

  const load = useCallback(() => {
    forgeApi.myKeys().then((k) => { setKeys(k); setError(null); }).catch(setError);
    forgeApi.ssh().then(setInfo).catch(setError);
  }, []);
  useEffect(() => { if (hosting === "on") load(); }, [hosting, load]);

  if (hosting === null) return null;
  if (hosting === "off") {
    return (
      <div className="page">
        <PageHead eyebrow="Settings" title="SSH keys" />
        <Empty title={P.t0}>{P.t1}<Mono>forge.hosting</Mono>.</Empty>
      </div>
    );
  }

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await forgeApi.addMyKey({ name: name.trim(), public_key: pub.trim(), expires_at: expires ? new Date(expires + "T23:59:59Z").toISOString() : undefined });
      setName(""); setPub(""); setExpires("");
      load();
    } catch (err) { setError(err); } finally { setBusy(false); }
  };
  const remove = async (k: SshKey) => {
    if (!window.confirm(`Delete the key "${k.name}"? It stops working at once.`)) return;
    setError(null);
    try { await forgeApi.deleteMyKey(k.id); load(); } catch (err) { setError(err); }
  };
  return (
    <div className="page">
      <PageHead eyebrow="Settings" title="SSH keys"
        sentence={<>{P.t2}<Link href="/repositories">deploy key</Link>.</>} />

      {info && !info.enabled && (
        <div className="callout callout-allowed" role="status">
          <strong>{P.t3}</strong> {P.t4}<Mono>forge.ssh.listen</Mono>{P.t5}<Link href="/settings/access/keys">API token</Link>.
        </div>
      )}

      <Sheet title="Add a key" pad>
        <form onSubmit={add} className="inputs-form" aria-label="Add an SSH key">
          <div className="tokenform-grid">
            <label className="field"><span>Name</span><input value={name} maxLength={100} placeholder="for example: work laptop" onChange={(e) => setName(e.target.value)} /></label>
            <label className="field"><span>Expires (optional)</span><input type="date" value={expires} onChange={(e) => setExpires(e.target.value)} /></label>
          </div>
          <label className="field"><span>Public key</span>
            <textarea value={pub} required rows={3} className="mono" placeholder={P.t6} onChange={(e) => setPub(e.target.value)} />
          </label>
          <p className="sub">{P.t7}<Mono>ssh-keygen -t ed25519</Mono>. Never paste a private key.</p>
          <div><button type="submit" className="btn btn-primary" disabled={busy || !pub.trim()}>{busy ? "Adding…" : "Add key"}</button></div>
        </form>
      </Sheet>

      <ErrorBox error={error} />
      <Sheet title="Your keys">
        {keys && keys.length === 0 && <Empty title="You have no SSH keys" />}
        {keys && keys.length > 0 && (
          <table className="table">
            <thead><tr><th>Name</th><th>Fingerprint</th><th>Expires</th><th>Last used</th><th><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id}>
                  <td>{k.name}<div className="faint small">{k.type}</div></td>
                  <td className="mono small">{k.fingerprint}</td>
                  <td>{k.expires_at ? (Date.parse(k.expires_at) <= now ? <span className="bad-ink">expired</span> : clock(Date.parse(k.expires_at))) : "never"}</td>
                  <td>{k.last_used_at ? <span title={clock(Date.parse(k.last_used_at))}>{ago(Date.parse(k.last_used_at), now)}<span className="faint"> from {k.last_used_from}</span></span> : "never"}</td>
                  <td className="td-actions"><button type="button" className="btn btn-danger-outline btn-sm" onClick={() => remove(k)}>Delete</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Sheet>

      <Sheet title="Verify this server" id="host-keys">
        {info && !info.enabled && <p className="sub pad">{P.t8}</p>}
        {info && info.enabled && (
          <>
            <p className="sub pad">The first time you connect to <Mono>{sshAddress(info)}</Mono>, <Mono>ssh</Mono> {P.t9}{info.certificates && P.t10}</p>
            <table className="table">
              <thead><tr><th>Type</th><th>Fingerprint (SHA256)</th></tr></thead>
              <tbody>{info.host_keys.map((h) => <tr key={h.algorithm}><td>{h.type}</td><td className="mono small">{h.fingerprint}</td></tr>)}</tbody>
            </table>
          </>
        )}
      </Sheet>
    </div>
  );
}
