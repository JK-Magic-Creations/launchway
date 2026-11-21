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
  const at = stations.findIndex((s) => s.state !== "ok" && s.state !== "skip");
  const summary = label ?? stations.map((s) => `${s.label}: ${WORDS[s.state]}`).join(", ");
  const cls = ["lline", big && "big", compact && "compact"].filter(Boolean).join(" ");
  return (
    <div className={cls} style={{ gridTemplateColumns: `repeat(${Math.max(stations.length, 1)}, ${compact ? "minmax(0, 84px)" : "minmax(0, 1fr)"})` }}
      role={onSelect ? "group" : "img"} aria-label={summary}>
      {stations.map((s, i) => {
        const inner = (
          <>
            <span className="p">{pin(s.state)}</span>
            <span className="t">{s.label}</span>
            <span className="u" title={s.detail}>{s.detail ?? ""}</span>
            {i === at && s.state !== "idle" && <span className="visually-hidden"> (now)</span>}
          </>
        );
        const c = `ls ${s.state}${i === stations.length - 1 && s.state === "ok" ? " last" : ""}`;
        return onSelect
          ? <button key={s.key} type="button" className={c} aria-pressed={selected === s.key} aria-label={`${s.label}: ${WORDS[s.state]}${s.detail ? ". " + s.detail : ""}`} onClick={() => onSelect(s.key)}>{inner}</button>
          : <div key={s.key} className={c}>{inner}</div>;
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
