import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Explorer } from "./Explorer";
import { buildTree, flightSeg, isFlightSeg, UNFILED, type Seg, type XItem } from "./tree";
import { routeFetch, squadronList } from "../test/squadronFixtures";

const seg = (label: string): Seg => ({ key: label, label });
const item = (name: string, path: Seg[]): XItem => ({
  id: name, name, path, labels: [], tone: "ok", history: [], running: 0, href: `/projects/${name}/pipeline`, favorite: false, last: { text: "Passed · #4", at: 1000 },
});
const items = [
  item("pay-api", [seg("Payments"), flightSeg("pay-api")]), item("pay-ledger", [seg("Payments"), flightSeg("pay-ledger")]),
  item("docs", [UNFILED, flightSeg("docs")]),
];

beforeEach(() => history.replaceState(null, "", "/pipelines"));
afterEach(() => vi.unstubAllGlobals());

describe("the Squadron > Flight > pipeline tree", () => {
  it("makes a Flight step under its Squadron and a built-in Unfiled place, never a slash in a key", () => {
    const t = buildTree([...items, item("a/b", [UNFILED, flightSeg("a/b")])]);
    expect(t.get("Payments")!.children).toEqual(["Payments/fl:pay-api", "Payments/fl:pay-ledger"]);
    expect(t.get("unfiled")!.children).toContain("unfiled/fl:a|b");
    expect(isFlightSeg(flightSeg("x"))).toBe(true);
    expect(t.get("unfiled")!.count).toBe(2);
  });

  it("shows the tree with Unfiled, leaves the Flight out of a card's place line, and adds checkboxes to Flights", async () => {
    const r = routeFetch({ "GET /api/v1/units": squadronList() });
    vi.stubGlobal("fetch", vi.fn(r.f));
    render(<Explorer noun={{ singular: "pipeline", plural: "pipelines" }} items={items} now={5000} empty={<p>none</p>} flights={{}} />);
    const tree = screen.getByRole("tree");
    expect(within(tree).getByRole("treeitem", { name: /^Unfiled 1 item/ })).toBeInTheDocument();
    expect(within(tree).getByRole("treeitem", { name: /^Payments 2 items/ })).toBeInTheDocument();
    const card = screen.getByRole("article", { name: "docs" });
    expect(within(card).getByText("Unfiled")).toBeInTheDocument();
    await userEvent.click(within(card).getByRole("checkbox", { name: "Select Flight docs" }));
    expect(screen.getByRole("button", { name: /^Move to / })).toBeEnabled();
    await userEvent.click(screen.getByRole("checkbox", { name: /Select all/ }));
    expect(screen.getByText(/3 Flights selected/)).toBeInTheDocument();
    // Selecting only what is in view: filtering to one Flight narrows the selection to it.
    await userEvent.type(screen.getByRole("searchbox"), "ledger");
    expect(screen.getByText(/1 Flight selected/)).toBeInTheDocument();
  });

  it("has no selection controls when the items are not Flights", () => {
    render(<Explorer noun={{ singular: "pipeline", plural: "pipelines" }} items={items} now={5000} empty={<p>none</p>} />);
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
    expect(screen.queryAllByRole("checkbox", { name: /Select Flight/ })).toHaveLength(0);
  });
});
