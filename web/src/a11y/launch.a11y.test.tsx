import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";
import { launchRun, launchTree } from "../test/launchFixtures";
import { run as runFixture } from "../test/fixtures";

// Every tab of the Launch page, and the failing canary, scanned by axe-core in both themes with an empty baseline.
describe.each(["light", "dark"])("the Launch page, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  const open = async (path: string, broken = false) => {
    const t = Date.now();
    const lines = [{ t: t - 3000, src: "stable/shop-aaa-1", line: "GET /cart 200 12ms" }, { t: t - 2000, src: "canary/shop-bbb-1", line: "GET /cart 500 3ms" }];
    installServer({
      "/api/v1/runs/run_7": launchRun(runFixture({ id: "run_7", number: 7, status: "running" }) as never),
      "/api/v1/runs/run_7/deployments/dep_1/inspect": launchTree(broken),
      "/api/v1/runs/run_7/deployments/dep_1/inspect/manifest": { object: "x", yaml: "kind: ConfigMap\ndata:\n  password: '***'\n" },
      "/api/v1/runs/run_7/deployments/dep_1/inspect/logs": { lines, notes: [] },
      "/api/v1/runs/run_7/deployments/dep_1/controls": { environment: "prod", protected: true, controls: [], withdraw: false },
    });
    history.replaceState(null, "", path);
    render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
  };
  const expectClean = async (what: string) => {
    await new Promise((r) => setTimeout(r, 200));
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it("the overview, mid-canary", async () => {
    await open("/runs/run_7/launch/dep_1");
    await screen.findByRole("list", { name: /Steps of the canary rollout/ }, { timeout: 8000 });
    await expectClean("overview");
  }, 20000);
  it("the overview of a failing canary", async () => {
    await open("/runs/run_7/launch/dep_1", true);
    await screen.findByRole("alert", { name: "What is wrong" }, { timeout: 8000 });
    await expectClean("failing canary");
  }, 20000);
  it("the object tree with a ConfigMap open", async () => {
    await open("/runs/run_7/launch/dep_1?tab=objects&obj=ConfigMap%2Fshop%2Fshop-config");
    await screen.findByText(/host=db/, { exact: false }, { timeout: 8000 });
    await expectClean("objects");
  }, 20000);
  it("a pod with its containers", async () => {
    await open("/runs/run_7/launch/dep_1?tab=objects&obj=Pod%2Fshop%2Fshop-bbb-1", true);
    await screen.findByRole("table", { name: /Containers of shop-bbb-1/ }, { timeout: 8000 });
    await expectClean("pod");
  }, 20000);
  it("the logs, stable and canary side by side", async () => {
    await open("/runs/run_7/launch/dep_1?tab=logs&side=1");
    await screen.findAllByRole("log", {}, { timeout: 8000 });
    await waitFor(() => expect(screen.getAllByText(/GET \/cart/).length).toBeGreaterThan(0));
    await expectClean("logs");
  }, 20000);
  it("traffic and tests", async () => {
    await open("/runs/run_7/launch/dep_1?tab=traffic");
    await screen.findByText(/result\[0\] >= 0.95/, {}, { timeout: 8000 });
    await expectClean("traffic");
  }, 20000);
  it("history", async () => {
    await open("/runs/run_7/launch/dep_1?tab=history");
    await screen.findByRole("heading", { name: "Commands to the rollout" }, { timeout: 8000 });
    await expectClean("history");
  }, 20000);
});
