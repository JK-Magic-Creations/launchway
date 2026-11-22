import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi, afterEach } from "vitest";
import type { DeploymentView } from "../api";
import { MeContext } from "../auth";
import { InstallationProvider } from "../guided/facts";
import { environment, me, project, run, runner } from "../test/fixtures";
import { expectEntitiesLinked } from "../test/entities";

const dep: DeploymentView = {
  id: "d1", run_id: "run_9", run_number: 9, project: "shop-api", step: "deploy", environment: "staging", service: "api", target: "argocd/x", application: "shop-api-staging",
  mode: "argocd", version: "1.2.0", changed: true, phase: "healthy", detail: "", opened_at: 5, holding: false, observed_at: 100,
  snapshot: { app: { found: true, sync: "OutOfSync", health: "Healthy", source: 0, automated: true } },
};
const envs = [environment({ name: "staging", projects: ["shop-api"], services: [{ project: "shop-api", service: "api", waiting: [], last: dep, current: { deployment: "d1", version: "1.2.0", run: "run_9", run_number: 9, since: 5 } }] })];

vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return {
    ...real,
    api: {
      ...real.api,
      projects: vi.fn(), runs: vi.fn(), runners: vi.fn(), environments: vi.fn(), project: vi.fn(), refs: vi.fn().mockResolvedValue([]),
    },
  };
});
vi.mock("../configApi", async (orig) => {
  const real = await orig<typeof import("../configApi")>();
  return { ...real, configApi: { ...real.configApi, libraries: vi.fn(), templates: vi.fn() } };
});
vi.mock("../triggers", async (orig) => {
  const real = await orig<typeof import("../triggers")>();
  return { ...real, triggersApi: { ...real.triggersApi, schedules: vi.fn().mockRejectedValue(new Error("none")) } };
});

import { api } from "../api";
import { configApi } from "../configApi";
import { ApplicationsListPage as ApplicationsPage } from "./ApplicationsList";
import { DeploymentsOverviewPage as DeploymentsPage } from "./DeploymentsOverview";
import { TemplatesPage } from "./Library";
import { RepoPage as ProjectPage } from "./RepoPage";
import { CodeListPage as ProjectsPage } from "./CodeList";
import { setTerms, resetTerms } from "../terms";
// These tests are about behavior and read the plain words: the plain business set. The themed words have their own tests (themed.test.tsx).
beforeEach(() => setTerms({ set: "business" }));
afterEach(() => resetTerms());

const NAMES = ["shop-api", "shop-api-staging", "staging", "platform", "go-service", "build-1"];

function app(ui: React.ReactNode) {
  return render(<MeContext.Provider value={me()}><InstallationProvider me={me()} approvals={[]}>{ui}</InstallationProvider></MeContext.Provider>);
}

beforeEach(() => {
  history.pushState(null, "", "/");
  vi.mocked(api.projects).mockResolvedValue([project({ last_run: run() })]);
  vi.mocked(api.project).mockResolvedValue(project({ last_run: run() }));
  vi.mocked(api.runs).mockResolvedValue([run()]);
  vi.mocked(api.runners).mockResolvedValue([runner()]);
  vi.mocked(api.environments).mockResolvedValue(envs);
  vi.mocked(configApi.libraries).mockResolvedValue([{ name: "platform", last_fetch: 1 }]);
  vi.mocked(configApi.templates).mockResolvedValue({ library: "platform", version: "v1", commit: "abcdef123456", versions: ["v1"], templates: [{ name: "go-service", description: "Builds a Go service", params: [], outputs: [] }] });
});

describe("every name is a link", () => {
  it("on the projects list", async () => {
    const { container } = app(<ProjectsPage />);
    await screen.findByRole("link", { name: "shop-api" });
    expectEntitiesLinked(container, NAMES);
    expect(screen.getByRole("link", { name: "shop-api" })).toHaveAttribute("href", "/projects/shop-api");
  });
  it("on a project's page", async () => {
    const { container } = app(<ProjectPage name="shop-api" />);
    await waitFor(() => expect(screen.getByText("Deployed to")).toBeInTheDocument());
    await screen.findByRole("link", { name: "staging" });
    expectEntitiesLinked(container, ["staging", "build-1"]);
  });
  it("on the deployments overview", async () => {
    const { container } = app(<DeploymentsPage />);
    await screen.findByRole("link", { name: "staging" });
    expectEntitiesLinked(container, NAMES);
  });
  it("on the applications list, with Argo CD's states in plain words", async () => {
    const { container } = app(<ApplicationsPage />);
    await screen.findByRole("link", { name: "shop-api-staging" });
    expectEntitiesLinked(container, NAMES);
    expect(screen.getByText("Differs from Git")).toBeInTheDocument();
    expect(screen.getByText("Healthy")).toBeInTheDocument();
  });
  it("on the templates list", async () => {
    const { container } = app(<TemplatesPage />);
    await screen.findByRole("link", { name: "go-service" });
    expect(screen.getByRole("link", { name: "go-service" })).toHaveAttribute("href", "/library/templates/platform/go-service");
    expectEntitiesLinked(container, ["go-service"]);
  });
});

describe("honest empty states", () => {
  it("the projects list offers the action that adds one", async () => {
    vi.mocked(api.projects).mockResolvedValue([]);
    app(<ProjectsPage />);
    expect(await screen.findByText("No Codebases yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add a Codebase" })).toHaveAttribute("href", "/projects/new");
  });
  it("deployments with no environment offers the wizard", async () => {
    vi.mocked(api.environments).mockResolvedValue([]);
    app(<DeploymentsPage />);
    expect(await screen.findByText("No environments yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add an environment" })).toHaveAttribute("href", "/config/wizard/add-environment");
  });
  it("applications explains what makes one appear", async () => {
    vi.mocked(api.environments).mockResolvedValue([environment({ services: [] })]);
    app(<ApplicationsPage />);
    expect(await screen.findByText("No applications seen yet")).toBeInTheDocument();
  });
});
