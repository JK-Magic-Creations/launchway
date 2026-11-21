import { Icon } from "../kit/Icon";
import { onLink } from "../router";
import { visibleSections, type Ctx, type SectionId } from "../nav/model";

export type PulseState = "quiet" | "running" | "waiting" | "failing";

/** The hairline under the top bar: one line for the whole installation. Empty when all is well; an
 * amber or red segment appears only when something needs a person, and a slow comet crosses while work runs. */
export function Pulse({ state }: { state: PulseState }) {
  return (
    <div className="pulse" data-state={state} aria-hidden="true">
      <i className="p-run" />
      <i className="p-wait" />
      <i className="p-bad" />
    </div>
  );
}

/** Route is the top row: the ten areas in four groups, each with one line of help on hover and on focus. */
export function Route({ ctx, on, needs }: { ctx: Ctx; on: SectionId | null; needs: number }) {
  const list = visibleSections(ctx);
  return (
    <nav className="route" aria-label="Areas">
      {list.map((s, i) => (
        <span className="route-item" key={s.id}>
          {i > 0 && s.group !== list[i - 1].group && <span className="sep" aria-hidden="true" />}
          <a href={s.href} onClick={onLink} data-tip={s.promise} aria-describedby={`tip-${s.id}`} aria-current={on === s.id ? "page" : undefined}>
            {s.label}
            {s.id === "home" && needs > 0 && <span className="badge" aria-label={`${needs} ${needs === 1 ? "thing needs" : "things need"} you`}>{needs}</span>}
          </a>
          <span id={`tip-${s.id}`} className="visually-hidden">{s.promise}</span>
        </span>
      ))}
    </nav>
  );
}

/** The bottom bar on a phone: Home, Code, Deployments, Traffic, Changes and All areas. */
export function TabBar({ ctx, on, needs, onAreas }: { ctx: Ctx; on: SectionId | null; needs: number; onAreas: () => void }) {
  const ids: SectionId[] = ["home", "projects", "deployments", "traffic", "changes"];
  const list = visibleSections(ctx).filter((s) => ids.includes(s.id));
  return (
    <nav className="tabbar" aria-label="Areas, bottom bar">
      {list.map((s) => (
        <a key={s.id} href={s.href} onClick={onLink} aria-current={on === s.id ? "page" : undefined}>
          <Icon name={s.icon} />{s.label}
          {s.id === "home" && needs > 0 && <span className="badge" aria-label={`${needs} need you`}>{needs}</span>}
        </a>
      ))}
      <button type="button" onClick={onAreas}><Icon name="apps" />All areas</button>
    </nav>
  );
}
