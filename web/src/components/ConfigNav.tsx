// ConfigNav is the configuration section's own menu: home, the guided
// set-ups, and each kind of object, named with the configured level names.

import type { ConfigOverview } from "../configApi";
import { kindLabel, kindShown } from "../configApi";
import { onLink } from "../router";

export interface WizardInfo { id: string; title: string; kind?: string; text: string }

export const WIZARDS: WizardInfo[] = [
  { id: "connect-repository", title: "Connect a repository", kind: "project", text: "Watch a Git repository: its branch, what starts runs, and where its pipeline comes from. Then run it." },
  { id: "add-runner", title: "Add a runner", kind: "runner-pool", text: "Pick or make a pool, make a join token, and get the exact command for a machine, a container or Kubernetes." },
  { id: "add-environment", title: "Add an environment", kind: "environment", text: "Where deploy steps deploy: its target, who may deploy there, whether it is protected." },
  { id: "add-policy", title: "Add a policy", kind: "policy", text: "Rules runs must meet before they deploy or finish, enforced or only reported." },
  { id: "add-health-source", title: "Add a health source", kind: "health-source", text: "Metrics or logs that verify steps read to judge a deployment." },
  { id: "add-model", title: "Add an AI model", kind: "model", text: "A model steps may ask, which {projects} may use it, and where its key is kept." },
];

export function wizardFor(kind: string): WizardInfo | undefined {
  return WIZARDS.find((w) => w.kind === kind);
}

export function ConfigNav({ overview, path }: { overview: ConfigOverview | null; path: string }) {
  if (!overview?.available) return null;
  const kinds = overview.kinds.filter((k) => kindShown(overview, k));
  const on = (href: string) => path === href || (href !== "/config" && path.startsWith(href + "/"));
  return (
    <nav className="cf-subnav" aria-label="Configuration">
      <a href="/config" onClick={onLink} className={"cf-subnav-link" + (path === "/config" ? " cf-subnav-on" : "")} aria-current={path === "/config" ? "page" : undefined}>Overview</a>
      {kinds.map((k) => {
        const href = `/config/${k.path}`;
        return (
          <a key={k.name} href={href} onClick={onLink} className={"cf-subnav-link" + (on(href) ? " cf-subnav-on" : "")} aria-current={on(href) ? "page" : undefined}>
            {kindLabel(overview, k)}
          </a>
        );
      })}
    </nav>
  );
}
