import type { RunSummary } from "../api";

/**
 * "1 allowed failure": a run that went on past steps that failed and were
 * allowed to (on_failure: continue). It shows a run that is succeeded as
 * not quite clean. With onSelect it is a button that opens the step (the
 * first one, or the one named); without, a plain badge for a row that is
 * already a link, naming the steps in its title.
 */
export function AllowedFailureBadge({ run, onSelect }: { run: Pick<RunSummary, "allowed_failures" | "allowed_failure_steps">; onSelect?: (step: string) => void }) {
  const n = run.allowed_failures ?? 0;
  if (n === 0) return null;
  const steps = run.allowed_failure_steps ?? [];
  const text = `${n} allowed failure${n === 1 ? "" : "s"}`;
  const title = `${steps.join(", ") || "A step"} failed, and the pipeline allows that (on_failure: continue): the run went on as if it had succeeded`;
  if (!onSelect || steps.length === 0) return <span className="badge-allowed" title={title}>{text}</span>;
  return (
    <button type="button" className="badge-allowed badge-button" title={title} onClick={() => onSelect(steps[0])}>{text}</button>
  );
}
