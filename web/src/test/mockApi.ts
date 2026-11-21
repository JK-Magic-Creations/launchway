import { vi } from "vitest";
import type { RunDetail } from "../api";
import { approval, environment, me, project, run, runner, step } from "./fixtures";

/** A stand-in for the server for tests that draw whole pages: fixtures for the main lists, an empty answer
 * for anything else. `overrides` maps a path (without the query) to the JSON to answer with. */
export function installServer(overrides: Record<string, unknown> = {}, who = me()) {
  const failed = run({ id: "run_7", number: 7, status: "failed", message: "Fix the price rounding", steps: [{ id: "test", status: "failed" }] });
  // Two steps run together after check-code, so the run page's graph has a stage with parallel steps.
  const parallel = [step({ id: "check-code", status: "succeeded", needs: [] }), step({ id: "test", needs: ["check-code"] }),
    step({ id: "security-go", status: "running", needs: ["check-code"] }), step({ id: "build", status: "skipped", needs: ["test", "security-go"], skip_kind: "not_needed" })];
  const detail: RunDetail = {
    ...failed, source: { kind: "git", url: "https://git.example/acme/shop-api.git" }, graph: { name: "shop-api", on: {}, steps: parallel.map((s) => ({ id: s.id, needs: s.needs, image: "golang", run: "go test", retry: 0 })) } as unknown as RunDetail["graph"],
    levels: [["check-code"], ["test", "security-go"], ["build"]], step_list: parallel, reruns: [], not_understood: [],
  };
  const answers: Record<string, unknown> = {
    "/api/v1/me": who,
    "/api/v1/info": { version: "test", auth_mode: who.auth_mode },
    "/api/v1/config": { available: true, kinds: [], levels: { installation: { singular: "installation", plural: "installations" }, organisation: { singular: "organization", plural: "organizations" }, unit: { singular: "unit", plural: "units" }, project: { singular: "project", plural: "projects" } }, admin: true },
    "/api/v1/projects": [project({ last_run: failed }), project({ name: "shop-web", last_run: run() })],
    "/api/v1/runs": [failed, run()],
    "/api/v1/runs/run_7": detail,
    "/api/v1/approvals": [approval()],
    "/api/v1/runners": [runner()],
    "/api/v1/environments": [environment()],
    "/api/v1/runs/run_2": { ...detail, id: "run_2", project: "shop-web", number: 118, status: "running", step_list: [step({ id: "approve", status: "waiting", kind: "approval" })] },
    "/api/v1/deployments": [],
    "/api/v1/ai/models": { models: [], server_can_explain: false },
    "/api/v1/runs/run_7/variables": { run_id: "run_7", variables: [] },
    "/api/v1/artifact-store": { store: "local", available: true, advice: [], kept: { files: 0, bytes: 0 } },
    "/api/v1/projects/shop-api": project({ last_run: failed }),
    "/api/v1/projects/shop-web": project({ name: "shop-web", last_run: run() }),
    "/api/v1/projects/shop-api/coverage": { runs: [] },
    "/api/v1/projects/shop-api/schedules": { schedules: [], webhooks: [], fires: [] },
    "/api/v1/runs/run_7/steps/test/logs": { lines: [] },
    "/api/v1/variables": { words: {}, variables: [] },
    "/api/v1/libraries": [{ name: "launchway", last_fetch: 0, builtin: true }],
    "/api/v1/libraries/launchway/templates": { library: "launchway", version: "builtin", commit: "0123456789abcdef0123456789abcdef01234567", versions: ["builtin"], templates: [
      { name: "go-service", kind: "flight plan", description: "A Go service with a Dockerfile: format, vet, test, build, scan and image", outputs: [],
        params: [{ name: "go_version", type: "string", default: "1.27", description: "The Go version of the image" }, { name: "push", type: "boolean", default: "false", description: "Push the image" }],
        example: "pipeline: my-project\nsteps:\n  ci:\n    uses: launchway/go-service\n",
        graph: [{ id: "ci/check", description: "Format and vet" }, { id: "ci/test", description: "Run every test" }, { id: "ci/build", needs: ["ci/check", "ci/test"], description: "Build" }] },
      { name: "secret-scan", kind: "step template", description: "Look for private keys committed to the repository", outputs: [], params: [] }] },
    "/api/v1/projects/shop-api/variables": { words: {}, variables: [] },
    "/api/v1/projects/shop-api/tests": { ref: "main", runs: [], slowest: [], failing: [], flaky: [], refs: ["main"] },
    "/api/v1/runner-pools": { pools: [], manage: true, identities: false },
    "/api/v1/policies": { catalogue: [], sources: [], recent: [], max_waiver_days: 30 },
    "/api/v1/waivers": { waivers: [], waive_projects: [], rules: {} },
    "/api/v1/config/history": { changes: [] },
    "/api/v1/encryption": { encrypted: true, dump_alone_cannot_reveal_secrets: true, root_key_holder: { kind: "key_file", where: "/etc/launchway-keys/root.key", strength: "A copy of the database reveals no secret.", reachable: true },
      keys: { current_key_encryption_key: 1, data_keys: [{}, {}], needs_rewrap: false, oldest_key_at: 1 }, columns: [{ table: "meta", column: "value", what: "the signing and sealing keys and the shared runner token", sealed: 5, remaining: 0 }],
      secrets_sealed: 5, secrets_remaining: 0, migration: { state: "complete" }, stored_addresses_with_credentials: 0, warnings: [], advice: ["Keep a copy of the root key somewhere other than the database's backups."],
      not_yet_encrypted: ["artifacts", "logs", "build caches", "hosted Git repository packs"], summary: "Encryption at rest is on: 5 stored secrets are sealed and none remain in the clear." },
    "/api/v1/setup-guide": guideFixture(),
    "/api/v1/signin/test": { tested: false },
    "/api/v1/encryption/root-key-copy": { taken: false, can_take: true },
    ...forgeFixtures(),
    ...rollbackFixtures(detail),
    ...pullFixtures(),
    ...browseFixtures(),
    ...overrides,
  };
  class FakeSource { onopen = null; onerror = null; addEventListener() {} removeEventListener() {} close() {} }
  vi.stubGlobal("EventSource", FakeSource);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input).split("?")[0];
    const hit = answers[String(input)] !== undefined ? answers[String(input)] : answers[path];
    const body = hit !== undefined ? hit : path.endsWith("s") || path.includes("list") ? [] : {};
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
}

/** Hosted repositories, SSH keys, deploy keys, webhooks and their delivery log: the answers the Git hosting pages draw from. */
export function forgeFixtures(): Record<string, unknown> {
  const repo = { id: "repo_1", path: "acme/shop", home: "acme", name: "shop", default_branch: "main", visibility: "private",
    clone: { https: "https://launchway.example/git/acme/shop.git", ssh: "ssh://git@launchway.example:2222/acme/shop.git" } };
  const key = { id: "key_1", kind: "user", name: "laptop", type: "ssh-ed25519", fingerprint: "SHA256:abc123", public_key: "ssh-ed25519 AAAA", created_at: "2026-11-01T10:00:00Z", last_used_at: "2026-11-20T09:00:00Z", last_used_from: "203.0.113.4" };
  const hook = { id: "hook_1", scope: "repository", repository: "acme/shop", name: "build server", url: "https://ci.example/hook", events: ["push", "create"], active: true, has_secret: true, failures: 0, created_at: "2026-11-02T10:00:00Z" };
  const delivery = { id: "del_1", hook_id: "hook_1", event: "push", state: "delivered", attempts: 1, status: 200, duration_ms: 41, created_at: "2026-11-20T10:00:00Z", payload: { ref: "refs/heads/main" } };
  const failedDelivery = { ...delivery, id: "del_2", state: "failed", attempts: 3, status: 502, error: "the receiver answered 502" };
  const hooks = { items: [hook], events: ["push", "create", "delete", "repository"] };
  const deliveries = { items: [delivery, failedDelivery] };
  return {
    "/api/v1/repositories": { items: [repo] },
    "/api/v1/repositories/acme/shop": repo,
    "/api/v1/ssh": { enabled: true, host: "launchway.example", port: 2222, certificates: false,
      host_keys: [{ algorithm: "ssh-ed25519", type: "ed25519", fingerprint: "SHA256:hostkeyfingerprint", public_key: "ssh-ed25519 BBBB", created_at: "2026-10-01T10:00:00Z" }] },
    "/api/v1/user/ssh-keys": { items: [key] },
    "/api/v1/repositories/acme/shop/archive/issues": { items: [
      { number: 12, title: "Checkout crashes on empty carts", state: "closed", author: "bob", body: "", labels: ["bug"], assignees: ["ada"], comment_count: 1, created_at: "2024-01-02T03:04:05Z" },
      { number: 3, title: "Add a receipt page", state: "open", author: "ada", body: "", labels: [], assignees: [], comment_count: 0, created_at: "2023-12-01T00:00:00Z" }], total: 2, open: 1, closed: 1, page: 1, per_page: 50 },
    "/api/v1/repositories/acme/shop/archive/issues/12": { number: 12, title: "Checkout crashes on empty carts", state: "closed", author: "bob", body: "Steps:\n<script>alert(1)</script>", labels: ["bug"], assignees: ["ada"],
      url: "https://old.example/acme/shop/issues/12", comment_count: 1, created_at: "2024-01-02T03:04:05Z", closed_at: "2024-01-03T03:04:05Z",
      comments: [{ id: "77", author: "ada", body: "Fixed in the next release.", created_at: "2024-01-03T01:00:00Z" }] },
    "/api/v1/repositories/acme/shop/deploy-keys": { items: [{ ...key, id: "key_2", kind: "deploy", name: "build server", can_write: false }] },
    "/api/v1/repositories/acme/shop/protected-branches": { patterns: ["main", "release/*"] },
    "/api/v1/repositories/acme/shop/hooks": hooks,
    "/api/v1/repositories/acme/shop/hooks/hook_1/deliveries": deliveries,
    "/api/v1/owners/acme/hooks": { items: [{ ...hook, scope: "owner", owner: "acme", repository: undefined }], events: hooks.events },
  };
}

/** Rolling back after a deployment succeeded: an environment with a service that may be rolled back (and one
 * that may not), the emergency rollbacks that need a review, and a rollback run's page. */
export function rollbackFixtures(base: RunDetail): Record<string, unknown> {
  const target = (version: string, n: number, previous = false) => ({
    deployment: "dep_" + version, run: "run_" + n, run_number: n, version, at: 1_700_000_000_000, previous,
    reverts: [{ deployment: "dep_x", run: "run_9", run_number: 9, version: "1.4.0", at: 1_700_000_100_000 }],
    consequence: `prod goes from 1.4.0 back to ${version}; the deployment of 1.4.0 (run #9) is reverted in Git; the database and configuration are not rolled back`,
    suggestions: ["If 1.4.0 included a database migration, check that it is backward compatible with " + version + ": a rollback never undoes a database."],
  });
  const unit = { project: "shop-api", service: "shop", waiting: [], current: { deployment: "dep_1.4.0", version: "1.4.0", run: "run_9", run_number: 9, since: 1_700_000_100_000 },
    rollback: { name: "rollback", available: true, who: "operator", emergency: true, risk: "prod goes back to an earlier version", targets: [target("1.3.2", 7, true), target("1.3.0", 5)] } };
  const idle = { project: "shop-web", service: "web", waiting: [], rollback: { name: "rollback", available: false, who: "operator", reason: "web has only one successful deployment in prod", would_help: "deploy a second version first" } };
  const review = { run: "run_rb", number: 10, project: "shop-api", environment: "prod", service: "shop", from_version: "1.4.0", to_version: "1.3.2", reason: "checkout fails", actor: "ann", at: 1_700_000_200_000,
    acknowledge: { name: "review", available: true, who: "approver" } };
  const rbRun: RunDetail = { ...base, id: "run_rb", number: 10, project: "shop-api", status: "succeeded", message: "Roll back shop in prod to 1.3.2",
    rollback: { environment: "prod", service: "shop", headline: "Rolled shop in prod back from 1.4.0 to 1.3.2", emergency: true, review_required: true, reason: "checkout fails", actor: "ann",
      to: { deployment: "dep_1.3.2", version: "1.3.2", run: "run_7", run_number: 7, at: 1_700_000_000_000 },
      from: { deployment: "dep_1.4.0", version: "1.4.0", run: "run_9", run_number: 9 },
      reverts: [{ deployment: "dep_1.4.0", version: "1.4.0", commit: "abc1234", run: "run_9", run_number: 9 }],
      note: "A rollback never undoes a database or configuration." } };
  return {
    "/api/v1/environments": [environment(), environment({ name: "prod", projects: ["shop-api", "shop-web"], services: [unit as never, idle as never] })],
    "/api/v1/rollbacks/review": [review],
    "/api/v1/runs/run_rb": rbRun,
    "/api/v1/runs/run_rb/steps/test/logs": { lines: [] },
    "/api/v1/runs/run_rb/variables": { run_id: "run_rb", variables: [] },
  };
}

/** Pull requests of acme/shop: one open pull request with a thread, a review, a failing required check and a file with a patch. */
export function pullFixtures(): Record<string, unknown> {
  const base = "/api/v1/repositories/acme/shop";
  const pull = {
    id: "pul_1", number: 1, repository: "acme/shop", title: "Round prices to the cent", body: "Fixes the **rounding** bug.\n\n- one\n- two\n\n<script>alert(1)</script>", author: "ada",
    source_branch: "fix-rounding", target_branch: "main", state: "open", draft: false, labels: ["bug"], assignees: [], requested_reviewers: ["grace"],
    head_sha: "a".repeat(40), base_sha: "b".repeat(40), merge_base: "b".repeat(40), linked_runs: ["run_7"], created_at: "2026-11-20T10:00:00Z", updated_at: "2026-11-20T11:00:00Z", revisions: 2,
    mergeability: { state: "clean", behind: 1, ahead: 1 }, fetch_ref: "refs/pull/1/head", your_role: "admin",
    readiness: { can_merge: false, why: "needs 1 more approval; `test` has not passed", approvals: 0, changes_requested_by: [], open_threads: 1, methods: ["merge", "squash", "rebase"], default_method: "merge",
      blockers: [{ code: "approvals", why: "needs 1 more approval" }, { code: "check", why: "`test` has not passed" }],
      checks: [{ name: "ci", status: "running", required: false, run_id: "run_7", run_number: 7 }, { name: "ci/test", status: "failed", required: true, run_id: "run_7", run_number: 7 }] },
    actions: [{ name: "merge", available: false, reason: "needs 1 more approval", who: "operator" }, { name: "edit", available: true }, { name: "close", available: true }, { name: "review", available: true }, { name: "mark_draft", available: true }, { name: "auto_merge", available: true }],
  };
  const patch = "diff --git a/price.go b/price.go\n--- a/price.go\n+++ b/price.go\n@@ -1,3 +1,4 @@\n func Round(x float64) float64 {\n-\treturn x\n+\treturn math.Round(x*100) / 100\n }\n+// done\n";
  const thread = { id: "pcm_1", path: "price.go", line: 3, side: "new", outdated: false, resolved: false, comments: [{ id: "pcm_1", thread_id: "pcm_1", author: "grace", body: "Why not `math.Floor`?", path: "price.go", line: 3, side: "new", created_at: "2026-11-20T10:30:00Z" }] };
  return {
    [`${base}/merge-rules`]: { rules: [{ pattern: "main", required_approvals: 1, approval_role: "operator", required_checks: ["ci/test"], require_up_to_date: true, require_resolved_threads: false, no_self_approval: true, require_signed_commits: false, allow_admin_bypass: false }],
      actions: [{ name: "edit_rules", available: true }, { name: "edit_settings", available: true }, { name: "require_signed_commits", available: false, reason: "signed commits are not checked yet, so a rule that asks for them is refused" }] },
    [`${base}/pull-settings`]: { allow_merge: true, allow_squash: true, allow_rebase: false, default_method: "merge", delete_branch_after_merge: false, dismiss_stale_approvals: true, merge_message_template: "", squash_message_template: "" },
    "/api/v1/launch-lines": [{ run: "run_7", number: 7, project: "shop-api", message: "Round prices", actor: "ada", ref: "fix-rounding", sha: "a".repeat(40), status: "running", created_at: 1_700_000_000_000, finished_at: 0,
      stations: [{ key: "commit", label: "Commit", state: "done", detail: "a1b2c3d" }, { key: "checks", label: "Checks", state: "running", detail: "lint" }, { key: "tests", label: "Tests", state: "idle", detail: "" }], tone: "run", now: "Checks: running" }],
    [`${base}/pulls`]: { items: [pull], total: 1 },
    [`${base}/pulls/1`]: pull,
    [`${base}/pulls/1/conversation`]: { comments: [{ id: "pcm_2", author: "ada", body: "Ready when you are.", created_at: "2026-11-20T10:10:00Z" }], threads: [thread],
      reviews: [{ id: "prv_1", author: "grace", verdict: "request_changes", body: "Two things.", commit_sha: "c".repeat(40), state: "submitted", created_at: "2026-11-20T10:40:00Z", stale: true }],
      revisions: [{ seq: 1, head_sha: "c".repeat(40), base_sha: "b".repeat(40), forced: false, created_at: "2026-11-20T10:00:00Z" }, { seq: 2, head_sha: "a".repeat(40), base_sha: "b".repeat(40), forced: false, actor: "ada", created_at: "2026-11-20T11:00:00Z" }] },
    [`${base}/pulls/1/files`]: { files: [{ path: "price.go", status: "modified", additions: 2, deletions: 1, binary: false }, { path: "logo.png", status: "added", additions: 0, deletions: 0, binary: true }], total: 2, additions: 2, deletions: 1 },
    [`${base}/pulls/1/patch`]: { path: "price.go", diff: patch, bytes: patch.length },
    [`${base}/pulls/1/commits`]: { items: [{ sha: "a".repeat(40), author: "Ada", authored_at: "2026-11-20T10:00:00Z", subject: "Round prices" }] },
    [`${base}/compare`]: { base: "main", head: "fix-rounding", ahead: 1, behind: 0, commits: [{ sha: "a".repeat(40), author: "Ada", authored_at: "2026-11-20T10:00:00Z", subject: "Round prices" }],
      changes: { files: [], total: 1, additions: 2, deletions: 1 }, mergeability: { state: "clean", behind: 0, ahead: 1 } },
  };
}

/** The first-run guide: what the server says about each step, with the actions available now and the reason for each that is not. */
export function guideFixture() {
  const act = (name: string, available: boolean, reason?: string, would_help?: string) => ({ name, available, who: "admin", ...(available ? {} : { reason, would_help }) });
  return {
    kubernetes: false,
    steps: [
      { id: "sign-in", title: "How people sign in", state: "todo", summary: "People sign in with built-in accounts (a password and an authenticator).",
        actions: [act("test_provider", true), act("keep_built_in", true), act("switch_provider_on", false, "Switching the provider on is a setting in the server's own file and takes a restart.", "Test the provider here first.")] },
      { id: "encryption", title: "Encryption and the root key", state: "todo", summary: "Stored secrets are encrypted under a root key held outside the database. No recovery copy has been taken yet.",
        actions: [act("download_root_key_copy", true)],
        encryption: { encrypted: true, dump_alone_cannot_reveal_secrets: true, secrets_sealed: 5, secrets_remaining: 0, summary: "Encryption at rest is on.", root_key_holder: { kind: "key_file", where: "/etc/launchway-keys/root.key", strength: "", reachable: true },
          migration: { state: "complete" }, warnings: [], advice: [], not_yet_encrypted: ["artifacts", "logs"] } },
      { id: "runner", title: "A runner", state: "done", summary: "1 runner is connected and can take work.", actions: [act("make_join_token", true)] },
      { id: "git-hosting", title: "Git hosting", state: "todo", summary: "Git hosting is off.", actions: [act("turn_on_hosting", true)] },
      { id: "import", title: "Bring in your repositories", state: "unavailable", summary: "Importing needs Git hosting, which is off.",
        actions: [act("add_connection", false, "Git hosting is off.", "Turn on Git hosting first (step 6) and restart."), act("paste_token", false, "Git hosting is off."), act("import", false, "Git hosting is off.")] },
      { id: "pipeline", title: "Your first pipeline", state: "todo", summary: "Make a first pipeline from a starter for one of your repositories.", actions: [act("start_wizard", true)] },
    ],
    checklist: [
      { id: "sign-in", state: "ok", title: "Sign-in is required", detail: "People must sign in (built-in accounts) before they can see or do anything." },
      { id: "tls", state: "attention", title: "Traffic is encrypted in transit", detail: "The address is plain http." },
      { id: "encryption", state: "ok", title: "Stored secrets are encrypted", detail: "Secrets in the database are sealed under a root key kept outside it." },
    ],
  };
}

/** The code of acme/shop: a tree with last commits and a README, a Go file, blame, commits, a commit, a comparison,
 * branches and tags, and a search. Keyed by the whole address (the query included) where the query decides the answer. */
export function browseFixtures(): Record<string, unknown> {
  const b = "/api/v1/repositories/acme/shop";
  const c1 = "1".repeat(40), c2 = "2".repeat(40);
  const last = (sha: string, subject: string) => ({ sha, subject, author: "ada", at: "2026-11-20T10:00:00Z" });
  const root = { path: "", type: "dir", ref: "main", commit: c2, sha: "t1", entries: [
    { name: "README.md", path: "README.md", type: "file", size: 40, sha: "r1", last: last(c1, "Add the readme") },
    { name: "price.go", path: "price.go", type: "file", size: 60, sha: "p1", last: last(c2, "Round prices") },
    { name: "docs", path: "docs", type: "dir", sha: "d1", last: last(c1, "Add docs") }] };
  const code = "package shop\n\n// Round rounds.\nfunc Round(x float64) float64 {\n\treturn x\n}\n";
  const file = { path: "price.go", type: "file", ref: "main", commit: c2, sha: "p1", size: code.length, mode: "100644", encoding: "utf8", content: code };
  const commit = { sha: c2, subject: "Round prices", message: "Round prices\n\nTo the cent.", author: "ada", authored_at: "2026-11-20T10:00:00Z", committer: "ada", committed_at: "2026-11-20T10:00:00Z", parents: 1, parent_shas: [c1] };
  const ref = (name: string, kind: string, extra = {}) => ({ name, kind, sha: c2, commit: c2, subject: "Round prices", author: "ada", committed_at: "2026-11-20T10:00:00Z", ahead: 0, behind: 0, ...extra });
  return {
    [`${b}/files?last=1`]: root,
    [`${b}/files?ref=${c2}&path=README.md`]: { path: "README.md", type: "file", ref: c2, commit: c2, sha: "r1", encoding: "utf8", content: "# Shop\n\nSee [the docs](docs/guide.md) and ![logo](logo.png).\n\n<script>alert(1)</script>\n" },
    [`${b}/files?path=price.go&last=1`]: file,
    [`${b}/files?ref=main&path=price.go&last=1`]: file,
    [`${b}/files?path=docs&last=1`]: { path: "docs", type: "dir", ref: "main", commit: c2, sha: "d1", entries: [{ name: "guide.md", path: "docs/guide.md", type: "file", size: 5, sha: "g1", last: last(c1, "Add docs") }] },
    [`${b}/commits`]: { ref: "main", items: [commit, { ...commit, sha: c1, subject: "Add the readme", message: "Add the readme", parents: 0, parent_shas: [] }] },
    [`${b}/commits/${c2}`]: { commit, runs: [{ id: "run_7", project: "shop-api", number: 7, pipeline: "ci", status: "failed", created_at: 1 }] },
    [`${b}/diff`]: { files: [{ path: "price.go", status: "modified", additions: 1, deletions: 1, binary: false }], total: 1, additions: 1, deletions: 1, truncated: false },
    [`${b}/diff/patch`]: { path: "price.go", bytes: 90, diff: "@@ -1,3 +1,4 @@\n func Round(x float64) float64 {\n-\treturn x\n+\treturn math.Round(x*100) / 100\n }\n" },
    [`${b}/blame`]: { path: "price.go", commit: c2, total: 2, from: 1, to: 2, lines: [{ n: 1, sha: c1, text: "package shop", origin: 1 }, { n: 2, sha: c2, text: "func Round() {}", origin: 2 }],
      commits: { [c1]: { sha: c1, author: "ada", at: "2026-11-01T10:00:00Z", subject: "Add the readme", path: "price.go" }, [c2]: { sha: c2, author: "grace", at: "2026-11-20T10:00:00Z", subject: "Round prices", previous: c1, previous_path: "price.go", path: "price.go" } } },
    [`${b}/branches`]: { items: [ref("fix-rounding", "branch", { ahead: 1, behind: 1, open_pull: 1, protected: false }), ref("main", "branch", { default: true, protected: true })], total: 2, truncated: false, default_branch: "main" },
    [`${b}/tags`]: { items: [ref("v1", "tag", { annotated: true, message: "Version one", tagger: "ada" })], total: 1, truncated: false },
    [`${b}/search`]: { commit: c2, files: [{ path: "price.go", matches: [{ line: 4, text: "func Round(x float64) float64 {", ranges: [[5, 10]] }] }], matches: 1, partial: true, reason: "the search stopped after 10 seconds; narrow it by folder or extension", millis: 12 },
    [`${b}/compare`]: { base: "main", head: "fix-rounding", base_sha: c1, head_sha: c2, merge_base: c1, ahead: 1, behind: 1, commits: [{ sha: c2, author: "ada", authored_at: "2026-11-20T10:00:00Z", subject: "Round prices" }],
      changes: { files: [{ path: "price.go", status: "modified", additions: 1, deletions: 1, binary: false }], total: 1, additions: 1, deletions: 1 }, mergeability: { state: "clean", behind: 1, ahead: 1 } },
  };
}
