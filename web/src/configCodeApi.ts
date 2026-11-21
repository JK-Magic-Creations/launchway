import { call } from "./api";

// The API of configuration as code: the run reports of Ansible and plan steps, the project's Configuration page,
// and the built-in state's workspaces. Nothing here ever carries a secret value.

export interface ConfigReportRow {
  id: string;
  run_id: string;
  step: string;
  attempt: number;
  project: string;
  environment?: string;
  kind: "ansible" | "plan";
  commit: string;
  check: boolean;
  status: string;
  recap: string;
  created_at: number;
  run_number?: number;
}

export interface AnsibleDiff { before?: string; after?: string; text: string }
export interface AnsibleResult { status: "ok" | "changed" | "failed" | "skipped" | "unreachable"; changed?: boolean; duration_ms?: number; msg?: string; ignored?: boolean; diff?: AnsibleDiff[] }
export interface AnsibleTask { name: string; action?: string; handler?: boolean; no_log?: boolean; results: Record<string, AnsibleResult> }
export interface AnsiblePlay { name: string; tasks: AnsibleTask[] }
export interface AnsibleHost { host: string; outcome: "ok" | "changed" | "failed" | "unreachable"; changed_tasks?: string[]; ok: number; changed: number; failures: number; unreachable: number; skipped: number }
export interface AnsibleDocument {
  check: boolean; diff: boolean; duration_ms: number; ansible_version?: string; playbook?: string; environment?: string;
  plays: AnsiblePlay[]; hosts: AnsibleHost[];
  summary: { hosts: number; changed: number; ok: number; failed: number; unreachable: number; recap: string };
  failure?: { play: string; task: string; host: string; status: string; msg: string };
}

export interface PlanAttr { path: string; before?: string; after?: string; sensitive?: boolean; forces_replacement?: boolean }
export interface PlanResource { address: string; type: string; action: "create" | "update" | "delete" | "replace" | "read"; reason?: string; attrs?: PlanAttr[]; attrs_cut?: boolean }
export interface PlanDocument {
  plan: {
    tool_version?: string; summary: string;
    counts: { add: number; change: number; destroy: number; replace: number };
    resources: PlanResource[]; outputs: { name: string; action: string; sensitive?: boolean }[];
    risk: { level: "none" | "low" | "high"; destroyed?: string[]; replaced?: string[]; notes?: string[] };
  };
  meta?: Record<string, string>;
}

export interface ConfigReport<D = AnsibleDocument | PlanDocument> {
  report: ConfigReportRow;
  summary: { failed_hosts?: string[]; drift?: { drifted: number; hosts: number; text: string; clean: boolean; items: { host: string; tasks: string[] }[] } | null };
  document: D;
}

export interface EnvironmentConfig { name: string; ansible?: ConfigReportRow; drift?: ConfigReportRow; plan?: ConfigReportRow; last_good?: ConfigReportRow }
export interface StateWorkspace { name: string; serial: number; version: number; environment?: string; lock?: { actor: string; run_id?: string } }
export interface ConfigurationOverview { environments: EnvironmentConfig[]; state: StateWorkspace[]; sealed: boolean }

const enc = encodeURIComponent;

export const configApi = {
  overview: (project: string, background = false) =>
    call<ConfigurationOverview>("GET", `/api/v1/projects/${enc(project)}/configuration`, undefined, { background }),
  report: (run: string, step: string, kind?: string) =>
    call<ConfigReport>("GET", `/api/v1/runs/${enc(run)}/config-reports/${enc(step)}${kind ? `?kind=${enc(kind)}` : ""}`),
  rerunFailed: (run: string, step: string) =>
    call<{ run: { id: string; number: number }; hosts: string[] }>("POST", `/api/v1/runs/${enc(run)}/config-reports/${enc(step)}/rerun-failed`, {}),
};
