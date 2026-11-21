import { describe, expect, it } from "vitest";
import { me } from "../test/fixtures";
import { breadcrumbs, locate, NO_FACTS, pageTitle, redirectFor, sectionOf, SECTIONS, visibleSections, visibleTabs, section, MOVED, type Facts } from "./model";

const facts = (over: Partial<Facts> = {}): Facts => ({ ...NO_FACTS, ...over });

describe("sections", () => {
  it("has the ten areas, in the order of a change's journey", () => {
    expect(SECTIONS.map((s) => s.label)).toEqual(["Home", "Code", "Pipelines", "Images", "Deployments", "Traffic", "Changes", "Activity", "Library", "Settings"]);
  });
  it("groups them: Home, then build, then run and watch, then elsewhere", () => {
    expect(SECTIONS.map((s) => s.group)).toEqual([0, 1, 1, 1, 2, 2, 2, 3, 3, 3]);
  });
  it("keeps all ten visible, also for a new installation", () => {
    expect(visibleSections({ me: me(), facts: facts() })).toHaveLength(10);
  });
  it("gives every area a sentence for hover, the launcher and the Guide", () => {
    for (const s of SECTIONS) expect(s.promise.length, s.id).toBeGreaterThan(20);
  });
  it("shows the applications page only when an application has been seen", () => {
    const tabs = (n: number | null) => visibleTabs(section("deployments"), { me: me(), facts: facts({ applications: n }) }).map((t) => t.id);
    expect(tabs(null)).toEqual(["overview", "environments", "hosts", "machine-images"]);
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
    expect(tabs(me())).toEqual(["overview", "runners", "access", "approvals", "rules", "variables", "standards", "words", "security", "audit", "config"]);
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
      ["/deployments", "deployments"], ["/deployments/environments", "deployments"], ["/deployments/applications/a", "deployments"],
      ["/library/templates/lib/t", "library"], ["/images", "images"], ["/images/components", "images"], ["/images/charts", "images"],
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

describe("old addresses", () => {
  it("sends each of the old menu's addresses to its new place and keeps the query", () => {
    expect(redirectFor("/approvals")).toBe("/activity/approvals");
    expect(redirectFor("/library/images", "?digest=sha256:1")).toBe("/images?digest=sha256:1");
    expect(redirectFor("/library/components", "?name=x")).toBe("/images/components?name=x");
    expect(redirectFor("/components")).toBe("/images/components");
    expect(redirectFor("/policy/")).toBe("/settings/rules");
  });
  it("leaves current addresses alone", () => {
    expect(redirectFor("/projects")).toBeNull();
    expect(redirectFor("/runs/x")).toBeNull();
    expect(redirectFor("/images")).toBeNull();
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
});
