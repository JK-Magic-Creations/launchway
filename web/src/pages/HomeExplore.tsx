import type { ReactNode } from "react";
import type { Approval, EnvironmentView, Project } from "../api";
import type { AppRow } from "../guided/derive";
import { needsLook } from "../guided/derive";
import { Icon, type IconName } from "../kit/Icon";
import { Status } from "../kit/Status";
import { onLink } from "../router";
import { terms } from "../terms";

export interface ExploreProps {
  projects: Project[];
  applications: AppRow[];
  environments: EnvironmentView[];
  approvals: Approval[];
  /** Different images found, or null while it is not known. */
  images: number | null;
}

interface Tile { key: string; icon: IconName; label: string; n: ReactNode; unit: string; state: ReactNode; href: string }

/** The tiles of the Explore strip, each with one live number and one state line. A calm installation
 * shows no warning on any tile. Traffic and Changes are not built yet and say so. */
export function exploreTiles(p: ExploreProps): Tile[] {
  const failing = p.projects.filter((x) => x.last_run?.status === "failed").length;
  const unreachable = p.projects.filter((x) => x.error).length;
  const look = p.applications.filter(needsLook).length;
  return [
    { key: "code", icon: "code", label: "Code", n: p.projects.length, unit: p.projects.length === 1 ? "project" : "projects", href: "/projects",
      state: unreachable ? <><Status state="warn" /> {unreachable} cannot be reached</> : p.projects.length ? <><Status state="ok" /> All reachable</> : "None yet" },
    { key: "pipelines", icon: "graph", label: "Pipelines", n: p.projects.length, unit: p.projects.length === 1 ? "pipeline" : "pipelines", href: "/pipelines",
      state: failing ? <><Status state="bad" /> {failing} stopped</> : p.projects.some((x) => x.last_run) ? <><Status state="ok" /> Last runs are fine</> : terms().say("No {runs} yet") },
    { key: "images", icon: "package", label: "Images", n: p.images ?? "…", unit: p.images === 1 ? "image" : "images", href: "/images",
      state: p.images === null ? "Counting" : p.images === 0 ? "Nothing built yet" : terms().say("Built by your {runs}") },
    { key: "deployments", icon: "deploy", label: terms().say("{Deploys}"), n: p.applications.length || p.environments.length, unit: p.applications.length ? (p.applications.length === 1 ? "application" : "applications") : terms().say(p.environments.length === 1 ? "{environment}" : "{environments}"), href: "/deployments",
      state: look ? <><Status state="warn" /> {look} need a look</> : p.environments.length ? <><Status state="ok" /> All clear</> : "No places yet" },
    { key: "waiting", icon: "gate", label: "Waiting", n: p.approvals.length, unit: p.approvals.length === 1 ? "decision" : "decisions", href: "/activity/approvals",
      state: p.approvals.length ? <><Status state="wait" /> For a person</> : "Nothing waits" },
    { key: "traffic", icon: "flow", label: "Traffic", n: "–", unit: "", href: "/traffic", state: "Coming" },
    { key: "changes", icon: "calendar", label: "Changes", n: "–", unit: "", href: "/changes", state: "Coming" },
  ];
}

export function Explore(p: ExploreProps) {
  return (
    <section className="explore" aria-labelledby="explore-h">
      <div className="ex-h"><h2 id="explore-h">Explore</h2><span className="faint">What Launchway holds, with its live count</span></div>
      <div className="ex-g">
        {exploreTiles(p).map((t) => (
          <a key={t.key} className="ex" href={t.href} onClick={onLink}>
            <span className="ex-i"><Icon name={t.icon} /><b>{t.label}</b></span>
            <span className="ex-n">{t.n}</span>
            <span className="ex-l">{t.unit || " "}</span>
            <span className="ex-s">{t.state}</span>
          </a>
        ))}
      </div>
    </section>
  );
}
