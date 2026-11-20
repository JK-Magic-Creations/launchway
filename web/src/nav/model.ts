// The navigation model: six sections, the tabs inside them, which are
// shown, where every page sits, and where the old addresses went. It is
// plain data and pure functions, so it is tested without a browser.

import type { Me } from "../api";
import { match } from "../router";

export type SectionId = "home" | "projects" | "deployments" | "activity" | "library" | "settings";

/** What the navigation needs to know about the installation to decide what to
 * show. A number is null until it is known; unknown counts as nothing to show. */
export interface Facts {
  projects: number | null;
  runs: number | null;
  runners: number | null;
  environments: number | null;
  applications: number | null;
  libraries: number | null;
  approvals: number;
}

export const NO_FACTS: Facts = { projects: null, runs: null, runners: null, environments: null, applications: null, libraries: null, approvals: 0 };

export interface Ctx { me: Me; facts: Facts }

export interface Tab { id: string; label: string; href: string; show?: (c: Ctx) => boolean }

export interface Section {
  id: SectionId;
  label: string;
  href: string;
  /** One sentence: what the section is for. Shown on the section's own page and in the menu's tooltip. */
  promise: string;
  tabs: Tab[];
  show?: (c: Ctx) => boolean;
}

const has = (n: number | null) => (n ?? 0) > 0;

export const SECTIONS: Section[] = [
  { id: "home", label: "Home", href: "/", promise: "What should I do next?", tabs: [] },
  {
    id: "projects", label: "Projects", href: "/projects", promise: "Your applications.",
    tabs: [],
  },
  {
    id: "deployments", label: "Deployments", href: "/deployments", promise: "What is running, where, and is it healthy?",
    tabs: [
      { id: "overview", label: "Overview", href: "/deployments" },
      { id: "environments", label: "Environments", href: "/deployments/environments" },
      // Appears when a deploy runner has seen an Argo CD application.
      { id: "applications", label: "Applications", href: "/deployments/applications", show: ({ facts }) => has(facts.applications) },
    ],
  },
  {
    id: "activity", label: "Activity", href: "/activity", promise: "What happened, and what is waiting for me?",
    tabs: [
      { id: "runs", label: "Runs", href: "/activity" },
      { id: "approvals", label: "Approvals", href: "/activity/approvals" },
    ],
  },
  {
    id: "library", label: "Library", href: "/library", promise: "The building blocks, to look things up.",
    // Quiet until there is something in it: a library the server reads, or a run that could have built something.
    show: ({ facts }) => has(facts.libraries) || has(facts.runs),
    tabs: [
      { id: "templates", label: "Templates", href: "/library/templates", show: ({ facts }) => has(facts.libraries) },
      { id: "images", label: "Images", href: "/library/images" },
      { id: "components", label: "Components", href: "/library/components" },
    ],
  },
  {
    id: "settings", label: "Settings", href: "/settings", promise: "Set up once.",
    tabs: [
      { id: "overview", label: "Overview", href: "/settings" },
      { id: "runners", label: "Runners", href: "/settings/runners", show: ({ me }) => me.runners },
      { id: "access", label: "People and access", href: "/settings/access" },
      { id: "rules", label: "Rules", href: "/settings/rules", show: ({ me }) => me.runners },
      { id: "audit", label: "Audit trail", href: "/settings/audit", show: ({ me }) => me.admin },
      { id: "config", label: "Configuration", href: "/config", show: ({ me }) => me.admin },
    ],
  },
];

export function visibleSections(c: Ctx): Section[] {
  return SECTIONS.filter((s) => !s.show || s.show(c));
}

export function visibleTabs(s: Section, c: Ctx): Tab[] {
  return s.tabs.filter((t) => !t.show || t.show(c));
}

// ---- where pages sit ----

export interface Crumb { label: string; href?: string }

export interface PageInfo {
  /** The pattern the page is routed by. */
  pattern: string;
  section: SectionId;
  tab?: string;
  /** The window title. */
  title: (p: Record<string, string>) => string;
  /** The trail above the page title, after the section: tabs and things. The last crumb is the page itself. */
  trail: (p: Record<string, string>) => Crumb[];
}

const enc = encodeURIComponent;

function page(pattern: string, section: SectionId, tab: string | undefined, title: (p: Record<string, string>) => string,
  trail: (p: Record<string, string>) => Crumb[]): PageInfo {
  return { pattern, section, tab, title, trail };
}

/** Every page, most specific first. The pattern is the router's. */
export const PAGES: PageInfo[] = [
  page("/", "home", undefined, () => "Home", () => []),

  page("/projects", "projects", undefined, () => "Projects", () => []),
  page("/projects/:name", "projects", undefined, (p) => p.name, (p) => [{ label: p.name }]),
  page("/projects/:name/pipeline", "projects", undefined, (p) => `Pipeline of ${p.name}`, (p) => [{ label: p.name, href: `/projects/${enc(p.name)}` }, { label: "Pipeline" }]),
  page("/projects/:name/pipeline/edit", "projects", undefined, (p) => `Edit the pipeline of ${p.name}`,
    (p) => [{ label: p.name, href: `/projects/${enc(p.name)}` }, { label: "Pipeline", href: `/projects/${enc(p.name)}/pipeline` }, { label: "Edit" }]),
  page("/projects/:name/tests", "projects", undefined, (p) => `Tests of ${p.name}`, (p) => [{ label: p.name, href: `/projects/${enc(p.name)}` }, { label: "Tests and coverage" }]),
  page("/projects/:name/security", "projects", undefined, (p) => `Security of ${p.name}`, (p) => [{ label: p.name, href: `/projects/${enc(p.name)}` }, { label: "Security" }]),
  page("/playground", "projects", undefined, () => "Try a pipeline", () => [{ label: "Try a pipeline without saving" }]),

  page("/deployments", "deployments", "overview", () => "Deployments", () => []),
  page("/deployments/environments", "deployments", "environments", () => "Environments", () => [{ label: "Environments" }]),
  page("/deployments/environments/:name", "deployments", "environments", (p) => `Environment ${p.name}`,
    (p) => [{ label: "Environments", href: "/deployments/environments" }, { label: p.name }]),
  page("/deployments/applications", "deployments", "applications", () => "Applications", () => [{ label: "Applications" }]),
  page("/deployments/applications/:name", "deployments", "applications", (p) => `Application ${p.name}`,
    (p) => [{ label: "Applications", href: "/deployments/applications" }, { label: p.name }]),

  page("/activity", "activity", "runs", () => "Runs", () => [{ label: "Runs" }]),
  page("/activity/approvals", "activity", "approvals", () => "Approvals", () => [{ label: "Approvals" }]),
  page("/runs/:id", "activity", "runs", (p) => `Run ${p.id}`, (p) => [{ label: "Runs", href: "/activity" }, { label: `Run ${p.id.slice(0, 8)}` }]),

  page("/library", "library", undefined, () => "Library", () => []),
  page("/library/templates", "library", "templates", () => "Templates", () => [{ label: "Templates" }]),
  page("/library/templates/:library/:name", "library", "templates", (p) => `Template ${p.name}`,
    (p) => [{ label: "Templates", href: "/library/templates" }, { label: p.library }, { label: p.name }]),
  page("/library/images", "library", "images", () => "Images", () => [{ label: "Images" }]),
  page("/library/components", "library", "components", () => "Components", () => [{ label: "Components" }]),

  page("/settings", "settings", "overview", () => "Settings", () => []),
  page("/settings/runners", "settings", "runners", () => "Runners", () => [{ label: "Runners" }]),
  page("/settings/access", "settings", "access", () => "People and access", () => [{ label: "People and access" }]),
  page("/settings/access/keys", "settings", "access", () => "Keys for scripts",
    () => [{ label: "People and access", href: "/settings/access" }, { label: "Keys for scripts" }]),
  page("/settings/access/agents", "settings", "access", () => "Connect an agent",
    () => [{ label: "People and access", href: "/settings/access" }, { label: "Connect an agent" }]),
  page("/settings/rules", "settings", "rules", () => "Rules", () => [{ label: "Rules" }]),
  page("/settings/audit", "settings", "audit", () => "Audit trail", () => [{ label: "Audit trail" }]),

  page("/config", "settings", "config", () => "Configuration", () => [{ label: "Configuration" }]),
  page("/config/wizard/:id", "settings", "config", (p) => `Set-up: ${p.id.replace(/-/g, " ")}`,
    (p) => [{ label: "Configuration", href: "/config" }, { label: p.id.replace(/-/g, " ") }]),
  page("/config/:kind/new", "settings", "config", (p) => `New ${p.kind}`, (p) => [{ label: "Configuration", href: "/config" }, { label: p.kind, href: `/config/${enc(p.kind)}` }, { label: "New" }]),
  page("/config/:kind/:name", "settings", "config", (p) => p.name, (p) => [{ label: "Configuration", href: "/config" }, { label: p.kind, href: `/config/${enc(p.kind)}` }, { label: p.name }]),
  page("/config/:kind", "settings", "config", (p) => p.kind, (p) => [{ label: "Configuration", href: "/config" }, { label: p.kind }]),
];

export interface Located { info: PageInfo; params: Record<string, string> }

/** Finds the page for an address. Order matters where a pattern could also
 * match: /config/wizard/x before /config/:kind/:name, /projects/:name/pipeline/edit first. */
export function locate(path: string): Located | null {
  const clean = path.length > 1 ? path.replace(/\/+$/, "") : path;
  for (const info of PAGES) {
    const params = match(info.pattern, clean);
    if (params) return { info, params };
  }
  return null;
}

export function section(id: SectionId): Section {
  return SECTIONS.find((s) => s.id === id)!;
}

/** The trail for a page: the section, then the page's own crumbs. A last crumb that
 * stands in for the page has no link. label overrides the last crumb once a page knows its thing's real name. */
export function breadcrumbs(path: string, label?: string): Crumb[] {
  const at = locate(path);
  if (!at) return [];
  const s = section(at.info.section);
  const trail = at.info.trail(at.params);
  if (trail.length === 0) return [{ label: s.label }];
  const out: Crumb[] = [{ label: s.label, href: s.href }, ...trail];
  const last = out[out.length - 1];
  out[out.length - 1] = { label: label ?? last.label };
  return out;
}

export function pageTitle(path: string): string {
  const at = locate(path);
  return at ? `${at.info.title(at.params)} · Launchway` : "Launchway";
}

// ---- the old addresses ----

/** Where each address of the flat menu moved. The old address keeps working: it is replaced, query kept. */
export const MOVED: [from: string, to: string][] = [
  ["/approvals", "/activity/approvals"],
  ["/environments", "/deployments/environments"],
  ["/runners", "/settings/runners"],
  ["/tokens", "/settings/access/keys"],
  ["/agents", "/settings/access/agents"],
  ["/audit", "/settings/audit"],
  ["/policy", "/settings/rules"],
  ["/components", "/library/components"],
  ["/images", "/library/images"],
];

/** The new address for an old one, or null if the address is not an old one. */
export function redirectFor(path: string, search = ""): string | null {
  const clean = path.length > 1 ? path.replace(/\/+$/, "") : path;
  for (const [from, to] of MOVED) if (clean === from) return to + search;
  return null;
}

/** The section a path belongs to; none for an address no page owns. */
export function sectionOf(path: string): SectionId | null {
  return locate(path)?.info.section ?? null;
}

/** Whether a tab is the one shown for the path. */
export function tabOn(path: string): string | undefined {
  return locate(path)?.info.tab;
}
