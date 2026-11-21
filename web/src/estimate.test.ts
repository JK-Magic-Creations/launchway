import { describe, expect, it } from "vitest";
import type { RunEstimate, StepEstimate } from "./api";
import { approx, runTiming, stepTiming } from "./estimate";

const est = (o: Partial<StepEstimate> = {}): StepEstimate => ({ samples: 8, enough: true, typical_ms: 120_000, low_ms: 100_000, high_ms: 150_000, slow_ms: 200_000, basis: "same_kind", ...o });
const running = (e: StepEstimate | undefined, startedAgo: number, now = 1_000_000) =>
  ({ status: "running" as const, started_at: now - startedAgo, finished_at: 0, estimate: e });

describe("approx", () => {
  it("rounds and never claims precision", () => {
    expect(approx(12_400)).toBe("12s");
    expect(approx(118_000)).toBe("2 min");
    expect(approx(6 * 60_000 + 20_000)).toBe("6 min");
    expect(approx(75 * 60_000)).toBe("75 min");
    expect(approx(2 * 3600_000 + 12 * 60_000)).toBe("2 h 10 min");
  });
});

describe("stepTiming", () => {
  const now = 1_000_000;
  it("says how far a running step is against what is usual", () => {
    const t = stepTiming(running(est(), 72_000), now);
    expect(t.kind).toBe("normal");
    expect(t.text).toBe("1m 12s of about 2 min");
    expect(t.fraction).toBeCloseTo(0.6, 1);
  });
  it("turns calm and plain past the 90th percentile", () => {
    const t = stepTiming(running(est(), 300_000), now);
    expect(t.kind).toBe("slow");
    expect(t.text).toBe("taking longer than usual (usually ~2 min, now 5 min)");
    expect(t.fraction).toBe(1);
  });
  it("is not slow between typical and the 90th percentile", () => {
    expect(stepTiming(running(est(), 180_000), now).kind).toBe("normal");
  });
  it("mentions the slower case of a history with two groups", () => {
    const t = stepTiming(running(est({ bimodal: { fast_ms: 120_000, slow_ms: 540_000, fast_share: 0.75 } }), 60_000), now);
    expect(t.text).toBe("1m 0s of about 2 min (9 min when the faster case does not apply)");
  });
  it("says there is no estimate for a new step and for one with too little history", () => {
    expect(stepTiming(running(est({ enough: false, samples: 0 }), 5000), now).text).toBe("No estimate yet: this step has not run before.");
    expect(stepTiming(running(est({ enough: false, samples: 2 }), 5000), now).text).toBe("No estimate yet: 2 earlier runs, three are needed.");
  });
  it("says nothing for a step that waits for a person, or has no estimate field", () => {
    expect(stepTiming(running(est({ person: true }), 5000), now).kind).toBe("none");
    expect(stepTiming(running(undefined, 5000), now).kind).toBe("none");
  });
  it("compares a finished step with its usual time", () => {
    const t = stepTiming({ status: "succeeded", started_at: 1000, finished_at: 61_000, estimate: est() }, now);
    expect(t.kind).toBe("done");
    expect(t.text).toBe("took 1m 0s, usually about 2 min");
  });
});

describe("runTiming", () => {
  const base: RunEstimate = { as_of: 1_000_000, known: true, remaining_ms: 360_000, runs: 12 };
  it("counts down from when the server worked it out", () => {
    expect(runTiming(base, "running", 1_000_000).text).toBe("about 6 min left");
    expect(runTiming(base, "running", 1_000_000 + 120_000).text).toBe("about 4 min left");
  });
  it("says why there is no estimate", () => {
    expect(runTiming({ as_of: 1, known: false, reason: "no_history", runs: 0 }, "running", 2).text).toContain("have not run before");
    expect(runTiming({ as_of: 1, known: false, reason: "too_few", runs: 2 }, "running", 2).text).toContain("three earlier runs");
  });
  it("leaves out the wait for a person and says so", () => {
    const t = runTiming({ ...base, waiting_for_person: ["approve"] }, "running", 1_000_000);
    expect(t.text).toMatch(/^about 6 min left, after /);
    expect(t.detail).toContain("approve");
    expect(t.detail).toContain("not counted");
  });
  it("says at least, when a step has no history", () => {
    const t = runTiming({ ...base, partial: true, unknown_steps: ["notes"] }, "running", 1_000_000);
    expect(t.detail).toContain("At least that: notes has no history yet");
  });
  it("says when it is running past its time", () => {
    expect(runTiming({ ...base, remaining_ms: 5000, overrun: true }, "running", 1_000_000 + 60_000).text).toBe("taking longer than usual");
  });
  it("compares a finished run with the usual", () => {
    const done: RunEstimate = { as_of: 1, known: true, took_ms: 483_000, usual_ms: 549_000, diff_percent: -12, runs: 9 };
    expect(runTiming(done, "succeeded", 2).text).toBe("took 8m 3s, 12% faster than usual");
    expect(runTiming({ ...done, diff_percent: 3 }, "succeeded", 2).text).toBe("took 8m 3s, about as long as usual");
    expect(runTiming(done, "failed", 2).text).toBe("");
  });
});
