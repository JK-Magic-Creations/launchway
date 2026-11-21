// The documentation is kept honest per release: every place the product points into it, and every place
// it points into the product, must exist. This reads docs/ and the product's own tables; nothing is built.
import { describe, expect, it } from "vitest";
import wizardsSource from "../pages/wizards/Wizards.tsx?raw";
import navSource from "../../../docs/learn/nav.json?raw";
import { locate, PAGES } from "../nav/model";
import { EXPLAIN, KEY_ANCHOR, LEARN_FOR, WIZARD_LEARN, type LearnTarget } from "./links";

// The Markdown of every page that is in Learn (see scripts/docs/learn-pages.mjs), by its path below docs/.
const raw = import.meta.glob(
  ["../../../docs/*.md", "../../../docs/{guide,howto,reference,start,tutorials,security,learn,paths}/**/*.md"],
  { query: "?raw", import: "default", eager: true },
) as Record<string, string>;
const SOURCES = new Map(Object.entries(raw).map(([k, v]) => [k.replace("../../../docs/", ""), v]));
const SKIP = new Set(["demo.md", "pending.md", "index.md", "roadmap-list.md"]);
const read = (rel: string) => SOURCES.get(rel) ?? "";
const has = (rel: string) => SOURCES.has(rel);
const dirname = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");
/** A path joined to a directory, with . and .. folded away. */
function join(dir: string, rel: string): string {
  const out = dir === "" ? [] : dir.split("/");
  for (const part of rel.split("/")) { if (part === "..") out.pop(); else if (part !== ".") out.push(part); }
  return out.join("/");
}

/** The file of a Learn page: docs/<slug>.md, docs/<slug>/index.md, or the same under docs/learn/. */
function fileOf(slug: string): string | null {
  for (const base of ["", "learn/"]) {
    for (const f of [`${base}${slug}.md`, `${base}${slug}/index.md`]) if (slug !== "" && has(f)) return f;
  }
  return null;
}

/** GitHub-style heading anchors, as the site and the product make them. */
function anchors(src: string): Set<string> {
  const seen = new Map<string, number>();
  const out = new Set<string>();
  let fenced = false;
  for (const line of src.split("\n")) {
    if (/^```/.test(line)) { fenced = !fenced; continue; }
    const m = !fenced && line.match(/^#{1,6}\s+(.*?)\s*#*\s*$/);
    if (!m) continue;
    const text = m[1].replace(/\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/[`*_]/g, "");
    let slug = text.toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, "").replace(/\s/g, "-");
    const n = seen.get(slug) ?? 0;
    seen.set(slug, n + 1);
    if (n > 0) slug += "-" + n;
    out.add(slug);
  }
  return out;
}

const learnMarkdown = (): string[] => [...SOURCES.keys()].filter((f) => !SKIP.has(f));

function checkTarget(where: string, t: LearnTarget): string[] {
  const f = fileOf(t.slug);
  if (!f) return [`${where}: there is no page ${t.slug}`];
  if (t.anchor && !anchors(read(f)).has(t.anchor)) return [`${where}: ${f} has no heading #${t.anchor}`];
  return [];
}

describe("the product's links into Learn", () => {
  it("point at pages of the product", () => {
    const patterns = new Set(PAGES.map((p) => p.pattern));
    for (const k of Object.keys(LEARN_FOR)) expect(patterns.has(k), `${k} is not a page of the product`).toBe(true);
  });
  it("point at pages and headings that exist", () => {
    const bad = [
      ...Object.entries(LEARN_FOR).flatMap(([k, t]) => checkTarget(k, t)),
      ...Object.entries(WIZARD_LEARN).flatMap(([k, t]) => checkTarget("wizard " + k, t)),
      ...Object.entries(EXPLAIN).flatMap(([k, t]) => checkTarget("explain " + k, t)),
      ...Object.entries(KEY_ANCHOR).flatMap(([k, a]) => checkTarget("pipeline key " + k, { slug: "reference/pipeline", anchor: a, label: k })),
    ];
    expect(bad).toEqual([]);
  });
  it("name every wizard", () => {
    const wizards = [...wizardsSource.matchAll(/case "([a-z-]+)": return <\w+Wizard/g)].map((m) => m[1]);
    expect(wizards.length).toBeGreaterThan(5);
    expect(Object.keys(WIZARD_LEARN).sort()).toEqual(wizards.sort());
  });
});

describe("the Learn pages' links", () => {
  const pages = learnMarkdown();
  it("to the product are routes that exist", () => {
    const bad: string[] = [];
    for (const f of pages) {
      for (const m of read(f).matchAll(/\]\(app:(\/[^)\s#]*)(?:#[^)]*)?\)/g)) {
        const route = m[1];
        if (!locate(route)) bad.push(`${f}: ${route} is not a page of the product`);
        else if (route.startsWith("/config/wizard/") && !(route.slice(15) in WIZARD_LEARN)) bad.push(`${f}: ${route} is not a wizard`);
      }
    }
    expect(bad).toEqual([]);
  });
  it("to other pages and their headings exist", () => {
    const bad: string[] = [];
    for (const f of pages) {
      // Screenshots are not shipped in the product (they would show an older interface); their text is.
      const src = read(f).replace(/```[\s\S]*?```/g, "").replace(/!\[[^\]]*\]\([^)]*\)/g, "");
      for (const m of src.matchAll(/\]\((?!https?:|mailto:|app:)([^)\s]*)\)/g)) {
        const [path, hash] = m[1].split("#");
        const target = path === "" ? f : join(dirname(f), path);
        // Pages of the public site that are not shipped in the product (the roadmap, how it is built): shown as text.
        if (/^(public|roadmap|contributing|development)\//.test(target)) continue;
        if (!target.endsWith(".md")) { bad.push(`${f}: ${m[1]} is not a Markdown page`); continue; }
        if (!has(target)) { bad.push(`${f}: ${m[1]} does not exist`); continue; }
        if (hash && !anchors(read(target)).has(hash)) bad.push(`${f}: ${target} has no heading #${hash}`);
      }
    }
    expect(bad).toEqual([]);
  });
  it("are all in the contents", () => {
    const nav = JSON.parse(navSource) as { sections: { items: (string | { slug: string })[] }[]; paths: { steps: string[] }[] };
    const slugs = nav.sections.flatMap((s) => s.items.map((i) => (typeof i === "string" ? i : i.slug)));
    const missing = [...slugs, ...nav.paths.flatMap((p) => p.steps)].filter((s) => !fileOf(s));
    expect(missing).toEqual([]);
  });
});
