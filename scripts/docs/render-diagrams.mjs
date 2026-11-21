#!/usr/bin/env node
// Draws the Mermaid diagrams of the Learn pages as SVG, once in light and once in dark, into
// docs/.vitepress/diagrams (named by a hash of the diagram's source). The product ships these
// drawings, so building it needs no browser. Run it after changing a diagram:
//   make docs-diagrams-cache          draws the missing ones, removes the unused
//   node scripts/docs/render-diagrams.mjs --force   draws all again
//   node scripts/docs/render-diagrams.mjs --check   fails if one is missing or unused
// Needs playwright-core and Mermaid (docs/node_modules) and a Chromium: LAUNCHWAY_DOCS_CHROME=/path/to/chrome.
import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { diagramKey, learnFiles, mermaidBlocks } from './learn-pages.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const docs = join(root, 'docs')
const out = join(docs, '.vitepress/diagrams')
const check = process.argv.includes('--check')
const force = process.argv.includes('--force') // draw everything again, such as after the colours change

const wanted = new Map()
for (const f of learnFiles(docs)) for (const src of mermaidBlocks(readFileSync(join(docs, f), 'utf8'))) wanted.set(diagramKey(src), { src, page: f })
mkdirSync(out, { recursive: true })
const have = new Set(readdirSync(out).filter((n) => n.endsWith('.svg')).map((n) => n.replace(/\.(light|dark)\.svg$/, '')))
const missing = [...wanted.keys()].filter((k) => force || !existsSync(join(out, k + '.light.svg')) || !existsSync(join(out, k + '.dark.svg')))
const unused = [...have].filter((k) => !wanted.has(k))

if (check) {
  for (const k of missing) console.error(`${wanted.get(k).page}: a diagram has no drawing (${k})`)
  for (const k of unused) console.error(`docs/.vitepress/diagrams/${k}.*.svg is not used by any page`)
  if (missing.length || unused.length) { console.error('run: make docs-diagrams-cache'); process.exit(1) }
  console.log(`render-diagrams: ${wanted.size} diagrams, all drawn`)
  process.exit(0)
}
for (const k of unused) for (const t of ['light', 'dark']) rmSync(join(out, `${k}.${t}.svg`), { force: true })

// The product's colours (web/src/styles/tokens.css), so a drawing sits in the page as if it were part of it.
const THEMES = {
  light: { background: '#ffffff', primaryColor: '#e8ecfa', primaryTextColor: '#171a21', primaryBorderColor: '#3e5bd1', lineColor: '#5a6270',
    secondaryColor: '#f7f8fa', secondaryTextColor: '#171a21', secondaryBorderColor: '#c9ced6', tertiaryColor: '#ffffff', tertiaryBorderColor: '#c9ced6',
    noteBkgColor: '#fbecd1', noteTextColor: '#7d4806', noteBorderColor: '#a15f08', actorBkg: '#e8ecfa', actorBorder: '#3e5bd1', actorTextColor: '#171a21',
    signalColor: '#434a57', signalTextColor: '#171a21', labelBoxBkgColor: '#f7f8fa', labelBoxBorderColor: '#c9ced6', labelTextColor: '#171a21',
    loopTextColor: '#171a21', activationBkgColor: '#e8ecfa', sequenceNumberColor: '#ffffff', edgeLabelBackground: '#ffffff', clusterBkg: '#f7f8fa', clusterBorder: '#c9ced6', titleColor: '#171a21' },
  dark: { background: '#171a20', primaryColor: '#1f2745', primaryTextColor: '#e8eaee', primaryBorderColor: '#8ba2ff', lineColor: '#8f97a5',
    secondaryColor: '#1c2027', secondaryTextColor: '#e8eaee', secondaryBorderColor: '#3a414d', tertiaryColor: '#171a20', tertiaryBorderColor: '#3a414d',
    noteBkgColor: '#35260a', noteTextColor: '#f6cf86', noteBorderColor: '#f0b64e', actorBkg: '#1f2745', actorBorder: '#8ba2ff', actorTextColor: '#e8eaee',
    signalColor: '#b0b6c1', signalTextColor: '#e8eaee', labelBoxBkgColor: '#1c2027', labelBoxBorderColor: '#3a414d', labelTextColor: '#e8eaee',
    loopTextColor: '#e8eaee', activationBkgColor: '#1f2745', sequenceNumberColor: '#0d1430', edgeLabelBackground: '#171a20', clusterBkg: '#1c2027', clusterBorder: '#3a414d', titleColor: '#e8eaee' },
}
if (missing.length === 0) { console.log(`render-diagrams: ${wanted.size} diagrams, nothing to draw`); process.exit(0) }

const require = createRequire(join(docs, 'package.json'))
const { chromium } = require('playwright-core')
const browser = await chromium.launch(process.env.LAUNCHWAY_DOCS_CHROME ? { executablePath: process.env.LAUNCHWAY_DOCS_CHROME } : {})
const page = await browser.newPage()
await page.setContent('<html><body></body></html>')
await page.addScriptTag({ path: require.resolve('mermaid/dist/mermaid.min.js') })
let n = 0
for (const k of missing) {
  for (const theme of ['light', 'dark']) {
    const svg = await page.evaluate(async ({ src, theme, vars, id }) => {
      // eslint-disable-next-line no-undef
      mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'base', themeVariables: vars,
        fontFamily: 'ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif', flowchart: { padding: 10, htmlLabels: true, nodeSpacing: 28, rankSpacing: 34 }, sequence: { useMaxWidth: true, mirrorActors: false, width: 130 } })
      // eslint-disable-next-line no-undef
      return (await mermaid.render(id, src)).svg
    }, { src: wanted.get(k).src, theme, vars: THEMES[theme], id: `d${k}${theme[0]}` })
    writeFileSync(join(out, `${k}.${theme}.svg`), svg)
  }
  n++
}
await browser.close()
console.log(`render-diagrams: drew ${n} diagrams (${wanted.size} in all, ${unused.length} unused removed)`)
