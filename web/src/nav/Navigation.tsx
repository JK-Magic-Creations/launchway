import type { ReactNode } from "react";
import { onLink } from "../router";
import { breadcrumbs, section, tabOn, sectionOf, visibleSections, visibleTabs, type Crumb, type Ctx, type SectionId } from "./model";

const ICONS: Record<SectionId, ReactNode> = {
  home: <path d="M3 11l9-8 9 8v9a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z" />,
  projects: <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />,
  deployments: <path d="M12 3l9 5-9 5-9-5zM3 13l9 5 9-5" />,
  activity: <path d="M3 12h4l3-8 4 16 3-8h4" />,
  library: <path d="M4 4h5v16H4zM11 4h5v16h-5zM18 6l3 .8-3.6 13.2-3-.8z" />,
  settings: <><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M2 12h3M19 12h3M5 5l2 2M17 17l2 2M19 5l-2 2M7 17l-2 2" /></>,
};

export function SectionIcon({ id }: { id: SectionId }) {
  return (
    <svg className="nav-ic" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {ICONS[id]}
    </svg>
  );
}

/** SectionLinks is the menu: the sections that have something to show, the current one marked. */
export function SectionLinks({ ctx, path, onNavigate }: { ctx: Ctx; path: string; onNavigate?: () => void }) {
  const on = sectionOf(path);
  const waiting = ctx.facts.approvals;
  return (
    <ul className="nav-links">
      {visibleSections(ctx).map((s) => (
        <li key={s.id}>
          <a href={s.href} onClick={(e) => { onLink(e); onNavigate?.(); }} className={"nav-link" + (on === s.id ? " nav-on" : "")}
            aria-current={on === s.id ? "page" : undefined} title={s.promise}>
            <SectionIcon id={s.id} />
            <span className="nav-label">{s.label}</span>
            {s.id === "activity" && waiting > 0 && <span className="nav-badge" aria-label={`${waiting} waiting for a decision`}>{waiting}</span>}
          </a>
        </li>
      ))}
    </ul>
  );
}

/** Breadcrumbs say where the page is and link back up. The last one is the page itself. */
export function Breadcrumbs({ path, label }: { path: string; label?: string }) {
  const crumbs = breadcrumbs(path, label);
  if (crumbs.length < 2) return null;
  return <CrumbList crumbs={crumbs} />;
}

export function CrumbList({ crumbs }: { crumbs: Crumb[] }) {
  return (
    <nav className="crumbs" aria-label="You are here">
      <ol>
        {crumbs.map((c, i) => {
          const last = i === crumbs.length - 1;
          return (
            <li key={i}>
              {c.href && !last ? <a href={c.href} onClick={onLink}>{c.label}</a> : <span aria-current={last ? "page" : undefined}>{c.label}</span>}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

/** SectionTabs are the pages inside the section the person is in. A section with a single page shows none. */
export function SectionTabs({ ctx, path }: { ctx: Ctx; path: string }) {
  const id = sectionOf(path);
  if (!id) return null;
  const tabs = visibleTabs(section(id), ctx);
  if (tabs.length < 2) return null;
  const on = tabOn(path);
  return (
    <nav className="sectabs" aria-label={section(id).label}>
      {tabs.map((t) => (
        <a key={t.id} href={t.href} onClick={onLink} className={"sectab" + (on === t.id ? " sectab-on" : "")} aria-current={on === t.id ? "page" : undefined}>{t.label}</a>
      ))}
    </nav>
  );
}

