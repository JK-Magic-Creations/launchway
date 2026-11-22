import { useCallback, useEffect, useState } from "react";

/** The page's view in its address: a few search parameters, so a view can be bookmarked and shared. Changing one
 * replaces the address (it does not add history entries for every click on a tab or a tree row). */
export function useUrlState<K extends string>(keys: readonly K[]): [Record<K, string>, (patch: Partial<Record<K, string>>) => void] {
  const read = useCallback(() => {
    const p = new URLSearchParams(location.search);
    return Object.fromEntries(keys.map((k) => [k, p.get(k) ?? ""])) as Record<K, string>;
  }, [keys]);
  const [state, setState] = useState(read);
  useEffect(() => {
    const on = () => setState(read());
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, [read]);
  const set = useCallback((patch: Partial<Record<K, string>>) => {
    const p = new URLSearchParams(location.search);
    for (const [k, v] of Object.entries(patch) as [K, string | undefined][]) {
      if (v) p.set(k, v); else p.delete(k);
    }
    const qs = p.toString();
    history.replaceState(null, "", location.pathname + (qs ? "?" + qs : "") + location.hash);
    setState(read());
  }, [read]);
  return [state, set];
}
