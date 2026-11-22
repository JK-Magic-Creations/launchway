import { describe, expect, it } from "vitest";
import { me } from "../test/fixtures";
import { breadcrumbs, tabOn, locate, NO_FACTS, pageTitle, redirectFor, sectionOf, SECTIONS, visibleSections, visibleTabs, section, MOVED, type Facts } from "./model";

const facts = (over: Partial<Facts> = {}): Facts => ({ ...NO_FACTS, ...over });

describe("sections", () => {
  it("has the eleven areas, in the order of a change's journey", () => {
    expect(SECTIONS.map((s) => s.label)).toEqual(["Tower", "Code", "Work", "Pipelines", "Depot", "Launches", "Radar", "Changes", "Activity", "Library", "Settings"]);
  });
  it("groups them: Home, then build, then run and watch, then elsewhere", () => {
    expect(SECTIONS.map((s) => s.group)).toEqual([0, 1, 1, 1, 1, 2, 2, 2, 3, 3, 3]);
  });
  it("keeps all eleven visible, also for a new installation", () => {
    expect(visibleSections({ me: me(), facts: facts() })).toHaveLength(11);
  });
  it("gives every area a sentence for hover, the launcher and the Guide", () => {
    for (const s of SECTIONS) expect(s.promise.length, s.id).toBeGreaterThan(20);
  });
  it("shows the applications page only when an application has been seen", () => {
    const tabs = (n: number | null) => visibleTabs(section("deployments"), { me: me(), facts: facts({ applications: n }) }).map((t) => t.id);
    expect(tabs(null)).toEqual(["overview", "environments", "releases", "hosts", "machine-images"]);
    expect(tabs(2)).toContain("applications");
  });
  it("marks the pages that are not built yet", () => {
    expect(section("traffic").tabs.every((t) => t.soon)).toBe(true);
    expect(section("changes").tabs.every((t) => t.soon)).toBe(true);
    expect(section("activity").tabs.some((t) => t.soon)).toBe(false);
  });
  it("hides what a person may not use", () => {
    const tabs = (m: ReturnType<typeof me>) => visibleTabs(section("settings"), { me: m, facts: facts() }).map((t) => t.id);
    expect(tabs(me({ admin: false, runners: false }))).toEqual(["overview", "access", "approvals", "variables", "standards"]);
    expect(tabs(me())).toEqual(["overview", "runners", "connections", "access", "approvals", "rules", "variables", "standards", "words", "security", "audit", "config"]);
    // Importing repositories needs Git hosting on; without it the tab is not offered.
    expect(tabs(me({ hosting: true } as object))).toContain("import");
    expect(tabs(me({ hosting: false } as object))).not.toContain("import");
  });
  it("never lists a tab twice or without an address", () => {
    for (const s of SECTIONS) {
      const hrefs = s.tabs.map((t) => t.href);
      expect(new Set(hrefs).size).toBe(hrefs.length);
      for (const t of s.tabs) expect(t.href.startsWith("/")).toBe(true);
    }
  });
});

describe("where pages sit", () => {
  it("places every existing page in a section", () => {
    const paths: [string, string][] = [
      ["/", "home"], ["/projects", "projects"], ["/projects/x", "projects"], ["/projects/x/pipeline", "pipelines"], ["/projects/x/pipeline/edit", "pipelines"],
      ["/projects/x/tests", "pipelines"], ["/projects/x/security", "pipelines"], ["/projects/x/caches", "pipelines"], ["/playground", "projects"],
      ["/activity", "activity"], ["/activity/approvals", "activity"], ["/runs/abc", "activity"],
      ["/deployments", "deployments"], ["/deployments/environments", "deployments"], ["/deployments/releases", "deployments"], ["/deployments/releases/matrix", "deployments"], ["/deployments/releases/shop/1.0.0", "deployments"], ["/deployments/applications/a", "deployments"],
      ["/library/templates/lib/t", "library"], ["/depot", "images"], ["/depot/components", "images"], ["/depot/charts", "images"],
      ["/pipelines", "pipelines"], ["/traffic", "traffic"], ["/traffic/protection", "traffic"], ["/changes", "changes"], ["/changes/freezes", "changes"],
      ["/deployments/hosts", "deployments"], ["/code/files", "projects"], ["/kit", "settings"],
      ["/settings/runners", "settings"], ["/settings/access/keys", "settings"], ["/settings/rules", "settings"], ["/settings/audit", "settings"],
      ["/config", "settings"], ["/config/wizard/add-runner", "settings"], ["/config/projects/x", "settings"], ["/config/projects", "settings"], ["/config/projects/new", "settings"],
    ];
    for (const [p, s] of paths) expect(sectionOf(p), p).toBe(s);
  });
  it("tells a wizard from an object", () => {
    expect(locate("/config/wizard/add-runner")?.info.pattern).toBe("/config/wizard/:id");
    expect(locate("/config/environments/new")?.info.pattern).toBe("/config/:kind/new");
  });
  it("knows no page for a made-up address", () => {
    expect(locate("/nope")).toBeNull();
    expect(breadcrumbs("/nope")).toEqual([]);
  });
  it("ignores a trailing slash", () => { expect(sectionOf("/projects/")).toBe("projects"); });
});

describe("breadcrumbs", () => {
  it("links every step back except the page itself", () => {
    expect(breadcrumbs("/projects/shop-api/pipeline/edit")).toEqual([
      { label: "Pipelines", href: "/pipelines" }, { label: "shop-api", href: "/projects/shop-api/pipeline" }, { label: "Edit" },
    ]);
  });
  it("lets a page give its thing's real name", () => {
    expect(breadcrumbs("/runs/abcdef123456", "Run #12 of shop-api").at(-1)).toEqual({ label: "Run #12 of shop-api" });
  });
  it("is just the section on the section's own page", () => { expect(breadcrumbs("/projects")).toEqual([{ label: "Code" }]); });
  it("names the template's library", () => {
    expect(breadcrumbs("/library/templates/platform/go-service").map((c) => c.label)).toEqual(["Library", "Templates", "platform", "go-service"]);
  });
});

describe("trails: area, thing, sub-thing, each level a link but the last", () => {
  const trail = (path: string) => breadcrumbs(path).map((c) => [c.label, c.href]);
  it("gives every page of a repository the same first levels", () => {
    for (const sub of ["code", "commits", "branches", "tags", "settings", "graph", "mirrors", "pulls", "blame", "compare"]) {
      const t = trail("/repositories/acme/shop/" + sub);
      expect(t.slice(0, 3), sub).toEqual([["Code", "/projects"], ["Repositories", "/repositories"], ["acme/shop", "/repositories/acme/shop/code"]]);
      expect(t.length, sub).toBe(4);
      expect(t[3][1], sub).toBeUndefined();
    }
  });
  it("puts a commit under Commits, and a pull request under Pull requests", () => {
    expect(trail("/repositories/acme/shop/commit/abcdef1234")).toEqual([["Code", "/projects"], ["Repositories", "/repositories"], ["acme/shop", "/repositories/acme/shop/code"], ["Commits", "/repositories/acme/shop/commits"], ["abcdef12", undefined]]);
    expect(trail("/repositories/acme/shop/pulls/7").map((c) => c[0])).toEqual(["Code", "Repositories", "acme/shop", "Pull requests", "#7"]);
  });
  it("lists the repository's own address as a page of its own: settings", () => {
    expect(locate("/repositories/acme/shop/settings")?.info.title({ owner: "acme", name: "shop" })).toBe("Settings of acme/shop");
  });
});

describe("old addresses", () => {
  it("sends each of the old menu's addresses to its new place and keeps the query", () => {
    expect(redirectFor("/approvals")).toBe("/activity/approvals");
    expect(redirectFor("/library/images", "?digest=sha256:1")).toBe("/depot?digest=sha256:1");
    expect(redirectFor("/library/components", "?name=x")).toBe("/depot/components?name=x");
    expect(redirectFor("/components")).toBe("/depot/components");
    expect(redirectFor("/policy/")).toBe("/settings/rules");
  });
  it("keeps every old Images address working: it is the Depot now", () => {
    expect(redirectFor("/images")).toBe("/depot");
    expect(redirectFor("/images/", "?q=app")).toBe("/depot?q=app");
    expect(redirectFor("/images/components", "?name=x")).toBe("/depot/components?name=x");
    expect(redirectFor("/images/registry")).toBe("/depot/store");
    expect(redirectFor("/images/registry/shop", "?repository=api")).toBe("/depot/store/shop?repository=api");
    expect(redirectFor("/images/digest/sha256:ab")).toBe("/depot/digest/sha256:ab");
    expect(redirectFor("/images/charts")).toBe("/depot/charts");
  });
  it("makes the Depot one area with its sections as tabs, and a trail for each page", () => {
    expect(sectionOf("/depot/store")).toBe("images");
    expect(tabOn("/depot/components")).toBe("components");
    expect(tabOn("/depot/store/shop")).toBe("store");
    expect(section("images").tabs.map((t) => t.label)).toEqual(["Images", "Components", "Built-in store"]);
    expect(breadcrumbs("/depot/store/shop").map((c) => c.label)).toEqual(["Depot", "Built-in store", "shop"]);
    expect(breadcrumbs("/depot/digest/sha256:abcdef").map((c) => c.href)).toEqual(["/depot", "/depot", undefined]);
  });
  it("leaves current addresses alone", () => {
    expect(redirectFor("/projects")).toBeNull();
    expect(redirectFor("/runs/x")).toBeNull();
    expect(redirectFor("/depot")).toBeNull();
  });
  it("only moves to addresses that have a page", () => {
    for (const [, to] of MOVED) expect(locate(to), to).not.toBeNull();
  });
});

describe("titles", () => {
  it("names the page", () => {
    expect(pageTitle("/projects/shop-api")).toBe("shop-api · Launchway");
    expect(pageTitle("/nope")).toBe("Launchway");
  });
  it("reads Sorties, the Sortie, then Launch on a Launch page, and the Sortie alone otherwise", () => {
    history.replaceState(null, "", "/runs/run_abcdef123?launch=dep_1&tab=logs");
    expect(breadcrumbs("/runs/run_abcdef123").map((c) => c.label)).toEqual(["Activity", "Sorties", "Sortie run_abcd", "Launch"]);
    expect(breadcrumbs("/runs/run_abcdef123")[2].href).toBe("/runs/run_abcdef123");
    history.replaceState(null, "", "/runs/run_abcdef123");
    expect(breadcrumbs("/runs/run_abcdef123").map((c) => c.label)).toEqual(["Activity", "Sorties", "Sortie run_abcd"]);
  });
});
