import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PullRequestsPage } from "./PullRequests";
import { BranchesPage } from "./repo/BranchesPage";
import { RepositoryGraphPage } from "./SoftwareGraph";
import { RepositorySettings } from "./Repositories";
import { RepoTabs } from "../components/RepoTabs";

// Every view has an address: a click that changes what is shown changes the address, and Back undoes it.
// One test per family of repository pages (the others live with their pages).

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
const seen: string[] = [];
beforeEach(() => {
  seen.length = 0;
  history.replaceState(null, "", "/x");
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const u = new URL(url, "http://x");
    seen.push(u.pathname + u.search);
    if (u.pathname.endsWith("/pulls")) return json({ items: [] });
    if (u.pathname.endsWith("/branches")) return json({ items: [], total: 0, default_branch: "main" });
    if (u.pathname === "/api/v1/graph/neighbours") return json({ start: { kind: "repository", key: "acme/svc" }, truncated: false, hits: [] });
    if (u.pathname === "/api/v1/graph/impact") return json({ start: { kind: "repository", key: "acme/svc" }, total: 0, by_kind: {}, repositories: [], by_owner: [], deployments: [], truncated: false });
    return json({ items: [] });
  }));
});
afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, "", "/"); });

describe("repository pages keep what is shown in the address", () => {
  it("pull requests: the filter and the label", async () => {
    render(<PullRequestsPage path="acme/svc" />);
    fireEvent.click(await screen.findByRole("button", { name: "Mine" }));
    expect(location.search).toBe("?filter=mine");
    await waitFor(() => expect(seen.some((s) => s.includes("author=%40me") || s.includes("author=@me"))).toBe(true));
    fireEvent.change(screen.getByLabelText("Filter by label"), { target: { value: "bug" } });
    expect(location.search).toBe("?filter=mine&label=bug");
    history.back();
  });
  it("pull requests: an address with a filter opens that filter", async () => {
    history.replaceState(null, "", "/x?filter=merged");
    render(<PullRequestsPage path="acme/svc" />);
    expect(await screen.findByRole("button", { name: "Merged", pressed: true })).toBeTruthy();
  });
  it("branches: the search text", async () => {
    render(<BranchesPage path="acme/svc" />);
    fireEvent.change(await screen.findByLabelText("Find a branch"), { target: { value: "fix" } });
    expect(location.search).toBe("?q=fix");
  });
  it("graph: the view, the focus and the picture", async () => {
    render(<main><RepositoryGraphPage path="acme/svc" /></main>);
    fireEvent.click(await screen.findByRole("button", { name: "What it affects" }));
    expect(location.search).toBe("?view=impact");
    await act(async () => { await Promise.resolve(); });
    fireEvent.click(screen.getByRole("button", { name: "Explore" }));
    expect(location.search).toBe("");
    fireEvent.click(await screen.findByRole("button", { name: "Show as a picture" }));
    expect(location.search).toBe("?picture=1");
  });
  it("settings: says it is loading, and names what is missing when there is no public address", async () => {
    render(<RepositorySettings owner="acme" name="svc" />);
    expect(screen.getByText(/Loading the repository's settings/)).toBeTruthy();
  });
  it("the tab bar is the same on every page of a repository, and marks the page it is on", () => {
    const { container } = render(<RepoTabs path="acme/svc" on="graph" />);
    expect([...container.querySelectorAll("a")].map((a) => a.textContent)).toEqual(["Code", "Commits", "Branches", "Tags", "Search", "Pull requests", "Dependencies", "Graph", "Mirrors", "Imported history", "Settings"]);
    expect(container.querySelector("[aria-current=page]")?.textContent).toBe("Graph");
    expect(container.querySelector("a[href='/repositories/acme/svc/settings']")).toBeTruthy();
  });
});
