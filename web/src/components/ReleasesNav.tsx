import { onLink } from "../router";

const TABS = [
  { href: "/deployments/releases", label: "Releases" },
  { href: "/deployments/releases/matrix", label: "Where each release is" },
  { href: "/deployments/releases/calendar", label: "Calendar" },
  { href: "/deployments/releases/formations", label: "Formations" },
  { href: "/deployments/releases/report", label: "Report" },
];

/** The pages of release management, one under the other's name: the list, the matrix, the calendar, formations and the report. */
export function ReleasesNav({ path }: { path: string }) {
  return (
    <nav aria-label="Release management">
      <ul className="rel-nav">
        {TABS.map((t) => <li key={t.href}><a href={t.href} onClick={onLink} aria-current={path === t.href ? "page" : undefined}>{t.label}</a></li>)}
      </ul>
    </nav>
  );
}
