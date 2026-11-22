// Calls for hosted repositories: SSH keys, deploy keys, protected branches and
// webhooks. They mirror internal/api/forge_keys.go and forge_hooks.go.

import { call } from "./api";

export interface HostedRepo {
  id: string; path: string; home?: string; name: string; default_branch?: string; visibility: string; archived?: boolean; mirror?: boolean;
  clone: { https?: string; ssh?: string };
}

export interface SshKey {
  id: string; kind: string; name: string; type: string; fingerprint: string; public_key: string; can_write?: boolean;
  created_at: string; expires_at?: string; last_used_at?: string; last_used_from?: string;
}

export interface HostKey { algorithm: string; type: string; fingerprint: string; public_key: string; created_at: string }

export interface SshInfo { enabled: boolean; host?: string; port?: number; certificates: boolean; host_keys: HostKey[] }

export interface Hook {
  id: string; scope: "repository" | "owner"; repository?: string; owner?: string; name: string; url: string; events: string[];
  active: boolean; has_secret: boolean; failures: number; disabled_reason?: string; created_at: string;
}

export interface Delivery {
  id: string; hook_id: string; event: string; state: "pending" | "delivered" | "failed"; attempts: number; status: number; duration_ms: number;
  error?: string; response?: string; test?: boolean; redelivery_of?: string; created_at: string; next_attempt?: string; payload: unknown;
}

export interface TagRule { pattern: string; create_role: string; change_role: string; pipelines: string[] }

export interface NewHook { name?: string; url: string; events: string[]; secret?: string; active?: boolean }

/** A hook's scope: one repository (owner and name) or every repository of an owner. */
export interface HookScope { owner: string; name?: string }

const enc = encodeURIComponent;
const repoBase = (o: string, n: string) => `/api/v1/repositories/${enc(o)}/${enc(n)}`;
const hooksBase = (s: HookScope) => (s.name ? `${repoBase(s.owner, s.name)}/hooks` : `/api/v1/owners/${enc(s.owner)}/hooks`);

export interface RepoReach {
  people: { who: string; kind: "user" | "group"; role: string }[];
  tokens: { id: string; name: string; owner: string; access: "read" | "write"; pattern: string; expires_at: number; last_used: number; source_ranges?: string[] }[];
}

export const forgeApi = {
  reach: (o: string, n: string) => call<RepoReach>("GET", `${repoBase(o, n)}/credentials`),
  makeCredential: (o: string, n: string, b: { name: string; access: "read" | "write"; expires_in_days: number; source_ranges?: string[] }) =>
    call<{ id: string; token: string }>("POST", `${repoBase(o, n)}/credentials`, b),
  repositories: () => call<{ items: HostedRepo[] }>("GET", "/api/v1/repositories").then((r) => r.items),
  repository: (o: string, n: string) => call<HostedRepo>("GET", repoBase(o, n)),
  /** Change a repository: name, default branch, visibility or archived (an administrator of it). */
  update: (o: string, n: string, b: { owner?: string; name?: string; default_branch?: string; visibility?: string; archived?: boolean }) => call<HostedRepo>("PATCH", repoBase(o, n), b),
  /** Move a repository to the trash: it stops being served, its data is kept. */
  remove: (o: string, n: string) => call<void>("DELETE", repoBase(o, n)),
  ssh: () => call<SshInfo>("GET", "/api/v1/ssh"),
  myKeys: () => call<{ items: SshKey[] }>("GET", "/api/v1/user/ssh-keys").then((r) => r.items),
  addMyKey: (b: { name: string; public_key: string; expires_at?: string }) => call<SshKey>("POST", "/api/v1/user/ssh-keys", b),
  deleteMyKey: (id: string) => call<void>("DELETE", `/api/v1/user/ssh-keys/${enc(id)}`),
  deployKeys: (o: string, n: string) => call<{ items: SshKey[] }>("GET", `${repoBase(o, n)}/deploy-keys`).then((r) => r.items),
  addDeployKey: (o: string, n: string, b: { name: string; public_key: string; write: boolean; expires_at?: string }) =>
    call<SshKey>("POST", `${repoBase(o, n)}/deploy-keys`, b),
  deleteDeployKey: (o: string, n: string, id: string) => call<void>("DELETE", `${repoBase(o, n)}/deploy-keys/${enc(id)}`),
  protectedBranches: (o: string, n: string) => call<{ patterns: string[] }>("GET", `${repoBase(o, n)}/protected-branches`).then((r) => r.patterns),
  setProtectedBranches: (o: string, n: string, patterns: string[]) =>
    call<{ patterns: string[] }>("PUT", `${repoBase(o, n)}/protected-branches`, { patterns }).then((r) => r.patterns),
  protectedTags: (o: string, n: string) => call<{ rules: TagRule[] }>("GET", `${repoBase(o, n)}/protected-tags`).then((r) => r.rules),
  setProtectedTags: (o: string, n: string, rules: TagRule[]) =>
    call<{ rules: TagRule[] }>("PUT", `${repoBase(o, n)}/protected-tags`, { rules }).then((r) => r.rules),
  hooks: (s: HookScope) => call<{ items: Hook[]; events: string[] }>("GET", hooksBase(s)),
  addHook: (s: HookScope, b: NewHook) => call<{ hook: Hook; secret?: string; secret_note?: string }>("POST", hooksBase(s), b),
  updateHook: (s: HookScope, id: string, b: Partial<NewHook>) => call<Hook>("PATCH", `${hooksBase(s)}/${enc(id)}`, b),
  deleteHook: (s: HookScope, id: string) => call<void>("DELETE", `${hooksBase(s)}/${enc(id)}`),
  testHook: (s: HookScope, id: string) => call<Delivery>("POST", `${hooksBase(s)}/${enc(id)}/test`, {}),
  deliveries: (s: HookScope, id: string) => call<{ items: Delivery[] }>("GET", `${hooksBase(s)}/${enc(id)}/deliveries`).then((r) => r.items),
  redeliver: (s: HookScope, id: string, delivery: string) => call<Delivery>("POST", `${hooksBase(s)}/${enc(id)}/deliveries/${enc(delivery)}/redeliver`, {}),
};
