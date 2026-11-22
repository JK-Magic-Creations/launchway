import { useState } from "react";
import type { DeploymentView, RunDetail } from "../api";
import { DeploymentControls } from "../components/DeploymentControls";
import { Mono } from "../components/ui";
import type { LNode, LRollout, TreeView } from "../launchApi";
import { cause, span } from "./model";
import { EmergencyReturn, WorkloadControls } from "./Controls";
import { HealthTag } from "./Tree";
import { terms } from "../terms";
import { prose } from "../prose";
const P = await prose("launch.Overview");

/** What happens next, and who may step in, in a sentence. Zero touch is the default: this says so. */
export function nextUp(ro: LRollout | undefined, now: number, over: boolean): string {
  const going = !!ro && ro.phase !== "Healthy" && ro.strategy !== "rolling";
  if (over && !going) return P.t0;
  if (over) return P.t1 + nextUp(ro, now, false);
  if (!ro) return terms().say(P.t2);
  if (ro.aborted) return terms().say(P.t3);
  const cur = ro.steps.find((s) => s.state === "current");
  if (ro.user_paused) return P.t4;
  if (ro.phase === "Healthy" && !cur) return ro.strategy === "blueGreen" ? P.t5 : P.t6;
  if (cur?.kind === "pause" && ro.paused) {
    if (ro.pause_ends) return `Continues by itself in ${span((ro.pause_ends - now) / 1000)}. Anyone with the operator role may promote now, or abort.`;
    return P.t7;
  }
  if (cur?.kind === "analysis") return P.t8;
  if (cur) return `Working on step ${cur.index + 1}: ${cur.label}. The next step starts by itself when this one is healthy.`;
  return P.t9;
}

function Timeline({ ro, now }: { ro: LRollout; now: number }) {
  return (
    <ol className="lw-steps" aria-label={`Steps of the ${ro.strategy === "blueGreen" ? "blue-green" : "canary"} rollout`}>
      {ro.steps.map((s) => (
        <li key={s.index} className={`lw-step lw-step-${s.state}`} aria-current={s.state === "current" ? "step" : undefined}>
          <span className="lw-step-mark" aria-hidden="true">{s.state === "done" ? "✓" : s.state === "current" ? "●" : "○"}</span>
          <span className="lw-step-label">{s.label}</span>
          <span className="lw-step-state">
            {s.state === "done" ? "done" : s.state === "current" ? "now" : "next"}
            {s.state === "current" && s.kind === "pause" && ro.pause_started ? ` · ${span((now - ro.pause_started) / 1000)} elapsed${ro.pause_ends ? `, ${span((ro.pause_ends - now) / 1000)} left` : ", until promoted"}` : ""}
          </span>
        </li>
      ))}
    </ol>
  );
}

function Weights({ ro, sets }: { ro: LRollout; sets: LNode[] }) {
  const [table, setTable] = useState(false);
  const live = ro.actual_weight >= 0 ? ro.actual_weight : null;
  const want = ro.desired_weight >= 0 ? ro.desired_weight : null;
  const total = sets.reduce((a, n) => a + (n.replicas?.desired ?? 0), 0);
  return (
    <div className="lw-weights">
      <div className="lw-weights-head">
        <h2 className="lw-h">{P.t10}</h2>
        <button type="button" className="btn btn-sm" aria-pressed={table} onClick={() => setTable(!table)}>{table ? "Show as a bar" : "Show as a table"}</button>
      </div>
      {live !== null || want !== null ? (
        table ? (
          <table className="lw-table"><caption className="visually-hidden">Wanted and actual weights</caption>
            <thead><tr><th scope="col">Measure</th><th scope="col">New version</th><th scope="col">Stable</th></tr></thead>
            <tbody>
              {want !== null && <tr><th scope="row">Wanted by the current step</th><td>{want}%</td><td>{100 - want}%</td></tr>}
              {live !== null && <tr><th scope="row">Actual ({ro.actual_source})</th><td>{live}%</td><td>{100 - live}%</td></tr>}
            </tbody>
          </table>
        ) : (
          <div className="lw-bars">
            {want !== null && <Bar label="Wanted" pct={want} />}
            {live !== null && <Bar label={`Actual (${ro.actual_source})`} pct={live} />}
          </div>
        )
      ) : <p className="faint">{P.t11}</p>}
      <table className="lw-table lw-sets">
        <caption>{P.t12}</caption>
        <thead><tr><th scope="col">ReplicaSet</th><th scope="col">Role</th><th scope="col">Revision</th><th scope="col">Replicas (ready of wanted)</th><th scope="col">Share of pods</th></tr></thead>
        <tbody>
          {sets.map((n) => (
            <tr key={n.id}><th scope="row"><Mono>{n.name}</Mono></th><td><span className={`lw-role lw-role-${n.role}`}>{n.role}</span></td><td>{n.revision || "-"}</td>
              <td>{n.replicas?.ready}/{n.replicas?.desired}</td><td>{total ? Math.round(((n.replicas?.desired ?? 0) * 100) / total) : 0}%</td></tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Bar({ label, pct }: { label: string; pct: number }) {
  return (
    <div className="lw-bar-row" role="img" aria-label={`${label}: ${pct}% to the new version, ${100 - pct}% to the stable one`}>
      <span className="lw-bar-label">{label}</span>
      <span className="lw-bar-track"><span className="lw-bar-new" style={{ width: `${pct}%` }} /></span>
      <span className="lw-bar-num">{pct}% new · {100 - pct}% stable</span>
    </div>
  );
}

/** The Overview tab: the cause when it failed, what happens next, the strategy's steps, the weights, and the controls. */
export function Overview({ v, run, d, now, onOpen, reload }: { v: TreeView; run: RunDetail; d: DeploymentView; now: number; onOpen: (node: string) => void; reload: () => void }) {
  const ro = v.tree.rollout;
  const c = cause(v);
  const sets = v.tree.nodes.filter((n) => n.kind === "ReplicaSet" && n.role && (n.role !== "old" || (n.replicas?.desired ?? 0) > 0));
  const root = v.tree.nodes.find((n) => !n.parent && (n.kind === "Rollout" || n.kind === "Deployment") && n.change !== "leftover");
  const pre = ro?.pre_traffic;
  return (
    <div className="lw-over">
      {c && (
        <div className="callout callout-bad lw-cause" role="alert" aria-label="What is wrong">
          <strong>{c.text}</strong>
          <div className="lw-btns">
            {c.node && <button type="button" className="btn" onClick={() => onOpen(c.node!.id)}>Open it</button>}
            {c.node?.kind === "Pod" && v.can_logs && <button type="button" className="btn" onClick={() => onOpen("logs:" + c.node!.name)}>Read its logs</button>}
          </div>
        </div>
      )}
      {pre && (
        <div className={P.t13} role="note">
          <strong>{pre.live_weight}{P.t14}{pre.address}.</strong> {P.t15}</div>
      )}
      <div className="lw-grid">
        <section aria-labelledby="lw-next-h" className="lw-card">
          <h2 id="lw-next-h" className="lw-h">What happens next</h2>
          <p>{nextUp(ro, now, v.closed)}</p>
          {root && <p className="faint lw-small">Workload <Mono>{root.name}</Mono>: <HealthTag h={root.health} />{root.message ? ` · ${root.message}` : ""}</p>}
        </section>
        {ro && ro.steps.length > 0 && (
          <section aria-labelledby="lw-steps-h" className="lw-card">
            <h2 id="lw-steps-h" className="lw-h">{ro.strategy === "blueGreen" ? "Blue-green steps" : terms().say("{Canary} steps")}</h2>
            <Timeline ro={ro} now={now} />
          </section>
        )}
      </div>
      {ro && ro.strategy !== "rolling" && sets.length > 0 && <section className="lw-card"><Weights ro={ro} sets={sets} /></section>}
      {ro?.strategy === "rolling" && <p className="faint">{P.t16}</p>}
      <section className="lw-card" aria-label="Controls">
        <h2 className="lw-h">Control this Launch</h2>
        {(!!d.snapshot?.rollout || (d.commands?.length ?? 0) > 0) && <h3 className="h-xs">Rollout commands</h3>}
        <DeploymentControls run={run} d={d} now={now} />
        <WorkloadControls v={v} reload={reload} />
        <EmergencyReturn v={v} d={d} reload={reload} />
      </section>
      {v.tree.notes && v.tree.notes.length > 0 && (
        <ul className="lw-notes" aria-label="What could not be read">{v.tree.notes.map((n) => <li key={n}>{n}</li>)}</ul>
      )}
    </div>
  );
}
