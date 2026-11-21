import { learnHref, loadIndex } from "./content";
import { search } from "./search";

/** The documentation sections that answer a query, for the finder: from the index that ships in the binary, so it works offline. */
export async function learnHits(query: string, limit = 5): Promise<{ title: string; detail: string; href: string }[]> {
  const idx = await loadIndex();
  return search(idx, query, limit).map((h) => ({ title: h.heading, detail: h.snippet.slice(0, 100), href: learnHref(h.slug, h.anchor) }));
}
