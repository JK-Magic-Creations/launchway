// Long text kept out of the scripts. A page that has a lot of explaining to do keeps its sentences in
// src/prose/<module>.json (key t0, t1 ... in the order they appear in the source) and reads them as P.t0 at the top of
// the module: const P = await prose("pages.Run"). The build gives each file a name that carries its content hash and
// ships it gzipped beside the scripts (assets/), where the server sends it with a year of caching and an ETag, so a
// returning visit reads it from the browser's cache. The module finishes loading when its text has arrived, which is
// while the page's own "Loading" state is showing, so nothing is drawn with a gap where a paragraph will be.
//
// The text is the one source: find a sentence with grep in src/prose, then its key with grep for P.tN in the module
// of the same name. The wording, vocabulary and originality checks read these files like any source file.

import { readJson } from "./readJson";

const files = import.meta.glob("./prose/*.json", { query: "?url", import: "default", eager: true }) as Record<string, string>;

export type Prose = Record<string, string>;

const memo = new Map<string, Promise<Prose>>();

/** The sentences of one module, by key. Asked once per module; a failed read is asked again on the next visit. */
export function prose(name: string): Promise<Prose> {
  let p = memo.get(name);
  if (!p) {
    const url = files[`./prose/${name}.json`];
    p = url ? readJson<Prose>(url) : Promise.reject(new Error(`No text file for ${name}.`));
    memo.set(name, p);
    p.catch(() => memo.delete(name));
  }
  return p;
}
