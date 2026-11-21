// "N things need you": what needs a person on Home, in plain sentences with one action each. It starts
// from what the server already says needs a look (failed runs, waiting approvals, runners that do not
// answer, unhealthy applications) and says more about a failed run when the launch lines know where it stopped.

import type { LaunchLineView } from "../api/launch";
import { ago } from "../format";
import type { IconName } from "../kit/Icon";
import { attention, type Attention, type AttentionInput } from "./derive";

export interface Need extends Attention { icon: IconName }

const ICON: Record<string, IconName> = { run: "x", app: "alert", approval: "gate", drift: "alert", runner: "server" };

/** What needs a person, most urgent first. A failed run is described by the station it stopped at. */
export function needsYou(input: AttentionInput, lines: LaunchLineView[], now: number): Need[] {
  const byRun = new Map(lines.map((l) => [l.run, l]));
  return attention(input).map((a) => {
    const kind = a.key.split(":")[0];
    const need: Need = { ...a, icon: ICON[kind] ?? "alert" };
    if (kind === "run") {
      const line = byRun.get(a.key.slice(4));
      const at = line?.stations.find((s) => s.state === "failed");
      if (line && at) {
        need.title = `Run #${line.number} of ${line.project} stopped at ${at.label.toLowerCase()}`;
        need.detail = `${at.detail}. ${ago(line.finished_at || line.created_at, now)}.`;
      }
    }
    return need;
  });
}

/** The sentence under the heading: how many things need a person, and what else is going on. */
export function headline(needs: number, inFlight: number): { title: string; sentence: string } {
  const on = inFlight === 0 ? "Nothing is on its way to your users right now." : inFlight === 1 ? "One change is on its way to your users." : `${inFlight} changes are on their way to your users.`;
  if (needs === 0) return { title: "All clear", sentence: `Nothing needs you. ${on}` };
  return { title: needs === 1 ? "1 thing needs you" : `${needs} things need you`, sentence: `Everything else is moving along. ${on}` };
}
