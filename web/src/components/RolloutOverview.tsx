import type { DeploymentView } from "../api";
import { duration } from "../format";
import { AnalysisEvidence } from "./StepCause";

// The rollout at a glance, with the analysis behind it: its steps as a small
// line (the current one marked, a timed pause with the time left), then each
// analysis run with its metrics as they are measured. It refreshes with the
// deployment, which the server pushes as it is observed.
export function RolloutOverview({ d, now }: { d: DeploymentView; now: number }) {
  const ro = d.snapshot?.rollout;
  if (!ro?.found) return null;
  const names = ro.step_names ?? [];
  const total = Math.max(ro.steps, names.length);
  const left = ro.pause_until && ro.pause_until > now ? ro.pause_until - now : 0;
  const runs = d.snapshot?.analysis ?? [];
  return (
    <section className="rollout-overview" aria-label="The rollout">
      <h4 className="h-xs">The rollout</h4>
      {total > 0 && (
        <ol className="ro-steps" aria-label="Rollout steps">
          {Array.from({ length: total }, (_, i) => {
            const cur = ro.step === i;
            return (
              <li key={i} className={cur ? "ro-step ro-step-on" : (ro.step ?? 0) > i ? "ro-step ro-step-done" : "ro-step"} aria-current={cur ? "step" : undefined}>
                <span>{names[i] || `step ${i + 1}`}</span>
                {cur && ro.canary_weight !== undefined && <span className="faint"> now {ro.canary_weight}%</span>}
                {cur && left > 0 && <span className="faint"> paused, {duration(left)} left</span>}
                {cur && ro.paused && !left && <span className="faint"> paused until someone promotes it</span>}
              </li>
            );
          })}
        </ol>
      )}
      <p className="faint small">{ro.ready} of {ro.replicas} pods ready, {ro.updated} on the new version.</p>
      {runs.map((r) => <AnalysisEvidence key={r.name} run={r} />)}
    </section>
  );
}
