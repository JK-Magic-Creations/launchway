#!/usr/bin/env node
// Builds the roadmap pages from the public roadmap data.
//
//   docs/roadmap/roadmap.json   the public roadmap: "available" pairs and ranked "items", each with
//                               only rank, status, theme, title and value
//   docs/roadmap/template.html  the page, with /*ROADMAP_DATA*/ where the data goes
//
// The maintainers' own roadmap has more in it (notes on progress, work on their own
// infrastructure). `--sync=FILE` reads that file and writes docs/roadmap/roadmap.json from it, keeping
// only the public fields and applying the rules below, so a private note can never be copied
// into the site. The build refuses a roadmap.json that has other fields or that mentions
// anything on the DENY list.
//
// Writes docs/public/roadmap.html (self-contained: the data is inlined) and
// docs/roadmap-list.md (the same items as Markdown). With --check, writes
// nothing and exits 1 if either file is out of date.
// Usage: node scripts/docs/build-roadmap.mjs [--check] [--sync=FILE] [repo-root]
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'

// Work on the maintainers' own infrastructure, not the product: left out of the public roadmap.
const NOT_PUBLIC = ['Move every Gitea Actions workflow to Launchway']
// Items that this repository ships: shown as available rather than ranked.
const SHIPPED = ['Public documentation site and roadmap']
// Wording corrections: [pattern, replacement], applied to every value.
const REWORD = [[/, webhook, upstream\s+run\.$/, ' and webhook.']]
// Text that must not appear anywhere in the public roadmap.
const DENY = [/home[- ]?lab/i, /\byour OK\b/i, /upstream\s+run/i, /\b(?:\d{1,3}\.){3}\d{1,3}\b/, /\/home\//]

const args = process.argv.slice(2)
const check = args.includes('--check')
const syncFrom = (args.find((a) => a.startsWith('--sync=')) || '').slice('--sync='.length)
const root = args.find((a) => !a.startsWith('--')) || '.'
const p = (f) => join(root, f)
const fail = (m) => { console.error(`build-roadmap: ${m}`); process.exit(1) }

if (args.some((a) => a === '--sync' || a === '--sync=')) fail('--sync needs a file: --sync=path/to/roadmap.json')
if (syncFrom) {
  if (!existsSync(syncFrom)) fail(`${syncFrom} does not exist`)
  const full = JSON.parse(readFileSync(syncFrom, 'utf8'))
  for (const t of [...NOT_PUBLIC, ...SHIPPED]) {
    if (!full.items.some((i) => i.title === t) && !full.available.some(([a]) => a === t)) fail(`the rules name "${t}", which is not in ${syncFrom}; update NOT_PUBLIC or SHIPPED`)
  }
  const reword = (s) => REWORD.reduce((v, [from, to]) => v.replace(from, to), s)
  const kept = full.items.filter((i) => !NOT_PUBLIC.includes(i.title)).sort((a, b) => a.rank - b.rank)
  const shipped = kept.filter((i) => SHIPPED.includes(i.title))
  const ranked = kept.filter((i) => !SHIPPED.includes(i.title))
  const out = {
    updated: full.updated,
    available: [
      ...full.available.map(([title, value]) => [title, reword(value)]),
      ...shipped.map((i) => [i.title, reword(i.value)]),
    ],
    items: ranked.map((i, n) => ({ rank: n + 1, status: i.status, theme: i.theme, title: i.title, value: reword(i.value) })),
  }
  writeFileSync(p('docs/roadmap/roadmap.json'), JSON.stringify(out, null, 2) + '\n')
}

const raw = readFileSync(p('docs/roadmap/roadmap.json'), 'utf8')
const src = JSON.parse(raw)

if (!/^\d{4}-\d{2}-\d{2}$/.test(src.updated || '')) fail('roadmap.json needs "updated": "YYYY-MM-DD"')
for (const d of DENY) if (d.test(raw)) fail(`roadmap.json matches ${d}, which must not be published; fix the source or the rules in this script and run make docs-roadmap-sync`)
const statuses = ['in-progress', 'planned']
const fields = ['rank', 'status', 'theme', 'title', 'value']
const seen = new Set()
for (const i of src.items) {
  for (const k of fields) if (i[k] === undefined || i[k] === '') fail(`item ${JSON.stringify(i.title)} has no ${k}`)
  for (const k of Object.keys(i)) if (!fields.includes(k)) fail(`item ${JSON.stringify(i.title)} has the field "${k}"; only ${fields.join(', ')} are public`)
  if (!statuses.includes(i.status)) fail(`item ${i.title}: status ${i.status} is not one of ${statuses.join(', ')}`)
  if (seen.has(i.rank)) fail(`rank ${i.rank} is used twice`)
  seen.add(i.rank)
}
if (![...seen].sort((a, b) => a - b).every((r, n) => r === n + 1)) fail('ranks must run 1, 2, 3 ... with no gaps')
for (const a of src.available) if (!Array.isArray(a) || a.length !== 2) fail('each "available" entry is [title, value]')

const items = [
  ...src.available.map(([title, value]) => ({ status: 'available', title, value })),
  ...src.items,
]

const data = { updated: src.updated, items }
const json = JSON.stringify(data).replace(/</g, '\\u003c')
const tpl = readFileSync(p('docs/roadmap/template.html'), 'utf8')
if (!tpl.includes('/*ROADMAP_DATA*/')) fail('template.html has no /*ROADMAP_DATA*/')
const html = tpl.replace('/*ROADMAP_DATA*/', json)

const cell = (s) => String(s).replace(/\|/g, '\\|')
const byRank = (a, b) => a.rank - b.rank
const sec = (status) => items.filter((i) => i.status === status)
let md = `# Roadmap, as a list

What Launchway does today, what is being built now, and what is planned, in order of value. The
same items are drawn on the [interactive roadmap](./public/roadmap.html). As of ${src.updated}.

<!-- Generated by scripts/docs/build-roadmap.mjs from docs/roadmap/roadmap.json. Do not edit by hand. -->

Only items marked **available** are on the main branch. Order is by value, not a schedule, and
plans change. Launchway is not certified against any compliance standard; compliance items
provide controls and evidence.

## Available today

| Item | What it gives you |
|---|---|
${sec('available').map((i) => `| ${cell(i.title)} | ${cell(i.value)} |`).join('\n')}

## Being built now

| Rank | Item | Theme | What it gives you |
|---|---|---|---|
${sec('in-progress').sort(byRank).map((i) => `| ${i.rank} | ${cell(i.title)} | ${cell(i.theme)} | ${cell(i.value)} |`).join('\n')}

## Planned

| Rank | Item | Theme | What it gives you |
|---|---|---|---|
${sec('planned').sort(byRank).map((i) => `| ${i.rank} | ${cell(i.title)} | ${cell(i.theme)} | ${cell(i.value)} |`).join('\n')}
`

const outputs = [['docs/public/roadmap.html', html], ['docs/roadmap-list.md', md]]
let stale = 0
for (const [f, content] of outputs) {
  const cur = existsSync(p(f)) ? readFileSync(p(f), 'utf8') : ''
  if (cur === content) continue
  if (check) { console.error(`build-roadmap: ${f} is out of date; run make docs-roadmap`); stale++ }
  else writeFileSync(p(f), content)
}
if (stale) process.exit(1)
console.log(`build-roadmap: ${items.length} items (${sec('available').length} available, ${sec('in-progress').length} in progress, ${sec('planned').length} planned)${check ? ' are up to date' : ' written'}`)
