#!/usr/bin/env node
// The vocabulary guard. docs/reference/vocabulary.md says what the rules are for.
//   node scripts/vocabulary/check.mjs            fail when an error rule has more hits than the baseline allows
//   node scripts/vocabulary/check.mjs --report   list every hit, with the review rules
//   node scripts/vocabulary/check.mjs --update-baseline   lower the baseline (it never grows)
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { loadVocabulary, listFiles, compileChecks, fileFindings, loadList, captionLabels } from './lib.mjs'

const root = process.cwd()
const args = process.argv.slice(2)
const vocab = loadVocabulary(root)
const checks = compileChecks(vocab)
const labels = captionLabels(vocab)
const allow = loadList(root, 'quality/vocabulary-allow.json')?.allow ?? []
const baseline = loadList(root, 'quality/vocabulary-baseline.json') ?? { counts: {} }

// A roadmap name or title is also the address of its anchor, and addresses are stable identifiers.
const allowed = (f, line) => (f.kind === 'json' && /^\s*"(name|title|short)"\s*:/.test(line)) || allow.some((a) => (f.path === a.path || (a.path.endsWith('/') && f.path.startsWith(a.path))) && (!a.rule || a.rule === f.rule) && (!a.contains || line.includes(a.contains)))

// The table itself: every word has a plain meaning, no combat word or real program, unit or rank name is a
// product word, and every themed word has a guard entry (so a new word cannot be added without its old form being watched).
const tableProblems = []
{
  const g = vocab.guard || {}
  const deny = new RegExp('\\b(' + (g.denyAsProductWords || []).join('|') + ')\\b', 'i')
  const real = new RegExp('\\b(' + (g.realNames || []).map((x) => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|') + ')\\b')
  const guarded = new Set([...(vocab.superseded || []).map((x) => x.key), ...(g.guardedByChecks || [])])
  for (const w of vocab.words.words) {
    if (!w.meaning || w.meaning.trim().length < 8) tableProblems.push(`${w.key}: has no plain meaning`)
    const shown = [w.aviation.one, w.aviation.many, w.aviation.do, w.aviation.did, w.aviation.doing].filter(Boolean).join(' / ')
    if (deny.test(shown)) tableProblems.push(`${w.key}: "${shown}" is a combat word; the theme is flying and ground operations only`)
    if (real.test(shown)) tableProblems.push(`${w.key}: "${shown}" is a real program, unit or rank name`)
    if (w.aviation.one !== w.plain.one && !guarded.has(w.key) && w.group !== 'levels' && w.group !== 'statuses') tableProblems.push(`${w.key}: add an entry to "superseded" in docs/vocabulary.json so its plain word is watched`)
  }
  for (const s of vocab.superseded || []) if (!vocab.words.words.some((w) => w.key === s.key)) tableProblems.push(`superseded entry "${s.key}" is not a word in the table`)
}

const all = []
for (const { path, kind } of listFiles(root, vocab)) {
  const text = readFileSync(join(root, path), 'utf8')
  const lines = text.split('\n')
  for (const f of fileFindings(path, kind, text, checks, labels)) if (!allowed(f, lines[f.line - 1])) all.push({ ...f, text: lines[f.line - 1].trim().slice(0, 140) })
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
for (const p of tableProblems) { bad++; console.error('vocabulary: table: ' + p) }
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
