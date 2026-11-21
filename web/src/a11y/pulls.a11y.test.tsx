import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer } from "../test/mockApi";
import { scan } from "../test/axe";

// The views of a pull request that need a click to appear (its files with the diff in both layouts, a comment
// form on a line, the commits), scanned by axe-core in both themes. The pages themselves are in pages.a11y.test.tsx.
describe.each(["light", "dark"])("Pull request views, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); installServer(); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  const open = async () => {
    history.replaceState(null, "", "/repositories/acme/shop/pulls/1");
    render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
  };
  const expectClean = async (what: string) => {
    await new Promise((r) => setTimeout(r, 150));
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it("says in words what is missing and offers no merge button that would not work", async () => {
    await open();
    await screen.findByText(/Needs 1 more approval; `test` has not passed/, {}, { timeout: 8000 });
    expect(screen.queryByRole("button", { name: /^Merge$|^Merge past/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Close" })).toBeTruthy();
  }, 20000);
  it.each([["Unified"], ["Side by side"]])("the files, %s", async (layout) => {
    await open();
    fireEvent.click(await screen.findByRole("tab", { name: /Files changed/ }, { timeout: 8000 }));
    fireEvent.click(await screen.findByRole("button", { name: layout }));
    await screen.findByRole("table", { name: "Changes to price.go" }, { timeout: 8000 });
    await expectClean(`files, ${layout}`);
  }, 20000);
  it("the comment form on a line", async () => {
    await open();
    fireEvent.click(await screen.findByRole("tab", { name: /Files changed/ }, { timeout: 8000 }));
    fireEvent.click(await screen.findByRole("button", { name: "Comment on price.go line 2" }, { timeout: 8000 }));
    await screen.findByRole("textbox", { name: "Comment" });
    await expectClean("comment form");
  }, 20000);
  it("the commits", async () => {
    await open();
    fireEvent.click(await screen.findByRole("tab", { name: "Commits" }, { timeout: 8000 }));
    await screen.findByText("Round prices", {}, { timeout: 8000 });
    await expectClean("commits");
  }, 20000);
});
