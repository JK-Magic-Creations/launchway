import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MeContext } from "../../auth";
import { me } from "../../test/fixtures";
import { BranchesPage, TagsPage } from "./BranchesPage";

const calls: string[] = [];
const ref = (kind: string, name: string) => ({ name, kind, sha: "a".repeat(40), commit: "a".repeat(40), subject: "s", author: "Sam", committed_at: "2026-01-01T00:00:00Z", ahead: 1, behind: 0, default: name === "main" });
beforeEach(() => {
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const u = new URL(url, "http://x");
    calls.push(`${init?.method ?? "GET"} ${u.pathname}${u.search} ${init?.body ?? ""}`.trim());
    const j = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (u.pathname.endsWith("/branches") && (!init?.method || init.method === "GET")) return j({ items: [ref("branch", "main"), ref("branch", "feature/a")], total: 2, default_branch: "main" });
    if (u.pathname.endsWith("/tags") && (!init?.method || init.method === "GET")) return j({ items: [ref("tag", "v1")], total: 1 });
    if (u.pathname.endsWith("/branches/rename")) return j({ name: "feature/b", was: "feature/a", commit: "a".repeat(40), note: "" });
    if (u.pathname.endsWith("/tags") && init?.method === "POST") return j({ name: "v2", commit: "a".repeat(40) }, 201);
    if (u.pathname.endsWith("/tags") && init?.method === "DELETE") return j({ name: "v1", was: "a".repeat(40), undo: "x" });
    if (u.pathname === "/api/v1/repositories/acme/shop") return j({ id: "r", path: "acme/shop", name: "shop", visibility: "private", clone: {} });
    return j({ items: [] });
  }));
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
const operator = me({ admin: false, roles: { "acme/shop": "operator" } });
const viewer = me({ admin: false, roles: { "acme/shop": "viewer" } });
const as = (who: typeof operator, ui: React.ReactNode) => render(<MeContext.Provider value={who}><main>{ui}</main></MeContext.Provider>);

describe("making and deleting tags, renaming branches", () => {
  it("offers a viewer none of it", async () => {
    as(viewer, <TagsPage path="acme/shop" />);
    await screen.findByText("v1");
    expect(screen.queryByRole("form", { name: "Make a tag" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Delete/ })).toBeNull();
  });
  it("makes a tag at the default branch", async () => {
    as(operator, <TagsPage path="acme/shop" />);
    fireEvent.change(await screen.findByPlaceholderText("v1.0.0"), { target: { value: "v2" } });
    await waitFor(() => expect((screen.getByRole("button", { name: "Make tag" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "Make tag" }));
    await waitFor(() => expect(calls).toContain('POST /api/v1/repositories/acme/shop/tags {"name":"v2","from":"main"}'));
  });
  it("deletes a tag only after asking, and says how to bring it back", async () => {
    as(operator, <TagsPage path="acme/shop" />);
    await waitFor(() => screen.getByRole("button", { name: /Delete/ }));
    fireEvent.click(screen.getByRole("button", { name: /Delete/ }));
    expect(screen.getByText(/The commit stays; only the name goes/)).toBeTruthy();
    expect(calls.some((c) => c.startsWith("DELETE"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Delete tag" }));
    expect(await screen.findByText(/brings it back/)).toBeTruthy();
    expect(calls.some((c) => c.startsWith("DELETE /api/v1/repositories/acme/shop/tags?name=v1"))).toBe(true);
  });
  it("renames a branch, but not the default branch", async () => {
    as(operator, <BranchesPage path="acme/shop" />);
    const rename = await screen.findAllByText(/^Rename/, { selector: "button" });
    expect(rename).toHaveLength(1);
    fireEvent.click(rename[0]);
    fireEvent.change(screen.getByLabelText("New name"), { target: { value: "feature/b" } });
    fireEvent.click(screen.getByRole("button", { name: "Rename branch" }));
    await waitFor(() => expect(calls).toContain('POST /api/v1/repositories/acme/shop/branches/rename {"name":"feature/a","new_name":"feature/b"}'));
  });
});
