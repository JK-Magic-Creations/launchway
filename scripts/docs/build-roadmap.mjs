#!/usr/bin/env node
// Builds the roadmap pages from the public roadmap data.
//
//   docs/roadmap/roadmap.json   the public roadmap: the vision, what is available, and the
//                               milestones with their items (title, one sentence, status, link)
//   docs/roadmap/template.html  the page shell, with /*ROADMAP_BODY*/ where the content goes
//
// Writes docs/public/roadmap.html (self-contained, readable without JavaScript),
// docs/roadmap-list.md (the same items as Markdown) and one detail page per milestone,
// docs/roadmap/milestone-N.md and docs/roadmap/beyond.md. With --check, writes nothing and exits 1 if
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
const need = (o, keys, what) => { for (const k of keys) if (o[k] === undefined || o[k] === '' || (Array.isArray(o[k]) && !o[k].length && k !== 'buildsOn')) fail(`${what} has no ${k}`) }
const checkLink = (link, what) => {
  if (!link) return
  if (/^https:\/\//.test(link)) return
  const [page] = link.split('#')
  if (!existsSync(p(`docs/${page}.md`))) fail(`${what} links to ${link}, but docs/${page}.md does not exist`)
}
const checkArea = (a, what) => { if (a && !existsSync(p(`docs/roadmap/${a}.md`))) fail(`${what} names the capability page "${a}", but docs/roadmap/${a}.md does not exist`) }

if (!Array.isArray(src.vision) || src.vision.length !== 3) fail('"vision" is three sentences')
const titles = new Set()
const note = (t) => { if (titles.has(t)) fail(`the title "${t}" is used twice`); titles.add(t) }
for (const a of src.available) {
  only(a, ['theme', 'title', 'value', 'link'], `available item ${a.title}`)
  need(a, ['theme', 'title', 'value'], `available item ${a.title}`)
  checkLink(a.link, a.title)
  note(a.title)
}
for (const [k, what] of [['strong', 'standsToday'], ['missing', 'standsToday']]) if (!Array.isArray(src[what]?.[k]) || !src[what][k].length) fail(`"${what}" needs a list "${k}"`)
for (const q of src.buildQueue || []) { only(q, ['title', 'value', 'area'], `queue item ${q.title}`); need(q, ['title', 'value'], `queue item ${q.title}`); checkArea(q.area, q.title) }
const ids = new Set(src.milestones.map((m) => m.id))
for (const m of src.milestones) {
  only(m, ['id', 'number', 'when', 'name', 'short', 'promise', 'audience', 'what', 'scenarios', 'diagram', 'diagramNote', 'done', 'notIncluded', 'buildsOn', 'deepDives', 'groups'], `milestone ${m.id}`)
  need(m, ['id', 'when', 'name', 'short', 'promise', 'audience', 'what', 'scenarios', 'diagram', 'diagramNote', 'done', 'notIncluded', 'buildsOn', 'groups'], `milestone ${m.id}`)
  if (!WHEN[m.when]) fail(`milestone ${m.id}: when ${m.when} is not one of ${Object.keys(WHEN).join(', ')}`)
  for (const b of m.buildsOn) if (!ids.has(b) || b === m.id) fail(`milestone ${m.id} builds on "${b}", which is not another milestone`)
  for (const d of m.deepDives || []) { only(d, ['title', 'area'], `a deep dive of ${m.id}`); checkArea(d.area, `a deep dive of ${m.id}`) }
  if (!/^(flowchart|sequenceDiagram|graph|stateDiagram)/.test(m.diagram)) fail(`milestone ${m.id}: the diagram must be Mermaid`)
  for (const g of m.groups) {
    only(g, ['name', 'blurb', 'items'], `a group of ${m.id}`)
    need(g, ['name', 'blurb', 'items'], `a group of ${m.id}`)
    for (const i of g.items) {
      only(i, ['status', 'title', 'value', 'detail', 'needs', 'area', 'link'], `item ${i.title}`)
      need(i, ['status', 'title', 'value', 'detail'], `item ${i.title}`)
      if (!STATUS[i.status]) fail(`item ${i.title}: status ${i.status} is not one of ${Object.keys(STATUS).join(', ')}`)
      checkLink(i.link, i.title)
      checkArea(i.area, i.title)
      note(i.title)
    }
  }
}

const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]))
const href = (link) => (/^https:/.test(link) ? link : `./${link.replace(/#.*$/, '')}.html${link.includes('#') ? '#' + link.split('#')[1] : ''}`)
const mdHref = (link, from = '') => (/^https:/.test(link) ? link : `${from}${link.replace(/#.*$/, '')}.md${link.includes('#') ? '#' + link.split('#')[1] : ''}`)
const icon = { available: '✓', 'in-progress': '◐', planned: '○' }
const chip = (s) => `<span class="chip st-${s}"><span aria-hidden="true">${icon[s]}</span> ${STATUS[s]}</span>`
const anchor = (m) => (m.id === 'beyond' ? 'beyond' : `milestone-${m.number}`)
const pageName = (m) => (m.id === 'beyond' ? 'beyond' : `milestone-${m.number}`)
const slug = (t) => t.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
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
const counts = (c) => `${c.available} available · ${c['in-progress']} in progress · ${c.planned} planned`

const allMilestoneItems = src.milestones.flatMap(itemsOf)
const totals = count([...src.available.map(() => ({ status: 'available' })), ...allMilestoneItems])
const capCount = src.available.length + allMilestoneItems.length
const byId = Object.fromEntries(src.milestones.map((m) => [m.id, m]))

const AREAS = [
  ['build-order', 'How the build is ordered', 'The phases, from what exists today to the longer plans, in one picture.'],
  ['git-hosting', 'Built-in Git hosting and change review', 'How a push travels, how it is kept safe, and how a change is reviewed and merged.'],
  ['pipelines', 'Pipelines and flow control', 'How a run is planned, ordered and controlled, and how custom and generated steps fit.'],
  ['registry', 'The built-in registry', 'Images, charts and other artifacts from build to running, with policy at both ends.'],
  ['delivery', 'Delivery: applications, rollouts and hosts', 'The applications hierarchy, rollouts, machine images and image-based hosts, and how to debug a deployment.'],
  ['traffic', 'Live traffic, protection and reliability', 'The traffic map, limits and attack signals, reliability targets and toggles in one view.'],
  ['software-graph', 'The software graph and automated patching', 'What is deployed where, and the loop that patches it safely.'],
  ['ai', 'AI as an optional helper', 'The gateway, local models, spending limits and the protected path.'],
  ['hosted-service', 'The hosted service and sandboxed runners', 'Sign-up, three ways to run with easy moves, size limits, never public file storage, opening in stages, isolation layers and the public demo.'],
  ['guided-interface', 'The guided interface', 'Follow a change on its launch line, and the overview, focus and detail pattern.'],
  ['zero-downtime', 'Zero-downtime delivery', 'One model for every deployment: a plan, a shifter, proofs, controls and the last stable state, with the watch afterward.'],
  ['network', 'Network and traffic', 'The path of a request from the edge to a workload, where findings come from, DNS and service meshes.'],
  ['databases', 'Databases', 'Keeping Launchway\u2019s own database up, and delivering changes to yours.'],
  ['kubernetes-vms', 'Kubernetes and virtual machines', 'Clusters, workloads, autoscaling, serverless and virtual machines, starting from the change.'],
  ['security-trust', 'Security and trust', 'Encryption at rest, the built-in vault, scans as gates and an honest path to certification.'],
  ['repository-standards', 'Standards and scanning for every repository', 'One profile per repository, checks you switch on, advise or block, findings in one list and fixes as pull requests.'],
  ['build-tools-caches', 'Build tools and caches', 'A cache that makes a build faster and never different, recipes for the popular tools and mobile builds.'],
  ['dependency-order', 'Dependency order and releases', 'Libraries before what uses them, release units that go together, publish-only pipelines and branching patterns.'],
  ['platform-services', 'Platform services', 'Object storage, Terraform and OpenTofu state, a managed cache and feature toggles, each built in and swappable.'],
  ['config-as-code', 'Configuration as code', 'Plans, inventories, per-host results and drift for Terraform, OpenTofu and Ansible.'],
  ['developer-experience', 'Developer experience and the golden path', 'A small workload description, landings per environment, change records and editor and assistant tools.'],
  ['installing', 'Installing Launchway', 'One guided installer for a laptop, a server or Kubernetes, and a first sign-in that walks you through the rest.'],
  ['inputs-and-forms', 'Inputs and forms', 'Run forms, pickers and lookups, so values are picked and checked, not typed.'],
  ['triggers', 'Triggers', 'Everything that starts a Sortie, declared beside the pipeline and explained on the record.'],
  ['importing', 'Bringing work in', 'Pipelines and more brought in from other systems, with nothing dropped silently.'],
  ['policy-and-governance', 'Standing orders, Clearance and evidence', 'The fixed points where rules are judged, Clearances and Holds, and the evidence an auditor asks for.'],
  ['speed', 'Speed and continuous deployment', 'Where the time goes, building only what a change affects, speed budgets, and continuous deployment where the rules allow.'],
  ['branching', 'Branching patterns', 'Recognizing the branching pattern a repository uses, branch roles pipelines read, and moving between patterns.'],
  ['quality-and-reports', 'Quality and reports', 'The Debrief, coverage of changed lines, quality gates and one report for every Sortie and release.'],
]

// The big picture: one change travelling from idea to running software, with what surrounds it.
const stations = [
  ['Idea', ['work items', 'decisions'], 'Milestone 1', 'milestone-1'],
  ['Code', ['hosting, review', 'search'], 'Milestone 1', 'milestone-1'],
  ['Pipelines', ['build, test', 'scan'], 'Milestones 1, 2', 'milestone-2'],
  ['Packages', ['registry', 'promotion'], 'Milestone 4', 'milestone-4'],
  ['Deploy', ['apps, hosts', 'clouds'], 'Milestone 3', 'milestone-3'],
  ['Runtime', ['settings, toggles', 'traffic, protection'], 'Milestone 3', 'milestone-3'],
  ['Insight', ['graph, metrics', 'patching'], 'Milestones 3, 5', 'milestone-5'],
]
const bigPicture = () => {
  const w = 128, gap = 14, x0 = 20, y0 = 46, h = 104
  const x = (n) => x0 + n * (w + gap)
  const st = stations.map((s, n) => `    <a href="./roadmap/${s[3]}.html"><g class="bp-station"><rect x="${x(n)}" y="${y0}" width="${w}" height="${h}" rx="12"/><text x="${x(n) + w / 2}" y="${y0 + 28}" class="bp-t">${esc(s[0])}</text><text x="${x(n) + w / 2}" y="${y0 + 50}" class="bp-s">${esc(s[1][0])}</text><text x="${x(n) + w / 2}" y="${y0 + 67}" class="bp-s">${esc(s[1][1])}</text><text x="${x(n) + w / 2}" y="${y0 + 88}" class="bp-m">${esc(s[2])}</text></g></a>`).join('\n')
  const arrows = stations.slice(1).map((_, n) => `    <path d="M${x(n) + w + 1} ${y0 + h / 2} H${x(n + 1) - 2}" class="bp-arrow" marker-end="url(#bp-head)"/>`).join('\n')
  const last = x(6) + w / 2, first = x(0) + w / 2
  return `<svg class="diagram" viewBox="0 0 1020 336" role="img" aria-labelledby="bp-title bp-desc" xmlns="http://www.w3.org/2000/svg">
  <title id="bp-title">The big picture</title>
  <desc id="bp-desc">One change travels from an idea, through code and review, pipelines, packages and deployment, to running software, and what is learned comes back as the next change. Policy, permissions and audit apply at every step, and everything rests on one database and Git.</desc>
  <defs><marker id="bp-head" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 L10 5 L0 10 z" class="bp-headfill"/></marker></defs>
  <text x="20" y="28" class="bp-label">One change, travelling from idea to running software</text>
${st}
${arrows}
  <path d="M${last} ${y0 + h + 2} V${y0 + h + 34} H${first} V${y0 + h + 4}" class="bp-loop" marker-end="url(#bp-head)"/>
  <text x="510" y="${y0 + h + 54}" class="bp-label bp-mid">What is learned and patched comes back as the next change</text>
  <rect x="20" y="230" width="980" height="38" rx="10" class="bp-band"/>
  <text x="510" y="254" class="bp-band-t bp-mid">Policy, permissions and audit at every station · AI helps where you turn it on (Milestone 6)</text>
  <rect x="20" y="280" width="980" height="38" rx="10" class="bp-band bp-base"/>
  <text x="510" y="304" class="bp-band-t bp-mid">One database and Git · your servers, your Kubernetes or the hosted service (Milestone 7)</text>
</svg>`
}

// The dependency diagram, drawn from each milestone's "buildsOn".
const dependencyDiagram = () => {
  const layer = {}
  const depth = (m) => (layer[m.id] ??= m.buildsOn.length ? 1 + Math.max(...m.buildsOn.map((b) => depth(byId[b]))) : 0)
  src.milestones.forEach(depth)
  const cols = Math.max(...Object.values(layer)) + 1
  const w = 210, h = 54, gx = 90, gy = 16, pad = 14
  const rows = Array.from({ length: cols }, () => [])
  src.milestones.forEach((m) => rows[layer[m.id]].push(m))
  const tall = Math.max(...rows.map((r) => r.length))
  const H = tall * (h + gy) - gy + pad * 2
  const pos = {}
  rows.forEach((r, c) => r.forEach((m, n) => { pos[m.id] = { x: pad + c * (w + gx), y: pad + (H - pad * 2 - (r.length * (h + gy) - gy)) / 2 + n * (h + gy) } }))
  const W = pad * 2 + cols * w + (cols - 1) * gx
  const edges = src.milestones.flatMap((m) => m.buildsOn.map((b) => { const a = pos[b], z = pos[m.id]; const x1 = a.x + w, y1 = a.y + h / 2, x2 = z.x - 2, y2 = z.y + h / 2, mx = (x1 + x2) / 2; return `  <path d="M${x1} ${y1} C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}" class="bp-arrow" marker-end="url(#dep-head)"/>` })).join('\n')
  const nodes = src.milestones.map((m) => { const q = pos[m.id]; return `  <a href="./roadmap/${pageName(m)}.html"><g class="dep-node when-${m.when}"><rect x="${q.x}" y="${q.y}" width="${w}" height="${h}" rx="11"/><text x="${q.x + 14}" y="${q.y + 23}" class="bp-t dep-l">${m.number ? 'Milestone ' + m.number : 'After the milestones'}</text><text x="${q.x + 14}" y="${q.y + 42}" class="dep-n">${esc(m.short)} · ${WHEN[m.when]}</text></g></a>` }).join('\n')
  return `<svg class="diagram" viewBox="0 0 ${W} ${H}" role="img" aria-labelledby="dep-title" xmlns="http://www.w3.org/2000/svg">
  <title id="dep-title">How the milestones build on each other</title>
  <defs><marker id="dep-head" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 L10 5 L0 10 z" class="bp-headfill"/></marker></defs>
${edges}
${nodes}
</svg>`
}
const depText = src.milestones.map((m) => `${m.number ? 'Milestone ' + m.number : 'Beyond'} ${m.buildsOn.length ? 'builds on ' + m.buildsOn.map((b) => 'milestone ' + byId[b].number).join(' and ') : 'comes first'}`).join('; ')

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
      <p class="m-detail"><a class="btn" href="./roadmap/${pageName(m)}.html">Open the detail page<span class="visually-hidden"> for ${esc(m.short)}</span> →</a></p>
    </div>
    <div class="m-progress">${bar(c)}<span class="m-count">${counts(c)}</span><span class="m-count">${list.length} capabilities in ${m.groups.length} groups</span></div>
  </header>
${m.groups.map((g) => {
  const gc = count(g.items)
  return `  <details class="subgroup">
    <summary><span class="g-name">${esc(g.name)}</span><span class="g-count">${g.items.length} · ${bar(gc)}</span></summary>
    <p class="g-blurb">${esc(g.blurb)}</p>
    <ul class="grid">
${g.items.map((i) => `      <li class="card st-${i.status}" data-status="${i.status}">${chip(i.status)}<h4><a href="./roadmap/${pageName(m)}.html#${slug(i.title)}">${esc(i.title)}</a></h4><p>${esc(i.value)}${more(i)}</p></li>`).join('\n')}
    </ul>
  </details>`
}).join('\n')}
</section>`
}

const wc = src.whatChanged
const whatChangedHtml = wc ? `<section class="what-changed" aria-labelledby="h-changed">
    <h2 id="h-changed">What changed in the roadmap</h2>
    <p class="wc-date">${esc(wc.date)}</p>
    <ul>
${wc.items.map((x) => `      <li>${esc(x)}</li>`).join('\n')}
    </ul>
  </section>` : ''

const st = src.standsToday
const standsHtml = st ? `<section class="what-changed" aria-labelledby="h-stands">
    <h2 id="h-stands">Where Launchway stands today</h2>
    <p class="wc-date">What is already strong</p>
    <ul>
${st.strong.map((x) => `      <li>${esc(x)}</li>`).join('\n')}
    </ul>
    <p class="wc-date">What is not there yet</p>
    <ul>
${st.missing.map((x) => `      <li>${esc(x)}</li>`).join('\n')}
    </ul>
    <p class="wc-date">${esc(st.note || '')}</p>
  </section>` : ''
const wf = src.whoFor || []
const whoForHtml = wf.length ? `<section class="what-changed" aria-labelledby="h-who">
    <h2 id="h-who">Who Launchway is for</h2>
    <p class="wc-date">${esc(src.whoForNote || '')}</p>
    <ul>
${wf.map((x) => `      <li><b>${esc(x.who)}.</b> ${esc(x.wants)} <i>${esc(x.today)}</i></li>`).join('\n')}
    </ul>
  </section>` : ''
const queueHtml = (src.buildQueue || []).length ? `<section class="what-changed" aria-labelledby="h-queue">
    <h2 id="h-queue">The order we are building in</h2>
    <p class="wc-date">After what is already in flight, in this order.</p>
    <ol>
${src.buildQueue.map((q) => `      <li><b>${q.area ? `<a href="./roadmap/${q.area}.html">${esc(q.title)}</a>` : esc(q.title)}.</b> ${esc(q.value)}</li>`).join('\n')}
    </ol>
  </section>` : ''

const body = `<main id="content">
  <p class="eyebrow">Roadmap</p>
  <h1>Where Launchway is going</h1>
  <ol class="vision" aria-label="The vision">
${src.vision.map((v) => `    <li>${esc(v)}</li>`).join('\n')}
  </ol>
  <p class="asof">As of ${src.updated}. Items are ordered by value, in milestones marked Now, Next and Later. There are no dates: plans change, and an item moves to Available only when it is merged, tested and documented. Read it at three levels: this page, a detail page for each milestone, and a page for each large area.</p>
  <p class="asof">How it is designed, chapter by chapter, with diagrams and screen designs: <a href="./plan/index.html">the platform plan</a>. Planned work there is labelled as planned.</p>

${whatChangedHtml}

${standsHtml}

${whoForHtml}

${queueHtml}

  <div class="stats" aria-label="Where things stand">
    <div class="stat st-available"><b>${totals.available}</b><span>${chip('available')}</span></div>
    <div class="stat st-in-progress"><b>${totals['in-progress']}</b><span>${chip('in-progress')}</span></div>
    <div class="stat st-planned"><b>${totals.planned}</b><span>${chip('planned')}</span></div>
    <div class="stat"><b>${capCount}</b><span class="stat-l">capabilities in all</span></div>
  </div>

  <nav aria-label="Milestones">
${journey}
  </nav>

  <section class="picture" aria-labelledby="h-picture">
    <div class="sec-head"><h2 id="h-picture">The big picture</h2><span class="sec-note">Choose a station to open its milestone.</span></div>
    <div class="diagram-wrap">
${bigPicture()}
    </div>
    <p class="diagram-note">Code, pipelines, packages, deployments, runtime settings and insight fit together around one change. Each station is a milestone below.</p>
  </section>

  <section class="picture" aria-labelledby="h-deps">
    <div class="sec-head"><h2 id="h-deps">How the milestones build on each other</h2><span class="sec-note">An arrow means the later milestone uses the earlier one.</span></div>
    <div class="diagram-wrap">
${dependencyDiagram()}
    </div>
    <p class="diagram-note">${esc(depText)}.</p>
  </section>

  <section class="areas" aria-labelledby="h-areas">
    <div class="sec-head"><h2 id="h-areas">The large areas, in depth</h2><span class="sec-note">Each has diagrams and explains how it works.</span></div>
    <ul class="grid areas-grid">
${AREAS.map((a) => `      <li class="card"><h4><a href="./roadmap/${a[0]}.html">${esc(a[1])}</a></h4><p>${esc(a[2])}</p></li>`).join('\n')}
    </ul>
  </section>

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
    <div class="sec-head"><h2 id="h-throughout">How we build</h2><span class="sec-note">Throughout, not a milestone: the standard every item is held to.</span></div>
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

const cell = (s) => String(s).replace(/\\/g, '\\\\').replace(/\|/g, '\\|')
const row = (i, m) => `| [${cell(i.title)}](${m ? `roadmap/${pageName(m)}.md#${slug(i.title)}` : '#'}) | ${STATUS[i.status]} | ${cell(i.value)}${i.link ? ` ([docs](${mdHref(i.link)}))` : ''} |`
const md = `# Roadmap, as a list

What Launchway does today and what is planned, in milestones marked Now, Next and Later. The same
items are drawn on the [roadmap](./public/roadmap.html), and each milestone has a detail page. As of ${src.updated}.

<!-- Generated by scripts/docs/build-roadmap.mjs from docs/roadmap/roadmap.json. Do not edit by hand. -->

${src.vision.join(' ')}

${st ? `## Where Launchway stands today\n\n**Already strong.**\n\n${st.strong.map((x) => `- ${x}`).join('\n')}\n\n**Not there yet.**\n\n${st.missing.map((x) => `- ${x}`).join('\n')}\n\n${st.note || ''}\n` : ''}

${wf.length ? `## Who Launchway is for\n\n${src.whoForNote || ''}\n\n${wf.map((x) => `- **${x.who}.** ${x.wants} *${x.today}*`).join('\n')}\n` : ''}

${(src.buildQueue || []).length ? `## The order we are building in\n\nAfter what is already in flight, in this order.\n\n${src.buildQueue.map((q, n) => `${n + 1}. **${q.title}.** ${q.value}${q.area ? ` ([area page](roadmap/${q.area}.md))` : ''}`).join('\n')}\n` : ''}

${wc ? `**What changed in the roadmap, ${wc.date}.**\n\n${wc.items.map((x) => `- ${x}`).join('\n')}\n` : ''}

Order is by value, not a schedule, and plans change. An item is **available** only when it is on the
main branch, tested and documented. Launchway is not certified against any compliance standard;
compliance items provide controls and evidence.

## Available today

| Item | What it gives you |
|---|---|
${src.available.map((a) => `| ${cell(a.title)} | ${cell(a.value)}${a.link ? ` ([more](${mdHref(a.link)}))` : ''} |`).join('\n')}

${src.milestones.map((m) => `## ${m.number ? `Milestone ${m.number}: ` : ''}${m.name} (${WHEN[m.when]})

${m.promise} [Detail page](roadmap/${pageName(m)}.md).

${m.groups.map((g) => `### ${g.name}

| Item | Status | What it gives you |
|---|---|---|
${g.items.map((i) => row(i, m)).join('\n')}
`).join('\n')}`).join('\n')}
## How we build

Throughout, not a milestone: the standard every item is held to.

${src.principles.map((x) => `- **${x.title}.** ${x.value}`).join('\n')}
`

const pageChip = (s) => `<span class="rm-chip st-${s}"><span aria-hidden="true">${icon[s]}</span> ${STATUS[s]}</span>`
const bullets = (a) => a.map((x) => `- ${x}`).join('\n')
const milestonePage = (m, n) => {
  const list = itemsOf(m), c = count(list)
  const prev = src.milestones[n - 1], next = src.milestones[n + 1]
  const nav = [prev ? `[← ${prev.number ? 'Milestone ' + prev.number : 'Previous'}: ${prev.short}](${pageName(prev)}.md)` : '', next ? `[${next.number ? 'Milestone ' + next.number : 'Beyond'}: ${next.short} →](${pageName(next)}.md)` : ''].filter(Boolean).join(' · ')
  return `# ${m.number ? `Milestone ${m.number}: ` : ''}${m.name}

<!-- Generated by scripts/docs/build-roadmap.mjs from docs/roadmap/roadmap.json. Do not edit by hand. -->

<p class="rm-crumbs"><a href="../roadmap.html">Roadmap</a> › ${m.number ? `Milestone ${m.number}` : 'Beyond'} · <a href="../roadmap-list.html">as a list</a></p>

<p class="rm-meta"><span class="rm-when when-${m.when}">${WHEN[m.when]}</span> ${counts(c)} · ${list.length} capabilities</p>

${m.promise}

## What it is, and who it is for

${m.what}

**Who it is for.** ${m.audience}

## What you will be able to do

${bullets(m.scenarios)}

## How it fits together

\`\`\`mermaid
${m.diagram}
\`\`\`

*${m.diagramNote}*

${m.buildsOn.length ? `**Builds on:** ${m.buildsOn.map((b) => `[${byId[b].number ? 'Milestone ' + byId[b].number : 'Beyond'}: ${byId[b].short}](${pageName(byId[b])}.md)`).join(', ')}.` : '**Builds on:** nothing; this is where it starts.'} ${(() => { const l = src.milestones.filter((x) => x.buildsOn.includes(m.id)); return l.length ? `**Leads to:** ${l.map((x) => `[${x.number ? 'Milestone ' + x.number : 'Beyond'}: ${x.short}](${pageName(x)}.md)`).join(', ')}.` : '' })()}
${m.deepDives && m.deepDives.length ? `\n**Go deeper:** ${m.deepDives.map((d) => `[${d.title}](${d.area}.md)`).join(' · ')}.\n` : ''}
## The capabilities

| Group | Capabilities | Available | In progress | Planned |
|---|---|---|---|---|
${m.groups.map((g) => { const gc = count(g.items); return `| [${cell(g.name)}](#g-${slug(g.name)}) | ${g.items.length} | ${gc.available} | ${gc['in-progress']} | ${gc.planned} |` }).join('\n')}

${m.groups.map((g) => `### ${g.name} {#g-${slug(g.name)}}

${g.blurb}

${g.items.map((i) => `#### ${i.title} {#${slug(i.title)}}

<p class="rm-meta">${pageChip(i.status)}</p>

${i.value} ${i.detail}

${i.needs ? `**Depends on:** ${i.needs}\n\n` : ''}${[i.link ? `[Read the documentation](${mdHref(i.link, '../')})` : '', i.area ? `[Area page](${i.area}.md)` : ''].filter(Boolean).join(' · ')}${i.link || i.area ? '\n' : ''}`).join('\n')}`).join('\n')}
## What done means

This milestone is done when all of these are true.

${bullets(m.done)}

## What is deliberately not in it

${bullets(m.notIncluded)}

---

${nav}
`
}
for (const [a] of AREAS) if (!existsSync(p(`docs/roadmap/${a}.md`))) fail(`the capability page docs/roadmap/${a}.md does not exist`)

const outputs = [['docs/public/roadmap.html', html], ['docs/roadmap-list.md', md], ...src.milestones.map((m, n) => [`docs/roadmap/${pageName(m)}.md`, milestonePage(m, n)])]
for (const [f, content] of outputs.slice(1)) for (const d of DENY) if (d.test(content)) fail(`${f} matches ${d}, which must not be published`)
let stale = 0
for (const [f, content] of outputs) {
  const cur = existsSync(p(f)) ? readFileSync(p(f), 'utf8') : ''
  if (cur === content) continue
  if (check) { console.error(`build-roadmap: ${f} is out of date; run make docs-roadmap`); stale++ }
  else writeFileSync(p(f), content)
}
if (stale) process.exit(1)
console.log(`build-roadmap: ${totals.available} available, ${totals['in-progress']} in progress, ${totals.planned} planned; ${capCount} capabilities across ${src.milestones.length} milestones${check ? ' are up to date' : ' written'}`)
