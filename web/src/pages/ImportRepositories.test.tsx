import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ImportRepositoriesPage } from "./ImportRepositories";

const conn = { name: "oldgit", kind: "gitea", base_url: "https://git.example.com", credential: "env:OLDGIT_TOKEN", enabled: true };
const item = (o: object) => ({ source: "acme/web", repository: "acme/web", url: "u", visibility: "private", action: "create", state: "planned", reason: "would be created private and left a pull mirror", ...o });

function serve() {
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    let out: unknown;
    if (url === "/api/v1/connections") out = { items: [conn] };
    else if (body.dry_run) out = { dry_run: true, summary: {}, items: [item({}), item({ source: "acme/mine", repository: "acme/mine", action: "skip", state: "skipped", reason: "a repository of that name exists and is not a mirror of this address; it is left alone" })] };
    else out = { dry_run: false, summary: {}, items: [item({ state: "verified", hosted_refs: 5, verified: true })] };
    return new Response(JSON.stringify(out), { status: 200 });
  }));
}
afterEach(() => vi.unstubAllGlobals());

describe("the import wizard", () => {
  it("shows what would be made, and what is left alone and why, before anything is copied", async () => {
    serve();
    render(<ImportRepositoriesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Show what would be made" }));
    expect(await screen.findByText(/would be created private/)).toBeTruthy();
    expect(screen.getByText(/it is left alone/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Import 1 repositories" })).toBeTruthy();
  });
  it("copies each repository, then verifies", async () => {
    serve();
    render(<ImportRepositoriesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Show what would be made" }));
    fireEvent.click(await screen.findByRole("button", { name: "Import 1 repositories" }));
    await waitFor(() => expect(screen.getByText(/verified, 5 refs equal/)).toBeTruthy());
    await waitFor(() => expect(screen.getByText("Every ref was checked against the source")).toBeTruthy());
  });
});
