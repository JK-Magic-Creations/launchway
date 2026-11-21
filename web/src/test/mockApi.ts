import { vi } from "vitest";
import type { RunDetail } from "../api";
import { approval, environment, me, project, run, runner, step } from "./fixtures";

/** A stand-in for the server for tests that draw whole pages: fixtures for the main lists, an empty answer
 * for anything else. `overrides` maps a path (without the query) to the JSON to answer with. */
export function installServer(overrides: Record<string, unknown> = {}, who = me()) {
  const failed = run({ id: "run_7", number: 7, status: "failed", message: "Fix the price rounding", steps: [{ id: "test", status: "failed" }] });
  const detail: RunDetail = {
    ...failed, source: { kind: "git", url: "https://git.example/acme/shop-api.git" }, graph: { name: "shop-api", on: {}, steps: [{ id: "test", image: "golang", run: "go test", retry: 0 }] } as unknown as RunDetail["graph"],
    levels: [["test"]], step_list: [step()], reruns: [], not_understood: [],
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
    "/api/v1/projects/shop-api/variables": { words: {}, variables: [] },
    "/api/v1/projects/shop-api/tests": { ref: "main", runs: [], slowest: [], failing: [], flaky: [], refs: ["main"] },
    "/api/v1/runner-pools": { pools: [], manage: true, identities: false },
    "/api/v1/policies": { catalogue: [], sources: [], recent: [], max_waiver_days: 30 },
    "/api/v1/waivers": { waivers: [], waive_projects: [], rules: {} },
    "/api/v1/config/history": { changes: [] },
    ...forgeFixtures(),
    ...rollbackFixtures(detail),
    ...overrides,
  };
  class FakeSource { onopen = null; onerror = null; addEventListener() {} removeEventListener() {} close() {} }
  vi.stubGlobal("EventSource", FakeSource);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input).split("?")[0];
    const hit = answers[path];
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
