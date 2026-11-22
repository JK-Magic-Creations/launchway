import type { ComponentType } from "react";
import type { HomeAnswer } from "../../organizeApi";

// The panel contract. Home is a grid of panels in three columns. A panel is a typed card with a title, a size (1, 2 or 3 columns),
// settings, and its own loading, empty and error states. Each type is a lazy chunk, so a panel nobody has costs nothing.
//
// A later feature (work items, releases, a registry, an extension) adds a panel by adding one entry to PANEL_TYPES and a component
// that takes PanelProps; it does not touch the Home page. The arrangement (which panels, in what order, how big, and their settings)
// is a layout document (page "home", key "panels" and "order"), so the shared default for a level and each person's own
// arrangement apply to it with no more code. See docs/contributing/panels.md.

export type Size = 1 | 2 | 3;

export interface PanelProps<S = Record<string, unknown>> {
  id: string;
  title: string;
  settings: S;
  size: Size;
  /** The one aggregated answer Home was drawn from (needs, running, projects, favorites). */
  home: HomeAnswer;
  now: number;
}

/** One field a person fills in when they add a panel of this type. */
export interface Field { key: string; label: string; kind: "text" | "textarea" | "project" | "address"; help?: string; required?: boolean }

export interface PanelType {
  type: string;
  /** What it is called in the "Add a panel" list, and what it shows. */
  label: string;
  description: string;
  defaultSize: Size;
  fields: Field[];
  /** The component, loaded when a panel of the type is first drawn. */
  load: () => Promise<{ default: ComponentType<PanelProps> }>;
}

export const PANEL_TYPES: PanelType[] = [
  { type: "continue", label: "Pick up where you left off", description: "What you started or opened lately, with what changed since, and your recent history.", defaultSize: 3, fields: [],
    load: () => import("./ContinuePanel") as never },
  { type: "pinned", label: "A saved view", description: "A link to a filtered list or any view of a page, kept on Home.", defaultSize: 1,
    fields: [{ key: "href", label: "Address", kind: "address", help: "An address on this server, such as /pipelines?result=bad", required: true }], load: () => import("./PinnedPanel") as never },
  { type: "project", label: "One project", description: "The status of a single project: its latest result, what runs, what needs a look.", defaultSize: 1,
    fields: [{ key: "project", label: "Project", kind: "project", required: true }], load: () => import("./ProjectPanel") as never },
  { type: "note", label: "Notes and links", description: "Text with links, for example a team's runbook. Written in Markdown; only safe formatting is shown.", defaultSize: 2,
    fields: [{ key: "text", label: "Text", kind: "textarea", help: "Markdown: **bold**, `code`, [links](https://example.com), lists and headings.", required: true }], load: () => import("./NotePanel") as never },
];

export const panelType = (type: string): PanelType | undefined => PANEL_TYPES.find((t) => t.type === type);

/** A panel as the layout document keeps it. */
export interface PanelDef { id: string; type: string; title: string; size: Size; settings: Record<string, unknown> }

/** Checks what a layout document says about its panels, so a hand-edited or newer document never breaks Home. */
export function cleanPanels(raw: unknown): PanelDef[] {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  const out: PanelDef[] = [];
  for (const p of raw.slice(0, 40)) {
    if (!p || typeof p !== "object") continue;
    const o = p as Record<string, unknown>;
    const id = typeof o.id === "string" && /^[a-z0-9:_-]{1,40}$/i.test(o.id) ? o.id : "";
    if (!id || seen.has(id) || !panelType(String(o.type))) continue;
    seen.add(id);
    const size = o.size === 1 || o.size === 2 || o.size === 3 ? o.size : panelType(String(o.type))!.defaultSize;
    out.push({ id, type: String(o.type), title: typeof o.title === "string" ? o.title.slice(0, 80) : panelType(String(o.type))!.label, size, settings: o.settings && typeof o.settings === "object" ? (o.settings as Record<string, unknown>) : {} });
  }
  return out;
}
