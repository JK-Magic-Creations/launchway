import { useEffect, useState } from "react";
import { api, type ArtifactStore, type Runner } from "../api";
import { ago } from "../format";
import { useNow } from "../live";
import { Empty, ErrorBox, Mono } from "../components/ui";
import { bytes } from "../components/Security";
import { HealthSourcesSection, ModelsSection } from "../components/Explain";

export function RunnersPage() {
  const [runners, setRunners] = useState<Runner[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const now = useNow(2000);
  useEffect(() => {
    const load = (background: boolean) => api.runners(background).then(setRunners).catch(setError);
    load(false);
    const t = setInterval(() => load(true), 3000);
    return () => clearInterval(t);
  }, []);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Runners</h1>
          <p className="sub">Machines that run steps. A runner only ever connects out to the server, so it can sit in any network.</p>
        </div>
      </header>
      <ErrorBox error={error} />
      {runners && runners.length === 0 && <Empty title="No runners have connected yet" />}
      {runners && runners.length > 0 && (
        <div className="card card-flush">
          <table className="table">
            <thead><tr><th>Runner</th><th>Executor</th><th>Version</th><th>Last seen</th></tr></thead>
            <tbody>
              {runners.map((r) => {
                const online = r.online;
                return (
                  <tr key={r.id}>
                    <td><span className={"online " + (online ? "online-on" : "")} aria-label={online ? "online" : "offline"} />{r.name}<div className="faint small mono">{r.id}</div></td>
                    <td>
                      {r.executor === "deploy" ? "deploy runner" : r.executor}
                      {r.images && <div className="faint small">builds images</div>}
                      {(r.pool || (r.targets?.length ?? 0) > 0) && (
                        <div className="faint small">
                          {r.pool && <>pool <Mono>{r.pool}</Mono></>}{r.pool && r.targets?.length ? ", " : ""}
                          {r.targets?.length ? <>serves {r.targets.map((t, i) => <span key={t}>{i > 0 && ", "}<Mono>{t}</Mono></span>)}</> : null}
                        </div>
                      )}
                    </td>
                    <td><Mono>{r.version}</Mono></td>
                    <td>{online ? "connected" : `offline, last seen ${ago(r.last_seen, now)}`}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <section className="card">
        <h2 className="h-sm">Add a runner on another machine</h2>
        <p className="sub">Copy the <Mono>launchway</Mono> binary to the machine, then run it with this server's address and the token in <Mono>launchway-data/runner-token</Mono>:</p>
        <pre className="code">{`launchway runner --server ${location.origin} --token <token> --name build-2`}</pre>
      </section>
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
