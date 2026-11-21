import { call } from "./api";

// Feature toggles: a value an application reads at run time, so that something can be turned on, off or rolled out
// to a share of users without a deployment. The server decides everything (what a rule means, whether a change
// waits for an approval, who may do what); these are its answers and the calls the pages make.

export type ToggleType = "boolean" | "string" | "number" | "json";
export const TOGGLE_TYPES: ToggleType[] = ["boolean", "string", "number", "json"];
export const TOGGLE_KINDS = ["release", "kill", "experiment", "operational", "permission"] as const;

export interface Clause { attribute?: string; op: string; values?: unknown[]; negate?: boolean }
export interface Share { value: unknown; weight: number }
export interface Serve { value?: unknown; rollout?: { by?: string; buckets: Share[] } }
export interface Rule { id?: string; description?: string; clauses?: Clause[]; serve: Serve }
export interface ToggleState {
  on: boolean; off_value: unknown; prerequisites?: { toggle: string; value: unknown }[]; rules?: Rule[]; fallthrough: Serve;
}

export const OPERATORS: { id: string; label: string; needs: "one" | "many" | "none" }[] = [
  { id: "equals", label: "is", needs: "one" }, { id: "in", label: "is one of", needs: "many" },
  { id: "prefix", label: "starts with", needs: "many" }, { id: "suffix", label: "ends with", needs: "many" },
  { id: "contains", label: "contains", needs: "many" }, { id: "exists", label: "is present", needs: "none" },
  { id: "in_segment", label: "is in the segment", needs: "many" },
  { id: "semver_eq", label: "version equals", needs: "one" }, { id: "semver_lt", label: "version is below", needs: "one" },
  { id: "semver_lte", label: "version is at most", needs: "one" }, { id: "semver_gt", label: "version is above", needs: "one" },
  { id: "semver_gte", label: "version is at least", needs: "one" },
  { id: "number_eq", label: "number equals", needs: "one" }, { id: "number_lt", label: "number is below", needs: "one" },
  { id: "number_lte", label: "number is at most", needs: "one" }, { id: "number_gt", label: "number is above", needs: "one" },
  { id: "number_gte", label: "number is at least", needs: "one" },
];

export interface EnvView {
  environment: string; configured: boolean; state: ToggleState; mode: "off" | "on" | "targeted" | "rollout"; serves: string;
  version: number; updated_at?: number; updated_by?: string; settled_since?: number; waiting: number; watch_until?: number;
}
export interface StaleNote { reason: "settled" | "unused" | "expired"; detail: string }
export interface Toggle {
  level: string; scope?: string; key: string; type: ToggleType; kind: string; description: string; salt: string;
  default: unknown; safe_value: unknown; client_visible: boolean; owner: string; tags: string[]; expires_at?: number;
  origin?: string; lock?: { reason: string; by: string }; version: number; created_at: number; created_by: string; updated_at: number; updated_by: string;
  inherited: boolean; environments: EnvView[]; stale: StaleNote[]; last_evaluated?: Record<string, string>;
}
export interface Approval { user: string; at: number; comment?: string }
export interface Change {
  id: number; at: number; project: string; level: string; scope?: string; toggle: string; environment?: string; action: string; actor: string;
  reason: string; via: string; status: "applied" | "pending" | "rejected" | "withdrawn"; old?: unknown; new?: unknown;
  approvals: { need: number; by: Approval[] }; decided_by?: string; decided_at?: number; reverts?: number; reverted_by?: number; watch_until?: number;
}
export interface Usage { environment: string; evaluations: number; last_evaluated?: string; variations: Record<string, number> }
export interface KeyInfo { id: string; project: string; environment: string; kind: "server" | "client"; name: string; prefix: string; origins?: string[]; created_at: number; created_by: string; expires_at?: number; revoked_at?: number; last_used_at?: number }
export interface Guards { require_reason: boolean; require_approval: boolean; approvals: number; watch_minutes: number; from: Record<string, string>; locked_by?: Record<string, string> }
export interface Schedule { id: number; toggle: { key: string }; environment: string; run_at: number; reason: string; created_by: string; status: string; result?: string }
export interface Outcome { change: Change; waiting: boolean; needs?: string; also_undone?: number }
export interface RevertResult { change: number; toggle: string; reverted_by?: number; error?: string }

const p = (project: string) => `/api/v1/projects/${encodeURIComponent(project)}`;
const k = (key: string) => encodeURIComponent(key);

export const togglesApi = {
  list: (project: string, background = false) =>
    call<{ project: string; environments: string[]; toggles: Toggle[]; waiting: Change[] }>("GET", `${p(project)}/toggles`, undefined, { background }),
  get: (project: string, key: string, background = false) => call<Toggle>("GET", `${p(project)}/toggles/${k(key)}`, undefined, { background }),
  create: (project: string, body: { key: string; type: ToggleType; kind?: string; description?: string; owner?: string; expires?: string; default?: unknown; reason: string }) =>
    call<Toggle>("POST", `${p(project)}/toggles`, body),
  update: (project: string, key: string, body: Record<string, unknown>) => call<Toggle>("PATCH", `${p(project)}/toggles/${k(key)}`, body),
  remove: (project: string, key: string, reason: string) => call<unknown>("DELETE", `${p(project)}/toggles/${k(key)}?reason=${encodeURIComponent(reason)}`),
  setState: (project: string, key: string, env: string, body: { state: ToggleState; reason: string; at?: string }) =>
    call<Outcome | { scheduled: Schedule }>("PUT", `${p(project)}/toggles/${k(key)}/environments/${encodeURIComponent(env)}`, body),
  history: (project: string, key: string, background = false) => call<{ changes: Change[] }>("GET", `${p(project)}/toggles/${k(key)}/history`, undefined, { background }),
  usage: (project: string, key: string) => call<{ usage: Usage[]; privacy: string }>("GET", `${p(project)}/toggles/${k(key)}/usage`),
  tryContext: (project: string, key: string, context: Record<string, unknown>) =>
    call<{ values: Record<string, { value: unknown; reason: string; rule_id?: string }> }>("POST", `${p(project)}/toggles/${k(key)}/evaluate`, { context }),
  revert: (project: string, id: number, reason: string) => call<Outcome>("POST", `${p(project)}/toggle-changes/${id}/revert`, { reason }),
  decide: (project: string, id: number, what: "approve" | "reject" | "withdraw", comment: string) =>
    call<unknown>("POST", `${p(project)}/toggle-changes/${id}/${what}`, { comment }),
  timeline: (project: string, env: string, background = false) =>
    call<{ changes: Change[] }>("GET", `${p(project)}/toggle-timeline?environment=${encodeURIComponent(env)}`, undefined, { background }),
  revertSince: (project: string, env: string, since: number, reason: string) =>
    call<{ reverted: RevertResult[] }>("POST", `${p(project)}/toggle-revert-since`, { environment: env, since, reason }),
  schedules: (project: string) => call<{ schedules: Schedule[] }>("GET", `${p(project)}/toggle-schedules`),
  cancelSchedule: (project: string, id: number) => call<unknown>("DELETE", `${p(project)}/toggle-schedules/${id}`),
  safeguards: (project: string) => call<{ environments: Record<string, Guards> }>("GET", `${p(project)}/toggle-safeguards`),
  setSafeguards: (project: string, body: Record<string, unknown>) => call<Guards>("PUT", `${p(project)}/toggle-safeguards`, body),
  keys: (project: string) => call<{ keys: KeyInfo[] }>("GET", `${p(project)}/toggle-keys`),
  makeKey: (project: string, body: { environment: string; kind: string; name: string; origins?: string[] }) =>
    call<{ key: string; info: KeyInfo; note: string }>("POST", `${p(project)}/toggle-keys`, body),
  rotateKey: (project: string, id: string) => call<{ key: string; info: KeyInfo; note: string }>("POST", `${p(project)}/toggle-keys/${encodeURIComponent(id)}/rotate`, {}),
  revokeKey: (project: string, id: string) => call<unknown>("DELETE", `${p(project)}/toggle-keys/${encodeURIComponent(id)}`),
};

/** What a person typed for a value, in the toggle's type. */
export function valueOf(type: ToggleType, text: string): unknown {
  switch (type) {
    case "boolean": return text === "true";
    case "number": return Number(text);
    case "json": return JSON.parse(text);
    default: return text;
  }
}

export function showValue(v: unknown): string { return typeof v === "string" ? v : JSON.stringify(v); }
