import { describe, expect, it } from "vitest";
import { stepUsageText } from "./usageText";

describe("stepUsageText", () => {
  it("says what was reserved and used", () => {
    expect(stepUsageText({ measured: true, memory_peak_bytes: 1181116006, cpu_peak_milli: 1900, reserved_memory_bytes: 4 * 2 ** 30 }))
      .toBe("reserved 4.0Gi, used 1.1Gi peak, 1.9 CPU peak");
  });
  it("says so when a step was not measured", () => {
    expect(stepUsageText({ measured: false, note: "no cgroup" })).toBe("use not measured");
  });
});
