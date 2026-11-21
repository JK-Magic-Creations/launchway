#!/usr/bin/env node
// Applies the safe mechanical replacements (American spelling, "audit log" to "audit trail") to the
// text people read, and prints what needs a person. Re-runnable: after other branches merge, run it
// again. `--dry` shows without writing. `--docs` rewrites the term tables in docs/reference/vocabulary.md.
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { loadVocabulary, listFiles, compileChecks, fileFindings, mechanicalFix, loadList } from './lib.mjs'

const root = process.cwd()
const args = process.argv.slice(2)
const dry = args.includes('--dry')
const vocab = loadVocabulary(root)
const checks = compileChecks(vocab)
const allow = loadList(root, 'quality/vocabulary-allow.json')?.allow ?? []
const allowed = (f, line) => allow.some((a) => (f.path === a.path || (a.path.endsWith('/') && f.path.startsWith(a.path))) && (!a.rule || a.rule === f.rule) && (!a.contains || line.includes(a.contains)))
const only = args.filter((a) => !a.startsWith('--'))

if (args.includes('--docs')) {
  const page = 'docs/reference/vocabulary.md'
  let md = readFileSync(join(root, page), 'utf8')
  const row = (t) => `| **${t.default}** | ${t.plain} | ${t.meaning} | ${t.renameable ? 'yes' : 'no'} | ${t.mustNotBeCalled.length ? t.mustNotBeCalled.join(', ') : 'none'} |`
  const terms = ['| Default word | Plain word | Meaning | Renameable | Never call it |', '|---|---|---|---|---|', ...vocab.terms.map(row)].join('\n')
  const rules = Object.entries(vocab.rules).filter(([k]) => k !== 'disclaimer').map(([k, v]) => `- **${k}.** ${v}`).join('\n')
  const sub = (name, body) => { md = md.replace(new RegExp(`(<!-- vocabulary:${name} -->)[\\s\\S]*?(<!-- /vocabulary:${name} -->)`), `$1\n${body}\n$2`) }
  sub('terms', terms); sub('rules', rules)
  const checks = ['| Rule | Kind | Use instead |', '|---|---|---|', ...vocab.checks.map((c) => `| \`${c.id}\` | ${c.severity === 'error' ? 'fails the check' : 'review list'} | ${c.use} |`)].join('\n')
  sub('checks', checks)
  if (!dry) writeFileSync(join(root, page), md)
  console.log('vocabulary-fix: rewrote the tables in ' + page)
  process.exit(0)
}
let fixed = 0
const human = []
for (const { path, kind } of listFiles(root, vocab)) {
  if (only.length && !only.some((o) => path.startsWith(o))) continue
  const text = readFileSync(join(root, path), 'utf8')
  const lines = text.split('\n')
  const fs = fileFindings(path, kind, text, checks).sort((a, b) => a.start - b.start).filter((f) => f.severity === 'error' && !allowed(f, lines[f.line - 1]))
  let out = text, shift = 0
  for (const f of fs) {
    const to = mechanicalFix(f)
    if (to === null) { human.push(`${f.path}:${f.line}: "${f.match}" (${f.rule}): use ${f.use}`); continue }
    out = out.slice(0, f.start + shift) + to + out.slice(f.end + shift); shift += to.length - (f.end - f.start); fixed++
  }
  if (out !== text && !dry) writeFileSync(join(root, path), out)
}
console.log(`vocabulary-fix: ${fixed} replacement(s)${dry ? ' (dry run)' : ''}; ${human.length} for a person:`)
for (const h of human) console.log('  ' + h)
