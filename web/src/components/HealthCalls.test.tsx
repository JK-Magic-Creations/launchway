import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { VerificationView } from "../api";
import { HealthCalls } from "./HealthCalls";

const view = {
  intervals: [{
    index: 0, start: 0, end: 30000,
    metrics: [{
      name: "healthy", note: "1 of 3 requests to /healthz returned 503",
      attempts: [
        { t: 1000, ok: true, status: 200, latency_ms: 10 },
        { t: 11000, ok: false, status: 503, latency_ms: 12, error: "returned 503" },
        { t: 21000, ok: true, status: 200, latency_ms: 14 },
      ],
    }],
  }],
} as unknown as VerificationView;

describe("the calls of a health check", () => {
  it("says how many answered, the sentence, and lists the failed calls", () => {
    render(<HealthCalls v={view} name="healthy" />);
    expect(screen.getByText(/3 calls made by the deploy Station: 2 answered as expected, 1 did not/)).toBeInTheDocument();
    expect(screen.getByText("Interval 1: 1 of 3 requests to /healthz returned 503")).toBeInTheDocument();
    expect(screen.getByText("returned 503")).toBeInTheDocument();
  });
  it("shows nothing for a metric read from a source", () => {
    const { container } = render(<HealthCalls v={view} name="other" />);
    expect(container).toBeEmptyDOMElement();
  });
});
