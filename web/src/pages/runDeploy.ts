import { type DeploymentView, type RunDetail, type StepView } from "../api";
import { deploymentOf, environmentOf, freshness, phaseLabel, shortPhase } from "../components/Deployment";

export function deployOf(d: Record<string, unknown>): Record<string, unknown> {
  return (d.deploy as Record<string, unknown> | undefined) ?? {};
}

export function commandOf(d: Record<string, unknown>): Record<string, string | undefined> {
  return (deployOf(d).command as Record<string, string | undefined> | undefined) ?? {};
}

// deployPhases says, for each deploy or rollback step at work, where its
// deployment stands, in words. Out-of-date observations say so instead.
export function deployPhases(run: RunDetail): Record<string, string> {
  const out: Record<string, string> = {};
  const now = Date.now();
  for (const s of run.step_list) {
    if (s.status === "waiting" && s.wait_kind === "lock") { out[s.id] = "waiting for lock"; continue; }
    if (s.status !== "waiting" || s.wait_kind !== "observe") continue;
    const d: DeploymentView | undefined = deploymentOf(run, s);
    if (!d) continue;
    const f = freshness(d, now);
    const where = s.kind === "rollback" ? "rollback" : s.kind === "rollout" ? "rollout" : environmentOf(run, s) || "deploy";
    out[s.id] = `${where}: ${f.state === "never" ? "not observed" : f.state === "error" ? "cannot see it" : f.state === "stale" ? "out of date" : shortPhase(d.phase)}`;
  }
  return out;
}

// deployingPhase is where a deploy step's deployment stands, in words, only
// if what was observed is current (the panel explains the other cases).
export function deployingPhase(run: RunDetail, s: StepView): string | undefined {
  if (s.wait_kind !== "observe") return undefined;
  const d = deploymentOf(run, s);
  if (!d) return undefined;
  const f = freshness(d, Date.now());
  return f.state === "fresh" ? phaseLabel(d.phase) : undefined;
}
