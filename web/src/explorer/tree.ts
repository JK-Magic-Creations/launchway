// The Explorer's data: items that live in a place (a path of levels and folders), the tree that places make,
// matching (fuzzy) and the roll-up of status over a branch of the tree. All pure, so it is tested without a page.

export type Tone = "bad" | "wait" | "run" | "ok" | "none";

/** One step of where an item lives: a level (Wing, Squadron) or a folder. */
export interface Seg { key: string; label: string; folderId?: string }

export interface XItem {
  id: string;
  name: string;
  description?: string;
  /** Where it lives, outermost first (not including the item). */
  path: Seg[];
  /** Other places it also shows in without living there: the person's own collections. */
  also?: Seg[][];
  labels: string[];
  /** The group the item belongs to for "kind" filters: language, repository kind... */
  kind?: string;
  tone: Tone;
  /** The last result in words ("Passed", "Failed"), when it was (ms) and a link to it. */
  last?: { text: string; at: number; href?: string };
  /** Recent results, oldest first. */
  history: Tone[];
  running: number;
  href: string;
  favorite: boolean;
  mine?: boolean;
  /** Quick actions that exist for this item. */
  actions?: { label: string; href: string }[];
}

export interface XNode {
  key: string; // the path keys joined with "/"
  label: string;
  depth: number;
  parent: string | null;
  children: string[];
  folderId?: string;
  count: number; // items in it and below
  tones: Record<Tone, number>;
}

export const ROOT = "";
const ORDER: Tone[] = ["bad", "wait", "run", "ok", "none"];

/** The worst tone present: failed, then waiting, running, passed. */
export function worst(t: Record<Tone, number>): Tone {
  return ORDER.find((k) => t[k] > 0) ?? "none";
}
export const emptyTones = (): Record<Tone, number> => ({ bad: 0, wait: 0, run: 0, ok: 0, none: 0 });

export function nodeKey(path: Seg[]): string { return path.map((s) => s.key).join("/"); }

/** Builds the tree of nodes from the places of the items, plus folders that are empty (so they can be filed into). */
export function buildTree(items: XItem[], emptyFolders: { path: Seg[] }[] = []): Map<string, XNode> {
  const nodes = new Map<string, XNode>();
  nodes.set(ROOT, { key: ROOT, label: "", depth: 0, parent: null, children: [], count: 0, tones: emptyTones() });
  const ensure = (path: Seg[]) => {
    for (let i = 1; i <= path.length; i++) {
      const k = nodeKey(path.slice(0, i));
      if (!nodes.has(k)) {
        const parent = nodeKey(path.slice(0, i - 1));
        nodes.set(k, { key: k, label: path[i - 1].label, depth: i, parent, children: [], folderId: path[i - 1].folderId, count: 0, tones: emptyTones() });
        nodes.get(parent)!.children.push(k);
      } else if (path[i - 1].folderId && !nodes.get(k)!.folderId) nodes.get(k)!.folderId = path[i - 1].folderId;
    }
  };
  for (const f of emptyFolders) ensure(f.path);
  for (const it of items) {
    const seen = new Set<string>();
    for (const path of [it.path, ...(it.also ?? [])]) {
      ensure(path);
      for (let i = 0; i <= path.length; i++) {
        const k = nodeKey(path.slice(0, i));
        if (seen.has(k)) continue; // an item counts once in a place, however it got there
        seen.add(k);
        const n = nodes.get(k)!;
        n.count++;
        n.tones[it.tone]++;
      }
    }
  }
  for (const n of nodes.values()) n.children.sort((a, b) => nodes.get(a)!.label.localeCompare(nodes.get(b)!.label, undefined, { numeric: true }));
  return nodes;
}

/** The visible rows of the tree, depth first, given which nodes are open. */
export function visibleNodes(nodes: Map<string, XNode>, open: Set<string>, keep?: (n: XNode) => boolean): XNode[] {
  const out: XNode[] = [];
  const walk = (k: string) => {
    for (const c of nodes.get(k)!.children) {
      const n = nodes.get(c)!;
      if (keep && !keep(n)) continue;
      out.push(n);
      if (open.has(c)) walk(c);
    }
  };
  walk(ROOT);
  return out;
}

/** Splits a path-like name into folders and a leaf: "payments/api/build" lives in payments, then api. */
export function derivePath(name: string): { path: Seg[]; leaf: string } {
  const parts = name.split("/").filter(Boolean);
  if (parts.length < 2) return { path: [], leaf: name };
  const leaf = parts.pop()!;
  return { path: parts.map((p, i) => ({ key: "~" + parts.slice(0, i + 1).join("/"), label: p })), leaf };
}

// ---- matching ----

/** How well q matches text, 0 for not at all: exact, prefix, word prefix, substring, one typo, letters in order. */
export function score(q: string, text: string): number {
  q = q.toLowerCase(); text = text.toLowerCase();
  if (!q || !text) return 0;
  if (text === q) return 100;
  if (text.startsWith(q)) return 85;
  if (text.split(/[^a-z0-9]+/).some((w) => w.startsWith(q))) return 70;
  if (text.includes(q)) return 55;
  if (q.length >= 3) {
    for (const w of [...text.split(/[^a-z0-9]+/), text]) {
      if (within1(q, w.slice(0, q.length + 1)) || within1(q, w.slice(0, q.length)) || within1(q, w.slice(0, q.length - 1))) return 35;
    }
    let i = 0;
    for (const c of text) if (c === q[i]) i++;
    if (i === q.length) return 20;
  }
  return 0;
}

function within1(a: string, b: string): boolean {
  if (a === b) return true;
  if (Math.abs(a.length - b.length) > 1) return false;
  let i = 0;
  while (i < a.length && i < b.length && a[i] === b[i]) i++;
  if (a.length === b.length) return a.slice(i + 1) === b.slice(i + 1) || (a[i] === b[i + 1] && a[i + 1] === b[i] && a.slice(i + 2) === b.slice(i + 2));
  return a.length > b.length ? a.slice(i + 1) === b.slice(i) : a.slice(i) === b.slice(i + 1);
}

/** The best score of a query against an item's name, description, labels and place. Every word of the query must match. */
export function itemScore(q: string, it: XItem): number {
  const words = q.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return 1;
  let total = 0;
  for (const w of words) {
    const nm = score(w, it.name);
    const best = Math.max(nm > 0 ? nm + 5 : 0, score(w, it.description ?? ""), ...it.labels.map((l) => score(w, l)), score(w, it.path.map((p) => p.label).join(" ")), score(w, it.kind ?? ""));
    if (best <= 0) return 0;
    total += best;
  }
  return total / words.length;
}

/** Splits text around the places q matches, for highlighting: [text, matched][]. */
export function highlight(text: string, q: string): [string, boolean][] {
  const words = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return [[text, false]];
  const lower = text.toLowerCase();
  const marks = new Array<boolean>(text.length).fill(false);
  for (const w of words) {
    let at = lower.indexOf(w);
    while (at >= 0) { for (let i = at; i < at + w.length; i++) marks[i] = true; at = lower.indexOf(w, at + w.length); }
  }
  const out: [string, boolean][] = [];
  let start = 0;
  for (let i = 1; i <= text.length; i++) {
    if (i === text.length || marks[i] !== marks[start]) { out.push([text.slice(start, i), marks[start]]); start = i; }
  }
  return out;
}
