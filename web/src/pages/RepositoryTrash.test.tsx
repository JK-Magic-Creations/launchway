import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MeContext } from "../auth";
import { RepositoryTrashPage } from "./RepositoryTrash";
import type { TrashedRepo } from "../trashApi";

const row = (over: Partial<TrashedRepo>): TrashedRepo => ({
  id: "rep_1", path: "acme/shop", home: "acme", name: "shop", visibility: "private", clone: {},
  deleted_at: "2026-11-01T10:00:00Z", deleted_by: "dana", purge_at: "2026-12-01T10:00:00Z", ...over,
});

function serve(items: TrashedRepo[], restoreStatus = 200) {
  const calls: { method: string; url: string; body?: string }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (u: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (u === "/api/v1/repositories") return new Response(JSON.stringify({ items: [] }), { status: 200 });
    calls.push({ method, url: u, body: init?.body as string | undefined });
    if (u === "/api/v1/repository-trash") return new Response(JSON.stringify({ items }), { status: 200 });
    if (u.endsWith("/restore")) return restoreStatus === 200
      ? new Response(JSON.stringify({ ...items[0], path: "acme/shop-old", name: "shop-old" }), { status: 200 })
      : new Response(JSON.stringify({ error: "another repository has the address acme/shop now: restore this one under another name" }), { status: restoreStatus });
    return new Response(JSON.stringify({ path: "acme/shop", packs: 3, blobs_deleted: 3, blobs_kept: 0, bytes: 10 }), { status: 200 });
  }));
  return calls;
}

const me = (admin: boolean) => ({ admin }) as never;
const page = (admin: boolean) => render(<MeContext.Provider value={me(admin)}><RepositoryTrashPage /></MeContext.Provider>);

afterEach(() => vi.unstubAllGlobals());

describe("deleted repositories", () => {
  it("lists who deleted a repository and when it goes, and says so when there are none", async () => {
    serve([row({})]);
    page(false);
    expect(await screen.findByText(/by dana/)).toBeTruthy();
    expect(screen.getByText(/removed for good on/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove for good" })).toBeNull(); // not an administrator of everything
  });
  it("says nothing is in the trash", async () => {
    serve([]);
    page(false);
    expect(await screen.findByText("No deleted repositories")).toBeTruthy();
  });
  it("restores under another name when the address was taken, and says why it was refused first", async () => {
    const calls = serve([row({ name_taken: true })], 409);
    page(false);
    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));
    const form = screen.getByRole("form", { name: "Restore acme/shop" });
    expect((within(form).getByLabelText(/Name to restore it under/) as HTMLInputElement).value).toBe("shop-restored");
    await userEvent.click(within(form).getByRole("button", { name: "Restore" }));
    expect(await screen.findByText(/under another name/)).toBeTruthy();
    expect(JSON.parse(calls.find((c) => c.url.endsWith("/restore"))!.body!)).toEqual({ name: "shop-restored" });
  });
  it("removes for good only when the address is typed, and only for an administrator", async () => {
    const calls = serve([row({})]);
    page(true);
    await userEvent.click(await screen.findByRole("button", { name: "Remove for good" }));
    const group = screen.getByRole("group", { name: "Remove acme/shop for good" });
    const go = within(group).getByRole("button", { name: "Remove for good" }) as HTMLButtonElement;
    expect(go.disabled).toBe(true);
    await userEvent.type(within(group).getByLabelText(/Type/), "acme/shop");
    expect(go.disabled).toBe(false);
    await userEvent.click(go);
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "DELETE")!.body!)).toEqual({ confirm: "acme/shop" });
  });
});
