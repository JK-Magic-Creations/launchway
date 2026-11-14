import { useCallback, useEffect, useRef, useState } from "react";
import { api, type Approval, type RunSummary } from "./api";

// useStream subscribes to the server's live event stream. Handlers are kept
// in a ref so callers can pass inline functions without reconnecting.
// onOpen runs on every (re)connection, so callers can refetch anything they
// may have missed while disconnected.
export function useStream(
  url: string | null,
  handlers: Record<string, (data: unknown) => void>,
  onOpen?: () => void,
): boolean {
  const ref = useRef(handlers);
  ref.current = handlers;
  const openRef = useRef(onOpen);
  openRef.current = onOpen;
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    if (!url) return;
    const es = new EventSource(url);
    let first = true;
    es.onopen = () => {
      setConnected(true);
      if (!first) openRef.current?.();
      first = false;
    };
    es.onerror = () => setConnected(false);
    const types = ["run", "step", "log"];
    const listeners = types.map((t) => {
      const fn = (e: MessageEvent) => {
        try { ref.current[t]?.(JSON.parse(e.data)); } catch { /* ignore malformed */ }
      };
      es.addEventListener(t, fn as EventListener);
      return [t, fn] as const;
    });
    return () => {
      listeners.forEach(([t, fn]) => es.removeEventListener(t, fn as EventListener));
      es.close();
    };
  }, [url]);
  return connected;
}

// useNow re-renders every interval, for live durations.
export function useNow(intervalMs = 1000, active = true): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  }, [intervalMs, active]);
  return now;
}

export interface Approvals {
  list: Approval[] | null; // null until first loaded
  error: unknown;
  live: boolean;
}

// useApprovals keeps the list of approvals waiting for a decision up to
// date. A run's live summary says which of its steps are waiting, so the
// list is fetched again whenever a run starts or stops waiting.
export function useApprovals(): Approvals {
  const [list, setList] = useState<Approval[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const timer = useRef<number | undefined>(undefined);
  const listRef = useRef(list);
  listRef.current = list;

  const load = useCallback(() => {
    api.approvals().then((l) => { setList(l); setError(null); }).catch(setError);
  }, []);
  useEffect(() => {
    load();
    return () => window.clearTimeout(timer.current);
  }, [load]);

  const live = useStream("/api/v1/stream", {
    run: (d) => {
      const r = d as RunSummary;
      if ((r.waiting?.length ?? 0) > 0 || listRef.current?.some((a) => a.run_id === r.id)) {
        // Several changes often arrive together; fetch once for all of them.
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(load, 150);
      }
    },
  }, load);
  return { list, error, live };
}
