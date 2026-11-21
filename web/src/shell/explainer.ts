import { createContext, useContext, useEffect } from "react";

/** A page that can offer its own explanation (the run page, for a failed step) puts it here; the Guide
 * shows it as a button, only when a model is configured. */
export interface Explainer { label: string; run: () => Promise<string> }
export const ExplainerContext = createContext<(e: Explainer | null) => void>(() => {});
export function useRegisterExplainer(e: Explainer | null): void {
  const set = useContext(ExplainerContext);
  const label = e?.label;
  useEffect(() => { set(e); return () => set(null); }, [label]); // eslint-disable-line react-hooks/exhaustive-deps
}
