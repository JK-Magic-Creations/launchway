import { describe, expect, it } from "vitest";
import type { DeploymentView, EnvironmentView, UnitView } from "../api";
import { approval, environment, me, project, run, runner, step } from "../test/fixtures";
import { applications, attention, checklist, explainFailure, needsLook, nextStep, plainHealth, plainSync } from "./derive";

const empty = { projects: [], runners: [], runs: [], environments: [] };

describe("checklist", () => {
  it("starts with a project and is done by real state, not clicks", () => {
    const items = checklist(me(), empty);
    expect(items.map((i) => i.id)).toEqual(["project", "runner", "run", "environment", "signin"]);
    expect(nextStep(items).item?.id).toBe("project");
    const some = checklist(me({ auth_mode: "none" }), { projects: [project()], runners: [runner()], runs: [run()], environments: [environment()] });
    expect(some.filter((i) => i.done).map((i) => i.id)).toEqual(["project", "runner", "run", "environment"]);
    expect(nextStep(some)).toMatchObject({ done: 4, total: 5 });
    expect(nextStep(some).item?.id).toBe("signin");
  });
  it("counts an offline runner and a failed run as not done", () => {
    const items = checklist(me(), { ...empty, runners: [runner({ online: false })], runs: [run({ status: "failed" })] });
    expect(items.find((i) => i.id === "runner")?.done).toBe(false);
    expect(items.find((i) => i.id === "run")?.done).toBe(false);
  });
  it("leaves out what does not apply", () => {
    expect(checklist(me({ runners: false, admin: false }), empty).map((i) => i.id)).toEqual(["project", "run", "environment"]);
  });
  it("is finished when everything is", () => {
    const items = checklist(me(), { projects: [project()], runners: [runner()], runs: [run()], environments: [environment()] });
    expect(nextStep(items).item).toBeNull();
  });
  it("points the first-run item at the first project", () => {
    const items = checklist(me(), { ...empty, projects: [project({ name: "a b" })] });
    expect(items.find((i) => i.id === "run")?.href).toBe("/projects/a%20b");
  });
});

const dep = (over: Partial<DeploymentView>): DeploymentView => ({
  id: "d1", run_id: "run_9", run_number: 9, project: "shop-api", step: "deploy", environment: "staging", service: "api", target: "argocd/shop-api-staging",
  application: "shop-api-staging", mode: "argocd", version: "1.2.0", changed: true, phase: "healthy", detail: "", opened_at: 5, holding: false, observed_at: 100,
  snapshot: { app: { found: true, sync: "Synced", health: "Healthy", source: 0, automated: true, revision: "abc12345" } }, ...over,
});
const unit = (over: Partial<UnitView>): UnitView => ({ project: "shop-api", service: "api", waiting: [], ...over });
const env = (name: string, units: UnitView[]): EnvironmentView => environment({ name, services: units });

describe("applications", () => {
  it("lists the applications the deploy runners saw, newest observation winning", () => {
    const rows = applications([
      env("staging", [unit({ last: dep({}) })]),
      env("production", [unit({ last: dep({ application: "shop-api-prod", observed_at: 50, snapshot: { app: { found: true, sync: "OutOfSync", health: "Degraded", source: 0, automated: false, health_message: "1 of 3 crashing" } } }) })]),
    ]);
    expect(rows.map((r) => r.name)).toEqual(["shop-api-prod", "shop-api-staging"]);
    expect(rows[0]).toMatchObject({ sync: "OutOfSync", health: "Degraded", healthMessage: "1 of 3 crashing", environment: "production" });
    expect(rows.map(needsLook)).toEqual([true, false]);
  });
  it("prefers a newer observation made while nothing was deploying", () => {
    const rows = applications([env("staging", [unit({ last: dep({}), live: { deployment: "d1", observed_at: 200, observed_by: "k8s-deploy", snapshot: { app: { found: true, sync: "OutOfSync", health: "Healthy", source: 0, automated: true } } } })])]);
    expect(rows[0]).toMatchObject({ sync: "OutOfSync", observedAt: 200, observedBy: "k8s-deploy" });
  });
  it("shows an application not found as missing, and skips services never deployed", () => {
    const rows = applications([env("staging", [unit({ last: dep({ snapshot: { app: { found: false, sync: "", health: "", source: 0, automated: false } } }) }), unit({ service: "web" })])]);
    expect(rows).toHaveLength(1);
    expect(rows[0].health).toBe("Missing");
  });
  it("is empty when nothing has an application", () => { expect(applications([env("dev", [])])).toEqual([]); });
  it("says Argo CD's words plainly", () => {
    expect(plainSync("OutOfSync").label).toBe("Differs from Git");
    expect(plainSync("").label).toBe("Not known yet");
    expect(plainHealth("Degraded")).toEqual({ label: "Unhealthy", tone: "bad" });
    expect(plainHealth("Progressing").label).toBe("Starting up");
  });
});

describe("attention", () => {
  const base = { projects: [], approvals: [], runners: [], applications: [], me: me() };
  it("is empty when nothing needs anyone", () => { expect(attention(base)).toEqual([]); });
  it("puts failed runs and broken applications before waiting approvals and offline runners", () => {
    const apps = applications([env("production", [unit({ last: dep({ snapshot: { app: { found: true, sync: "Synced", health: "Degraded", source: 0, automated: false } } }) })])]);
    const list = attention({
      projects: [project({ last_run: run({ id: "run_412", number: 412, status: "failed" }) })],
      approvals: [approval()], runners: [runner({ online: false, name: "edge" })], applications: apps, me: me(),
    });
    expect(list.map((a) => a.key.split(":")[0])).toEqual(["run", "app", "approval", "runner"]);
    expect(list[0]).toMatchObject({ title: "Sortie #412 of shop-api failed", button: "See what went wrong", href: "/runs/run_412" });
    expect(list[0].entities).toEqual([{ kind: "project", name: "shop-api" }]);
  });
  it("does not mention runners to a person who cannot see them", () => {
    expect(attention({ ...base, runners: [runner({ online: false })], me: me({ runners: false }) })).toEqual([]);
  });
  it("reports a drifted application as a warning, not an error", () => {
    const apps = applications([env("staging", [unit({ last: dep({ snapshot: { app: { found: true, sync: "OutOfSync", health: "Healthy", source: 0, automated: false } } }) })])]);
    expect(attention({ ...base, applications: apps })[0].tone).toBe("warn");
  });
});

describe("explainFailure", () => {
  it("is null when nothing failed", () => { expect(explainFailure([step({ status: "succeeded" })])).toBeNull(); });
  it("explains a command that ended with an exit code, with its last lines", () => {
    const f = explainFailure([step({ id: "build", status: "succeeded", position: 1 }), step({ exit_code: 2, excerpt: "boom" })])!;
    expect(f).toMatchObject({ kind: "exit", step: "test", exitCode: 2, excerpt: "boom", others: 0 });
    expect(f.headline).toContain("exit code 2");
  });
  it("says a timeout is a timeout", () => {
    const f = explainFailure([step({ error: "timed out after 10m0s", exit_code: null })])!;
    expect(f.kind).toBe("timeout");
    expect(f.advice[0]).toContain("10m");
  });
  it("says exit code 137 is most likely out of memory, not certain", () => {
    const f = explainFailure([step({ exit_code: 137, error: "exit code 137" })])!;
    expect(f.kind).toBe("killed");
    expect(f.headline).toMatch(/most likely.*memory/);
  });
  it("says a rule refused the step, not that the step broke", () => {
    const f = explainFailure([step({ policy_blocked: true, error: "policy x refused" })])!;
    expect(f.kind).toBe("policy");
    expect(f.headline).toMatch(/rule stopped/);
  });
  it("says when the command never ran", () => {
    const f = explainFailure([step({ exit_code: null, error: "image pull failed" })])!;
    expect(f.kind).toBe("infrastructure");
  });
  it("explains the first failed step and counts the others", () => {
    const f = explainFailure([step({ id: "b", position: 3 }), step({ id: "a", position: 1 })])!;
    expect(f.step).toBe("a");
    expect(f.others).toBe(1);
  });
  it("never leaves a failed step unexplained", () => {
    expect(explainFailure([step({ exit_code: null, error: "" })])!.kind).toBe("unknown");
  });
});
