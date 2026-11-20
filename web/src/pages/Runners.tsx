import { useCallback, useEffect, useState } from "react";
import { api, type ArtifactStore, type JoinToken, type Runner, type RunnerAccept, type RunnerPool, type RunnerPools } from "../api";
import { ago, clock } from "../format";
import { useNow } from "../live";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { bytes } from "../components/Security";
import { HealthSourcesSection, ModelsSection } from "../components/Explain";
import "../runners.css";

// INSTALLER_URL is where the one-line installer for a runner on a plain
// machine is served. The packaging work provides the script; until it is
// served by the server, this is the address it will have.
// TODO(packaging): keep in step with the installer's real path.
export const INSTALLER_URL = `${location.origin}/install/runner.sh`;

// RUNNER_CHART is the Helm chart runners in Kubernetes are installed with.
// TODO(packaging): the runner-only chart, once it is published.
export const RUNNER_CHART = "oci://ghcr.io/jk-magic-creations/charts/launchway";

const TTLS: [string, string][] = [["1h", "1 hour"], ["24h", "1 day"], ["168h", "7 days"]];

// RunnersPage shows the runner pools, every runner with how it proves who
// it is, what it accepts and what it is running, and lets those who manage
// runners make join tokens (with the commands to use them), revoke join
// tokens, and revoke or rotate enrolled runners.
export function RunnersPage() {
  const [runners, setRunners] = useState<Runner[] | null>(null);
  const [pools, setPools] = useState<RunnerPools | null>(null);
  const [tokens, setTokens] = useState<JoinToken[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const now = useNow(2000);
  const manage = pools?.manage ?? false;

  const load = useCallback((background: boolean) => {
    api.runners(background).then(setRunners).catch(setError);
    api.runnerPools(background).then((p) => {
      setPools(p);
      if (p.manage && p.identities) api.joinTokens(background).then(setTokens).catch(setError);
    }).catch(setError);
  }, []);
  useEffect(() => {
    load(false);
    const t = setInterval(() => load(true), 3000);
    return () => clearInterval(t);
  }, [load]);

  const act = async (what: () => Promise<unknown>) => {
    setError(null);
    try { await what(); load(true); } catch (err) { setError(err); }
  };
  const revoke = (r: Runner) => {
    const reason = window.prompt(`Revoke runner ${r.name} (${r.id})? It is refused at once, everywhere; the steps it holds run again elsewhere.\n\nWhy? (kept in the audit trail)`);
    if (reason === null) return;
    act(() => api.revokeRunner(r.id, reason));
  };
  const rotate = (r: Runner) => act(() => api.rotateRunner(r.id));

  const legacy = (runners ?? []).filter((r) => r.online && (r.auth === "shared" || r.auth === "pool"));
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Runners</h1>
          <p className="sub">Machines that run steps. A runner only ever connects out to the server, so it can sit in any network.
            Each belongs to a pool, which decides which projects' work it may take.</p>
        </div>
      </header>
      <ErrorBox error={error} />

      {legacy.length > 0 && (
        <div className="callout callout-draft" role="status">
          <strong>{legacy.length === 1 ? "1 runner uses" : `${legacy.length} runners use`} a shared token, which gives no per-runner identity.</strong>{" "}
          The server cannot tell runners with the same token apart, so it trusts what each says about itself, and a leaked token lets
          anyone take that pool's work. Enrol them with a join token instead{manage ? " (below)" : ""}, then set <Mono>require_identity</Mono> on the pool.
        </div>
      )}

      {pools && <PoolsCard pools={pools.pools} runners={runners ?? []} />}

      <h2 className="h-sm rn-h">Runners</h2>
      {runners && runners.length === 0 && <Empty title="No runners have connected yet" />}
      {runners && runners.length > 0 && (
        <div className="card card-flush table-scroll rn-scroll">
          <table className="table rn-table">
            <thead><tr><th>Runner</th><th>Pool</th><th>Identity</th><th>Accepts</th><th>Running</th><th>Last seen</th>{manage && <th><span className="visually-hidden">Actions</span></th>}</tr></thead>
            <tbody>
              {runners.map((r) => {
                const revoked = !!r.identity?.revoked_at;
                return (
                  <tr key={r.id} className={revoked ? "rn-revoked" : ""}>
                    <td>
                      <span className={"online " + (r.online ? "online-on" : "")} aria-label={r.online ? "online" : "offline"} />{r.name}
                      <div className="faint small mono">{r.id}</div>
                      <div className="faint small">
                        {r.executor === "deploy" ? "deploy runner" : r.executor}{r.version && <> · <Mono>{r.version}</Mono></>}
                        {r.os && <> · {r.os}/{r.arch}</>}{r.images && <> · builds images</>}
                      </div>
                      {(r.labels?.length ?? 0) > 0 && <div className="rn-labels">{r.labels!.map((l) => <span key={l} className="rn-label">{l}</span>)}</div>}
                    </td>
                    <td>
                      <Mono>{r.pool || "default"}</Mono>
                      {r.targets?.length ? <div className="faint small">serves {r.targets.join(", ")}</div> : null}
                    </td>
                    <td><IdentityCell r={r} now={now} /></td>
                    <td className="rn-accept">{acceptSummary(r.accept)}</td>
                    <td>
                      {(r.work?.length ?? 0) === 0 ? <span className="faint">nothing</span> : r.work!.map((w) => (
                        <div key={w.run + w.step}><Link href={`/runs/${w.run}`}>{w.project} #{w.number}</Link> <span className="faint">{w.step}</span></div>
                      ))}
                    </td>
                    <td>{revoked ? "revoked" : r.online ? "connected" : `offline, last seen ${ago(r.last_seen, now)}`}</td>
                    {manage && (
                      <td className="td-actions">
                        {r.identity && !revoked && (
                          <div className="rn-actions">
                            <button type="button" className="btn btn-sm" onClick={() => rotate(r)} disabled={!!r.identity.rotate_asked}
                              title="Ask the runner to make a new key at its next request">{r.identity.rotate_asked ? "Rotation asked" : "Rotate key"}</button>
                            <button type="button" className="btn btn-danger-outline btn-sm" onClick={() => revoke(r)}>Revoke</button>
                          </div>
                        )}
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {pools && manage && pools.identities && <AddRunner pools={pools.pools} onMade={() => load(true)} onError={setError} />}
      {pools && manage && pools.identities && tokens && (
        <JoinTokens tokens={tokens} now={now} onRevoke={(t) => {
          if (window.confirm(`Revoke join token ${t.id}? No more runners can enrol with it; those it enrolled keep working.`)) act(() => api.revokeJoinToken(t.id));
        }} />
      )}
      {pools && !manage && (
        <section className="card">
          <h2 className="h-sm">Add a runner on another machine</h2>
          <p className="sub">Someone who manages runners (the role <Mono>runner_admin</Mono> names, admin by default, on every project)
            makes a join token for a pool here. With it, on the machine:</p>
          <pre className="code">{`launchway runner --server ${location.origin} --join <join token>`}</pre>
        </section>
      )}
      <StoreCard />
      <ModelsSection />
      <HealthSourcesSection />
    </div>
  );
}

const STORE_NAMES: Record<string, string> = { database: "the database", filesystem: "the filesystem", s3: "S3" };

// StoreCard says where this server keeps artifacts (and the SBOMs and
// provenance kept as artifacts), how full the store is, and what to do if
// some are kept where this server cannot read them.
function StoreCard() {
  const [st, setSt] = useState<ArtifactStore | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    const load = (background: boolean) => api.artifactStore(background).then((s) => { setSt(s); setError(null); }).catch(setError);
    load(false);
    const t = setInterval(() => load(true), 30000);
    return () => clearInterval(t);
  }, []);
  if (error) return <section className="card"><h2 className="h-sm">Artifact storage</h2><ErrorBox error={error} /></section>;
  if (!st) return null;
  const u = st.usage;
  const share = u && u.limit_bytes > 0 ? Math.min(1, u.used_bytes / u.limit_bytes) : 0;
  return (
    <section className="card store" aria-label="Artifact storage">
      <div className="card-head">
        <h2 className="h-sm">Artifact storage</h2>
        {st.available
          ? <span className="pill pill-ok">Kept in {STORE_NAMES[st.store] ?? st.store}</span>
          : <span className="pill pill-bad">Not kept on this server</span>}
      </div>
      {!st.available && st.problem && <div className="callout callout-bad">{st.problem}</div>}
      {st.available && st.why && <p className="sub">{st.why}</p>}
      {st.available && st.store === "database" && !st.why && (
        <p className="sub">Artifacts, SBOMs and provenance are kept in the database, shared by every server. Nothing else to run.</p>
      )}
      {u && (
        <div className="store-usage">
          <div className="store-meter" role="meter" aria-label="Store used" aria-valuemin={0} aria-valuemax={u.limit_bytes} aria-valuenow={u.used_bytes}>
            <span className={share >= 0.8 ? "store-meter-fill store-meter-high" : "store-meter-fill"} style={{ width: `${Math.max(share * 100, u.used_bytes > 0 ? 1 : 0)}%` }} />
          </div>
          <div className="store-figures">
            <span><b>{bytes(u.used_bytes)}</b> of {bytes(u.limit_bytes)} used</span>
            <span className="faint">{u.objects} {u.objects === 1 ? "object" : "objects"}</span>
            {u.open_uploads > 0 && <span className="faint">{u.open_uploads} uploading</span>}
            {u.collect_bytes > 0 && <span className="faint">{bytes(u.collect_bytes)} being freed</span>}
            <span className="faint">files up to {bytes(u.max_object_bytes)}</span>
          </div>
        </div>
      )}
      {!u && st.available && (
        <p className="sub">{st.kept.files} {st.kept.files === 1 ? "file" : "files"} kept, {bytes(st.kept.bytes)}.</p>
      )}
      {st.advice.map((a) => <div key={a} className="callout callout-wait store-advice">{a}</div>)}
    </section>
  );
}

function IdentityCell({ r, now }: { r: Runner; now: number }) {
  const id = r.identity;
  if (id?.revoked_at) {
    return (
      <div>
        <span className="pill pill-bad">revoked</span>
        <div className="faint small">by {id.revoked_by || "?"} {ago(id.revoked_at, now)}{id.revoke_reason && <>: {id.revoke_reason}</>}</div>
      </div>
    );
  }
  if (id) {
    return (
      <div>
        <span className="pill pill-ok">enrolled</span>{id.ephemeral && <span className="faint small"> ephemeral</span>}
        <div className="faint small mono" title="The fingerprint of the runner's public key">{id.key_fingerprint}</div>
        <div className="faint small" title={clock(id.key_created_at)}>key made {ago(id.key_created_at, now)}; enrolled {ago(id.enrolled_at, now)}{id.enrolled_by && <> with a token from {id.enrolled_by}</>}</div>
      </div>
    );
  }
  return (
    <div>
      <span className="pill pill-warn">{r.auth === "pool" ? "pool token" : "shared token"}</span>
      <div className="faint small">no identity of its own</div>
    </div>
  );
}

function acceptSummary(a?: RunnerAccept) {
  if (!a) return <span className="faint">anything its pool serves</span>;
  const parts: string[] = [];
  const list = (name: string, l?: string[]) => { if (l?.length) parts.push(`${name} ${l.join(", ")}`); };
  list("projects", a.projects);
  list("repositories", a.repositories);
  list("images", a.images);
  if (a.images_by_digest) parts.push("images by digest only");
  list("executors", a.executors);
  list("environments", a.environments);
  if (a.pull_requests === false) parts.push("no pull requests");
  if (a.drafts === false) parts.push("no drafts");
  if (a.max_timeout) parts.push(`steps up to ${a.max_timeout}`);
  list("never sets", a.env_deny);
  return <ul className="rn-accept-list">{parts.map((p) => <li key={p}>{p}</li>)}</ul>;
}

function PoolsCard({ pools, runners }: { pools: RunnerPool[]; runners: Runner[] }) {
  return (
    <>
      <h2 className="h-sm rn-h">Pools</h2>
      <div className="card card-flush table-scroll rn-scroll">
        <table className="table">
          <thead><tr><th>Pool</th><th>Serves</th><th>Runners may use</th><th>Online</th></tr></thead>
          <tbody>
            {pools.map((p) => {
              const online = runners.filter((r) => r.online && (r.pool || "default") === p.name && !r.identity?.revoked_at).length;
              return (
                <tr key={p.name}>
                  <td><Mono>{p.name}</Mono><div className="faint small">{p.kind === "build" ? "build" : "deploy"} pool{!p.configured && ", not configured"}</div></td>
                  <td>
                    {p.kind === "deploy" ? <>targets {p.targets.join(", ")}</>
                      : p.projects.length ? <>projects {p.projects.join(", ")}</>
                      : <>every project no other pool serves</>}
                    {p.events.length > 0 && <div className="faint small">only runs started by {p.events.join(", ")}</div>}
                  </td>
                  <td>
                    {p.require_identity ? <span className="pill pill-ok">enrolled runners only</span>
                      : p.shared_token ? <><span className="pill pill-warn">enrolled or shared token</span><div className="faint small">set require_identity to refuse the shared token</div></>
                      : "enrolled runners"}
                  </td>
                  <td>{online}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}

function AddRunner({ pools, onMade, onError }: { pools: RunnerPool[]; onMade: () => void; onError: (e: unknown) => void }) {
  const [pool, setPool] = useState(pools.find((p) => p.kind === "build")?.name ?? "default");
  const [uses, setUses] = useState(1);
  const [ttl, setTTL] = useState("1h");
  const [labels, setLabels] = useState("");
  const [ephemeral, setEphemeral] = useState(false);
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [made, setMade] = useState<{ token: string; t: JoinToken } | null>(null);
  const [tab, setTab] = useState<"vm" | "container" | "kubernetes">("vm");
  const [copied, setCopied] = useState("");

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); onError(null); setCopied("");
    try {
      const l = labels.split(",").map((s) => s.trim()).filter(Boolean);
      const out = await api.createJoinToken({ pool, max_uses: uses, ttl, ephemeral, description: description.trim() || undefined, labels: l.length ? l : undefined });
      setMade({ token: out.token, t: out.join_token });
      onMade();
    } catch (err) { onError(err); } finally { setBusy(false); }
  };
  const copy = async (what: string, text: string) => {
    try { await navigator.clipboard.writeText(text); setCopied(what); } catch { setCopied(""); }
  };

  const p = pools.find((x) => x.name === pool);
  const origin = location.origin;
  const commands = made ? installCommands(origin, made.token, made.t, p) : null;
  return (
    <section className="card rn-add">
      <h2 className="h-sm">Add a runner</h2>
      <p className="sub">Make a join token for a pool. A runner enrols with it once: it makes a key that never leaves it, and gets an
        identity of its own that can be revoked on its own. The token is shown once, and stored only as a hash.</p>
      <form onSubmit={create} className="inputs-form" aria-label="Make a join token">
        <div className="rn-form">
          <label className="field">
            <span>Pool</span>
            <select value={pool} onChange={(e) => setPool(e.target.value)}>
              {pools.map((x) => <option key={x.name} value={x.name}>{x.name} ({x.kind})</option>)}
            </select>
          </label>
          <label className="field">
            <span>Runners it may enrol</span>
            <input type="number" min={1} max={10000} value={uses} onChange={(e) => setUses(Math.max(1, Number(e.target.value) || 1))} />
          </label>
          <label className="field">
            <span>Valid for</span>
            <select value={ttl} onChange={(e) => setTTL(e.target.value)}>{TTLS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select>
          </label>
          <label className="field">
            <span>Labels</span>
            <input value={labels} placeholder="for example: arm64, gpu" onChange={(e) => setLabels(e.target.value)} />
          </label>
          <label className="field">
            <span>Description</span>
            <input value={description} maxLength={200} placeholder="what or where it is for" onChange={(e) => setDescription(e.target.value)} />
          </label>
        </div>
        <label className="rn-check">
          <input type="checkbox" checked={ephemeral} onChange={(e) => setEphemeral(e.target.checked)} />
          <span>Ephemeral: each runner is revoked when it leaves (for pods that enrol when they start)</span>
        </label>
        <div><button type="submit" className="btn btn-primary" disabled={busy}>{busy ? "Making…" : "Make join token"}</button></div>
      </form>

      {made && commands && (
        <div className="callout callout-ok rn-made" role="status">
          <strong>Join token for pool {made.t.pool}: {made.t.max_uses === 1 ? "one runner" : `${made.t.max_uses} runners`}, until {clock(made.t.expires_at)}. Copy it now: it will not be shown again.</strong>
          <div className="tokenvalue">
            <input readOnly value={made.token} aria-label="The join token" onFocus={(e) => e.currentTarget.select()} className="mono" />
            <button type="button" className="btn" onClick={() => copy("token", made.token)}>{copied === "token" ? "Copied" : "Copy"}</button>
          </div>
          <div className="tabs" role="tablist">
            {([["vm", "A machine"], ["container", "A container"], ["kubernetes", "Kubernetes"]] as const).map(([k, l]) => (
              <button key={k} type="button" role="tab" aria-selected={tab === k} className={"tab" + (tab === k ? " tab-on" : "")} onClick={() => setTab(k)}>{l}</button>
            ))}
          </div>
          {commands[tab].map((c) => (
            <div key={c.title} className="rn-cmd">
              <div className="rn-cmd-head"><span>{c.title}</span>
                <button type="button" className="btn btn-sm" onClick={() => copy(c.title, c.text)}>{copied === c.title ? "Copied" : "Copy"}</button></div>
              <pre className="code">{c.text}</pre>
            </div>
          ))}
          <button type="button" className="btn" onClick={() => setMade(null)}>Done</button>
        </div>
      )}
    </section>
  );
}

type Cmd = { title: string; text: string };

function installCommands(origin: string, token: string, t: JoinToken, p?: RunnerPool): Record<"vm" | "container" | "kubernetes", Cmd[]> {
  const labels = t.labels.length ? `\nlabels: [${t.labels.join(", ")}]` : "";
  const projects = p && p.kind === "build" && p.projects.length ? p.projects : [];
  const acceptYaml = `accept:${projects.length ? `\n  projects: [${projects.map((x) => JSON.stringify(x)).join(", ")}]` : "\n  # projects: [shop, \"web-*\"]"}
  # images: ["registry.example.com/*"]
  pull_requests: false
  drafts: false
  max_timeout: 2h
  env_deny: [LD_PRELOAD, BASH_ENV, ENV, "GIT_*"]`;
  return {
    vm: [
      { title: "Install and enrol (one line)", text: `curl -fsSL ${INSTALLER_URL} | sudo sh -s -- --server ${origin} --join ${token}` },
      { title: "Or, with the launchway binary already on the machine", text: `launchway runner --server ${origin} --join ${token} --config /etc/launchway/runner.yaml` },
      { title: "/etc/launchway/runner.yaml: the runner's own limits (optional)", text: `server: ${origin}\ndata: /var/lib/launchway-runner${labels}\n${acceptYaml}` },
    ],
    container: [
      { title: "Run the runner in a container", text: `podman run -d --name launchway-runner -v lw-runner:/var/lib/launchway-runner \\\n  -e LAUNCHWAY_JOIN=${token} ghcr.io/jk-magic-creations/launchway \\\n  runner --server ${origin} --data /var/lib/launchway-runner --executor host` },
    ],
    kubernetes: [
      { title: "The join token, as a Secret", text: `kubectl -n launchway create secret generic launchway-runner-join --from-literal=token=${token}` },
      {
        title: "Helm values (runner section of the Launchway chart)",
        text: `runner:\n  enabled: true\n  serverURL: ${origin}\n  join: { existingSecret: launchway-runner-join }${t.labels.length ? `\n  labels: [${t.labels.join(", ")}]` : ""}${projects.length ? `\n  accept: { projects: [${projects.map((x) => JSON.stringify(x)).join(", ")}], pull_requests: false }` : ""}`,
      },
      { title: "Install", text: `helm upgrade --install launchway-runners ${RUNNER_CHART} -n launchway -f runner-values.yaml` },
      ...(t.ephemeral || t.max_uses > 1 ? [] : [{ title: "Note", text: "Each pod enrols when it starts: make the token ephemeral, with enough uses for every pod start." }]),
    ],
  };
}

function JoinTokens({ tokens, now, onRevoke }: { tokens: JoinToken[]; now: number; onRevoke: (t: JoinToken) => void }) {
  if (tokens.length === 0) return null;
  return (
    <>
      <h2 className="h-sm rn-h">Join tokens</h2>
      <div className="card card-flush table-scroll rn-scroll">
        <table className="table">
          <thead><tr><th>Token</th><th>Pool</th><th>Used</th><th>State</th><th>Made</th><th><span className="visually-hidden">Actions</span></th></tr></thead>
          <tbody>
            {tokens.map((t) => (
              <tr key={t.id}>
                <td><span className="mono small">{t.id}</span>{t.description && <div className="faint small">{t.description}</div>}
                  {t.labels.length > 0 && <div className="rn-labels">{t.labels.map((l) => <span key={l} className="rn-label">{l}</span>)}</div>}</td>
                <td><Mono>{t.pool}</Mono>{t.ephemeral && <div className="faint small">ephemeral</div>}</td>
                <td>{t.uses} of {t.max_uses}</td>
                <td>
                  <span className={"pill " + (t.state === "active" ? "pill-ok" : t.state === "revoked" ? "pill-bad" : "pill-idle")}>{t.state}</span>
                  <div className="faint small">{t.state === "active" ? `until ${clock(t.expires_at)}` : t.revoked_at ? `by ${t.revoked_by}` : ""}</div>
                </td>
                <td title={clock(t.created_at)}>{ago(t.created_at, now)} by {t.created_by}</td>
                <td className="td-actions">{t.state === "active" && <button type="button" className="btn btn-danger-outline btn-sm" onClick={() => onRevoke(t)}>Revoke</button>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
