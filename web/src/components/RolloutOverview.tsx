import type { DeploymentView } from "../api";
import { AnalysisEvidence } from "./StepCause";

// The rollout at a glance, with the analysis behind it: its steps as a small
// line (the current one marked, a timed pause with the time left), then each
// analysis run with its metrics as they are measured. It refreshes with the
// deployment, which the server pushes as it is observed.
export function RolloutOverview({ d }: { d: DeploymentView; now: number }) {
  const ro = d.snapshot?.rollout;
  if (!ro?.found || d.verdict) return null;
  const runs = d.snapshot?.analysis ?? [];
  return (
    <section className="rollout-overview" aria-label="The rollout">
      <h4 className="h-xs">Analysis, as measured</h4>
      <p className="faint small">{ro.ready} of {ro.replicas} pods ready, {ro.updated} on the new version.</p>
      {runs.map((r) => <AnalysisEvidence key={r.name} run={r} />)}
    </section>
  );
}
