import { useCallback, useEffect, useState } from "react";
import { api, type ApiToken } from "../api";
import { ago, clock } from "../format";
import { useMe } from "../auth";
import { Empty, ErrorBox, Mono } from "../components/ui";

const EXPIRY = [7, 30, 90, 180, 365];

// TokensPage lets people make API tokens for scripts, see them, and revoke
// them. A token's value is shown once, when it is made.
export function TokensPage() {
  const me = useMe();
  const [tokens, setTokens] = useState<ApiToken[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [days, setDays] = useState(90);
  const [busy, setBusy] = useState(false);
  const [made, setMade] = useState<{ name: string; token: string } | null>(null);
  const [copied, setCopied] = useState(false);

  const load = useCallback(() => { api.tokens().then((t) => { setTokens(t); setError(null); }).catch(setError); }, []);
  useEffect(() => { if (me?.auth_mode === "oidc") load(); }, [load, me?.auth_mode]);

  if (me?.auth_mode !== "oidc") {
    return (
      <div className="page">
        <header className="page-head"><div><h1>API tokens</h1></div></header>
        <Empty title="API tokens need sign-in">This server has no sign-in, so there is nobody to make a token for.</Empty>
      </div>
    );
  }

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null); setCopied(false);
    try {
      const t = await api.createToken(name.trim(), days);
      setMade({ name: t.name, token: t.token });
      setName("");
      load();
    } catch (err) { setError(err); } finally { setBusy(false); }
  };

  const revoke = async (t: ApiToken) => {
    if (!window.confirm(`Revoke the token "${t.name}"? Anything using it stops working at once.`)) return;
    setError(null);
    try { await api.revokeToken(t.id); load(); } catch (err) { setError(err); }
  };

  const copy = async () => {
    if (!made) return;
    try { await navigator.clipboard.writeText(made.token); setCopied(true); } catch { setCopied(false); }
  };

  const now = Date.now();
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>API tokens</h1>
          <p className="sub">For scripts and other tools. A token acts as you, with the rights you have when it is used, until it expires or you revoke it.</p>
        </div>
      </header>

      <section className="card">
        <form className="tokenform" onSubmit={create}>
          <label className="field">
            <span>Name</span>
            <input value={name} maxLength={100} required placeholder="for example: release script" onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="field">
            <span>Expires after</span>
            <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {EXPIRY.map((d) => <option key={d} value={d}>{d} days</option>)}
            </select>
          </label>
          <button type="submit" className="btn btn-primary" disabled={busy || !name.trim()}>{busy ? "Making…" : "Make token"}</button>
        </form>
      </section>

      {made && (
        <div className="callout callout-ok tokenmade" role="status">
          <strong>Your new token "{made.name}". Copy it now: it will not be shown again.</strong>
          <div className="tokenvalue">
            <input readOnly value={made.token} aria-label="The new token" onFocus={(e) => e.currentTarget.select()} className="mono" />
            <button type="button" className="btn" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
          </div>
          <span>Use it as <Mono>Authorization: Bearer …</Mono>, for example <Mono>{`curl -H "Authorization: Bearer $TOKEN" ${location.origin}/api/v1/runs`}</Mono></span>
          <button type="button" className="btn" onClick={() => setMade(null)}>Done</button>
        </div>
      )}

      <ErrorBox error={error} />
      {tokens && tokens.length === 0 && <Empty title="You have no tokens" />}
      {tokens && tokens.length > 0 && (
        <div className="card card-flush">
          <table className="table">
            <thead><tr><th>Name</th><th>Made</th><th>Expires</th><th>Last used</th><th><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {tokens.map((t) => (
                <tr key={t.id}>
                  <td>{t.name}<div className="faint small mono">{t.id}</div></td>
                  <td title={clock(t.created_at)}>{ago(t.created_at, now)}</td>
                  <td>{t.expires_at <= now ? <span className="bad-ink">expired</span> : clock(t.expires_at)}</td>
                  <td>{t.last_used ? <span title={clock(t.last_used)}>{ago(t.last_used, now)}</span> : "never"}</td>
                  <td className="td-actions"><button type="button" className="btn btn-danger-outline btn-sm" onClick={() => revoke(t)}>Revoke</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
