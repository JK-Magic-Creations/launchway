// The words Launchway uses for its own concepts: one source of labels. Every place the interface shows one of
// these words asks this module, so an installation can choose the aviation set (the default) or the plain
// business set, and rename any single word. They are display labels only: API paths, JSON fields, enum values,
// pipeline keys, CLI commands and flags, configuration kinds, audit actions, stored role names, page addresses
// and database columns keep their plain forms. Words that are broadly accepted (pipeline, step, alert, branch,
// commit, pull request, secret, variable) are not in the table: they are never themed.
//
// The table lives in ONE place, the server (internal/settings/vocabulary.go). The overview carries it
// (loadOverview calls setVocabulary) and web/src/vocabulary.json is a generated mirror of it, loaded lazily at
// start so the words never weigh on the first download (loadVocabulary).

import { useSyncExternalStore } from "react";

export type SetId = "aviation" | "business";

/** The shapes of one word in one set. one and many are capitalized as a name; the verb forms are lower case. */
export interface Forms { one: string; many?: string; do?: string; did?: string; doing?: string }

/** One concept: a stable key, its word in each set, and a plain meaning. */
export interface WordDef {
  key: string; group: string; aviation: Forms; plain: Forms; meaning: string; renameable?: boolean;
}
export interface Group { ID: string; Title: string }

let TABLE: WordDef[] = [];
let GROUPS: Group[] = [];
let BY_KEY = new Map<string, WordDef>();

/** The whole table, in the order of the Words page. */
export function vocabulary(): readonly WordDef[] { return TABLE; }
export function vocabularyGroups(): readonly Group[] { return GROUPS; }
export function wordDef(key: string): WordDef | undefined { return BY_KEY.get(key); }
/** The keys an installation may rename. */
export function renameableKeys(): string[] { return TABLE.filter((w) => w.renameable).map((w) => w.key); }

/** setVocabulary installs the table (from the overview or the generated file) and redraws the words. */
export function setVocabulary(words: readonly WordDef[], groups?: readonly Group[]): void {
  if (words === TABLE || !words.length) return;
  TABLE = [...words];
  if (groups) GROUPS = [...groups];
  BY_KEY = new Map(TABLE.map((w) => [w.key, w]));
  current = makeTerms(lastConfig); version++;
  listeners.forEach((l) => l());
}

/** loadVocabulary reads the generated mirror (a separate chunk, so it is not part of the first download). */
export function loadVocabulary(): Promise<void> {
  return import("./vocabulary.json").then((m) => { const d = (m.default ?? m) as { groups: Group[]; words: WordDef[] }; setVocabulary(d.words, d.groups); });
}

export const SETS: Record<SetId, { name: string; blurb: string }> = {
  aviation: { name: "Aviation", blurb: "Base, Wing, Squadron, Project. Tower, Sortie, Station, Launch. The default." },
  business: { name: "Plain business", blurb: "Installation, Organization, Team, Project. Home, Run, Runner, Deploy. Ordinary words only." },
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

/** The same word in the middle of a sentence: a name keeps its capital; plain English is lower case. */
function midWord(set: SetId, def: WordDef | undefined, s: string): string {
  if (def && (set === "aviation" || def.group === "levels")) return s;
  if (/[A-Z]/.test(s.slice(1))) return s;
  return s.charAt(0).toLowerCase() + s.slice(1);
}

const LEVEL_KEYS: Record<string, string> = { installation: "installation", organisation: "organization", unit: "unit", project: "project" };

export type VerbForm = "do" | "does" | "did" | "doing";

export interface Terms {
  readonly set: SetId;
  readonly overrides: Readonly<Record<string, string>>;
  /** The word as a name, singular: "Sortie". An unknown key gives its own name, readably. */
  label(key: string): string;
  /** The same (the helper's main call): t.word("run"). */
  word(key: string): string;
  /** The word in the middle of a sentence: a name keeps its capital in the aviation set; the plain set reads as plain English. */
  lower(key: string): string;
  plural(key: string): string;
  /** The plural in the middle of a sentence. */
  pluralLower(key: string): string;
  /** A verb form, lower case: t.verb("deploy", "did") is "launched" (aviation) or "deployed" (business). */
  verb(key: string, form?: VerbForm): string;
  /** The word's verb as a button label: "Launch", "Return to base", "Abort". */
  action(key: string): string;
  /** The plain word of either set, whatever the set in use: for descriptions, confirmations and statuses. */
  plain(key: string): string;
  /** The plain meaning, for a tooltip. */
  meaning(key: string): string;
  /** Whether the word shown differs from its plain word (so a tooltip or a plain word beside it is worth showing). */
  themed(key: string): boolean;
  /** Replace {key}, {Key}, {keys}, {key:did}, {key:do}, {key:doing} and {key:plain} in a sentence. */
  say(text: string): string;
}

/** makeTerms is pure: the same configuration always gives the same words. */
export function makeTerms(cfg: TermsConfig = {}): Terms {
  const set: SetId = isSetId(cfg.set) ? cfg.set : DEFAULT_SET;
  const overrides: Record<string, string> = {};
  for (const [k, v] of Object.entries(cfg.overrides ?? {})) if (typeof v === "string" && v.trim() !== "") overrides[k] = v;
  const legacy: Record<string, { singular?: string; plural?: string }> = {};
  for (const [lk, v] of Object.entries(cfg.levels ?? {})) if (v && LEVEL_KEYS[lk]) legacy[LEVEL_KEYS[lk]] = v;
  const forms = (key: string, s: SetId = set): Forms | undefined => { const d = BY_KEY.get(key); return d ? (s === "business" ? d.plain : d.aviation) : undefined; };
  const readable = (key: string) => key.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
  const builtIn = (key: string, singular: string) => singular === "Organisation" || singular === "Unit" || (["aviation", "business"] as SetId[]).some((s) => forms(key, s)?.one === singular);

  const single = (key: string): string => {
    if (overrides[key]) return overrides[key];
    const l = legacy[key];
    // A level name the server reports for an installation that never renamed anything must not hide the chosen
    // set, so a legacy name counts only when it differs from the two sets' own words.
    if (l?.singular && !builtIn(key, l.singular)) return l.singular;
    return forms(key)?.one ?? readable(key);
  };
  const plural = (key: string): string => {
    if (overrides[key + "_plural"]) return overrides[key + "_plural"];
    if (overrides[key]) return pluralise(overrides[key]);
    const l = legacy[key];
    if (l?.singular && !builtIn(key, l.singular)) return l.plural || pluralise(l.singular);
    const f = forms(key);
    return f ? (f.many || pluralise(f.one)) : pluralise(single(key));
  };
  const mid = (key: string, s: string) => midWord(set, BY_KEY.get(key), s);
  const verb = (key: string, form: VerbForm = "do"): string => {
    const f = forms(key);
    if (form === "does") { const b = verb(key, "do"); return /(s|x|ch|sh)$/.test(b) ? b + "es" : b + "s"; }
    const v = !overrides[key] && f ? f[form] : undefined;
    return v || single(key).toLowerCase();
  };
  const t: Terms = {
    set, overrides,
    label: single, word: single,
    lower: (k) => mid(k, single(k)),
    plural,
    pluralLower: (k) => mid(k, plural(k)),
    verb,
    action: (k) => { const v = verb(k, "do"); return v.charAt(0).toUpperCase() + v.slice(1); },
    plain: (k) => BY_KEY.get(k)?.plain.one ?? single(k),
    meaning: (k) => BY_KEY.get(k)?.meaning ?? "",
    themed: (k) => single(k) !== (BY_KEY.get(k)?.plain.one ?? single(k)),
    say(text) {
      const fill = (all: string, raw: string, mod?: string): string => {
        const cap = raw.charAt(0) !== raw.charAt(0).toLowerCase();
        const k = raw.charAt(0).toLowerCase() + raw.slice(1);
        const known = (x: string) => BY_KEY.has(x);
        if (known(k)) {
          if (mod === "plain") return t.plain(k).toLowerCase();
          if (mod) { const v = verb(k, mod as VerbForm); return cap ? v.charAt(0).toUpperCase() + v.slice(1) : v; }
          return cap ? single(k) : t.lower(k);
        }
        if (!mod && k.endsWith("s") && known(k.slice(0, -1))) return cap ? plural(k.slice(0, -1)) : t.pluralLower(k.slice(0, -1));
        return all;
      };
      // "a {environment}" becomes "an Airfield" or "a Station": the article follows the word that is shown.
      return text
        .replace(/\b([Aa])n? (\{([A-Za-z_]+)(?::(do|does|did|doing|plain))?\})/g, (all, a: string, ph: string, raw: string, mod?: string) => {
          const out = fill(ph, raw, mod);
          return out === ph ? all : `${a}${/^[aeiou]/i.test(out) ? "n" : ""} ${out}`;
        })
        .replace(/\{([A-Za-z_]+)(?::(do|does|did|doing|plain))?\}/g, (all, raw: string, mod?: string) => fill(all, raw, mod));
    },
  };
  return t;
}

// ---- the words in effect ----

let lastConfig: TermsConfig = {};
let current: Terms = makeTerms();
let version = 0;
const listeners = new Set<() => void>();

/** setTerms is called when the installation settings are read or changed. It says nothing when the words are the same. */
export function setTerms(cfg: TermsConfig): void {
  const next = makeTerms(cfg);
  lastConfig = cfg;
  if (next.set === current.set && JSON.stringify(next.overrides) === JSON.stringify(current.overrides) && sameLevels(next, current)) return;
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
