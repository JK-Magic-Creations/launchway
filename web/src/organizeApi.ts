// Calls for how things are organized, attention and the Home page. They mirror internal/api/home.go,
// organize.go and search.go.

import { call } from "./api";

export type Area = "pipelines" | "repositories" | "projects" | "runs";

export interface AttentionItem {
  key: string; kind: "failed" | "deployment" | "decision" | "stuck"; tone: "bad" | "warn"; project: string; branch?: string; pull_request?: number;
  run_id?: string; run_number?: number; title: string; why: string; do: string; since: number; href: string;
}
export interface Retry { project: string; branch?: string; pull_request?: number; failed_id: string; failed_number: number; now_id: string; now_number: number; now_status: string }
export interface HomeRunning {
  id: string; project: string; number: number; branch?: string; pull_request?: number; status: string; step?: string; started_at: number; created_at: number;
  actor: string; message?: string; mine: boolean; unit?: string; retry_of?: number; waiting?: boolean;
}
export interface HomeProject { name: string; favorite: boolean; last?: string; last_id?: string; last_number?: number; last_at?: number; running: number; needs: number; history: string[] }
export interface HomeCategory { id: string; kind: "unit" | "none"; label: string; projects: HomeProject[]; needs: number; running: number; failing: number }
export interface HomeRecent { id: string; project: string; number: number; branch?: string; status: string; finished_at: number }
export interface HomeAnswer {
  view: "mine" | "all"; at: number; needs: AttentionItem[]; dismissed: AttentionItem[]; running: HomeRunning[]; categories: HomeCategory[];
  favorites: HomeProject[]; recent: HomeRecent[]; counts: Record<string, number>; layout: LayoutAnswer;
}
export interface FolderView { id: string; scope: string; area: string; parent: string; name: string; description: string; position: number; path: string; count: number; may_arrange: boolean }
export interface OrganizeAnswer {
  area: Area; folders: FolderView[]; items: Record<string, { folder?: string; labels?: string[] }>; favorites: string[];
  layout: LayoutAnswer; may_arrange_base: boolean;
}
export interface LayoutAnswer {
  page: string; scope: string; shared: Record<string, unknown>; personal: Record<string, unknown>; effective: Record<string, unknown>;
  levels: { scope: string; data: Record<string, unknown>; locked: boolean }[]; locked_at?: string; customized: boolean; may_arrange: Record<string, boolean>;
}
export interface SearchHit { kind: string; name: string; detail?: string; path?: string; href: string; score: number }

export interface TrailEntry { kind: string; obj: string; project: string; title: string; href: string; action: string; state: string; at: number; now_state?: string; changed?: string }
export interface Trail { continue: TrailEntry[]; recent: TrailEntry[]; paused: boolean; retention_days: number }

export const organizeApi = {
  trail: (limit = 100) => call<Trail>("GET", `/api/v1/activity?limit=${limit}`),
  note: (e: Partial<TrailEntry>) => call<void>("PUT", "/api/v1/activity", e),
  clearTrail: () => call<unknown>("DELETE", "/api/v1/activity"),
  pauseTrail: (paused: boolean) => call<unknown>("PUT", "/api/v1/activity/pause", { paused }),
  home: (view: "mine" | "all", background = false) => call<HomeAnswer>("GET", `/api/v1/home?view=${view}`, undefined, { background }),
  attention: () => call<{ needs: AttentionItem[]; dismissed: AttentionItem[]; retrying: Retry[]; count: number }>("GET", "/api/v1/attention"),
  dismiss: (key: string, snoozeHours = 0) => call<unknown>("POST", "/api/v1/attention/dismiss", { key, snooze_hours: snoozeHours }),
  restore: (key: string) => call<unknown>("POST", "/api/v1/attention/restore", { key }),
  organize: (area: Area) => call<OrganizeAnswer>("GET", `/api/v1/organize/${area}`),
  createFolder: (area: Area, f: { scope: string; name: string; parent?: string; description?: string }) => call<FolderView>("POST", `/api/v1/organize/${area}/folders`, f),
  renameFolder: (id: string, name: string) => call<FolderView>("PATCH", `/api/v1/organize/folders/${encodeURIComponent(id)}`, { name }),
  deleteFolder: (id: string) => call<unknown>("DELETE", `/api/v1/organize/folders/${encodeURIComponent(id)}`),
  move: (area: Area, item: string, folder: string) => call<unknown>("POST", `/api/v1/organize/${area}/move`, { item, folder }),
  labels: (area: Area, item: string, labels: string[]) => call<unknown>("PUT", `/api/v1/organize/${area}/labels`, { item, labels }),
  favorite: (item: string, on: boolean) => call<{ favorites: string[] }>("POST", "/api/v1/me/favorites", { item, on }),
  layout: (page: string, scope = "base") => call<LayoutAnswer>("GET", `/api/v1/layouts/${page}?scope=${encodeURIComponent(scope)}`),
  setLayout: (page: string, scope: string, data: Record<string, unknown>, locked = false) => call<LayoutAnswer>("PUT", `/api/v1/layouts/${page}`, { scope, data, locked }),
  setMyLayout: (page: string, data: Record<string, unknown>) => call<LayoutAnswer>("PUT", `/api/v1/me/layouts/${page}`, data),
  resetMyLayout: (page?: string) => call<unknown>("DELETE", page ? `/api/v1/me/layouts/${page}` : "/api/v1/me/layouts"),
  search: (q: string, limit = 20) => call<{ q: string; hits: SearchHit[]; favorites?: string[]; recent?: TrailEntry[] }>("GET", `/api/v1/search?q=${encodeURIComponent(q)}&limit=${limit}`),
  attentionRuns: (which: "needs" | "running", background = false) => call<import("./api").RunSummary[]>("GET", `/api/v1/runs?attention=${which}`, undefined, { background }),
};
