import { setTerms, resetTerms } from "../terms";
import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MeContext } from "../auth";
import { InstallationProvider } from "../guided/facts";
import { approval, me } from "../test/fixtures";
import type { Approvals } from "../live";

vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return { ...real, api: { ...real.api, projects: vi.fn().mockResolvedValue([]), runs: vi.fn().mockResolvedValue([]), runners: vi.fn().mockResolvedValue([]), environments: vi.fn().mockResolvedValue([]),
    run: vi.fn().mockResolvedValue({ id: "run_2", sha: "c0a3b19aaaa", event: "push", step_list: [], tests: { total: 40, passed: 40, failed: 0, errored: 0, skipped: 0, flaky: 0, duration_ms: 1 } }), approve: vi.fn(), reject: vi.fn() } };
});
vi.mock("../api/v2b", () => ({ v2bApi: { approvalSettings: vi.fn().mockResolvedValue({ allow_self: false, sign_in: true }) } }));
vi.mock("../configApi", async (orig) => ({ ...(await orig<typeof import("../configApi")>()), configApi: { libraries: vi.fn().mockResolvedValue([]) } }));

import { DecisionsPage } from "./Decisions";

const feed = (list: Approvals["list"]): Approvals => ({ list, error: null, live: true });
function show(who: Parameters<typeof me>[0], a = approval({ started_by: "sam" })) {
  const m = me(who);
  render(<MeContext.Provider value={m}><InstallationProvider me={m} approvals={[a]}><DecisionsPage approvals={feed([a])} /></InstallationProvider></MeContext.Provider>);
}

beforeEach(() => { history.pushState(null, "", "/activity/approvals"); });

afterEach(() => resetTerms());

describe("the decision card", () => {
  it("tells the person who started the run exactly why they cannot approve, with the link", async () => {
    show({ user: "sam", roles: { "shop-web": "operator" } });
    expect(await screen.findByText(/You started this run\. On this base the person who starts a run cannot give clearance for it/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings → Clearances" })).toHaveAttribute("href", "/settings/approvals");
    expect(screen.getByRole("button", { name: "Go" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "No-go" })).toBeDisabled(); // a reason is needed first
  });
  it("shows the evidence that exists and what each answer does", async () => {
    show({ user: "dana", roles: { "shop-web": "operator" } });
    expect(await screen.findByText("40 of 40")).toBeInTheDocument();
    expect(screen.getByText("no scan in this run")).toBeInTheDocument();
    expect(screen.getByText("If you say Go")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Go" })).toBeEnabled();
  });
  it("uses the plain business words when that set is chosen, with the same consequences", async () => {
    setTerms({ set: "business" });
    show({ user: "dana", roles: { "shop-web": "operator" } });
    expect(await screen.findByText("If you say Approve")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
    expect(screen.getByText("If you say Reject")).toBeInTheDocument();
  });
  it("says when nothing is waiting", () => {
    const m = me();
    render(<MeContext.Provider value={m}><InstallationProvider me={m} approvals={[]}><DecisionsPage approvals={feed([])} /></InstallationProvider></MeContext.Provider>);
    expect(screen.getByText("All clear")).toBeInTheDocument();
  });
});
