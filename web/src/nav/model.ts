// The navigation model: six sections, the tabs inside them, which are
// shown, where every page sits, and where the old addresses went. It is
// plain data and pure functions, so it is tested without a browser.

import type { Me } from "../api";
import { match } from "../router";
import type { IconName } from "../kit/Icon";
import { comingTitle } from "../shell/comingTitles";
import { terms } from "../terms";

/** The words in effect, filled into a sentence: {Hold} is what this installation calls a freeze. */
const say = (t: string) => terms().say(t);

export type SectionId = "home" | "projects" | "work" | "pipelines" | "images" | "deployments" | "traffic" | "changes" | "activity" | "library" | "settings";

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
  { id: "home", get label() { return say("{Home}"); }, href: "/", icon: "home", group: 0, promise: "What needs you now, the changes on their way, and a map of everything Launchway holds.", tabs: [] },
  { id: "projects", label: "Code", href: "/projects", icon: "code", group: 1, get promise() { return say("Repositories and {projects}: files, commits, branches and reviews."); }, tabs: [] },
  {
    id: "work", label: "Work", href: "/work", icon: "list", group: 1,
    get promise() { return say("Issues, boards, backlogs and sprints, and the agents that work on them."); }, tabs: [],
  },
  { id: "pipelines", label: "Pipelines", href: "/pipelines", icon: "graph", group: 1, get promise() { return say("The steps that build, test and release your software, and how their {runs} went."); }, tabs: [] },
  {
    id: "images", get label() { return say("{Registry}"); }, href: "/depot", icon: "package", group: 1,
    get promise() { return say("Container images, Helm charts and files your pipelines build, the built-in store, and what is in them: one place."); },
    tabs: [
      { id: "images", label: "Images", href: "/depot", icon: "package" },
      { id: "components", label: "Components", href: "/depot/components", icon: "list" },
      { id: "store", label: "Built-in store", href: "/depot/store", icon: "server" },
    ],
  },
  {
    id: "deployments", get label() { return say("{Deploys}"); }, href: "/deployments", icon: "deploy", group: 2, side: "What is running, and where.",
    get promise() { return say("What is running where: applications, {environments}, hosts and machine images."); },
    tabs: [
      { id: "overview", label: "Overview", href: "/deployments", icon: "deploy" },
      { id: "environments", get label() { return say("{Environments}"); }, href: "/deployments/environments", icon: "list", aka: "Environments" },
      { id: "releases", label: "Releases", href: "/deployments/releases", icon: "package" },
      // Appears when a deploy runner has seen an Argo CD application.
      { id: "applications", label: "Applications", href: "/deployments/applications", icon: "graph", show: ({ facts }) => has(facts.applications) },
      { id: "hosts", label: "Hosts", href: "/deployments/hosts", icon: "server", soon: true },
      { id: "machine-images", label: "Machine images", href: "/deployments/machine-images", icon: "package", soon: true },
    ],
  },
  {
    id: "traffic", get label() { return say("{Monitoring}"); }, href: "/traffic", icon: "flow", group: 2, side: "How requests flow, and how they are protected.",
    promise: "How requests flow between services, and what protects and watches them.",
    tabs: [
      { id: "map", label: "Traffic map", href: "/traffic", icon: "flow", soon: true },
      { id: "protection", label: "Protection", href: "/traffic/protection", icon: "shield", soon: true },
      { id: "reliability", label: "Reliability", href: "/traffic/reliability", icon: "clock", soon: true },
      { id: "toggles", get label() { return say("{Toggles}"); }, href: "/traffic/toggles", icon: "bolt", aka: "Toggles", soon: true },
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
    id: "activity", label: "Activity", href: "/activity", icon: "activity", group: 3, get side() { return say("{Runs} and decisions."); },
    get promise() { return say("Every {run}, and everything waiting for a person to decide."); },
    tabs: [
      { id: "runs", get label() { return say("{Runs}"); }, href: "/activity", icon: "activity", aka: "Runs" },
      { id: "approvals", label: "Waiting for a decision", href: "/activity/approvals", icon: "gate" },
      { id: "events", label: "Events", href: "/activity/events", icon: "clock" },
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
    get promise() { return say(terms().say("People, {runners}, rules, signal sources, configuration, the words Launchway uses and the {logbook}.")); },
    tabs: [
      { id: "overview", label: "Overview", href: "/settings", icon: "settings" },
      { id: "runners", get label() { return say("{Runners}"); }, href: "/settings/runners", icon: "server", aka: "Runners", show: ({ me }) => me.runners },
      { id: "access", label: "People and access", href: "/settings/access", icon: "users" },
      { id: "approvals", get label() { return say("{Clearances}"); }, href: "/settings/approvals", icon: "gate", aka: "Approvals" },
      { id: "rules", label: "Rules", href: "/settings/rules", icon: "shield", show: ({ me }) => me.runners },
      { id: "variables", label: "Variables", href: "/settings/variables", icon: "list" },
      { id: "standards", get label() { return say("{Standards}"); }, href: "/settings/standards", icon: "shield", aka: "Standards" },
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
/** A page of one project's pipeline: its title, and the trail back to the project. */
const projectCrumb = (p: Record<string, string>): Crumb => ({ label: p.name, href: `/projects/${enc(p.name)}/pipeline` });
const togglesCrumb = (p: Record<string, string>): Crumb => ({ label: terms().say("{Toggles}"), href: `/projects/${enc(p.name)}/toggles` });
const projectSub = (sub: string, title: string, label: string) =>
  page(`/projects/:name/${sub}`, "pipelines", undefined, (p) => `${terms().say(title)} of ${p.name}`, (p) => [projectCrumb(p), { label: terms().say(label) }]);

const repo = (p: Record<string, string>) => p.owner + "/" + p.name;
const repoHref = (p: Record<string, string>) => `/repositories/${enc(p.owner)}/${enc(p.name)}`;
/** The Launch a run page shows (?launch=), if any: the trail then reads Sorties › Sortie › Launch. */
const launchOf = (): string => (typeof location === "undefined" ? "" : new URLSearchParams(location.search).get("launch") ?? "");
const repoCrumb = (p: Record<string, string>, to = ""): Crumb => ({ label: repo(p), href: repoHref(p) + to });
const reposCrumb: Crumb = { label: "Repositories", href: "/repositories" };
/** Repositories, then this repository (its front page is the code), for the trail of every page of one repository. */
const repoTrail = (p: Record<string, string>): Crumb[] => [reposCrumb, repoCrumb(p, "/code")];
const cap = (w: string) => w[0].toUpperCase() + w.slice(1);
/** A page of one repository: its title ("Mirrors of owner/name"), and the trail back to the repository. */
const repoSub = (sub: string, title: string, label: string, themed = false) => {
  const word = (t: string) => (themed ? terms().say(t) : t);
  return page(`/repositories/:owner/:name/${sub}`, "projects", undefined, (p) => `${word(title)} ${repo(p)}`, (p) => [...repoTrail(p), { label: word(label) }]);
};

/** A page of Settings whose tab, title and trail are one word. */
const settingsPage = (tab: string, label: string) => page(`/settings/${tab}`, "settings", tab, () => terms().say(label), () => [{ label: terms().say(label) }]);

export const PAGES: PageInfo[] = [
  page("/", "home", undefined, () => terms().say("{Home}"), () => []),

  page("/projects", "projects", undefined, () => "Code", () => []),
  page("/code/:page", "projects", undefined, (p) => comingTitle("/code/" + p.page), (p) => [{ label: comingTitle("/code/" + p.page) }]),

  page("/pipelines", "pipelines", undefined, () => "Pipelines", () => []),
  // Organize into Squadrons: before any /units/:name page, so the word organize is never taken for a Squadron's name.
  page("/units/organize", "home", undefined, () => say("Organize into {Units}"), () => [{ label: say("Organize into {Units}") }]),
  page("/projects/new", "projects", undefined, () => terms().say("Add a {project}"), () => [{ label: terms().say("Add a {project}") }]),
  page("/units", "home", undefined, () => terms().say("{Units}"), () => [{ label: terms().say("{Units}") }]),
  page("/units/new", "home", undefined, () => terms().say("Add a {unit}"), () => [{ label: terms().say("{Units}"), href: "/units" }, { label: terms().say("Add a {unit}") }]),
  page("/units/:name", "home", undefined, (p) => terms().say(`{Unit} ${p.name}`), (p) => [{ label: terms().say("{Units}"), href: "/units" }, { label: p.name }]),
  page("/units/:name/projects/new", "home", undefined, (p) => terms().say(`Add a {project} to ${p.name}`),
    (p) => [{ label: terms().say("{Units}"), href: "/units" }, { label: p.name, href: `/units/${enc(p.name)}` }, { label: terms().say("Add a {project}") }]),
  page("/projects/:name", "projects", undefined, (p) => p.name, (p) => [{ label: p.name }]),
  page("/projects/:name/pipeline", "pipelines", undefined, (p) => `Pipeline of ${p.name}`, (p) => [{ label: p.name }]),
  page("/projects/:name/pipeline/edit", "pipelines", undefined, (p) => `Edit the pipeline of ${p.name}`,
    (p) => [{ label: p.name, href: `/projects/${enc(p.name)}/pipeline` }, { label: "Edit" }]),
  projectSub("tests", "Tests", "Tests and coverage"),
  projectSub("security", "Security", "Security"),
  projectSub("caches", "Caches", "Build caches"),
  projectSub("standards", "{Standards}", "{Standards}"),
  page("/projects/:name/configuration", "pipelines", undefined, (p) => `Configuration of ${p.name}`, (p) => [{ label: p.name, href: `/projects/${enc(p.name)}/pipeline` }, { label: "Configuration" }]),
  page("/runs/:id/configuration/:step", "pipelines", undefined, (p) => `Configuration report of ${p.step}`, () => [{ label: terms().say("{Runs}"), href: "/activity" }, { label: "Configuration report" }]),
  projectSub("variables", "Variables", "Variables"),
  page("/setup/guide", "settings", "guide", () => "Setup guide", () => [{ label: "Setup guide" }]),
  page("/projects/:name/toggles", "pipelines", undefined, (p) => `Toggles of ${p.name}`, (p) => [projectCrumb(p), { label: terms().say("{Toggles}") }]),
  page("/projects/:name/toggles/:toggle", "pipelines", undefined, (p) => `Toggle ${p.toggle} of ${p.name}`, (p) => [projectCrumb(p), togglesCrumb(p), { label: p.toggle }]),
  page("/projects/:name/toggle-settings", "pipelines", undefined, (p) => `Keys and safeguards of ${p.name}`, (p) => [projectCrumb(p), togglesCrumb(p), { label: "Keys and safeguards" }]),
  page("/repositories/import", "settings", "import", () => "Import repositories", () => [{ label: "Import repositories" }]),
  page("/repositories/trash", "projects", undefined, () => "Deleted repositories", () => [reposCrumb, { label: "Deleted repositories" }]),
  page("/repositories/:owner/:name", "projects", undefined, (p) => p.owner + "/" + p.name, (p) => [reposCrumb, { label: p.owner + "/" + p.name }]),
  repoSub("settings", "Settings of", "Settings"),
  repoSub("mirrors", "Mirrors of", "Mirrors"),
  repoSub("dependencies", "Dependencies of", "Dependencies"),
  repoSub("graph", "Graph of", "Graph"),
  page("/graph", "projects", undefined, () => "Graph", () => [{ label: "Graph" }]),
  repoSub("updates", "{Dependency_updatess} of", "{Dependency_updatess}", true),
  repoSub("history", "Imported history of", "Imported history"),
  repoSub("pulls", "Pull requests of", "Pull requests"),
  page("/repositories/:owner/:name/pulls/new", "projects", undefined, (p) => `New pull request in ${repo(p)}`, (p) => [...repoTrail(p), { label: "Pull requests", href: repoHref(p) + "/pulls" }, { label: "New" }]),
  page("/repositories/:owner/:name/pulls/:number", "projects", undefined, (p) => `Pull request #${p.number} in ${repo(p)}`, (p) => [...repoTrail(p), { label: "Pull requests", href: repoHref(p) + "/pulls" }, { label: `#${p.number}` }]),
  page("/repositories/:owner/:name/:view", "projects", undefined, (p) => `${cap(p.view)}: ${repo(p)}`, (p) => [...repoTrail(p), { label: cap(p.view) }]),
  page("/repositories/:owner/:name/commit/:sha", "projects", undefined, (p) => `Commit ${p.sha.slice(0, 8)}: ${repo(p)}`, (p) => [...repoTrail(p), { label: "Commits", href: repoHref(p) + "/commits" }, { label: p.sha.slice(0, 8) }]),
  page("/playground", "projects", undefined, () => "Try a pipeline", () => [{ label: "Try a pipeline without saving" }]),

  page("/deployments", "deployments", "overview", () => say("{Deploys}"), () => []),
  page("/deployments/environments", "deployments", "environments", () => terms().say("{Environments}"), () => [{ label: terms().say("{Environments}") }]),
  page("/deployments/environments/:name", "deployments", "environments", (p) => terms().say(`{Environment} ${p.name}`),
    (p) => [{ label: terms().say("{Environments}"), href: "/deployments/environments" }, { label: p.name }]),
  ...["", "/:a", "/:a/:b"].map((x) => page("/deployments/releases" + x, "deployments", "releases", (p) => (p.b ? `Release ${p.a} ${p.b}` : p.a ? `Releases: ${p.a}` : "Releases"),
    (p) => [{ label: "Releases", href: p.a ? "/deployments/releases" : undefined }, ...(p.a ? [{ label: p.b ? `${p.a} ${p.b}` : p.a }] : [])])),
  page("/deployments/releases/:a/:b/get/:c", "deployments", "releases", (p) => `Download ${p.c}: ${p.a} ${p.b}`,
    (p) => [{ label: "Releases", href: "/deployments/releases" }, { label: `${p.a} ${p.b}`, href: `/deployments/releases/${p.a}/${p.b}` }, { label: p.c }]),
  page("/deployments/hosts", "deployments", "hosts", () => "Hosts", () => [{ label: "Hosts" }]),
  page("/deployments/machine-images", "deployments", "machine-images", () => "Machine images", () => [{ label: "Machine images" }]),
  page("/deployments/applications", "deployments", "applications", () => "Applications", () => [{ label: "Applications" }]),
  page("/deployments/applications/:name", "deployments", "applications", (p) => `Application ${p.name}`,
    (p) => [{ label: "Applications", href: "/deployments/applications" }, { label: p.name }]),

  page("/repositories", "projects", undefined, () => "Repositories", () => [{ label: "Repositories" }]),
  page("/owners/:owner", "projects", undefined, (p) => `Owner ${p.owner}`,
    (p) => [{ label: "Repositories", href: "/repositories" }, { label: p.owner }]),

  page("/activity", "activity", "runs", () => terms().say("{Runs}"), () => [{ label: terms().say("{Runs}") }]),
  page("/activity/approvals", "activity", "approvals", () => "Waiting for a decision", () => [{ label: "Waiting for a decision" }]),
  page("/activity/events", "activity", "events", () => "Events", () => []),
  page("/runs/:id", "activity", "runs", (p) => (launchOf() ? "Launch of " : "") + terms().say(`{Run} ${p.id}`), (p) => launchOf()
    ? [{ label: terms().say("{Runs}"), href: "/activity" }, { label: terms().say(`{Run} ${p.id.slice(0, 8)}`), href: `/runs/${enc(p.id)}` }, { label: "Launch" }]
    : [{ label: terms().say("{Runs}"), href: "/activity" }, { label: terms().say(`{Run} ${p.id.slice(0, 8)}`) }]),

  page("/depot", "images", "images", () => "Images", () => [{ label: "Images" }]),
  page("/depot/components", "images", "components", () => "Components", () => [{ label: "Components" }]),
  page("/depot/store", "images", "store", () => "Built-in store", () => [{ label: "Built-in store" }]),
  page("/depot/store/:project", "images", "store", (p) => `Built-in store of ${p.project}`, (p) => [{ label: "Built-in store", href: "/depot/store" }, { label: p.project }]),
  page("/depot/digest/:digest", "images", "images", (p) => `Image ${p.digest.slice(0, 19)}`, (p) => [{ label: "Images", href: "/depot" }, { label: "Image " + p.digest.slice(0, 19) }]),
  page("/depot/:page", "images", undefined, (p) => comingTitle("/depot/" + p.page), (p) => [{ label: comingTitle("/depot/" + p.page) }]),

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
  page("/learn", "home", undefined, () => terms().say("{Learn}"), () => []),
  page("/learn/:a", "home", undefined, () => terms().say("{Learn}"), () => []),
  page("/learn/:a/:b", "home", undefined, () => terms().say("{Learn}"), () => []),

  page("/kit", "settings", undefined, () => "Design kit", () => [{ label: "Design kit" }]),

  page("/settings", "settings", "overview", () => "Settings", () => []),
  page("/settings/runners", "settings", "runners", () => terms().say("{Runners}"), () => [{ label: terms().say("{Runners}") }]),
  settingsPage("access", "People and access"),
  page("/settings/access/keys", "settings", "access", () => "Keys for scripts",
    () => [{ label: "People and access", href: "/settings/access" }, { label: "Keys for scripts" }]),
  page("/settings/access/ssh-keys", "settings", "access", () => "SSH keys",
    () => [{ label: "People and access", href: "/settings/access" }, { label: "SSH keys" }]),
  page("/settings/access/agents", "settings", "access", () => "Connect an agent",
    () => [{ label: "People and access", href: "/settings/access" }, { label: "Connect an agent" }]),
  page("/settings/approvals", "settings", "approvals", () => say("{Clearances}"), () => [{ label: say("{Clearances}") }]),
  settingsPage("standards", "{Standards}"),
  settingsPage("variables", "Variables"),
  settingsPage("words", "Words"),
  settingsPage("rules", "Rules"),
  page("/settings/audit", "settings", "audit", () => say("{Logbook}"), () => [{ label: say("{Logbook}") }]),

  page("/config", "settings", "config", () => "Configuration", () => [{ label: "Configuration" }]),
  page("/config/wizard/:id", "settings", "config", (p) => `Set-up: ${p.id.replace(/-/g, " ")}`,
    (p) => [{ label: "Configuration", href: "/config" }, { label: p.id.replace(/-/g, " ") }]),
  page("/config/:kind/new", "settings", "config", (p) => `New ${kindWord(p.kind)}`, (p) => [{ label: "Configuration", href: "/config" }, { label: kindWord(p.kind), href: `/config/${enc(p.kind)}` }, { label: "New" }]),
  page("/config/:kind/:name", "settings", "config", (p) => p.name, (p) => [{ label: "Configuration", href: "/config" }, { label: kindWord(p.kind), href: `/config/${enc(p.kind)}` }, { label: p.name }]),
  page("/config/:kind", "settings", "config", (p) => kindWord(p.kind), (p) => [{ label: "Configuration", href: "/config" }, { label: kindWord(p.kind) }]),
];

const WORK = page("/work", "work", undefined, (p) => (p.w ? `Work: ${p.w}` : "Work"), (p) => (p.w ? [{ label: p.w }] : []));

export interface Located { info: PageInfo; params: Record<string, string> }

/** Finds the page for an address. Order matters where a pattern could also
 * match: /config/wizard/x before /config/:kind/:name, /projects/:name/pipeline/edit first. */
export function locate(path: string): Located | null {
  const clean = path.length > 1 ? path.replace(/\/+$/, "") : path;
  // Every work address (its pages are chunks of their own) is one page here: the rest of the address is its name.
  if (clean === "/work" || clean.startsWith("/work/")) return { info: WORK, params: { w: clean.slice(6).split("/").map(decodeURIComponent).join(" / ").replace("project:", "") } };
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
  // The registry became its own area, Images (docs 42, section 17), and the area is now called Depot.
  ["/components", "/depot/components"],
  ["/library/images", "/depot"],
  ["/library/components", "/depot/components"],
  // The area was called Images and lived under /images: every old address keeps working.
  ["/images", "/depot"],
  ["/images/components", "/depot/components"],
  ["/images/registry", "/depot/store"],
];

/** Old addresses that moved with whatever follows them: /images/digest/x is /depot/digest/x. */
const MOVED_PREFIX: [from: string, to: string][] = [
  ["/images/registry/", "/depot/store/"],
  ["/images/", "/depot/"],
];

/** The new address for an old one, or null if the address is not an old one. */
export function redirectFor(path: string, search = ""): string | null {
  const clean = path.length > 1 ? path.replace(/\/+$/, "") : path;
  for (const [from, to] of MOVED) if (clean === from) return to + search;
  for (const [from, to] of MOVED_PREFIX) if (clean.startsWith(from)) return to + clean.slice(from.length) + search;
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
