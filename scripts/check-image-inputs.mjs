#!/usr/bin/env node
// The image build cannot rot unseen. The Dockerfile's web stage copies web/ and then runs `npm run build`, which type-checks
// every file under web/src, tests included. A file there that imports something outside web/ (the vocabulary table in
// docs/, a page of the documentation read as text) fails that build unless the stage copies it first. Nothing else ran the
// stage, so the image once could not be built at all. This reads every relative import (and import.meta.glob pattern) in
// web/src that leaves web/, and fails unless the web stage has a COPY of its top-level directory before `npm run build`.
// `make image-build-check` runs the stage itself, where a container tool exists.
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, resolve, relative, dirname, sep } from 'node:path'

const root = resolve(new URL('..', import.meta.url).pathname)
const web = join(root, 'web')
const dockerfile = readFileSync(join(root, 'Dockerfile'), 'utf8').split('\n')

// The web stage: from its FROM ... AS web to the next FROM.
const start = dockerfile.findIndex((l) => /^FROM\s+\S+\s+AS\s+web\b/i.test(l))
if (start < 0) { console.error('check-image-inputs: the Dockerfile has no stage named web'); process.exit(1) }
let end = dockerfile.findIndex((l, i) => i > start && /^FROM\s/i.test(l)); if (end < 0) end = dockerfile.length
const stage = dockerfile.slice(start, end)
const build = stage.findIndex((l) => /^RUN\s+npm run build\b/.test(l))
if (build < 0) { console.error('check-image-inputs: the web stage has no `RUN npm run build`'); process.exit(1) }
const copiedBefore = new Set()
for (const l of stage.slice(0, build)) {
  const m = /^COPY\s+(?!--from)(.+)\s+\S+\s*$/.exec(l)
  if (m) for (const src of m[1].trim().split(/\s+/)) copiedBefore.add(src.replace(/\/+$/, '').split('/')[0])
}

const files = []
const walk = (d) => { for (const n of readdirSync(d)) { const p = join(d, n); if (n === 'node_modules' || n === 'dist') continue; statSync(p).isDirectory() ? walk(p) : /\.(ts|tsx|mts|js|mjs)$/.test(n) && files.push(p) } }
walk(join(web, 'src'))
// Relative specifiers in import/export from, import(), and import.meta.glob(...) first arguments.
const spec = /(?:from\s+|import\s*\(\s*|import\s+|glob\s*\(\s*\[?\s*)["'`](\.{1,2}\/[^"'`]*)["'`]/g
const needs = new Map()
for (const f of files) {
  const src = readFileSync(f, 'utf8')
  const globs = [...src.matchAll(/glob\s*\(\s*\[([^\]]*)\]/g)].flatMap((m) => [...m[1].matchAll(/["'`]([^"'`]+)["'`]/g)].map((x) => x[1]))
  for (const s of [...[...src.matchAll(spec)].map((m) => m[1]), ...globs.filter((g) => g.startsWith('.'))]) {
    const target = resolve(dirname(f), s.replace(/[?*{].*$/, ''))
    const rel = relative(root, target)
    if (rel.startsWith('..') || (rel === '' )) continue
    if (rel === 'web' || rel.startsWith('web' + sep)) continue
    const top = rel.split(sep)[0]
    if (!needs.has(top)) needs.set(top, [])
    needs.get(top).push(`${relative(root, f)} imports ${s}`)
  }
}
let bad = 0
for (const [top, who] of needs) {
  if (!existsSync(join(root, top))) continue
  if (!copiedBefore.has(top)) {
    bad++
    console.error(`check-image-inputs: the web stage of the Dockerfile does not copy ${top}/ before \`npm run build\`, and web/src reads it:\n  ${who.slice(0, 3).join('\n  ')}\n  Add \`COPY ${top}/ /src/${top}/\` before the build, or the image cannot be built.`)
  }
}
if (bad) process.exit(1)
console.log(`check-image-inputs: ok (web/src reads ${[...needs.keys()].join(', ') || 'nothing'} outside web/; the web stage copies ${[...copiedBefore].join(', ')} before the build)`)
