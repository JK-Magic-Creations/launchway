import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

const call = vi.fn();
vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), call: (...a: unknown[]) => call(...a) }));

import { EventsPage } from "./Events";

const started = { id: "evt_1", project: "shop", at: 1794646800000, source: "push", verified: true, summary: "Push to main at 3f2a1c9",
  replay: { ref: "refs/heads/main", sha: "3f2a1c9" },
  decisions: [{ id: 1, at: 1, trigger: "push", decision: "started", reason: "started run #7", run_id: "run_7", run_number: 7 }] };
const refused = { id: "evt_2", project: "shop", at: 1794646700000, source: "webhook", source_name: "deploy", verified: false, summary: "Webhook call: deploy",
  decisions: [{ id: 2, at: 1, trigger: "webhook:deploy", decision: "refused", reason: "the signature did not match" }] };

describe("the events page", () => {
  it("shows each event with every decision and its reason, and offers replay only for verified events", async () => {
    call.mockResolvedValue([started, refused]);
    render(<EventsPage />);
    expect(await screen.findByText("Push to main at 3f2a1c9")).toBeInTheDocument();
    expect(screen.getByText(/started run #7/)).toBeInTheDocument();
    expect(screen.getByText(/the signature did not match/)).toBeInTheDocument();
    expect(screen.getByText("not verified")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /^Replay / })).toHaveLength(1);
  });

  it("filters by decision through the API and replays an event, saying what started", async () => {
    call.mockImplementation((method: string, path: string) => {
      if (method === "POST") return Promise.resolve({ run: { id: "run_8", number: 8, project: "shop" }, replay_of: "evt_1" });
      return Promise.resolve(path.includes("decision=refused") ? [refused] : [started, refused]);
    });
    render(<EventsPage />);
    await screen.findByText("Push to main at 3f2a1c9");
    await userEvent.click(screen.getByRole("button", { name: "Replay Push to main at 3f2a1c9" }));
    expect(await screen.findByText("Replayed: run #8 of shop started.")).toBeInTheDocument();
    expect(call).toHaveBeenCalledWith("POST", "/api/v1/events/evt_1/replay", {});
    await userEvent.selectOptions(screen.getByLabelText("Decision"), "refused");
    await userEvent.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() => expect(screen.queryByText("Push to main at 3f2a1c9")).toBeNull());
    expect(screen.getByText("Webhook call: deploy")).toBeInTheDocument();
  });

  it("says plainly when nothing matches, and shows an error instead of a blank page", async () => {
    call.mockResolvedValueOnce([]);
    const { unmount } = render(<EventsPage />);
    expect(await screen.findByText("No events match")).toBeInTheDocument();
    unmount();
    call.mockRejectedValueOnce(new Error("boom"));
    render(<EventsPage />);
    expect(await screen.findByText(/boom/)).toBeInTheDocument();
  });
});
