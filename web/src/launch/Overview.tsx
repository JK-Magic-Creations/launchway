import { useState } from "react";
import type { DeploymentView, RunDetail } from "../api";
import { DeploymentControls } from "../components/DeploymentControls";
import { Mono } from "../components/ui";
import type { LNode, LRollout, TreeView } from "../launchApi";
import { cause, span } from "./model";
import { EmergencyReturn, WorkloadControls } from "./Controls";
import { HealthTag } from "./Tree";

/** What happens next, and who may step in, in a sentence. Zero touch is the default: this says so. */
export function nextUp(ro: LRollout | undefined, now: number, over: boolean): string {
  if (over) return "This Launch is over. Nothing happens by itself; Return to base goes back to an earlier release.";
  if (!ro) return "The deploy runner has not shown a rollout for this delivery yet.";
  if (ro.aborted) return "The rollout was aborted: all traffic is on the stable version. Retry it, or roll back.";
  const cur = ro.steps.find((s) => s.state === "current");
  if (ro.user_paused) return "A person paused this rollout. It stays where it is until someone resumes it.";
  if (ro.phase === "Healthy" && !cur) return ro.strategy === "blueGreen" ? "The new version is active. The old one is kept for an instant return, then scaled down." : "The rollout is complete: the new version has all traffic.";
  if (cur?.kind === "pause" && ro.paused) {
    if (ro.pause_ends) return `Continues by itself in ${span((ro.pause_ends - now) / 1000)}. Anyone with the operator role may promote now, or abort.`;
    return "Waiting at a pause that only a person ends: promote it to go on, or abort. Nothing continues by itself.";
  }
  if (cur?.kind === "analysis") return "Waiting for the analysis to finish. It passes by itself or fails the rollout; an operator may also abort.";
  if (cur) return `Working on step ${cur.index + 1}: ${cur.label}. The next step starts by itself when this one is healthy.`;
  return "The rollout is progressing by itself.";
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
        <h2 className="lw-h">Share of real traffic to the new version</h2>
        <button type="button" className="btn btn-sm" aria-pressed={table} onClick={() => setTable(!table)}>{table ? "Show as a bar" : "Show as a table"}</button>
      </div>
      {live !== null || want !== null ? (
        table ? (
          <table className="lw-table"><caption className="sr-only">Wanted and actual weights</caption>
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
      ) : <p className="faint">This strategy has no traffic weights.</p>}
      <table className="lw-table lw-sets">
        <caption>ReplicaSets: wanted and actual replicas</caption>
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
        <div className="callout callout-ok lw-pre-banner" role="note">
          <strong>{pre.live_weight}% of real traffic; testing through {pre.address}.</strong> Real users are not sent to the new version until the tests pass.
        </div>
      )}
      <div className="lw-grid">
        <section aria-labelledby="lw-next-h" className="lw-card">
          <h2 id="lw-next-h" className="lw-h">What happens next</h2>
          <p>{nextUp(ro, now, v.closed)}</p>
          {root && <p className="faint lw-small">Workload <Mono>{root.name}</Mono>: <HealthTag h={root.health} />{root.message ? ` · ${root.message}` : ""}</p>}
        </section>
        {ro && ro.steps.length > 0 && (
          <section aria-labelledby="lw-steps-h" className="lw-card">
            <h2 id="lw-steps-h" className="lw-h">{ro.strategy === "blueGreen" ? "Blue-green steps" : "Canary steps"}</h2>
            <Timeline ro={ro} now={now} />
          </section>
        )}
      </div>
      {ro && ro.strategy !== "rolling" && sets.length > 0 && <section className="lw-card"><Weights ro={ro} sets={sets} /></section>}
      {ro?.strategy === "rolling" && <p className="faint">This is a plain Deployment: Kubernetes replaces its pods a few at a time. There are no traffic weights or steps; pause, resume, restart and scale apply, and the ReplicaSets below show the update.</p>}
      <section className="lw-card" aria-label="Controls">
        <h2 className="lw-h">Control this Launch</h2>
        <h3 className="h-xs">Rollout commands</h3>
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
