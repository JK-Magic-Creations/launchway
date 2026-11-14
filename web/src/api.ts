// Types mirror the Go API in internal/engine and internal/source.

export type RunStatus = "queued" | "running" | "succeeded" | "failed" | "canceled";
export type StepStatus =
  | "pending" | "queued" | "leased" | "running" | "waiting"
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
  /** Approval steps waiting for someone to decide. */
  waiting: string[];
  /** A run of pipeline text from the editor that is not in Git (event "draft"). */
  draft: boolean;
  /** Names the draft's text: sha256:… */
  pipeline_hash?: string;
}

export interface Condition { branch?: string[]; tag?: string[]; event?: string[] }

/** Where a secret is kept. The value itself never leaves the runner. */
export interface SecretRef { store: string; key: string; field?: string }

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
  /** A failed attempt's last lines of output, masked like the log. */
  excerpt: string;
  container?: string;
  image?: string;
  run?: string;
  needs?: string[];
  when?: Condition;
  secrets?: Record<string, SecretRef>;
  retry: number;
  timeout: string;
  description?: string;
  // Approvals only. A waiting approval started waiting at started_at.
  kind?: "approval";
  message?: string;
  deadline: number;
  decided_by: string;
  decided_at: number;
  comment: string;
}

export interface GraphContainer {
  name: string; image: string; env?: Record<string, string>; cpu?: string; memory?: string; service?: boolean; command?: string[];
}

export interface GraphStep {
  id: string;
  description?: string;
  kind?: "approval";
  message?: string;
  container?: string;
  image: string;
  run: string;
  needs?: string[];
  when?: Condition;
  env?: Record<string, string>;
  secrets?: Record<string, SecretRef>;
  retry: number;
  timeout: string;
}

export interface Graph {
  name: string;
  on: { push?: { branches?: string[]; tags?: string[] } };
  containers?: GraphContainer[];
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

export interface PushSettings { branch_prefix: string; allow_direct: string[] | null }

/** What the caller may do with the pipeline editor on a project. */
export interface EditorRights {
  drafts_allow: "admin" | "operator" | "none";
  may_draft: boolean;
  draft_secrets: boolean;
  may_save: boolean;
  may_force: boolean;
}

export interface Project {
  name: string;
  url: string;
  default_branch: string;
  poll_seconds: number;
  last_fetch: number;
  error?: string;
  decisions: Decision[];
  last_run: RunSummary | null;
  /** How the editor may save to the repository; null if it may not. */
  push: PushSettings | null;
  editor: EditorRights;
}

/** The pipeline file at the tip of a branch, as the editor opens it. */
export interface PipelineFile {
  ref: string;
  kind: "branch" | "tag";
  sha: string;
  subject: string;
  author: string;
  committed_at: number;
  path: string;
  /** False: the branch has no pipeline yet, and text is a starting point. */
  exists: boolean;
  text: string;
}

export interface SaveRequest {
  base_ref: string;
  base_sha: string;
  pipeline: string;
  message: string;
  /** Empty for a new branch, or base_ref to commit to it directly. */
  branch: string;
  force?: boolean;
}

export interface SaveResult {
  branch: string;
  sha: string;
  base_ref: string;
  base_sha: string;
  path: string;
  direct: boolean;
  pull_request_url?: string;
}

/** The pipeline text a run ran: from Git, or kept with a draft run. */
export interface RunPipelineText { text: string; draft: boolean; hash?: string }

export interface Ref { name: string; kind: "branch" | "tag"; sha: string; subject: string; author: string; committed_at: number }

export interface Approval {
  run_id: string;
  project: string;
  run_number: number;
  step_id: string;
  message: string;
  started_by: string;
  commit_subject: string;
  short_sha: string;
  branch: string;
  tag?: string;
  waiting_since: number;
  deadline: number;
}

// online: seen within the last two minutes. Runners not seen for a day are no longer listed.
export interface Runner { id: string; name: string; executor: string; version: string; last_seen: number; online: boolean }

export interface Problem { line?: number; column?: number; path?: string; message: string }

export interface ValidateResult { ok: boolean; problems?: Problem[]; graph?: Graph; levels?: string[][] }

export class ApiError extends Error {
  constructor(public status: number, message: string, public problems?: Problem[]) { super(message); }
}

/** Who is signed in, and what they may do. The server checks every request
 * regardless; the UI uses this only to hide what would be refused. */
export type Role = "viewer" | "operator" | "admin";
export interface Me {
  user: string;
  groups: string[];
  auth_mode: "none" | "oidc";
  via: "none" | "session" | "token";
  admin: boolean;
  runners: boolean;
  roles: Record<string, Role>;
}

export interface ApiToken { id: string; name: string; created_at: number; expires_at: number; last_used: number }

export interface AuditEntry {
  id: number; at: number; user: string; source: string; action: string;
  project: string; target: string; outcome: "ok" | "denied" | "failed"; detail: string;
}

export interface AuditQuery { user?: string; project?: string; action?: string; from?: number; to?: number; before?: number; limit?: number }

/** Sent when any call finds the caller is not signed in. */
export const SIGNED_OUT = "launchway:signed-out";

interface CallOptions {
  raw?: boolean;
  /** Made by the page on its own (polling, refreshing after a live update),
   * so it does not keep an idle session alive. */
  background?: boolean;
}

async function call<T>(method: string, path: string, body?: unknown, opts: CallOptions = {}): Promise<T> {
  const headers: Record<string, string> = {};
  const init: RequestInit = { method, headers, credentials: "same-origin" };
  // Every request that changes something says it comes from these pages;
  // the server refuses such requests without it.
  if (method !== "GET") headers["X-Launchway-CSRF"] = "1";
  if (opts.background) headers["X-Launchway-Background"] = "1";
  if (body !== undefined) {
    if (opts.raw) {
      init.body = body as string;
      headers["Content-Type"] = "text/yaml";
    } else {
      init.body = JSON.stringify(body);
      headers["Content-Type"] = "application/json";
    }
  }
  const resp = await fetch(path, init);
  const text = await resp.text();
  let data: unknown = undefined;
  try { data = text ? JSON.parse(text) : undefined; } catch { /* not JSON */ }
  if (!resp.ok) {
    if (resp.status === 401) window.dispatchEvent(new Event(SIGNED_OUT));
    const body = data as { error?: string; problems?: Problem[] } | undefined;
    throw new ApiError(resp.status, body?.error ?? (text || resp.statusText), body?.problems);
  }
  return data as T;
}

export const api = {
  info: () => call<{ version: string; auth_mode: "none" | "oidc"; actor?: string }>("GET", "/api/v1/info"),
  me: () => call<Me>("GET", "/api/v1/me"),
  logout: () => call<void>("POST", "/auth/logout"),
  runs: (q: { project?: string; status?: string; limit?: number } = {}, background = false) => {
    const p = new URLSearchParams();
    if (q.project) p.set("project", q.project);
    if (q.status) p.set("status", q.status);
    if (q.limit) p.set("limit", String(q.limit));
    return call<RunSummary[]>("GET", "/api/v1/runs?" + p.toString(), undefined, { background });
  },
  run: (id: string, background = false) => call<RunDetail>("GET", `/api/v1/runs/${id}`, undefined, { background }),
  events: (id: string) => call<RunEvent[]>("GET", `/api/v1/runs/${id}/events`),
  pipelineFile: (id: string) => call<RunPipelineText>("GET", `/api/v1/runs/${id}/pipeline-file`),
  logs: (id: string, step: string, attempt: number, after = 0) =>
    call<{ attempt: number; lines: LogLine[] }>("GET",
      `/api/v1/runs/${id}/steps/${encodeURIComponent(step)}/logs?attempt=${attempt}&after=${after}`),
  cancel: (id: string) => call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/cancel`),
  resume: (id: string) => call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/resume`),
  approve: (id: string, step: string, comment: string) =>
    call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/steps/${encodeURIComponent(step)}/approve`, { comment }),
  reject: (id: string, step: string, comment: string) =>
    call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/steps/${encodeURIComponent(step)}/reject`, { comment }),
  approvals: (background = false) => call<Approval[]>("GET", "/api/v1/approvals", undefined, { background }),
  projects: (background = false) => call<Project[]>("GET", "/api/v1/projects", undefined, { background }),
  project: (name: string) => call<Project>("GET", `/api/v1/projects/${encodeURIComponent(name)}`),
  refs: (name: string) => call<Ref[]>("GET", `/api/v1/projects/${name}/refs`),
  pipeline: (name: string, ref: string) =>
    call<PipelineFile>("GET", `/api/v1/projects/${encodeURIComponent(name)}/pipeline?ref=${encodeURIComponent(ref)}`),
  draftRun: (name: string, ref: string, pipeline: string) =>
    call<RunSummary>("POST", `/api/v1/projects/${encodeURIComponent(name)}/draft-runs`, { ref, pipeline }),
  savePipeline: (name: string, req: SaveRequest) =>
    call<SaveResult>("POST", `/api/v1/projects/${encodeURIComponent(name)}/pipeline`, req),
  startRun: (name: string, ref: string) => call<RunSummary>("POST", `/api/v1/projects/${name}/runs`, { ref }),
  runners: (background = false) => call<Runner[]>("GET", "/api/v1/runners", undefined, { background }),
  validate: (yaml: string) => call<ValidateResult>("POST", "/api/v1/validate", yaml, { raw: true }),
  tokens: () => call<ApiToken[]>("GET", "/api/v1/tokens"),
  createToken: (name: string, expiresInDays: number) =>
    call<ApiToken & { token: string }>("POST", "/api/v1/tokens", { name, expires_in_days: expiresInDays }),
  revokeToken: (id: string) => call<void>("DELETE", `/api/v1/tokens/${encodeURIComponent(id)}`),
  audit: (q: AuditQuery) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
    return call<{ entries: AuditEntry[]; next_before: number }>("GET", "/api/v1/audit?" + p.toString());
  },
};
