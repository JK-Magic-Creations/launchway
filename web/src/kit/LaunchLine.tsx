import { Icon } from "./Icon";
import { stateOfStation, type State } from "./Status";

/** One station: a place a change has to pass. state is the design's state; unknown states are drawn as not started. */
export interface LineStation { key: string; label: string; state: State; detail?: string }

/** The station states the server's launch lines use, as the design's. */
export function toStations(list: { key: string; label: string; state: string; detail?: string }[]): LineStation[] {
  return list.map((s) => ({ key: s.key, label: s.label, state: stateOfStation(s.state), detail: s.detail }));
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
            <span className="visually-hidden">{`${s.label}: ${WORDS[s.state]}${s.detail ? ". " + s.detail : ""}${i === at && s.state !== "idle" ? " (now)" : ""}`}</span>
          </>
        );
        const c = `ls ${s.state}${i === stations.length - 1 && s.state === "ok" ? " last" : ""}`;
        return onSelect
          ? <button key={s.key} type="button" className={c} aria-pressed={selected === s.key} onClick={() => onSelect(s.key)}>{inner}</button>
          : <div key={s.key} className={c} role="listitem">{inner}</div>;
      })}
    </div>
  );
}

const WORDS: Record<State, string> = { ok: "passed", bad: "failed", warn: "needs a look", wait: "waiting for a person", run: "running", idle: "not yet", skip: "skipped" };

function pin(state: State) {
  switch (state) {
    case "ok": return <Icon name="check" />;
    case "bad": return <Icon name="x" />;
    case "wait": return <Icon name="pause" />;
    case "run": return <span className="st run spinner" aria-hidden="true" />;
    default: return null;
  }
}
