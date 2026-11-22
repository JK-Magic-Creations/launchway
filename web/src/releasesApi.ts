import { call } from "./api";

// Releases and release management: what the server reports, and asking for changes. The server decides what a
// person may do and its refusals are shown as they are; nothing here guesses a permission.

export type ReleaseState = "draft" | "candidate" | "published" | "yanked";

export interface ReleaseDigest { image: string; digest: string; step?: string }

export interface ReleaseAsset {
  id: string; name: string; kind: "file" | "image" | "sbom" | "scan"; size: number; sha256?: string; image?: string; digest?: string;
  detail?: string; run_id?: string; run_number?: number; step_id?: string; artifact_id?: string; source_sha?: string; added_by: string;
  added_at: number; uploaded?: boolean; downloads?: number;
}

export interface Release {
  id: string; project: string; version: string; scheme: string; state: ReleaseState; title?: string; notes: string; notes_source?: string;
  branch?: string; sha: string; tag?: string; tag_object?: string; tag_created?: boolean; prerelease: boolean; public?: boolean;
  run_id?: string; run_number?: number; digests: ReleaseDigest[]; previous?: string; commit_time?: number; created_at: number;
  created_by?: string; candidate_at?: number; candidate_by?: string; published_at?: number; published_by?: string; yanked_at?: number;
  yanked_by?: string; yank_reason?: string; assets?: ReleaseAsset[]; immutable: boolean; latest?: boolean;
}

export interface Promotion {
  id: string; release_id: string; project: string; version: string; airfield: string; sha: string; digests: ReleaseDigest[];
  run_id?: string; run_number?: number; formation_id?: string; planned_for?: number; requested_by: string; requested_at: number;
  override?: string; overridden?: string; state: string; detail?: string;
}

export interface Clearance { id: string; airfield: string; who: string; role: string; comment?: string; at: number }

export interface ReleaseView {
  release: Release; settings: ReleaseSettings; promotions: Promotion[]; clearances?: Clearance[]; links?: Record<string, string>;
  may: { edit: boolean; publish: boolean; promote: boolean; yank: boolean; administer: boolean; outside_retry?: boolean }; warnings?: string[];
  /** How the release stands on each push mirror's outside Git host; empty when no mirror publishes releases. */
  outside?: OutsidePublish[];
}

/** One release on one push mirror's outside Git host. The reason never holds a credential. */
export interface OutsidePublish {
  id: string; release_id: string; mirror_id: string; mirror_url?: string; repository?: string;
  state: "pending" | "published" | "failed"; url?: string; reason?: string; attempts: number; updated_at: number;
}

export interface ReleaseSettings { project: string; scheme: string; tag_prefix: string; public: boolean; changelog: string }

export interface ChecklistItem { id: string; label: string; status: "pass" | "fail" | "waiting" | "skipped"; detail: string; overridable?: boolean }
export interface Checklist {
  release_id: string; project: string; version: string; airfield: string; items: ChecklistItem[]; ready: boolean; can_override: boolean;
  already_there?: boolean; clearances: Clearance[];
}
export interface ChecklistAnswer { checklist: Checklist; may_promote: boolean; may_override: boolean; environment_refusal?: string }

export interface Reason { change: { title: string; kind: string; ref?: number }; bump: string; why: string }
/** What a person needs to cut a release: the proposal, the commit and tag it would make, and what the pipeline built at that commit. */
export interface NextAnswer {
  proposal: Proposal; source: string; commit: string; branch?: string; highest?: string; scheme: string; tag_prefix: string;
  hosted: boolean; can_tag: boolean; build: null | { run_id: string; run_number: number; files: { name: string; path: string; size: number; sha256: string }[] };
}
export interface Proposal { next: string; last?: string; bump: string; summary: string; reasons: Reason[]; warnings?: string[] }

export interface Cell {
  airfield: string; version?: string; release_id?: string; since?: number; by?: string; health: "healthy" | "failed" | "returned" | "empty";
  run?: string; run_number?: number; launching?: { version: string; run: string; since: number; release_id?: string };
}
export interface MatrixRow { project: string; latest?: string; cells: Cell[]; releases: number }
export interface Matrix { airfields: string[]; rows: MatrixRow[]; can_promote: { project: string; airfield: string; release: string; version: string }[] }

export interface CalendarEntry { at: number; end?: number; kind: "planned" | "in_progress" | "done" | "freeze"; project?: string; airfield?: string; version?: string; release?: string; title: string; by?: string; state?: string }
export interface Freeze { id: string; project?: string; airfield?: string; from: number; to: number; reason: string; by?: string; canceled?: boolean }

export interface FormationMember { release_id: string; project: string; version: string; position: number; state: string; promotion_id?: string; detail?: string }
export interface Formation { id: string; name: string; note?: string; state: string; airfield?: string; created_by?: string; created_at: number; members: FormationMember[]; order_source: string }

export interface Measures {
  launches: number; failures: number; frequency_per_week: number; lead_time_median_s: number; lead_time_count: number;
  change_failure_rate: number; time_to_restore_median_s: number; restore_count: number; unrestored: number; From: string; To: string;
}
export interface Report {
  project?: string; from: number; to: number; measures: Measures; series: Measures[]; by_airfield: Record<string, Measures>; by_project: Record<string, Measures>;
  shipped: { release: Release; reached: { airfield: string; at: number; lead_seconds?: number; state: string; by?: string }[]; failures: number; returns: number }[];
  step_seconds: number;
}

const p = (project: string) => `/api/v1/projects/${encodeURIComponent(project)}`;
const rel = (project: string, version: string) => `${p(project)}/releases/${encodeURIComponent(version)}`;
const q = (o: Record<string, string | undefined>) => {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) if (v) s.set(k, v);
  const t = s.toString();
  return t ? `?${t}` : "";
};
const bg = (b?: boolean) => ({ background: !!b });

export const releasesApi = {
  list: (o: { project?: string; state?: string } = {}, b?: boolean) => call<Release[]>("GET", `/api/v1/releases${q(o)}`, undefined, bg(b)),
  get: (project: string, version: string, b?: boolean) => call<ReleaseView>("GET", rel(project, version), undefined, bg(b)),
  next: (project: string, o: { from?: string; pre?: string } = {}) => call<NextAnswer>("GET", `${p(project)}/releases/next${q(o)}`),
  notes: (project: string, o: { to?: string; version?: string } = {}) => call<{ notes: { markdown: string; count: number; source: string } }>("GET", `${p(project)}/releases/notes${q(o)}`),
  create: (project: string, body: { version: string; from?: string; adopt?: string; title?: string; notes?: string; generate_notes?: boolean; run_id?: string; allow_lower?: boolean; attach_cargo?: boolean; publish?: boolean; public?: boolean }) =>
    call<ReleaseView>("POST", `${p(project)}/releases`, body),
  edit: (project: string, version: string, body: { title?: string; notes?: string }) => call<ReleaseView>("PATCH", rel(project, version), body),
  regenerate: (project: string, version: string) => call<ReleaseView>("POST", `${rel(project, version)}/notes`, {}),
  remove: (project: string, version: string) => call<{ ok: boolean }>("DELETE", rel(project, version)),
  candidate: (project: string, version: string) => call<ReleaseView>("POST", `${rel(project, version)}/candidate`, {}),
  publish: (project: string, version: string, body: { public?: boolean } = {}) => call<ReleaseView>("POST", `${rel(project, version)}/publish`, body),
  yank: (project: string, version: string, reason: string) => call<ReleaseView>("POST", `${rel(project, version)}/yank`, { reason }),
  retryOutside: (project: string, version: string, mirror: string) => call<{ outside: OutsidePublish[] }>("POST", `${rel(project, version)}/outside/${encodeURIComponent(mirror)}/retry`, {}),
  changelog: (project: string, version: string) => call<{ proposal: { branch: string; base: string; pull_request_url?: string }; pull_request?: { number: number; url: string }; pull_request_error?: string }>("POST", `${rel(project, version)}/changelog`, {}),
  removeAsset: (project: string, version: string, name: string) => call<{ ok: boolean }>("DELETE", `${rel(project, version)}/assets/${encodeURIComponent(name)}`),
  attach: (project: string, version: string, body: { run_id: string; images?: boolean; files?: { artifact: string; path: string; name?: string }[] }) => call<ReleaseAsset[]>("POST", `${rel(project, version)}/assets`, body),
  /** Uploads a file: the body is the file itself, its name is in the address. */
  async upload(project: string, version: string, file: File): Promise<ReleaseAsset> {
    const resp = await fetch(`${rel(project, version)}/assets/${encodeURIComponent(file.name)}`, { method: "PUT", credentials: "same-origin", body: file,
      headers: { "X-Launchway-CSRF": "1", "Content-Type": "application/octet-stream" } });
    const text = await resp.text();
    let data: { error?: string } = {};
    try { data = text ? JSON.parse(text) : {}; } catch { /* not JSON */ }
    if (!resp.ok) throw new Error(data.error ?? (text || resp.statusText));
    return data as unknown as ReleaseAsset;
  },
  downloadUrl: (project: string, version: string, name: string) => `${rel(project, version)}/assets/${encodeURIComponent(name)}`,
  /** The address of a release's page, and of the page that downloads one file (it signs a phone in first, then starts the download). */
  pageUrl: (project: string, version: string) => `/deployments/releases/${encodeURIComponent(project)}/${encodeURIComponent(version)}`,
  getUrl: (project: string, version: string, name: string) => `/deployments/releases/${encodeURIComponent(project)}/${encodeURIComponent(version)}/get/${encodeURIComponent(name)}`,
  publicUrl: (project: string, version: string, name: string) => `/public/releases/${encodeURIComponent(project)}/${encodeURIComponent(version)}/${encodeURIComponent(name)}`,
  checklist: (project: string, version: string, airfield: string) => call<ChecklistAnswer>("GET", `${rel(project, version)}/checklist${q({ airfield })}`),
  clearance: (project: string, version: string, airfield: string, comment: string) => call<Clearance>("POST", `${rel(project, version)}/clearances`, { airfield, comment }),
  promote: (project: string, version: string, airfield: string, override?: string) => call<{ promotion: Promotion; run: { id: string; number: number } }>("POST", `${rel(project, version)}/promote`, { airfield, override }),
  plan: (project: string, version: string, airfield: string, at: string, note: string) => call<Promotion>("POST", `${rel(project, version)}/plan`, { airfield, at, note }),
  returnToBase: (project: string, airfield: string, reason: string, to?: string) => call<{ id: string; number: number }>("POST", `${p(project)}/release-returns`, { airfield, reason, to }),
  compare: (project: string, a: string, b: string) => call<{ older: string; newer: string; changes: { title: string; kind: string; ref?: number; author?: string }[]; notes: string; files: { files: string[]; more?: number; stat: string; manifests: string[] }; digests: { older: ReleaseDigest[]; newer: ReleaseDigest[] } }>("GET", `${p(project)}/releases/compare${q({ a, b })}`),
  matrix: (b?: boolean) => call<Matrix>("GET", "/api/v1/release-matrix", undefined, bg(b)),
  calendar: (o: { from?: string; to?: string } = {}, b?: boolean) => call<{ from: number; to: number; now: number; entries: CalendarEntry[] }>("GET", `/api/v1/release-calendar${q(o)}`, undefined, bg(b)),
  freezes: (b?: boolean) => call<Freeze[]>("GET", "/api/v1/release-freezes", undefined, bg(b)),
  freeze: (body: { project?: string; airfield?: string; from?: string; to: string; reason: string }) => call<Freeze>("POST", "/api/v1/release-freezes", body),
  cancelFreeze: (id: string) => call<{ ok: boolean }>("POST", `/api/v1/release-freezes/${encodeURIComponent(id)}/cancel`, {}),
  report: (o: { project?: string; from?: string; to?: string } = {}, b?: boolean) => call<Report>("GET", `/api/v1/release-report${q(o)}`, undefined, bg(b)),
  reportUrl: (o: { project?: string; from?: string; to?: string; format?: string }) => `/api/v1/release-report${q(o)}`,
  formations: (b?: boolean) => call<Formation[]>("GET", "/api/v1/release-formations", undefined, bg(b)),
  formation: (name: string, b?: boolean) => call<Formation>("GET", `/api/v1/release-formations/${encodeURIComponent(name)}`, undefined, bg(b)),
  createFormation: (name: string, note: string, releases: string[], order?: "dependencies") => call<Formation>("POST", "/api/v1/release-formations", { name, note, releases, order }),
  promoteFormation: (name: string, airfield: string, override?: string) => call<Formation>("POST", `/api/v1/release-formations/${encodeURIComponent(name)}/promote`, { airfield, override }),
  settings: (project: string) => call<{ settings: ReleaseSettings; installation_allows_public: boolean; writable: boolean; schemes: string[] }>("GET", `${p(project)}/release-settings`),
};
