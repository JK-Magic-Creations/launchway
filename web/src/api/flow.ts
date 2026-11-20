// Flow control (server schema 24): what the run summary says about the steps
// that failed and were allowed to. The rest of the flow-control types are
// declared here too, so that api.ts does not grow.

declare module "../api" {
  interface RunSummary {
    /** How many steps failed for good and were allowed to (on_failure: continue). */
    allowed_failures?: number;
    /** Their names, in step order. */
    allowed_failure_steps?: string[];
  }
}

export {};
