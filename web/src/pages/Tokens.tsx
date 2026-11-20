import { useCallback, useEffect, useState } from "react";
import type { Role } from "../api";
import { ago, clock } from "../format";
import { useMe } from "../auth";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { triggersApi, type Token, type TokenLimits } from "../triggers";
import "../triggers.css";

const EXPIRY = [7, 30, 90, 180, 365, 730, 3650];
const ROLES: Role[] = ["viewer", "operator", "admin"];

// TokensPage lets people make API tokens for scripts and the command line,
// limited to a role and to projects, see when and from where each was last
// used, and revoke them. A token's value is shown once, when it is made.
// Admins also see and may revoke everyone's.
export function TokensPage() {
  const me = useMe();
  const [tokens, setTokens] = useState<Token[] | null>(null);
  const [all, setAll] = useState<Token[] | null>(null);
  const [limits, setLimits] = useState<TokenLimits | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [days, setDays] = useState(90);
  const [role, setRole] = useState<Role | "">("");
  const [projects, setProjects] = useState("");
  const [busy, setBusy] = useState(false);
  const [made, setMade] = useState<{ name: string; token: string } | null>(null);
  const [copied, setCopied] = useState(false);

  const load = useCallback(() => {
    triggersApi.tokens().then((t) => { setTokens(t); setError(null); }).catch(setError);
    if (me?.admin) triggersApi.allTokens().then(setAll).catch(setError);
  }, [me?.admin]);
  useEffect(() => {
    if (me?.auth_mode !== "oidc") return;
    load();
    triggersApi.tokenLimits().then((l) => { setLimits(l); setDays(l.default_days); }).catch(setError);
  }, [load, me?.auth_mode]);

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
      const patterns = projects.split(",").map((s) => s.trim()).filter(Boolean);
      const t = await triggersApi.createToken({ name: name.trim(), expires_in_days: days, role: role || undefined, projects: patterns.length ? patterns : undefined });
      setMade({ name: t.name, token: t.token });
      setName(""); setProjects(""); setRole("");
      load();
    } catch (err) { setError(err); } finally { setBusy(false); }
  };

  const revoke = async (t: Token, anyone: boolean) => {
    if (!window.confirm(`Revoke the token "${t.name}"${anyone && t.user ? ` of ${t.user}` : ""}? Anything using it stops working at once.`)) return;
    setError(null);
    try { await (anyone ? triggersApi.revokeAnyToken(t.id) : triggersApi.revokeToken(t.id)); load(); } catch (err) { setError(err); }
  };

  const copy = async () => {
    if (!made) return;
    try { await navigator.clipboard.writeText(made.token); setCopied(true); } catch { setCopied(false); }
  };

  const maxDays = limits?.max_days ?? 365;
  const roles = ROLES.slice(0, limits?.your_role ? ROLES.indexOf(limits.your_role) + 1 : 0);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>API tokens</h1>
          <p className="sub">For scripts, the <Mono>launchway</Mono> command line and AI agents (see <Link href="/agents">Connect an agent</Link>).
            A token acts as you, with the rights you have when it is used, never more than the role and projects it is limited to, until it
            expires or is revoked.</p>
        </div>
      </header>

      <section className="card">
        <form onSubmit={create} className="inputs-form" aria-label="Make a token">
          <div className="tokenform-grid">
            <label className="field">
              <span>Name</span>
              <input value={name} maxLength={100} required placeholder="for example: release script" onChange={(e) => setName(e.target.value)} />
            </label>
            <label className="field">
              <span>Expires after</span>
              <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
                {EXPIRY.filter((d) => d <= maxDays).map((d) => <option key={d} value={d}>{d} days</option>)}
                {!EXPIRY.includes(maxDays) && <option value={maxDays}>{maxDays} days</option>}
              </select>
            </label>
            <label className="field">
              <span>Role</span>
              <select value={role} onChange={(e) => setRole(e.target.value as Role | "")}>
                <option value="">Same as mine{limits?.your_role ? ` (${limits.your_role})` : ""}</option>
                {roles.map((r) => <option key={r} value={r}>{r}</option>)}
              </select>
            </label>
            <label className="field">
              <span>Projects</span>
              <input value={projects} placeholder="every project, or: pay-*, web" onChange={(e) => setProjects(e.target.value)} />
            </label>
          </div>
          <p className="sub">Every token expires, after at most {maxDays} days on this server. The role can be no higher than your own.</p>
          <div><button type="submit" className="btn btn-primary" disabled={busy || !name.trim()}>{busy ? "Making…" : "Make token"}</button></div>
        </form>
      </section>

      {made && (
        <div className="callout callout-ok tokenmade" role="status">
          <strong>Your new token "{made.name}". Copy it now: it will not be shown again.</strong>
          <div className="tokenvalue">
            <input readOnly value={made.token} aria-label="The new token" onFocus={(e) => e.currentTarget.select()} className="mono" />
            <button type="button" className="btn" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
          </div>
          <span>On the command line: <Mono>{`launchway login --server ${location.origin}`}</Mono> and paste it. In scripts, send it as <Mono>Authorization: Bearer …</Mono>.</span>
          <button type="button" className="btn" onClick={() => setMade(null)}>Done</button>
        </div>
      )}

      <ErrorBox error={error} />
      {tokens && tokens.length === 0 && <Empty title="You have no tokens" />}
      {tokens && tokens.length > 0 && <TokenTable tokens={tokens} onRevoke={(t) => revoke(t, false)} />}
      {me.admin && all && (
        <>
          <h2 className="h-sm" style={{ marginTop: 24 }}>Everyone's tokens</h2>
          <p className="sub">As an admin of every project you can revoke any token, for example one that leaked or whose owner left.</p>
          {all.length === 0 ? <Empty title="Nobody has a token" /> : <TokenTable tokens={all} owner onRevoke={(t) => revoke(t, true)} />}
        </>
      )}
    </div>
  );
}

function TokenTable({ tokens, owner, onRevoke }: { tokens: Token[]; owner?: boolean; onRevoke: (t: Token) => void }) {
  const now = Date.now();
  return (
    <div className="card card-flush">
      <table className="table">
        <thead><tr>{owner && <th>Owner</th>}<th>Name</th><th>Limited to</th><th>Made</th><th>Expires</th><th>Last used</th><th><span className="visually-hidden">Actions</span></th></tr></thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id}>
              {owner && <td>{t.user}</td>}
              <td>{t.name}<div className="faint small mono">{t.id}</div></td>
              <td className="token-scope">{t.role ?? "owner's role"}{" · "}{t.projects?.length ? t.projects.join(", ") : "every project"}</td>
              <td title={clock(t.created_at)}>{ago(t.created_at, now)}</td>
              <td>{t.expires_at <= now ? <span className="bad-ink">expired</span> : clock(t.expires_at)}</td>
              <td>{t.last_used ? <span title={clock(t.last_used)}>{ago(t.last_used, now)}{t.last_used_from && <span className="faint"> from {t.last_used_from}</span>}</span> : "never"}</td>
              <td className="td-actions"><button type="button" className="btn btn-danger-outline btn-sm" onClick={() => onRevoke(t)}>Revoke</button></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
