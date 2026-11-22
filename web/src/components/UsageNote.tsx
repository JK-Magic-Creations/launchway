import { stepUsageText, type StepUsage } from "./usageText";

/** What a step reserved and used, next to the step; hover for how it was read. */
export function UsageNote({ usage }: { usage: StepUsage }) {
  const title = usage.measured ? `How it was read: ${usage.method ?? "unknown"}. ${usage.note ?? ""}` : usage.note;
  return <span className="meta" data-testid="step-usage" title={title}>{stepUsageText(usage)}</span>;
}
