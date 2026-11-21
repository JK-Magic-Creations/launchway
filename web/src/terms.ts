// The words Launchway uses for its own concepts: one source of labels. Every place the interface shows one of
// these words asks this module, so an installation can choose the aviation set (the default) or the plain
// business set, and rename any single word. They are display labels only: API paths, JSON fields, pipeline
// keys, CLI commands, configuration kinds, audit actions and database columns keep their plain forms. Words
// that are broadly accepted (project, pipeline, step, run, environment, template, library, variable, branch,
// commit, pull request, secret) are not here: they are never themed.
//
// The server (internal/settings/terms.go) stores the choice and the renames in the installation settings and
// knows the same keys, to refuse a rename of a word that does not exist.

import { useSyncExternalStore } from "react";

export type SetId = "aviation" | "business";

/** Each word has a singular and an optional plural (otherwise it is made from the singular). */
type Word = string | [string, string];

/** The keys, in the order they are shown on the Words page, with what each one means in plain English. */
export const TERM_META: { key: string; means: string }[] = [
  { key: "installation", means: "The whole Launchway installation: the top level." },
  { key: "organization", means: "The group of teams that owns projects: the second level." },
  { key: "unit", means: "A group of projects inside an organization: the third level." },
  { key: "project", means: "One piece of software with its pipelines: the fourth level." },
  { key: "guide", means: "The helper and guide panel beside every page." },
  { key: "preflight", means: "The checks made before a deploy." },
  { key: "checklist", means: "The list of things that must be true before a change goes on." },
  { key: "clearance", means: "An approval: a person decides whether a change may go on." },
  { key: "go", means: "The button that approves a change." },
  { key: "no_go", means: "The button that refuses a change." },
  { key: "hold", means: "A freeze: a time when changes may not go out." },
  { key: "flight_plan", means: "The ready-made pipeline for a kind of project." },
  { key: "maneuver", means: "A reusable step, or a few steps, that a pipeline uses." },
  { key: "hangar", means: "The library of Flight plans and Maneuvers that ships with Launchway." },
  { key: "logbook", means: "The audit trail: who did what, and when." },
  { key: "payload", means: "What a change delivers." },
  { key: "in_flight", means: "How a change on its way to production is described." },
  { key: "launch_line", means: "The line a change travels along, from commit to production." },
];

export const SETS: Record<SetId, { name: string; blurb: string; words: Record<string, Word> }> = {
  aviation: {
    name: "Aviation",
    blurb: "Base, Wing, Squadron, Project. Wingman, Preflight, Clearance, Logbook. The default.",
    words: {
      installation: ["Base", "Bases"],
      organization: ["Wing", "Wings"],
      unit: ["Squadron", "Squadrons"],
      project: ["Project", "Projects"],
      guide: "Wingman",
      preflight: "Preflight",
      checklist: "Checklist",
      clearance: "Clearance",
      go: "Go",
      no_go: "No-go",
      hold: "Hold",
      flight_plan: "Flight plan",
      maneuver: "Maneuver",
      hangar: "Hangar",
      logbook: "Logbook",
      payload: "Payload",
      in_flight: "In flight",
      launch_line: "Launch line",
    },
  },
  business: {
    name: "Plain business",
    blurb: "Installation, Organization, Team, Project. Guide, Checks, Approval, Audit trail.",
    words: {
      installation: ["Installation", "Installations"],
      organization: ["Organization", "Organizations"],
      unit: ["Team", "Teams"],
      project: ["Project", "Projects"],
      guide: "Guide",
      preflight: "Checks",
      checklist: "Checklist",
      clearance: "Approval",
      go: "Approve",
      no_go: "Reject",
      hold: "Freeze",
      flight_plan: "Pipeline template",
      maneuver: "Step template",
      hangar: "Template library",
      logbook: "Audit trail",
      payload: "Contents",
      in_flight: "In progress",
      launch_line: "Launch line",
    },
  },
};

export const DEFAULT_SET: SetId = "aviation";

/** What the installation settings hold: the chosen set and the renamed words. */
export interface TermsConfig {
  set?: string;
  overrides?: Record<string, string>;
  /** Level names an older installation set under `levels`: they apply under the set and under the renames. */
  levels?: Partial<Record<"installation" | "organisation" | "unit" | "project", { singular?: string; plural?: string }>>;
}

export function isSetId(s: unknown): s is SetId { return s === "aviation" || s === "business"; }

/** The same rule the server applies to a label. */
export function termProblem(v: string): string | null {
  if (v.trim() === "") return "Give it at least one character, or reset it.";
  if (v.length > 40) return "Use at most 40 characters.";
  if (/[<>&\n\r\t]/.test(v)) return "Use plain words: no < > or & and one line only.";
  return null;
}

function pluralise(s: string): string {
  if (/[^aeiou]y$/i.test(s)) return s.slice(0, -1) + "ies";
  if (/(s|x|ch|sh)$/i.test(s)) return s + "es";
  return s + "s";
}

/** Lower-case a label for the middle of a sentence, unless it has a capital inside (an acronym or a name). */
function lowerWord(s: string): string {
  return /[A-Z]/.test(s.slice(1)) ? s : s.charAt(0).toLowerCase() + s.slice(1);
}

const LEVEL_KEYS: Record<string, string> = { installation: "installation", organisation: "organization", unit: "unit", project: "project" };

export interface Terms {
  readonly set: SetId;
  readonly overrides: Readonly<Record<string, string>>;
  /** The label, as written: "Flight plan". An unknown key gives its own name, readably. */
  label(key: string): string;
  /** The label in the middle of a sentence: "flight plan". */
  lower(key: string): string;
  plural(key: string): string;
  pluralLower(key: string): string;
  /** Replace {key}, {Key} and {keys} in a sentence: "Waiting for a {clearance}". */
  say(text: string): string;
}

/** makeTerms is pure: the same configuration always gives the same words. */
export function makeTerms(cfg: TermsConfig = {}): Terms {
  const set: SetId = isSetId(cfg.set) ? cfg.set : DEFAULT_SET;
  const overrides: Record<string, string> = {};
  for (const [k, v] of Object.entries(cfg.overrides ?? {})) if (typeof v === "string" && v.trim() !== "") overrides[k] = v;
  const base = SETS[set].words;
  const legacy: Record<string, { singular?: string; plural?: string }> = {};
  for (const [lk, v] of Object.entries(cfg.levels ?? {})) if (v && LEVEL_KEYS[lk]) legacy[LEVEL_KEYS[lk]] = v;

  const single = (key: string): string => {
    if (overrides[key]) return overrides[key];
    const l = legacy[key];
    // The built-in default of the plain level names is what the server reports for an installation that never
    // renamed anything; it must not hide the chosen set, so a legacy name counts only when it differs from the
    // two sets' own words.
    if (l?.singular && !isBuiltInLevel(key, l.singular)) return l.singular;
    const w = base[key];
    if (w === undefined) return key.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
    return Array.isArray(w) ? w[0] : w;
  };
  const plural = (key: string): string => {
    if (overrides[key + "_plural"]) return overrides[key + "_plural"];
    if (overrides[key]) return pluralise(overrides[key]);
    const l = legacy[key];
    if (l?.singular && !isBuiltInLevel(key, l.singular)) return l.plural || pluralise(l.singular);
    const w = base[key];
    if (Array.isArray(w)) return w[1];
    return pluralise(single(key));
  };
  const t: Terms = {
    set, overrides,
    label: single,
    lower: (k) => lowerWord(single(k)),
    plural,
    pluralLower: (k) => lowerWord(plural(k)),
    say(text) {
      return text.replace(/\{([A-Za-z_]+)\}/g, (all, raw: string) => {
        const first = raw.charAt(0), cap = first !== first.toLowerCase();
        const k = raw.charAt(0).toLowerCase() + raw.slice(1);
        const known = (x: string) => x in base;
        if (known(k)) return cap ? single(k) : t.lower(k);
        if (k.endsWith("s") && known(k.slice(0, -1))) return cap ? plural(k.slice(0, -1)) : t.pluralLower(k.slice(0, -1));
        return all;
      });
    },
  };
  return t;
}

function isBuiltInLevel(key: string, singular: string): boolean {
  return ["aviation", "business"].some((s) => {
    const w = SETS[s as SetId].words[key];
    return (Array.isArray(w) ? w[0] : w) === singular;
  }) || singular === "Organisation" || singular === "Unit";
}

// ---- the words in effect ----

let current: Terms = makeTerms();
let version = 0;
const listeners = new Set<() => void>();

/** setTerms is called when the installation settings are read or changed. It says nothing when the words are the same. */
export function setTerms(cfg: TermsConfig): void {
  const next = makeTerms(cfg);
  if (next.set === current.set && JSON.stringify(next.overrides) === JSON.stringify(current.overrides)
    && SETS[next.set].words === SETS[current.set].words && sameLevels(next, current)) return;
  current = next; version++;
  listeners.forEach((l) => l());
}

function sameLevels(a: Terms, b: Terms): boolean {
  return ["installation", "organization", "unit", "project"].every((k) => a.label(k) === b.label(k) && a.plural(k) === b.plural(k));
}

/** The words in effect, for code that is not a component (data tables, tests). */
export function terms(): Terms { return current; }

/** The words in effect, and the page redraws when they change. */
export function useTerms(): Terms {
  useSyncExternalStore((l) => { listeners.add(l); return () => { listeners.delete(l); }; }, () => version);
  return current;
}

/** A number that changes whenever the words do: used as a key so every screen redraws. */
export function useTermsVersion(): number {
  return useSyncExternalStore((l) => { listeners.add(l); return () => { listeners.delete(l); }; }, () => version);
}

/** Back to the built-in words (tests, sign-out). */
export function resetTerms(): void { setTerms({}); }
