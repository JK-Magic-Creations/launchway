import { call } from "./api";

// The API of Standing orders: the profile of checks, the settings of each
// level, a repository's effective profile, its findings and a run's Inspection.

export type State = "off" | "advise" | "block";
export type Sev = "critical" | "high" | "medium" | "low" | "unknown";

export interface Source { kind: "default" | "level" | "file"; level?: string; scope?: string }
export interface LockInfo { level: string; scope?: string; reason: string; who: string }
export interface Link { kind: string; level?: string; scope?: string; display: string; overridden?: boolean; ignored?: string }
export interface Value<T> { value: T; from: Source; locked?: LockInfo; chain?: Link[] }

export interface EffectiveCheck {
  id: string; title: string; why: string; kind: string; tool: string; ecosystem: string; gates: boolean; network?: string; fix: string;
  budget_seconds: number; state: Value<State>; applies: boolean; because: string; active: boolean; reason: string; unavailable?: string; license?: string; image?: string;
}
export interface Exemption {
  check?: string; rule?: string; package?: string; path?: string; fingerprint?: string; status?: string; reason: string; owner?: string; expires: string; from: Source; expired?: boolean;
}
export interface Effective {
  profile_version: string;
  off: Value<boolean>; off_reason?: string;
  checks: EffectiveCheck[];
  fail_on: Value<Sev>; require_fix: Value<boolean>; default_branch: Value<State>; incomplete: Value<string>;
  licenses_allow: Value<string[]>; licenses_deny: Value<string[]>;
  exemptions: Exemption[]; detected: { ecosystem: string; title: string; because: string }[]; notices: string[];
}
export interface ProjectStandards {
  effective: Effective; commit?: string; branch?: string; file: boolean; file_problems?: string[]; file_text?: string; empty?: boolean;
}

export interface ProfileCheck {
  id: string; title: string; why: string; kind: string; tool: string; ecosystem: string; default: State; gates: boolean; network?: string; fix: string;
  license?: string; image?: string; budget_seconds: number; available: boolean; unavailable?: string; outputs: string[];
}
export interface Profile {
  version: string; checks: ProfileCheck[]; ecosystems: { id: string; title: string }[]; key_names: Record<string, string>; file: string;
  defaults: { fail_on: Sev; require_fix: boolean; default_branch: State; incomplete: string; licenses_deny: string[] };
  words: Record<string, string>;
}

export interface Settings {
  off?: boolean; off_reason?: string;
  checks?: Record<string, State>;
  gate?: { fail_on?: Sev; require_fix?: boolean; default_branch?: State; incomplete?: string };
  licenses?: { allow?: string[]; deny?: string[] };
  ignore?: { check?: string; rule?: string; package?: string; path?: string; status?: string; reason: string; owner?: string; expires: string }[];
  locks?: Record<string, string>;
}
export interface LevelSettings { level: string; scope: string; settings: Settings; version: number; words: Record<string, string> }

export interface Decision { status: string; reason: string; by: string; at: number; expires_at: number; active: boolean }
export interface Tracked {
  fingerprint: string; check?: string; tool: string; rule: string; severity: Sev; kind: string; title: string; file?: string; line?: number;
  package?: string; version?: string; fixed_in?: string; advisories?: string[]; reachable?: string; fix?: string;
  first_run: number; first_seen: number; last_run: number; last_seen: number; status: string; fixed_at?: number; decision?: Decision;
}
export interface BacklogList {
  findings: Tracked[]; matched: number; by_severity: Record<string, number>; by_check: Record<string, number>; open: number; accepted: number; fixed: number;
}
export interface Series {
  project: string; points: { day: string; total: number; critical: number; high: number; medium: number; low: number }[];
  mean_days_to_fix: number; fixed_count: number; oldest_open?: number; open: number; by_severity: Record<string, number>;
}

export type Standing = "new" | "existing" | "accepted" | "false-positive" | "unscored";
export interface InspectionFinding {
  fingerprint: string; check?: string; tool: string; rule: string; severity: Sev; kind: string; title: string; file?: string; line?: number;
  package?: string; version?: string; fixed_in?: string; advisories?: string[]; reachable?: string; fix?: string; help_uri?: string;
  standing: Standing; gating?: boolean; decision?: Decision; step: string;
}
export interface Inspection {
  present: boolean; blocked: boolean; summary: string; default_branch: boolean; no_baseline?: boolean; pending?: boolean; profile_version?: string; fail_on?: string;
  counts: Record<string, number>; checks: { id: string; title: string; state: string; ran: boolean; new: number; total: number }[];
  notices?: string[]; incomplete?: string[]; findings: InspectionFinding[]; matched: number; truncated?: boolean;
}
export interface CoverageRow {
  project: string; unit?: string; off: boolean; off_by?: string; off_reason?: string; empty?: boolean;
  checks: Record<string, { state: State; active: boolean; locked?: boolean }>; open: Record<string, number>; notices: number;
}
export interface Coverage { level: string; scope: string; projects: CoverageRow[]; any_off: boolean; words: Record<string, string> }

const enc = encodeURIComponent;
let profileCache: Promise<Profile> | null = null;

export const standardsApi = {
  profile: () => (profileCache ??= call<Profile>("GET", "/api/v1/standards/profile")),
  project: (name: string, background = false) => call<ProjectStandards>("GET", `/api/v1/projects/${enc(name)}/standards`, undefined, { background }),
  file: (name: string) => call<{ path: string; in_repository: boolean; repository_text: string; problems: string[]; from_settings: string }>("GET", `/api/v1/projects/${enc(name)}/standards/file`),
  level: (level: string, scope: string) => call<LevelSettings>("GET", `/api/v1/standards/settings?level=${enc(level)}&scope=${enc(scope)}`),
  setLevel: (level: string, scope: string, settings: Settings, version: number) =>
    call<{ version: number }>("PUT", "/api/v1/standards/settings", { level, scope, settings, version }),
  coverage: (level: string, scope: string) => call<Coverage>("GET", `/api/v1/standards/coverage?level=${enc(level)}&scope=${enc(scope)}`),
  backlog: (name: string, q: { status?: string; severity?: string; check?: string; q?: string; sort?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "") p.set(k, String(v));
    return call<BacklogList>("GET", `/api/v1/projects/${enc(name)}/findings?${p}`);
  },
  history: (name: string) => call<Series>("GET", `/api/v1/projects/${enc(name)}/findings/history?days=90`),
  inspection: (run: string, q: { standing?: string; check?: string; limit?: number; offset?: number }, background = false) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "") p.set(k, String(v));
    return call<Inspection>("GET", `/api/v1/runs/${enc(run)}/inspection?${p}`, undefined, { background });
  },
  accept: (project: string, fp: string, status: string, reason: string, expires: string) =>
    call<Decision>("POST", `/api/v1/projects/${enc(project)}/findings/${enc(fp)}/accept`, { status, reason, expires }),
  /** One decision for every open finding in a place: a glob of repository paths, optionally of one check. */
  acceptPath: (project: string, path: string, check: string, status: string, reason: string, expires: string) =>
    call<{ decided: number }>("POST", `/api/v1/projects/${enc(project)}/findings/accept-path`, { path, check, status, reason, expires }),
  reopen: (project: string, fp: string) => call<{ reopened: boolean }>("POST", `/api/v1/projects/${enc(project)}/findings/${enc(fp)}/reopen`, {}),
};
