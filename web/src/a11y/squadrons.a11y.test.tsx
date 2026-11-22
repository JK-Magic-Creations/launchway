import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { installServer, squadronFixtures } from "../test/mockApi";
import { me } from "../test/fixtures";
import { scan } from "../test/axe";

// The pages that make and show Squadrons and Flights, drawn whole in both themes and scanned by axe-core with no baseline:
// every step of adding a Squadron and a Flight, the Squadron's three tabs, the reasons shown to people who may not, and
// the states that need a click (a validation message, the starter pipeline after a Flight is added).
describe.each(["light", "dark"])("squadrons and flights, %s theme", (theme) => {
  beforeEach(() => {
    HTMLCanvasElement.prototype.getContext = (() => null) as never;
    document.documentElement.setAttribute("data-theme", theme);
  });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  const open = async (path: string, overrides: Record<string, unknown> = {}, who = me({ admin: true })) => {
    const levels = { installation: { singular: "Base", plural: "Bases" }, organisation: { singular: "Wing", plural: "Wings" }, unit: { singular: "Squadron", plural: "Squadrons" }, project: { singular: "Flight", plural: "Flights" } };
    installServer({ "/api/v1/config": { available: true, kinds: [], levels, admin: true }, ...squadronFixtures(), ...overrides }, who);
    history.replaceState(null, "", path);
    const { container } = render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    await waitFor(() => { if (container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 8000 }).catch(() => {});
    await new Promise((r) => setTimeout(r, 200));
  };
  const clean = async (what: string) => {
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it.each([
    ["the list", "/units"], ["adding a Squadron", "/units/new"], ["a Squadron", "/units/payments"], ["its people tab", "/units/payments?tab=people"],
    ["its operations tab", "/units/payments?tab=operations"], ["an empty Squadron", "/units/storefront"], ["adding a Flight: the search", "/units/payments/projects/new"],
    ["adding a Flight: a connection", "/units/payments/projects/new?connection=github-acme"], ["adding a Flight: the confirmation", "/units/payments/projects/new?pick=acme%2Fshop&from=hosted"],
    ["the chooser", "/projects/new"],
  ])("%s has no accessibility violations", async (what, path) => {
    await open(path);
    await clean(what);
  }, 20000);

  it("the reasons for a person who may not make anything have none", async () => {
    const why = { squadrons: [], unfiled: [], may_manage: false, why: "You need the admin role on every Flight to make a Squadron." };
    await open("/units/new", { "/api/v1/units": why }, me({ admin: false }));
    await screen.findByText(/admin role on every Flight/);
    await clean("add a Squadron, refused");
  }, 20000);

  it("the validation message of the name field has none", async () => {
    await open("/units/new");
    await userEvent.type(await screen.findByLabelText(/^Name/), "bad name");
    await screen.findByText(/letters, digits/);
    await clean("add a Squadron, with a message");
  }, 20000);

  it("the starter pipeline after a Flight is added has none", async () => {
    await open("/units/payments/projects/new?pick=acme%2Fshop&from=hosted");
    await userEvent.click(await screen.findByRole("button", { name: /Add the (Flight|Codebase)/ }));
    await screen.findByLabelText("A starter pipeline, read only");
    await clean("the starter");
  }, 20000);
});
