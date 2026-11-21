import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MeContext } from "../auth";
import { InstallationProvider } from "../guided/facts";
import { me } from "../test/fixtures";
import { resetTerms } from "../terms";

vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return { ...real, api: { ...real.api, projects: vi.fn().mockResolvedValue([]), runs: vi.fn().mockResolvedValue([]), runners: vi.fn().mockResolvedValue([]), environments: vi.fn().mockResolvedValue([]) } };
});
vi.mock("../configApi", async (orig) => ({
  ...(await orig<typeof import("../configApi")>()),
  configApi: {
    libraries: vi.fn().mockResolvedValue([]),
    templates: vi.fn().mockResolvedValue({ library: "launchway", version: "builtin", commit: "0123456789abcdef0123456789abcdef01234567", versions: ["builtin"], templates: [
      { name: "go-service", kind: "flight plan", description: "Flight plan: a Go service with a Dockerfile", outputs: [],
        params: [{ name: "push", type: "boolean", default: "false", description: "Push the image" }],
        example: "pipeline: my-project\nsteps:\n  ci:\n    uses: launchway/go-service\n",
        graph: [{ id: "ci/check", description: "Format and vet" }, { id: "ci/build", needs: ["ci/check"], description: "Build" }] },
      { name: "secret-scan", kind: "step template", description: "Maneuver: look for secrets", outputs: [], params: [] },
    ] }),
  },
}));

import { StartersPage } from "./Starters";

beforeEach(() => { history.pushState(null, "", "/library/starters"); });
afterEach(() => resetTerms());

describe("the starters gallery", () => {
  it("lists flight plans and step templates apart, and shows a starter's inputs, example and steps on request", async () => {
    const m = me({});
    render(<MeContext.Provider value={m}><InstallationProvider me={m} approvals={[]}><StartersPage /></InstallationProvider></MeContext.Provider>);
    await waitFor(() => screen.getByText("launchway/go-service"));
    expect(screen.getByText("launchway/secret-scan")).toBeTruthy();
    expect(screen.getByText("A Go service with a Dockerfile")).toBeTruthy(); // the kind prefix is dropped, and it starts with a capital
    await userEvent.click(screen.getAllByRole("button", { name: "Show details" })[0]);
    expect(screen.getByText("push")).toBeTruthy();
    expect(document.querySelector("pre")?.textContent).toContain("uses: launchway/go-service");
    expect(screen.getByText("check")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
  });

  it("filters by what is typed", async () => {
    const m = me({});
    render(<MeContext.Provider value={m}><InstallationProvider me={m} approvals={[]}><StartersPage /></InstallationProvider></MeContext.Provider>);
    await waitFor(() => screen.getByText("launchway/go-service"));
    await userEvent.type(screen.getByLabelText(/Find a starter/), "secret");
    expect(screen.queryByText("launchway/go-service")).toBeNull();
    expect(screen.getByText("launchway/secret-scan")).toBeTruthy();
  });
});
