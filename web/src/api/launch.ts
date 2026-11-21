// GET /api/v1/launch-lines: where each recent change is on its way (internal/api/launchlines.go).
import { call } from "../api";

/** Station states as the server says them. */
export type StationState = "done" | "running" | "waiting" | "failed" | "idle" | "skipped";

export interface StationView {
  /** commit, checks, tests, package, env:<name> or verified. */
  key: string;
  label: string;
  state: StationState;
  /** One short phrase: what happened here, or why it waits. */
  detail: string;
  /** The step to open to see more. */
  step?: string;
}

export interface LaunchLineView {
  run: string;
  number: number;
  project: string;
  message: string;
  actor: string;
  ref: string;
  sha: string;
  status: string;
  created_at: number;
  finished_at: number;
  stations: StationView[];
  /** The worst state on the line. */
  tone: "bad" | "wait" | "run" | "ok" | "idle";
  /** Where the change is, in one plain sentence. */
  now: string;
}

export const launchApi = {
  /** The line of one run (what the pull request page shows for its latest run). */
  line: (run: string) => call<LaunchLineView[]>("GET", `/api/v1/launch-lines?run=${encodeURIComponent(run)}`).then((l) => l[0] ?? null),
  lines: (limit = 12, background = false, project = "") =>
    call<LaunchLineView[]>("GET", `/api/v1/launch-lines?limit=${limit}${project ? "&project=" + encodeURIComponent(project) : ""}`, undefined, { background }),
};
