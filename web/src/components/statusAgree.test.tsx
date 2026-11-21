import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { RunDetail, StepView } from "../api";
import { RunRow, stepsAsStations } from "../pages/Activity";
import { run, step } from "../test/fixtures";
import { RunLine, stepStations } from "./RunLine";
import { whyWaiting, runSentence } from "./whyWaiting";

// The list of runs and the run's own page draw a step from the same mapping. A step that is queued, waiting
// for room, a runner or a person is never drawn as running; a run whose only active step is queued is holding.

const done = (id: string): StepView => step({ id, status: "succeeded", attempt: 1, exit_code: 0, error: "", needs: [] });
const cases: Record<string, { status?: "running" | "canceled"; steps: StepView[]; waiting?: string[]; spins: boolean; holding?: boolean }> = {
  "queued, waiting for a runner": { steps: [done("build"), step({ id: "test", status: "queued", needs: ["build"], exit_code: null, error: "" })], spins: false, holding: true },
  "queued, waiting for room": { steps: [done("build"), step({ id: "test", status: "queued", needs: ["build"], waiting_for: "The cluster is full", waiting_code: "cluster_full", exit_code: null, error: "" })], spins: false, holding: true },
  "waiting for approval": { steps: [done("build"), step({ id: "ship", status: "waiting", kind: "approval", needs: ["build"], exit_code: null, error: "" })], waiting: ["ship"], spins: false },
  "running": { steps: [done("build"), step({ id: "test", status: "running", needs: ["build"], started_at: 5, exit_code: null, error: "" })], spins: true },
  "retrying after a lost runner": { steps: [done("build"), step({ id: "test", status: "queued", attempt: 3, failure_cause: "infrastructure", retry: 3, needs: ["build"], exit_code: null, error: "" })], spins: false, holding: true },
  "re-run from a step": { steps: [done("build"), done("lint"), step({ id: "test", status: "queued", attempt: 2, needs: ["lint"], exit_code: null, error: "" }), step({ id: "ship", status: "pending", needs: ["test"], exit_code: null, error: "" })], spins: false, holding: true },
  "superseded": { status: "canceled", steps: [done("build"), step({ id: "test", status: "skipped", skip_kind: "superseded", needs: ["build"], exit_code: null, error: "" })], spins: false },
  "canceled": { status: "canceled", steps: [done("build"), step({ id: "test", status: "canceled", needs: ["build"], exit_code: null, error: "" })], spins: false },
};

describe.each(Object.entries(cases))("%s", (_name, c) => {
  const status = c.status ?? "running";
  const summary = run({ status, finished_at: 0, steps: c.steps.map((s) => ({ id: s.id, status: s.status, needs: s.needs })), waiting: c.waiting ?? [] });
  const detail = { ...summary, step_list: c.steps, graph: { name: "p", on: {}, steps: [] }, levels: [], reruns: [], not_understood: [] } as unknown as RunDetail;

  it("the list and the page agree on every step", () => {
    const list = stepsAsStations(summary).map((s) => [s.key, s.state]);
    const page = stepStations(detail, 1000).map((s) => [s.key, s.state]);
    expect(list).toEqual(page);
  });
  it("only a running step spins", () => {
    const { container: row } = render(<RunRow r={summary} now={1000} />);
    const { container: page } = render(<RunLine run={detail} selected={null} onSelect={() => {}} now={1000} />);
    for (const el of [row, page]) expect(!!el.querySelector(".spinner, .st.run")).toBe(c.spins);
  });
  it("a run whose only active step is queued says it is holding", () => {
    if (!c.holding) return;
    const { container } = render(<RunRow r={summary} now={1000} />);
    expect(container.textContent).toContain("Holding");
    expect(container.querySelector(".st.run")).toBeNull();
    expect(runSentence(detail, 1000)).toContain("holding");
  });
});

describe("the reason", () => {
  it("a retry after a lost runner says so", () => {
    const s = step({ id: "test", status: "queued", attempt: 3, failure_cause: "infrastructure", retry: 3 });
    const d = { ...run({ status: "running" }), step_list: [s] } as unknown as RunDetail;
    expect(whyWaiting(d, s, 1000)?.text).toBe("Retrying on another runner, attempt 3, because the last one stopped responding.");
  });
});
