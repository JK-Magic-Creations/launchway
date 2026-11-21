import { terms } from "../terms";
import type { BacklogList, Coverage, Effective, EffectiveCheck, Inspection, Profile, ProjectStandards, Series } from "../standardsApi";

const check = (id: string, over: Partial<EffectiveCheck> = {}): EffectiveCheck => ({
  id, title: id === "secrets" ? "Secrets in the repository's history" : "Known vulnerabilities in dependencies", why: "Because it protects what you ship.", kind: "vulnerability", tool: "osv-scanner", ecosystem: "any",
  gates: true, fix: "advice", budget_seconds: 600, state: { value: "block", from: { kind: "default" } }, applies: true, because: "go.mod was found", active: true, reason: "", license: "Apache-2.0", ...over,
});

export const profile = (): Profile => ({
  version: "2026-11-21.1", file: ".launchway/standards.yaml", key_names: {}, words: { installation: terms().label("installation"), organization: terms().label("organization"), unit: terms().label("unit"), project: terms().label("project") },
  ecosystems: [{ id: "any", title: "Any repository" }, { id: "go", title: "Go" }],
  defaults: { fail_on: "high", require_fix: true, default_branch: "advise", incomplete: "pass", licenses_deny: ["AGPL-3.0"] },
  checks: [
    { id: "secrets", title: "Secrets in the repository's history", why: "A committed secret stays in the history.", kind: "secret", tool: "gitleaks", ecosystem: "any", default: "block", gates: true, fix: "advice", budget_seconds: 600, available: true, outputs: [] },
    { id: "vuln-deps", title: "Known vulnerabilities in dependencies", why: "A vulnerable dependency puts you at risk.", kind: "vulnerability", tool: "osv-scanner", ecosystem: "any", default: "block", gates: true, fix: "dependency-bump", budget_seconds: 600, available: true, outputs: [] },
  ],
});

export const effective = (over: Partial<Effective> = {}): Effective => ({
  profile_version: "2026-11-21.1", off: { value: false, from: { kind: "default" } },
  checks: [check("secrets", { title: "Secrets in the repository's history", tool: "gitleaks", license: "MIT", kind: "secret" }),
    check("vuln-deps", { state: { value: "advise", from: { kind: "level", level: "project", scope: "shop-api" }, chain: [
      { kind: "level", level: "project", display: "advise" }, { kind: "level", level: "organization", display: "block", overridden: true }] } }),
    check("sast-python", { title: "Security rules for Python code", ecosystem: "python", applies: false, active: false, reason: "nothing in the repository makes it apply", state: { value: "advise", from: { kind: "default" } } })],
  fail_on: { value: "high", from: { kind: "default" } }, require_fix: { value: true, from: { kind: "default" } }, default_branch: { value: "advise", from: { kind: "default" } },
  incomplete: { value: "pass", from: { kind: "default" } }, licenses_allow: { value: [], from: { kind: "default" } }, licenses_deny: { value: ["AGPL-3.0"], from: { kind: "default" } },
  exemptions: [], detected: [{ ecosystem: "any", title: "Any repository", because: "it applies to every repository" }, { ecosystem: "go", title: "Go", because: "go.mod was found" }], notices: [], ...over,
});

export const projectStandards = (over: Partial<ProjectStandards> = {}): ProjectStandards => ({ effective: effective(), commit: "abc", branch: "main", file: false, ...over });

export const emptyBacklog = (): BacklogList => ({ findings: [], matched: 0, by_severity: {}, by_check: {}, open: 0, accepted: 0, fixed: 0 });
export const emptySeries = (): Series => ({ project: "shop-api", points: [], mean_days_to_fix: 0, fixed_count: 0, open: 0, by_severity: {} });

export const backlog = (): BacklogList => ({
  findings: [{ fingerprint: "f1f1f1f1f1f1f1f1", check: "vuln-deps", tool: "osv-scanner", rule: "CVE-2023-45288", severity: "high", kind: "vulnerability", title: "Package 'golang.org/x/net@0.17.0' is vulnerable to 'CVE-2023-45288'.",
    file: "go.mod", package: "golang.org/x/net", version: "0.17.0", fixed_in: "0.23.0", fix: "Update golang.org/x/net to 0.23.0 or later.", first_run: 3, first_seen: 1_790_000_000_000, last_run: 9, last_seen: 1_794_000_000_000, status: "open" }],
  matched: 1, by_severity: { high: 1 }, by_check: { "vuln-deps": 1 }, open: 1, accepted: 0, fixed: 0,
});
export const series = (): Series => ({
  project: "shop-api", mean_days_to_fix: 4.5, fixed_count: 2, open: 1, by_severity: { high: 1 }, oldest_open: 1_790_000_000_000,
  points: [{ day: "2026-11-01", total: 4, critical: 0, high: 2, medium: 2, low: 0 }, { day: "2026-11-08", total: 3, critical: 0, high: 1, medium: 2, low: 0 }, { day: "2026-11-15", total: 1, critical: 0, high: 1, medium: 0, low: 0 }],
});

export const inspection = (blocked = true): Inspection => ({
  present: true, blocked, summary: blocked ? "Blocked: this change introduces 1 finding at high or above. 1 existing finding were already on the default branch and are not counted." : "Passed: this change introduces nothing at high or above.",
  default_branch: false, profile_version: "2026-11-21.1", fail_on: "high", counts: { new: blocked ? 1 : 0, existing: 1 },
  checks: [{ id: "vuln-deps", title: "Known vulnerabilities in dependencies", state: "block", ran: true, new: blocked ? 1 : 0, total: 2 }, { id: "secrets", title: "Secrets in the repository's history", state: "block", ran: true, new: 0, total: 0 }],
  findings: [
    ...(blocked ? [{ fingerprint: "aaaa1111bbbb2222", check: "vuln-deps", tool: "osv-scanner", rule: "CVE-2026-0001", severity: "high" as const, kind: "vulnerability", title: "Package 'example.org/lib@1.0.0' is vulnerable to 'CVE-2026-0001'.", file: "go.mod", package: "example.org/lib", version: "1.0.0", fixed_in: "1.2.0", standing: "new" as const, gating: true, step: "inspect-vuln-deps" }] : []),
    { fingerprint: "cccc3333dddd4444", check: "vuln-deps", tool: "osv-scanner", rule: "CVE-OLD", severity: "medium", kind: "vulnerability", title: "Package 'example.org/old@0.1.0' is vulnerable to 'CVE-OLD'.", file: "go.mod", package: "example.org/old", version: "0.1.0", standing: "existing", step: "inspect-vuln-deps" },
  ],
  matched: blocked ? 2 : 1,
});

export const coverage = (): Coverage => ({
  level: "installation", scope: "", any_off: false, words: { installation: terms().label("installation") },
  projects: [{ project: "shop-api", off: false, checks: { secrets: { state: "block", active: true, locked: true }, "vuln-deps": { state: "advise", active: true } }, open: { high: 1 }, notices: 0 },
    { project: "shop-web", off: true, off_reason: "moving to another scanner", checks: {}, open: {}, notices: 0 }],
});
