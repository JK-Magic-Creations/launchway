import { useCallback, useEffect, useState } from "react";
import { configApi, type ConfigReportRow, type ConfigurationOverview } from "../configCodeApi";
import { ago } from "../format";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { ProjectHead } from "./PipelineChrome";
import "../config-code.css";
import { terms } from "../terms";

function ReportLine({ label, r }: { label: string; r?: ConfigReportRow }) {
  if (!r) return <div className="cc-row"><span className="label">{label}</span><span className="faint">none yet</span></div>;
  return (
    <div className="cc-row">
      <span className="label">{label}</span>
      <Link href={`/runs/${encodeURIComponent(r.run_id)}/configuration/${encodeURIComponent(r.step)}?kind=${r.kind}`}>{r.recap}</Link>
      <span className="faint small">{terms().say("{run} #")}{r.run_number ?? "?"}, {ago(r.created_at)}</span>
    </div>
  );
}

// ProjectConfigurationPage ties configuration as code together for one project: per Airfield the last apply, the
// drift found by the last check-mode run, the last plan, and the state workspaces Launchway keeps. Every line
// opens the report it comes from.
export function ProjectConfigurationPage({ name }: { name: string }) {
  const [data, setData] = useState<ConfigurationOverview | null>(null);
  const [error, setError] = useState<unknown>(null);
  const load = useCallback((bg: boolean) => { configApi.overview(name, bg).then((d) => { setData(d); setError(null); }).catch(setError); }, [name]);
  useEffect(() => { load(false); const t = setInterval(() => load(true), 10000); return () => clearInterval(t); }, [load]);
  return (
    <div className="page">
      <ProjectHead name={name} on="configuration" title={<>Configuration of {name}</>}
        sentence={<>What Ansible and OpenTofu or Terraform did to each Airfield, what has drifted since, and the state Launchway keeps for you.</>} />
      <ErrorBox error={error} />
      {!data && !error && <div className="loading">Loading…</div>}
      {data && data.environments.length === 0 && (
        <Empty title="No configuration runs yet">
          Add a step that uses <Mono>launchway/ansible-playbook</Mono> or <Mono>launchway/infrastructure</Mono>; its reports appear here once it has run.
        </Empty>
      )}
      {data && data.environments.length > 0 && (
        <div className="cc-grid">
          {data.environments.map((e) => (
            <section key={e.name || "none"} className="card cc-env" aria-label={e.name || "No Airfield named"}>
              <h2>{e.name || "No Airfield named"}</h2>
              <ReportLine label="Last apply" r={e.ansible} />
              <ReportLine label="Drift check" r={e.drift} />
              <ReportLine label="Last plan" r={e.plan} />
              {e.last_good && (
                <p className="faint small">Last known good: commit <Mono>{e.last_good.commit.slice(0, 8)}</Mono> (run #{e.last_good.run_number ?? "?"}{terms().say("). To go back, run the playbook of that commit again: configuration is {rollback:did} by applying the previous definition, not by time travel.")}</p>
              )}
            </section>
          ))}
        </div>
      )}
      {data && (
        <section className="card" aria-label="State">
          <h2>State</h2>
          {!data.sealed && <div className="callout callout-draft" role="status">This server has no root key, so it cannot keep Terraform or OpenTofu state: state holds secrets in clear text, and Launchway seals it. Create a root key first (<Mono>launchway encryption init</Mono>).</div>}
          {data.state.length === 0 ? <p className="faint">No state yet. The first apply of a plan step creates it, with nothing to set up.</p> : (
            <table className="cc-matrix">
              <caption className="sr-only">State workspaces</caption>
              <thead><tr><th scope="col">Workspace</th><th scope="col">Version</th><th scope="col">Serial</th><th scope="col">Lock</th></tr></thead>
              <tbody>
                {data.state.map((w) => (
                  <tr key={w.name}><th scope="row">{w.name}</th><td>{w.version}</td><td>{w.serial}</td><td>{w.lock ? `locked by ${w.lock.actor}` : "free"}</td></tr>
                ))}
              </tbody>
            </table>
          )}
          <p className="faint small">{terms().say("Versions, locks and {rollback}:")} <Mono>launchway state versions {name} WORKSPACE</Mono>.</p>
        </section>
      )}
    </div>
  );
}
