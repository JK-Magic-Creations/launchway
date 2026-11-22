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

/** What a step attempt reserved and used, measured by the Station (schema 54); `measured: false` means it was not measured. */
export interface StepUsage {
  measured: boolean;
  method?: string;
  note?: string;
  memory_peak_bytes?: number;
  cpu_peak_milli?: number;
  cpu_ms?: number;
  reserved_memory_bytes?: number;
  reserved_cpu_milli?: number;
  attempt?: number;
  /** Where the step's numbers came from: declared, learned, default or raised (after a memory kill). */
  sizing?: string;
  sizing_runs?: number;
  sizing_text?: string;
  killed_for_memory?: boolean;
}

/** A size in the units quantities use: 1.1Gi, 256Mi. */
export function sizeText(n: number): string {
  if (n >= 2 ** 30) return `${(n / 2 ** 30).toFixed(1)}Gi`;
  if (n >= 2 ** 20) return `${Math.floor(n / 2 ** 20)}Mi`;
  if (n >= 2 ** 10) return `${Math.floor(n / 2 ** 10)}Ki`;
  return String(n);
}

/** What a step reserved and used, in a line; a step that was not measured says so. Then where its numbers came from. */
export function stepUsageText(u: StepUsage): string {
  const parts: string[] = [];
  if (u.measured) {
    if (u.reserved_memory_bytes) parts.push(`reserved ${sizeText(u.reserved_memory_bytes)}`);
    parts.push(`used ${sizeText(u.memory_peak_bytes ?? 0)} peak`);
    if (u.cpu_peak_milli) parts.push(`${Number((u.cpu_peak_milli / 1000).toPrecision(2))} CPU peak`);
  } else if (u.note || !u.sizing) {
    parts.push("use not measured");
  }
  if (u.killed_for_memory) parts.push("killed for memory");
  if (u.sizing_text) parts.push(`numbers: ${u.sizing_text}`);
  return parts.join(", ");
}
