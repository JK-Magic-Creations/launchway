// Types and calls for run inputs, loops, schedules, webhooks and limited
// API tokens. They mirror internal/pipeline (inputs.go, loops.go,
// schedule.go, webhook.go), internal/source/triggers.go and
// internal/api/tokens.go.

import { call, type RunDetail, type RunSummary, type LibraryOverrides, type Problem, type Role } from "./api";

export type InputType = "string" | "choice" | "boolean" | "number" | "list";

/** One value a run is started with, as the pipeline declares it. */
export interface RunInput {
  name: string;
  type: InputType;
  description?: string;
  /** Canonical: true/false, a number as text, a JSON list for a list. */
  default?: string;
  required?: boolean;
  options?: string[];
  pattern?: string;
  min?: number;
  max?: number;
}

export interface InputsInfo {
  ref: string;
  sha: string;
  inputs: RunInput[];
  problems?: Problem[];
  libraries: string[];
}

/** What a form holds for one input. */
export type InputValue = string | boolean | string[];

/** extra: written by add, outside the lists; allowed_failure: continue_for lets it fail without failing the run. */
export interface LoopMember { id: string; each: Record<string, string>; extra?: boolean; allowed_failure?: boolean }
export interface Loop { id: string; description?: string; keys: string[]; members: LoopMember[]; max_parallel?: number; fail_fast: boolean }

export interface TriggerDetail {
  inputs?: Record<string, string>;
  schedule?: string;
  scheduled_for?: number;
  webhook?: string;
}

/** A run with what schema 13 adds to it. */
export type RunWithTriggers = RunDetail & {
  trigger_detail?: TriggerDetail;
  graph: RunDetail["graph"] & { inputs?: RunInput[]; loops?: Loop[] };
};

export interface ScheduleFire { key: string; fire_at: number; at: number; outcome: "started" | "error" | "skipped"; detail: string; run_id?: string }

export interface ScheduleView {
  key: string; cron: string; timezone: string; ref: string; inputs?: Record<string, string>;
  next: number; last?: ScheduleFire;
}

export interface WebhookView { name: string; ref: string; inputs?: Record<string, string>; configured: boolean; auth?: "token" | "hmac" }

export interface SchedulesView {
  enabled: boolean; branch: string; sha?: string; problem?: string; read_at: number;
  schedules: ScheduleView[]; fires: ScheduleFire[]; webhooks: WebhookView[]; git_hook?: string;
}

/** An API token as listed. Admins also see whose it is. */
export interface Token {
  id: string; name: string; created_at: number; expires_at: number; last_used: number;
  last_used_from?: string;
  /** The highest role it acts with; absent: its owner's. */
  role?: Role;
  /** Project patterns it is limited to; absent: every project. */
  projects?: string[];
  /** Hosted repositories it is limited to (and then to nothing else), with the access it has to them. */
  repositories?: { pattern: string }[];
  repo_access?: "read" | "write";
  /** Address ranges it may be used from; absent: anywhere. */
  source_ranges?: string[];
  user?: string;
}

/** A non-person account that owns tokens for automation. */
export interface ServiceAccount { name: string; description?: string; role: Role; disabled: boolean; created_at: number; created_by?: string; }

export interface NewToken {
  name: string; expires_in_days: number; role?: Role; projects?: string[];
  repositories?: string[]; repo_access?: "read" | "write"; source_ranges?: string[];
}

export interface TokenLimits { max_days: number; default_days: number; your_role: Role | ""; }

const enc = encodeURIComponent;

/** The value the server is sent for a form value. */
function wire(v: InputValue): string | boolean | string[] { return v; }

export const triggersApi = {
  inputs: (project: string, ref: string) => call<InputsInfo>("GET", `/api/v1/projects/${enc(project)}/inputs?ref=${enc(ref)}`),
  startRun: (project: string, ref: string, inputs: Record<string, InputValue>, libraries?: LibraryOverrides) => {
    const body: Record<string, unknown> = { ref, inputs: Object.fromEntries(Object.entries(inputs).map(([k, v]) => [k, wire(v)])) };
    if (libraries && Object.keys(libraries).length) body.libraries = libraries;
    return call<RunSummary>("POST", `/api/v1/projects/${enc(project)}/runs`, body);
  },
  schedules: (project: string, background = false) => call<SchedulesView>("GET", `/api/v1/projects/${enc(project)}/schedules`, undefined, { background }),
  tokens: () => call<Token[]>("GET", "/api/v1/tokens"),
  tokenLimits: () => call<TokenLimits>("GET", "/api/v1/tokens/limits"),
  createToken: (t: NewToken) => call<Token & { token: string }>("POST", "/api/v1/tokens", t),
  serviceAccounts: () => call<{ service_accounts: ServiceAccount[] }>("GET", "/api/v1/service-accounts").then((r) => r.service_accounts),
  createServiceAccount: (a: { name: string; role: Role; description: string }) => call<ServiceAccount>("POST", "/api/v1/service-accounts", a),
  updateServiceAccount: (name: string, change: { role?: Role; disabled?: boolean }) => call<ServiceAccount>("PATCH", `/api/v1/service-accounts/${enc(name)}`, change),
  deleteServiceAccount: (name: string) => call<void>("DELETE", `/api/v1/service-accounts/${enc(name)}`),
  createServiceToken: (name: string, t: NewToken) => call<Token & { token: string }>("POST", `/api/v1/service-accounts/${enc(name)}/tokens`, t),
  revokeToken: (id: string) => call<void>("DELETE", `/api/v1/tokens/${enc(id)}`),
  allTokens: () => call<Token[]>("GET", "/api/v1/admin/tokens"),
  revokeAnyToken: (id: string) => call<void>("DELETE", `/api/v1/admin/tokens/${enc(id)}`),
};

/** The form's starting value for an input: its default, or nothing. */
export function initialValue(i: RunInput): InputValue {
  const d = i.default;
  switch (i.type) {
    case "boolean": return d === "true";
    case "list": try { return d ? JSON.parse(d) as string[] : []; } catch { return []; }
    default: return d ?? "";
  }
}

/** Says what is wrong with a value before the server is asked, or "". The
 * server checks again and has the last word. */
export function checkValue(i: RunInput, v: InputValue): string {
  switch (i.type) {
    case "boolean": return "";
    case "list": return (v as string[]).length > 100 ? "a list has at most 100 items" : "";
    case "number": {
      const s = String(v).trim();
      if (s === "") return i.required && i.default === undefined ? "a value is required" : "";
      const n = Number(s);
      if (!Number.isFinite(n)) return `"${s}" is not a number`;
      if (i.min !== undefined && n < i.min) return `must be at least ${i.min}`;
      if (i.max !== undefined && n > i.max) return `must be at most ${i.max}`;
      return "";
    }
    case "choice": return i.options?.includes(String(v)) || (!i.required && v === "") ? "" : "choose one of the options";
    default: {
      const s = String(v);
      if (s === "") return i.required ? "a value is required" : "";
      if (i.pattern) {
        try { if (!new RegExp(i.pattern, "u").test(s)) return `does not match the pattern ${i.pattern}`; } catch { /* the server's syntax; it checks */ }
      }
      return "";
    }
  }
}
