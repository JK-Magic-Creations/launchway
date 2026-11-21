import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { scan } from "../test/axe";
import { Explorer } from "./Explorer";
import type { Seg, Tone, XItem } from "./tree";

const seg = (label: string, folderId?: string): Seg => ({ key: label, label, folderId });
const item = (name: string, path: Seg[], tone: Tone = "ok", extra: Partial<XItem> = {}): XItem => ({
  id: name, name, path, labels: [], tone, history: ["ok", "bad", "ok"], running: 0, href: `/projects/${name}/pipeline`, favorite: false,
  last: { text: tone === "bad" ? "Failed · #4" : "Passed · #4", at: 1000 }, ...extra,
});
const payments = [seg("Payments", "f1")];
const items = [
  item("pay-api", payments, "bad", { labels: ["tier-1"], running: 1 }), item("pay-ledger", payments, "ok", { labels: ["tier-1", "go"] }),
  item("web-shop", [seg("Storefront")], "ok", { description: "The public shop" }), item("docs", [], "none"),
];
const props = () => ({ noun: { singular: "pipeline", plural: "pipelines" }, items, now: 5000, empty: <p>none</p> });

beforeEach(() => history.replaceState(null, "", "/pipelines"));
afterEach(() => vi.unstubAllGlobals());

describe("the Explorer", () => {
  it("shows cards with where each lives, its last result and a text alternative for the history", () => {
    render(<Explorer {...props()} />);
    const card = screen.getByRole("article", { name: "pay-api" });
    expect(within(card).getByText("Payments")).toBeInTheDocument();
    expect(within(card).getByText(/Failed/)).toBeInTheDocument();
    expect(within(card).getByRole("img", { name: /Recent results of pay-api: 2 passed, 1 failed/ })).toBeInTheDocument();
    expect(within(card).getByText("1 running")).toBeInTheDocument();
  });
  it("puts what needs a look first", () => {
    render(<Explorer {...props()} />);
    expect(screen.getAllByRole("article").map((a) => a.getAttribute("aria-label"))[0]).toBe("pay-api");
  });
  it("filters the tree and the pane as you type, highlights the match and keeps the filter in the address", async () => {
    render(<Explorer {...props()} />);
    await userEvent.type(screen.getByRole("searchbox"), "ledger");
    expect(screen.getAllByRole("article")).toHaveLength(1);
    expect(screen.getByRole("article").querySelector("mark")?.textContent).toBe("ledger");
    expect(location.search).toBe("?q=ledger");
    const tree = screen.getByRole("tree");
    expect(within(tree).queryByRole("treeitem", { name: /Storefront/ })).not.toBeInTheDocument();
    expect(within(tree).getByRole("treeitem", { name: /Payments, 1 item/ })).toBeInTheDocument();
  });
  it("forgives a typo and says helpfully when nothing matches", async () => {
    render(<Explorer {...props()} />);
    await userEvent.type(screen.getByRole("searchbox"), "ledgre");
    expect(screen.getAllByRole("article")).toHaveLength(1);
    await userEvent.clear(screen.getByRole("searchbox"));
    await userEvent.type(screen.getByRole("searchbox"), "zzzz");
    expect(screen.getByText(/No pipeline matches/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getAllByRole("article")).toHaveLength(4);
  });
  it("opens with the view in the address and restores it", () => {
    history.replaceState(null, "", "/pipelines?q=pay&status=bad&view=table&sort=name");
    render(<Explorer {...props()} />);
    expect((screen.getByRole("searchbox") as HTMLInputElement).value).toBe("pay");
    expect(screen.queryAllByRole("article")).toHaveLength(0);
    expect(screen.getByRole("group", { name: "pay-api" })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "pay-ledger" })).not.toBeInTheDocument();
  });
  it("ignores an address it cannot read", () => {
    history.replaceState(null, "", "/pipelines?view=sideways&sort=%00&node=nowhere&status=");
    render(<Explorer {...props()} />);
    expect(screen.getAllByRole("article")).toHaveLength(4);
  });
  it("the tree: arrows move, right opens, left closes, enter selects, type-ahead jumps, with roles and levels", async () => {
    render(<Explorer {...props()} />);
    const tree = screen.getByRole("tree");
    const rows = () => within(tree).getAllByRole("treeitem");
    expect(rows().map((r) => r.getAttribute("aria-level"))).toEqual(["1", "2", "2"]);
    rows()[0].focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(rows()[1]);
    await userEvent.keyboard("s");
    expect(document.activeElement?.textContent).toMatch(/Storefront/);
    await userEvent.keyboard("{Enter}");
    expect(location.search).toBe("?node=Storefront");
    expect(screen.getAllByRole("article")).toHaveLength(1);
    await userEvent.keyboard("{Home}");
    expect(document.activeElement).toBe(rows()[0]);
    await userEvent.keyboard("{End}");
    expect(document.activeElement).toBe(rows().at(-1));
  });
  it("the tree: a folder with sub-folders opens and closes with the arrow keys", async () => {
    const nested = [item("a", [seg("Team"), seg("Svc", "f2")]), item("b", [seg("Team")])];
    render(<Explorer {...props()} items={nested} />);
    const tree = screen.getByRole("tree");
    const team = within(tree).getByRole("treeitem", { name: /^Team/ });
    expect(team).toHaveAttribute("aria-expanded", "false");
    team.focus();
    await userEvent.keyboard("{ArrowRight}");
    expect(team).toHaveAttribute("aria-expanded", "true");
    expect(within(tree).getByRole("treeitem", { name: /^Svc/ })).toBeInTheDocument();
    await userEvent.keyboard("{ArrowLeft}");
    expect(team).toHaveAttribute("aria-expanded", "false");
  });
  it("shows a status roll-up in words on a folder that holds a failure", () => {
    render(<Explorer {...props()} />);
    expect(within(screen.getByRole("tree")).getByRole("treeitem", { name: /Payments, 2 items, 1 failed/ })).toBeInTheDocument();
  });
  it("the slash key jumps to the filter", async () => {
    render(<Explorer {...props()} />);
    await userEvent.keyboard("/");
    expect(document.activeElement).toBe(screen.getByRole("searchbox"));
  });
  it("moves an item to a folder from its menu, announces it and can undo", async () => {
    const onMove = vi.fn().mockResolvedValue(undefined);
    render(<Explorer {...props()} onMove={onMove} folders={[{ id: "f1", path: "Payments" }, { id: "f9", path: "Storefront/New" }]} />);
    await userEvent.click(screen.getByRole("button", { name: "Arrange for everyone" }));
    expect(screen.getByRole("note")).toHaveTextContent(/changes the folders for everyone/);
    await userEvent.click(screen.getByRole("button", { name: "Move web-shop to…" }));
    const dialog = screen.getByRole("dialog", { name: "Move web-shop" });
    await userEvent.selectOptions(within(dialog).getByRole("combobox"), "f9");
    await userEvent.click(within(dialog).getByRole("button", { name: "Move" }));
    await waitFor(() => expect(onMove).toHaveBeenCalledWith(expect.objectContaining({ id: "web-shop" }), "f9"));
    await userEvent.click(await screen.findByRole("button", { name: "Undo" }));
    expect(onMove).toHaveBeenLastCalledWith(expect.objectContaining({ id: "web-shop" }), "");
  });
  it("dropping a card on a folder moves it", async () => {
    const onMove = vi.fn().mockResolvedValue(undefined);
    render(<Explorer {...props()} onMove={onMove} />);
    await userEvent.click(screen.getByRole("button", { name: "Arrange for everyone" }));
    const data = new Map<string, string>();
    const dt = { setData: (k: string, v: string) => data.set(k, v), getData: (k: string) => data.get(k) ?? "" };
    fireEvent.dragStart(screen.getByRole("article", { name: "web-shop" }), { dataTransfer: dt });
    act(() => { fireEvent.drop(within(screen.getByRole("tree")).getByRole("treeitem", { name: /Payments/ }), { dataTransfer: dt }); });
    await waitFor(() => expect(onMove).toHaveBeenCalledWith(expect.objectContaining({ id: "web-shop" }), "f1"));
  });
  it("has no move controls when the person may not arrange", () => {
    render(<Explorer {...props()} />);
    expect(screen.queryByRole("button", { name: "Arrange for everyone" })).not.toBeInTheDocument();
  });
  it("virtualizes thousands of items: only a window is in the page", () => {
    const many = Array.from({ length: 3000 }, (_, i) => item(`svc-${i}`, [seg("Big")]));
    Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 600 });
    Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 900 });
    render(<Explorer {...props()} items={many} initialView="table" />);
    expect(screen.getAllByRole("group").length).toBeLessThan(80);
    expect(screen.getByRole("status")).toHaveTextContent("3000 of 3000 pipelines");
    delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientHeight;
    delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientWidth;
  });
  it("shows the empty state when there is nothing at all", () => {
    render(<Explorer {...props()} items={[]} />);
    expect(screen.getByText("none")).toBeInTheDocument();
  });
  it("groups by label and by status", async () => {
    history.replaceState(null, "", "/pipelines?group=status");
    render(<Explorer {...props()} />);
    expect(screen.getByRole("heading", { name: /^failed/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /^passed/ })).toBeInTheDocument();
  });
});

describe.each(["light", "dark"])("the Explorer is accessible, %s theme", (theme) => {
  beforeEach(() => document.documentElement.setAttribute("data-theme", theme));
  afterEach(() => document.documentElement.removeAttribute("data-theme"));
  it.each([["cards", "/pipelines"], ["table", "/pipelines?view=table"], ["grouped", "/pipelines?group=label&q=pay"]])("%s has no violations", async (_n, path) => {
    history.replaceState(null, "", path);
    const { container } = render(<main><h1>Pipelines</h1><Explorer {...props()} onMove={vi.fn()} folders={[{ id: "f1", path: "Payments" }]} onFavorite={vi.fn()} /></main>);
    await userEvent.click(screen.getByRole("button", { name: "Arrange for everyone" }));
    expect(await scan(container, { page: false })).toEqual([]);
  });
  it("the move dialog has no violations", async () => {
    const { container } = render(<main><h1>Pipelines</h1><Explorer {...props()} onMove={vi.fn()} folders={[{ id: "f1", path: "Payments" }]} /></main>);
    await userEvent.click(screen.getByRole("button", { name: "Arrange for everyone" }));
    await userEvent.click(screen.getByRole("button", { name: "Move docs to…" }));
    expect(await scan(document.body, { page: false })).toEqual([]);
    void container;
  });
});
