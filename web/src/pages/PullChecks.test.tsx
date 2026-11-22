import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";

afterEach(() => vi.unstubAllGlobals());

const base = "/api/v1/repositories/acme/shop";
const checks = {
  head: "a".repeat(40), target_branch: "main", policy_verdict: "warn", deployments: [{ environment: "preview", version: "v1", phase: "live" }],
  checks: [{
    run_id: "run_7", number: 7, project: "shop-api", pipeline: "app", status: "failed", tone: "bad", sentence: "Tests failed on the head commit.", detail: true,
    tests: { totals: { total: 10, passed: 8, failed: 2, errored: 0, skipped: 0 }, new_failures: ["Suite t1"], new_failure_count: 1, compared: true },
    coverage: { lines_percent: 80, change: -2 }, findings: { counts: { total: 2, critical: 0, high: 1, medium: 1, low: 0, unknown: 0 }, new: { total: 1, critical: 0, high: 1, medium: 0, low: 0, unknown: 0 }, new_total: 1 },
    policy_verdict: "warn",
  }],
  annotations: [{ kind: "finding", file: "price.go", line: 2, severity: "high", tool: "scanner", message: "Rounding drops cents here", run_id: "run_7", new: true }],
};

describe("results for the head commit on the pull request page", () => {
  it("shows the pipelines' results and marks the line a finding names, with a message that opens by keyboard", async () => {
    installServer({ [`${base}/pulls/1/checks`]: checks });
    history.replaceState(null, "", "/repositories/acme/shop/pulls/1");
    render(<App />);
    await screen.findByText("Tests failed on the head commit.", {}, { timeout: 8000 });
    expect(screen.getByText(/8 of 10 tests passed, 2 failed; 1 newly failing against main/)).toBeTruthy();
    expect(screen.getByText(/2 findings, 1 new in this change/)).toBeTruthy();
    expect(screen.getByText(/Deployed to preview: v1 \(live\)/)).toBeTruthy();
    fireEvent.click(await screen.findByRole("tab", { name: /Files changed/ }, { timeout: 8000 }));
    const mark = await screen.findByRole("button", { name: "Finding on line 2" }, { timeout: 8000 });
    expect(mark.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(mark);
    await waitFor(() => expect(screen.getByRole("note").textContent).toContain("scanner: Rounding drops cents here"));
    expect(mark.getAttribute("aria-expanded")).toBe("true");
  }, 20000);
});
