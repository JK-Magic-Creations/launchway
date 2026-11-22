import { useCallback, useMemo } from "react";
import { setUrl, useSearch } from "../urlState";

/** The Launch page's view in its address: a few search parameters, so a view can be bookmarked and shared. It is the
 * address of src/urlState.ts, shared with every other page: choosing a tab makes a history entry (Back returns to the
 * one before), a selection inside a tab replaces the address, and a link or a reload opens the same view. */
export function useUrlState<K extends string>(keys: readonly K[]): [Record<K, string>, (patch: Partial<Record<K, string>>) => void] {
  const search = useSearch();
  const state = useMemo(() => {
    const p = new URLSearchParams(search);
    return Object.fromEntries(keys.map((k) => [k, p.get(k) ?? ""])) as Record<K, string>;
  }, [search, keys]);
  const set = useCallback((patch: Partial<Record<K, string>>) => {
    const changes: Record<string, string | null> = {};
    for (const [k, v] of Object.entries(patch) as [K, string | undefined][]) changes[k] = v || null;
    setUrl(changes, "tab" in patch ? "push" : "replace");
  }, []);
  return [state, set];
}
