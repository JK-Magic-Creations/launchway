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

export interface EventSource {
  id: string; project: string; name: string; url: string; has_secret: boolean; enabled: boolean;
  mapping: Record<string, string>; filter?: string; dedupe_key?: string; schema?: string;
  debounce_seconds: number; batch_seconds: number; max_body: number; rate_per_minute: number;
}

export interface SourceTest { status: string; reason: string; problems?: string[]; inputs?: Record<string, string>; dedupe_key?: string }

export interface SourceForm {
  name: string; mapping: Record<string, string>; filter?: string; dedupe_key?: string; schema?: string; ref?: string;
  debounce_seconds?: number; batch_seconds?: number;
}

const base = (project: string) => `/api/v1/projects/${encodeURIComponent(project)}/event-sources`;

export const sourcesApi = {
  list: (project: string) => call<EventSource[]>("GET", base(project)),
  create: (project: string, f: SourceForm) => call<{ source: EventSource; secret: string }>("POST", base(project), f),
  rotate: (project: string, name: string) => call<{ source: EventSource; secret: string }>("POST", `${base(project)}/${encodeURIComponent(name)}`, { rotate_secret: true }),
  setEnabled: (project: string, name: string, enabled: boolean) => call<{ source: EventSource }>("POST", `${base(project)}/${encodeURIComponent(name)}`, { enabled }),
  remove: (project: string, name: string) => call<{ ok: boolean }>("DELETE", `${base(project)}/${encodeURIComponent(name)}`),
  test: (project: string, name: string, body: unknown) => call<SourceTest>("POST", `${base(project)}/${encodeURIComponent(name)}/test`, { body }),
};
