import { Icon } from "../kit/Icon";
import { onLink } from "../router";
import { section, tabOn, visibleTabs, type Ctx, type SectionId } from "../nav/model";

/** SideNav is the persistent left column inside an area: the pages of the area, the current one marked.
 * Below 1100 px it becomes a row of chips under the header. An area with one page, or none, has none. */
export function SideNav({ ctx, area, path }: { ctx: Ctx; area: SectionId | null; path: string }) {
  if (!area) return null;
  const s = section(area);
  const tabs = visibleTabs(s, ctx);
  if (tabs.length < 2) return null;
  const on = tabOn(path);
  const waiting = ctx.facts.approvals;
  return (
    <nav className="side" aria-label={`In ${s.label}`}>
      <div className="side-h">{s.label}</div>
      {s.side && <div className="side-d">{s.side}</div>}
      {tabs.map((t) => (
        <a key={t.id} href={t.href} onClick={onLink} aria-current={on === t.id ? "page" : undefined}>
          <Icon name={t.icon ?? "list"} size="s" />
          <span>{t.label}</span>
          {t.soon && <span className="n soon">soon</span>}
          {area === "activity" && t.id === "approvals" && waiting > 0 && <span className="n" aria-label={`${waiting} waiting`}>{waiting}</span>}
        </a>
      ))}
    </nav>
  );
}
