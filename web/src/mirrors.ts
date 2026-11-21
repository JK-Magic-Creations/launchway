// Types and calls for the mirrors of hosted repositories. They mirror
// internal/forge/mirror and internal/api/forge_mirror.go.

import { call } from "./api";

export interface Repository {
  id: string;
  path: string;
  home?: string;
  name: string;
  default_branch?: string;
  visibility: "private" | "internal" | "public";
  archived?: boolean;
  mirror?: boolean;
  clone: { https?: string; ssh?: string };
}

/** A mirror as the server shows it: where the credential is read from, never the credential. */
export interface Mirror {
  id: string;
  repository: string;
  direction: "pull" | "push";
  url: string;
  username?: string;
  credential?: string;
  interval_seconds: number;
  enabled: boolean;
  prune: boolean;
  state: "pending" | "ok" | "error";
  running?: boolean;
  last_attempt?: string;
  last_success?: string;
  last_error?: string;
  failures: number;
  next_run?: string;
  refs: number;
  lag_seconds: number;
  behind: boolean;
}

export interface NewMirror {
  direction: "pull" | "push";
  url: string;
  username?: string;
  credential?: { env?: string; file?: string };
  interval_seconds?: number;
  prune?: boolean;
}

const base = (path: string) => "/api/v1/repositories/" + path.split("/").map(encodeURIComponent).join("/");

export const mirrorsApi = {
  repository: (path: string) => call<Repository>("GET", base(path)),
  mirrors: (path: string, background = false) => call<{ items: Mirror[] }>("GET", base(path) + "/mirrors", undefined, { background }).then((p) => p.items),
  add: (path: string, m: NewMirror) => call<Mirror>("POST", base(path) + "/mirrors", m),
  sync: (path: string, id: string) => call<Mirror>("POST", `${base(path)}/mirrors/${encodeURIComponent(id)}/sync`),
  pause: (path: string, id: string, enabled: boolean) => call<Mirror>("PATCH", `${base(path)}/mirrors/${encodeURIComponent(id)}`, { enabled }),
  remove: (path: string, id: string) => call<void>("DELETE", `${base(path)}/mirrors/${encodeURIComponent(id)}`),
  stop: (path: string, finalSync: boolean) => call<Repository>("POST", base(path) + "/stop-mirroring", { final_sync: finalSync }),
};

// ---- import ----

export interface Connection { name: string; kind: string; base_url?: string; credential?: string; enabled: boolean }

export interface SourceRequest { connection?: string; urls?: string[]; owner?: string; repository?: string; target_owner?: string; username?: string }

export interface ImportItem {
  source: string; repository: string; url: string; visibility: string;
  action: "create" | "resume" | "skip"; state: "planned" | "queued" | "verified" | "skipped" | "failed";
  reason?: string; error?: string; source_refs?: number; hosted_refs?: number; verified?: boolean; differences?: string[];
}
export interface ImportResult { dry_run: boolean; items: ImportItem[]; summary: Record<string, number> }

export const importApi = {
  connections: () => call<{ items: Connection[] }>("GET", "/api/v1/connections").then((p) => p.items),
  addConnection: (c: { name: string; kind: string; base_url?: string; credential?: { env?: string; file?: string } }) => call<Connection>("POST", "/api/v1/connections", c),
  plan: (s: SourceRequest, mirror: boolean) => call<ImportResult>("POST", "/api/v1/imports", { ...s, mirror, dry_run: true }),
  one: (s: SourceRequest, repository: string, mirror: boolean) => call<ImportResult>("POST", "/api/v1/imports", { ...s, repository, mirror, wait: true }),
};
