import { navigate } from "../router";

export type RepoTabId = "overview" | "code" | "commits" | "branches" | "tags" | "search" | "pulls" | "dependencies" | "graph" | "mirrors" | "history" | "settings";

const TABS: [RepoTabId, string, string][] = [
  ["overview", "Overview", ""], ["code", "Code", "code"], ["commits", "Commits", "commits"], ["branches", "Branches", "branches"], ["tags", "Tags", "tags"],
  ["search", "Search", "search"], ["pulls", "Pull requests", "pulls"], ["dependencies", "Dependencies", "dependencies"], ["graph", "Graph", "graph"],
  ["mirrors", "Mirrors", "mirrors"], ["history", "Imported history", "history"], ["settings", "Settings", "settings"],
];

/** The one tab bar of a repository: every page of it shows the same tabs, so no page is a dead end. */
export function RepoTabs({ path, on }: { path: string; on?: RepoTabId }) {
  return (
    <nav className="tabs" aria-label="Repository">
      {TABS.map(([id, label, sub]) => {
        const href = `/repositories/${path}${sub ? "/" + sub : ""}`;
        return (
          <a key={id} href={href} className={"tab" + (on === id ? " tab-on" : "")} aria-current={on === id ? "page" : undefined}
            onClick={(e) => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) { e.preventDefault(); navigate(href); } }}>{label}</a>
        );
      })}
    </nav>
  );
}
