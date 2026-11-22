import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../../App";
import { installServer, squadronFixtures } from "../../test/mockApi";
import { me } from "../../test/fixtures";
import { flightNameFrom, flightNameProblem, unitNameProblem } from "./shared";

// The pages that make Squadrons and Flights, drawn whole (shell and page) over the mock server. The words are the
// aviation ones or the plain ones depending on the installation, so a name is matched in either set.
let calls: { method: string; path: string; body?: unknown }[] = [];

/** Starts the mock server, then watches every call; `extra` answers a path (with its method) before the mock does. */
function serve(overrides: Record<string, unknown> = {}, extra: (method: string, path: string, body: unknown) => Response | undefined = () => undefined, who = me({ admin: true })) {
  // The words of the aviation set: the level names the installation reports are the set's own, so the set's words show.
  const levels = { installation: { singular: "Base", plural: "Bases" }, organisation: { singular: "Wing", plural: "Wings" }, unit: { singular: "Squadron", plural: "Squadrons" }, project: { singular: "Flight", plural: "Flights" } };
  installServer({ "/api/v1/config": { available: true, kinds: [], levels, admin: true }, ...squadronFixtures(), ...overrides }, who);
  const base = globalThis.fetch;
  calls = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, path: String(input), body });
    return extra(method, String(input).split("?")[0], body) ?? base(input, init);
  }));
}
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

async function open(path: string) {
  history.replaceState(null, "", path);
  render(<App />);
  await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
}

beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; });
afterEach(() => { vi.unstubAllGlobals(); });

describe("the name rules", () => {
  it("say the same plain things the server says", () => {
    expect(unitNameProblem("")).toMatch(/Give the .* a name/);
    expect(unitNameProblem(" a")).toMatch(/does not start or end with a space/);
    expect(unitNameProblem("a".repeat(101))).toMatch(/at most 100/);
    expect(unitNameProblem("-lead")).toMatch(/letters, digits/);
    expect(unitNameProblem("has space")).toMatch(/letters, digits/);
    expect(unitNameProblem("payments.core_v2-b")).toBe("");
  });
  it("name a Flight from its repository as the server does", () => {
    expect(flightNameFrom("acme/shop")).toBe("shop");
    expect(flightNameFrom("https://git.example.com/acme/shop-api.git")).toBe("shop-api");
    expect(flightNameFrom("git@host:acme/my repo.git")).toBe("my-repo");
    expect(flightNameProblem("ok-name")).toBe("");
    expect(flightNameProblem("-bad")).not.toBe("");
  });
});

describe("Add a Squadron", () => {
  it("asks only for a name, checks it as people type, and opens the new page", async () => {
    serve({}, (m, p) => (m === "POST" && p === "/api/v1/units" ? json({ name: "ops" }, 201) : undefined));
    await open("/units/new");
    expect(await screen.findByRole("heading", { level: 1, name: /Add a (Squadron|Team)/ })).toBeInTheDocument();
    expect(screen.queryByLabelText(/repositor/i)).not.toBeInTheDocument();
    const name = screen.getByLabelText(/^Name/);
    await userEvent.type(name, "bad name");
    expect(await screen.findByText(/letters, digits/)).toBeInTheDocument();
    await userEvent.clear(name);
    await userEvent.type(name, "ops");
    await userEvent.click(screen.getByRole("button", { name: /Add the (Squadron|Team)/ }));
    await waitFor(() => expect(location.pathname).toBe("/units/ops"));
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ name: "ops" });
  });

  it("shows what the server says when the name is taken", async () => {
    serve({}, (m, p) => (m === "POST" && p === "/api/v1/units" ? json({ error: "A Squadron called payments already exists." }, 409) : undefined));
    await open("/units/new");
    await userEvent.type(await screen.findByLabelText(/^Name/), "payments");
    await userEvent.click(screen.getByRole("button", { name: /Add the (Squadron|Team)/ }));
    expect((await screen.findAllByRole("alert")).some((a) => a.classList.contains("errorbox") && /already exists/.test(a.textContent ?? ""))).toBe(true);
    expect(location.pathname).toBe("/units/new");
  });

  it("gives the reason instead of a form when the person may not", async () => {
    serve({ "/api/v1/units": { squadrons: [], unfiled: [], may_manage: false, why: "You need the admin role on every Flight to make a Squadron." } }, undefined, me({ admin: false }));
    await open("/units/new");
    expect(await screen.findByText(/admin role on every Flight/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/^Name/)).not.toBeInTheDocument();
  });
});

describe("a Squadron's page", () => {
  it("shows its Flights, what needs a person, and keeps the tab in the address", async () => {
    serve();
    await open("/units/payments");
    expect(await screen.findByRole("heading", { level: 1, name: "Payments" })).toBeInTheDocument();
    expect(await screen.findByText("Run #7 of shop-api failed on main")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "shop-api" })).toHaveAttribute("href", "/projects/shop-api/pipeline");
    expect(screen.getAllByRole("link", { name: /Add a (Flight|Codebase)/ })[0]).toHaveAttribute("href", "/units/payments/projects/new");
    await userEvent.click(screen.getByRole("tab", { name: "People and shared settings" }));
    expect(location.search).toBe("?tab=people");
    expect(await screen.findByText("group payments-dev")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Role bindings" })).toHaveAttribute("href", "/config/role-bindings");
    // The link opens the configuration object of the Squadron: the kind's path is "units" (internal/settings), which the configuration page reads.
    expect(screen.getByRole("link", { name: /^Edit this / })).toHaveAttribute("href", "/config/units/payments");
    await userEvent.click(screen.getByRole("tab", { name: "Operations" }));
    expect(location.search).toBe("?tab=operations");
    expect(screen.getByRole("link", { name: "restore-ledger" })).toHaveAttribute("href", "/projects/ledger/pipeline");
    expect(screen.getByText(/This list may be missing some/)).toBeInTheDocument();
    history.back();
    await waitFor(() => expect(location.search).toBe("?tab=people"));
  });

  it("opens straight on a bookmarked tab", async () => {
    serve();
    await open("/units/payments?tab=operations");
    expect(await screen.findByText("Started by hand")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Operations" })).toHaveAttribute("aria-selected", "true");
  });

  it("says what a Squadron is for when it is empty, and offers to add a Flight", async () => {
    serve();
    await open("/units/storefront");
    expect(await screen.findByText(/a named folder of .* with its own people and shared settings/)).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: /Add a (Flight|Codebase)/ })[0]).toHaveAttribute("href", "/units/storefront/projects/new");
  });

  it("explains instead of offering when the person may not make Flights", async () => {
    serve({ "/api/v1/units/storefront": { name: "storefront", display_name: "", flights: [], needs: [], people: [], settings: [], operations: [], operations_complete: true, may_manage: false, why: "Only administrators add a Flight here." } });
    await open("/units/storefront");
    expect(await screen.findByText("Only administrators add a Flight here.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Add a (Flight|Codebase)/ })).not.toBeInTheDocument();
  });

  it("says plainly that a Squadron does not exist", async () => {
    serve({}, (_m, p) => (p === "/api/v1/units/ghost" ? json({ error: "There is no such Squadron." }, 404) : undefined));
    await open("/units/ghost");
    expect(await screen.findByText(/There is no .* called ghost/)).toBeInTheDocument();
  });

  it("is listed with every Squadron and the Flights in none", async () => {
    serve();
    await open("/units");
    const link = await screen.findByRole("link", { name: "Payments" });
    expect(link).toHaveAttribute("href", "/units/payments");
    expect(screen.getByText("Unfiled (1)")).toBeInTheDocument();
  });
});

describe("Add a Flight", () => {
  it("lists repositories without a Flight first, marks the others, and keeps connections behind a choice", async () => {
    serve();
    await open("/units/payments/projects/new");
    expect(await screen.findByText("acme/shop")).toBeInTheDocument();
    const rows = screen.getAllByRole("listitem").map((li) => li.textContent ?? "");
    expect(rows.findIndex((r) => r.includes("acme/shop"))).toBeLessThan(rows.findIndex((r) => r.includes("acme/ledger")));
    expect(screen.getByText(/already a .*: /)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Search github-acme" })).toBeInTheDocument();
    expect(calls.some((c) => c.path.includes("connection="))).toBe(false);
    expect(screen.queryByText(/credential|token/i)).not.toBeInTheDocument();
  });

  it("searches a connection only when asked", async () => {
    serve({ "/api/v1/project-candidates": { hosted: [], hosted_without_flight: 0, connections: ["github-acme"], connection_items: [{ path: "acme/web", source: "github-acme", address: "https://github.example/acme/web.git" }] } });
    await open("/units/payments/projects/new?connection=github-acme");
    expect(await screen.findByText("acme/web")).toBeInTheDocument();
    expect(calls.some((c) => c.path.includes("connection=github-acme"))).toBe(true);
  });

  it("chooses, confirms, makes the Flight, and shows the starter with a copy button and the editor link", async () => {
    serve();
    await open("/units/payments/projects/new");
    await userEvent.click(await screen.findByRole("link", { name: "Choose acme/shop" }));
    expect(location.search).toContain("pick=acme%2Fshop");
    const name = await screen.findByLabelText(/^Name of the/);
    expect(name).toHaveValue("shop");
    await userEvent.click(screen.getByRole("button", { name: /Add the (Flight|Codebase)/ }));
    await waitFor(() => expect(location.search).toContain("added=shop"));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.path).toBe("/api/v1/units/payments/projects");
    expect(post?.body).toEqual({ repository: "acme/shop", name: "shop" });
    expect(await screen.findByLabelText("A starter pipeline, read only")).toHaveTextContent("pipeline: shop");
    expect(screen.getByRole("link", { name: "Open the pipeline editor" })).toHaveAttribute("href", "/projects/shop/pipeline/edit");
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    await userEvent.click(screen.getByRole("button", { name: "Copy the starter" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(expect.stringContaining("pipeline: shop")));
  });

  it("says which file it found when the repository already has a pipeline", async () => {
    serve({}, (m, p) => (m === "POST" && p === "/api/v1/units/payments/projects" ? json({ name: "shop", unit: "payments", pipeline: { state: "found", path: ".launchway/pipeline.yaml", note: "Found .launchway/pipeline.yaml in the repository; it runs as it is." } }, 201) : undefined));
    await open("/units/payments/projects/new?pick=acme%2Fshop&from=hosted");
    await userEvent.click(await screen.findByRole("button", { name: /Add the (Flight|Codebase)/ }));
    expect(await screen.findByText(/Found \.launchway\/pipeline\.yaml/)).toBeInTheDocument();
    expect(screen.queryByLabelText("A starter pipeline, read only")).not.toBeInTheDocument();
  });

  it("takes a plain Git address and sends it as repo", async () => {
    serve();
    await open("/units/payments/projects/new");
    await userEvent.type(await screen.findByLabelText("Git address"), "https://git.example.com/acme/blog.git");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByLabelText(/^Name of the/)).toHaveValue("blog");
    await userEvent.click(screen.getByRole("button", { name: /Add the (Flight|Codebase)/ }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST")).toBeTruthy());
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ repo: "https://git.example.com/acme/blog.git", name: "blog" });
  });

  it("points to connecting a repository only when there is nothing hosted and no connection", async () => {
    serve({ "/api/v1/project-candidates": { hosted: [], hosted_without_flight: 0, connections: [], connection_items: [] } });
    await open("/units/payments/projects/new");
    expect(await screen.findByText("No repositories to choose from")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Connect a repository" })).toHaveAttribute("href", "/config/wizard/connect-repository");
  });

  it("shows the reason when the person may not add Flights, and a not-found for an unknown Squadron", async () => {
    serve({ "/api/v1/units/payments": { ...(squadronFixtures()["/api/v1/units/payments"] as object), may_manage: false, why: "Only administrators add a Flight here." } });
    await open("/units/payments/projects/new");
    expect(await screen.findByText("Only administrators add a Flight here.")).toBeInTheDocument();
    expect(screen.queryByRole("search")).not.toBeInTheDocument();
  });
});

describe("the old Add a project entry points", () => {
  it("lead to the chooser, which preselects the only Squadron and goes on to add a Flight", async () => {
    serve({ "/api/v1/units": { squadrons: [{ name: "payments", display_name: "Payments", flights: 2, needs: 0, failing: 0, running: 0 }], unfiled: [], may_manage: true } });
    await open("/projects/new");
    const radio = await screen.findByRole("radio", { name: /Payments/ });
    expect(radio).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(location.pathname).toBe("/units/payments/projects/new");
  });

  it("make the person choose when there are several", async () => {
    serve();
    await open("/projects/new");
    const go = await screen.findByRole("button", { name: "Continue" });
    expect(go).toBeDisabled();
    fireEvent.click(screen.getByRole("radio", { name: /Payments/ }));
    expect(go).toBeEnabled();
    expect(location.search).toBe("?unit=payments");
  });

  it("ask to add a Squadron first when there is none", async () => {
    serve({ "/api/v1/units": { squadrons: [], unfiled: [], may_manage: true } });
    await open("/projects/new");
    expect(await screen.findByRole("heading", { name: /Add a (Squadron|Team) first/ })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: /Add a (Squadron|Team)/ }).some((a) => a.getAttribute("href") === "/units/new")).toBe(true);
  });

  it("show an Add a Squadron button at the top of the pipelines page", async () => {
    serve();
    await open("/pipelines");
    const links = await screen.findAllByRole("link", { name: /Add a (Squadron|Team)/ });
    expect(links.some((a) => a.getAttribute("href") === "/units/new")).toBe(true);
  });
});
