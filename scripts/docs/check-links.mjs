#!/usr/bin/env node
// Checks every internal link and #anchor in the built docs site.
// Usage: node scripts/docs/check-links.mjs [dist-dir] [base]
// Plain Node, no dependencies. Exits 1 if a link points to a missing page,
// file or anchor.
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, relative, dirname, posix } from 'node:path'

const dist = process.argv[2] || 'docs/.vitepress/dist'
const base = (process.argv[3] || process.env.DOCS_BASE || '/launchway/').replace(/\/?$/, '/')

function walk(dir) {
  const out = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...walk(p))
    else out.push(p)
  }
  return out
}

const files = walk(dist)
const html = files.filter((f) => f.endsWith('.html'))
const idsCache = new Map()
function idsOf(file) {
  if (!idsCache.has(file)) {
    const s = readFileSync(file, 'utf8')
    const ids = new Set()
    for (const m of s.matchAll(/\sid="([^"]+)"/g)) ids.add(decodeEntities(m[1]))
    for (const m of s.matchAll(/\sname="([^"]+)"/g)) ids.add(decodeEntities(m[1]))
    idsCache.set(file, ids)
  }
  return idsCache.get(file)
}
function decodeEntities(s) {
  // &amp; last, so that "&amp;quot;" ends as the text &quot;, not as a quote
  return s.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&')
}

function resolveTarget(fromFile, href) {
  // Returns the file in dist the link points to, or null.
  let [path] = href.split('#')
  path = path.split('?')[0]
  let abs
  if (path === '') abs = '/' + relative(dist, fromFile).split('\\').join('/')
  else if (path.startsWith('/')) {
    if (!path.startsWith(base)) return { error: `outside the base ${base}` }
    abs = '/' + path.slice(base.length)
  } else {
    const fromDir = posix.dirname('/' + relative(dist, fromFile).split('\\').join('/'))
    abs = posix.normalize(posix.join(fromDir, path))
  }
  const candidates = [abs, abs + '.html', posix.join(abs, 'index.html')]
  for (const c of candidates) {
    const f = join(dist, decodeURIComponent(c))
    if (existsSync(f) && statSync(f).isFile()) return { file: f }
  }
  return { error: 'no such page or file' }
}

let problems = 0
let checked = 0
for (const file of html) {
  const s = readFileSync(file, 'utf8')
  for (const m of s.matchAll(/<a\s[^>]*?href="([^"]*)"/g)) {
    const href = decodeEntities(m[1])
    if (/blob\/[^/]+\/(\.\.\/|.*\/\.\.\/)/.test(href)) {
      console.error(`${relative(dist, file)}: ${href}: a GitHub link with ".." in it does not resolve; link to the page with a relative path`)
      problems++
      continue
    }
    if (/^(https?:|mailto:|data:|javascript:)/.test(href) || href.startsWith('//')) continue
    checked++
    const t = resolveTarget(file, href)
    const where = relative(dist, file)
    if (t.error) {
      console.error(`${where}: ${href}: ${t.error}`)
      problems++
      continue
    }
    const hash = href.includes('#') ? decodeURIComponent(href.split('#')[1]) : ''
    if (hash && t.file.endsWith('.html') && !idsOf(t.file).has(hash)) {
      console.error(`${where}: ${href}: no anchor #${hash} in ${relative(dist, t.file)}`)
      problems++
    }
  }
}
if (problems) {
  console.error(`check-links: ${problems} broken link(s) of ${checked} checked`)
  process.exit(1)
}
console.log(`check-links: ${checked} internal links and anchors in ${html.length} pages are fine`)
