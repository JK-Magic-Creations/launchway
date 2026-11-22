import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import { MeContext } from "../auth";
import { InstallationProvider } from "../guided/facts";
import { me } from "../test/fixtures";
import { CodeListPage } from "./CodeList";
import { forgeApi } from "../forgeApi";
import { setTerms, resetTerms } from "../terms";

vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return { ...real, api: { ...real.api, projects: vi.fn(), runs: vi.fn(), runners: vi.fn(), environments: vi.fn() } };
});
vi.mock("../forgeApi", async (orig) => {
  const real = await orig<typeof import("../forgeApi")>();
  return { ...real, forgeApi: { ...real.forgeApi, repositories: vi.fn() } };
});
beforeEach(() => { setTerms({ set: "business" }); history.pushState(null, "", "/projects"); });
afterEach(() => { resetTerms(); vi.unstubAllGlobals(); });

// The owner hit "None is connected yet" on production with 44 hosted repositories and no project.
describe("the Code page with hosted repositories and no project", () => {
  it("lists the hosted repositories and links into browsing them", async () => {
    vi.mocked(api.projects).mockResolvedValue([]);
    vi.mocked(api.runs).mockResolvedValue([]);
    vi.mocked(api.runners).mockResolvedValue([]);
    vi.mocked(api.environments).mockResolvedValue([]);
    const repo = (n: string) => ({ id: n, name: n, home: "acme", path: `acme/${n}`, visibility: "private", default_branch: "main", clone: { https: "" } });
    vi.mocked(forgeApi.repositories).mockResolvedValue([repo("shop"), repo("docs")] as never);
    render(<MeContext.Provider value={me()}><InstallationProvider me={me()} approvals={[]}><CodeListPage /></InstallationProvider></MeContext.Provider>);
    expect(await screen.findByRole("link", { name: "acme/shop" })).toHaveAttribute("href", "/repositories/acme/shop");
    expect(screen.getByRole("link", { name: "acme/docs" })).toBeInTheDocument();
    expect(screen.getByText(/2 repositories are hosted by Launchway; none is connected as a Codebase/)).toBeInTheDocument();
    expect(screen.queryByText(/None is connected yet/)).not.toBeInTheDocument();
  });
});
