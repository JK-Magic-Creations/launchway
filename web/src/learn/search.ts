// Full-text search over the prebuilt index. It runs in the browser, on a file that ships in the binary,
// so it works with no network. The words are cut the way scripts/docs/build-learn.mjs cuts them.

import type { SearchIndex } from "./content";

const STOP = new Set("the a an and or of to in is it for on with as at by be this that are you your can how what from not if then when do does".split(" "));

export function words(s: string): string[] {
  const out: string[] = [];
  for (const w of s.toLowerCase().match(/[a-z0-9][a-z0-9_.-]*[a-z0-9]|[a-z0-9]/g) ?? []) {
    const parts = w.split(/[.\-_]/).filter((x) => x.length > 1);
    if (parts.length > 1) out.push(w, ...parts); else out.push(w);
  }
  return out.filter((w) => !STOP.has(w));
}

export interface Hit { slug: string; anchor: string; heading: string; snippet: string; score: number }

const keysOf = new WeakMap<SearchIndex, string[]>();
function keys(idx: SearchIndex): string[] {
  let k = keysOf.get(idx);
  if (!k) { k = Object.keys(idx.terms); keysOf.set(idx, k); }
  return k;
}

/** The sections that answer the question, best first. Every word must be found (the last may be unfinished);
 * if no section has them all, the ones that have most of them are shown. */
export function search(idx: SearchIndex, query: string, limit = 25): Hit[] {
  const qs = words(query);
  if (qs.length === 0) return [];
  const per: Map<number, number>[] = qs.map((q, i) => {
    const m = new Map<number, number>();
    const add = (list: number[] | undefined, mult: number) => {
      if (!list) return;
      for (let j = 0; j < list.length; j += 2) m.set(list[j], (m.get(list[j]) ?? 0) + list[j + 1] * mult);
    };
    add(idx.terms[q], 3);
    // Unfinished words: the last one always, and any word of three letters or more.
    if (i === qs.length - 1 || q.length >= 3) for (const k of keys(idx)) if (k !== q && k.startsWith(q)) add(idx.terms[k], 1);
    return m;
  });
  const score = new Map<number, number>();
  const count = new Map<number, number>();
  for (const m of per) for (const [id, s] of m) { score.set(id, (score.get(id) ?? 0) + s); count.set(id, (count.get(id) ?? 0) + 1); }
  const best = Math.max(0, ...count.values());
  const need = best === qs.length ? qs.length : Math.max(1, best);
  const ranked = [...score].filter(([id]) => (count.get(id) ?? 0) >= need).sort((a, b) => b[1] - a[1] || a[0] - b[0]);
  // The best section of each page first; further sections of a page only after every page has had one.
  const seen = new Set<number>();
  const first: Hit[] = [], more: Hit[] = [];
  for (const [id, s] of ranked) {
    const [p, anchor, heading, snippet] = idx.chunks[id];
    const hit = { slug: idx.pages[p], anchor, heading, snippet, score: s };
    (seen.has(p) ? more : first).push(hit);
    seen.add(p);
  }
  return [...first, ...more].slice(0, limit);
}
