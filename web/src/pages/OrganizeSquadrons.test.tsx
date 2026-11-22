import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { OrganizePage } from "./OrganizeSquadrons";
import { OrganizeCallout } from "../components/OrganizeCallout";
import { proposal, routeFetch, squadronList } from "../test/squadronFixtures";

const result = { created: ["shop"], moved: ["shop-api", "shop-web", "payments-api", "payments-ledger"], blocked: [], unchanged: [], undo: { "shop-api": "" }, undo_delete: ["shop"], to: "" };
const stub = (list = squadronList()) => {
  const r = routeFetch({ "GET /api/v1/units": list, "GET /api/v1/unit-organize": proposal(), "POST /api/v1/unit-organize": result, "POST /api/v1/unit-organize/undo": { moved: [], blocked: [], unchanged: [], undo: {}, to: "" } });
  vi.stubGlobal("fetch", vi.fn(r.f));
  return r;
};
afterEach(() => vi.unstubAllGlobals());
const group = (name: string) => screen.getByRole("heading", { name: new RegExp("^" + name) }).closest("li") as HTMLElement;

describe("Organize into Squadrons", () => {
  it("shows the proposal, lets a person rename, merge and move by keyboard-friendly controls, applies once and can undo", async () => {
    const r = stub();
    render(<OrganizePage />);
    await screen.findByRole("heading", { name: /^shop/ });
    // Rename the proposed group.
    const name = within(group("shop")).getByRole("textbox", { name: /Name of the Squadron/ });
    await userEvent.clear(name);
    await userEvent.type(name, "storefront-team");
    expect(within(group("storefront-team")).getByText(/A new Squadron will be made/)).toBeInTheDocument();
    // A name that is not allowed is said in words and Apply waits.
    await userEvent.clear(name);
    await userEvent.type(name, "bad name");
    expect(await screen.findByText(/A name uses letters, digits/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled();
    await userEvent.clear(name);
    await userEvent.type(name, "shop");
    // Move docs (left unfiled) into shop; then move payments-ledger to unfiled.
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Move docs to" }), "1");
    expect(within(group("shop")).getByText("docs")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Move payments-ledger to" }), "unfiled");
    expect(screen.getByRole("combobox", { name: "Move payments-ledger to" })).toBeInTheDocument();
    // Merge payments into shop.
    await userEvent.selectOptions(within(group("payments")).getByRole("combobox", { name: /Merge payments into/ }), "1");
    expect(screen.queryByRole("heading", { name: /^payments/ })).not.toBeInTheDocument();
    expect(within(group("shop")).getByText("payments-api")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    const done = await screen.findByText(/in one audited change/);
    expect(done.textContent).toMatch(/Filed 4 Flights in one audited change, and made 1 Squadron: shop/);
    const sent = r.calls.find((c) => c.key === "POST /api/v1/unit-organize")!.body as { groups: { name: string; flights: string[] }[] };
    expect(sent.groups).toHaveLength(1);
    expect(sent.groups[0].name).toBe("shop");
    expect(sent.groups[0].flights.sort()).toEqual(["docs", "payments-api", "shop-api", "shop-web"]);
    await userEvent.click(screen.getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(r.calls.some((c) => c.key === "POST /api/v1/unit-organize/undo")).toBe(true));
    expect(r.calls.find((c) => c.key === "POST /api/v1/unit-organize/undo")!.body).toEqual({ homes: { "shop-api": "" }, delete_units: ["shop"] });
    expect(await screen.findByText(/Undone/)).toBeInTheDocument();
  });

  it("says why when the person may not organize", async () => {
    stub(squadronList({ may_manage: false, why: "Making and changing Squadrons is for people who administer every Flight today." }));
    render(<OrganizePage />);
    expect(await screen.findByText(/administer every Flight/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply" })).not.toBeInTheDocument();
  });
});

describe("the callout", () => {
  it("offers the wizard only when Flights are unfiled and the person may manage", async () => {
    stub();
    const { unmount } = render(<OrganizeCallout />);
    expect(await screen.findByText(/3 Flights are in no Squadron/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Organize them" })).toHaveAttribute("href", "/units/organize");
    unmount();
    stub(squadronList({ may_manage: false }));
    render(<OrganizeCallout />);
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("link", { name: /Organize/ })).not.toBeInTheDocument();
  });
  it("is silent when every Flight is filed", async () => {
    stub(squadronList({ unfiled: [] }));
    render(<OrganizeCallout />);
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });
});
