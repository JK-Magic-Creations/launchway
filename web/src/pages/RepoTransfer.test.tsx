import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RepoTransfer } from "./RepoTransfer";
import type { HostedRepo } from "../forgeApi";

const repo: HostedRepo = { id: "rep_1", path: "acme/shop", home: "acme", name: "shop", visibility: "private", clone: {} };
afterEach(() => vi.unstubAllGlobals());

describe("transfer a repository", () => {
  it("says what moves and what the new owner gains, and moves it only when the address is typed", async () => {
    const calls: { method: string; url: string; body?: string }[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? "GET", url, body: init?.body as string | undefined });
      return new Response(JSON.stringify({ ...repo, path: "ops/shop", home: "ops" }), { status: 200 });
    }));
    history.replaceState(null, "", "/repositories/acme/shop/settings");
    render(<RepoTransfer repo={repo} owner="acme" />);
    const open = screen.getByRole("button", { name: "Transfer…" }) as HTMLButtonElement;
    expect(open.disabled).toBe(true);
    await userEvent.type(screen.getByLabelText("New owner"), "ops");
    await userEvent.click(open);
    const group = screen.getByRole("group", { name: "Confirm the transfer" });
    expect(within(group).getByText(/What moves with it/)).toBeTruthy();
    expect(within(group).getByText(/What ops gains/)).toBeTruthy();
    expect(within(group).getByText(/keeps working for Git over HTTPS and SSH/)).toBeTruthy();
    const go = within(group).getByRole("button", { name: "Transfer to ops" }) as HTMLButtonElement;
    expect(go.disabled).toBe(true);
    await userEvent.type(within(group).getByLabelText(/Type/), "acme/shop");
    await userEvent.click(go);
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0].method).toBe("PATCH");
    expect(JSON.parse(calls[0].body!)).toEqual({ owner: "ops" });
    await waitFor(() => expect(location.pathname).toBe("/repositories/ops/shop/settings"));
  });
});
