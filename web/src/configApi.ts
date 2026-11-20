// The configuration API (/api/v1/config): types and calls. Types mirror
// internal/settings and internal/engine/configstore.go.

import { useEffect, useState } from "react";
import { ApiError, call } from "./api";

export interface LevelName { singular: string; plural: string }
export interface Levels { installation: LevelName; organisation: LevelName; unit: LevelName; project: LevelName }

export interface ConfigKind {
  /** As stored: project. */
  name: string;
  /** In URLs: projects. */
  path: string;
  title: string;
  plural: string;
  /** The name of the only object of a singleton kind (installation, organisation). */
  singleton?: string;
  /** The object may be defined at a unit. */
  levels: boolean;
  doc: string;
  file_key: string;
}

export interface UnitView { name: string; display_name: string; description?: string; projects: string[] }

export interface ConfigOverview {
  available: boolean;
  kinds: ConfigKind[];
  levels: Levels;
  organisation?: { display_name?: string; description?: string };
  units?: UnitView[];
  units_in_use?: boolean;
  status?: ConfigStatus;
  admin?: boolean;
}

/** What the servers run: the stored revision, this server's applied revision and each server's last report. */
export interface ConfigStatus {
  revision: number; stored_revision?: number; server?: string; problem?: string;
  safe_mode?: { reason: string; using: string };
  servers?: { server: string; revision: number; state: "applied" | "failed"; problem?: string; at: number }[];
}

export type Spec = Record<string, unknown>;

export interface ConfigObject {
  kind: string; name: string; unit: string; spec: Spec; version: number;
  managed_by: string; created_at: number; created_by: string; updated_at: number; updated_by: string;
  deleted: boolean; deleted_at?: number; deleted_by?: string;
}

export interface ConfigChange {
  id: number; revision: number; kind: string; name: string; version: number;
  action: "create" | "update" | "delete" | "restore"; actor: string; at: number; managed_by: string;
  unit_before: string; unit_after: string; before?: Spec; after?: Spec; import_id?: string; comment?: string;
}

export interface ChangeRequest {
  kind: string; name: string; action?: "put" | "delete" | "restore"; unit?: string; spec?: Spec;
  /** Always sent: -1 for an object that does not exist yet, otherwise the version that was read. */
  version: number; comment?: string;
}

export interface EffectiveEntry { kind: string; name: string; from: string; unit?: string }
export interface EffectiveSetting { key: string; value: unknown; from: string }
export interface Effective { project: string; unit?: string; objects: EffectiveEntry[]; settings: EffectiveSetting[] }

export interface RepoCheck {
  ok: boolean; problem?: string; blocked?: boolean; blocked_host?: string; default_branch?: string; branches: string[]; has_pipeline_file: boolean;
  /** The pipeline file found, the commit it was read at, and whether it is valid there. */
  pipeline_file?: string; commit?: string; valid?: boolean;
  problems?: { file: string; line?: number; column?: number; commit: string; message: string }[];
}

export interface SignInRequest {
  issuer: string; client_id: string; redirect_url: string; client_secret_env?: string;
  groups_claim?: string; username_claim?: string; admin_groups?: string[]; admin_users?: string[];
}
export interface SignInCheck {
  ok: boolean; problems: string[]; snippet: string;
  /** The server refused to contact the address (it is internal); blocked_host is what an administrator may allow. */
  blocked?: boolean; blocked_host?: string;
  endpoints: { authorization?: string; token?: string; jwks?: string; claims_offered?: string[]; groups_claim_hint?: string };
}

export interface LibraryInfo { name: string; last_fetch: number }
export interface TemplateParam { name: string; type: string; default?: string; description?: string; options?: string[] }
export interface TemplateInfo { name: string; description?: string; params: TemplateParam[]; outputs: string[]; problems?: { message: string }[] }
export interface LibraryTemplates { library: string; version: string; commit: string; versions: string[]; templates: TemplateInfo[]; truncated?: boolean }

const enc = encodeURIComponent;

/** lower reads a Go struct written without json tags (Singular) as the API
 * documents it (singular). */
function lower<T>(v: unknown): T {
  if (Array.isArray(v)) return v.map((x) => lower(x)) as T;
  if (v && typeof v === "object") {
    return Object.fromEntries(Object.entries(v as Record<string, unknown>).map(([k, x]) =>
      [k.replace(/^[A-Z]/, (c) => c.toLowerCase()).replace(/([a-z])([A-Z])/g, "$1_$2").toLowerCase(), lower(x)])) as T;
  }
  return v as T;
}

export const configApi = {
  overview: () => call<ConfigOverview>("GET", "/api/v1/config").then((o) => ({
    ...o, levels: lower<Levels>(o.levels), organisation: o.organisation ? lower<ConfigOverview["organisation"]>(o.organisation) : undefined,
    units: o.units ? lower<UnitView[]>(o.units) : undefined,
  })),
  list: (kind: string, deleted = false) =>
    call<{ kind: ConfigKind; objects: ConfigObject[] }>("GET", `/api/v1/config/${enc(kind)}${deleted ? "?deleted=1" : ""}`),
  get: (kind: string, name: string) => call<ConfigObject>("GET", `/api/v1/config/${enc(kind)}/${enc(name)}`),
  create: (kind: string, body: { name: string; unit?: string; spec: Spec; comment?: string }) =>
    call<{ object: ConfigObject; changed: boolean }>("POST", `/api/v1/config/${enc(kind)}`, body),
  /** A change always names the version it read: the server refuses it if somebody changed the object meanwhile. A version of 0 or less is an object never written (a setting still at its defaults): it is created. */
  put: (kind: string, name: string, body: { unit?: string; spec: Spec; comment?: string; version: number; take_ownership?: boolean }) =>
    body.version > 0
      ? call<{ object: ConfigObject; changed: boolean }>("PUT", `/api/v1/config/${enc(kind)}/${enc(name)}`, body)
      : call<{ object: ConfigObject; changed: boolean }>("POST", `/api/v1/config/${enc(kind)}`, { name, unit: body.unit, spec: body.spec, comment: body.comment }),
  remove: (kind: string, name: string, version: number, comment?: string, take_ownership?: boolean) =>
    call<{ object: ConfigObject }>("DELETE", `/api/v1/config/${enc(kind)}/${enc(name)}`, { version, comment, take_ownership }),
  restore: (kind: string, name: string, version: number, comment?: string) =>
    call<{ object: ConfigObject }>("POST", `/api/v1/config/${enc(kind)}/${enc(name)}/restore`, { version, comment }),
  history: (kind: string, name: string) =>
    call<{ changes: ConfigChange[] }>("GET", `/api/v1/config/${enc(kind)}/${enc(name)}/history`),
  allHistory: (limit = 30) => call<{ changes: ConfigChange[] }>("GET", `/api/v1/config/history?limit=${limit}`),
  effective: (project: string) => call<Effective>("GET", `/api/v1/config/effective/${enc(project)}`),
  /** The server's own checks of changes, storing nothing. Problems are its messages. */
  validate: async (changes: ChangeRequest[]): Promise<string[]> => {
    try {
      const r = await call<{ valid: boolean; problems?: string[] }>("POST", "/api/v1/config/validate", { changes }, { background: true });
      return r.problems ?? [];
    } catch (e) {
      // A server may answer a refused change with 400 and the problems.
      if (e instanceof ApiError && e.status === 400) {
        const ps = (e.problems as unknown as string[] | undefined) ?? [];
        return ps.length ? ps.map((p) => (typeof p === "string" ? p : (p as { message: string }).message)) : [e.message];
      }
      throw e;
    }
  },
  /** All or none: what a wizard creates. */
  apply: (changes: ChangeRequest[]) =>
    call<{ results: { object: ConfigObject; changed: boolean }[] }>("POST", "/api/v1/config/apply", { changes }),
  checkRepository: (repo: string, branch?: string) =>
    call<RepoCheck>("POST", "/api/v1/config/check/repository", { repo, branch: branch || undefined }),
  checkSignIn: (req: SignInRequest) => call<SignInCheck>("POST", "/api/v1/config/check/signin", req),
  libraries: () => call<LibraryInfo[]>("GET", "/api/v1/libraries"),
  templates: (library: string, version?: string) =>
    call<LibraryTemplates>("GET", `/api/v1/libraries/${enc(library)}/templates${version ? "?version=" + enc(version) : ""}`),
};

/** A check the server does not offer answers 404 (an older server). */
export function notOffered(e: unknown): boolean {
  return e instanceof ApiError && (e.status === 404 || e.status === 405);
}

// The overview names the levels everywhere; it is read once and shared.
let overviewCache: ConfigOverview | null = null;
let overviewWait: Promise<ConfigOverview> | null = null;
const listeners = new Set<(o: ConfigOverview) => void>();

export function loadOverview(force = false): Promise<ConfigOverview> {
  if (!force && overviewCache) return Promise.resolve(overviewCache);
  if (!force && overviewWait) return overviewWait;
  overviewWait = configApi.overview().then((o) => {
    overviewCache = o; overviewWait = null;
    listeners.forEach((l) => l(o));
    return o;
  }, (e) => { overviewWait = null; throw e; });
  return overviewWait;
}

/** The overview, reloaded after a change that can rename levels or add units. */
export function useOverview(): { overview: ConfigOverview | null; error: unknown } {
  const [overview, setOverview] = useState<ConfigOverview | null>(overviewCache);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    listeners.add(setOverview);
    loadOverview().then(setOverview, setError);
    return () => { listeners.delete(setOverview); };
  }, []);
  return { overview, error };
}

export function kindOf(o: ConfigOverview | null, pathOrName: string): ConfigKind | undefined {
  return o?.kinds.find((k) => k.path === pathOrName || k.name === pathOrName);
}

/** The name people read for a kind: the levels' names where a kind is a level. */
export function kindLabel(o: ConfigOverview | null, k: ConfigKind, plural = true): string {
  const lv = o?.levels;
  if (lv) {
    if (k.name === "project") return plural ? lv.project.plural : lv.project.singular;
    if (k.name === "unit") return plural ? lv.unit.plural : lv.unit.singular;
    if (k.name === "organisation") return lv.organisation.singular;
    if (k.name === "installation") return `${lv.installation.singular} settings`;
  }
  return plural ? k.plural : k.title;
}

/** Whether a kind is shown: units only when they are in use (or asked for directly). */
export function kindShown(o: ConfigOverview | null, k: ConfigKind): boolean {
  return k.name !== "unit" || !!o?.units_in_use;
}

/** The display name of the level an object is defined at. */
export function levelOf(o: ConfigOverview | null, unit: string): string {
  if (!unit) return o?.organisation?.display_name || o?.levels.organisation.singular || "Organisation";
  const u = o?.units?.find((x) => x.name === unit);
  return `${o?.levels.unit.singular ?? "Unit"} ${u?.display_name || unit}`;
}
