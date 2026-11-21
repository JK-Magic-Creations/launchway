// The layout of the picture of the graph: columns by distance from the focus (what the focus depends on to its
// left, what depends on it to its right), nodes stacked in each column. It is plain arithmetic on arrays, so it
// runs in a worker (layout.worker.ts) and, where there is no worker, on the page itself.

export interface LNode { id: string; kind: string; label: string }
export interface LEdge { from: string; to: string; kind: string }
export interface Placed { id: string; x: number; y: number }
export interface Layout { nodes: Placed[]; width: number; height: number; shown: number; total: number }

export const COL_W = 230;
export const ROW_H = 30;
export const PAD = 40;
export const MAX_SHOWN = 600;

/** Places nodes. Edges point from the thing that depends to the thing depended on, or as the caller gives them:
 * the column is the shortest number of steps from the focus, negative against the edges and positive along them. */
export function layout(nodes: LNode[], edges: LEdge[], focus: string): Layout {
  const out = new Map<string, string[]>();
  const into = new Map<string, string[]>();
  for (const e of edges) {
    (out.get(e.from) ?? out.set(e.from, []).get(e.from)!).push(e.to);
    (into.get(e.to) ?? into.set(e.to, []).get(e.to)!).push(e.from);
  }
  const col = new Map<string, number>();
  if (nodes.some((n) => n.id === focus)) col.set(focus, 0);
  const walk = (adj: Map<string, string[]>, sign: number) => {
    let frontier = [focus];
    const seen = new Set([focus]);
    for (let d = 1; frontier.length && d < 64; d++) {
      const next: string[] = [];
      for (const id of frontier) for (const w of adj.get(id) ?? []) {
        if (seen.has(w)) continue;
        seen.add(w);
        if (!col.has(w)) col.set(w, sign * d);
        next.push(w);
      }
      frontier = next;
    }
  };
  walk(out, 1);
  walk(into, -1);
  for (const n of nodes) if (!col.has(n.id)) col.set(n.id, 0);
  const shown = [...nodes].sort((a, b) => Math.abs(col.get(a.id)!) - Math.abs(col.get(b.id)!) || a.kind.localeCompare(b.kind) || a.label.localeCompare(b.label)).slice(0, MAX_SHOWN);
  const lo = Math.min(0, ...shown.map((n) => col.get(n.id)!));
  const rows = new Map<number, number>();
  const placed: Placed[] = [];
  let height = 0;
  for (const n of [...shown].sort((a, b) => col.get(a.id)! - col.get(b.id)! || a.kind.localeCompare(b.kind) || a.label.localeCompare(b.label))) {
    const c = col.get(n.id)!;
    const r = rows.get(c) ?? 0;
    rows.set(c, r + 1);
    placed.push({ id: n.id, x: PAD + (c - lo) * COL_W, y: PAD + r * ROW_H });
    height = Math.max(height, PAD + (r + 1) * ROW_H);
  }
  const cols = Math.max(...shown.map((n) => col.get(n.id)!), 0) - lo + 1;
  return { nodes: placed, width: PAD * 2 + cols * COL_W, height: height + PAD, shown: shown.length, total: nodes.length };
}
