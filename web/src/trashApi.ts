// Calls for the repository trash. They mirror internal/api/forge_trash.go.

import { call } from "./api";
import type { HostedRepo } from "./forgeApi";

export interface TrashedRepo extends HostedRepo {
  deleted_at: string; deleted_by: string; purge_at: string; name_taken?: boolean; purging?: boolean;
}

export interface PurgeSummary { path: string; packs: number; blobs_deleted: number; blobs_kept: number; bytes: number }

const enc = encodeURIComponent;

export const trashApi = {
  list: () => call<{ items: TrashedRepo[] }>("GET", "/api/v1/repository-trash").then((r) => r.items),
  restore: (id: string, b: { owner?: string; name?: string } = {}) => call<HostedRepo>("POST", `/api/v1/repository-trash/${enc(id)}/restore`, b),
  purge: (id: string, confirm: string) => call<PurgeSummary>("DELETE", `/api/v1/repository-trash/${enc(id)}`, { confirm }),
};
