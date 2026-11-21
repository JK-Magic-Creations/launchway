import { onLink } from "../router";
import { breadcrumbs, type Crumb } from "./model";

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
