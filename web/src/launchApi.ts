// What a Launch page shows of a delivery: the live tree of the objects it made, manifests, logs, and the few
// controls on its workload. These mirror internal/deploy/inspect and internal/api/inspect.go.

import { call } from "./api";

export type Health = "Healthy" | "Progressing" | "Degraded" | "Suspended" | "Missing" | "Completed" | "Unknown";

export interface LEvent { type: string; reason: string; message: string; count: number; last: number }
export interface LContainer { name: string; image: string; init?: boolean; ready: boolean; restarts: number; state: string; reason?: string; exit_code?: number; previous?: boolean }
export interface LWeight { backend: string; weight: number; match?: string }
export interface LNode {
  id: string; kind: string; group?: string; namespace?: string; name: string; parent?: string; section?: string;
  health: Health; message?: string; created?: number; change?: "new" | "changed" | "unchanged" | "leftover";
  role?: string; revision?: string; hash?: string; image?: string;
  replicas?: { desired: number; ready: number; available: number; updated: number };
  containers?: LContainer[]; not_ready?: { why: string; last_logs?: string[] };
  keys?: string[]; data?: Record<string, string>; selects?: string[]; ports?: string[]; weights?: LWeight[]; bound?: string[]; used_by?: string[];
  events?: LEvent[];
}
export interface LStep { index: number; kind: string; label: string; weight?: number; seconds?: number; state: "done" | "current" | "next" }
export interface LPoint { at?: number; phase: string; value?: string; note?: string }
export interface LMetric { name: string; provider?: string; query?: string; success?: string; failure?: string; phase: string; points?: LPoint[] }
export interface LAnalysis { name: string; phase: string; message?: string; metrics: LMetric[]; jobs?: string[]; created?: number }
export interface LRollout {
  name: string; namespace: string; kind: string; strategy: "canary" | "blueGreen" | "rolling"; phase: string; message?: string;
  paused: boolean; user_paused?: boolean; aborted: boolean; steps: LStep[]; current: number;
  desired_weight: number; actual_weight: number; actual_source?: string; services?: Record<string, string>;
  pause_started?: number; pause_ends?: number; pre_traffic?: { address: string; live_weight: number }; analysis?: LAnalysis[];
}
export interface LTree {
  application?: string; namespaces: string[]; at: number; since?: number; nodes: LNode[]; notes?: string[]; truncated?: boolean; rollout?: LRollout;
}
export interface LAction { action: "pause" | "resume" | "restart" | "scale"; ok: boolean; why?: string }
export interface TreeView {
  tree: LTree; actions: LAction[]; run: string; deployment: string; project: string; environment: string; application: string; target: string;
  closed: boolean; protected: boolean; can_logs: boolean; can_control: boolean; why?: string;
}
export interface LogLine { t: number; src: string; line: string }
export interface LogQuery { pod?: string; container?: string; replica_set?: string; role?: string; job?: string; previous?: boolean; tail?: number }

const base = (run: string, dep: string) => `/api/v1/runs/${encodeURIComponent(run)}/deployments/${encodeURIComponent(dep)}/inspect`;

export function logQuery(q: LogQuery): string {
  const p = new URLSearchParams();
  if (q.pod) p.set("pod", q.pod);
  if (q.container) p.set("container", q.container);
  if (q.replica_set) p.set("replica_set", q.replica_set);
  if (q.role) p.set("role", q.role);
  if (q.job) p.set("job", q.job);
  if (q.previous) p.set("previous", "true");
  if (q.tail) p.set("tail", String(q.tail));
  return p.toString();
}

export const launchApi = {
  tree: (run: string, dep: string, background = true) => call<TreeView>("GET", base(run, dep), undefined, { background }),
  manifest: (run: string, dep: string, object: string) =>
    call<{ object: string; yaml: string }>("GET", `${base(run, dep)}/manifest?object=${encodeURIComponent(object)}`),
  logs: (run: string, dep: string, q: LogQuery) =>
    call<{ lines: LogLine[]; notes?: string[] }>("GET", `${base(run, dep)}/logs?${logQuery(q)}`),
  streamUrl: (run: string, dep: string, q: LogQuery) => `${base(run, dep)}/logs/stream?${logQuery(q)}`,
  control: (run: string, dep: string, body: { action: string; object: string; replicas?: number; reason: string }) =>
    call<{ result: string; detail: string }>("POST", `${base(run, dep)}/control`, body),
};
