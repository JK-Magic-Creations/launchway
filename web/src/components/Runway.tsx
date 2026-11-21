import type { DeploySnapshot } from "../api";
import { Analyses, RolloutView } from "./Deployment";

/** Runway is the rollout drawn as a runway: the canary steps as stations, the share of traffic on the new version,
 * its pauses, the copies ready and the rollout's own checks. The run page and the application page draw the same
 * rollout with this one component. muted: the data is out of date, so nothing is shown as good. */
export function Runway({ snapshot, muted, now }: { snapshot: Pick<DeploySnapshot, "rollout" | "analysis">; muted: boolean; now: number }) {
  if (!snapshot.rollout && !(snapshot.analysis?.length)) return null;
  return (
    <div className="runway">
      {snapshot.rollout && <RolloutView r={snapshot.rollout} muted={muted} now={now} />}
      {snapshot.analysis && snapshot.analysis.length > 0 && <Analyses list={snapshot.analysis} muted={muted} />}
    </div>
  );
}
