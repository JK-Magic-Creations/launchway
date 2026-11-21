// The navigation model: six sections, the tabs inside them, which are
// shown, where every page sits, and where the old addresses went. It is
// plain data and pure functions, so it is tested without a browser.

import type { Me } from "../api";
import { match } from "../router";
import type { IconName } from "../kit/Icon";
import { comingTitle } from "../shell/comingData";
import { terms } from "../terms";

/** The words in effect, filled into a sentence: {Hold} is what this installation calls a freeze. */
const say = (t: string) => terms().say(t);

export type SectionId = "home" | "projects" | "pipelines" | "images" | "deployments" | "traffic" | "changes" | "activity" | "library" | "settings";

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

export interface Tab {
  id: string; label: string; href: string; show?: (c: Ctx) => boolean;
  icon?: IconName;
  /** The plain name of a page whose label is one of the themed words, so a search for it still finds the page. */
  aka?: string;
  /** The page does not exist yet: it is an honest "coming" page that says what it will do. */
  soon?: boolean;
}

export interface Section {
  id: SectionId;
  label: string;
  href: string;
  /** The icon in the launcher and on the phone's bottom bar. */
  icon: IconName;
  /** The group on the top route: groups are separated by a thin divider. */
  group: 0 | 1 | 2 | 3;
  /** One sentence: what the area is for. Shown on hover, in the launcher and in the Guide. */
  promise: string;
  /** The pages inside the area, shown as the left column. An area with none has no left column. */
  tabs: Tab[];
  /** The word under the area's name in the left column. */
  side?: string;
  show?: (c: Ctx) => boolean;
}

const has = (n: number | null) => (n ?? 0) > 0;

/** The ten areas, in the order of a change's journey, then the places visited less often (docs 42, section 17). */
export const SECTIONS: Section[] = [
  { id: "home", label: "Home", href: "/", icon: "home", group: 0, promise: "What needs you now, the changes on their way, and a map of everything Launchway holds.", tabs: [] },
  { id: "projects", label: "Code", href: "/projects", icon: "code", group: 1, promise: "Repositories and projects: files, commits, branches and reviews.", tabs: [] },
  { id: "pipelines", label: "Pipelines", href: "/pipelines", icon: "graph", group: 1, promise: "The steps that build, test and release your software, and how their runs went.", tabs: [] },
  { id: "images", label: "Images", href: "/images", icon: "package", group: 1, promise: "Container images, Helm charts and files your pipelines build, with what is in them.", tabs: [] },
  {
    id: "deployments", label: "Deployments", href: "/deployments", icon: "deploy", group: 2, side: "What is running, and where.",
    promise: "What is running where: applications, environments, hosts and machine images.",
    tabs: [
      { id: "overview", label: "Overview", href: "/deployments", icon: "deploy" },
      { id: "environments", label: "Environments", href: "/deployments/environments", icon: "list" },
      // Appears when a deploy runner has seen an Argo CD application.
      { id: "applications", label: "Applications", href: "/deployments/applications", icon: "graph", show: ({ facts }) => has(facts.applications) },
      { id: "hosts", label: "Hosts", href: "/deployments/hosts", icon: "server", soon: true },
      { id: "machine-images", label: "Machine images", href: "/deployments/machine-images", icon: "package", soon: true },
    ],
  },
  {
    id: "traffic", label: "Traffic", href: "/traffic", icon: "flow", group: 2, side: "How requests flow, and how they are protected.",
    promise: "How requests flow between services, and what protects and watches them.",
    tabs: [
      { id: "map", label: "Traffic map", href: "/traffic", icon: "flow", soon: true },
      { id: "protection", label: "Protection", href: "/traffic/protection", icon: "shield", soon: true },
      { id: "reliability", label: "Reliability", href: "/traffic/reliability", icon: "clock", soon: true },
      { id: "toggles", label: "Toggles", href: "/traffic/toggles", icon: "bolt", soon: true },
    ],
  },
  {
    id: "changes", label: "Changes", href: "/changes", icon: "calendar", group: 2, side: "Planned, approved and done.",
    get promise() { return say("Change management: what is changing, when, why, who gave {clearance} and how it went."); },
    tabs: [
      { id: "calendar", label: "Calendar", href: "/changes", icon: "calendar", soon: true },
      { id: "records", label: "Change records", href: "/changes/records", icon: "list", soon: true },
      { id: "freezes", get label() { return say("{Hold} windows"); }, href: "/changes/freezes", icon: "freeze", aka: "Freeze windows", soon: true },
      { id: "rules", get label() { return say("{Clearance} rules"); }, href: "/changes/rules", icon: "gate", aka: "Approval rules", soon: true },
      { id: "tickets", label: "Ticket system", href: "/changes/tickets", icon: "ticket", soon: true },
    ],
  },
  {
    id: "activity", label: "Activity", href: "/activity", icon: "activity", group: 3, side: "Runs and decisions.",
    promise: "Every run, and everything waiting for a person to decide.",
    tabs: [
      { id: "runs", label: "Runs", href: "/activity", icon: "activity" },
      { id: "approvals", label: "Waiting for a decision", href: "/activity/approvals", icon: "gate" },
    ],
  },
  {
    id: "library", label: "Library", href: "/library", icon: "library", group: 3, side: "Templates for pipelines.",
    promise: "Ready-made templates and steps for pipelines.",
    tabs: [
      { id: "starters", label: "Starters", href: "/library/starters", icon: "package" },
      { id: "templates", label: "Templates", href: "/library/templates", icon: "graph", show: ({ facts }) => has(facts.libraries) },
    ],
  },
  {
    id: "settings", label: "Settings", href: "/settings", icon: "settings", group: 3, side: "Set up once, then rarely.",
    get promise() { return say("People, runners, rules, signal sources, configuration, the words Launchway uses and the {logbook}."); },
    tabs: [
      { id: "overview", label: "Overview", href: "/settings", icon: "settings" },
      { id: "runners", label: "Runners", href: "/settings/runners", icon: "server", show: ({ me }) => me.runners },
      { id: "access", label: "People and access", href: "/settings/access", icon: "users" },
      { id: "approvals", get label() { return say("{Clearances}"); }, href: "/settings/approvals", icon: "gate", aka: "Approvals" },
      { id: "rules", label: "Rules", href: "/settings/rules", icon: "shield", show: ({ me }) => me.runners },
      { id: "variables", label: "Variables", href: "/settings/variables", icon: "list" },
      { id: "import", label: "Import repositories", href: "/repositories/import", icon: "code", show: ({ me }) => me.admin && (me as Me & { hosting?: boolean }).hosting === true },
      { id: "words", label: "Words", href: "/settings/words", icon: "info", show: ({ me }) => me.admin },
      { id: "security", label: "Security", href: "/settings/security", icon: "shield", show: ({ me }) => me.admin },
      { id: "audit", get label() { return say("{Logbook}"); }, href: "/settings/audit", icon: "clock", aka: "Audit trail", show: ({ me }) => me.admin },
      { id: "config", label: "Configuration", href: "/config", icon: "globe", show: ({ me }) => me.admin },
    ],
  },
];

/** The areas shown. All ten are always there: the owner asked for Traffic and Changes by name, and an
 * area whose features are not built yet says so on its own page instead of vanishing (docs 42, section 17). */
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

/** What a configuration kind is called in a trail: the level words where the kind is a level. */
function kindWord(kind: string): string {
  const t = terms();
  switch (kind) {
    case "projects": case "project": return t.plural("project");
    case "units": case "unit": return t.plural("unit");
    case "organisation": case "organization": return t.label("organization");
    case "installation": return `${t.label("installation")} settings`;
    default: return kind;
  }
}

function page(pattern: string, section: SectionId, tab: string | undefined, title: (p: Record<string, string>) => string,
  trail: (p: Record<string, string>) => Crumb[]): PageInfo {
  return { pattern, section, tab, title, trail };
}

/** Every page, most specific first. The pattern is the router's. */
export const PAGES: PageInfo[] = [
  page("/", "home", undefined, () => "Home", () => []),

  page("/projects", "projects", undefined, () => "Code", () => []),
  page("/code/:page", "projects", undefined, (p) => comingTitle("/code/" + p.page), (p) => [{ label: comingTitle("/code/" + p.page) }]),
  page("/pipelines", "pipelines", undefined, () => "Pipelines", () => []),
  page("/projects/:name", "projects", undefined, (p) => p.name, (p) => [{ label: p.name }]),
  page("/projects/:name/pipeline", "pipelines", undefined, (p) => `Pipeline of ${p.name}`, (p) => [{ label: p.name }]),
  page("/projects/:name/pipeline/edit", "pipelines", undefined, (p) => `Edit the pipeline of ${p.name}`,
    (p) => [{ label: p.name, href: `/projects/${enc(p.name)}/pipeline` }, { label: "Edit" }]),
  page("/projects/:name/tests", "pipelines", undefined, (p) => `Tests of ${p.name}`, (p) => [{ label: p.name, href: `/projects/${enc(p.name)}/pipeline` }, { label: "Tests and coverage" }]),
  page("/projects/:name/security", "pipelines", undefined, (p) => `Security of ${p.name}`, (p) => [{ label: p.name, href: `/projects/${enc(p.name)}/pipeline` }, { label: "Security" }]),
  page("/projects/:name/caches", "pipelines", undefined, (p) => `Caches of ${p.name}`, (p) => [{ label: p.name, href: `/projects/${enc(p.name)}/pipeline` }, { label: "Build caches" }]),
  page("/projects/:name/variables", "pipelines", undefined, (p) => `Variables of ${p.name}`, (p) => [{ label: p.name, href: `/projects/${enc(p.name)}/pipeline` }, { label: "Variables" }]),
  page("/setup/guide", "settings", "guide", () => "Setup guide", () => [{ label: "Setup guide" }]),
  page("/repositories/import", "settings", "import", () => "Import repositories", () => [{ label: "Import repositories" }]),
  page("/repositories/:owner/:name", "projects", undefined, (p) => p.owner + "/" + p.name, (p) => [{ label: p.owner + "/" + p.name }]),
  page("/repositories/:owner/:name/mirrors", "projects", undefined, (p) => `Mirrors of ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}` }, { label: "Mirrors" }]),
  page("/repositories/:owner/:name/dependencies", "projects", undefined, (p) => `Dependencies of ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}` }, { label: "Dependencies" }]),
  page("/repositories/:owner/:name/updates", "projects", undefined, (p) => `Dependency updates of ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}` }, { label: "Dependency updates" }]),
  page("/repositories/:owner/:name/history", "projects", undefined, (p) => `Imported history of ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}` }, { label: "Imported history" }]),
  page("/repositories/:owner/:name/pulls", "projects", undefined, (p) => `Pull requests of ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}` }, { label: "Pull requests" }]),
  page("/repositories/:owner/:name/pulls/new", "projects", undefined, (p) => `New pull request in ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}` }, { label: "Pull requests", href: `/repositories/${enc(p.owner)}/${enc(p.name)}/pulls` }, { label: "New" }]),
  page("/repositories/:owner/:name/pulls/:number", "projects", undefined, (p) => `Pull request #${p.number} in ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}` }, { label: "Pull requests", href: `/repositories/${enc(p.owner)}/${enc(p.name)}/pulls` }, { label: `#${p.number}` }]),
  page("/repositories/:owner/:name/:view", "projects", undefined, (p) => `${p.view[0].toUpperCase() + p.view.slice(1)}: ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}/code` }, { label: p.view[0].toUpperCase() + p.view.slice(1) }]),
  page("/repositories/:owner/:name/commit/:sha", "projects", undefined, (p) => `Commit ${p.sha.slice(0, 8)}: ${p.owner}/${p.name}`,
    (p) => [{ label: p.owner + "/" + p.name, href: `/repositories/${enc(p.owner)}/${enc(p.name)}/code` }, { label: p.sha.slice(0, 8) }]),
  page("/playground", "projects", undefined, () => "Try a pipeline", () => [{ label: "Try a pipeline without saving" }]),

  page("/deployments", "deployments", "overview", () => "Deployments", () => []),
  page("/deployments/environments", "deployments", "environments", () => "Environments", () => [{ label: "Environments" }]),
  page("/deployments/environments/:name", "deployments", "environments", (p) => `Environment ${p.name}`,
    (p) => [{ label: "Environments", href: "/deployments/environments" }, { label: p.name }]),
  page("/deployments/hosts", "deployments", "hosts", () => "Hosts", () => [{ label: "Hosts" }]),
  page("/deployments/machine-images", "deployments", "machine-images", () => "Machine images", () => [{ label: "Machine images" }]),
  page("/deployments/applications", "deployments", "applications", () => "Applications", () => [{ label: "Applications" }]),
  page("/deployments/applications/:name", "deployments", "applications", (p) => `Application ${p.name}`,
    (p) => [{ label: "Applications", href: "/deployments/applications" }, { label: p.name }]),

  page("/repositories", "projects", undefined, () => "Repositories", () => [{ label: "Repositories" }]),
  page("/owners/:owner", "projects", undefined, (p) => `Owner ${p.owner}`,
    (p) => [{ label: "Repositories", href: "/repositories" }, { label: p.owner }]),

  page("/activity", "activity", "runs", () => "Runs", () => [{ label: "Runs" }]),
  page("/activity/approvals", "activity", "approvals", () => "Waiting for a decision", () => [{ label: "Waiting for a decision" }]),
  page("/runs/:id", "activity", "runs", (p) => `Run ${p.id}`, (p) => [{ label: "Runs", href: "/activity" }, { label: `Run ${p.id.slice(0, 8)}` }]),

  page("/images", "images", undefined, () => "Images", () => []),
  page("/images/components", "images", undefined, () => "Components", () => [{ label: "Components" }]),
  page("/images/digest/:digest", "images", undefined, (p) => `Image ${p.digest.slice(0, 19)}`, (p) => [{ label: "Image " + p.digest.slice(0, 19) }]),
  page("/images/:page", "images", undefined, (p) => comingTitle("/images/" + p.page), (p) => [{ label: comingTitle("/images/" + p.page) }]),

  page("/traffic", "traffic", "map", () => "Traffic map", () => []),
  page("/traffic/:page", "traffic", undefined, (p) => comingTitle("/traffic/" + p.page), (p) => [{ label: comingTitle("/traffic/" + p.page) }]),
  page("/changes", "changes", "calendar", () => "Calendar", () => []),
  page("/changes/:page", "changes", undefined, (p) => comingTitle("/changes/" + p.page), (p) => [{ label: comingTitle("/changes/" + p.page) }]),

  page("/library", "library", undefined, () => "Library", () => []),
  page("/library/starters", "library", "starters", () => "Starters", () => [{ label: "Starters" }]),
  page("/library/templates", "library", "templates", () => "Templates", () => [{ label: "Templates" }]),
  page("/library/templates/:library/:name", "library", "templates", (p) => `Template ${p.name}`,
    (p) => [{ label: "Templates", href: "/library/templates" }, { label: p.library }, { label: p.name }]),

  // The Learn area is not an eleventh area: it is help, reached from the top bar, the Guide and the pages' own links.
  page("/learn", "home", undefined, () => "Learn", () => []),
  page("/learn/:a", "home", undefined, () => "Learn", () => []),
  page("/learn/:a/:b", "home", undefined, () => "Learn", () => []),

  page("/kit", "settings", undefined, () => "Design kit", () => [{ label: "Design kit" }]),

  page("/settings", "settings", "overview", () => "Settings", () => []),
  page("/settings/runners", "settings", "runners", () => "Runners", () => [{ label: "Runners" }]),
  page("/settings/access", "settings", "access", () => "People and access", () => [{ label: "People and access" }]),
  page("/settings/access/keys", "settings", "access", () => "Keys for scripts",
    () => [{ label: "People and access", href: "/settings/access" }, { label: "Keys for scripts" }]),
  page("/settings/access/ssh-keys", "settings", "access", () => "SSH keys",
    () => [{ label: "People and access", href: "/settings/access" }, { label: "SSH keys" }]),
  page("/settings/access/agents", "settings", "access", () => "Connect an agent",
    () => [{ label: "People and access", href: "/settings/access" }, { label: "Connect an agent" }]),
  page("/settings/approvals", "settings", "approvals", () => say("{Clearances}"), () => [{ label: say("{Clearances}") }]),
  page("/settings/variables", "settings", "variables", () => "Variables", () => [{ label: "Variables" }]),
  page("/settings/words", "settings", "words", () => "Words", () => [{ label: "Words" }]),
  page("/settings/rules", "settings", "rules", () => "Rules", () => [{ label: "Rules" }]),
  page("/settings/audit", "settings", "audit", () => say("{Logbook}"), () => [{ label: say("{Logbook}") }]),

  page("/config", "settings", "config", () => "Configuration", () => [{ label: "Configuration" }]),
  page("/config/wizard/:id", "settings", "config", (p) => `Set-up: ${p.id.replace(/-/g, " ")}`,
    (p) => [{ label: "Configuration", href: "/config" }, { label: p.id.replace(/-/g, " ") }]),
  page("/config/:kind/new", "settings", "config", (p) => `New ${kindWord(p.kind)}`, (p) => [{ label: "Configuration", href: "/config" }, { label: kindWord(p.kind), href: `/config/${enc(p.kind)}` }, { label: "New" }]),
  page("/config/:kind/:name", "settings", "config", (p) => p.name, (p) => [{ label: "Configuration", href: "/config" }, { label: kindWord(p.kind), href: `/config/${enc(p.kind)}` }, { label: p.name }]),
  page("/config/:kind", "settings", "config", (p) => kindWord(p.kind), (p) => [{ label: "Configuration", href: "/config" }, { label: kindWord(p.kind) }]),
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
  // The registry became its own area, Images (docs 42, section 17).
  ["/components", "/images/components"],
  ["/library/images", "/images"],
  ["/library/components", "/images/components"],
];

/** The new address for an old one, or null if the address is not an old one. */
export function redirectFor(path: string, search = ""): string | null {
  const clean = path.length > 1 ? path.replace(/\/+$/, "") : path;
  for (const [from, to] of MOVED) if (clean === from) return to + search;
  return null;
}

/** Whether an address is in the Learn area (the documentation inside the product). */
export function isLearn(path: string): boolean {
  return path === "/learn" || path.startsWith("/learn/");
}

/** The section a path belongs to; none for an address no page owns, and none for Learn, which is not an area. */
export function sectionOf(path: string): SectionId | null {
  if (isLearn(path)) return null;
  return locate(path)?.info.section ?? null;
}

/** Whether a tab is the one shown for the path. */
export function tabOn(path: string): string | undefined {
  return locate(path)?.info.tab;
}
