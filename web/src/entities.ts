// Every thing a person sees has a name and an address. This is the one
// place that knows the addresses, so that a name is never written as plain
// text where a link would do (see EntityLink and its test).

export type EntityKind = "project" | "pipeline" | "run" | "environment" | "application" | "template" | "image" | "component" | "runner";

const enc = encodeURIComponent;

export interface EntityRef {
  kind: EntityKind;
  /** The name shown, and, unless id is given, the name in the address. */
  name: string;
  /** What the address is made from when it is not the name (a run's id, a template's library). */
  id?: string;
  /** A template belongs to a library. */
  library?: string;
}

export function entityHref(e: EntityRef): string {
  switch (e.kind) {
    case "project": return `/projects/${enc(e.id ?? e.name)}`;
    case "pipeline": return `/projects/${enc(e.id ?? e.name)}/pipeline`;
    case "run": return `/runs/${enc(e.id ?? e.name)}`;
    case "environment": return `/deployments/environments/${enc(e.id ?? e.name)}`;
    case "application": return `/deployments/applications/${enc(e.id ?? e.name)}`;
    case "template": return `/library/templates/${enc(e.library ?? "")}/${enc(e.id ?? e.name)}`;
    case "image": return `/images/digest/${enc(e.id ?? e.name)}`;
    case "component": return `/images/components?name=${enc(e.id ?? e.name)}`;
    case "runner": return `/settings/runners#${enc(e.id ?? e.name)}`;
  }
}

/** The word for a kind, for a link's title: "Open the project shop-api". */
export const KIND_WORD: Record<EntityKind, string> = {
  project: "project", pipeline: "pipeline", run: "run", environment: "environment", application: "application",
  template: "template", image: "image", component: "component", runner: "runner",
};
