import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CodePage } from "./CodePage";

function serve() {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    const u = new URL(url, "http://x");
    const j = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    if (u.pathname === "/api/v1/repositories/acme/shop") return j({ id: "r", path: "acme/shop", home: "acme", name: "shop", default_branch: "main", visibility: "private", clone: {} });
    if (u.pathname.endsWith("/files")) return j({ path: "", type: "dir", ref: "release/1.0", commit: "abc", sha: "s", entries: [{ name: "a.txt", path: "a.txt", type: "file", sha: "x" }] });
    if (u.pathname.endsWith("/protected-branches")) return j({ patterns: [] });
    return j({ items: [] });
  }));
}
afterEach(() => vi.unstubAllGlobals());

describe("the code page", () => {
  it("offers a snapshot of the ref in both formats, as plain links the server answers", async () => {
    serve();
    history.replaceState(null, "", "/repositories/acme/shop/code?ref=release%2F1.0");
    render(<main><CodePage path="acme/shop" /></main>);
    const tgz = await screen.findByRole("link", { name: /Download tar.gz/ });
    expect(tgz.getAttribute("href")).toBe("/api/v1/repositories/acme/shop/download?ref=release%2F1.0&format=tar.gz");
    expect(screen.getByRole("link", { name: /Download zip/ }).getAttribute("href")).toBe("/api/v1/repositories/acme/shop/download?ref=release%2F1.0&format=zip");
  });
});
