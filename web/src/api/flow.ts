// Flow control (server schema 24): what the API says about conditions on
// step outputs, tolerated failures, retry waits and causes, a run's timeout,
// lanes, the target branch of a pull request and re-running from several
// steps. These add to the types in ../api.ts.


declare module "../api" {
  interface RunSummary {
    /** All the steps a re-run started from, when it started from several; rerun_from is the first. */
    rerun_froms?: string[];
    /** The branch the pull request targets, for a pull-request run of a pipeline that filters by it. */
    pull_request_into?: string;
    /** When the run's timeout will cancel it (Unix ms). Absent when it has none, or while an approval waits (that time does not count). */
    deadline_at?: number;
    /** The run's place in its pipeline's lane (runs of the project take turns); absent when it has none. */
    lane?: LaneInfo;
    /** Where the run is in its project's queue (limits on concurrent runs); `held` says none of its steps is queued yet, and `reason` why. */
    /** Set for a restricted run (a pull request from an automation account or a fork): why, and whether a maintainer approved it. */
    restriction?: { code: string; reason: string; approved: boolean; approved_by?: string; summary: string };
    queue?: { class: string; held: boolean; manual?: boolean; code?: string; reason?: string };
    /** How many steps failed for good and were allowed to (on_failure: continue). */
    allowed_failures?: number;
    /** Their names, in step order. */
    allowed_failure_steps?: string[];
  }

  interface Condition {
    /** A run input and the values that run the step (see equals). */
    input?: string;
    /** step.key: an output of a step this one needs; the step runs only when it is one of equals. */
    output?: string;
    equals?: string[];
    upstream?: string;
  }

  interface StepView {
    /** A step queued for a retry that waits: when a runner may be given it (Unix ms). */
    not_before?: number;
    /** Why the last failed attempt failed: exit, timeout or infrastructure. */
    failure_cause?: string;
    retry_wait?: string;
    retry_backoff?: number;
    retry_on?: string[];
    /** The step failed for good and its pipeline allows that (on_failure: continue): shown as failed (allowed). */
    allowed_failure?: boolean;
    /** From the definition: continue, or empty for stop. */
    on_failure?: string;
  }

  /** What a re-run would do: the steps it starts from, those that run again, and those it reuses. */
  interface RerunPlan { from?: string[] }

  interface RerunBrief { froms?: string[] }

  interface ValidateResult {
    /** Valid, but will not work as written for the project (for example, a pull-request filter by target branch where the project has no forge). */
    warnings?: string[];
  }
}

/** Where a run stands in its lane. */
export interface LaneInfo {
  name: string;
  key: string;
  /** The lane in words: release/ref main. */
  label: string;
  /** holding: it goes; waiting: queued behind another run; replaced: a newer run took its place; skipped: the lane was busy. */
  state: "holding" | "waiting" | "replaced" | "skipped";
  /** 1 for the run next in line, for a waiting run. */
  position?: number;
  /** The run that holds the lane (waiting or skipped), or that took this one's place (replaced). */
  behind?: { id: string; number: number };
  reason?: string;
}

/** Where a re-run starts: named steps, or the ones that failed or were canceled. */
export interface RerunFrom { from?: string[]; failed?: boolean }

/** The query of a re-run plan: the steps to start from, or the failed ones. */
export function rerunQuery(w: string | RerunFrom): string {
  const q = new URLSearchParams();
  const r = typeof w === "string" ? { from: [w] } : w;
  (r.from ?? []).forEach((f) => q.append("from", f));
  if (r.failed) q.set("failed", "true");
  return q.toString();
}

/** The body of a re-run request. */
export function rerunBody(w: string | RerunFrom): { from: string | string[]; failed?: boolean } {
  return typeof w === "string" ? { from: w } : { from: w.from ?? [], failed: !!w.failed };
}

