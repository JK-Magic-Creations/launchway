import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Debrief, FlakyList } from "../debriefApi";
import { scan } from "../test/axe";

// The Debrief and the flaky tests page, with every part filled (failures with
// a diff, a flaky test, a quarantined one, a grid, coverage of a change, a
// trend), scanned by axe-core in both themes, and what each button and link
// does.

const ref = (n: number, sha = "c0ffee1234567890") => ({ id: "run_" + n, number: n, ref: "refs/heads/main", branch: "main", sha, created_at: 1_763_600_000_000 });
const sample: Debrief = {
  version: 1, generated_at: 1_763_700_000_000, install: { name: "Launchway" }, project: "storefront", run: ref(42, "c0ffee1234567890"), result: "failed",
  summary: { total: 240, passed: 228, failed: 3, errored: 1, skipped: 8, flaky: 2, quarantined: 1, duration_ms: 184_000, pass_rate: 98.3 },
  change: { base: ref(41, "aaaa1111bbbb2222"), newly_failing: 2, newly_passing: 1, total_delta: 6, duration_delta_ms: 4200, slower: [{ key: "k1", suite: "cart", name: "TestCheckoutTotals", base_ms: 900, ms: 3100 }] },
  failures: [
    { key: "k1", suite: "cart", name: "TestCheckoutTotals", case_id: 11, file: "cart/checkout_test.go", line: 88, status: "failed", duration_ms: 3100, message: "got 1999, want 2099",
      details: "goroutine 7 [running]:\ncart.TestCheckoutTotals(0xc000)", output: "loading fixtures", diff: { expected: "2099", actual: "1999" }, new_here: true,
      first_failed_in: ref(42), failing_on_base: "no", flaky_score: 0 },
    { key: "k2", suite: "ui/login", name: "renders the form", case_id: 12, status: "failed", duration_ms: 800, variant: "firefox", message: "expected a but was b",
      diff: { expected: "Sign in\nEmail", actual: "Sign in\nUsername", lines: [{ op: " ", text: "Sign in" }, { op: "-", text: "Email" }, { op: "+", text: "Username" }] },
      failing_on_base: "yes", attachments: [{ name: "failure.png", kind: "image" }] },
    { key: "k3", suite: "api", name: "TestTimeout", case_id: 13, status: "errored", duration_ms: 30000, message: "deadline exceeded", failing_on_base: "unknown", flaky_score: 20 },
  ],
  flaky: [{ key: "k4", suite: "ui/search", name: "debounces input", score: 25, reason: "passed and failed on the same commit", this_run: "passed on retry", first_seen: 1_763_000_000_000, commits: 12, same_commit: 3 }],
  quarantined: [{ key: "k5", suite: "db", name: "TestSlowMigration", owner: "bo", expires: "2026-12-15", reason: "network in CI", status: "failed" }],
  slowest: [{ key: "k3", suite: "api", name: "TestTimeout", duration_ms: 30000, avg_ms: 400 }, { key: "k1", suite: "cart", name: "TestCheckoutTotals", duration_ms: 3100 }],
  suites: [{ name: "cart", depth: 0, total: 60, passed: 59, failed: 1, skipped: 0, duration_ms: 9000 }, { name: "cart/pricing", depth: 1, total: 20, passed: 20, failed: 0, skipped: 0, duration_ms: 2000 }],
  matrix: { columns: ["chromium", "firefox"], rows: [{ key: "k2", suite: "ui/login", name: "renders the form", cells: ["passed", "failed"] }] },
  coverage: { lines: { covered: 8200, total: 10000, percent: 82 }, branches: { covered: 300, total: 500, percent: 60 }, functions: { covered: 90, total: 100, percent: 90 }, base_lines: 81.5,
    change: { covered: 12, total: 20, percent: 60, advisory: "60% of the changed lines are covered; the advised floor is 80%.", files: [{ path: "cart/checkout.go", covered: 12, total: 20, percent: 60, missed: "40-47" }] },
    least: [{ path: "cart/legacy.go", covered: 5, total: 50, percent: 10, missed: "1-20, 30-60" }], files: 120 },
  trend: Array.from({ length: 14 }, (_, i) => ({ number: 29 + i, sha: "0000" + i, at: 1_763_000_000_000 + i * 86_400_000, total: 220 + i, pass_rate: i % 4 === 3 ? 97.5 : 100, duration_ms: 170_000 + i * 900, flaky: i % 3 })),
  notes: ["The report shows the first 3 of 4 failures."],
};
const flaky: FlakyList = { project: "storefront", window: 60, considered: 3, rules: "A test is flaky when the same commit both passed and failed it.", tests: [
  { key: "k4", suite: "ui/search", name: "debounces input", score: 25, reason: "passed and failed on the same commit", commits: 12, same_commit: 3, blips: 0, branches: ["main", "fix-cart"], first_seen: 1_763_000_000_000,
    last_run: 42, last_status: "passed", history: ["passed", "failed", "retried", "passed", "passed"], suggest: '- test: "ui/search::debounces input"\n  owner: your-name\n  expires: YYYY-MM-DD' }] };

vi.mock("../debriefApi", async (orig) => {
  const real = await orig<typeof import("../debriefApi")>();
  return { ...real, debriefApi: { debrief: vi.fn(async () => sample), flaky: vi.fn(async () => flaky) } };
});
vi.mock("../pages/PipelineChrome", () => ({ ProjectHead: ({ title }: { title: React.ReactNode }) => <h1>{title}</h1> }));

import { DebriefView } from "../pages/Debrief";
import { ProjectFlaky } from "../pages/ProjectFlaky";

describe.each(["light", "dark"])("the Debrief, %s theme", (theme) => {
  beforeEach(() => { document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); });

  it("has no accessibility findings with every part filled", async () => {
    const { container } = render(<main><DebriefView runId="run_42" version="1" openCase={() => {}} /></main>);
    await screen.findByText(/4 of 240 tests failed/);
    const found = await scan(container);
    expect(found, found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")).toEqual([]);
  }, 20000);

  it("the flaky tests page has no findings", async () => {
    const { container } = render(<main><ProjectFlaky name="storefront" /></main>);
    await screen.findByText("debounces input");
    const found = await scan(container);
    expect(found, found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")).toEqual([]);
  }, 20000);
});

describe("the Debrief does what it says", () => {
  it("every chart has its numbers in a table, and every export is a real link", async () => {
    const open = vi.fn();
    render(<main><DebriefView runId="run_42" version="1" openCase={open} /></main>);
    await screen.findByText(/4 of 240 tests failed; 2 of them pass on main/);
    expect(screen.getByRole("img", { name: /Results of 240 tests/ })).toBeTruthy();
    expect(screen.getByRole("table", { name: "Results" })).toBeTruthy();
    expect(screen.getByRole("img", { name: /Pass rate over the last 14 runs/ })).toBeTruthy();
    fireEvent.click(screen.getByText("Show the trend as a table"));
    expect(screen.getAllByRole("row").length).toBeGreaterThan(14);
    for (const f of ["html", "pdf", "json", "junit"]) {
      const links = screen.getAllByRole("link").filter((a) => a.getAttribute("href") === `/api/v1/runs/run_42/debrief/export?format=${f}`);
      expect(links.length, f).toBe(1);
      expect(links[0].hasAttribute("download")).toBe(true);
    }
    fireEvent.click(screen.getAllByRole("button", { name: "Open the full result" })[0]);
    expect(open).toHaveBeenCalledWith(11);
  });

  it("searches the failures and narrows to the new ones; says where each first failed and whether main fails it too", async () => {
    render(<main><DebriefView runId="run_42" version="1" openCase={() => {}} /></main>);
    await screen.findByText(/4 of 240/);
    expect(screen.getByText(/this change broke it/)).toBeTruthy();
    expect(screen.getByText(/so this change did not break it/)).toBeTruthy();
    expect(screen.getByText(/it did not run there/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Sortie #42" })).toBeTruthy();
    fireEvent.change(screen.getByRole("searchbox", { name: "Search the failures" }), { target: { value: "login" } });
    expect(screen.queryByText("TestCheckoutTotals", { selector: ".dfail-name" })).toBeNull();
    expect(screen.getByText("renders the form", { selector: ".dfail-name" })).toBeTruthy();
    fireEvent.change(screen.getByRole("searchbox", { name: "Search the failures" }), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: /New here/ }));
    await waitFor(() => expect(screen.queryByText("TestTimeout", { selector: ".dfail-name" })).toBeNull());
  });
});

describe("the flaky page", () => {
  it("shows the rules, the history in words, and a quarantine entry to copy", async () => {
    render(<main><ProjectFlaky name="storefront" /></main>);
    await screen.findByText("debounces input");
    expect(screen.getByText(/same commit both passed and failed/)).toBeTruthy();
    expect(screen.getByLabelText(/Latest results, oldest first: passed, failed, retried/)).toBeTruthy();
    fireEvent.click(screen.getByText("Suggested quarantine entry"));
    expect(screen.getByRole("button", { name: "Copy quarantine entry" })).toBeTruthy();
  });
});
