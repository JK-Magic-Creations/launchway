import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DeploymentView } from "../api";
import { MeContext } from "../auth";
import { InstallationProvider } from "../guided/facts";
import { environment, me } from "../test/fixtures";

vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return { ...real, api: { ...real.api, projects: vi.fn().mockResolvedValue([]), runs: vi.fn().mockResolvedValue([]), runners: vi.fn().mockResolvedValue([]), environments: vi.fn(),
    run: vi.fn().mockResolvedValue({ id: "run_9", step_list: [] }), deployments: vi.fn().mockResolvedValue([]), deploymentControls: vi.fn().mockResolvedValue({ controls: [] }) } };
});
vi.mock("../configApi", async (orig) => ({ ...(await orig<typeof import("../configApi")>()), configApi: { libraries: vi.fn().mockResolvedValue([]) } }));

import { api } from "../api";
import { ApplicationViewPage } from "./ApplicationView";

const dep: DeploymentView = {
  id: "d1", run_id: "run_9", run_number: 9, project: "shop", step: "deploy", environment: "prod", service: "api", target: "argocd/x", application: "shop-prod", mode: "argocd",
  version: "1.4.0", changed: true, phase: "degraded", detail: "", opened_at: 1, holding: false, observed_at: Date.now(), observer: "deploy-1",
  snapshot: { app: { found: true, sync: "Synced", health: "Degraded", health_message: "1 of 4 pods crashed", source: 0, automated: false, revision: "abcdef123456" } },
};

function show() {
  const m = me();
  vi.mocked(api.environments).mockResolvedValue([environment({ name: "prod", projects: ["shop"], services: [{ project: "shop", service: "api", waiting: [], last: dep }] })]);
  render(<MeContext.Provider value={m}><InstallationProvider me={m} approvals={[]}><ApplicationViewPage name="shop-prod" /></InstallationProvider></MeContext.Provider>);
}

beforeEach(() => { localStorage.clear(); history.pushState(null, "", "/deployments/applications/shop-prod"); });

describe("the application page", () => {
  it("opens with the diagnosis sentence, the evidence and a prominent way into Argo CD", async () => {
    show();
    expect(await screen.findByRole("heading", { name: /Version 1\.4\.0 started, but it is not healthy/ })).toBeInTheDocument();
    expect(screen.getAllByText(/1 of 4 pods crashed/).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: /Open in Argo CD/ }).length).toBeGreaterThan(0);
  });
  it("asks for the console address once, then links straight to the application", async () => {
    show();
    const user = userEvent.setup();
    await user.click((await screen.findAllByRole("button", { name: /Open in Argo CD/ }))[0]);
    await user.type(screen.getByLabelText("Address of Argo CD"), "https://argo.example");
    await user.click(screen.getByRole("button", { name: "Save and open" }));
    const link = (await screen.findAllByRole("link", { name: /Open in Argo CD/ }))[0];
    expect(link).toHaveAttribute("href", "https://argo.example/applications/shop-prod");
  });
  it("is honest about an application nobody has seen", async () => {
    const m = me();
    vi.mocked(api.environments).mockResolvedValue([]);
    render(<MeContext.Provider value={m}><InstallationProvider me={m} approvals={[]}><ApplicationViewPage name="ghost" /></InstallationProvider></MeContext.Provider>);
    expect(await screen.findByText("Launchway has not seen this application")).toBeInTheDocument();
  });
});
