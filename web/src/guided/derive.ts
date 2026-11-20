// What Home and the Deployments pages say, worked out from real state the
// server already reports. Pure functions: tested without a browser.

import type { EntityRef } from "../entities";
import type { Approval, DeployApp, DeploymentView, EnvironmentView, Me, Project, RunSummary, Runner, StepView } from "../api";

// ---- the setup checklist ----

export interface CheckItem {
  id: "project" | "runner" | "run" | "environment" | "signin";
  title: string;
  /** Why it is needed, in one sentence a beginner can follow. */
  why: string;
  done: boolean;
  /** The wizard or page that does it. */
  href: string;
  button: string;
}

export interface Setup {
  projects: Project[];
  runners: Runner[];
  runs: RunSummary[];
  environments: EnvironmentView[];
}

/** The essentials, in the order of doing them. Each is done when the server's own state says so,
 * never because someone pressed a button. Sign-in and the runner are left out when they do not apply. */
export function checklist(me: Me, s: Setup): CheckItem[] {
  const firstProject = s.projects[0]?.name;
  const items: CheckItem[] = [
    {
      id: "project", title: "Add your first project",
      why: "Pick a repository. Launchway reads its pipeline from Git and runs it when you push.",
      done: s.projects.length > 0, href: "/config/wizard/connect-repository", button: "Add a project",
    },
  ];
  if (me.runners) {
    items.push({
      id: "runner", title: "Choose who does the work",
      why: "A runner is a computer that builds and tests. You can start with this server.",
      done: s.runners.some((r) => r.online), href: "/config/wizard/add-runner", button: "Add a runner",
    });
  }
  items.push({
    id: "run", title: "See your first run pass",
    why: "Open a project and start a run. Nothing is deployed by it.",
    done: s.runs.some((r) => r.status === "succeeded"), href: firstProject ? `/projects/${encodeURIComponent(firstProject)}` : "/projects", button: "Open your project",
  });
  items.push({
    id: "environment", title: "Create an environment",
    why: "A place your software goes, like staging or production. It takes one minute.",
    done: s.environments.length > 0, href: "/config/wizard/add-environment", button: "Add an environment",
  });
  if (me.admin) {
    items.push({
      id: "signin", title: "Turn on sign-in",
      why: "So people use the accounts they have, and groups decide what each may do.",
      done: me.auth_mode === "oidc", href: "/config/wizard/first-run?step=signin", button: "Set up sign-in",
    });
  }
  return items;
}

export interface Next { item: CheckItem | null; done: number; total: number }

/** The first thing not done yet, which the next-step card asks for. The step to run needs the earlier ones. */
export function nextStep(items: CheckItem[]): Next {
  return { item: items.find((i) => !i.done) ?? null, done: items.filter((i) => i.done).length, total: items.length };
}

// ---- what needs attention ----

export interface Attention {
  key: string;
  tone: "bad" | "warn";
  title: string;
  /** The things the title names, so each is a link. */
  entities: EntityRef[];
  detail: string;
  detailEntities?: EntityRef[];
  href: string;
  button: string;
}

export interface AttentionInput {
  projects: Project[];
  approvals: Approval[];
  runners: Runner[];
  applications: AppRow[];
  /** Whether a person who is not an administrator may be told about runners. */
  me: Me;
}

/** Most urgent first: failed runs, then broken applications, then what waits for a person, then the rest. */
export function attention(i: AttentionInput): Attention[] {
  const out: Attention[] = [];
  for (const p of i.projects) {
    const r = p.last_run;
    if (r && r.status === "failed") {
      out.push({ key: `run:${r.id}`, tone: "bad", title: `Run #${r.number} of ${p.name} failed`, entities: [{ kind: "project", name: p.name }], detail: r.message || r.pipeline, href: `/runs/${r.id}`, button: "See what went wrong" });
    }
  }
  for (const a of i.applications) {
    if (a.health === "Degraded" || a.health === "Missing") {
      out.push({ key: `app:${a.name}`, tone: "bad", title: `${a.name} is unhealthy`, entities: [{ kind: "application", name: a.name }], detail: a.healthMessage || "Argo CD says the application is not healthy.", href: `/deployments/applications/${encodeURIComponent(a.name)}`, button: "Look into it" });
    }
  }
  for (const a of i.approvals) {
    out.push({ key: `approval:${a.run_id}/${a.step_id}`, tone: "warn", title: `Waiting for you: ${a.message || a.step_id}`, entities: [{ kind: "project", name: a.project }], detail: `Run #${a.run_number} of ${a.project}${a.started_by ? `, started by ${a.started_by}` : ""}`, detailEntities: [{ kind: "project", name: a.project }], href: `/runs/${a.run_id}?step=${encodeURIComponent(a.step_id)}`, button: "Review" });
  }
  for (const a of i.applications) {
    if (a.sync === "OutOfSync" && a.health !== "Degraded" && a.health !== "Missing") {
      out.push({ key: `drift:${a.name}`, tone: "warn", title: `${a.name} differs from Git`, entities: [{ kind: "application", name: a.name }], detail: "The cluster is not what Git says it should be.", href: `/deployments/applications/${encodeURIComponent(a.name)}`, button: "See the difference" });
    }
  }
  if (i.me.runners) {
    for (const r of i.runners) {
      if (!r.online) out.push({ key: `runner:${r.id}`, tone: "warn", title: `${r.name} is not answering`, entities: [{ kind: "runner", name: r.name }], detail: "Steps sent to it will wait until it is back, or another runner takes them.", href: "/settings/runners", button: "Open runners" });
    }
  }
  return out;
}

// ---- Argo CD applications, as the deploy runners saw them ----

export interface AppRow {
  name: string;
  environment: string;
  project: string;
  service: string;
  sync: string;
  health: string;
  healthMessage?: string;
  revision?: string;
  version: string;
  /** The run whose deployment last touched it. */
  run: { id: string; number: number } | null;
  observedAt: number;
  observedBy?: string;
  target: string;
  /** What the deploy runner could not see, if so. */
  error?: string;
  found: boolean;
  automated: boolean;
}

/** The applications the deploy runners have seen, one row each: from the newest deployment of every
 * service in every environment, and the observation made since if there is a newer one. An application
 * a pipeline has never deployed is not here, because no runner has been asked to watch it. */
export function applications(envs: EnvironmentView[]): AppRow[] {
  const rows = new Map<string, AppRow>();
  for (const e of envs) {
    for (const u of e.services) {
      const d: DeploymentView | undefined = u.last;
      if (!d || !d.application) continue;
      const live = u.live && u.live.observed_at > (d.observed_at ?? 0) && u.live.snapshot ? u.live : undefined;
      const snap = live?.snapshot ?? d.snapshot;
      const app: DeployApp | undefined = snap?.app;
      const row: AppRow = {
        name: d.application, environment: e.name, project: u.project, service: u.service,
        sync: app?.found === false ? "Unknown" : app?.sync || "Unknown", health: app?.found === false ? "Missing" : app?.health || "Unknown",
        healthMessage: app?.health_message, revision: app?.revision, version: u.current?.version ?? d.version,
        run: { id: d.run_id, number: d.run_number }, observedAt: live?.observed_at ?? d.observed_at ?? 0,
        observedBy: live?.observed_by ?? d.observed_by, target: d.target, error: snap?.error, found: app?.found ?? false, automated: app?.automated ?? false,
      };
      const had = rows.get(row.name);
      if (!had || row.observedAt > had.observedAt) rows.set(row.name, row);
    }
  }
  return [...rows.values()].sort((a, b) => a.name.localeCompare(b.name));
}


/** An application needs a look when it differs from Git or is not healthy. */
export function needsLook(a: AppRow): boolean {
  return a.sync === "OutOfSync" || a.health === "Degraded" || a.health === "Missing" || !!a.error;
}

/** Argo CD's words as a beginner reads them. The originals stay available beside them. */
export function plainSync(sync: string): { label: string; tone: "ok" | "warn" | "idle" } {
  if (sync === "Synced") return { label: "Matches Git", tone: "ok" };
  if (sync === "OutOfSync") return { label: "Differs from Git", tone: "warn" };
  return { label: "Not known yet", tone: "idle" };
}

export function plainHealth(health: string): { label: string; tone: "ok" | "run" | "bad" | "warn" | "idle" } {
  switch (health) {
    case "Healthy": return { label: "Healthy", tone: "ok" };
    case "Progressing": return { label: "Starting up", tone: "run" };
    case "Degraded": return { label: "Unhealthy", tone: "bad" };
    case "Missing": return { label: "Not found", tone: "bad" };
    case "Suspended": return { label: "Paused", tone: "warn" };
    default: return { label: "Not known yet", tone: "idle" };
  }
}

// ---- a failed run, explained ----

export type FailureKind = "policy" | "timeout" | "killed" | "exit" | "infrastructure" | "unknown";

export interface Failure {
  kind: FailureKind;
  step: string;
  /** One plain sentence: what went wrong. */
  headline: string;
  /** What it usually means, and what to try. */
  advice: string[];
  exitCode: number | null;
  excerpt: string;
  /** More steps failed than the one explained. */
  others: number;
}

/** Explains the first failed step of a run from what the server recorded about it: its error, exit code, and
 * last lines of output. Exit code 137 is how a process killed for using too much memory ends, so it is said as
 * "most likely"; Launchway cannot see the kernel's reason. */
export function explainFailure(steps: StepView[]): Failure | null {
  const failed = steps.filter((s) => s.status === "failed");
  if (failed.length === 0) return null;
  const s = [...failed].sort((a, b) => a.position - b.position)[0];
  const base = { step: s.id, exitCode: s.exit_code ?? null, excerpt: s.excerpt ?? "", others: failed.length - 1 };
  const err = (s.error ?? "").trim();
  if (s.policy_blocked) {
    return { ...base, kind: "policy", headline: `A rule stopped ${s.id}.`, advice: ["The step itself did not fail: a rule in force refused it.", "Open the rule's decision to see what it wanted. If the rule is wrong for this case, an administrator can grant a time-limited waiver."] };
  }
  if (/^timed out after/i.test(err)) {
    return { ...base, kind: "timeout", headline: `${s.id} took too long and was stopped (${err.replace(/^timed out/i, "timed out")}).`, advice: [
      s.timeout ? `The step may take ${s.timeout}. Raise its timeout in the pipeline if it really needs longer.` : "Raise the step's timeout in the pipeline if it really needs longer.",
      "If it normally finishes faster, look at the last lines of output: it may be waiting for something that never came.",
    ] };
  }
  if (s.exit_code === 137) {
    return { ...base, kind: "killed", headline: `${s.id} was stopped from outside (exit code 137), most likely because it ran out of memory.`, advice: [
      "Give the step more memory, or make it use less.",
      "If it was not a memory problem, the runner may have been stopped while the step ran: run it again.",
    ] };
  }
  if (s.exit_code !== null && s.exit_code !== undefined && s.exit_code !== 0) {
    return { ...base, kind: "exit", headline: `${s.id} failed: its command ended with exit code ${s.exit_code}.`, advice: [
      "The last lines of output below are usually where the reason is.",
      "Fix it in your repository and push, or run the step again if it failed for a reason outside your code.",
    ] };
  }
  if (err) {
    return { ...base, kind: "infrastructure", headline: `${s.id} could not run: ${err.replace(/\.$/, "")}.`, advice: ["The step's command never ran, so the cause is how it was set up, such as its image or the machine it was sent to.", "Open the step to see the full message."] };
  }
  return { ...base, kind: "unknown", headline: `${s.id} failed.`, advice: ["Open the step's logs to see what it said."] };
}
