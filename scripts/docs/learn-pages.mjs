// Which pages of docs/ are in the product's Learn area, and what their addresses are. Shared by
// build-learn.mjs and render-diagrams.mjs.
import { createHash } from 'node:crypto'
import { readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

// Everything except the pages that are about the public site, the roadmap or building Launchway.
export const EXCLUDE = [/^roadmap\//, /^contributing\//, /^development\//, /^demo\.md$/, /^pending\.md$/, /^index\.md$/,
  /^roadmap-list\.md$/, /^node_modules\//, /^\.vitepress\//, /^README\.md$/, /^public\//]

function walk(dir) {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}

/** The address of a page inside Learn: docs/learn/x.md is x, docs/guide/index.md is guide. */
export function slugOf(rel) {
  const s = rel.replace(/\.md$/, '').replace(/^learn\//, '')
  return s === 'index' ? '' : s.replace(/\/index$/, '')
}

/** The Markdown files of Learn, as paths below docs/. */
export function learnFiles(docs) {
  return walk(docs).map((f) => relative(docs, f).split('\\').join('/'))
    .filter((f) => f.endsWith('.md') && !EXCLUDE.some((r) => r.test(f))).sort()
}

/** The name a diagram's drawing is kept under: a hash of its source. */
export const diagramKey = (src) => createHash('sha256').update(src.trim()).digest('hex').slice(0, 16)

/** Every ```mermaid block of a Markdown text. */
export function mermaidBlocks(src) {
  return [...src.matchAll(/```mermaid\n([\s\S]*?)```/g)].map((m) => m[1])
}
