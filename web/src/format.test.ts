import { describe, expect, it } from "vitest";
import { ago, duration, refLabel, shortDuration, starterLabel } from "./format";

describe("duration", () => {
  it.each([
    [-5, "0s"],
    [400, "0.4s"],
    [5_000, "5s"],
    [65_000, "1m 5s"],
    [3_700_000, "1h 1m"],
    [3 * 24 * 3600_000 + 2 * 3600_000, "3d 2h"],
  ])("writes %d ms as %s", (ms, want) => {
    expect(duration(ms)).toBe(want);
  });
});

describe("ago", () => {
  const now = 1_700_000_000_000;
  it("says nothing without a time", () => expect(ago(0, now)).toBe(""));
  it("says just now for the last seconds", () => expect(ago(now - 3_000, now)).toBe("just now"));
  it("counts minutes and hours", () => {
    expect(ago(now - 5 * 60_000, now)).toBe("5m ago");
    expect(ago(now - 3 * 3600_000, now)).toBe("3h ago");
  });
});

describe("labels", () => {
  it("trims a Go duration", () => {
    expect(shortDuration("1h0m0s")).toBe("1h");
    expect(shortDuration("1h30m0s")).toBe("1h30m");
  });
  it("names where a run came from", () => {
    expect(refLabel({ pull_request: 12, branch: "x" })).toBe("pull request #12");
    expect(refLabel({ tag: "v1" })).toBe("v1");
  });
  it("says who started a run", () => {
    const base = { author: "", actor: "ann", draft: false };
    expect(starterLabel({ ...base, event: "push", author: "bob" })).toBe("pushed by bob");
    expect(starterLabel({ ...base, event: "schedule" })).toBe("started by a schedule");
    expect(starterLabel({ ...base, event: "webhook", actor: "webhook:deploy" })).toBe("started by the webhook deploy");
  });
});
