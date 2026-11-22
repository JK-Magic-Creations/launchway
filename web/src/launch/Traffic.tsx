import type { RunDetail } from "../api";
import { useRunVerifications, VerificationPanel } from "../components/Verification";
import { Link, Mono } from "../components/ui";
import { clock } from "../format";
import type { LMetric, TreeView } from "../launchApi";
import { HealthTag } from "./Tree";
import { terms } from "../terms";
import { prose } from "../prose";
import { BtnSm, SpanFaint, ThCol, ThRow } from "../kit/bits";
const P = await prose("launch.Traffic");

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
          <caption className="visually-hidden">Measurements of {m.name}, oldest first</caption>
          <thead><tr><ThCol>When</ThCol><ThCol>Measured</ThCol><ThCol>Against the threshold</ThCol></tr></thead>
          <tbody>
            {m.points.map((p, i) => (
              <tr key={i}><td>{p.at ? clock(p.at) : "-"}</td><td><Mono>{p.value || "-"}</Mono></td>
                <td><span className={`lw-ph lw-ph-${p.phase.toLowerCase()}`}>{p.phase === "Successful" ? "✓ pass" : p.phase === "Failed" ? "✕ fail" : p.phase}</span>{p.note ? <SpanFaint> · {p.note}</SpanFaint> : null}</td></tr>
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
        {ro?.pre_traffic && <p className="callout callout-ok"><strong>{ro.pre_traffic.live_weight}{P.t0}{ro.pre_traffic.address}.</strong></p>}
        {routes.length === 0 && ro?.services && Object.keys(ro.services).length > 0 && (
          <p>{P.t1}{Object.entries(ro.services).map(([k, s]) => `${k} → ${s}`).join(", ")} {P.t2}</p>
        )}
        {routes.map((r) => (
          <table className="lw-table" key={r.id}>
            <caption><button type="button" className="linkish" onClick={() => onOpen(r.id)}>{r.kind} {r.name}</button> {r.message ? <SpanFaint>({r.message})</SpanFaint> : null}</caption>
            <thead><tr><ThCol>Backend</ThCol><ThCol>Weight</ThCol><ThCol>Only when</ThCol></tr></thead>
            <tbody>{r.weights!.map((w, i) => <tr key={i}><ThRow><Mono>{w.backend}</Mono></ThRow><td>{w.weight}</td><td>{w.match || "always"}</td></tr>)}</tbody>
          </table>
        ))}
        {ro && ro.actual_weight >= 0 && <p className="faint lw-small">{P.t3}{ro.actual_weight}% ({ro.actual_source}).</p>}
      </section>

      <section className="lw-card" aria-labelledby="lw-meas-h">
        <h2 id="lw-meas-h" className="lw-h">Measured: analysis and tests</h2>
        {runs.length === 0 && <p className="faint">{P.t4}{ro ? "" : " (it has no rollout)"}.</p>}
        {runs.map((a) => (
          <div key={a.name} className="lw-arun">
            <h3 className="h-xs"><Mono>{a.name}</Mono> <span className={`lw-ph lw-ph-${a.phase.toLowerCase()}`}>{a.phase}</span></h3>
            {a.message && <p className="lw-small">{a.message}</p>}
            {a.metrics.map((m) => <Proof key={m.name} m={m} />)}
          </div>
        ))}
        {!measured && (
          <p className="callout callout-info" role="note">
            {P.t5}<Link href="/learn">{terms().say("{Learn}")}</Link>{P.t6}</p>
        )}
      </section>

      {mine.length > 0 && (
        <section className="lw-card" aria-labelledby="lw-ver-h">
          <h2 id="lw-ver-h" className="lw-h">Verification of this Launch</h2>
          <p className="faint lw-small">{terms().say(P.t7)}</p>
          {mine.map((x) => {
            const step = run.step_list.find((st) => st.id === x.step);
            return step ? <VerificationPanel key={x.step} run={run} step={step} list={ver.list} reload={ver.reload} /> : null;
          })}
        </section>
      )}

      <section className="lw-card" aria-labelledby="lw-jobs-h">
        <h2 id="lw-jobs-h" className="lw-h">Test pods (push tests)</h2>
        {jobs.length === 0 ? <p className="faint">{P.t8}</p> : (
          <table className="lw-table">
            <caption className="visually-hidden">Test jobs of this delivery</caption>
            <thead><tr><ThCol>Job</ThCol><ThCol>State</ThCol><ThCol>What it sent and got back</ThCol></tr></thead>
            <tbody>
              {jobs.map((j) => (
                <tr key={j.id}><ThRow><button type="button" className="linkish" onClick={() => onOpen(j.id)}>{j.name}</button></ThRow><td><HealthTag h={j.health} /></td>
                  <td>{v.can_logs ? <BtnSm onClick={() => onOpen("logs:job:" + j.id)}>Read its output</BtnSm> : <SpanFaint>needs the operator role</SpanFaint>}</td></tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
