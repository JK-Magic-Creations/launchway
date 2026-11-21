// The pages whose features Launchway does not have yet. Each says what it will do, what will appear
// there and what makes it appear, in plain words, and offers the one action that exists today. They
// are real pages, not mock-ups: nothing on them is invented data.

export interface Coming {
  title: string;
  /** The area's name, for the eyebrow. */
  area: string;
  /** One sentence: what this page will do. */
  does: string;
  /** What will appear here, and what makes it appear. */
  appears: string;
  /** What can be done today. */
  action?: { label: string; href: string };
}

export const COMING: Record<string, Coming> = {
  "/code/files": {
    title: "Files, commits and branches", area: "Code",
    does: "Browse a project's code here: its files, the history of each, who changed which line, and the branches with the rules that protect them.",
    appears: "Every project you add will show its files and commits here once Launchway reads the repository itself. Today it reads only the pipeline file.",
    action: { label: "Add a project", href: "/config/wizard/connect-repository" },
  },
  "/code/reviews": {
    title: "Reviews", area: "Code",
    does: "Read a proposed change, comment, see which checks passed, and merge it when the rules are met. A blocked merge will say which rule and what unblocks it.",
    appears: "A change under review appears here when a pull request starts a run of a project. Runs of pull requests exist today, and are listed under Activity.",
    action: { label: "See runs", href: "/activity" },
  },
  "/images/charts": {
    title: "Charts", area: "Images",
    does: "List the packaged deployment descriptions your pipelines build, with their settings and the applications that use each version.",
    appears: "A chart appears here when a pipeline step packages one and pushes it to a registry that Launchway can read.",
    action: { label: "See images", href: "/images" },
  },
  "/images/files": {
    title: "Files", area: "Images",
    does: "List the plain files your pipelines produce, such as installers and archives, with their size, who made them and where they were used.",
    appears: "Files appear here when a step declares them as artifacts. Artifacts of a run can be read on the run's own page today.",
    action: { label: "See runs", href: "/activity" },
  },
  "/deployments/hosts": {
    title: "Hosts", area: "Deployments",
    does: "Show your machines, which operating system image each runs, whether a new image is staged and waiting for a restart, and what each has run.",
    appears: "A host appears here when it reports in to Launchway. Hosts that do the work of runs are listed today under Settings, Runners.",
    action: { label: "Open runners", href: "/settings/runners" },
  },
  "/deployments/machine-images": {
    title: "Machine images", area: "Deployments",
    does: "List the operating system images your machines start from, each built and scanned by a pipeline like a container image, and which hosts run it.",
    appears: "An image appears here when a pipeline builds one. Container images are listed today under Images.",
    action: { label: "See images", href: "/images" },
  },
  "/traffic": {
    title: "Traffic map", area: "Traffic",
    does: "Draw what really talks to what: the services, the requests between them, how many fail and how slow they are, so a person can see where a problem starts.",
    appears: "The map fills in from the numbers your signal sources hold. Add a source that holds request metrics, and the services you deploy will appear on it.",
    action: { label: "Add a signal source", href: "/config/wizard/add-health-source" },
  },
  "/traffic/protection": {
    title: "Protection", area: "Traffic",
    does: "Answer one question: is traffic protected, and is anyone attacking? Signs of attack come first with their evidence, then the limits on each route.",
    appears: "Routes and their limits appear here once Launchway can read the gateway in front of your services, through a signal source.",
    action: { label: "Add a signal source", href: "/config/wizard/add-health-source" },
  },
  "/traffic/reliability": {
    title: "Reliability", area: "Traffic",
    does: "Show whether each service meets its target, and how much failure the target still allows this month, so a team knows when to slow down.",
    appears: "A service appears here with a target you set, once a signal source gives its error and speed numbers. Verify steps already read such sources today.",
    action: { label: "Add a signal source", href: "/config/wizard/add-health-source" },
  },
  "/traffic/toggles": {
    title: "Toggles", area: "Traffic",
    does: "List the switches that turn a feature on for some visitors, show where each is on and for whom, and let a person turn one off in seconds without a new release.",
    appears: "A toggle appears here when your applications report it. Nothing reports toggles to Launchway yet.",
  },
  "/changes": {
    title: "Calendar", area: "Changes",
    does: "Lay out what is changing and when: planned releases, freeze windows and what already happened, so a team does not release into a bad hour.",
    appears: "Changes appear on the calendar when a release is planned. Today the runs that deploy are listed under Activity and each deployment under Deployments.",
    action: { label: "See runs", href: "/activity" },
  },
  "/changes/records": {
    title: "Change records", area: "Changes",
    does: "Keep one record per change: what, why, who approved it, what the evidence said and how it went. Auditors read it; nobody has to write it by hand.",
    appears: "A record is made for every release that goes through an approval rule. Approvals already taken are in the audit trail today.",
    action: { label: "Open the audit trail", href: "/settings/audit" },
  },
  "/changes/freezes": {
    title: "Freeze windows", area: "Changes",
    does: "Say when nothing may be released, such as month end or a holiday, and show the window on the calendar and on every deploy that it blocks.",
    appears: "A window you add here blocks deploys to the places you choose. No freeze windows are defined yet.",
  },
  "/changes/rules": {
    title: "Approval rules", area: "Changes",
    does: "Say which changes need whose approval, by risk and by place, in plain sentences, and show a person exactly which rule applies to their change.",
    appears: "A rule appears here when you write one. Approval steps in a pipeline, and the rules that judge a run, work today.",
    action: { label: "Open the rules", href: "/settings/rules" },
  },
  "/changes/tickets": {
    title: "Ticket system", area: "Changes",
    does: "Connect the system where your organisation records change requests, so a release links to its request and a request closes itself when the release is healthy.",
    appears: "Connected tickets appear on each change record. Nothing is connected yet, and the connection is not built yet.",
  },
};

/** The title of a coming page, or the last word of its address when there is none. */
export function comingTitle(path: string): string {
  return COMING[path]?.title ?? path.split("/").pop()?.replace(/-/g, " ") ?? "Not here";
}

/** The addresses that have a coming page. */
export function isComing(path: string): boolean {
  return Object.prototype.hasOwnProperty.call(COMING, path);
}
