import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { OverviewPage } from "./OverviewPage";

const repo = { id: "r", path: "acme/shop", home: "acme", name: "shop", default_branch: "main", visibility: "private", clone: { https: "https://lw.example/git/acme/shop.git" } };
function serve(root: "readme" | "none" | "empty") {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const u = new URL(url, "http://x");
    const j = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (u.pathname === "/api/v1/repositories/acme/shop") return j(repo);
    if (u.pathname.endsWith("/commits")) return root === "empty" ? j({ items: [] }) : j({ items: [{ sha: "abc1234", subject: "Start the shop", author: "Sam", committed_at: new Date().toISOString() }] });
    if (u.pathname.endsWith("/branches")) return j({ items: [], total: 3 });
    if (u.pathname.endsWith("/tags")) return j({ items: [], total: 1 });
    if (u.pathname.endsWith("/files") && !u.searchParams.get("path")) return root === "empty" ? j({ error: "no commits" }, 404) : j({ path: "", type: "dir", ref: "main", commit: "abc", sha: "s", entries: root === "readme" ? [{ name: "README.md", path: "README.md", type: "file", sha: "x" }] : [] });
    if (u.pathname.endsWith("/files")) return j({ path: "README.md", type: "file", ref: "main", commit: "abc", sha: "x", encoding: "utf8", content: "# Shop\n\nA small shop." });
    return j({});
  }));
}
afterEach(() => vi.unstubAllGlobals());

describe("the front page of a repository", () => {
  it("shows the README, the clone address and a line of facts", async () => {
    serve("readme");
    render(<main><OverviewPage path="acme/shop" /></main>);
    expect(await screen.findByRole("heading", { name: "Shop" })).toBeTruthy();
    expect(screen.getByLabelText("Facts about the repository").textContent).toMatch(/Default branch main.*Start the shop.*3 branches.*1 tag/);
    expect((screen.getByLabelText("Clone address") as HTMLInputElement).value).toContain("acme/shop.git");
  });
  it("says there is no README and where to put one", async () => {
    serve("none");
    render(<main><OverviewPage path="acme/shop" /></main>);
    expect(await screen.findByText("No README")).toBeTruthy();
  });
  it("tells an empty repository how to receive its first push", async () => {
    serve("empty");
    render(<main><OverviewPage path="acme/shop" /></main>);
    expect(await screen.findByText("This repository is empty")).toBeTruthy();
    expect(screen.getByLabelText("Facts about the repository").textContent).toContain("no commits yet");
  });
});
