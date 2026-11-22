import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProtectedTags } from "./ProtectedTags";
import type { HostedRepo } from "../forgeApi";

const repo: HostedRepo = { id: "r1", path: "acme/shop", home: "acme", name: "shop", visibility: "private", clone: {} };
const url = "/api/v1/repositories/acme/shop/protected-tags";

function serve(failWith?: string) {
  const calls: { method: string; body?: string }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (u: string, init?: RequestInit) => {
    expect(u).toBe(url);
    calls.push({ method: init?.method ?? "GET", body: init?.body as string | undefined });
    if (init?.method === "PUT") {
      if (failWith) return new Response(JSON.stringify({ error: failWith }), { status: 400 });
      return new Response(init.body as string, { status: 200 });
    }
    return new Response(JSON.stringify({ rules: [{ pattern: "v*", create_role: "admin", change_role: "", pipelines: [] }] }), { status: 200 });
  }));
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("protected tags", () => {
  it("shows the defaults and saves a changed and an added rule", async () => {
    const calls = serve();
    render(<ProtectedTags repo={repo} />);
    const form = await screen.findByRole("form", { name: "Protected tags" });
    expect((within(form).getByLabelText("Who may move or delete one") as HTMLSelectElement).value).toBe("");
    await userEvent.selectOptions(within(form).getByLabelText("Who may make a matching tag"), "operator");
    await userEvent.type(within(form).getByLabelText(/Flights that may make one/), "ship-*, web");
    await userEvent.type(within(form).getByLabelText("Protect tags matching"), "rc-*");
    await userEvent.click(within(form).getByRole("button", { name: "Add the pattern" }));
    await userEvent.click(within(form).getByRole("button", { name: "Save the protected tags" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "PUT")!.body!).rules).toEqual([
      { pattern: "v*", create_role: "operator", change_role: "", pipelines: ["ship-*", "web"] },
      { pattern: "rc-*", create_role: "admin", change_role: "", pipelines: [] },
    ]);
    expect(await screen.findByText("Saved.")).toBeTruthy();
  });
  it("removes a rule and says why a bad pattern was refused", async () => {
    const calls = serve('"v[*" is not a valid pattern');
    render(<ProtectedTags repo={repo} />);
    await userEvent.click(await screen.findByRole("button", { name: "Remove the rule for v*" }));
    expect(screen.getByText("No tag is protected.")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Save the protected tags" }));
    expect(await screen.findByText(/is not a valid pattern/)).toBeTruthy();
    expect(JSON.parse(calls.find((c) => c.method === "PUT")!.body!).rules).toEqual([]);
  });
});
