import { useCallback, useEffect, useState } from "react";
import { clock } from "../format";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { PageHead, Sheet } from "../kit/layout";
import { signingApi, type SigningKey } from "../signingApi";
import { useHosting } from "./SshKeys";

/** Signing keys: the SSH keys a person signs commits and tags with, so that what they sign shows as verified. */
export function SigningKeysPage() {
  const hosting = useHosting();
  const [keys, setKeys] = useState<SigningKey[] | null>(null);
  const [inst, setInst] = useState<{ public_key: string; fingerprint: string; address: string } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [pub, setPub] = useState("");
  const [expires, setExpires] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    signingApi.keys().then((k) => { setKeys(k); setError(null); }).catch(setError);
    signingApi.installationKey().then(setInst).catch(() => setInst(null));
  }, []);
  useEffect(() => { if (hosting === "on") load(); }, [hosting, load]);
  if (hosting === null) return null;
  if (hosting === "off") {
    return (
      <div className="page">
        <PageHead eyebrow="Settings" title="Signing keys" />
        <Empty title="Git hosting is off on this server">Verified commits are for the repositories Launchway hosts. An administrator turns hosting on with the installation setting <Mono>forge.hosting</Mono>.</Empty>
      </div>
    );
  }
  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      await signingApi.addKey({ name: name.trim(), public_key: pub.trim(), expires_at: expires ? new Date(expires + "T23:59:59Z").toISOString() : undefined });
      setName(""); setPub(""); setExpires(""); load();
    } catch (err) { setError(err); } finally { setBusy(false); }
  };
  const revoke = async (k: SigningKey) => {
    if (!window.confirm(`Revoke the key "${k.name}" from now on? Commits dated after this show as signed by a revoked key; earlier ones stay verified.`)) return;
    try { await signingApi.revokeKey(k.id); load(); } catch (err) { setError(err); }
  };
  const remove = async (k: SigningKey) => {
    if (!window.confirm(`Delete the key "${k.name}"? Everything it signed shows as signed by an unknown key.`)) return;
    try { await signingApi.deleteKey(k.id); load(); } catch (err) { setError(err); }
  };
  return (
    <div className="page">
      <PageHead eyebrow="Settings" title="Signing keys"
        sentence={<>Register the SSH key you sign commits and tags with, and what it signs shows as <strong>Verified</strong>. These are not the keys you push with: those are your <Link href="/settings/access/ssh-keys">SSH keys</Link>.</>} />
      <Sheet title="Add a signing key" pad>
        <form onSubmit={add} className="inputs-form" aria-label="Add a signing key">
          <div className="tokenform-grid">
            <label className="field"><span>Name</span><input value={name} maxLength={100} placeholder="for example: work laptop" onChange={(e) => setName(e.target.value)} /></label>
            <label className="field"><span>Stops counting on (optional)</span><input type="date" value={expires} onChange={(e) => setExpires(e.target.value)} /></label>
          </div>
          <label className="field"><span>Public key</span>
            <textarea value={pub} required rows={3} className="mono" placeholder="ssh-ed25519 AAAA… (the line in your .pub file)" onChange={(e) => setPub(e.target.value)} /></label>
          <p className="sub">Then tell Git to sign with it: <Mono>git config gpg.format ssh</Mono>, <Mono>git config user.signingkey ~/.ssh/id_ed25519.pub</Mono>, <Mono>git config commit.gpgsign true</Mono>. OpenPGP keys cannot be used yet.</p>
          <div><button type="submit" className="btn btn-primary" disabled={busy || !pub.trim()}>{busy ? "Adding…" : "Add key"}</button></div>
        </form>
      </Sheet>
      <ErrorBox error={error} />
      <Sheet title="Your signing keys">
        {keys && keys.length === 0 && <Empty title="You have no signing keys" />}
        {keys && keys.length > 0 && (
          <table className="table">
            <thead><tr><th>Name</th><th>Fingerprint</th><th>Status</th><th><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id}>
                  <td>{k.name}<div className="faint small">{k.type}</div></td>
                  <td className="mono small">{k.fingerprint}</td>
                  <td>{k.revoked_at ? <span className="bad-ink">revoked {clock(Date.parse(k.revoked_at))}</span> : k.expires_at ? `until ${clock(Date.parse(k.expires_at))}` : "active"}</td>
                  <td className="td-actions">
                    {!k.revoked_at && <button type="button" className="btn btn-sm" onClick={() => revoke(k)}>Revoke<span className="visually-hidden"> {k.name}</span></button>}{" "}
                    <button type="button" className="btn btn-danger-outline btn-sm" onClick={() => remove(k)}>Delete<span className="visually-hidden"> {k.name}</span></button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Sheet>
      {inst && (
        <Sheet title="The installation's own key" pad>
          <p className="sub">Launchway signs the merge and suggestion commits it makes with this key, so a rule that asks for verified commits does not stop its own work. It is published at <Mono>{inst.address}</Mono>.</p>
          <p className="mono small">{inst.fingerprint}</p>
        </Sheet>
      )}
    </div>
  );
}
