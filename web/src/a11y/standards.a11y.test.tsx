import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { InspectionPanel } from "../components/Inspection";
import { ProjectStandardsPage, SettingsStandardsPage } from "../pages/Standards";
import { MeContext } from "../auth";
import { installServer } from "../test/mockApi";
import { me } from "../test/fixtures";
import { backlog, coverage, effective, emptyBacklog, emptySeries, inspection, profile, projectStandards, series } from "../test/standardsFixtures";
import { scan } from "../test/axe";

// Standing orders: the project page, the level settings and the Inspection of a run, with data and empty,
// scanned by axe-core in both themes.
describe.each(["light", "dark"])("Standing orders pages, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });
  const clean = async (what: string) => {
    await new Promise((r) => setTimeout(r, 100));
    const found = await scan(document.body, { page: false });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };
  const server = (over: Record<string, unknown>) => installServer({ "/api/v1/standards/profile": profile(), ...over });

  it("the project page with findings, a lock and a notice", async () => {
    server({
      "/api/v1/projects/shop-api/standards": projectStandards({ effective: effective({ notices: ["The repository's own file sets the state of the check Secrets to advise, but it is locked by the Wing (the security team requires it), so block still applies."] }) }),
      "/api/v1/projects/shop-api/findings": backlog(), "/api/v1/projects/shop-api/findings/history": series(),
    });
    render(<MeContext.Provider value={me()}><ProjectStandardsPage name="shop-api" /></MeContext.Provider>);
    await screen.findByText("Worth knowing");
    await screen.findAllByText(/Package 'golang.org\/x\/net@0.17.0'/);
    expect(screen.getByText(/mean days to fix/)).toBeTruthy();
    expect(screen.getByText("The same as a table")).toBeTruthy();
    await clean("project page");
  }, 20000);

  it("the project page with nothing recorded yet", async () => {
    server({ "/api/v1/projects/shop-api/standards": projectStandards(), "/api/v1/projects/shop-api/findings": emptyBacklog(), "/api/v1/projects/shop-api/findings/history": emptySeries() });
    render(<MeContext.Provider value={me()}><ProjectStandardsPage name="shop-api" /></MeContext.Provider>);
    await screen.findByText(/The default branch has not been inspected yet/);
    await clean("project page, empty");
  }, 20000);

  it("says plainly when Standing orders are switched off", async () => {
    server({
      "/api/v1/projects/shop-api/standards": projectStandards({ effective: effective({ off: { value: true, from: { kind: "level", level: "project", scope: "shop-api" } }, off_reason: "evaluating another scanner" }) }),
      "/api/v1/projects/shop-api/findings": emptyBacklog(), "/api/v1/projects/shop-api/findings/history": emptySeries(),
    });
    render(<MeContext.Provider value={me()}><ProjectStandardsPage name="shop-api" /></MeContext.Provider>);
    await screen.findByText("Standing orders are switched off");
    expect(screen.getByText(/evaluating another scanner/)).toBeTruthy();
    await clean("off");
  }, 20000);

  it("the settings of a level with the coverage table", async () => {
    server({ "/api/v1/standards/settings": { level: "installation", scope: "", settings: {}, version: 0, words: {} }, "/api/v1/standards/coverage": coverage() });
    render(<MeContext.Provider value={me()}><SettingsStandardsPage /></MeContext.Provider>);
    await screen.findByText("Which repositories have which checks");
    await screen.findByText("What Base says");
    await clean("level settings");
  }, 20000);

  it("the Inspection of a blocked run, a finding open", async () => {
    server({ "/api/v1/runs/run_7/inspection": inspection(true) });
    render(<MeContext.Provider value={me()}><InspectionPanel runId="run_7" project="shop-api" version="1" /></MeContext.Provider>);
    expect(await screen.findByText("Blocked")).toBeTruthy();
    expect(screen.getByText(/Added by Standing orders/)).toBeTruthy();
    const summary = (await screen.findAllByText(/Package 'example.org\/lib@1.0.0'/))[0];
    summary.click();
    await waitFor(() => expect(screen.getByText("How to fix")).toBeTruthy());
    await clean("inspection");
  }, 20000);

  it("the Inspection of a run that passed, with nothing found", async () => {
    server({ "/api/v1/runs/run_7/inspection": { ...inspection(false), findings: [], matched: 0, counts: {} } });
    render(<MeContext.Provider value={me()}><InspectionPanel runId="run_7" project="shop-api" version="1" /></MeContext.Provider>);
    expect(await screen.findByText("Passed")).toBeTruthy();
    expect(screen.getByText("The checks found nothing.")).toBeTruthy();
    await clean("inspection, nothing found");
  }, 20000);

  it("shows nothing for a run without an Inspection", async () => {
    server({ "/api/v1/runs/run_7/inspection": { present: false, blocked: false, summary: "", default_branch: false, counts: {}, checks: [], findings: [], matched: 0 } });
    const { container } = render(<MeContext.Provider value={me()}><InspectionPanel runId="run_7" project="shop-api" version="1" /></MeContext.Provider>);
    await new Promise((r) => setTimeout(r, 150));
    expect(container.textContent).toBe("");
  });
});
