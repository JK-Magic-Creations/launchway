import type { Health, LNode, TreeView } from "../launchApi";

/** How each health reads: a word and a glyph, so that it never depends on color alone. */
export const HEALTH: Record<Health, { glyph: string; tone: "ok" | "run" | "bad" | "warn" | "idle" }> = {
  Healthy: { glyph: "✓", tone: "ok" },
  Completed: { glyph: "✓", tone: "idle" },
  Progressing: { glyph: "…", tone: "run" },
  Degraded: { glyph: "✕", tone: "bad" },
  Suspended: { glyph: "⏸", tone: "warn" },
  Missing: { glyph: "?", tone: "bad" },
  Unknown: { glyph: "?", tone: "idle" },
};

export const SECTIONS: { id: string; label: string }[] = [
  { id: "workloads", label: "Workloads" }, { id: "testing", label: "Tests and analysis" }, { id: "network", label: "Network and routes" },
  { id: "config", label: "Configuration" }, { id: "access", label: "Access" }, { id: "scaling", label: "Scaling and disruption" },
  { id: "storage", label: "Storage" }, { id: "other", label: "Other" },
];

export interface TreeItem { node?: LNode; section?: string; label: string; id: string; level: number; children: TreeItem[] }

/** The nodes as a tree: sections at the top, then each node under its parent. */
export function buildItems(nodes: LNode[], leftovers = true): TreeItem[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const kids = new Map<string, LNode[]>();
  const tops = new Map<string, LNode[]>();
  for (const n of nodes) {
    if (n.change === "leftover" && !leftovers) continue;
    if (n.parent && byId.has(n.parent)) kids.set(n.parent, [...(kids.get(n.parent) ?? []), n]);
    else {
      const s = n.change === "leftover" ? "leftover" : n.section ?? "other";
      tops.set(s, [...(tops.get(s) ?? []), n]);
    }
  }
  const rank = (n: LNode) => ({ stable: 0, active: 0, canary: 1, preview: 1, current: 0, old: 2 }[n.role ?? ""] ?? 3);
  const item = (n: LNode, level: number): TreeItem => ({
    node: n, id: n.id, label: n.name, level,
    children: (kids.get(n.id) ?? []).slice().sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name)).map((c) => item(c, level + 1)),
  });
  const out: TreeItem[] = [];
  for (const s of [...SECTIONS, { id: "leftover", label: "Left over from an earlier render" }]) {
    const list = tops.get(s.id);
    if (!list?.length) continue;
    out.push({ section: s.id, id: "section:" + s.id, label: `${s.label} (${list.length})`, level: 1, children: list.slice().sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)).map((n) => item(n, 2)) });
  }
  return out;
}

export function flatten(items: TreeItem[], open: Set<string>): TreeItem[] {
  const out: TreeItem[] = [];
  const walk = (list: TreeItem[]) => list.forEach((i) => { out.push(i); if (open.has(i.id)) walk(i.children); });
  walk(items);
  return out;
}

/** Ids of the items that have children, to open them all at first. */
export function parents(items: TreeItem[], into = new Set<string>()): Set<string> {
  items.forEach((i) => { if (i.children.length) { into.add(i.id); parents(i.children, into); } });
  return into;
}

export function podsOf(nodes: LNode[], parent: string): LNode[] {
  return nodes.filter((n) => n.kind === "Pod" && n.parent === parent);
}

/** What went wrong, led by the cause: the worst thing the tree shows, in words. */
export function cause(v: TreeView): { text: string; node?: LNode } | null {
  const bad = v.tree.nodes.filter((n) => n.health === "Degraded" && n.kind === "Pod" && n.change !== "leftover");
  const canary = bad.find((n) => v.tree.nodes.find((p) => p.id === n.parent)?.role === "canary" || v.tree.nodes.find((p) => p.id === n.parent)?.role === "preview");
  const pick = canary ?? bad[0];
  if (pick) {
    const rs = v.tree.nodes.find((p) => p.id === pick.parent);
    const what = rs?.role === "canary" ? "canary pods" : rs?.role === "preview" ? "preview pods" : rs?.role === "stable" ? "stable pods" : "pods";
    const last = pick.not_ready?.last_logs?.at(-1);
    return { node: pick, text: `${what[0].toUpperCase()}${what.slice(1)} are not healthy: ${pick.not_ready?.why ?? pick.message ?? "see the pod"}${last ? `. Last log line: ${last}` : ""}` };
  }
  const ro = v.tree.rollout;
  const failedRun = ro?.analysis?.find((a) => a.phase === "Failed" || a.phase === "Error");
  if (failedRun) return { text: `Analysis ${failedRun.name} ${failedRun.phase.toLowerCase()}${failedRun.message ? ": " + failedRun.message : ""}` };
  if (ro?.aborted) return { text: `The rollout was aborted${ro.message ? ": " + ro.message : ""}.` };
  const sick = v.tree.nodes.find((n) => n.health === "Degraded" && n.change !== "leftover");
  if (sick) return { node: sick, text: `${sick.kind} ${sick.name} is degraded${sick.message ? ": " + sick.message : ""}` };
  return null;
}

/** "3m 20s" for a number of seconds. */
export function span(sec: number): string {
  sec = Math.max(0, Math.round(sec));
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`;
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`;
}
