import { useCallback, useEffect, useState } from "react";
import type { Role } from "../api";
import { roleName } from "../roles";
import { ErrorBox, Mono } from "../components/ui";
import { triggersApi, type NewToken, type ServiceAccount } from "../triggers";
import { terms } from "../terms";
import { parseRanges, parseRepositories } from "./tokenLimits";

const ROLES: Role[] = ["viewer", "operator", "admin"];

/**
 * Service accounts: non-person accounts that own tokens for automation, so that none is made from
 * a person's account. An administrator makes the account, then a token for it (shown once).
 */
export function ServiceAccountsSection() {
  const [list, setList] = useState<ServiceAccount[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [description, setDescription] = useState("");
  const [forName, setForName] = useState<string | null>(null);
  const [tokenName, setTokenName] = useState("");
  const [repos, setRepos] = useState("");
  const [access, setAccess] = useState<"read" | "write">("read");
  const [ranges, setRanges] = useState("");
  const [made, setMade] = useState<{ account: string; token: string } | null>(null);

  const load = useCallback(() => { triggersApi.serviceAccounts().then((l) => { setList(l); setError(null); }).catch(setError); }, []);
  useEffect(load, [load]);

  const run = async (f: () => Promise<unknown>) => { setError(null); try { await f(); load(); } catch (e) { setError(e); } };
  const repoParsed = parseRepositories(repos);
  const rangeParsed = parseRanges(ranges);
  const problem = (!repoParsed.ok && repoParsed.problem) || (!rangeParsed.ok && rangeParsed.problem) || "";

  const makeToken = (e: React.FormEvent) => {
    e.preventDefault();
    if (!forName || !repoParsed.ok || !rangeParsed.ok) return;
    const body: NewToken = { name: tokenName.trim(), expires_in_days: 90 };
    if (repoParsed.values.length) { body.repositories = repoParsed.values; body.repo_access = access; }
    if (rangeParsed.values.length) body.source_ranges = rangeParsed.values;
    void run(async () => {
      const t = await triggersApi.createServiceToken(forName, body);
      setMade({ account: forName, token: t.token });
      setTokenName(""); setRepos(""); setRanges(""); setForName(null);
    });
  };

  return (
    <section aria-labelledby="service-accounts-h">
      <h2 className="h-sm" id="service-accounts-h" style={{ marginTop: 24 }}>Service accounts</h2>
      <p className="sub">An account with no password and no sign-in that owns the tokens of one automation. {terms().say("Its tokens never have more than its role, and the {logbook} names the token on every request.")}</p>
      <ErrorBox error={error} />
      <form className="inputs-form" aria-label="Make a service account" onSubmit={(e) => { e.preventDefault(); void run(async () => { await triggersApi.createServiceAccount({ name: name.trim(), role, description: description.trim() }); setName(""); setDescription(""); }); }}>
        <div className="tokenform-grid">
          <label className="field"><span>Name</span><input value={name} required placeholder="for example: gitops-controller" onChange={(e) => setName(e.target.value)} /></label>
          <label className="field"><span>Role</span>
            <select value={role} onChange={(e) => setRole(e.target.value as Role)}>{ROLES.map((r) => <option key={r} value={r}>{roleName(r)}</option>)}</select>
          </label>
          <label className="field"><span>What it is for</span><input value={description} maxLength={300} onChange={(e) => setDescription(e.target.value)} /></label>
        </div>
        <div><button type="submit" className="btn btn-primary" disabled={!name.trim()}>Make service account</button></div>
      </form>
      {made && (
        <div className="callout callout-ok tokenmade" role="status">
          <strong>The token for {made.account}. Copy it now: it will not be shown again.</strong>
          <div className="tokenvalue"><input readOnly value={made.token} aria-label="The new token" onFocus={(e) => e.currentTarget.select()} className="mono" /></div>
          <button type="button" className="btn" onClick={() => setMade(null)}>Done</button>
        </div>
      )}
      {forName && (
        <form className="inputs-form" aria-label={`Make a token for ${forName}`} onSubmit={makeToken}>
          <div className="tokenform-grid">
            <label className="field"><span>Token name</span><input value={tokenName} required onChange={(e) => setTokenName(e.target.value)} /></label>
            <label className="field"><span>Repositories</span><input value={repos} placeholder="none, or: acme/shop, acme/*" onChange={(e) => setRepos(e.target.value)} /></label>
            <label className="field"><span>Access to them</span>
              <select value={access} disabled={!repoParsed.ok || !repoParsed.values.length} onChange={(e) => setAccess(e.target.value as "read" | "write")}>
                <option value="read">Read only</option><option value="write">Read and push</option>
              </select>
            </label>
            <label className="field"><span>Only from addresses</span><input value={ranges} placeholder="anywhere, or: 203.0.113.0/24" onChange={(e) => setRanges(e.target.value)} /></label>
          </div>
          {problem && <p className="bad-ink" role="alert">{problem}</p>}
          <div><button type="submit" className="btn btn-primary" disabled={!tokenName.trim() || !!problem}>Make token for {forName}</button>{" "}
            <button type="button" className="btn" onClick={() => setForName(null)}>Cancel</button></div>
        </form>
      )}
      {list && list.length === 0 && <p className="sub">No service accounts yet. On the command line: <Mono>launchway service-accounts create NAME</Mono>.</p>}
      {list && list.length > 0 && (
        <div className="card card-flush">
          <table className="table">
            <thead><tr><th>Name</th><th>Role</th><th>State</th><th><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {list.map((a) => (
                <tr key={a.name}>
                  <td>{a.name}{a.description && <div className="faint small">{a.description}</div>}</td>
                  <td>{roleName(a.role)}</td>
                  <td>{a.disabled ? "switched off" : "on"}</td>
                  <td className="td-actions">
                    <button type="button" className="btn btn-sm" disabled={a.disabled} onClick={() => setForName(a.name)}>Make token</button>{" "}
                    <button type="button" className="btn btn-sm" onClick={() => void run(() => triggersApi.updateServiceAccount(a.name, { disabled: !a.disabled }))}>{a.disabled ? "Switch on" : "Switch off"}</button>{" "}
                    <button type="button" className="btn btn-danger-outline btn-sm" onClick={() => { if (window.confirm(`Delete ${a.name} and every token it owns? Anything using them stops working at once.`)) void run(() => triggersApi.deleteServiceAccount(a.name)); }}>Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
