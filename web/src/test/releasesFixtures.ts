import type { Checklist, Formation, Matrix, Release, ReleaseView, Report } from "../releasesApi";

const T = Date.UTC(2026, 10, 18, 14, 0, 0);
const day = 86400000;

const release = (over: Partial<Release> = {}): Release => ({
  id: "rel_1", project: "shop-api", version: "1.4.0", scheme: "semver", state: "published", notes: "## 1.4.0\n\n### Features\n\n- the matrix (#12) by ann\n\n### Fixes\n\n- a crash on start (#13) by bob\n\n### Contributors\n\nann, bob\n",
  notes_source: "generated from merged pull requests and commits", sha: "0123456789abcdef0123456789abcdef01234567", tag: "v1.4.0", tag_object: "t1", tag_created: true, prerelease: false,
  run_id: "run_7", run_number: 7, digests: [{ image: "registry.example/shop-api@sha256:" + "ab".repeat(32), digest: "sha256:" + "ab".repeat(32), step: "image" }], previous: "1.3.2",
  commit_time: T - 3 * day, created_at: T - 2 * day, created_by: "ann", published_at: T - day, published_by: "ann", immutable: true,
  assets: [
    { id: "ras_1", name: "shop-api-linux-amd64", kind: "file", size: 14_300_000, sha256: "cd".repeat(32), run_id: "run_7", run_number: 7, source_sha: "0123456789abcdef", added_by: "ann", added_at: T - 2 * day },
    { id: "ras_2", name: "image-image", kind: "image", size: 0, digest: "sha256:" + "ab".repeat(32), detail: "signed true, SBOM true", run_id: "run_7", run_number: 7, source_sha: "0123456789abcdef", added_by: "ann", added_at: T - 2 * day },
  ], ...over,
});

/** A published release whose only file has a name so long that the address a phone would open cannot be drawn as a QR code. */
export const releaseWithALongFileName = (): Release => release({ assets: [{ id: "ras_9", name: "shop-api-" + "very-long-name-".repeat(30) + ".tar.gz", kind: "file", size: 1000, sha256: "ef".repeat(32), run_id: "run_7", run_number: 7, source_sha: "0123456789abcdef", added_by: "ann", added_at: T - 2 * day }] });

export const releaseList = (): Release[] => [
  release({ id: "rel_3", version: "1.5.0", state: "draft", published_at: undefined, published_by: undefined, tag: undefined, immutable: false, assets: [] }),
  release({ id: "rel_2", version: "1.4.1-rc.1", state: "candidate", prerelease: true, immutable: false, published_at: undefined, assets: [] }),
  release(),
  release({ id: "rel_0", version: "1.3.2", state: "yanked", yank_reason: "crashes on start", yanked_by: "bob", yanked_at: T - day }),
];

export const view = (r: Release, may: Partial<ReleaseView["may"]> = {}): ReleaseView => ({
  release: r, settings: { project: "shop-api", scheme: "semver", tag_prefix: "v", public: false, changelog: "CHANGELOG.md" },
  promotions: r.state === "published" ? [{ id: "prm_1", release_id: r.id, project: r.project, version: r.version, airfield: "staging", sha: r.sha, digests: r.digests, run_id: "run_8", run_number: 8, requested_by: "ann", requested_at: T - day / 2, state: "launched" },
    { id: "prm_2", release_id: r.id, project: r.project, version: r.version, airfield: "prod", sha: r.sha, digests: r.digests, run_id: "run_9", run_number: 9, requested_by: "ada", requested_at: T - day / 4, state: "launched", override: "customer outage", overridden: "window" }] : [],
  clearances: r.state === "published" ? [{ id: "rcl_1", airfield: "prod", who: "bob", role: "operator", comment: "read the notes", at: T - day / 3 }] : [],
  may: { edit: !r.immutable, publish: !r.immutable, promote: r.state === "published" || r.state === "candidate", yank: r.state === "published" || r.state === "candidate", administer: true, outside_retry: r.state === "published", ...may },
  outside: r.state === "published" ? [
    { id: "rou_1", release_id: r.id, mirror_id: "mir_1", mirror_url: "https://git.example.com/acme/shop-api.git", repository: "acme/shop-api", state: "published", url: "https://git.example.com/acme/shop-api/releases/tag/v1.4.0", attempts: 2, updated_at: T - day / 2 },
    { id: "rou_2", release_id: r.id, mirror_id: "mir_2", mirror_url: "https://git.partner.example/acme/shop-api.git", repository: "acme/shop-api", state: "failed", reason: "the outside host refused the credential (403). A token that may push may not be allowed to make releases", attempts: 1, updated_at: T - day / 3 },
  ] : [],
});

const checklist = (ready: boolean): Checklist => ({
  release_id: "rel_1", project: "shop-api", version: "1.4.0", airfield: "prod", ready, can_override: !ready, clearances: [],
  items: [
    { id: "state", label: "The release is ready to promote", status: "pass", detail: "it is published" },
    { id: "notes", label: "Release notes are written", status: "pass", detail: "" },
    { id: "checks", label: "Checks are green on the release's commit", status: "pass", detail: "1 run(s) succeeded at 01234567" },
    { id: "previous", label: "It has been in staging", status: "pass", detail: "since 2026-11-17 14:00 UTC" },
    { id: "soak", label: "Soaked 2 hour(s) in staging", status: "pass", detail: "", overridable: true },
    { id: "clearance", label: "1 clearance(s) from operator or admin", status: ready ? "pass" : "waiting", detail: ready ? "bob" : "0 of 1 given" },
    { id: "window", label: "Inside the change window", status: ready ? "pass" : "waiting", detail: ready ? "" : "outside the window; it opens 2026-11-19 09:00 UTC", overridable: true },
    { id: "freeze", label: "No freeze is in force", status: "pass", detail: "", overridable: true },
  ],
});

export const checklistAnswer = (ready: boolean) => ({ checklist: checklist(ready), may_promote: true, may_override: !ready });

export const matrix = (): Matrix => ({
  airfields: ["dev", "staging", "prod"],
  rows: [
    { project: "shop-api", latest: "1.4.0", releases: 4, cells: [
      { airfield: "dev", version: "1.5.0-dev.3", health: "healthy", since: T - 3600000, by: "ci", run: "run_20", run_number: 20 },
      { airfield: "staging", version: "1.4.0", release_id: "rel_1", health: "healthy", since: T - day / 2, by: "ann", run: "run_8", run_number: 8 },
      { airfield: "prod", version: "1.3.2", release_id: "rel_0", health: "returned", since: T - day * 2, by: "bob", run: "run_5", run_number: 5 }] },
    { project: "shop-web", latest: "5.1.0", releases: 2, cells: [
      { airfield: "dev", version: "5.1.0", release_id: "rel_9", health: "healthy", since: T - day, by: "dana", run: "run_30", run_number: 30 },
      { airfield: "staging", version: "5.0.2", release_id: "rel_8", health: "healthy", since: T - 4 * day, by: "dana", run: "run_28", run_number: 28, launching: { version: "5.1.0", run: "run_31", since: T - 600000, release_id: "rel_9" } },
      { airfield: "prod", health: "empty" }] },
  ],
  can_promote: [{ project: "shop-api", airfield: "prod", release: "rel_1", version: "1.4.0" }, { project: "shop-web", airfield: "prod", release: "rel_9", version: "5.1.0" }],
});

export const calendar = () => ({ from: T - 14 * day, to: T + 30 * day, now: T, entries: [
  { at: T - 2 * day, kind: "done", project: "shop-api", airfield: "staging", version: "1.4.0", release: "rel_1", title: "shop-api 1.4.0 → staging", by: "ann", state: "launched" },
  { at: T - day / 4, kind: "in_progress", project: "shop-api", airfield: "prod", version: "1.4.0", release: "rel_1", title: "shop-api 1.4.0 → prod", by: "ada", state: "started" },
  { at: T + day, end: T + 3 * day, kind: "freeze", airfield: "prod", title: "Freeze of prod: quarter close", by: "ada", state: "coming" },
  { at: T + 2 * day, kind: "planned", project: "shop-web", airfield: "prod", version: "5.1.0", release: "rel_9", title: "shop-web 5.1.0 → prod", by: "dana", state: "planned" },
] });

export const freezes = () => [{ id: "frz_1", airfield: "prod", from: T + day, to: T + 3 * day, reason: "quarter close", by: "ada" }];

export const formations = (): Formation[] => [
  { id: "frm_1", name: "autumn launch", state: "partial", created_at: T - 3 * day, order_source: "explicit", airfield: "prod", members: [
    { release_id: "rel_1", project: "shop-api", version: "1.4.0", position: 0, state: "launched" }, { release_id: "rel_9", project: "shop-web", version: "5.1.0", position: 1, state: "failed", detail: "the Sortie failed" },
    { release_id: "rel_10", project: "shop-docs", version: "2.0.0", position: 2, state: "not_started", detail: "an earlier release of the formation failed" }] },
  { id: "frm_2", name: "winter launch", state: "planned", created_at: T - day, order_source: "explicit", members: [
    { release_id: "rel_1", project: "shop-api", version: "1.4.0", position: 0, state: "waiting" }, { release_id: "rel_9", project: "shop-web", version: "5.1.0", position: 1, state: "waiting" }] },
];

const m = (launches: number, failures: number, lead: number): Report["measures"] => ({ launches, failures, frequency_per_week: launches / 2, lead_time_median_s: lead, lead_time_count: launches, change_failure_rate: launches ? failures / launches : 0,
  time_to_restore_median_s: failures ? 5400 : 0, restore_count: failures, unrestored: 0, From: "", To: "" });

export const report = (): Report => ({ from: T - 28 * day, to: T, step_seconds: 7 * 86400, measures: m(14, 2, 21600),
  series: [m(2, 0, 25000), m(5, 1, 20000), m(3, 0, 19000), m(4, 1, 17000)], by_airfield: { dev: m(6, 0, 7200), staging: m(5, 1, 18000), prod: m(3, 1, 43200) }, by_project: { "shop-api": m(8, 1, 20000), "shop-web": m(6, 1, 23000) },
  shipped: [{ release: release(), reached: [{ airfield: "staging", at: T - day / 2, lead_seconds: 7200, state: "launched" }, { airfield: "prod", at: T - day / 4, lead_seconds: 54000, state: "launched" }], failures: 0, returns: 0 }] });

export function releaseFixtures(): Record<string, unknown> {
  return {
    "/api/v1/releases": releaseList(),
    "/api/v1/projects/shop-api/releases": releaseList(),
    "/api/v1/projects/shop-api/releases/1.4.0": view(release()),
    "/api/v1/projects/shop-api/releases/1.5.0": view(release({ id: "rel_3", version: "1.5.0", state: "draft", published_at: undefined, tag: undefined, immutable: false })),
    "/api/v1/projects/shop-api/releases/1.4.0/checklist": checklistAnswer(false),
    "/api/v1/projects/shop-api/releases/next": { proposal: { next: "1.5.0", last: "1.4.0", bump: "minor", summary: "1.5.0: 2 changes since 1.4.0; the highest is minor, so 1.4.0 → 1.5.0.", reasons: [{ change: { title: "feat: a page", kind: "pull", ref: 14 }, bump: "minor", why: "new feature" }] }, source: "x", commit: "0123456789abcdef0123456789abcdef01234567", branch: "main", highest: "1.4.0", scheme: "semver", tag_prefix: "v", hosted: true, can_tag: true, build: { run_id: "run_7", run_number: 7, files: [{ name: "shop-api.apk", path: "dist/shop-api.apk", size: 3000000, sha256: "cd".repeat(32) }] } },
    "/api/v1/release-matrix": matrix(),
    "/api/v1/release-calendar": calendar(),
    "/api/v1/release-freezes": freezes(),
    "/api/v1/release-formations": formations(),
    "/api/v1/release-report": report(),
  };
}
