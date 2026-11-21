import { call } from "./api";

// The API of the event log: what the server received, and what was decided about each event.

export type Decision = "started" | "held" | "waiting" | "filtered" | "superseded" | "skipped" | "duplicate" | "refused" | "error";

export const DECISIONS: Decision[] = ["started", "held", "waiting", "filtered", "superseded", "skipped", "duplicate", "refused", "error"];
export const SOURCES = ["push", "pull_request", "schedule", "webhook", "event_source", "manual", "run_result", "launch_result"];

export interface EventDecision {
  id: number;
  at: number;
  trigger: string;
  decision: Decision;
  reason: string;
  run_id?: string;
  run_number?: number;
}

export interface EventRecord {
  id: string;
  project: string;
  at: number;
  source: string;
  source_name?: string;
  verified: boolean;
  summary: string;
  ref?: string;
  sha?: string;
  payload?: unknown;
  replay?: { ref?: string; sha?: string; inputs?: Record<string, string> };
  replay_of?: string;
  decisions: EventDecision[];
}

export interface EventQuery { project?: string; source?: string; decision?: string; verified?: string; limit?: number }

export const eventsApi = {
  list: (q: EventQuery) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v) p.set(k, String(v));
    return call<EventRecord[]>("GET", `/api/v1/events?${p.toString()}`);
  },
  replay: (id: string) => call<{ run: { id: string; number: number; project: string }; replay_of: string }>("POST", `/api/v1/events/${encodeURIComponent(id)}/replay`, {}),
};
