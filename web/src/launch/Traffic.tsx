import type { RunDetail } from "../api";
import { useRunVerifications, VerificationPanel } from "../components/Verification";
import { Link, Mono } from "../components/ui";
import { clock } from "../format";
import type { LMetric, TreeView } from "../launchApi";
import { HealthTag } from "./Tree";

function Proof({ m }: { m: LMetric }) {
  return (
    <div className="lw-proof">
      <h4 className="lw-proof-h"><Mono>{m.name}</Mono> <span className={`lw-ph lw-ph-${m.phase.toLowerCase()}`}>{m.phase === "Successful" ? "✓ passed so far" : m.phase === "Failed" ? "✕ failed" : m.phase}</span></h4>
      <p className="lw-small">
        {m.success && <>Passes when <Mono>{m.success}</Mono>. </>}{m.failure && <>Fails when <Mono>{m.failure}</Mono>. </>}
        {m.query && <>Measured with <Mono>{m.provider}</Mono>: <Mono>{m.query}</Mono></>}
      </p>
      {m.points && m.points.length > 0 ? (
        <table className="lw-table">
          <caption className="sr-only">Measurements of {m.name}, oldest first</caption>
          <thead><tr><th scope="col">When</th><th scope="col">Measured</th><th scope="col">Against the threshold</th></tr></thead>
          <tbody>
            {m.points.map((p, i) => (
              <tr key={i}><td>{p.at ? clock(p.at) : "-"}</td><td><Mono>{p.value || "-"}</Mono></td>
                <td><span className={`lw-ph lw-ph-${p.phase.toLowerCase()}`}>{p.phase === "Successful" ? "✓ pass" : p.phase === "Failed" ? "✕ fail" : p.phase}</span>{p.note ? <span className="faint"> · {p.note}</span> : null}</td></tr>
            ))}
          </tbody>
        </table>
      ) : <p className="faint">No measurement yet.</p>}
    </div>
  );
}

/** The Traffic and tests tab: where requests go, what was measured against what, and the test pods that send the requests. */
export function TrafficTab({ v, run, onOpen }: { v: TreeView; run: RunDetail; onOpen: (id: string) => void }) {
  const ver = useRunVerifications(run);
  const mine = (Array.isArray(ver.list) ? ver.list : []).filter((x) => x.deployment === v.deployment);
  const ro = v.tree.rollout;
  const routes = v.tree.nodes.filter((n) => n.weights && n.weights.length > 0 && n.change !== "leftover");
  const runs = ro?.analysis ?? [];
  const jobs = v.tree.nodes.filter((n) => n.kind === "Job" && n.change !== "leftover");
  const measured = runs.some((r) => r.metrics.some((m) => m.provider && m.provider !== "job"));
  return (
    <div className="lw-traffic">
      <section className="lw-card" aria-labelledby="lw-tr-h">
        <h2 id="lw-tr-h" className="lw-h">Where requests go now</h2>
        {ro?.pre_traffic && <p className="callout callout-ok"><strong>{ro.pre_traffic.live_weight}% of real traffic; testing through {ro.pre_traffic.address}.</strong></p>}
        {routes.length === 0 && ro?.services && Object.keys(ro.services).length > 0 && (
          <p>No route object splits traffic here: the services {Object.entries(ro.services).map(([k, s]) => `${k} → ${s}`).join(", ")} decide, by which ReplicaSet each selects.</p>
        )}
        {routes.map((r) => (
          <table className="lw-table" key={r.id}>
            <caption><button type="button" className="linkish" onClick={() => onOpen(r.id)}>{r.kind} {r.name}</button> {r.message ? <span className="faint">({r.message})</span> : null}</caption>
            <thead><tr><th scope="col">Backend</th><th scope="col">Weight</th><th scope="col">Only when</th></tr></thead>
            <tbody>{r.weights!.map((w, i) => <tr key={i}><th scope="row"><Mono>{w.backend}</Mono></th><td>{w.weight}</td><td>{w.match || "always"}</td></tr>)}</tbody>
          </table>
        ))}
        {ro && ro.actual_weight >= 0 && <p className="faint lw-small">Actual share to the new version: {ro.actual_weight}% ({ro.actual_source}).</p>}
      </section>

      <section className="lw-card" aria-labelledby="lw-meas-h">
        <h2 id="lw-meas-h" className="lw-h">Measured: analysis and tests</h2>
        {runs.length === 0 && <p className="faint">No analysis or test has run for this delivery{ro ? "" : " (it has no rollout)"}.</p>}
        {runs.map((a) => (
          <div key={a.name} className="lw-run">
            <h3 className="h-xs"><Mono>{a.name}</Mono> <span className={`lw-ph lw-ph-${a.phase.toLowerCase()}`}>{a.phase}</span></h3>
            {a.message && <p className="lw-small">{a.message}</p>}
            {a.metrics.map((m) => <Proof key={m.name} m={m} />)}
          </div>
        ))}
        {!measured && (
          <p className="callout callout-info" role="note">
            No traffic metrics are measured here yet: requests per second, error rate and latency for the stable and the new version come from a metrics source.
            To see them, add a verify step with metrics and name a health source (see <Link href="/learn">Learn</Link>); they are then drawn here with the query shown.
          </p>
        )}
      </section>

      {mine.length > 0 && (
        <section className="lw-card" aria-labelledby="lw-ver-h">
          <h2 id="lw-ver-h" className="lw-h">Verification of this Launch</h2>
          <p className="faint lw-small">What the verify steps measured: each metric against its threshold, and for a built-in health check every call the deploy runner made.</p>
          {mine.map((x) => {
            const step = run.step_list.find((st) => st.id === x.step);
            return step ? <VerificationPanel key={x.step} run={run} step={step} list={ver.list} reload={ver.reload} /> : null;
          })}
        </section>
      )}

      <section className="lw-card" aria-labelledby="lw-jobs-h">
        <h2 id="lw-jobs-h" className="lw-h">Test pods (push tests)</h2>
        {jobs.length === 0 ? <p className="faint">No test job is running or was kept.</p> : (
          <table className="lw-table">
            <caption className="sr-only">Test jobs of this delivery</caption>
            <thead><tr><th scope="col">Job</th><th scope="col">State</th><th scope="col">What it sent and got back</th></tr></thead>
            <tbody>
              {jobs.map((j) => (
                <tr key={j.id}><th scope="row"><button type="button" className="linkish" onClick={() => onOpen(j.id)}>{j.name}</button></th><td><HealthTag h={j.health} /></td>
                  <td>{v.can_logs ? <button type="button" className="btn btn-sm" onClick={() => onOpen("logs:job:" + j.id)}>Read its output</button> : <span className="faint">needs the operator role</span>}</td></tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
