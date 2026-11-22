import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BadgeBox } from "./BadgeBox";

function serve(initial: { enabled: boolean; public_repository: boolean }) {
  let enabled = initial.enabled;
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_u: string, init?: RequestInit) => {
    if (init?.method === "PUT") { enabled = JSON.parse(String(init.body)).enabled; calls.push("PUT " + String(init.body)); }
    const shown = enabled || initial.public_repository;
    return new Response(JSON.stringify({ enabled, public_repository: initial.public_repository, shown, links: {}, markdown: { status: "![shop status](https://x/public/badges/shop/status.svg?branch=main)", release: "![shop release](https://x/public/badges/shop/release.svg)" } }), { status: 200 });
  }));
  return calls;
}
afterEach(() => vi.unstubAllGlobals());

describe("the badges of a Flight", () => {
  it("says they are off, offers the Markdown, and an administrator switches them on", async () => {
    const calls = serve({ enabled: false, public_repository: false });
    render(<BadgeBox project="shop" canSwitch />);
    expect(await screen.findByText(/These are off/)).toBeTruthy();
    expect((screen.getByLabelText("Status of the last Sortie on a branch") as HTMLInputElement).value).toContain("/public/badges/shop/status.svg");
    await userEvent.click(screen.getByRole("button", { name: "Show the badges to anyone" }));
    await waitFor(() => expect(screen.getByText(/switched on for this Flight/)).toBeTruthy());
    expect(calls).toEqual(['PUT {"enabled":true}']);
    expect(screen.getByRole("button", { name: "Hide the badges" })).toBeTruthy();
  });
  it("offers no switch to someone who may not, nor for a public repository", async () => {
    serve({ enabled: false, public_repository: false });
    const { unmount } = render(<BadgeBox project="shop" canSwitch={false} />);
    await screen.findByText(/These are off/);
    expect(screen.queryByRole("button", { name: /badges/ })).toBeNull();
    unmount();
    vi.unstubAllGlobals();
    serve({ enabled: false, public_repository: true });
    render(<BadgeBox project="shop" canSwitch />);
    expect(await screen.findByText(/the repository is public/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Show the badges|Hide the badges/ })).toBeNull();
    expect(screen.getAllByRole("button", { name: /Copy badge/ }).length).toBe(2);
  });
});
