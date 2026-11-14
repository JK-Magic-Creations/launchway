// Types mirror the Go API in internal/engine and internal/source.

export type RunStatus = "queued" | "running" | "succeeded" | "failed" | "canceled";
export type StepStatus =
  | "pending" | "queued" | "leased" | "running"
  | "succeeded" | "failed" | "skipped" | "canceled";

export interface StepBrief { id: string; status: StepStatus }

export interface RunSummary {
  id: string;
  project: string;
  number: number;
  pipeline: string;
  status: RunStatus;
  cancel_requested: boolean;
  event: string;
  ref: string;
  branch: string;
  tag: string;
  sha: string;
  actor: string;
  message: string;
  author: string;
  created_at: number;
  started_at: number;
  finished_at: number;
  steps: StepBrief[];
}

export interface Condition { branch?: string[]; tag?: string[]; event?: string[] }

export interface StepView {
  run_id: string;
  id: string;
  position: number;
  status: StepStatus;
  attempt: number;
  queued_at: number;
  started_at: number;
  finished_at: number;
  exit_code: number | null;
  error: string;
  skip_kind: string;
  runner: string;
  outputs: Record<string, string>;
  image?: string;
  run?: string;
  needs?: string[];
  when?: Condition;
  retry: number;
  timeout: string;
}

export interface GraphStep {
  id: string;
  image: string;
  run: string;
  needs?: string[];
  when?: Condition;
  env?: Record<string, string>;
  retry: number;
  timeout: string;
}

export interface Graph {
  name: string;
  on: { push?: { branches?: string[]; tags?: string[] } };
  steps: GraphStep[];
}

export interface RunDetail extends RunSummary {
  source: { kind: string; url?: string; path?: string };
  graph: Graph;
  levels: string[][];
  step_list: StepView[];
}

export interface LogLine { seq: number; at: number; stream: "stdout" | "stderr" | "system"; text: string }

export interface RunEvent {
  seq: number;
  run_id: string;
  type: string;
  at: number;
  data: Record<string, unknown>;
}

export interface Decision { at: number; ref: string; sha: string; outcome: "started" | "ignored" | "error"; detail: string; run_id?: string }

export interface Project {
  name: string;
  url: string;
  default_branch: string;
  poll_seconds: number;
  last_fetch: number;
  error?: string;
  decisions: Decision[];
  last_run: RunSummary | null;
}

export interface Ref { name: string; kind: "branch" | "tag"; sha: string; subject: string; author: string; committed_at: number }

export interface Runner { id: string; name: string; executor: string; version: string; last_seen: number }

export interface Problem { line?: number; column?: number; path?: string; message: string }

export interface ValidateResult { ok: boolean; problems?: Problem[]; graph?: Graph; levels?: string[][] }

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

async function call<T>(method: string, path: string, body?: unknown, raw = false): Promise<T> {
  const init: RequestInit = { method, headers: {} };
  if (body !== undefined) {
    if (raw) {
      init.body = body as string;
      (init.headers as Record<string, string>)["Content-Type"] = "text/yaml";
    } else {
      init.body = JSON.stringify(body);
      (init.headers as Record<string, string>)["Content-Type"] = "application/json";
    }
  }
  const resp = await fetch(path, init);
  const text = await resp.text();
  let data: unknown = undefined;
  try { data = text ? JSON.parse(text) : undefined; } catch { /* not JSON */ }
  if (!resp.ok) {
    const msg = (data as { error?: string } | undefined)?.error ?? (text || resp.statusText);
    throw new ApiError(resp.status, msg);
  }
  return data as T;
}

export const api = {
  info: () => call<{ version: string; actor: string; projects: number }>("GET", "/api/v1/info"),
  runs: (q: { project?: string; status?: string; limit?: number } = {}) => {
    const p = new URLSearchParams();
    if (q.project) p.set("project", q.project);
    if (q.status) p.set("status", q.status);
    if (q.limit) p.set("limit", String(q.limit));
    return call<RunSummary[]>("GET", "/api/v1/runs?" + p.toString());
  },
  run: (id: string) => call<RunDetail>("GET", `/api/v1/runs/${id}`),
  events: (id: string) => call<RunEvent[]>("GET", `/api/v1/runs/${id}/events`),
  pipelineFile: (id: string) => call<{ text: string }>("GET", `/api/v1/runs/${id}/pipeline-file`),
  logs: (id: string, step: string, attempt: number, after = 0) =>
    call<{ attempt: number; lines: LogLine[] }>("GET",
      `/api/v1/runs/${id}/steps/${encodeURIComponent(step)}/logs?attempt=${attempt}&after=${after}`),
  cancel: (id: string) => call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/cancel`),
  resume: (id: string) => call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/resume`),
  projects: () => call<Project[]>("GET", "/api/v1/projects"),
  refs: (name: string) => call<Ref[]>("GET", `/api/v1/projects/${name}/refs`),
  startRun: (name: string, ref: string) => call<RunSummary>("POST", `/api/v1/projects/${name}/runs`, { ref }),
  runners: () => call<Runner[]>("GET", "/api/v1/runners"),
  validate: (yaml: string) => call<ValidateResult>("POST", "/api/v1/validate", yaml, true),
};
