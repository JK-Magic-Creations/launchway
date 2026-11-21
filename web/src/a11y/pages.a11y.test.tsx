import { render, screen, waitFor } from "@testing-library/react";
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";
import baseline from "./baseline.json";

// Every routed page of the interface, drawn whole (shell and page) with fixtures, in both themes, and scanned
// by axe-core. See docs/contributing/design-principles.md; the browser pass (make web-a11y) covers what jsdom cannot.
const PAGES = [
  "/", "/projects", "/projects/shop-api", "/projects/shop-api/pipeline", "/projects/shop-api/tests", "/projects/shop-api/variables",
  "/pipelines", "/activity", "/activity/approvals", "/runs/run_7", "/deployments", "/deployments/environments",
  "/deployments/applications", "/images", "/images/components", "/library", "/library/templates", "/playground", "/kit",
  "/settings", "/settings/runners", "/settings/access", "/settings/access/keys", "/settings/access/agents", "/settings/approvals",
  "/settings/rules", "/settings/audit", "/settings/variables", "/settings/words", "/config", "/config/wizard/connect-repository",
  "/traffic", "/changes", "/learn", "/learn/start-here", "/learn/concepts/launch-line", "/nothing-here",
];

const listed = new Set<string>(baseline.pages);
const seen = new Set<string>();

describe.each(["light", "dark"])("every page, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); installServer(); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });
  it.each(PAGES)("%s has no accessibility violations", async (path) => {
    history.replaceState(null, "", path);
    const { container } = render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    // Let the lazy page and its first fetches settle.
    await waitFor(() => { if (container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 8000 }).catch(() => {});
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    const fresh = found.filter((f) => { const k = `${path}|${f.rule}`; if (listed.has(k)) { seen.add(k); return false; } return true; });
    const text = fresh.map((f) => `${f.rule} (${f.impact}): ${f.help}\n    ${f.nodes.join("\n    ")}`).join("\n");
    expect(fresh, `${path} (${theme}) has accessibility violations:\n${text}`).toEqual([]);
  }, 20000);
});

// The baseline may only shrink: a line whose finding is gone must be deleted.
describe("the baseline", () => {
  afterAll(() => vi.unstubAllGlobals());
  it("lists nothing that no longer occurs", () => {
    const stale = [...listed].filter((k) => !seen.has(k));
    expect(stale, "these baseline lines no longer match a finding: delete them from src/a11y/baseline.json").toEqual([]);
  });
});
