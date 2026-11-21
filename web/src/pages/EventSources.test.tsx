import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

const call = vi.fn();
vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), call: (...a: unknown[]) => call(...a),
  api: { projects: () => Promise.resolve([{ name: "shop" }]) } }));

import EventSources from "./EventSources";

const source = { id: "evs_1", project: "shop", name: "deploy", url: "https://x/api/v1/projects/shop/sources/deploy", has_secret: true, enabled: true,
  mapping: { version: "body.release.version" }, filter: 'body.action == "released"', debounce_seconds: 0, batch_seconds: 0, max_body: 65536, rate_per_minute: 60 };

describe("event sources", () => {
  it("makes a source, shows its secret once and tries a body against it", async () => {
    call.mockImplementation((method: string, path: string) => {
      if (method === "GET") return Promise.resolve([source]);
      if (method === "POST" && path.endsWith("/test")) return Promise.resolve({ status: "ready", reason: "this body would be accepted", inputs: { version: "v9" } });
      return Promise.resolve({ source, secret: "lwes_abc" });
    });
    render(<EventSources />);
    expect(await screen.findByText("deploy")).toBeInTheDocument();
    expect(screen.getByText(/body.release.version/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Name"), "second");
    await userEvent.type(screen.getByLabelText(/Inputs, one per line/), "version=body.v");
    await userEvent.click(screen.getByRole("button", { name: "Make the source" }));
    expect(await screen.findByText("lwes_abc")).toBeInTheDocument();
    expect(call).toHaveBeenCalledWith("POST", "/api/v1/projects/shop/event-sources", expect.objectContaining({ name: "second", mapping: { version: "body.v" } }));
    await userEvent.click(screen.getByRole("button", { name: "I have saved it" }));
    expect(screen.queryByText("lwes_abc")).toBeNull();
    fireEvent.change(screen.getByLabelText("Try a body for deploy"), { target: { value: '{"release":{"version":"v9"}}' } });
    await userEvent.click(screen.getByRole("button", { name: "Try the body on deploy" }));
    expect(await screen.findByText(/this body would be accepted/)).toBeInTheDocument();
    expect(screen.getByText("version = v9")).toBeInTheDocument();
  });

  it("says when the body to try is not JSON, and when a project has no sources", async () => {
    call.mockResolvedValue([]);
    render(<EventSources />);
    expect(await screen.findByText("This project has no event sources.")).toBeInTheDocument();
    call.mockResolvedValue([source]);
    const again = render(<EventSources />);
    await waitFor(() => expect(again.getAllByText("deploy").length).toBeGreaterThan(0));
    await userEvent.type(again.getAllByLabelText("Try a body for deploy")[0], "not json");
    await userEvent.click(again.getAllByRole("button", { name: "Try the body on deploy" })[0]);
    expect(await screen.findByText(/must be JSON/)).toBeInTheDocument();
  });
});
