import { duration } from "../format";

// What a model call used and cost, in words. Kept apart from the verification panels so that the shell's
// guide (which needs only these) does not pull the panels into the first load.
export function usageText(u?: { input_tokens: number; output_tokens: number; cost_usd?: number; duration_ms?: number; calls?: number }): string {
  if (!u) return "";
  const parts = [`${u.input_tokens.toLocaleString()} tokens in, ${u.output_tokens.toLocaleString()} out`];
  if (u.cost_usd) parts.push(`about ${costText(u.cost_usd)}`);
  if (u.duration_ms) parts.push(duration(u.duration_ms));
  if (u.calls && u.calls > 1) parts.push(`${u.calls} calls`);
  return parts.join(" · ");
}

export function costText(usd: number): string {
  if (usd === 0) return "$0";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
}
