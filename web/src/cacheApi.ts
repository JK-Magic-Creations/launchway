import { call } from "./api";

// The API of build caches: what a step's caches did, and what a project keeps.

declare module "./api" {
  interface StepView {
    /** What each of the step's build caches did in its latest attempt. */
    caches?: CacheResult[];
  }
}

/** What one cache of a step did: restored (hit, fallback), missed, saved or not saved. */
export interface CacheResult {
  name: string;
  /** The key worked out for the step (64 hex digits). */
  key?: string;
  /** What the key was made of: image digest, platform, versions, key files. */
  key_parts?: { label: string; value: string }[];
  result: "hit" | "fallback" | "miss" | "saved" | "not_saved";
  /** When saved or not saved: what the restore found. */
  restored?: "hit" | "fallback" | "miss";
  bytes?: number;
  scope?: string;
  /** Why there was no entry, or why nothing was saved. */
  reason?: string;
  seconds?: number;
}

/** A project's cache policy as it applies to it. */
export interface CachePolicy {
  enabled: boolean;
  max_project_gib: number;
  max_entry_gib: number;
  idle_days: number;
  /** Whose policy it is: the project's own, the installation's, or what is built in. */
  source: "project" | "installation" | "default";
}

export interface CacheEntry {
  id: string;
  name: string;
  key: string;
  /** default, ref:<ref> or pull:<n>. */
  scope: string;
  platform: string;
  bytes: number;
  files: number;
  status: "kept" | "uploading";
  created_at: number;
  last_used_at: number;
  hits: number;
  /** The run that wrote it. */
  run_id: string;
  run_number: number;
}

export interface CacheList {
  project: string;
  entries: CacheEntry[];
  totals: { entries: number; bytes: number; quota_bytes: number; max_entry_bytes: number; idle_days: number };
  policy: CachePolicy;
}

export const cacheApi = {
  list: (project: string, background = false) =>
    call<CacheList>("GET", `/api/v1/projects/${encodeURIComponent(project)}/caches`, undefined, { background }),
  deleteEntry: (project: string, id: string) =>
    call<{ ended: number; bytes: number }>("DELETE", `/api/v1/projects/${encodeURIComponent(project)}/caches/${encodeURIComponent(id)}`),
  deleteCachesNamed: (project: string, name: string) =>
    call<{ ended: number; bytes: number }>("DELETE", `/api/v1/projects/${encodeURIComponent(project)}/caches?name=${encodeURIComponent(name)}`),
};
