import { useMemo } from "react";
import type { Project, RunSummary } from "../api";
import { tickOf, trendOf } from "../guided/decide";
import { announce } from "../kit/announce";
import { Explorer } from "./Explorer";
import { useOrganized } from "./useOrganized";
import type { Tone, XItem } from "./tree";
import { terms } from "../terms";
import { LinkButton } from "../kit/layout";
import { hostName } from "../guided/code";
import { useLoad } from "../guided/useLoad";
import { organizeApi } from "../organizeApi";
import { useCallback } from "react";

const toneOf = (r: RunSummary | null | undefined): Tone => {
  if (!r) return "none";
  const t = tickOf(r);
  return t === "ok" ? "ok" : t === "bad" ? "bad" : t === "wait" ? "wait" : t === "run" ? "run" : "none";
};
const WORD: Record<string, string> = { succeeded: "Passed", failed: "Failed", running: "Running", queued: "Queued", canceled: "Canceled", waiting: "Waiting" };

/** The pipelines page's list as an Explorer: levels and folders on the left, each pipeline a card with where it
 * lives, its last result, a strip of recent results and what runs now. */
export default function PipelinesExplorer({ projects, runs, now, me, as = "pipelines" }: { projects: Project[]; runs: RunSummary[]; now: number; me: string; as?: "pipelines" | "projects" }) {
  const repos = as === "projects";
  const org = useOrganized(as);
  // The recent results of each pipeline's default branch, and what runs now, come from the same answer Home uses.
  const homeFn = useCallback((bg: boolean) => organizeApi.home("all", bg), []);
  const home = useLoad(homeFn, 15000).data;
  const stats = useMemo(() => new Map((home?.categories ?? []).flatMap((c) => c.projects).map((p) => [p.name, p])), [home]);
  const byProject = useMemo(() => {
    const m = new Map<string, RunSummary[]>();
    for (const r of runs) m.set(r.project, [...(m.get(r.project) ?? []), r]);
    return m;
  }, [runs]);
  const items = useMemo<XItem[]>(() => projects.map((p) => {
    const where = org.place(p.name);
    const rs = byProject.get(p.name) ?? (p.last_run ? [p.last_run] : []);
    const last = p.last_run ?? rs[0] ?? null;
    const trend = trendOf(rs);
    return {
      id: p.name, name: p.name, path: where.path, labels: where.labels, tone: p.error ? "bad" : toneOf(last),
      description: repos ? (p.error ? `Cannot reach the repository: ${p.error}` : `${p.url} · default branch ${p.default_branch}`) : undefined,
      kind: repos ? (hostName(p.url) || "local") : undefined,
      last: last ? { text: `${WORD[last.status] ?? last.status} · #${last.number}`, at: last.finished_at || last.created_at, href: `/runs/${last.id}` } : undefined,
      history: stats.has(p.name) ? [...stats.get(p.name)!.history].reverse().map((h): Tone => (h === "succeeded" ? "ok" : h === "failed" ? "bad" : "none")) : trend.ticks.map((t) => (t === "skip" ? "none" : t)) as Tone[],
      running: stats.get(p.name)?.running ?? rs.filter((r) => r.status === "running" || r.status === "queued").length,
      href: repos ? `/projects/${encodeURIComponent(p.name)}` : `/projects/${encodeURIComponent(p.name)}/pipeline`, favorite: org.favorites.has("project:" + p.name),
      mine: rs.some((r) => r.actor === me),
      actions: [{ label: repos ? "Pipeline" : "Open", href: `/projects/${encodeURIComponent(p.name)}/pipeline` }],
    };
  }), [projects, byProject, org, me, stats, repos]);
  const t = terms();
  return (
    <Explorer noun={repos ? { singular: "repository", plural: "repositories" } : { singular: "pipeline", plural: "pipelines" }} kinds={repos ? [...new Set(projects.map((p) => hostName(p.url) || "local"))].sort() : undefined} items={items} now={now}
      emptyFolders={org.emptyFolders} folders={org.folders}
      onMove={org.mayArrange ? async (it, folder) => { await org.move(it.id, folder); } : undefined}
      onCreateFolder={org.mayArrange ? org.createFolder : undefined}
      onFavorite={(it, on) => { void org.favorite("project", it.id, on); announce(`${it.name} ${on ? "added to" : "removed from"} favorites.`); }}
      emptyHint={<>Folders group pipelines inside a {t.label("unit")} or {t.label("project")}. A name like <code>payments/api</code> makes folders by itself.</>}
      toolbar={undefined}
      empty={<p>No pipelines yet. <LinkButton href="/config/wizard/connect-repository" kind="pri" icon="plus">Add a project</LinkButton></p>} />
  );
}
