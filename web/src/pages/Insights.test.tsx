import { render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MeContext } from "../auth";
import { me } from "../test/fixtures";
import { resetTerms } from "../terms";
import type { InsightsAnswer } from "../insightsApi";

const get = vi.fn();
vi.mock("../insightsApi", async (orig) => ({ ...(await orig<typeof import("../insightsApi")>()), insightsApi: { get: (...a: unknown[]) => get(...a), csvUrl: () => "/api/v1/insights?format=csv" } }));
vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), api: { runs: () => Promise.resolve([]) } }));

import { InstallationInsightsPage } from "./Insights";

const DAY = 86400000;
const answer: InsightsAnswer = {
  scope: "installation", name: "",
  insights: {
    from: Date.UTC(2026, 10, 1), to: Date.UTC(2026, 10, 4), bucket_seconds: 86400, runs: 5, succeeded: 3, failed: 1, canceled: 1, success_rate: 0.75,
    duration_p50_s: 60, duration_p95_s: 300, queue_wait_p50_s: 4, queue_wait_p95_s: 30,
    series: [0, 1, 2].map((i) => ({ at: Date.UTC(2026, 10, 1) + i * DAY, runs: 2, succeeded: 1, failed: 1, success_rate: 0.5, duration_p50_s: 60, duration_p95_s: 300,
      queue_wait_p50_s: 4, queue_wait_p95_s: 30, tests_total: 10, tests_failed: 1, test_failure_rate: 0.1 })),
    slowest_steps: [{ step: "build", runs: 4, failures: 0, failure_rate: 0, p50_s: 50, p95_s: 200 }],
    flakiest_steps: [{ step: "test", runs: 4, failures: 2, failure_rate: 0.5, p50_s: 20, p95_s: 40 }],
    tests_total: 30, tests_failed: 3, tests_flaky: 1,
    delivery: { launches: 2, failures: 1, frequency_per_week: 0.7, lead_time_median_s: 3600, change_failure_rate: 0.5, time_to_restore_median_s: 7200, unrestored: 0,
      series: [{ at: Date.UTC(2026, 10, 1), launches: 2, failures: 1, lead_time_median_s: 3600, time_to_restore_median_s: 7200 }] },
  },
};

beforeEach(() => { history.pushState(null, "", "/activity/insights"); });
afterEach(() => { resetTerms(); get.mockReset(); });

describe("the Insights page", () => {
  it("gives every chart a focusable image with a summary and the same numbers as a table, and names each series by more than colour", async () => {
    get.mockResolvedValue(answer);
    render(<MeContext.Provider value={me({})}><InstallationInsightsPage /></MeContext.Provider>);
    await waitFor(() => screen.getByText("Run success rate"));
    const charts = document.querySelectorAll("figure.ichart");
    expect(charts.length).toBeGreaterThanOrEqual(6);
    for (const fig of Array.from(charts)) {
      const img = fig.querySelector("svg[role=img]") as SVGElement;
      expect(img.getAttribute("tabindex")).toBe("0");
      expect(img.getAttribute("aria-label")).toMatch(/table below/);
      expect(fig.querySelector("details table")).toBeTruthy(); // the same numbers as a table
      expect(within(fig as HTMLElement).getByRole("button", { name: "Copy as image" })).toBeTruthy();
      expect(fig.querySelectorAll(".ichart-legend li").length).toBeGreaterThan(0);
    }
    // The two duration lines differ by dash pattern and marker, not only colour.
    const dur = document.getElementById("ins-duration")!;
    const paths = Array.from(dur.querySelectorAll("svg[role=img] path"));
    expect(new Set(paths.map((p) => p.getAttribute("stroke-dasharray") ?? "solid")).size).toBe(2);
    expect(screen.getByRole("link", { name: "Download as CSV" }).getAttribute("href")).toContain("format=csv");
    expect(screen.getAllByText("Flakiest steps").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Change failure rate").length).toBeGreaterThan(0);
  });
  it("asks again with the chosen period", async () => {
    get.mockResolvedValue(answer);
    render(<MeContext.Provider value={me({})}><InstallationInsightsPage /></MeContext.Provider>);
    await waitFor(() => screen.getByText("Run success rate"));
    expect(get.mock.calls[0][0]).toMatchObject({ days: 30 });
  });
});
