// The documentation that ships inside the binary: pages, the table of contents and the search index,
// built from docs/ at release time (scripts/docs/build-learn.mjs) and served from /learn-content/.
// Every file is gzipped; the browser unpacks it here. Nothing is fetched from outside this server.

export interface NavItem { slug: string; title: string }
export interface NavSection { id: string; title: string; blurb: string; items: NavItem[] }
export interface LearnPath { id: string; title: string; who: string; steps: string[] }
export interface Manifest {
  pages: { slug: string; title: string; section: string; desc: string }[];
  sections: NavSection[];
  paths: LearnPath[];
  /** Code colours: light and dark, referred to by the classes c0, c1 ... in the pages. */
  palette: [string, string][];
}
export interface Article {
  slug: string; title: string; section: string; html: string;
  toc: { level: number; id: string; text: string }[];
  prev: (NavItem & { section: string }) | null;
  next: (NavItem & { section: string }) | null;
}
export interface SearchIndex {
  pages: string[];
  /** page index, anchor, heading, the first words of the section */
  chunks: [number, string, string, string][];
  /** word -> chunk, weight, chunk, weight ... */
  terms: Record<string, number[]>;
}

const BASE = "/learn-content/";

async function unpack<T>(file: string): Promise<T> {
  const res = await fetch(BASE + file + ".gz");
  if (!res.ok || !res.body) throw new Error(res.status === 404 ? "This page is not in the documentation of this release." : `The documentation did not load (${res.status}).`);
  const text = await new Response(res.body.pipeThrough(new DecompressionStream("gzip"))).text();
  return JSON.parse(text) as T;
}

const memo = new Map<string, Promise<unknown>>();
function once<T>(file: string): Promise<T> {
  let p = memo.get(file) as Promise<T> | undefined;
  if (!p) {
    p = unpack<T>(file);
    memo.set(file, p);
    p.catch(() => memo.delete(file));
  }
  return p;
}

export const loadManifest = () => once<Manifest>("manifest.json");
export const loadArticle = (slug: string) => once<Article>(`pages/${slug}.json`);
export const loadIndex = () => once<SearchIndex>("search.json");

/** The address of a Learn page: slug, and an optional heading on it. */
export function learnHref(slug: string, anchor?: string): string {
  return "/learn" + (slug ? "/" + slug : "") + (anchor ? "#" + anchor : "");
}
