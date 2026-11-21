import { useEffect, useMemo, useState } from "react";
import { configApi, type AnsibleDocument, type AnsibleResult, type ConfigReport, type PlanDocument } from "../configCodeApi";
import { useMe, canOperate } from "../auth";
import { PageHead } from "../kit/layout";
import { ErrorBox, Link, Mono } from "../components/ui";
import "../config-code.css";

const STATUSES = ["failed", "unreachable", "changed", "ok", "skipped"] as const;
const WORST: Record<string, number> = { failed: 0, unreachable: 1, changed: 2, ok: 3, skipped: 4 };

function Cell({ status }: { status: string }) { return <span className={`cc-cell cc-${status}`}>{status}</span>; }

function worst(rs: AnsibleResult[]): string {
  return rs.reduce((w, r) => (WORST[r.status] < WORST[w] ? r.status : w), "skipped");
}

function AnsibleView({ rep, run }: { rep: ConfigReport<AnsibleDocument>; run: string }) {
  const me = useMe();
  const doc = rep.document;
  const [host, setHost] = useState("");
  const [status, setStatus] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState("");
  const failedHosts = rep.summary.failed_hosts ?? [];
  const hosts = useMemo(() => doc.hosts.filter((h) => (!host || h.host === host) && (!status || h.outcome === status)), [doc, host, status]);
  const mayRun = canOperate(me, rep.report.project) && failedHosts.length > 0 && !doc.check;

  const rerun = async () => {
    setBusy(true); setError(null);
    try {
      const r = await configApi.rerunFailed(run, rep.report.step);
      setSaid(`Started run #${r.run.number} on ${r.hosts.join(", ")}.`);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const changedList = doc.hosts.flatMap((h) => (h.changed_tasks ?? []).map((t) => ({ host: h.host, task: t })));
  return (
    <>
      <p className="lead" role="status">{doc.check ? "Check mode: nothing was changed. " : ""}{doc.summary.recap}</p>
      <ErrorBox error={error} />
      {said && <div className="callout callout-info" role="status">{said}</div>}
      {doc.failure && (
        <div className="callout callout-bad" role="alert">
          <strong>{doc.failure.status === "unreachable" ? "Could not reach" : "Failed"} {doc.failure.host}</strong> at the task <Mono>{doc.failure.task}</Mono>
          {doc.failure.play ? <> in <Mono>{doc.failure.play}</Mono></> : null}
          {doc.failure.msg && <pre className="code">{doc.failure.msg}</pre>}
        </div>
      )}
      {rep.summary.drift && (
        <div className={rep.summary.drift.clean ? "callout callout-info" : "callout callout-draft"} role="status">
          <strong>{rep.summary.drift.text}</strong>
          {rep.summary.drift.items.map((i) => <div key={i.host}><Mono>{i.host}</Mono>: {i.tasks.join(", ")}</div>)}
        </div>
      )}
      {mayRun && (
        <p><button type="button" className="btn" disabled={busy} onClick={rerun}>
          Run again on the {failedHosts.length === 1 ? "failed host" : `${failedHosts.length} failed hosts`} ({failedHosts.join(", ")})
        </button></p>
      )}
      <div className="cc-filters">
        <label>Host
          <select value={host} onChange={(e) => setHost(e.target.value)}>
            <option value="">All hosts</option>
            {doc.hosts.map((h) => <option key={h.host} value={h.host}>{h.host}</option>)}
          </select>
        </label>
        <label>Outcome
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">Any outcome</option>
            {["failed", "unreachable", "changed", "ok"].map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>
      </div>
      <section className="card" aria-label="Hosts by plays">
        <table className="cc-matrix">
          <caption className="sr-only">Hosts by plays: the worst outcome of each host in each play</caption>
          <thead>
            <tr><th scope="col">Host</th><th scope="col">Outcome</th>{doc.plays.map((p, i) => <th key={i} scope="col">{p.name || `Play ${i + 1}`}</th>)}</tr>
          </thead>
          <tbody>
            {hosts.length === 0 && <tr><td colSpan={doc.plays.length + 2} className="faint">No host matches.</td></tr>}
            {hosts.map((h) => (
              <tr key={h.host}>
                <th scope="row">{h.host}</th>
                <td><Cell status={h.outcome} /></td>
                {doc.plays.map((p, i) => {
                  const rs = p.tasks.map((t) => t.results[h.host]).filter(Boolean);
                  return <td key={i}>{rs.length ? <Cell status={worst(rs)} /> : <span className="faint">—</span>}</td>;
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </section>
      <section className="card" aria-label="What changed">
        <h2>{doc.check ? "What would change" : "What changed"}</h2>
        {changedList.length === 0 ? <p className="faint">Nothing {doc.check ? "would change" : "changed"}.</p> : (
          <ul>{changedList.filter((c) => !host || c.host === host).map((c, i) => <li key={i}><Mono>{c.host}</Mono>: {c.task}</li>)}</ul>
        )}
        {doc.plays.flatMap((p) => p.tasks.flatMap((t) => Object.entries(t.results)
          .filter(([h, r]) => (r.diff?.length ?? 0) > 0 && (!host || h === host) && (!status || r.status === status))
          .map(([h, r]) => (
            <details key={`${t.name}-${h}`}>
              <summary>{t.name} on {h}</summary>
              {r.diff!.map((d, i) => <pre key={i} className="cc-diff" tabIndex={0}>{d.text}</pre>)}
            </details>
          ))))}
        <p className="faint small">Values of tasks marked <Mono>no_log</Mono> are never recorded, and credential-like text is removed before the report is stored.</p>
      </section>
      <section className="card" aria-label="Failed and unreachable tasks">
        {STATUSES.slice(0, 2).map((s) => doc.plays.flatMap((p) => p.tasks.flatMap((t) => Object.entries(t.results).filter(([h, r]) => r.status === s && (!host || h === host))
          .map(([h, r]) => <div key={`${s}-${t.name}-${h}`} className="cc-row"><Cell status={s} /><Mono>{h}</Mono><span>{t.name}</span>{r.msg && <span className="faint">{r.msg}</span>}</div>))))}
      </section>
    </>
  );
}

function PlanView({ rep }: { rep: ConfigReport<PlanDocument> }) {
  const { plan, meta } = rep.document;
  return (
    <>
      <p className="lead" role="status">{plan.summary}</p>
      {meta?.state_backend === "none" && <div className="callout callout-draft" role="status">This plan was made without state, so every resource looks new. Pull request runs get no state credential: treat it as a check that the code is valid.</div>}
      <div className={`callout cc-risk-${plan.risk.level}`} role="status">
        <strong>Risk: {plan.risk.level}</strong>
        {(plan.risk.notes ?? []).map((n, i) => <div key={i}>{n}</div>)}
        {(plan.risk.destroyed ?? []).map((a) => <div key={a}>Destroyed: <Mono>{a}</Mono></div>)}
        {(plan.risk.replaced ?? []).map((a) => <div key={a}>Replaced: <Mono>{a}</Mono></div>)}
      </div>
      <section className="card" aria-label="Resources">
        <table className="cc-matrix">
          <caption className="sr-only">Resources, destroys and replacements first</caption>
          <thead><tr><th scope="col">Resource</th><th scope="col">Action</th><th scope="col">Changes</th></tr></thead>
          <tbody>
            {plan.resources.length === 0 && <tr><td colSpan={3} className="faint">No resource changes.</td></tr>}
            {plan.resources.map((r) => (
              <tr key={r.address}>
                <th scope="row"><Mono>{r.address}</Mono></th>
                <td><span className={`cc-cell ${r.action === "delete" || r.action === "replace" ? "cc-failed" : r.action === "update" ? "cc-changed" : "cc-ok"}`}>{r.action}</span></td>
                <td>
                  {(r.attrs ?? []).map((a) => (
                    <div key={a.path}><Mono>{a.path}</Mono>: {a.sensitive ? <em>(sensitive, hidden)</em> : <>{a.before ? <del>{a.before}</del> : null} {a.after ? <ins>{a.after}</ins> : null}</>}{a.forces_replacement ? " (forces replacement)" : ""}</div>
                  ))}
                  {r.attrs_cut && <span className="faint">More attributes differ than are listed.</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </>
  );
}

// ConfigReportPage shows what a configuration step found: for Ansible the hosts by plays, what changed, the failed
// task first and the button to run again on the failed hosts; for a plan the resources to add, change, destroy and
// replace with the risk first. The server wrote every value after removing credentials.
export function ConfigReportPage({ run, step }: { run: string; step: string }) {
  const [rep, setRep] = useState<ConfigReport | null>(null);
  const [error, setError] = useState<unknown>(null);
  const kind = new URLSearchParams(window.location.search).get("kind") ?? undefined;
  useEffect(() => { configApi.report(run, step, kind).then(setRep).catch(setError); }, [run, step, kind]);
  const project = rep?.report.project;
  return (
    <div className="page">
      <PageHead eyebrow={<>{project ? <Link href={`/projects/${encodeURIComponent(project)}/configuration`}>{project}</Link> : "Configuration"} · <Link href={`/runs/${encodeURIComponent(run)}`}>run {rep?.report.run_number ?? ""}</Link></>}
        title={<>{rep?.report.kind === "plan" ? "Plan" : "Ansible run"}: {step}</>}
        sentence={rep ? <>{rep.report.environment ? <>Airfield <Mono>{rep.report.environment}</Mono>. </> : null}Commit <Mono>{rep.report.commit.slice(0, 8)}</Mono>.</> : undefined} />
      <ErrorBox error={error} />
      {!rep && !error && <div className="loading">Loading…</div>}
      {rep?.report.status === "unreadable" && <div className="callout callout-bad" role="alert">The report could not be read: {rep.report.recap}</div>}
      {rep && rep.report.kind === "ansible" && rep.report.status !== "unreadable" && <AnsibleView rep={rep as ConfigReport<AnsibleDocument>} run={run} />}
      {rep && rep.report.kind === "plan" && rep.report.status !== "unreadable" && <PlanView rep={rep as ConfigReport<PlanDocument>} />}
    </div>
  );
}
