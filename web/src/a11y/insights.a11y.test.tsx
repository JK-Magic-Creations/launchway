import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { me } from "../test/fixtures";
import { scan } from "../test/axe";

// The Insights pages (Flight, Squadron, installation), drawn whole, in both themes, scanned by axe-core with no baseline.
const DAY = 86400000, T = Date.UTC(2026, 10, 1);
const point = (i: number) => ({ at: T + i * DAY, runs: 3, succeeded: 2, failed: 1, success_rate: 0.67, duration_p50_s: 62, duration_p95_s: 310, queue_wait_p50_s: 3, queue_wait_p95_s: 20, tests_total: 40, tests_failed: 2, test_failure_rate: 0.05 });
const insights = {
  scope: "installation", name: "",
  insights: {
    from: T, to: T + 5 * DAY, bucket_seconds: 86400, runs: 15, succeeded: 10, failed: 5, canceled: 0, success_rate: 0.67, duration_p50_s: 62, duration_p95_s: 310, queue_wait_p50_s: 3, queue_wait_p95_s: 20,
    series: [0, 1, 2, 3, 4].map(point),
    slowest_steps: [{ step: "build", runs: 12, failures: 1, failure_rate: 0.08, p50_s: 50, p95_s: 200 }],
    flakiest_steps: [{ step: "test", runs: 12, failures: 4, failure_rate: 0.33, p50_s: 20, p95_s: 40 }],
    tests_total: 200, tests_failed: 10, tests_flaky: 3,
    delivery: { launches: 4, failures: 1, frequency_per_week: 1.4, lead_time_median_s: 5400, change_failure_rate: 0.25, time_to_restore_median_s: 3600, unrestored: 0,
      series: [0, 1].map((i) => ({ at: T + i * DAY, launches: 2, failures: i, lead_time_median_s: 5400, time_to_restore_median_s: i ? 3600 : 0 })) },
  },
};

describe.each(["light", "dark"])("Insights, %s theme", (theme) => {
  beforeEach(() => {
    HTMLCanvasElement.prototype.getContext = (() => null) as never;
    document.documentElement.setAttribute("data-theme", theme);
    installServer({ "/api/v1/insights": insights, "/api/v1/units/platform": { name: "platform", display_name: "platform", flights: [], needs: [], may_manage: false, why: "" } }, me({ roles: { "shop-api": "admin" } }));
  });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  it.each([["the installation", "/activity/insights"], ["a Flight", "/projects/shop-api/insights"], ["a Squadron", "/units/platform/insights"]])("%s has no accessibility violations", async (what, path) => {
    history.replaceState(null, "", path);
    const { container } = render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    await screen.findByText("Run success rate", {}, { timeout: 8000 });
    await waitFor(() => { if (container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 8000 }).catch(() => {});
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  }, 20000);
});
