import { PageHead } from "../kit/layout";
import { roleName } from "../roles";
import { useCallback, useEffect, useState } from "react";
import type { Role } from "../api";
import { ago, clock } from "../format";
import { useMe } from "../auth";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { triggersApi, type NewToken, type Token, type TokenLimits } from "../triggers";
import { forgeApi } from "../forgeApi";
import { ServiceAccountsSection } from "./ServiceAccounts";
import { parseRepositories, parseRanges } from "./tokenLimits";
import "../triggers.css";
import { terms } from "../terms";
import { prose } from "../prose";
import { BtnPrimary, DivFaintSmall, DivPage, LabelField, PSub, SpanFaint, SpanHidden } from "../kit/bits";
const P = await prose("pages.Tokens");

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
  const [repos, setRepos] = useState("");
  const [access, setAccess] = useState<"read" | "write">("read");
  const [ranges, setRanges] = useState("");
  const [choices, setChoices] = useState<string[]>([]);
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
    forgeApi.repositories().then((r) => setChoices(r.map((x) => x.path))).catch(() => setChoices([]));
    triggersApi.tokenLimits().then((l) => { setLimits(l); setDays(l.default_days); }).catch(setError);
  }, [load, me?.auth_mode]);

  if (me?.auth_mode !== "oidc") {
    return (
      <DivPage>
        <PageHead eyebrow="Settings" title={<>API tokens</>} />
        <Empty title="API tokens need sign-in">{P.t0}</Empty>
      </DivPage>
    );
  }

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null); setCopied(false);
    try {
      const patterns = projects.split(",").map((s) => s.trim()).filter(Boolean);
      const body: NewToken = { name: name.trim(), expires_in_days: days, role: role || undefined, projects: patterns.length ? patterns : undefined };
      if (repoList.length) { body.repositories = repoList; body.repo_access = access; }
      if (rangeList.length) body.source_ranges = rangeList;
      const t = await triggersApi.createToken(body);
      setMade({ name: t.name, token: t.token });
      setName(""); setProjects(""); setRole(""); setRepos(""); setRanges("");
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

  const repoParsed = parseRepositories(repos);
  const rangeParsed = parseRanges(ranges);
  const repoList = repoParsed.ok ? repoParsed.values : [];
  const rangeList = rangeParsed.ok ? rangeParsed.values : [];
  const formProblem = (!repoParsed.ok && repoParsed.problem) || (!rangeParsed.ok && rangeParsed.problem) ||
    (repoList.length > 0 && projects.trim() ? P.t1 : "");
  const maxDays = limits?.max_days ?? 365;
  const roles = ROLES.slice(0, limits?.your_role ? ROLES.indexOf(limits.your_role) + 1 : 0);
  return (
    <DivPage>
      <PageHead eyebrow="Settings" title={<>API tokens</>} sentence={<>For scripts, the <Mono>launchway</Mono> {P.t2}<Link href="/settings/access/agents">Connect an agent</Link>{terms().say(P.t3)}</>} />

      <section className="card">
        <form onSubmit={create} className="inputs-form" aria-label="Make a token">
          <div className="tokenform-grid">
            <LabelField>
              <span>Name</span>
              <input value={name} maxLength={100} required placeholder="for example: release script" onChange={(e) => setName(e.target.value)} />
            </LabelField>
            <LabelField>
              <span>Expires after</span>
              <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
                {EXPIRY.filter((d) => d <= maxDays).map((d) => <option key={d} value={d}>{d} days</option>)}
                {!EXPIRY.includes(maxDays) && <option value={maxDays}>{maxDays} days</option>}
              </select>
            </LabelField>
            <LabelField>
              <span>Role</span>
              <select value={role} onChange={(e) => setRole(e.target.value as Role | "")}>
                <option value="">Same as mine{limits?.your_role ? ` (${limits.your_role})` : ""}</option>
                {roles.map((r) => <option key={r} value={r}>{roleName(r)}</option>)}
              </select>
            </LabelField>
            <LabelField>
              <span>{terms().say("{Projects}")}</span>
              <input value={projects} placeholder={terms().say(P.t4)} onChange={(e) => setProjects(e.target.value)} />
            </LabelField>
            <LabelField>
              <span>Repositories</span>
              <input value={repos} list="token-repos" placeholder="none, or: acme/shop, acme/*" aria-describedby="token-repos-help" onChange={(e) => setRepos(e.target.value)} />
              <datalist id="token-repos">{choices.map((c) => <option key={c} value={c} />)}</datalist>
            </LabelField>
            <LabelField>
              <span>Access to them</span>
              <select value={access} disabled={!repoList.length} onChange={(e) => setAccess(e.target.value as "read" | "write")}>
                <option value="read">Read only</option>
                <option value="write">Read and push</option>
              </select>
            </LabelField>
            <LabelField>
              <span>Only from addresses</span>
              <input value={ranges} placeholder="anywhere, or: 203.0.113.0/24" onChange={(e) => setRanges(e.target.value)} />
            </LabelField>
          </div>
          <PSub id="token-repos-help">{P.t5}<Mono>owner/name</Mono> or <Mono>owner/*</Mono>{P.t6}</PSub>
          {formProblem && <p className="bad-ink" role="alert">{formProblem}</p>}
          <PSub>{P.t7}{maxDays} {P.t8}</PSub>
          <div><BtnPrimary disabled={busy || !name.trim() || !!formProblem}>{busy ? "Making…" : "Make token"}</BtnPrimary></div>
        </form>
      </section>

      {made && (
        <div className="callout callout-ok tokenmade" role="status">
          <strong>Your new token "{made.name}{P.t9}</strong>
          <div className="tokenvalue">
            <input readOnly value={made.token} aria-label="The new token" onFocus={(e) => e.currentTarget.select()} className="mono" />
            <button type="button" className="btn" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
          </div>
          <span>On the command line: <Mono>{`launchway login --server ${location.origin}`}</Mono> {P.t10}<Mono>Authorization: Bearer …</Mono>.</span>
          <button type="button" className="btn" onClick={() => setMade(null)}>Done</button>
        </div>
      )}

      <ErrorBox error={error} />
      {tokens && tokens.length === 0 && <Empty title="You have no tokens" />}
      {tokens && tokens.length > 0 && <TokenTable tokens={tokens} onRevoke={(t) => revoke(t, false)} />}
      {me.admin && <ServiceAccountsSection />}
      {me.admin && all && (
        <>
          <h2 className="h-sm" style={{ marginTop: 24 }}>Everyone's tokens</h2>
          <PSub>{terms().say(P.t11)}</PSub>
          {all.length === 0 ? <Empty title="Nobody has a token" /> : <TokenTable tokens={all} owner onRevoke={(t) => revoke(t, true)} />}
        </>
      )}
    </DivPage>
  );
}

function TokenTable({ tokens, owner, onRevoke }: { tokens: Token[]; owner?: boolean; onRevoke: (t: Token) => void }) {
  const now = Date.now();
  return (
    <div className="card card-flush">
      <table className="table">
        <thead><tr>{owner && <th>{terms().say("{Owner}")}</th>}<th>Name</th><th>Limited to</th><th>Made</th><th>Expires</th><th>Last used</th><th><SpanHidden>Actions</SpanHidden></th></tr></thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id}>
              {owner && <td>{t.user}</td>}
              <td>{t.name}<div className="faint small mono">{t.id}</div></td>
              <td className="token-scope">{t.role ? roleName(t.role) : terms().say("{owner}'s role")}{" · "}{t.repo_access ? <>{t.repo_access === "write" ? "read and push" : "read only"}: {t.repositories?.length ? t.repositories.map((r) => r.pattern).join(", ") : "no repository left"}</> : t.projects?.length ? t.projects.join(", ") : terms().say("every {project}")}{t.source_ranges?.length ? <DivFaintSmall>only from {t.source_ranges.join(", ")}</DivFaintSmall> : null}</td>
              <td title={clock(t.created_at)}>{ago(t.created_at, now)}</td>
              <td>{t.expires_at <= now ? <span className="bad-ink">expired</span> : clock(t.expires_at)}</td>
              <td>{t.last_used ? <span title={clock(t.last_used)}>{ago(t.last_used, now)}{t.last_used_from && <SpanFaint> from {t.last_used_from}</SpanFaint>}</span> : "never"}</td>
              <td className="td-actions"><button type="button" className="btn btn-danger-outline btn-sm" onClick={() => onRevoke(t)}>Revoke</button></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
