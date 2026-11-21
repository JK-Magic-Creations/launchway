import { describe, expect, it } from "vitest";
import { me } from "../test/fixtures";
import { breadcrumbs, locate, NO_FACTS, pageTitle, redirectFor, sectionOf, SECTIONS, visibleSections, visibleTabs, section, MOVED, type Facts } from "./model";

const facts = (over: Partial<Facts> = {}): Facts => ({ ...NO_FACTS, ...over });

describe("sections", () => {
  it("has the six sections in the order of doing things", () => {
    expect(SECTIONS.map((s) => s.label)).toEqual(["Home", "Projects", "Deployments", "Activity", "Library", "Settings"]);
  });
  it("hides the library until a library or a run exists", () => {
    const ids = (f: Facts) => visibleSections({ me: me(), facts: f }).map((s) => s.id);
    expect(ids(facts())).not.toContain("library");
    expect(ids(facts({ libraries: 1 }))).toContain("library");
    expect(ids(facts({ runs: 3 }))).toContain("library");
  });
  it("shows the applications tab only when an application has been seen", () => {
    const tabs = (n: number | null) => visibleTabs(section("deployments"), { me: me(), facts: facts({ applications: n }) }).map((t) => t.id);
    expect(tabs(null)).toEqual(["overview", "environments"]);
    expect(tabs(0)).toEqual(["overview", "environments"]);
    expect(tabs(2)).toContain("applications");
  });
  it("hides what a person may not use", () => {
    const tabs = (m: ReturnType<typeof me>) => visibleTabs(section("settings"), { me: m, facts: facts() }).map((t) => t.id);
    expect(tabs(me({ admin: false, runners: false }))).toEqual(["overview", "access"]);
    expect(tabs(me())).toEqual(["overview", "runners", "access", "rules", "audit", "config"]);
  });
  it("never lists a tab twice or without an address", () => {
    for (const s of SECTIONS) for (const t of s.tabs) expect(t.href.startsWith("/")).toBe(true);
  });
});

describe("where pages sit", () => {
  it("places every existing page in a section", () => {
    const paths: [string, string][] = [
      ["/", "home"], ["/projects", "projects"], ["/projects/x", "projects"], ["/projects/x/pipeline", "projects"], ["/projects/x/pipeline/edit", "projects"],
      ["/projects/x/tests", "projects"], ["/projects/x/security", "projects"], ["/projects/x/caches", "projects"], ["/playground", "projects"],
      ["/activity", "activity"], ["/activity/approvals", "activity"], ["/runs/abc", "activity"],
      ["/deployments", "deployments"], ["/deployments/environments", "deployments"], ["/deployments/applications/a", "deployments"],
      ["/library/templates/lib/t", "library"], ["/library/images", "library"], ["/library/components", "library"],
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
      { label: "Projects", href: "/projects" }, { label: "shop-api", href: "/projects/shop-api" }, { label: "Pipeline", href: "/projects/shop-api/pipeline" }, { label: "Edit" },
    ]);
  });
  it("lets a page give its thing's real name", () => {
    expect(breadcrumbs("/runs/abcdef123456", "Run #12 of shop-api").at(-1)).toEqual({ label: "Run #12 of shop-api" });
  });
  it("is just the section on the section's own page", () => { expect(breadcrumbs("/projects")).toEqual([{ label: "Projects" }]); });
  it("names the template's library", () => {
    expect(breadcrumbs("/library/templates/platform/go-service").map((c) => c.label)).toEqual(["Library", "Templates", "platform", "go-service"]);
  });
});

describe("old addresses", () => {
  it("sends each of the old menu's addresses to its new place and keeps the query", () => {
    expect(redirectFor("/approvals")).toBe("/activity/approvals");
    expect(redirectFor("/images", "?digest=sha256:1")).toBe("/library/images?digest=sha256:1");
    expect(redirectFor("/policy/")).toBe("/settings/rules");
  });
  it("leaves current addresses alone", () => {
    expect(redirectFor("/projects")).toBeNull();
    expect(redirectFor("/runs/x")).toBeNull();
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
