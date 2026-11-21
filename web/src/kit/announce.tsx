import { useEffect, useState } from "react";

// Live regions that say what changed without moving focus: a run's new state, a command's outcome, an error in a form.
// There is one polite region (status, waits for the person to finish what they hear) and one assertive (alert, for what
// must be heard now: a failure, a lost session). Any code may call announce(); the shell mounts <Announcer /> once.

type Mode = "polite" | "assertive";
type Listener = (text: string) => void;
const listeners: Record<Mode, Set<Listener>> = { polite: new Set(), assertive: new Set() };
const pending: Record<Mode, string[]> = { polite: [], assertive: [] };

/** Says something to screen reader users. The same sentence twice in a row is still said twice. */
export function announce(text: string, mode: Mode = "polite"): void {
  if (!text) return;
  if (listeners[mode].size === 0) { pending[mode].push(text); return; }
  listeners[mode].forEach((l) => l(text));
}

/** The two regions. Mount once, near the top of the page; the text is cleared a little after, so it is not read again by browsing. */
export function Announcer() {
  const [polite, setPolite] = useState("");
  const [assertive, setAssertive] = useState("");
  useEffect(() => {
    let tp: number | undefined; let ta: number | undefined;
    const say = (set: (s: string) => void, timer: "p" | "a") => (text: string) => {
      set("");
      // An empty value first, then the text, so that a repeated sentence is a change the browser reports.
      window.setTimeout(() => set(text), 30);
      window.clearTimeout(timer === "p" ? tp : ta);
      const t = window.setTimeout(() => set(""), 8000);
      if (timer === "p") tp = t; else ta = t;
    };
    const p = say(setPolite, "p"); const a = say(setAssertive, "a");
    listeners.polite.add(p); listeners.assertive.add(a);
    pending.polite.splice(0).forEach(p); pending.assertive.splice(0).forEach(a);
    return () => { listeners.polite.delete(p); listeners.assertive.delete(a); window.clearTimeout(tp); window.clearTimeout(ta); };
  }, []);
  return (
    <>
      <div className="visually-hidden" role="status" aria-live="polite" aria-atomic="true" data-announcer="polite">{polite}</div>
      <div className="visually-hidden" role="alert" aria-live="assertive" aria-atomic="true" data-announcer="assertive">{assertive}</div>
    </>
  );
}
