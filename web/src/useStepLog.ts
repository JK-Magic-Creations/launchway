import { useCallback, useEffect, useRef, useState, type MutableRefObject } from "react";
import { api, type LogLine } from "./api";

/** The newest lines kept in a step's log view; earlier ones are let go (they stay in the full log). */
const KEEP = 20000;

interface Held { key: string; lines: LogLine[]; dropped: number; loaded: boolean }

/** useStepLog holds a step attempt's log lines: loaded once, then appended to from the live stream. A batch that
 * does not follow on from the last line (the stream missed some) or a reconnect makes it fetch what is missing,
 * by sequence number. */
export function useStepLog(runID: string, stepID: string, attempt: number, sink: MutableRefObject<((step: string, attempt: number, lines: LogLine[]) => void) | null>) {
  const key = `${runID}/${stepID}/${attempt}`;
  const [held, setHeld] = useState<Held>({ key, lines: [], dropped: 0, loaded: false });
  const alive = useRef(0);
  const last = useRef(0);
  // What is held belongs to another step until its own load answers: show nothing of it.
  const mine = held.key === key ? held : { key, lines: [], dropped: 0, loaded: false };

  const append = useCallback((incoming: LogLine[]) => {
    setHeld((h) => {
      if (h.key !== key) return h;
      const seen = h.lines.length ? h.lines[h.lines.length - 1].seq : 0;
      const fresh = incoming.filter((l) => l.seq > seen);
      if (!fresh.length) return h;
      let all = h.lines.concat(fresh);
      let dropped = h.dropped;
      if (all.length > KEEP) { dropped += all.length - KEEP; all = all.slice(all.length - KEEP); }
      last.current = all[all.length - 1].seq;
      return { ...h, lines: all, dropped };
    });
  }, [key]);

  // Fetch what the view lacks after its last line: after a gap in the stream, or a reconnect.
  const catchUp = useCallback(async (token: number) => {
    for (;;) {
      const r = await api.logs(runID, stepID, attempt, last.current).catch(() => null);
      if (!r || alive.current !== token || r.lines.length === 0) return;
      append(r.lines);
      if (r.lines.length < 5000) return;
    }
  }, [runID, stepID, attempt, append]);

  useEffect(() => {
    const token = ++alive.current;
    last.current = 0;
    (async () => {
      let after = 0;
      let all: LogLine[] = [];
      let skipped = 0;
      for (;attempt >= 1;) {
        const r = await api.logs(runID, stepID, attempt, after).catch(() => null);
        if (alive.current !== token || !r) break;
        all = all.concat(r.lines);
        if (all.length > KEEP) { skipped += all.length - KEEP; all = all.slice(all.length - KEEP); }
        if (r.lines.length < 5000) break;
        after = r.lines[r.lines.length - 1].seq;
      }
      if (alive.current === token) { last.current = all.length ? all[all.length - 1].seq : 0; setHeld({ key, lines: all, dropped: skipped, loaded: true }); }
    })();
    const own = alive;
    return () => { own.current++; };
  }, [runID, stepID, attempt, key]);

  useEffect(() => {
    sink.current = (s, a, incoming) => {
      if (s === "" && a === 0) { void catchUp(alive.current); return; } // the stream came back
      if (s !== stepID || a !== attempt) return;
      // A batch that does not follow on from the last line means lines were missed: fetch them.
      if (incoming.length && last.current > 0 && incoming[0].seq > last.current + 1) { void catchUp(alive.current); return; }
      append(incoming);
    };
    return () => { sink.current = null; };
  }, [stepID, attempt, sink, append, catchUp]);

  return { lines: mine.lines, dropped: mine.dropped, loaded: mine.loaded };
}
