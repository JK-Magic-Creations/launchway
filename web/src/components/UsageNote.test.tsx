import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { UsageNote } from "./UsageNote";

describe("UsageNote", () => {
  it("shows what a step reserved and used, and how it was read on hover", () => {
    render(<UsageNote usage={{ measured: true, method: "cgroup2", memory_peak_bytes: 512 * 2 ** 20, reserved_memory_bytes: 2 ** 30, cpu_peak_milli: 1500 }} />);
    const el = screen.getByTestId("step-usage");
    expect(el).toHaveTextContent("reserved 1.0Gi, used 512Mi peak, 1.5 CPU peak");
    expect(el).toHaveAttribute("title", expect.stringContaining("cgroup2"));
  });
  it("says so when a step was not measured, with the reason on hover", () => {
    render(<UsageNote usage={{ measured: false, note: "the host runs the step itself" }} />);
    const el = screen.getByTestId("step-usage");
    expect(el).toHaveTextContent("use not measured");
    expect(el).toHaveAttribute("title", "the host runs the step itself");
  });
});
