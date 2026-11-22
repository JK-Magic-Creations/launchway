// ConfigNav is the configuration section's own menu: home, the guided
// set-ups, and each kind of object, named with the configured level names.

import type { ConfigOverview } from "../configApi";
import { kindLabel, kindShown } from "../configApi";
import { onLink } from "../router";
import { prose } from "../prose";
const P = await prose("components.ConfigNav");

export interface WizardInfo { id: string; title: string; kind?: string; text: string; /** Where the card leads when it is not the wizard itself. */ href?: string }

export const WIZARDS: WizardInfo[] = [
  { id: "connect-repository", title: "Connect a repository", kind: "project", href: "/projects/new", text: P.t0 },
  { id: "add-runner", title: "Add a runner", kind: "runner-pool", text: P.t1 },
  { id: "add-environment", title: "Add an environment", kind: "environment", text: P.t2 },
  { id: "add-policy", title: "Add a policy", kind: "policy", text: P.t3 },
  { id: "add-health-source", title: "Add a health source", kind: "health-source", text: P.t4 },
  { id: "add-model", title: "Add an AI model", kind: "model", text: P.t5 },
  { id: "organize-squadrons", title: "Organize into {Units}", href: "/units/organize", text: P.t6 },
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
