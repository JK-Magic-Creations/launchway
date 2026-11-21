#!/usr/bin/env node
// Builds the roadmap pages from the public roadmap data.
//
//   docs/roadmap/roadmap.json   the public roadmap: the vision, what is available, and the
//                               milestones with their items (title, one sentence, status, link)
//   docs/roadmap/template.html  the page shell, with /*ROADMAP_BODY*/ where the content goes
//
// Writes docs/public/roadmap.html (self-contained, readable without JavaScript) and
// docs/roadmap-list.md (the same items as Markdown). With --check, writes nothing and exits 1 if
// either file is out of date. The build refuses data that has unknown fields, a bad status, a
// link to a page that does not exist, or text on the DENY list.
// Usage: node scripts/docs/build-roadmap.mjs [--check] [repo-root]
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'

// Text that must not appear anywhere in the public roadmap: private places and details, and
// dates promised (the roadmap is ordered Now, Next, Later and promises no calendar date).
const DENY = [
  /home[- ]?lab/i, /\byour OK\b/i, /upstream\s+run/i, /\b(?:\d{1,3}\.){3}\d{1,3}\b/, /\/home\//,
  /\b(?:Q[1-4]|H[12])\s?20\d\d\b/, /\b(?:by|in|before|until)\s+(?:early |late |mid-)?(?:January|February|March|April|May|June|July|August|September|October|November|December|20\d\d)\b/i,
  /\b[A-Z]{1,3}\d{2,3}-\d{2,3}\b/, /\bS\d{2}-\d{2}\b/, /\bE\d{2}\b/, /\bR-[A-M]\d+\b/,
]

const args = process.argv.slice(2)
const check = args.includes('--check')
const root = args.find((a) => !a.startsWith('--')) || '.'
const p = (f) => join(root, f)
const fail = (m) => { console.error(`build-roadmap: ${m}`); process.exit(1) }

const raw = readFileSync(p('docs/roadmap/roadmap.json'), 'utf8')
const src = JSON.parse(raw)
for (const d of DENY) if (d.test(raw)) fail(`roadmap.json matches ${d}, which must not be published; fix the text`)
if (!/^\d{4}-\d{2}-\d{2}$/.test(src.updated || '')) fail('roadmap.json needs "updated": "YYYY-MM-DD"')

const STATUS = { available: 'Available', 'in-progress': 'In progress', planned: 'Planned' }
const WHEN = { now: 'Now', next: 'Next', later: 'Later' }
const only = (o, keys, what) => { for (const k of Object.keys(o)) if (!keys.includes(k)) fail(`${what} has the field "${k}"; only ${keys.join(', ')} are public`) }
const need = (o, keys, what) => { for (const k of keys) if (o[k] === undefined || o[k] === '') fail(`${what} has no ${k}`) }
const checkLink = (link, what) => {
  if (!link) return
  if (/^https:\/\//.test(link)) return
  const [page] = link.split('#')
  if (!existsSync(p(`docs/${page}.md`))) fail(`${what} links to ${link}, but docs/${page}.md does not exist`)
}

if (!Array.isArray(src.vision) || src.vision.length !== 3) fail('"vision" is three sentences')
const titles = new Set()
const note = (t) => { if (titles.has(t)) fail(`the title "${t}" is used twice`); titles.add(t) }
for (const a of src.available) {
  only(a, ['theme', 'title', 'value', 'link'], `available item ${a.title}`)
  need(a, ['theme', 'title', 'value'], `available item ${a.title}`)
  checkLink(a.link, a.title)
  note(a.title)
}
for (const m of src.milestones) {
  only(m, ['id', 'number', 'when', 'name', 'short', 'promise', 'groups'], `milestone ${m.id}`)
  need(m, ['id', 'when', 'name', 'short', 'promise', 'groups'], `milestone ${m.id}`)
  if (!WHEN[m.when]) fail(`milestone ${m.id}: when ${m.when} is not one of ${Object.keys(WHEN).join(', ')}`)
  for (const g of m.groups) {
    need(g, ['name', 'items'], `a group of ${m.id}`)
    for (const i of g.items) {
      only(i, ['status', 'title', 'value', 'link'], `item ${i.title}`)
      need(i, ['status', 'title', 'value'], `item ${i.title}`)
      if (!STATUS[i.status]) fail(`item ${i.title}: status ${i.status} is not one of ${Object.keys(STATUS).join(', ')}`)
      checkLink(i.link, i.title)
      note(i.title)
    }
  }
}

const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]))
const href = (link) => (/^https:/.test(link) ? link : `./${link.replace(/#.*$/, '')}.html${link.includes('#') ? '#' + link.split('#')[1] : ''}`)
const mdHref = (link) => (/^https:/.test(link) ? link : `${link.replace(/#.*$/, '')}.md${link.includes('#') ? '#' + link.split('#')[1] : ''}`)
const icon = { available: '✓', 'in-progress': '◐', planned: '○' }
const chip = (s) => `<span class="chip st-${s}"><span aria-hidden="true">${icon[s]}</span> ${STATUS[s]}</span>`
const anchor = (m) => (m.id === 'beyond' ? 'beyond' : `milestone-${m.number}`)
const itemsOf = (m) => m.groups.flatMap((g) => g.items)
const count = (list) => ({
  available: list.filter((i) => i.status === 'available').length,
  'in-progress': list.filter((i) => i.status === 'in-progress').length,
  planned: list.filter((i) => i.status === 'planned').length,
})
const bar = (c) => {
  const total = c.available + c['in-progress'] + c.planned
  const seg = (s) => (c[s] ? `<span class="seg st-${s}" style="flex:${c[s]}"></span>` : '')
  return `<span class="bar" role="img" aria-label="${c.available} available, ${c['in-progress']} in progress, ${c.planned} planned of ${total}">${seg('available')}${seg('in-progress')}${seg('planned')}</span>`
}
const more = (i) => (i.link ? ` <a class="more" href="${esc(href(i.link))}">Read more<span class="visually-hidden"> about ${esc(i.title)}</span> →</a>` : '')

const allMilestoneItems = src.milestones.flatMap(itemsOf)
const totals = count([...src.available.map(() => ({ status: 'available' })), ...allMilestoneItems.filter((i) => i.status !== 'available')])

const journey = `<ol class="journey" aria-label="The milestones, in order">
  <li class="st-done"><a href="#available"><span class="node node-a" aria-hidden="true">✓</span><span class="jname">Available today</span><span class="jwhen">${src.available.length} things</span></a></li>
${src.milestones.map((m) => {
  const c = count(itemsOf(m))
  return `  <li class="when-${m.when}"><a href="#${anchor(m)}"><span class="node" aria-hidden="true">${m.number || '+'}</span><span class="jname">${esc(m.short)}</span><span class="jwhen">${WHEN[m.when]}</span>${bar(c)}</a></li>`
}).join('\n')}
</ol>`

const availableByTheme = [...new Set(src.available.map((a) => a.theme))]
const availableHtml = `<section class="group st-section" id="available" aria-labelledby="h-available">
  <div class="sec-head"><h2 id="h-available">Available today</h2><span class="sec-note">On the main branch, tested and documented. ${src.available.length} things.</span></div>
${availableByTheme.map((t) => `  <h3 class="theme-h">${esc(t)}</h3>
  <ul class="grid">
${src.available.filter((a) => a.theme === t).map((a) => `    <li class="card st-available" data-status="available"><h4>${a.link ? `<a href="${esc(href(a.link))}">${esc(a.title)}</a>` : esc(a.title)}</h4><p>${esc(a.value)}</p></li>`).join('\n')}
  </ul>`).join('\n')}
</section>`

const milestoneHtml = (m) => {
  const list = itemsOf(m)
  const c = count(list)
  const head = m.number ? `<span class="mnum" aria-hidden="true">${m.number}</span>` : `<span class="mnum mnum-plus" aria-hidden="true">+</span>`
  return `<section class="milestone when-${m.when}" id="${anchor(m)}" aria-labelledby="h-${anchor(m)}">
  <header class="m-head">
    ${head}
    <div class="m-title">
      <p class="when"><span class="when-chip when-${m.when}">${WHEN[m.when]}</span>${m.number ? ` Milestone ${m.number}` : ' After the milestones'}</p>
      <h2 id="h-${anchor(m)}">${esc(m.name)}</h2>
      <p class="promise">${esc(m.promise)}</p>
    </div>
    <div class="m-progress">${bar(c)}<span class="m-count">${c.available} available · ${c['in-progress']} in progress · ${c.planned} planned</span></div>
  </header>
${m.groups.map((g) => `  <div class="subgroup">
    <h3 class="theme-h">${esc(g.name)}</h3>
    <ul class="grid">
${g.items.map((i) => `      <li class="card st-${i.status}" data-status="${i.status}">${chip(i.status)}<h4>${esc(i.title)}</h4><p>${esc(i.value)}${more(i)}</p></li>`).join('\n')}
    </ul>
  </div>`).join('\n')}
</section>`
}

const body = `<main id="content">
  <p class="eyebrow">Roadmap</p>
  <h1>Where Launchway is going</h1>
  <ol class="vision" aria-label="The vision">
${src.vision.map((v) => `    <li>${esc(v)}</li>`).join('\n')}
  </ol>
  <p class="asof">As of ${src.updated}. Items are ordered by value, in milestones marked Now, Next and Later. There are no dates: plans change, and an item moves to Available only when it is merged, tested and documented.</p>

  <div class="stats" aria-label="Where things stand">
    <div class="stat st-available"><b>${totals.available}</b><span>${chip('available')}</span></div>
    <div class="stat st-in-progress"><b>${totals['in-progress']}</b><span>${chip('in-progress')}</span></div>
    <div class="stat st-planned"><b>${totals.planned}</b><span>${chip('planned')}</span></div>
  </div>

  <nav aria-label="Milestones">
${journey}
  </nav>

  <div class="controls" id="controls" hidden>
    <div class="chips" role="group" aria-label="Filter by status">
      <button type="button" class="fchip" data-f="" aria-pressed="true">All</button>
      <button type="button" class="fchip" data-f="available" aria-pressed="false">Available</button>
      <button type="button" class="fchip" data-f="in-progress" aria-pressed="false">In progress</button>
      <button type="button" class="fchip" data-f="planned" aria-pressed="false">Planned</button>
    </div>
    <label class="visually-hidden" for="q">Search the roadmap</label>
    <input id="q" class="search" type="search" placeholder="Search the roadmap" autocomplete="off">
    <p class="count" id="count" role="status" aria-live="polite"></p>
  </div>

${availableHtml}

${src.milestones.map(milestoneHtml).join('\n\n')}

  <section class="principles" aria-labelledby="h-throughout">
    <div class="sec-head"><h2 id="h-throughout">Throughout, not a milestone</h2><span class="sec-note">The standard every item is held to.</span></div>
    <ul class="grid principles-grid">
${src.principles.map((x) => `      <li class="card"><h4>${esc(x.title)}</h4><p>${esc(x.value)}</p></li>`).join('\n')}
    </ul>
  </section>

  <footer>
    <p>Launchway is not certified against any compliance standard; compliance items provide controls and evidence. Nothing here is a commitment to a date. The same items, as a table, are in <a href="./roadmap-list.html">the roadmap as a list</a>.</p>
  </footer>
</main>`

const tpl = readFileSync(p('docs/roadmap/template.html'), 'utf8')
if (!tpl.includes('/*ROADMAP_BODY*/')) fail('template.html has no /*ROADMAP_BODY*/')
const html = tpl.replace('/*ROADMAP_BODY*/', () => body)

const cell = (s) => String(s).replace(/\|/g, '\\|')
const row = (i) => `| ${cell(i.title)} | ${STATUS[i.status]} | ${cell(i.value)}${i.link ? ` ([more](${mdHref(i.link)}))` : ''} |`
const md = `# Roadmap, as a list

What Launchway does today and what is planned, in milestones marked Now, Next and Later. The same
items are drawn on the [roadmap](./public/roadmap.html). As of ${src.updated}.

<!-- Generated by scripts/docs/build-roadmap.mjs from docs/roadmap/roadmap.json. Do not edit by hand. -->

${src.vision.join(' ')}

Order is by value, not a schedule, and plans change. An item is **available** only when it is on the
main branch, tested and documented. Launchway is not certified against any compliance standard;
compliance items provide controls and evidence.

## Available today

| Item | What it gives you |
|---|---|
${src.available.map((a) => `| ${cell(a.title)} | ${cell(a.value)}${a.link ? ` ([more](${mdHref(a.link)}))` : ''} |`).join('\n')}

${src.milestones.map((m) => `## ${m.number ? `Milestone ${m.number}: ` : ''}${m.name} (${WHEN[m.when]})

${m.promise}

${m.groups.map((g) => `### ${g.name}

| Item | Status | What it gives you |
|---|---|---|
${g.items.map(row).join('\n')}
`).join('\n')}`).join('\n')}
## Throughout, not a milestone

${src.principles.map((x) => `- **${x.title}.** ${x.value}`).join('\n')}
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
console.log(`build-roadmap: ${totals.available} available, ${totals['in-progress']} in progress, ${totals.planned} planned across ${src.milestones.length} milestones${check ? ' are up to date' : ' written'}`)
