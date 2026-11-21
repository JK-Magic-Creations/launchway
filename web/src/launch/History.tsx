import { ago, clock } from "../format";
import type { DeploymentView } from "../api";
import { Link } from "../components/ui";

/** Who did what to this delivery: commands given to the rollout, and what the deploy runners observed. */
export function HistoryTab({ d, now, admin }: { d: DeploymentView; now: number; admin: boolean }) {
  const cmds = [...(d.commands ?? [])].reverse();
  const timeline = [...(d.timeline ?? [])].reverse();
  return (
    <div className="lw-hist">
      <section className="lw-card" aria-labelledby="lw-h-cmd">
        <h2 id="lw-h-cmd" className="lw-h">Commands to the rollout</h2>
        {cmds.length === 0 ? <p className="faint">No one has given the rollout a command.</p> : (
          <ol className="lw-list">
            {cmds.map((c) => (
              <li key={c.id}><strong>{c.action === "promote-full" ? "Full promotion" : c.action}</strong> by {c.origin === "person" ? c.actor : c.origin === "step" ? `step ${c.step}` : c.origin}
                {" "}<span className="faint" title={clock(c.at)}>{ago(c.at, now)}</span> · {c.state === "done" ? (c.result ?? "done").replace(/_/g, " ") : c.state}{c.comment ? ` · “${c.comment}”` : ""}{c.detail ? ` · ${c.detail}` : ""}</li>
            ))}
          </ol>
        )}
      </section>
      <section className="lw-card" aria-labelledby="lw-h-obs">
        <h2 id="lw-h-obs" className="lw-h">What the deploy runner saw</h2>
        {timeline.length === 0 ? <p className="faint">Nothing observed yet.</p> : (
          <ol className="lw-list">
            {timeline.map((t, i) => <li key={i}><span className="faint" title={clock(t.at)}>{ago(t.at, now)}</span> · {t.phase}{t.detail ? `: ${t.detail}` : ""}</li>)}
          </ol>
        )}
      </section>
      <p className="faint lw-small">Pause, resume, restart, scale, manifest and log reads are kept in the audit trail with who asked and why{admin ? <>: <Link href="/settings/audit">open the audit trail</Link></> : "; an administrator reads it"}.</p>
    </div>
  );
}
