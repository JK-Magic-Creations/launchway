// Types and calls for dependency updates. They mirror internal/api/depupdates.go
// and internal/depupdate (the report the server makes of a repository's default branch).

import { call } from "./api";

export interface Advisory { id: string; severity: string; score: number; fixed_in?: string; summary: string; url?: string }
export interface Dep { ecosystem: string; name: string; current: string; constraint?: string; direct: boolean; group?: string; file: string; line?: number }
export interface DepUpdate {
  dep: Dep; status: string; reason?: string; note?: string; latest?: string; target?: string; kind?: string; security?: boolean;
  advisories?: Advisory[]; published?: string; held_until?: string; major?: { target: string; published?: string };
}
export interface Decision {
  key: string; title: string; ecosystem: string; directory: string; security: boolean; action: "open" | "update" | "keep" | "queued" | "close";
  why: string; number?: number; updates: DepUpdate[];
}
export interface Policy {
  enabled: boolean; security: boolean; routine: boolean; cooldown_days: number;
  ecosystems: Record<string, { enabled: boolean }>; schedule: { interval: string; day?: string; hour: number; timezone?: string };
  limits: { max_open: number; max_per_run: number; max_open_per_ecosystem?: number }; auto_merge: { enabled: boolean };
  locked?: Record<string, string>; ignored?: string[];
}
export interface DependencyReport {
  repository: string; branch: string; commit: string; policy: Policy; files: string[]; updates: DepUpdate[]; decisions: Decision[];
  notes?: { file: string; message: string }[]; errors?: { file: string; message: string }[];
  vulnerability_error?: string; settings_file_error?: string; coming: string[]; ecosystems_available: string[];
}

const base = (path: string) => `/api/v1/repositories/${path}`;

export const depsApi = {
  report: (path: string) => call<DependencyReport>("GET", `${base(path)}/dependencies`),
  preview: (path: string) => call<DependencyReport>("GET", `${base(path)}/updates/preview`),
};
