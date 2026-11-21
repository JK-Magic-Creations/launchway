import { useCallback, useEffect, useSyncExternalStore } from "react";

// Every view has an address. A tab, a filter, the text in a search box, a sort, a grouping, a cards/table switch,
// a selected item: whatever changes what a page shows lives in the query string, so a link or a bookmark opens the
// same view and Back and Forward move between views. useUrlState is the one way to do it:
//   - a value equal to its default is left out of the address, so links stay short;
//   - a value that cannot be read (a hand-edited address) falls back to the default and never throws;
//   - typing changes the address with "replace" (it must not fill the history); choosing a tab or an item
//     "push"es (Back returns to the one before);
//   - names are plain words: ?tab=tests&status=failed&node=payments/api.
// Personal layout choices (which sections are hidden, a remembered cards/table choice) are saved preferences, not
// part of the address, unless a person uses "Copy link to this view".

type Listener = () => void;
const listeners = new Set<Listener>();
function subscribe(l: Listener): () => void {
  listeners.add(l);
  window.addEventListener("popstate", l);
  return () => { listeners.delete(l); window.removeEventListener("popstate", l); };
}
const snapshot = () => location.search;

/** A codec reads a value from the address text and writes it back. `parse` returns undefined for a bad value. */
export interface Codec<T> { parse: (s: string) => T | undefined; format: (v: T) => string }

export const str: Codec<string> = { parse: (s) => s, format: (v) => v };
export const bool: Codec<boolean> = { parse: (s) => (s === "1" || s === "true" ? true : s === "0" || s === "false" ? false : undefined), format: (v) => (v ? "1" : "0") };
export const int = (min = 0, max = 1e9): Codec<number> => ({ parse: (s) => { const n = Number(s); return Number.isInteger(n) && n >= min && n <= max ? n : undefined; }, format: String });
export function oneOf<T extends string>(...values: T[]): Codec<T> { return { parse: (s) => (values as string[]).includes(s) ? (s as T) : undefined, format: (v) => v }; }

/** Reads one parameter now (for code outside a component). */
export function readUrl<T>(key: string, def: T, codec: Codec<T>): T {
  const raw = new URLSearchParams(location.search).get(key);
  if (raw === null) return def;
  return codec.parse(raw) ?? def;
}

/** Changes parameters of the current address. null removes one. "push" adds a history entry; "replace" does not. */
export function setUrl(changes: Record<string, string | null>, mode: "push" | "replace" = "replace"): void {
  const p = new URLSearchParams(location.search);
  for (const [k, v] of Object.entries(changes)) { if (v === null || v === "") p.delete(k); else p.set(k, v); }
  const qs = p.toString().replace(/%2F/gi, "/").replace(/%3A/gi, ":");
  const next = location.pathname + (qs ? "?" + qs : "") + location.hash;
  if (next === location.pathname + location.search + location.hash) return;
  if (mode === "push") history.pushState(null, "", next); else history.replaceState(null, "", next);
  listeners.forEach((l) => l());
}

/** A value kept in the address: `const [tab, setTab] = useUrlState("tab", "overview", oneOf("overview", "tests"))`. */
export function useUrlState<T>(key: string, def: T, codec: Codec<T> = str as unknown as Codec<T>): [T, (v: T, mode?: "push" | "replace") => void] {
  const search = useSyncExternalStore(subscribe, snapshot, () => "");
  const raw = new URLSearchParams(search).get(key);
  const value = raw === null ? def : codec.parse(raw) ?? def;
  const set = useCallback((v: T, mode: "push" | "replace" = "replace") => {
    setUrl({ [key]: v === def || codec.format(v) === codec.format(def) ? null : codec.format(v) }, mode);
  }, [key, def, codec]);
  return [value, set];
}

// ---- the title of the view ----
// A page names its view ("Tests", "History") so a bookmark and a history entry read well; the shell puts it before the page's own title.
let viewTitle = "";
const titleListeners = new Set<() => void>();
export function currentViewTitle(): string { return viewTitle; }
export function onViewTitle(l: () => void): () => void { titleListeners.add(l); return () => { titleListeners.delete(l); }; }
/** Names the view the page is showing; "" (or leaving the page) goes back to the page's own title. */
export function useViewTitle(title: string): void {
  useEffect(() => {
    viewTitle = title; titleListeners.forEach((l) => l());
    return () => { viewTitle = ""; titleListeners.forEach((l) => l()); };
  }, [title]);
}
