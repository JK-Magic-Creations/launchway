import { createContext, useContext, useEffect } from "react";

/** A page that knows its thing's real name (a run's number, a project's title) says it here,
 * and the last crumb shows it instead of what the address alone could say. */
export const CrumbLabelContext = createContext<(label: string | undefined) => void>(() => {});

export function useCrumbLabel(label: string | undefined) {
  const set = useContext(CrumbLabelContext);
  useEffect(() => {
    set(label);
    return () => set(undefined);
  }, [label, set]);
}
