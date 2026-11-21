import { useMemo } from "react";
import type { RunDetail, StepView } from "../api";
import { duration } from "../format";
import { LaunchLine, type LineStation } from "../kit/LaunchLine";
import { stateOf, type State } from "../kit/Status";
import { shownStatus } from "./ui";
import { whyWaiting } from "./whyWaiting";

/** The state a step is drawn in on the line. A deploy step that waits for what runners observe is at work. */
function lineState(s: StepView): State {
  if (s.status === "failed" && s.allowed_failure) return "skip";
  if (s.status === "queued" || s.status === "leased") return "idle";
  return stateOf(shownStatus(s));
}

/** The stations of a run's living line: one for each step, in the order the pipeline runs them, each saying
 * how long it took or why it waits. */
export function stepStations(run: RunDetail, now: number): LineStation[] {
  const order = [...run.step_list].sort((a, b) => a.position - b.position);
  return order.map((s) => {
    const why = whyWaiting(run, s, now);
    const took = s.started_at > 0 ? duration((s.finished_at || now) - s.started_at) : "";
    const detail = why ? why.short : s.status === "failed" ? (s.allowed_failure ? "failed, allowed" : s.exit_code != null ? `exit code ${s.exit_code}` : "failed") : took;
    return { key: s.id, label: s.id, state: lineState(s), detail };
  });
}

/** RunLine draws the run's pipeline as a living line from its real steps. Choosing a station opens the step below. */
export function RunLine({ run, selected, onSelect, now }: { run: RunDetail; selected: string | null; onSelect: (step: string) => void; now: number }) {
  const stations = useMemo(() => stepStations(run, now), [run, now]);
  return (
    <div className="runline" data-count={stations.length} style={{ "--n": stations.length } as React.CSSProperties}>
      <LaunchLine stations={stations} big selected={selected ?? undefined} onSelect={onSelect} />
    </div>
  );
}
