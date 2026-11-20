#!/usr/bin/env node
// Renders every Mermaid diagram of the built docs site in a headless browser,
// in light and in dark, and fails if any diagram does not render.
// Usage: node scripts/docs/check-diagrams.mjs [dist-dir] [base]
// Needs playwright-core (a dev dependency of docs/) and a Chromium: either
// `npx playwright-core install chromium` once, or LAUNCHWAY_DOCS_CHROME=/path/to/chrome.
import { createServer } from 'node:http'
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, extname, relative } from 'node:path'
import { createRequire } from 'node:module'

const dist = process.argv[2] || 'docs/.vitepress/dist'
const base = (process.argv[3] || process.env.DOCS_BASE || '/launchway/').replace(/\/?$/, '/')
const require = createRequire(join(process.cwd(), 'docs', 'package.json'))
const { chromium } = require('playwright-core')

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.json': 'application/json', '.woff2': 'font/woff2' }
const server = createServer((req, res) => {
  let p = decodeURIComponent(new URL(req.url, 'http://x').pathname)
  if (!p.startsWith(base)) { res.writeHead(404); return res.end() }
  p = join(dist, p.slice(base.length))
  if (existsSync(p) && statSync(p).isDirectory()) p = join(p, 'index.html')
  if (!existsSync(p) && existsSync(p + '.html')) p += '.html'
  if (!existsSync(p)) { res.writeHead(404); return res.end() }
  res.writeHead(200, { 'content-type': types[extname(p)] || 'application/octet-stream' })
  res.end(readFileSync(p))
})
await new Promise((r) => server.listen(0, '127.0.0.1', r))
const origin = `http://127.0.0.1:${server.address().port}`

function walk(dir) {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}
const pages = walk(dist)
  .filter((f) => f.endsWith('.html') && !f.includes(`${'/'}assets${'/'}`))
  .filter((f) => readFileSync(f, 'utf8').includes('mermaid-diagram'))

const browser = await chromium.launch(process.env.LAUNCHWAY_DOCS_CHROME ? { executablePath: process.env.LAUNCHWAY_DOCS_CHROME } : {})
let diagrams = 0
let problems = 0
for (const scheme of ['light', 'dark']) {
  const ctx = await browser.newContext()
  await ctx.addInitScript((s) => localStorage.setItem('vitepress-theme-appearance', s), scheme)
  const page = await ctx.newPage()
  for (const f of pages) {
    const url = origin + base + relative(dist, f).split('\\').join('/')
    await page.goto(url, { waitUntil: 'networkidle' })
    try {
      await page.waitForFunction(() => [...document.querySelectorAll('.mermaid-diagram')].every((d) => d.querySelector('svg') || d.dataset.mermaidError), null, { timeout: 20000 })
    } catch {}
    const r = await page.evaluate(() => [...document.querySelectorAll('.mermaid-diagram')].map((d) => ({
      ok: !!d.querySelector('svg') && !d.dataset.mermaidError,
      err: d.querySelector('.mermaid-error')?.textContent || (d.querySelector('svg') ? '' : 'did not render'),
      dark: document.documentElement.classList.contains('dark'),
    })))
    for (const [i, d] of r.entries()) {
      diagrams++
      if (d.dark !== (scheme === 'dark')) { console.error(`${relative(dist, f)}: the page did not switch to ${scheme}`); problems++ }
      if (!d.ok) { console.error(`${relative(dist, f)}: diagram ${i + 1} (${scheme}): ${d.err}`); problems++ }
    }
  }
  await ctx.close()
}
await browser.close()
server.close()
if (problems) { console.error(`check-diagrams: ${problems} problem(s)`); process.exit(1) }
console.log(`check-diagrams: ${diagrams / 2} diagrams on ${pages.length} pages render in light and dark`)
