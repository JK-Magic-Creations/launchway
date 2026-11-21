// The layout of a pipeline drawn as a graph. Pure: it takes steps and what each needs and returns where to
// put them, so it is tested without a browser. Columns are stages: a step sits one column to the right of the
// deepest step it needs (longest path), so steps that do not wait for each other share a column and run together.
// Within a column the order is chosen to cross as few connectors as it can (barycenter sweeps, stable), and a
// connector that skips columns is routed through a slot kept for it in each column it passes.

export interface GNode { id: string; needs: string[] }

export interface LayoutOptions {
  nodeW: number; nodeH: number;
  /** Space between columns and between rows. */
  gapX: number; gapY: number;
  /** Space around the drawing; padTop leaves room for the stage labels. */
  pad: number; padTop: number;
  /** Height of the slot a skipping connector takes in a column. */
  laneH: number;
}

export const DEFAULTS: LayoutOptions = { nodeW: 196, nodeH: 62, gapX: 56, gapY: 14, pad: 12, padTop: 34, laneH: 10 };

export interface PlacedNode { id: string; col: number; row: number; x: number; y: number; w: number; h: number }
export interface Point { x: number; y: number }
export interface PlacedEdge {
  from: string; to: string;
  /** How many columns the connector crosses; more than 1 means it skips steps' columns. */
  span: number;
  points: Point[];
  /** An SVG path, smooth between columns and straight where it passes through one. */
  d: string;
}
export interface Column { col: number; x: number; ids: string[] }

export interface GraphLayout {
  nodes: PlacedNode[];
  edges: PlacedEdge[];
  columns: Column[];
  /** The steps each step needs and the steps that need it (only those drawn), in drawing order, top to bottom. */
  before: Record<string, string[]>;
  after: Record<string, string[]>;
  width: number; height: number;
  /** Connectors that cross each other; what the ordering tried to keep small. */
  crossings: number;
}

interface Item { id: string; real: boolean }

/** Depth of every step: 0 when it needs nothing, else one more than the deepest step it needs. Needs that name
 * no step are ignored; a loop (which the server refuses) does not hang. */
export function depths(nodes: GNode[]): Map<string, number> {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const depth = new Map<string, number>();
  const visiting = new Set<string>();
  const visit = (id: string): number => {
    const known = depth.get(id);
    if (known !== undefined) return known;
    if (visiting.has(id)) return 0;
    visiting.add(id);
    let d = 0;
    for (const n of byId.get(id)?.needs ?? []) if (n !== id && byId.has(n)) d = Math.max(d, visit(n) + 1);
    visiting.delete(id);
    depth.set(id, d);
    return d;
  };
  for (const n of nodes) visit(n.id);
  return depth;
}

/** The stages as lists of step IDs, in the order the steps were given. This is also the text alternative's shape. */
export function stages(nodes: GNode[]): string[][] {
  const d = depths(nodes);
  const out: string[][] = [];
  for (const n of nodes) (out[d.get(n.id) ?? 0] ??= []).push(n.id);
  return out.map((s) => s ?? []);
}

function countCrossings(cols: Item[][], links: Map<string, string[]>): number {
  let total = 0;
  for (let c = 0; c + 1 < cols.length; c++) {
    const pos = new Map(cols[c + 1].map((it, i) => [it.id, i]));
    const pairs: [number, number][] = [];
    cols[c].forEach((it, i) => { for (const t of links.get(it.id) ?? []) if (pos.has(t)) pairs.push([i, pos.get(t)!]); });
    for (let a = 0; a < pairs.length; a++) {
      for (let b = a + 1; b < pairs.length; b++) {
        if ((pairs[a][0] - pairs[b][0]) * (pairs[a][1] - pairs[b][1]) < 0) total++;
      }
    }
  }
  return total;
}

/** layoutGraph places steps in columns and routes a connector from each step to each step that needs it. */
export function layoutGraph(input: GNode[], options: Partial<LayoutOptions> = {}): GraphLayout {
  const o = { ...DEFAULTS, ...options };
  const ids = new Set(input.map((n) => n.id));
  const needs = new Map<string, string[]>();
  for (const n of input) needs.set(n.id, [...new Set(n.needs.filter((x) => x !== n.id && ids.has(x)))]);
  const depth = depths(input.map((n) => ({ id: n.id, needs: needs.get(n.id)! })));
  const ncols = input.length === 0 ? 0 : Math.max(...input.map((n) => depth.get(n.id) ?? 0)) + 1;

  // Columns hold the steps and, for a connector that crosses a column without stopping, a slot for it.
  const cols: Item[][] = Array.from({ length: ncols }, () => []);
  for (const n of input) cols[depth.get(n.id)!].push({ id: n.id, real: true });
  const links = new Map<string, string[]>(); // from an item to the items in the next column
  const pred = new Map<string, string[]>();
  const link = (a: string, b: string) => {
    links.set(a, [...(links.get(a) ?? []), b]);
    pred.set(b, [...(pred.get(b) ?? []), a]);
  };
  const chain = new Map<string, string[]>(); // "from>to" → slot ids between them
  for (const n of input) {
    for (const from of needs.get(n.id)!) {
      const a = depth.get(from)!, b = depth.get(n.id)!;
      let prev = from;
      const slots: string[] = [];
      for (let c = a + 1; c < b; c++) {
        const id = `~${from}>${n.id}@${c}`;
        cols[c].push({ id, real: false });
        slots.push(id);
        link(prev, id);
        prev = id;
      }
      link(prev, n.id);
      chain.set(from + ">" + n.id, slots);
    }
  }

  // Order within columns: sweep down (by where an item's predecessors are) and up (by its successors), keep the best.
  let best = cols.map((c) => [...c]);
  let bestCross = countCrossings(best, links);
  const sweep = (down: boolean) => {
    const order = down ? [...cols.keys()].slice(1) : [...cols.keys()].slice(0, -1).reverse();
    for (const c of order) {
      const ref = cols[down ? c - 1 : c + 1];
      const where = new Map(ref.map((it, i) => [it.id, i]));
      const rank = (it: Item, i: number): number => {
        const nb = (down ? pred.get(it.id) : links.get(it.id)) ?? [];
        const ps = nb.map((x) => where.get(x)).filter((x): x is number => x !== undefined);
        return ps.length ? ps.reduce((a, b) => a + b, 0) / ps.length : i;
      };
      cols[c] = cols[c].map((it, i) => ({ it, r: rank(it, i), i })).sort((a, b) => a.r - b.r || a.i - b.i).map((x) => x.it);
    }
  };
  for (let pass = 0; pass < 12 && bestCross > 0; pass++) {
    sweep(pass % 2 === 0);
    const x = countCrossings(cols, links);
    if (x < bestCross) { bestCross = x; best = cols.map((c) => [...c]); }
  }

  // Positions: columns are centred on each other, so a fan-out or fan-in is symmetric.
  const heights = best.map((c) => c.reduce((h, it, i) => h + (it.real ? o.nodeH : o.laneH) + (i ? o.gapY / (it.real ? 1 : 2) : 0), 0));
  const tallest = Math.max(0, ...heights);
  const pos = new Map<string, PlacedNode>();
  const columns: Column[] = [];
  best.forEach((items, c) => {
    const x = o.pad + c * (o.nodeW + o.gapX);
    let y = o.padTop + (tallest - heights[c]) / 2;
    const real: string[] = [];
    items.forEach((it, i) => {
      if (i) y += o.gapY / (it.real ? 1 : 2);
      const h = it.real ? o.nodeH : o.laneH;
      pos.set(it.id, { id: it.id, col: c, row: i, x, y, w: o.nodeW, h });
      y += h;
      if (it.real) real.push(it.id);
    });
    columns.push({ col: c, x, ids: real });
  });

  const nodes = input.map((n) => pos.get(n.id)!);
  const edges: PlacedEdge[] = [];
  for (const n of input) {
    for (const from of needs.get(n.id)!) {
      const a = pos.get(from)!, b = pos.get(n.id)!;
      const pts: Point[] = [{ x: a.x + a.w, y: a.y + a.h / 2 }];
      let d = `M${pts[0].x},${pts[0].y}`;
      const curve = (p: Point, q: Point) => { const mx = (p.x + q.x) / 2; return ` C${mx},${p.y} ${mx},${q.y} ${q.x},${q.y}`; };
      for (const slot of chain.get(from + ">" + n.id)!) {
        const s = pos.get(slot)!, last = pts[pts.length - 1];
        const enter = { x: s.x, y: s.y + s.h / 2 }, leave = { x: s.x + s.w, y: enter.y };
        d += curve(last, enter) + ` L${leave.x},${leave.y}`;
        pts.push(enter, leave);
      }
      const end = { x: b.x, y: b.y + b.h / 2 };
      d += curve(pts[pts.length - 1], end);
      pts.push(end);
      edges.push({ from, to: n.id, span: b.col - a.col, points: pts, d });
    }
  }

  const order = (xs: string[]) => xs.sort((p, q) => pos.get(p)!.y - pos.get(q)!.y);
  const before: Record<string, string[]> = {}, after: Record<string, string[]> = {};
  for (const n of input) { before[n.id] = order([...needs.get(n.id)!]); after[n.id] = []; }
  for (const e of edges) after[e.from].push(e.to);
  for (const k of Object.keys(after)) order(after[k]);

  return {
    nodes, edges, columns, before, after, crossings: bestCross,
    width: ncols === 0 ? o.pad * 2 : o.pad * 2 + ncols * o.nodeW + (ncols - 1) * o.gapX,
    height: o.padTop + tallest + o.pad,
  };
}

/** Runs of steps that go one after another with nothing beside them: each step has one successor and the next has
 * one predecessor. A chain of at least minLen can be folded into one node. */
export function serialChains(nodes: GNode[], minLen = 5): string[][] {
  const ids = new Set(nodes.map((n) => n.id));
  const preds = new Map<string, string[]>(), succs = new Map<string, string[]>();
  for (const n of nodes) {
    const ps = [...new Set(n.needs.filter((x) => x !== n.id && ids.has(x)))];
    preds.set(n.id, ps);
    for (const p of ps) succs.set(p, [...(succs.get(p) ?? []), n.id]);
  }
  const linkedNext = (id: string): string | undefined => {
    const s = succs.get(id) ?? [];
    return s.length === 1 && (preds.get(s[0]) ?? []).length === 1 ? s[0] : undefined;
  };
  const hasPrev = (id: string) => { const p = preds.get(id) ?? []; return p.length === 1 && linkedNext(p[0]) === id; };
  const out: string[][] = [];
  for (const n of nodes) {
    if (hasPrev(n.id)) continue;
    const run = [n.id];
    for (let nx = linkedNext(n.id); nx && !run.includes(nx); nx = linkedNext(nx)) run.push(nx);
    if (run.length >= minLen) out.push(run);
  }
  return out;
}
