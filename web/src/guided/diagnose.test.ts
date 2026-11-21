import { describe, expect, it } from "vitest";
import type { DeploymentView } from "../api";
import { argoLink, normalizeArgoUrl } from "./argo";
import { diagnose, logTail } from "./diagnose";
import { setTerms, resetTerms } from "../terms";
import { afterEach, beforeEach } from "vitest";
// These tests are about behavior and read the plain words: the plain business set. The themed words have their own tests (themed.test.tsx).
beforeEach(() => setTerms({ set: "business" }));
afterEach(() => resetTerms());

const dep = (over: Partial<DeploymentView> = {}): DeploymentView => ({
  id: "d1", run_id: "run_9", run_number: 9, project: "shop", step: "deploy", environment: "prod", service: "api", target: "argocd/x", application: "shop-prod",
  mode: "argocd", version: "1.4.0", changed: true, phase: "", detail: "", opened_at: 1, holding: false, observer: "deploy-1", ...over,
});

describe("diagnose", () => {
  it("says nothing is deployed when there is no deployment", () => {
    const d = diagnose(undefined, undefined, undefined, 10);
    expect(d.tone).toBe("quiet");
    expect(d.recommend).toBeNull();
  });
  it("names the failed analysis check and recommends rolling back a rollout in motion", () => {
    const d = diagnose(dep({ phase: "degraded", snapshot: { app: { found: true, sync: "Synced", health: "Degraded", health_message: "1 of 4 pods crashed", source: 0, automated: false },
      rollout: { found: true, phase: "Degraded", generation: 2, observed: true, steps: 3, step: 1, paused: false, aborted: false, replicas: 4, ready: 3, updated: 1, available: 3 },
      analysis: [{ name: "error-rate", phase: "Failed", metrics: [{ name: "http-errors", phase: "Failed", count: 5, successful: 2, failed: 3, inconclusive: 0, error: 0 }] }] } }), undefined, undefined, 10);
    expect(d.tone).toBe("bad");
    expect(d.known).toBe(true);
    expect(d.evidence.map((e) => e.text).join(" ")).toContain("error-rate failed: http-errors failed 3 of 5 measurements");
    expect(d.recommend).toMatchObject({ action: "abort", label: "Roll back" });
  });
  it("recommends a retry for an aborted rollout and promote for an indefinite pause", () => {
    const ro = { found: true, generation: 2, observed: true, steps: 3, step: 1, replicas: 4, ready: 4, updated: 4, available: 4 };
    expect(diagnose(dep({ phase: "rollout_aborted", snapshot: { rollout: { ...ro, phase: "Degraded", paused: false, aborted: true } } }), undefined, undefined, 1).recommend?.action).toBe("retry");
    const p = diagnose(dep({ phase: "rollout_paused", snapshot: { rollout: { ...ro, phase: "Paused", paused: true, aborted: false, pause_indefinite: true, pause_reasons: ["CanaryPauseStep"] } } }), undefined, undefined, 1);
    expect(p.tone).toBe("wait");
    expect(p.headline).toContain("waits for a person to promote it");
    expect(p.recommend?.action).toBe("promote");
  });
  it("says Launchway cannot tell why when a failure has no evidence", () => {
    const d = diagnose(dep({ verdict: "failed" }), undefined, undefined, 1);
    expect(d.known).toBe(false);
    expect(d.headline).toContain("Launchway cannot tell why");
    expect(d.recommend?.action).toBe("open");
  });
  it("uses the tail of the step log as evidence", () => {
    const d = diagnose(dep({ verdict: "failed", detail: "sync failed" }), undefined, { excerpt: "a\n\nb\nc\nlookup db: no such host\n" } as never, 1);
    expect(d.known).toBe(true);
    expect(d.evidence.some((e) => e.text.includes("lookup db: no such host"))).toBe(true);
  });
  it("says no runner serves the target when nothing observes it", () => {
    const d = diagnose(dep({ phase: "unobserved", observer: undefined }), undefined, undefined, 1);
    expect(d.headline).toContain("No deploy runner serves target argocd/x");
  });
  it("reports drift of a deployed application", () => {
    const d = diagnose(dep({ verdict: "deployed", phase: "healthy" }), { deployment: "d1", observed_at: 5, observed_by: "r", drift: "out_of_sync" }, undefined, 9);
    expect(d.tone).toBe("wait");
    expect(d.headline).toContain("no longer matches");
  });
  it("logTail keeps the last non-blank lines", () => { expect(logTail("a\n\nb\nc\nd", 2)).toEqual(["c", "d"]); });
});

describe("argoLink", () => {
  it("links to the application and to a resource of it", () => {
    expect(argoLink("https://argo.example", "shop-prod")).toBe("https://argo.example/applications/shop-prod");
    expect(argoLink("https://argo.example", "shop-prod", { kind: "Deployment", name: "api", namespace: "shop", group: "apps" })).toContain("node=apps%2FDeployment%2Fshop%2Fapi%2F0");
  });
  it("accepts only web addresses", () => {
    expect(normalizeArgoUrl(" https://argo.example/ ")).toBe("https://argo.example");
    expect(normalizeArgoUrl("javascript:alert(1)")).toBe("");
    expect(normalizeArgoUrl("argo")).toBe("");
  });
});
