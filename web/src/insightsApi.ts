import { call } from "./api";

// The API of the Insights pages: loaded only by them.

export interface InsightPoint {
  at: number; runs: number; succeeded: number; failed: number; success_rate: number;
  duration_p50_s: number; duration_p95_s: number; queue_wait_p50_s: number; queue_wait_p95_s: number;
  tests_total: number; tests_failed: number; test_failure_rate: number;
}
export interface InsightStepRow { step: string; runs: number; failures: number; failure_rate: number; p50_s: number; p95_s: number }
export interface InsightDeliveryPoint { at: number; launches: number; failures: number; lead_time_median_s: number; time_to_restore_median_s: number }
export interface InsightDelivery {
  launches: number; failures: number; frequency_per_week: number; lead_time_median_s: number;
  change_failure_rate: number; time_to_restore_median_s: number; unrestored: number; series: InsightDeliveryPoint[];
}
export interface Insights {
  from: number; to: number; bucket_seconds: number; branch?: string;
  runs: number; succeeded: number; failed: number; canceled: number; success_rate: number;
  duration_p50_s: number; duration_p95_s: number; queue_wait_p50_s: number; queue_wait_p95_s: number;
  series: InsightPoint[]; slowest_steps: InsightStepRow[]; flakiest_steps: InsightStepRow[];
  tests_total: number; tests_failed: number; tests_flaky: number; delivery?: InsightDelivery; truncated?: boolean;
}
export interface InsightsAnswer { scope: "flight" | "squadron" | "installation"; name: string; insights: Insights }
export interface InsightsAsk { flight?: string; squadron?: string; days: number; branch?: string }

const query = (a: InsightsAsk, format?: string) => {
  const q = new URLSearchParams();
  if (a.flight) q.set("flight", a.flight);
  if (a.squadron) q.set("squadron", a.squadron);
  q.set("from", new Date(Date.now() - a.days * 86400000).toISOString());
  if (a.branch) q.set("branch", a.branch);
  if (format) q.set("format", format);
  return "?" + q.toString();
};

export const insightsApi = {
  get: (a: InsightsAsk, background?: boolean) => call<InsightsAnswer>("GET", `/api/v1/insights${query(a)}`, undefined, background ? { background: true } : undefined),
  csvUrl: (a: InsightsAsk) => `/api/v1/insights${query(a, "csv")}`,
};
