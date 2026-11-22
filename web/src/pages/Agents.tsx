import { PageHead } from "../kit/layout";
import { roleName } from "../roles";
import { useEffect, useState } from "react";
import type { Role } from "../api";
import { useMe } from "../auth";
import { CopyBlock } from "../components/Tests";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { triggersApi, type TokenLimits } from "../triggers";
import "../triggers.css";
import { terms } from "../terms";
import { prose } from "../prose";
const P = await prose("pages.Agents");

const ROLES: Role[] = ["viewer", "operator", "admin"];
const PLACEHOLDER = "lw_PASTE_YOUR_TOKEN_HERE";

// What an agent can do over MCP, as the server lists it to a token that may
// change things. A viewer token sees only the first group.
const READ_TOOLS = [
  "list_projects", "get_project", "list_runs", "get_run", "wait_for_run", "get_step_log", "get_test_results",
  "get_security_findings", "list_artifacts", "read_artifact_file", "list_images", "get_deployments", "get_verification", "list_approvals",
  "validate_pipeline", "get_pipeline_reference", "list_templates", "get_step_resources", "list_caches", "get_cache_policy",
];
const CHANGE_TOOLS = ["start_run", "cancel_run", "retry_run", "decide_approval", "control_rollout", "mark_verification", "delete_cache"];

// AgentsPage shows how to connect an AI agent to this server over the Model
// Context Protocol: make a token for it, then paste the configuration for
// an agent that connects over HTTP, or one that starts a command.
export function AgentsPage() {
  const me = useMe();
  const [limits, setLimits] = useState<TokenLimits | null>(null);
  const [name, setName] = useState("coding agent");
  const [role, setRole] = useState<Role | "">("");
  const [projects, setProjects] = useState("");
  const [days, setDays] = useState(30);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [token, setToken] = useState<string | null>(null);
  const signIn = me?.auth_mode === "oidc";

  useEffect(() => {
    if (!signIn) return;
    triggersApi.tokenLimits().then((l) => { setLimits(l); setDays(Math.min(30, l.max_days)); }).catch(setError);
  }, [signIn]);

  if (!me) return null;
  const endpoint = location.origin + (me.mcp?.path ?? "/mcp");
  if (me.mcp && !me.mcp.enabled) {
    return (
      <div className="page">
        <PageHead eyebrow="Settings" title={<>Connect an agent</>} />
        <Empty title={P.t0}>{P.t1}<Mono>mcp.enabled: false</Mono>).</Empty>
      </div>
    );
  }

  const make = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const patterns = projects.split(",").map((s) => s.trim()).filter(Boolean);
      const t = await triggersApi.createToken({ name: name.trim(), expires_in_days: days, role: role || undefined, projects: patterns.length ? patterns : undefined });
      setToken(t.token);
    } catch (err) { setError(err); } finally { setBusy(false); }
  };

  const tok = token ?? PLACEHOLDER;
  const httpConfig = JSON.stringify({
    mcpServers: { launchway: signIn ? { type: "http", url: endpoint, headers: { Authorization: `Bearer ${tok}` } } : { type: "http", url: endpoint } },
  }, null, 2);
  const stdioEnv: Record<string, string> = { LAUNCHWAY_SERVER: location.origin };
  if (signIn) stdioEnv.LAUNCHWAY_TOKEN = tok;
  const stdioConfig = JSON.stringify({ mcpServers: { launchway: { command: "launchway", args: ["mcp"], env: stdioEnv } } }, null, 2);
  const check = [
    `curl -s ${endpoint} \\`,
    ...(signIn ? [`  -H 'Authorization: Bearer ${tok}' \\`] : []),
    P.t2,
    P.t3,
    P.t4,
  ].join("\n");
  const maxDays = limits?.max_days ?? 365;
  const roles = ROLES.slice(0, limits?.your_role ? ROLES.indexOf(limits.your_role) + 1 : 0);

  return (
    <div className="page agents">
      <PageHead eyebrow="Settings" title={<>Connect an agent</>} sentence={<>{terms().say(P.t5)} {terms().lower("logbook")} with the token's name.</>} />

      <section className="card agents-step" aria-labelledby="agents-1">
        <h2 id="agents-1" className="h-sm"><span className="agents-num" aria-hidden="true">1</span>Make a token for the agent</h2>
        {signIn ? (
          <form onSubmit={make} className="inputs-form" aria-label="Make a token for an agent">
            <p className="sub">{P.t6}<strong>viewer</strong> {P.t7}{terms().lower("clearance")} {P.t8}<Link href="/settings/access/keys">API tokens</Link> page.</p>
            <div className="tokenform-grid">
              <label className="field"><span>Name</span>
                <input value={name} maxLength={100} required onChange={(e) => setName(e.target.value)} /></label>
              <label className="field"><span>Role</span>
                <select value={role} onChange={(e) => setRole(e.target.value as Role | "")}>
                  <option value="">Same as mine{limits?.your_role ? ` (${limits.your_role})` : ""}</option>
                  {roles.map((r) => <option key={r} value={r}>{roleName(r)}</option>)}
                </select></label>
              <label className="field"><span>{terms().say("{Projects}")}</span>
                <input value={projects} placeholder={terms().say(P.t9)} onChange={(e) => setProjects(e.target.value)} /></label>
              <label className="field"><span>Expires after</span>
                <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
                  {[1, 7, 30, 90].filter((d) => d <= maxDays).map((d) => <option key={d} value={d}>{d} days</option>)}
                </select></label>
            </div>
            <div><button type="submit" className="btn btn-primary" disabled={busy || !name.trim()}>{busy ? "Making…" : "Make token"}</button></div>
            {token && (
              <div className="callout callout-ok" role="status">
                <strong>{P.t10}</strong>
              </div>
            )}
            <ErrorBox error={error} />
          </form>
        ) : (
          <p className="sub">{P.t11}</p>
        )}
      </section>

      <section className="card agents-step" aria-labelledby="agents-2">
        <h2 id="agents-2" className="h-sm"><span className="agents-num" aria-hidden="true">2</span>Add Launchway to the agent</h2>
        <p className="sub">{P.t12}<Mono>launchway</Mono> command on that machine).</p>
        <dl className="agents-facts">
          <dt>Address</dt><dd className="mono">{endpoint}</dd>
          {signIn && <><dt>Header</dt><dd className="mono">Authorization: Bearer {token ? "lw_…" : "<the token>"}</dd></>}
          <dt>Command</dt><dd className="mono">launchway mcp</dd>
        </dl>
        <div className="agents-configs">
          <CopyBlock label="Over HTTP" text={httpConfig} />
          <CopyBlock label="As a command" text={stdioConfig} />
        </div>
        {me.mcp?.oauth_resource && (
          <p className="sub">{P.t13}{" "}
            <Mono>{me.mcp.oauth_resource}</Mono>{P.t14}</p>
        )}
      </section>

      <section className="card agents-step" aria-labelledby="agents-3">
        <h2 id="agents-3" className="h-sm"><span className="agents-num" aria-hidden="true">3</span>Check that it answers</h2>
        <CopyBlock label="From a terminal" text={check} />
        <p className="sub">{terms().say(P.t15)}</p>
      </section>

      <section className="card agents-step" aria-labelledby="agents-4">
        <h2 id="agents-4" className="h-sm">What an agent can do</h2>
        <div className="agents-tools">
          <div>
            <h3 className="agents-h3">Read and diagnose</h3>
            <ul className="list-items">{READ_TOOLS.map((t) => <li key={t} className="list-item">{t}</li>)}</ul>
          </div>
          <div>
            <h3 className="agents-h3">Change things <span className="faint">(operator tokens only)</span></h3>
            <ul className="list-items">{CHANGE_TOOLS.map((t) => <li key={t} className="list-item">{t}</li>)}</ul>
          </div>
        </div>
        <p className="sub">{P.t16}{terms().pluralLower("clearance")} for people.</p>
      </section>
    </div>
  );
}
