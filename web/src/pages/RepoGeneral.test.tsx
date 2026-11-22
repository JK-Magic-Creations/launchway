import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RepoGeneral } from "./RepoGeneral";
import type { HostedRepo } from "../forgeApi";

const repo: HostedRepo = { id: "rep_1", path: "acme/shop", home: "acme", name: "shop", default_branch: "main", visibility: "private", clone: {} };
const calls: string[] = [];
beforeEach(() => {
  calls.length = 0;
  history.replaceState(null, "", "/repositories/acme/shop/settings");
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const u = new URL(url, "http://x");
    calls.push(`${init?.method ?? "GET"} ${u.pathname} ${init?.body ?? ""}`);
    const j = (v: unknown, status = 200) => new Response(status === 204 ? null : JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (u.pathname.endsWith("/branches") && (!init?.method || init.method === "GET")) return j({ items: [{ name: "main" }, { name: "trunk" }], total: 2, default_branch: "main" });
    if (init?.method === "DELETE") return j(null, 204);
    return j({ ...repo, ...(JSON.parse(String(init?.body ?? "{}"))), path: "acme/" + (JSON.parse(String(init?.body ?? "{}")).name ?? "shop") });
  }));
});
afterEach(() => vi.unstubAllGlobals());

describe("repository general settings", () => {
  it("tells a person without the role who may change them, and offers nothing that would be refused", () => {
    render(<RepoGeneral repo={repo} admin={false} onChanged={() => {}} />);
    expect(screen.getByText(/Only an administrator of this repository can rename it/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Delete/ })).toBeNull();
  });
  it("renames, says what stops working, and opens the new address", async () => {
    const changed = vi.fn();
    render(<RepoGeneral repo={repo} admin onChanged={changed} />);
    expect(screen.getByText(/do not redirect/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "store" } });
    fireEvent.click(screen.getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(changed).toHaveBeenCalled());
    expect(calls).toContain('PATCH /api/v1/repositories/acme/shop {"name":"store"}');
    expect(location.pathname).toBe("/repositories/acme/store/settings");
  });
  it("changes visibility and the default branch", async () => {
    render(<RepoGeneral repo={repo} admin onChanged={() => {}} />);
    fireEvent.change(screen.getByLabelText("Who may see it"), { target: { value: "internal" } });
    fireEvent.click(screen.getByRole("button", { name: "Change visibility" }));
    await waitFor(() => expect(calls.some((c) => c.includes('{"visibility":"internal"}'))).toBe(true));
    await screen.findByRole("option", { name: "trunk" });
    fireEvent.change(screen.getByLabelText("Default branch"), { target: { value: "trunk" } });
    fireEvent.click(screen.getByRole("button", { name: "Change default branch" }));
    await waitFor(() => expect(calls.some((c) => c.includes('{"default_branch":"trunk"}'))).toBe(true));
  });
  it("archives and deletes only after the repository's name is typed, and says what is lost", async () => {
    render(<RepoGeneral repo={repo} admin onChanged={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Archive this repository" }));
    const go = screen.getByRole("button", { name: "Archive" });
    expect((go as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/Type acme\/shop to confirm/), { target: { value: "acme/shop" } });
    fireEvent.click(go);
    await waitFor(() => expect(calls.some((c) => c.includes('{"archived":true}'))).toBe(true));

    fireEvent.click(screen.getByRole("button", { name: "Delete this repository" }));
    expect(screen.getByText(/clones, pushes, pull requests and webhooks of it stop working at once/)).toBeTruthy();
    const del = screen.getByRole("button", { name: "Delete" });
    expect((del as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/Type acme\/shop to confirm/), { target: { value: "acme/shop" } });
    fireEvent.click(del);
    await waitFor(() => expect(calls).toContain("DELETE /api/v1/repositories/acme/shop "));
    expect(location.pathname).toBe("/repositories");
  });
  it("unarchives an archived repository", async () => {
    render(<RepoGeneral repo={{ ...repo, archived: true }} admin onChanged={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Unarchive" }));
    await waitFor(() => expect(calls.some((c) => c.includes('{"archived":false}'))).toBe(true));
  });
});
