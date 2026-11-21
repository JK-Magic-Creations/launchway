import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { launchRun, launchTree } from "../test/launchFixtures";
import { buildItems, cause, flatten, parents } from "../launch/model";
import { nextUp } from "../launch/Overview";
import { sources } from "../launch/Logs";
import { run as runFixture } from "../test/fixtures";

describe("what a Launch page works out", () => {
  it("builds the tree under sections, ReplicaSets under the rollout and pods under them", () => {
    const items = buildItems(launchTree().tree.nodes);
    expect(items.map((i) => i.label.replace(/ \(\d+\)/, ""))).toEqual(["Workloads", "Network and routes", "Configuration", "Scaling and disruption", "Left over from an earlier render"]);
    const rollout = items[0].children[0];
    expect(rollout.node?.kind).toBe("Rollout");
    // stable before canary, test run after
    expect(rollout.children.map((c) => c.label)).toEqual(["shop-aaa", "shop-bbb", "shop-bbb-2-3"]);
    expect(rollout.children[0].children).toHaveLength(3);
    expect(flatten(items, new Set()).length).toBe(items.length);
    expect(parents(items).has("section:workloads")).toBe(true);
  });
  it("leads with the cause: crash-looping canary pods, with the last log line", () => {
    expect(cause(launchTree(false))).toBeNull();
    const c = cause(launchTree(true));
    expect(c?.text).toMatch(/^Canary pods are not healthy: container app is waiting: CrashLoopBackOff/);
    expect(c?.text).toMatch(/Last log line: panic: database password is empty/);
  });
  it("says what happens next, and who may step in", () => {
    const ro = launchTree().tree.rollout!;
    expect(nextUp(ro, Date.now(), false)).toMatch(/only a person ends/);
    expect(nextUp({ ...ro, pause_ends: Date.now() + 90_000 }, Date.now(), false)).toMatch(/Continues by itself in 1m 29s|Continues by itself in 1m 30s/);
    expect(nextUp({ ...ro, aborted: true }, Date.now(), false)).toMatch(/aborted/);
    expect(nextUp(ro, Date.now(), true)).toMatch(/Launch is over/);
  });
  it("offers stable and canary logs, pods and test jobs, never a leftover", () => {
    const ids = sources(launchTree().tree.nodes).map((s) => s.id);
    expect(ids).toContain("role:stable");
    expect(ids).toContain("role:canary");
    expect(ids).toContain("job:Job/shop/shop-bbb-2-3-push");
    expect(ids).toContain("pod:shop-bbb-1");
  });
});

describe("the Launch page", () => {
  beforeEach(() => {
    HTMLCanvasElement.prototype.getContext = (() => null) as never;
    const base = installServer();
    void base;
  });
  afterEach(() => { vi.unstubAllGlobals(); });

  const open = async (path: string, broken = false) => {
    const run = launchRun(runFixture({ id: "run_7", number: 7, status: "running" }) as never);
    installServer({
      "/api/v1/runs/run_7": run,
      "/api/v1/runs/run_7/deployments/dep_1/inspect": launchTree(broken),
      "/api/v1/runs/run_7/deployments/dep_1/inspect/manifest": { object: "ConfigMap/shop/shop-config", yaml: "kind: ConfigMap\ndata:\n  password: '***'\n" },
      "/api/v1/runs/run_7/deployments/dep_1/controls": { environment: "prod", protected: true, controls: [], withdraw: false },
    });
    history.replaceState(null, "", path);
    render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
  };

  it("shows the strategy's steps, what happens next and the weights", async () => {
    await open("/runs/run_7/launch/dep_1");
    expect(await screen.findByRole("heading", { name: /Launch of shop to prod/ }, { timeout: 8000 })).toBeTruthy();
    const steps = await screen.findByRole("list", { name: /Steps of the canary rollout/ });
    expect(within(steps).getByText(/Pause until someone promotes/).closest("li")?.getAttribute("aria-current")).toBe("step");
    expect(screen.getByText(/Waiting at a pause that only a person ends/)).toBeTruthy();
    expect(screen.getAllByRole("img", { name: /Actual \(traffic router\): 20% to the new version, 80% to the stable one/ })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Show as a table" }));
    expect(screen.getByRole("table", { name: /Wanted and actual weights/ })).toBeTruthy();
  }, 20000);

  it("offers only the controls the cluster allows, and says why not for the rest", async () => {
    await open("/runs/run_7/launch/dep_1");
    const group = await screen.findByRole("group", { name: "Control the workload" });
    expect(within(group).getByRole("button", { name: "Pause" })).toBeTruthy();
    expect(within(group).getByRole("button", { name: "Restart the pods" })).toBeTruthy();
    expect(within(group).queryByRole("button", { name: "Scale" })).toBeNull();
    expect(within(group).getByText((_, el) => el?.tagName === "LI" && /Scale is not offered: the autoscaler shop manages the replica count/.test(el.textContent ?? ""))).toBeTruthy();
  }, 20000);

  it("leads with the cause when the canary crash-loops", async () => {
    await open("/runs/run_7/launch/dep_1", true);
    const alert = await screen.findByRole("alert", { name: "What is wrong" }, { timeout: 8000 });
    expect(alert.textContent).toMatch(/Canary pods are not healthy.*CrashLoopBackOff/);
    fireEvent.click(within(alert).getByRole("button", { name: "Open it" }));
    expect(location.search).toContain("tab=objects");
    expect(location.search).toContain("obj=Pod%2Fshop%2Fshop-bbb-1");
    expect(await screen.findByText(/panic: database password is empty/)).toBeTruthy();
  }, 20000);

  it("is a tree you can use from the keyboard, and a ConfigMap shows redacted data and keys only for a Secret", async () => {
    await open("/runs/run_7/launch/dep_1?tab=objects");
    const tree = await screen.findByRole("tree", { name: "Objects of this Launch" }, { timeout: 8000 });
    const items = within(tree).getAllByRole("treeitem");
    expect(items.filter((i) => i.getAttribute("tabindex") === "0")).toHaveLength(1);
    items[0].focus();
    fireEvent.keyDown(items[0], { key: "ArrowDown" });
    fireEvent.click(within(tree).getByRole("treeitem", { name: /ConfigMap shop-config/ }));
    expect(await screen.findByText("host=db", { exact: false })).toBeTruthy();
    expect(screen.getByText(/password=\*\*\*/)).toBeTruthy();
    fireEvent.click(within(tree).getByRole("treeitem", { name: /Secret shop-creds/ }));
    expect(await screen.findByText(/never their values/)).toBeTruthy();
    expect(location.search).toContain("obj=Secret");
  }, 20000);

  it("shows measurements against their threshold, with the query, and the test pods", async () => {
    await open("/runs/run_7/launch/dep_1?tab=traffic");
    expect(await screen.findByText(/result\[0\] >= 0.95/, {}, { timeout: 8000 })).toBeTruthy();
    expect(screen.getByText(/sum\(rate\(http_requests_total/)).toBeTruthy();
    expect(screen.getByText("✓ pass")).toBeTruthy();
    expect(screen.getByText("✕ fail")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Read its output" })).toBeTruthy();
  }, 20000);
});
