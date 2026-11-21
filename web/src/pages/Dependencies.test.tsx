import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DependenciesPage, UpdatesPage } from "./Dependencies";
import { scan } from "../test/axe";
import type { DependencyReport } from "../depsApi";

const dep = (name: string, eco: string, current: string, file: string, line = 3) => ({ ecosystem: eco, name, current, direct: true, file, line, group: "prod" });
const report: DependencyReport = {
  repository: "acme/shop", branch: "main", commit: "0123456789abcdef0123456789abcdef01234567", files: ["go.mod", "web/package.json", "Dockerfile"],
  coming: ["Maven and Gradle", "Cargo"], ecosystems_available: ["go", "npm", "python", "container", "workflow"],
  policy: { enabled: true, security: true, routine: true, cooldown_days: 3, ecosystems: { go: { enabled: true }, npm: { enabled: true } },
    schedule: { interval: "weekly", day: "monday", hour: 6, timezone: "UTC" }, limits: { max_open: 3, max_per_run: 2 }, auto_merge: { enabled: false }, locked: { auto_merge: "instance" } },
  updates: [
    { dep: dep("example.com/lib", "go", "v1.4.0", "go.mod"), status: "security", reason: "v1.4.0 is affected by GHSA-1; v1.4.2 fixes it.", latest: "v1.4.2", target: "v1.4.2", kind: "patch", security: true, published: new Date(Date.now() - 5 * 86_400_000).toISOString(),
      advisories: [{ id: "GHSA-1", severity: "critical", score: 9.8, fixed_in: "1.4.2", summary: "Path traversal" }] },
    { dep: dep("tinyfmt", "npm", "1.0.0", "web/package.json"), status: "update", latest: "2.0.0", target: "1.1.0", kind: "minor", major: { target: "2.0.0" }, published: new Date(Date.now() - 40 * 86_400_000).toISOString() },
    { dep: dep("left-pad", "npm", "1.3.0", "web/package.json", 4), status: "ignored", reason: "Ignored by the update settings: pinned on purpose", latest: "1.3.1" },
    { dep: { ...dep("loose-envify", "npm", "1.4.0", "web/package-lock.json"), direct: false }, status: "transitive" },
  ],
  decisions: [
    { key: "go-security-example-com-lib", title: "Fix GHSA-1 by updating example.com/lib to v1.4.2", ecosystem: "go", directory: "", security: true, action: "keep", why: "The open pull request already proposes exactly this.", number: 7,
      updates: [{ dep: dep("example.com/lib", "go", "v1.4.0", "go.mod"), status: "security", target: "v1.4.2" }] },
    { key: "npm-minor-and-patch-web", title: "Update npm dependencies", ecosystem: "npm", directory: "web", security: false, action: "queued", why: "This run already opened 2 pull requests (limit 2); the rest wait for the next run.",
      updates: [{ dep: dep("tinyfmt", "npm", "1.0.0", "web/package.json"), status: "update", target: "1.1.0" }] },
  ],
  notes: [{ file: "web/yarn.lock", message: "yarn.lock found: detected, but updates for this package manager are not available yet" }],
};

function serve() {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(report), { status: 200, headers: { "Content-Type": "application/json" } })));
}

describe.each(["light", "dark"])("dependency pages, %s theme", (theme) => {
  beforeEach(() => { document.documentElement.setAttribute("data-theme", theme); serve(); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  it("shows the dependencies as a table with the vulnerability, the age and the open pull request", async () => {
    render(<main><DependenciesPage path="acme/shop" /></main>);
    const table = await screen.findByRole("table", { name: /Dependencies of acme\/shop/ });
    const lib = within(table).getByRole("row", { name: /example.com\/lib/ });
    expect(within(lib).getByText(/critical/)).toBeTruthy();
    expect(within(lib).getByText(/fixed in 1.4.2/)).toBeTruthy();
    expect(within(lib).getByRole("link", { name: "#7" }).getAttribute("href")).toBe("/repositories/acme/shop/pulls/7");
    expect(within(lib).getByText("5 days")).toBeTruthy();
    expect(screen.getByText(/Ignored by the update settings: pinned on purpose/)).toBeTruthy();
    expect(screen.getByText(/Coming, not available yet: Maven and Gradle, Cargo/)).toBeTruthy();
    expect(screen.getByText(/yarn.lock found/)).toBeTruthy();
    const found = await scan(document.body, { page: true });
    expect(found.map((f) => f.rule + ": " + f.help)).toEqual([]);
  });

  it("hides what other packages pull in until asked, and filters by name, ecosystem and vulnerability", async () => {
    render(<main><DependenciesPage path="acme/shop" /></main>);
    await screen.findByRole("table");
    expect(screen.queryByText("loose-envify")).toBeNull();
    fireEvent.click(screen.getByLabelText(/Only what the repository asks for/));
    expect(await screen.findByText("loose-envify")).toBeTruthy();
    fireEvent.click(screen.getByLabelText(/Only those with a known vulnerability/));
    await waitFor(() => expect(screen.queryByText("tinyfmt")).toBeNull());
    expect(screen.getByText("example.com/lib")).toBeTruthy();
  });

  it("shows the settings that apply, says what is locked, and previews only when asked", async () => {
    render(<main><UpdatesPage path="acme/shop" /></main>);
    expect(await screen.findByText(/auto_merge \(by the instance level\)/)).toBeTruthy();
    expect(screen.getByText(/every monday|weekly on monday at 06:00 UTC/i)).toBeTruthy();
    expect(screen.getByText(/never automatic/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Save/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Preview what would be opened" }));
    const t = await screen.findByRole("table", { name: "What the updater would do" });
    expect(within(t).getByText("Waiting")).toBeTruthy();
    expect(within(t).getByText(/limit 2/)).toBeTruthy();
    const found = await scan(document.body, { page: true });
    expect(found.map((f) => f.rule + ": " + f.help)).toEqual([]);
  });
});
