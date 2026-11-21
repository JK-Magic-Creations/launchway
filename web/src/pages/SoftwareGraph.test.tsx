import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GraphPage, RepositoryGraphPage } from "./SoftwareGraph";
import { scan } from "../test/axe";
import { layout, MAX_SHOWN } from "../softgraph/layout";
import { requirementsYaml } from "../softgraph/needsYaml";
import type { GEdge, GNode, Needs } from "../softgraph/api";

const node = (kind: GNode["kind"], key: string, extra: Partial<GNode> = {}): GNode => ({ kind, key, name: key, confidence: "extracted", origin: "manifests", ...extra });
const edge = (id: number, kind: string, from: GNode, to: GNode, extra: Partial<GEdge> = {}): GEdge => ({ id, kind, from: { kind: from.kind, key: from.key }, to: { kind: to.kind, key: to.key }, origin: "manifests", confidence: "extracted", valid_from: 1, ...extra });

const repo = node("repository", "acme/svc");
const comp = node("component", "acme/svc:.", { name: "acme/svc", attrs: { dir: "." } });
const lib = node("package", "go:example.test/libgo");
const dep = edge(2, "depends-on", comp, lib, { constraint: "v1.2.0", resolved: "v1.2.0", qualifier: "go.mod" });
const contains = edge(1, "contains", repo, comp);
const deploy = node("deployment", "prod/shop/api", { attrs: { airfield: "prod", version: "1.0.0" } });

const needs: Needs[] = [{
  component: node("component", "acme/svc:Containerfile", { name: "acme/svc/Containerfile", attrs: { dir: ".", file: "Containerfile" } }), repository: "acme/svc", file: ".launchway/requirements.yaml",
  facts: [{ facet: "arch", value: "arm64", source: "declared", evidence: ".launchway/requirements.yaml" }, { facet: "memory", value: "1073741824", source: "declared" }, { facet: "port", value: "8080/tcp", source: "inferred", evidence: "EXPOSE 8080 in Containerfile" }],
  advice: [{ facet: "arch", inferred: "amd64", declared: "arm64", message: "arch: the file declares arm64; Launchway read amd64 (the pipeline builds it). The declaration stands." }],
}];

const calls: string[] = [];
function serve(order: object = { order: ["acme/lib", "acme/svc"], levels: [["acme/lib"], ["acme/svc"]], edges: [{ consumer: "acme/svc", provider: "acme/lib", via: ["go:example.test/libgo"], kinds: ["package"], versions: ["go:example.test/libgo@v1.2.0"] }] }) {
  calls.length = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const u = new URL(url, "http://x");
    calls.push((init?.method ?? "GET") + " " + u.pathname + u.search);
    const ok = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
    switch (u.pathname) {
      case "/api/v1/graph": return ok({ nodes: { repository: 2, package: 1 }, edges: 3, sources: [] });
      case "/api/v1/graph/search": return ok({ items: [repo], more: false });
      case "/api/v1/graph/neighbours":
        return u.searchParams.get("direction") === "downstream"
          ? ok({ start: repo, truncated: false, hits: [{ node: comp, depth: 1, path: [{ edge: contains, node: { kind: "component", key: comp.key } }] }] })
          : ok({ start: repo, truncated: u.searchParams.get("ref") === "repository:acme/svc" ? false : false, hits: [] });
      case "/api/v1/graph/path": return ok({ from: repo, to: lib, found: true, way: "affects", steps: [{ edge: contains, node: { kind: "component", key: comp.key } }, { edge: dep, node: { kind: "package", key: lib.key } }] });
      case "/api/v1/graph/impact": return ok({ start: repo, total: 2, by_kind: { component: 1, deployment: 1 }, repositories: ["acme/web"], truncated: true, truncated_reason: "depth",
        by_owner: [{ owner: "@apps-team", count: 2, items: [{ node: comp, depth: 1, owner: "@apps-team", path: [{ edge: contains, node: { kind: "component", key: comp.key } }] }] }],
        deployments: [{ deployment: deploy, airfield: "prod", digest: "sha256:abcdef0123456789", version: "1.0.0", exact: false, why: [{ edge: contains, node: { kind: "component", key: comp.key } }] }] });
      case "/api/v1/graph/order": return ok(order);
      case "/api/v1/graph/rebuild-set": return ok({ repository: "acme/svc", items: [{ repository: "acme/web", level: 1, why: [{ consumer: "acme/web", provider: "acme/svc", via: ["image"], kinds: ["image"] }] }] });
      case "/api/v1/graph/overrides": return init?.method === "POST" ? ok({ id: 9, kind: "ignore", from: "acme/svc", to: "acme/lib", reason: "r", created_at: 1 }, 201) : ok({ items: [{ id: 3, kind: "ignore", from: "acme/web", to: "acme/lib", reason: "web only needs its types", author: "ann", created_at: 1 }] });
      case "/api/v1/graph/needs": return ok({ items: needs, file: ".launchway/requirements.yaml" });
      case "/api/v1/graph/placement": return ok({ items: [{ component: needs[0].component, facts: needs[0].facts, fits: [], hosts_considered: 3, gaps: [{ facts: [], message: "needs arch arm64: nothing satisfies it" }] }] });
      case "/api/v1/graph/specification": return ok({ version: 1, kind: "launchway.machine-specification", architecture: "arm64" });
      case "/api/v1/graph/where": return ok({ start: lib, airfields: ["prod"], deployments: [{ deployment: deploy, airfield: "prod", digest: "sha256:abc", version: "1.0.0", exact: true, why: [] }] });
      case "/api/v1/repositories/acme/svc/requirements/proposal": return ok({ number: 12, branch: "launchway/requirements-1", title: "t" }, 201);
    }
    return ok({}, 404);
  }));
}

describe.each(["light", "dark"])("the software graph, %s theme", (theme) => {
  beforeEach(() => { document.documentElement.setAttribute("data-theme", theme); serve(); URL.createObjectURL = () => "blob:x"; });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  it("lists what is next to the focus as a table, expands a row and shows the path, and is accessible", async () => {
    const { container } = render(<main><RepositoryGraphPage path="acme/svc" /></main>);
    const table = await screen.findByRole("table", { name: /Nodes around repository:acme\/svc/ });
    const row = await within(table).findByRole("row", { name: /acme\/svc:\./ });
    expect(row).toBeTruthy();
    fireEvent.click(within(table).getByRole("button", { name: "Expand component acme/svc:." }));
    await waitFor(() => expect(calls.filter((c) => c.includes("neighbours") && c.includes("component"))).not.toHaveLength(0));
    fireEvent.click(within(table).getByRole("button", { name: "Show the path from the focus to component acme/svc:." }));
    expect(await screen.findByText(/A change to the focus reaches it/)).toBeTruthy();
    expect(await scan(container, { page: false })).toEqual([]);
  });

  it("draws a picture beside the table on request, and the table stays", async () => {
    const { container } = render(<main><RepositoryGraphPage path="acme/svc" /></main>);
    await screen.findByRole("table", { name: /Nodes around/ });
    fireEvent.click(screen.getByRole("button", { name: "Show as a picture" }));
    const pic = await screen.findByRole("img", { name: /Picture of the graph/ }, { timeout: 5000 });
    expect(pic.querySelectorAll(".sg-node").length).toBeGreaterThan(0);
    expect(screen.getByRole("table", { name: /Nodes around/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Zoom in" })).toBeTruthy();
    expect(await scan(container, { page: false })).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Hide the picture" }));
    expect(screen.queryByRole("img", { name: /Picture of the graph/ })).toBeNull();
  });

  it("filters by kind", async () => {
    render(<main><RepositoryGraphPage path="acme/svc" /></main>);
    await screen.findByRole("table", { name: /Nodes around/ });
    fireEvent.click(await screen.findByRole("checkbox", { name: "Component" }));
    expect(await screen.findByText(/2 of 2 nodes shown|1 of 2 nodes shown|nodes shown/)).toBeTruthy();
  });

  it("answers what a change affects, grouped by owner, with the way and an honest cut-off", async () => {
    const { container } = render(<main><RepositoryGraphPage path="acme/svc" /></main>);
    fireEvent.click(await screen.findByRole("button", { name: "What it affects" }));
    expect(await screen.findByText(/reaches 2 things in 1 other repository, and 1 running deployment/)).toBeTruthy();
    expect(screen.getByText(/Not the whole answer/)).toBeTruthy();
    expect(screen.getByRole("heading", { name: "@apps-team (2)" })).toBeTruthy();
    expect(screen.getAllByText("Why").length).toBeGreaterThan(0);
    expect(await scan(container, { page: false })).toEqual([]);
  });

  it("shows the build order as a list with the override, and refuses a correction without a reason", async () => {
    const { container } = render(<main><RepositoryGraphPage path="acme/svc" /></main>);
    fireEvent.click(await screen.findByRole("button", { name: "Build order" }));
    const list = await screen.findByRole("list", { name: "Build order" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(await screen.findByText(/web only needs its types/)).toBeTruthy();
    expect(await screen.findByRole("list", { name: "What must be built again, in order" })).toBeTruthy();
    const form = screen.getByRole("form", { name: "Add a correction" });
    const submit = within(form).getByRole("button", { name: "Add the correction" });
    expect((submit as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(form).getAllByRole("combobox")[1], { target: { value: "acme/svc" } });
    fireEvent.change(within(form).getAllByRole("combobox")[2], { target: { value: "acme/lib" } });
    expect((submit as HTMLButtonElement).disabled).toBe(true); // still no reason
    fireEvent.change(within(form).getByRole("textbox"), { target: { value: "svc only uses its test helpers" } });
    expect((submit as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(submit);
    await waitFor(() => expect(calls).toContain("POST /api/v1/graph/overrides"));
    expect(await scan(container, { page: false })).toEqual([]);
  });

  it("reports a cycle with its path", async () => {
    serve({ order: ["a", "b"], levels: [["a", "b"]], edges: [], cycles: [{ path: ["a", "b", "a"], members: ["a", "b"], suggestion: "if a does not need b, ignore it" }] });
    render(<main><RepositoryGraphPage path="acme/svc" /></main>);
    fireEvent.click(await screen.findByRole("button", { name: "Build order" }));
    expect((await screen.findByRole("alert")).textContent).toContain("a → b → a");
  });

  it("shows declared and inferred needs, the advice, and opens a pull request from the edit action", async () => {
    const { container } = render(<main><RepositoryGraphPage path="acme/svc" /></main>);
    fireEvent.click(await screen.findByRole("button", { name: "What it needs" }));
    const t = await screen.findByRole("table", { name: /What acme\/svc:Containerfile needs/ });
    expect(within(t).getAllByText("Declared in the file").length).toBe(2);
    expect(within(t).getByText("Inferred by Launchway")).toBeTruthy();
    expect(screen.getAllByText(/The declaration stands/).length).toBeGreaterThan(0);
    expect(await screen.findByText(/needs arch arm64: nothing satisfies it/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Edit the requirements file" }));
    const box = (await screen.findByRole("textbox", { name: "Requirements file" })) as HTMLTextAreaElement;
    expect(box.value).toContain("architectures: [arm64]");
    expect(box.value).toContain("memory: 1Gi");
    fireEvent.click(screen.getByRole("button", { name: "Open a pull request" }));
    expect(await screen.findByRole("link", { name: "pull request #12" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Make the specification" }));
    expect(await screen.findByRole("link", { name: "Download as JSON" })).toBeTruthy();
    expect(await scan(container, { page: false })).toEqual([]);
  });

  it("the page of everything: finds a node and shows where it runs", async () => {
    const { container } = render(<main><GraphPage /></main>);
    expect(await screen.findByText(/It holds 3 nodes and 3 relations/)).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox", { name: "Name contains" }), { target: { value: "svc" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(await screen.findByRole("button", { name: "Show repository acme/svc" }));
    fireEvent.click(await screen.findByRole("button", { name: "Where it runs" }));
    expect(await screen.findByText(/Running in 1 Airfield/)).toBeTruthy();
    expect(await scan(container, { page: false })).toEqual([]);
  });
});

describe("the layout", () => {
  it("lays 700 nodes out quickly, in columns by distance, and says how many it drew", () => {
    const nodes = [{ id: "f", kind: "repository", label: "focus" }];
    const edges = [];
    for (let i = 0; i < 700; i++) { nodes.push({ id: "n" + i, kind: i % 2 ? "package" : "component", label: "node " + i }); edges.push({ from: i < 20 ? "f" : "n" + (i % 20), to: "n" + i, kind: "depends-on" }); }
    const t0 = performance.now();
    const l = layout(nodes, edges, "f");
    expect(performance.now() - t0).toBeLessThan(500);
    expect(l.total).toBe(701);
    expect(l.shown).toBe(MAX_SHOWN);
    expect(new Set(l.nodes.map((n) => n.x)).size).toBeGreaterThan(1);
  });
  it("puts what the focus depends on to its left", () => {
    const l = layout([{ id: "a", kind: "package", label: "a" }, { id: "f", kind: "component", label: "f" }, { id: "b", kind: "component", label: "b" }],
      [{ from: "f", to: "a", kind: "depends-on" }, { from: "b", to: "f", kind: "depends-on" }], "f");
    const x = Object.fromEntries(l.nodes.map((n) => [n.id, n.x]));
    expect(x.b).toBeLessThan(x.f);
    expect(x.f).toBeLessThan(x.a);
  });
});

describe("the requirements file written from what the graph says", () => {
  it("writes one component at the top and several under components", () => {
    expect(requirementsYaml(needs)).toContain("architectures: [arm64]\nmemory: 1Gi");
    expect(requirementsYaml(needs)).toContain('ports:\n  - "8080/tcp"');
    const two = [needs[0], { ...needs[0], component: { ...needs[0].component, key: "x:y", attrs: { dir: "web", file: "web/Containerfile" } } }];
    expect(requirementsYaml(two)).toContain("components:\n  ");
    expect(requirementsYaml([])).toContain("version: 1");
  });
});
