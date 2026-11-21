import { useMemo } from "react";
import type { HostedRepo } from "../forgeApi";
import { announce } from "../kit/announce";
import { Explorer } from "./Explorer";
import { useOrganized } from "./useOrganized";
import type { XItem } from "./tree";

/** The hosted repositories as an Explorer: by owner (a path like acme/shop is a folder acme), and by any
 * folders people made, with labels and favorites. A repository has no run result of its own, so its card
 * shows its visibility and default branch. */
export default function RepositoriesExplorer({ repos, now }: { repos: HostedRepo[]; now: number }) {
  const org = useOrganized("repositories");
  const items = useMemo<XItem[]>(() => repos.map((r) => {
    const where = org.place(r.path);
    return {
      id: r.path, name: r.path, description: `${r.visibility}${r.archived ? ", archived" : ""} · default branch ${r.default_branch}`, path: where.path, labels: [...where.labels, ...(r.archived ? ["archived"] : [])],
      kind: r.visibility, tone: "none", history: [], running: 0, href: `/repositories/${encodeURIComponent(r.home ?? "")}/${encodeURIComponent(r.name)}`,
      favorite: org.favorites.has("repository:" + r.path), actions: [{ label: "Open", href: `/repositories/${encodeURIComponent(r.home ?? "")}/${encodeURIComponent(r.name)}` }],
    };
  }), [repos, org]);
  const kinds = useMemo(() => [...new Set(repos.map((r) => r.visibility))].sort(), [repos]);
  return (
    <Explorer noun={{ singular: "repository", plural: "repositories" }} items={items} now={now} kinds={kinds} initialView="table" results={false}
      emptyFolders={org.emptyFolders} folders={org.folders} collections={org.collections} onCollections={org.saveCollections}
      onMove={org.mayArrange ? async (it, folder) => { await org.move(it.id, folder); } : undefined}
      onCreateFolder={org.mayArrange ? org.createFolder : undefined}
      onFavorite={(it, on) => { void org.favorite("repository", it.id, on); announce(`${it.name} ${on ? "added to" : "removed from"} favorites.`); }}
      empty={<p>No repository is hosted here yet.</p>} />
  );
}
