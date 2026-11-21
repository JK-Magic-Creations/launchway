// The time estimates the server adds to runs and steps (see internal/engine/estimate.go). Declared here and
// merged into the interfaces of api.ts, which is at its size limit.

/** What a step usually takes, from the same step of earlier runs of the pipeline. */
export interface StepEstimate {
  samples: number; enough: boolean;
  typical_ms?: number; low_ms?: number; high_ms?: number; slow_ms?: number;
  basis: "same_kind" | "all_runs";
  bimodal?: { fast_ms: number; slow_ms: number; fast_share: number };
  person?: boolean;
  last_run?: string; last_run_number?: number; last_ms?: number;
}

/** What a run is expected to take, or took. */
export interface RunEstimate {
  as_of: number; known: boolean; reason?: "no_history" | "too_few";
  total_ms?: number; remaining_ms?: number; remaining_low_ms?: number; remaining_high_ms?: number;
  partial?: boolean; unknown_steps?: string[]; overrun?: boolean;
  waiting_for_person?: string[]; queued?: { step: string; since: number; reason?: string }[];
  basis?: string; took_ms?: number; usual_ms?: number; diff_percent?: number; runs: number;
}

declare module "./api" {
  interface RunSummary { estimate?: RunEstimate }
  interface StepView { estimate?: StepEstimate }
}
