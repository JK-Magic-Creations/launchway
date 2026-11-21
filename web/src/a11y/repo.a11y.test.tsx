import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";

const c2 = "2".repeat(40);

// The pages that read a repository's code, scanned by axe-core in both themes with an empty baseline, and a
// walk through tree, file, blame and commit using only the keyboard.
describe.each(["light", "dark"])("Repository code pages, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; Element.prototype.scrollIntoView = () => {}; document.documentElement.setAttribute("data-theme", theme); installServer(); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });
  const open = async (path: string) => {
    history.replaceState(null, "", path);
    render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
  };
  const clean = async (what: string) => {
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it("the tree with its README", async () => {
    await open("/repositories/acme/shop/code");
    await screen.findByRole("table", { name: /Files and folders/ }, { timeout: 8000 });
    const readme = await screen.findByRole("region", { name: "README.md" }, { timeout: 8000 });
    expect(readme.querySelector("script")).toBeNull();
    expect(readme.querySelector("a[href*='path=docs%2Fguide.md']")).toBeTruthy();
    expect(readme.querySelector("img")?.getAttribute("src")).toContain("/raw?");
    await clean("tree");
  }, 20000);
  it("a file with line numbers", async () => {
    await open("/repositories/acme/shop/code?path=price.go");
    await screen.findByRole("table", { name: "Contents of price.go" }, { timeout: 8000 });
    expect(screen.getByRole("link", { name: "Raw" }).getAttribute("href")).toContain("/raw?");
    await clean("file");
  }, 20000);
  it("blame", async () => {
    await open(`/repositories/acme/shop/blame?ref=main&path=price.go`);
    await screen.findByRole("table", { name: /Who changed each line/ }, { timeout: 8000 });
    expect(screen.getByRole("link", { name: /Blame before this change/ })).toBeTruthy();
    await clean("blame");
  }, 20000);
  it.each([["commits", "/repositories/acme/shop/commits", "Round prices"], ["a commit", `/repositories/acme/shop/commit/${c2}`, "Runs for this commit"],
    ["compare", "/repositories/acme/shop/compare?head=fix-rounding&base=main", "Open a pull request"], ["branches", "/repositories/acme/shop/branches", "fix-rounding"],
    ["tags", "/repositories/acme/shop/tags", "Version one"], ["search", "/repositories/acme/shop/search?q=Round", "Partial results"]])("%s", async (what, path, text) => {
    await open(path);
    await screen.findAllByText(new RegExp(text), {}, { timeout: 8000 });
    await clean(what);
  }, 20000);
  it("the editor", async () => {
    await open("/repositories/acme/shop/code?path=price.go&edit=1");
    await screen.findByRole("form", { name: "Edit a file" }, { timeout: 8000 }).catch(() => {});
    await clean("editor");
  }, 20000);
  it("reaches a file, its blame and a commit with the keyboard alone", async () => {
    const user = userEvent.setup();
    await open("/repositories/acme/shop/code");
    await screen.findByRole("table", { name: /Files and folders/ }, { timeout: 8000 });
    await user.keyboard("j");
    expect(document.activeElement?.textContent).toBe("docs/");
    await user.keyboard("j");
    expect(document.activeElement?.textContent).toBe("price.go");
    await user.keyboard("{Enter}");
    await screen.findByRole("table", { name: "Contents of price.go" }, { timeout: 8000 });
    await user.keyboard("b");
    await screen.findByRole("table", { name: /Who changed each line/ }, { timeout: 8000 });
    const link = screen.getAllByRole("link").find((a) => a.textContent === c2.slice(0, 8))!;
    link.focus();
    await user.keyboard("{Enter}");
    await screen.findByText("Runs for this commit", {}, { timeout: 8000 });
    fireEvent.keyDown(document.body, { key: "t" });
  }, 30000);
});
