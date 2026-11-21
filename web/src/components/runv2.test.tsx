import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { DeploymentView, RunDetail } from "../api";
import { run, step } from "../test/fixtures";
import { FailurePanel } from "./FailurePanel";
import { RunHeader } from "./RunHeader";
import { RunLine, stepStations } from "./RunLine";
import { runSentence, whyWaiting } from "./whyWaiting";
import { explainFailure } from "../guided/derive";

const NOW = 100_000;
const detail = (steps: ReturnType<typeof step>[], over: Partial<RunDetail> = {}): RunDetail => ({
  ...run({ status: "running", id: "run_1", number: 97 }), source: { kind: "git" }, graph: { name: "p", on: {}, steps: [] } as never, levels: [], step_list: steps, reruns: [], not_understood: [], ...over,
} as RunDetail);
const dep = (over: Partial<DeploymentView> = {}): DeploymentView => ({
  id: "d1", run_id: "run_1", run_number: 97, project: "shop-api", step: "ship", environment: "prod", service: "api", target: "home", application: "shop-api-prod", mode: "argocd",
  version: "1.4.0", changed: true, phase: "unobserved", detail: "", opened_at: 1, holding: false, ...over,
});
const s = (id: string, over: Partial<ReturnType<typeof step>> = {}) => step({ id, status: "pending", exit_code: null, error: "", excerpt: "", ...over });

describe("every wait says why", () => {
  it("a step that waits for others names them", () => {
    const r = detail([s("lint", { status: "running", position: 0 }), s("test", { needs: ["lint"], position: 1 })]);
    expect(whyWaiting(r, r.step_list[1], NOW)?.text).toBe("Waiting for lint to finish first.");
  });
  it("a step queued for a runner says so, using the server's hint and how long", () => {
    const r = detail([s("build", { status: "queued", queued_at: NOW - 65_000 })], { pool_hint: "No pool serves shop-api." });
    expect(whyWaiting(r, r.step_list[0], NOW)).toMatchObject({ kind: "runner", text: "No pool serves shop-api. It has waited 1m 5s." });
    const plain = detail([s("build", { status: "queued" })]);
    expect(whyWaiting(plain, plain.step_list[0], NOW)?.text).toBe("Waiting for a runner to take it.");
  });
  it("a retry explains its own delay", () => {
    const r = detail([s("flaky", { status: "queued", not_before: NOW + 30_000, attempt: 2, retry: 3, failure_cause: "timeout" })]);
    expect(whyWaiting(r, r.step_list[0], NOW)?.text).toContain("Trying again in 30s (attempt 2 of 4) after timeout");
  });
  it("an approval says what it asks and by when", () => {
    const r = detail([s("gate", { status: "waiting", kind: "approval", message: "Release 3.2?", deadline: 5 })]);
    const w = whyWaiting(r, r.step_list[0], NOW)!;
    expect(w.kind).toBe("person");
    expect(w.text).toContain("Release 3.2?");
    expect(w.text).toContain("Approve or reject (Go or No-go) before");
  });
  it("a deploy waiting for the lock says why a lock exists", () => {
    const r = detail([s("ship", { status: "waiting", kind: "deploy", wait_kind: "lock", environment: "prod" })], { deployments: [dep()] });
    expect(whyWaiting(r, r.step_list[0], NOW)?.text).toContain("prod's lock, so two deployments never run at once");
  });
  it("a waiting deploy uses the deployment's own explanation word for word", () => {
    const r = detail([s("ship", { status: "waiting", kind: "deploy", wait_kind: "observe", environment: "prod" })], { deployments: [dep({ phase: "rollout_progressing", detail: "2 of 4 new copies are ready" })] });
    expect(whyWaiting(r, r.step_list[0], NOW)).toMatchObject({ kind: "engine", text: "2 of 4 new copies are ready" });
  });
  it("a deploy nobody observes says what to check, never a bare waiting", () => {
    const r = detail([s("ship", { status: "waiting", kind: "deploy", wait_kind: "observe", environment: "prod" })], { deployments: [dep()] });
    const t = whyWaiting(r, r.step_list[0], NOW)!.text;
    expect(t).toContain("No deploy runner has reported yet");
    expect(t).toContain("home");
  });
  it("gives nothing for a step that runs or ended", () => {
    const r = detail([s("a", { status: "running" }), s("b", { status: "succeeded" })]);
    expect(whyWaiting(r, r.step_list[0], NOW)).toBeNull();
    expect(whyWaiting(r, r.step_list[1], NOW)).toBeNull();
  });
  it("never returns a bare 'Waiting' for any waiting state", () => {
    for (const status of ["pending", "queued", "leased", "waiting"] as const) {
      const r = detail([s("x", { status, kind: status === "waiting" ? "deploy" : undefined })]);
      expect((whyWaiting(r, r.step_list[0], NOW)?.text ?? "").length, status).toBeGreaterThan(20);
    }
  });
});

describe("the run's sentence", () => {
  it("says where a failed run stopped, and where a running one is, with why it waits", () => {
    expect(runSentence(detail([s("test", { status: "failed", exit_code: 1 })], { status: "failed" }), NOW)).toBe("Run #97 stopped at test.");
    const r = detail([s("ship", { status: "waiting", kind: "deploy", wait_kind: "lock", environment: "prod" })]);
    expect(runSentence(r, NOW)).toMatch(/^Run #97 is waiting\. ship: Waiting for prod's lock/);
    expect(runSentence(detail([s("a", { status: "succeeded" })], { status: "succeeded" }), NOW)).toBe("Run #97 finished. Everything passed.");
  });
});

describe("the living line", () => {
  const r = detail([s("lint", { status: "succeeded", position: 0, started_at: 1000, finished_at: 3000 }), s("test", { status: "failed", position: 1, exit_code: 2, started_at: 3000, finished_at: 4000 }),
    s("ship", { status: "pending", position: 2, needs: ["test"] })]);
  it("has a station for each real step, saying how long or why", () => {
    const st = stepStations(r, NOW);
    expect(st.map((x) => [x.key, x.state])).toEqual([["lint", "ok"], ["test", "bad"], ["ship", "idle"]]);
    expect(st[0].detail).toBe("2s");
    expect(st[1].detail).toBe("exit code 2");
    expect(st[2].detail).toBe("After test");
  });
  it("opens the chosen step", async () => {
    const pick = vi.fn();
    render(<RunLine run={r} selected="test" onSelect={pick} now={NOW} />);
    await userEvent.click(screen.getByRole("button", { name: /^lint/ }));
    expect(pick).toHaveBeenCalledWith("lint");
    expect(screen.getByRole("button", { name: /^test/ })).toHaveAttribute("aria-pressed", "true");
  });
});

describe("the run header and its density", () => {
  const base = (density: "summary" | "everything") => (
    <RunHeader run={detail([s("test", { status: "failed", exit_code: 1 })], { status: "failed", started_at: 1000, finished_at: 5000, sha: "a41f9c2aaaa", branch: "main" })} now={NOW} done live density={density}
      onDensity={() => {}} select={() => {}} setTab={() => {}} actions={null} />
  );
  it("Summary shows the sentence and the band, and says what it hides", () => {
    render(base("summary"));
    expect(screen.getByText("Run #97 stopped at test.")).toBeInTheDocument();
    expect(screen.getByText("Took")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Run details" })).not.toBeInTheDocument();
    expect(screen.getByText(/Choose Everything/)).toBeInTheDocument();
  });
  it("Everything adds the run's every field", () => {
    render(base("everything"));
    const d = screen.getByRole("region", { name: "Run details" });
    expect(d).toHaveTextContent("a41f9c2a");
    expect(d).toHaveTextContent("main");
    expect(d).toHaveTextContent("took");
  });
  it("the toggle says which is pressed", () => {
    render(base("summary"));
    expect(screen.getByRole("button", { name: "Summary" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Everything" })).toHaveAttribute("aria-pressed", "false");
  });
});

describe("what went wrong", () => {
  it("keeps the headline, the advice, the last lines and the page's actions", () => {
    const f = explainFailure([s("test", { status: "failed", exit_code: 1, excerpt: "FAIL TestPrice" })])!;
    render(<FailurePanel failure={f} actions={<button type="button">Run again</button>} />);
    expect(screen.getByRole("heading", { name: "What went wrong" })).toBeInTheDocument();
    expect(screen.getByText(/test failed: its command ended with exit code 1/)).toBeInTheDocument();
    expect(screen.getByText("FAIL TestPrice")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run again" })).toBeInTheDocument();
  });
});
