import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RepositoryPage } from "./Repository";

const repo = { id: "rep_1", path: "acme/app", name: "app", visibility: "private", mirror: true, clone: { https: "https://lw.example/git/acme/app.git" } };
const mirror = { id: "mir_1", repository: "acme/app", direction: "pull", url: "https://git.example.com/acme/app.git", credential: "env:GITEA_TOKEN",
  interval_seconds: 3600, enabled: true, prune: true, state: "error", failures: 3, refs: 12, lag_seconds: 7200, behind: true,
  last_error: "the other server asked for credentials this mirror does not have (401)" };

function serve(items: unknown[]) {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const body = url.endsWith("/mirrors") ? { items } : repo;
    return new Response(JSON.stringify(body), { status: 200 });
  }));
}

afterEach(() => vi.unstubAllGlobals());

describe("the Mirrors tab", () => {
  it("shows a mirror's status, its last error and where its credential is read from, never the credential", async () => {
    serve([mirror]);
    render(<RepositoryPage path="acme/app" tab="mirrors" />);
    expect(await screen.findByText(/Pull mirror of/)).toBeTruthy();
    expect(screen.getByText(/It has fallen behind/)).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("401");
    expect(screen.getByText("env:GITEA_TOKEN")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Stop mirroring/ })).toBeTruthy();
  });
  it("explains an empty list and offers to add one, with the token kept as a reference", async () => {
    serve([]);
    render(<RepositoryPage path="acme/app" tab="mirrors" />);
    await waitFor(() => expect(screen.getByText("No mirrors")).toBeTruthy());
    expect(screen.getByRole("form", { name: "Add a mirror" })).toBeTruthy();
    expect(screen.getByText(/An environment variable of the server/)).toBeTruthy();
    expect(screen.getByText(/forge\.mirror_allow/)).toBeTruthy();
  });
});
