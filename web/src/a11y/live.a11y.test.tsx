import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import type { RunDetail, StepView } from "../api";
import { run, step } from "../test/fixtures";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";

// The run page while it is running, with estimates and a live log, and after it, scanned by axe-core in both
// themes. There is no baseline: nothing here may have a finding.
const now = Date.now();
const steps: StepView[] = [
  step({ id: "build", status: "succeeded", needs: [], started_at: now - 300_000, finished_at: now - 240_000, attempt: 1,
    estimate: { samples: 9, enough: true, typical_ms: 62_000, low_ms: 55_000, high_ms: 70_000, slow_ms: 80_000, basis: "same_kind", last_run: "run_6", last_run_number: 6 } }),
  step({ id: "test", status: "running", needs: ["build"], started_at: now - 72_000, attempt: 1,
    estimate: { samples: 9, enough: true, typical_ms: 120_000, low_ms: 100_000, high_ms: 150_000, slow_ms: 200_000, basis: "same_kind", last_run: "run_6", last_run_number: 6 } }),
  step({ id: "image", status: "running", needs: ["build"], started_at: now - 600_000, attempt: 1,
    estimate: { samples: 9, enough: true, typical_ms: 120_000, low_ms: 100_000, high_ms: 150_000, slow_ms: 200_000, basis: "all_runs", last_run: "run_6", last_run_number: 6, bimodal: { fast_ms: 120_000, slow_ms: 540_000, fast_share: 0.7 } } }),
  step({ id: "notes", status: "pending", needs: ["test"], estimate: { samples: 0, enough: false, basis: "all_runs" } }),
  step({ id: "approve", status: "waiting", kind: "approval", needs: ["test", "image"], estimate: { samples: 3, enough: false, person: true, basis: "all_runs" } }),
];
const detail = (status: "running" | "succeeded"): RunDetail => ({
  ...run({ id: "run_7", number: 7, status, message: "Fix the price rounding" }),
  source: { kind: "git", url: "https://git.example/acme/shop-api.git" },
  graph: { name: "shop-api", on: {}, steps: steps.map((s) => ({ id: s.id, needs: s.needs, image: "golang", run: "go test", retry: 0 })) } as unknown as RunDetail["graph"],
  levels: [["build"], ["test", "image"], ["notes", "approve"]], step_list: steps, reruns: [], not_understood: [],
  estimate: status === "running"
    ? { as_of: now, known: true, remaining_ms: 360_000, remaining_low_ms: 300_000, remaining_high_ms: 420_000, partial: true, unknown_steps: ["notes"], waiting_for_person: ["approve"], overrun: true, runs: 9 }
    : { as_of: now, known: true, took_ms: 483_000, usual_ms: 549_000, diff_percent: -12, runs: 9 },
} as RunDetail);
const lines = [1, 2, 3].map((i) => ({ seq: i, at: now - 40_000, stream: "stdout", text: `#${i} [build ${i}/9] RUN go build ./...  CACHED` }));

describe.each(["light", "dark"])("the live run page, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });
  it.each([["running", "image"], ["running", "test"], ["succeeded", "test"]] as const)("a %s run with %s selected has no violations", async (status, sel) => {
    installServer({ "/api/v1/runs/run_7": detail(status), [`/api/v1/runs/run_7/steps/${sel}/logs`]: { lines, attempt: 1 } });
    history.replaceState(null, "", `/runs/run_7?step=${sel}`);
    render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    await screen.findByText(/#1 \[build 1\/9\]/, {}, { timeout: 8000 });
    if (status === "running") await screen.findByText(/left/, {}, { timeout: 8000 });
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    expect(found, found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")).toEqual([]);
  }, 20000);
});
