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
