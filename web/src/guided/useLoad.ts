import { useCallback, useEffect, useRef, useState } from "react";

export interface Loaded<T> { data: T | null; error: unknown; loading: boolean; reload: () => void }

/** useLoad runs a read when the page opens (and when fn changes), again every `every` ms in the
 * background when asked, and on reload(). fn must be stable (useCallback). A failed read keeps what was loaded. */
export function useLoad<T>(fn: (background: boolean) => Promise<T>, every = 0): Loaded<T> {
  const [state, setState] = useState<{ data: T | null; error: unknown; loading: boolean }>({ data: null, error: null, loading: true });
  const alive = useRef(true);
  const run = useCallback((background: boolean) => {
    fn(background).then((data) => { if (alive.current) setState({ data, error: null, loading: false }); })
      .catch((error) => { if (alive.current) setState((s) => ({ data: s.data, error, loading: false })); });
  }, [fn]);
  useEffect(() => {
    alive.current = true;
    run(false);
    const t = every > 0 ? window.setInterval(() => run(true), every) : 0;
    return () => { alive.current = false; if (t) window.clearInterval(t); };
  }, [run, every]);
  const reload = useCallback(() => run(true), [run]);
  return { ...state, reload };
}
