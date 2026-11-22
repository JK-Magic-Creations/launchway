import { Icon } from "./Icon";
import { stateOfStation, type State } from "./Status";
import { terms } from "../terms";
import { SpanHidden } from "./bits";

/** One station: a place a change has to pass. state is the design's state; unknown states are drawn as not started. */
export interface LineStation { key: string; label: string; state: State; detail?: string; parallel?: Parallel }

/** A station that stands for several steps: how many, how many can run at the same time, how each is doing. */
export interface Parallel {
  total: number; together: number; done: number; running: number; holding?: number; waiting: number; failed: number; skipped: number; idle: number;
  steps: { id: string; state: string }[];
}

/** The station states the server's launch lines use, as the design's. */
export function toStations(list: { key: string; label: string; state: string; detail?: string; parallel?: Parallel }[]): LineStation[] {
  return list.map((s) => ({ key: s.key, label: s.label, state: stateOfStation(s.state), detail: s.detail, parallel: s.parallel }));
}

/** "3 at once: 2 passed, 1 running": what a station made of several steps is doing, in words. */
export function parallelText(p: Parallel): string {
  const parts = ([[p.done, "passed"], [p.running, "running"], [p.holding ?? 0, terms().say("holding for a {runner}")], [p.waiting, "waiting for a person"], [p.failed, "failed"], [p.skipped, "skipped"], [p.idle, "not started"]] as const)
    .filter(([n]) => n > 0).map(([n, w]) => `${n} ${w}`);
  const head = p.together >= p.total ? `${p.total} at once` : p.together > 1 ? `${p.total} steps, ${p.together} at once` : `${p.total} steps in a row`;
  return `${head}: ${parts.join(", ")}`;
}

/** LaunchLine draws a change's journey: stations on a track. Track colour carries state: slate passed,
 * blue moving, amber waiting for a person, red stopped, dotted not yet; the last station is the only green one.
 * compact hides the words (rows); big is the large form. With onSelect each station is a button. */
export function LaunchLine({ stations, big, compact, selected, onSelect, label }: {
  stations: LineStation[]; big?: boolean; compact?: boolean; selected?: string; onSelect?: (key: string) => void; label?: string;
}) {
  if (stations.length === 0) return null;
  const at = stations.findIndex((s) => s.state !== "ok" && s.state !== "skip");
  const summary = label ?? "Launch line: " + stations.map((s) => `${s.label} ${WORDS[s.state]}`).join(", ");
  const cls = ["lline", big && "big", compact && "compact"].filter(Boolean).join(" ");
  // The text alternative is the line itself as a list: each station with its state in words and its detail, whether or not
  // the picture shows the words (a compact line hides them). The drawing is hidden from assistive technology.
  return (
    <div className={cls} style={{ gridTemplateColumns: `repeat(${Math.max(stations.length, 1)}, ${compact ? "minmax(0, 84px)" : "minmax(0, 1fr)"})` }}
      role={onSelect ? "group" : "list"} aria-label={summary}>
      {stations.map((s, i) => {
        const inner = (
          <>
            <span className="p" aria-hidden="true">{pin(s.state)}</span>
            <span className="t" aria-hidden="true">{s.label}</span>
            <span className="u" title={s.detail} aria-hidden="true">{s.detail ?? ""}</span>
            {s.parallel && <Stack p={s.parallel} />}
            <SpanHidden>{`${s.label}: ${WORDS[s.state]}${s.detail ? ". " + s.detail : ""}${s.parallel ? ". " + parallelText(s.parallel) : ""}${i === at && s.state !== "idle" ? " (now)" : ""}`}</SpanHidden>
          </>
        );
        const c = `ls ${s.state === "hold" ? "idle hold" : s.state}${i === stations.length - 1 && s.state === "ok" ? " last" : ""}`;
        return onSelect
          ? <button key={s.key} type="button" className={c} aria-pressed={selected === s.key} onClick={() => onSelect(s.key)}>{inner}</button>
          : <div key={s.key} className={c} role="listitem">{inner}</div>;
      })}
    </div>
  );
}

const WORDS: Record<State, string> = { ok: "passed", bad: "failed", warn: "needs a look", wait: "waiting for a person", run: "running", hold: "holding, waiting for a runner", idle: "not yet", skip: "skipped" };

function pin(state: State) {
  switch (state) {
    case "ok": return <Icon name="check" />;
    case "bad": return <Icon name="x" />;
    case "wait": return <Icon name="pause" />;
    case "hold": return <Icon name="clock" />;
    case "run": return <span className="st run spinner" aria-hidden="true" />;
    default: return null;
  }
}

const STEP_WORDS: Record<string, string> = { done: "passed", running: "running", waiting: "waiting for a person", failed: "failed", skipped: "skipped", idle: "not started" };

/** The mark of a station that stands for several steps: a small stack with the count, and, on hover or when the row has
 * focus, each step with its state. The words are in the station's text alternative; the stack is for the eye. */
function Stack({ p }: { p: Parallel }) {
  return (
    <span className={"pstack" + (p.together > 1 ? " together" : "")} aria-hidden="true">
      <span className="pstack-n">{p.together > 1 ? "×" : ""}{p.total}</span>
      <span className="ppop">
        <b>{parallelText(p)}</b>
        {p.steps.map((x) => <span key={x.id} className={"pp-" + x.state}>{x.id} <i>{STEP_WORDS[x.state] ?? x.state}</i></span>)}
      </span>
    </span>
  );
}
