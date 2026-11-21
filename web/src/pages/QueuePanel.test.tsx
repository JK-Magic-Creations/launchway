import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const call = vi.fn();
vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), call: (...a: unknown[]) => call(...a) }));

import QueuePanel from "./QueuePanel";

describe("the queue on Activity", () => {
  it("lists held runs in order with why each waits, and offers only the actions the server says are available", async () => {
    call.mockResolvedValue([{ run: "run_9", number: 9, project: "shop", position: 1, code: "automation_limit",
      reason: "waiting for room: this project allows 2 runs started by automation accounts at once, and 2 are going (runs #1, #2)",
      actions: [{ name: "run_next", available: true }, { name: "hold", available: true }, { name: "release", available: false, reason: "the run is not held by a person" }] }]);
    render(<QueuePanel project="" />);
    expect(await screen.findByText(/allows 2 runs started by automation accounts at once/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run next" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Hold" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Release" })).toBeNull();
  });
  it("shows nothing when no run waits", async () => {
    call.mockResolvedValue([]);
    const { container } = render(<QueuePanel project="" />);
    await vi.waitFor(() => expect(call).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
