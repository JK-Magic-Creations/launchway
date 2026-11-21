#!/usr/bin/env node
// The vocabulary guard. docs/reference/vocabulary.md says what the rules are for.
//   node scripts/vocabulary/check.mjs            fail when an error rule has more hits than the baseline allows
//   node scripts/vocabulary/check.mjs --report   list every hit, with the review rules
//   node scripts/vocabulary/check.mjs --update-baseline   lower the baseline (it never grows)
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { loadVocabulary, listFiles, compileChecks, fileFindings, loadList } from './lib.mjs'

const root = process.cwd()
const args = process.argv.slice(2)
const vocab = loadVocabulary(root)
const checks = compileChecks(vocab)
const allow = loadList(root, 'quality/vocabulary-allow.json')?.allow ?? []
const baseline = loadList(root, 'quality/vocabulary-baseline.json') ?? { counts: {} }

const allowed = (f, line) => allow.some((a) => (f.path === a.path || (a.path.endsWith('/') && f.path.startsWith(a.path))) && (!a.rule || a.rule === f.rule) && (!a.contains || line.includes(a.contains)))

const all = []
for (const { path, kind } of listFiles(root, vocab)) {
  const text = readFileSync(join(root, path), 'utf8')
  const lines = text.split('\n')
  for (const f of fileFindings(path, kind, text, checks)) if (!allowed(f, lines[f.line - 1])) all.push({ ...f, text: lines[f.line - 1].trim().slice(0, 140) })
}
const errors = all.filter((f) => f.severity === 'error')
const counts = {}
for (const f of errors) counts[f.rule + '|' + f.path] = (counts[f.rule + '|' + f.path] || 0) + 1

if (args.includes('--report')) {
  const by = {}
  for (const f of all) (by[f.rule + ' (' + f.severity + ')'] ||= []).push(f)
  for (const [r, list] of Object.entries(by)) { console.log(`\n== ${r}: ${list.length}`); for (const f of list) console.log(`${f.path}:${f.line}: ${f.match}  | ${f.text}`) }
  process.exit(0)
}
if (args.includes('--update-baseline')) {
  const next = {}
  for (const [k, n] of Object.entries(counts)) {
    const old = baseline.counts[k]
    if (old !== undefined && n > old) { console.error(`vocabulary: ${k} grew from ${old} to ${n}; fix the new use instead`); process.exit(1) }
    if (old === undefined && Object.keys(baseline.counts).length) { console.error(`vocabulary: ${k} is new (${n}); fix it instead`); process.exit(1) }
    next[k] = n
  }
  writeFileSync(join(root, 'quality/vocabulary-baseline.json'), JSON.stringify({ comment: 'Counts of vocabulary errors that predate the guard (scripts/vocabulary/check.mjs). They may only shrink: lower with `make vocabulary-update`; fix, do not add.', counts: Object.fromEntries(Object.entries(next).sort()) }, null, 2) + '\n')
  console.log(`vocabulary: baseline now ${Object.values(next).reduce((a, b) => a + b, 0)} hits in ${Object.keys(next).length} places`)
  process.exit(0)
}
let bad = 0
for (const [k, n] of Object.entries(counts)) {
  const allowedN = baseline.counts[k] ?? 0
  if (n > allowedN) {
    bad++
    const [rule, path] = k.split('|')
    console.error(`vocabulary: ${path}: ${n} use(s) of a forbidden word (${rule}), ${allowedN} allowed`)
    for (const f of errors.filter((x) => x.rule === rule && x.path === path)) console.error(`  ${f.path}:${f.line}: "${f.match}": use ${f.use}\n    ${f.text}`)
  }
}
if (bad) { console.error('\nvocabulary: see docs/reference/vocabulary.md. Fix the wording, or add a justified entry to quality/vocabulary-allow.json. `scripts/vocabulary-fix` applies the mechanical ones.'); process.exit(1) }
const total = errors.length
console.log(`make vocabulary: ok (${total} baseline hit${total === 1 ? '' : 's'} left to fix)`)
