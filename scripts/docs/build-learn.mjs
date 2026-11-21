#!/usr/bin/env node
// Builds the documentation that ships inside the Launchway binary: the Learn area.
//
// The same Markdown sources as the public site (docs/) are rendered with the site's own Markdown
// renderer (VitePress, so headings, anchors, notes and code highlighting match), cut into one JSON
// file per page, and written beside the web UI in web/dist/learn-content/, from where Go's embed
// takes them into the binary. Diagrams are not drawn here: they are pre-rendered SVG (light and
// dark) kept in docs/.vitepress/diagrams, made by `make docs-diagrams-cache`, so this step needs
// no browser and no network. A prebuilt full-text index is written too.
//
// Usage: node scripts/docs/build-learn.mjs [--out web/dist/learn-content] [--check]
//   --check  validate everything and write nothing.
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { gzipSync } from 'node:zlib'
import { dirname, join, posix, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { diagramKey, learnFiles, slugOf } from './learn-pages.mjs'

process.env.LAUNCHWAY_LEARN = '1'
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const docs = join(root, 'docs')
const args = process.argv.slice(2)
const check = args.includes('--check')
const outDir = resolve(root, args.includes('--out') ? args[args.indexOf('--out') + 1] : 'web/dist/learn-content')

// ---- which pages ----
const files = learnFiles(docs)
const slugs = new Map(files.map((f) => [slugOf(f), f]))

const navConfig = JSON.parse(readFileSync(join(docs, 'learn', 'nav.json'), 'utf8'))
const problems = []
const fail = (m) => problems.push(m)

// ---- markdown ----
const { createMarkdownRenderer } = await import(pathToFileURL(join(docs, 'node_modules/vitepress/dist/node/index.js')).href)
const cfg = (await import(pathToFileURL(join(docs, '.vitepress/config.mts')).href)).default
const md = await createMarkdownRenderer(docs, cfg.markdown, '/', undefined)

const diagramDir = join(docs, '.vitepress/diagrams')

function text(html) {
  return html.replace(/<button[^>]*class="copy"[^>]*><\/button>/g, '').replace(/<a class="header-anchor"[^>]*>.*?<\/a>/g, '')
    .replace(/<style[\s\S]*?<\/style>/g, '').replace(/<[^>]+>/g, ' ').replace(/&ZeroWidthSpace;/g, '')
    .replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, '&')
    .replace(/\s+/g, ' ').trim()
}

// Shiki writes a style attribute per word; the same colour pairs are shared as classes instead.
const palette = []
const paletteIdx = new Map()
function shareColours(html) {
  return html.replace(/<span style="--shiki-light:(#[0-9A-Fa-f]+)(?:;--shiki-dark:(#[0-9A-Fa-f]+))?">/g, (_, l, d) => {
    const k = `${l}|${d ?? l}`
    if (!paletteIdx.has(k)) { paletteIdx.set(k, palette.length); palette.push([l, d ?? l]) }
    return `<span class="c${paletteIdx.get(k)}">`
  })
}

const diagrams = new Set()
/** The words of a diagram, for a reader who cannot see it: a flowchart as "A leads to B" lines, a sequence diagram as
 * "A to B: message" lines. Anything else is shown as its source. Returns HTML for a <details> block. */
function diagramText(code) {
  const esc = (t) => t.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  const clean = (t) => esc(t.replace(/<br\s*\/?>/gi, ', ').replace(/^"|"$/g, '').trim())
  const lines = code.split('\n').map((l) => l.trim()).filter(Boolean)
  const kind = lines[0] ?? ''
  const items = []
  if (/^(flowchart|graph)\b/.test(kind)) {
    const label = new Map()
    const node = (t) => {
      const m = /^([A-Za-z0-9_]+)\s*(?:\[\(?"?([^\]"]*)"?\)?\]|\(+"?([^)"]*)"?\)+|\{"?([^}"]*)"?\})?$/.exec(t.trim())
      if (!m) return clean(t)
      const text = m[2] ?? m[3] ?? m[4]
      if (text !== undefined) label.set(m[1], clean(text))
      return label.get(m[1]) ?? m[1]
    }
    for (const l of lines.slice(1)) {
      const m = /^(.+?)\s*(?:-->|-\.->|==>|---)\s*(?:\|([^|]*)\|\s*)?(.+)$/.exec(l.replace(/\s--\s*("[^"]*"|[^>]*?)\s*-->/, ' -->|$1| '))
      if (m) { const from = node(m[1]); const to = node(m[3]); items.push(`${from} leads to ${to}${m[2] ? ` (${clean(m[2])})` : ''}.`) } else node(l)
    }
  } else if (/^sequenceDiagram\b/.test(kind)) {
    const who = new Map()
    for (const l of lines.slice(1)) {
      let m
      if ((m = /^(?:participant|actor)\s+(\S+)(?:\s+as\s+(.+))?$/.exec(l))) who.set(m[1], clean(m[2] ?? m[1]))
      else if ((m = /^(\S+?)\s*(?:->>|-->>|->|-->|-x)\+?\s*(\S+?):\s*(.+)$/.exec(l))) items.push(`${who.get(m[1]) ?? esc(m[1])} to ${who.get(m[2]) ?? esc(m[2])}: ${clean(m[3])}.`)
      else if ((m = /^Note (?:over|left of|right of)\s+([^:]+):\s*(.+)$/.exec(l))) items.push(`Note on ${who.get(m[1].trim()) ?? esc(m[1].trim())}: ${clean(m[2])}.`)
    }
  }
  if (items.length === 0) return `<details class="lx-text"><summary>The diagram as text</summary><pre>${esc(code)}</pre></details>`
  return `<details class="lx-text"><summary>The diagram as text</summary><ol>${items.map((i) => `<li>${i}</li>`).join('')}</ol></details>`
}

function renderPage(rel) {
  let src = readFileSync(join(docs, rel), 'utf8')
  src = src.replace(/^---\n[\s\S]*?\n---\n/, '')
  const dir = posix.dirname(rel)
  let html = md.render(src, { relativePath: rel, path: rel })
  // Diagrams: the pre-rendered drawings.
  html = html.replace(/<Mermaid code="([^"]*)" ?\/>/g, (_, code) => {
    const key = diagramKey(decodeURIComponent(code))
    const light = join(diagramDir, key + '.light.svg'), dark = join(diagramDir, key + '.dark.svg')
    if (!existsSync(light) || !existsSync(dark)) { fail(`${rel}: a diagram has no pre-rendered drawing (${key}); run make docs-diagrams-cache`); return '' }
    diagrams.add(key)
    // A wide drawing may shrink to four fifths of its size to fit; below that it scrolls, so its words stay readable.
    const draw = (f) => readFileSync(f, 'utf8').replace(/<svg ([^>]*?)viewBox="[-\d.]+ [-\d.]+ ([\d.]+) [\d.]+"/, (m, pre, w) => m.replace(/ style="/, ` style="min-width: ${Math.round(+w * 0.8)}px; `))
    // The drawing is hidden from screen readers; the same content follows as text. The figure scrolls, so it takes focus.
    return `<figure class="lx-diagram" role="group" aria-label="Diagram, with its text below" tabindex="0"><div class="lx-light" aria-hidden="true">${draw(light)}</div><div class="lx-dark" aria-hidden="true">${draw(dark)}</div>${diagramText(decodeURIComponent(code))}</figure>`
  })
  // Links: to a Learn page, to a place in the product (app:), or to something that is not here.
  html = html.replace(/<a ([^>]*?)href="([^"]*)"([^>]*)>([\s\S]*?)<\/a>/g, (all, pre, href, post, inner) => {
    if (/class="header-anchor"/.test(all)) return ''
    if (href.startsWith('#')) return `<a href="${href}">${inner}</a>`
    if (href.startsWith('app:')) return `<a class="lx-app" href="${href.slice(4)}" data-app="1">${inner}</a>`
    if (/^(https?:|mailto:)/.test(href)) return `<a class="lx-ext" href="${href}" target="_blank" rel="noopener noreferrer">${inner}</a>`
    const m = href.match(/^([^#?]*)(#.*)?$/)
    let target = posix.normalize(posix.join(dir, m[1])).replace(/\.html$/, '.md')
    if (m[1] === '') target = rel
    else if (m[1].endsWith('/')) target = posix.join(target, 'index.md')
    else if (!target.endsWith('.md')) target += '.md'
    const slug = slugOf(target.replace(/^\.\//, ''))
    if (slugs.has(slug) || (slug === '' && false)) return `<a href="/learn/${slug}${m[2] ?? ''}">${inner}</a>`
    return `<span class="lx-off" title="This page is on the public website only">${inner}</span>`
  })
  html = html.replace(/<img [^>]*alt="([^"]*)"[^>]*>/g, '<em class="lx-img">[$1]</em>')
  html = html.replace(/<button title="Copy Code" class="copy"><\/button>/g, '<button type="button" class="copy" aria-label="Copy the code"></button>')
  html = html.replace(/ v-pre=""/g, '').replace(/ tabindex="0"/g, ' tabindex="0"')
  // <!-- try --> before a code block makes it a pipeline the reader can open in the playground.
  html = html.replace(/<!-- try -->\s*<div class="language-yaml /g, '<div class="lx-try language-yaml ')
  html = shareColours(html)
  return html
}

function plainTitle(html) {
  const m = html.match(/<h1[^>]*>([\s\S]*?)<\/h1>/)
  return m ? text(m[1]) : ''
}
function headings(html) {
  const out = []
  for (const m of html.matchAll(/<h([23]) id="([^"]+)"[^>]*>([\s\S]*?)<\/h\1>/g)) out.push({ level: +m[1], id: m[2], text: text(m[3]) })
  return out
}
// The page without its first heading, which the viewer shows as the title.
function body(html) { return html.replace(/<h1[^>]*>[\s\S]*?<\/h1>\s*/, '') }

// ---- navigation ----
const used = new Set()
const pages = new Map()
for (const [slug, rel] of slugs) {
  const html = renderPage(rel)
  const title = plainTitle(html) || slug
  const firstP = html.replace(/<h1[\s\S]*?<\/h1>/, '').match(/<p>([\s\S]*?)<\/p>/)
  pages.set(slug, { slug, rel, html: body(html), title, desc: firstP ? text(firstP[1]).slice(0, 220) : '', toc: headings(html) })
}
const sections = navConfig.sections.map((s) => ({
  id: s.id, title: s.title, blurb: s.blurb,
  items: s.items.map((it) => {
    const slug = typeof it === 'string' ? it : it.slug
    if (!pages.has(slug)) { fail(`learn/nav.json: ${slug} is not a page`); return null }
    used.add(slug)
    return { slug, title: (typeof it === 'object' && it.title) || pages.get(slug).title }
  }).filter(Boolean),
}))
const paths = navConfig.paths.map((p) => {
  for (const s of p.steps) if (!pages.has(s)) fail(`learn/nav.json: path ${p.id} names ${s}, which is not a page`)
  return p
})
for (const slug of pages.keys()) if (!used.has(slug)) fail(`${slugs.get(slug)} is not in learn/nav.json: add it to a section, or exclude it in build-learn.mjs`)

const order = sections.flatMap((s) => s.items.map((i) => ({ ...i, section: s.id })))
const idxOf = new Map(order.map((o, i) => [o.slug, i]))

// ---- the search index ----
const STOP = new Set('the a an and or of to in is it for on with as at by be this that are you your can how what from not if then when do does'.split(' '))
const tok = (s) => (s.toLowerCase().match(/[a-z0-9][a-z0-9_.-]*[a-z0-9]|[a-z0-9]/g) || []).flatMap((w) => {
  const parts = w.split(/[.\-_]/).filter((x) => x.length > 1)
  return parts.length > 1 ? [w, ...parts] : [w]
}).filter((w) => !STOP.has(w))
const chunks = [], terms = new Map(), pageList = order.map((o) => o.slug)
function addTerm(t, id, w) {
  let e = terms.get(t); if (!e) terms.set(t, e = new Map())
  e.set(id, Math.max(e.get(id) ?? 0, 0) + w)
}
for (const slug of pageList) {
  const pg = pages.get(slug)
  // cut the page at each heading
  const parts = pg.html.split(/(?=<h[23] id=")/)
  parts.forEach((part, i) => {
    const hm = part.match(/^<h([23]) id="([^"]+)"[^>]*>([\s\S]*?)<\/h\1>/)
    const heading = hm ? text(hm[3]) : ''
    const t = text(hm ? part.slice(hm[0].length) : part)
    if (!hm && !t) return
    const id = chunks.length
    const para = (hm ? part.slice(hm[0].length) : part).match(/<p>([\s\S]*?)<\/p>/)
    chunks.push([pageList.indexOf(slug), hm ? hm[2] : '', heading, (para ? text(para[1]) : t).slice(0, 150)])
    for (const w of tok(t)) addTerm(w, id, 1)
    for (const w of tok(heading)) addTerm(w, id, 4)
    for (const w of tok(pg.title)) addTerm(w, id, i === 0 ? 3 : 1)
  })
}
const termsOut = {}
for (const [t, e] of [...terms].sort((a, b) => (a[0] < b[0] ? -1 : 1))) {
  if (e.size > chunks.length * 0.6) continue // a word on nearly every section finds nothing
  termsOut[t] = [...e].flat()
}

// ---- write ----
// Every file is gzipped (the browser unpacks it): the binary carries a fifth of the text.
const gz = (o) => gzipSync(JSON.stringify(o), { level: 9 })
if (problems.length) {
  console.error('build-learn: ' + problems.length + ' problem(s):\n  ' + [...new Set(problems)].join('\n  '))
  process.exit(1)
}
const manifest = {
  pages: order.map((o) => ({ slug: o.slug, title: o.title, section: o.section, desc: pages.get(o.slug).desc })),
  sections, paths, palette,
}
if (!check) {
  rmSync(outDir, { recursive: true, force: true })
  mkdirSync(join(outDir, 'pages'), { recursive: true })
  for (const o of order) {
    const p = pages.get(o.slug), i = idxOf.get(o.slug)
    const out = { slug: o.slug, title: o.title, section: o.section, html: p.html, toc: p.toc,
      prev: i > 0 ? order[i - 1] : null, next: i < order.length - 1 ? order[i + 1] : null }
    const f = join(outDir, 'pages', o.slug + '.json')
    mkdirSync(dirname(f), { recursive: true })
    writeFileSync(f + '.gz', gz(out))
  }
  writeFileSync(join(outDir, 'manifest.json.gz'), gz(manifest))
  writeFileSync(join(outDir, 'search.json.gz'), gz({ pages: pageList, chunks, terms: termsOut }))
}
const bytes = (f) => (existsSync(f) ? statSync(f).size : 0)
console.log(`build-learn: ${order.length} pages, ${diagrams.size} diagrams, ${chunks.length} search sections` +
  (check ? ' (checked, nothing written)' : `; manifest ${bytes(join(outDir, 'manifest.json.gz'))} B, search ${bytes(join(outDir, 'search.json.gz'))} B (gzipped)`))
