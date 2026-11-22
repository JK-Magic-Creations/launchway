#!/usr/bin/env node
// Applies the safe mechanical replacements (American spelling, "audit log" to "audit trail") to the
// text people read, and prints what needs a person. Re-runnable: after other branches merge, run it
// again. `--dry` shows without writing. `--docs` rewrites the term tables in docs/reference/vocabulary.md.
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { themify } from './themify.mjs'
import { loadVocabulary, listFiles, compileChecks, fileFindings, mechanicalFix, loadList, captionLabels } from './lib.mjs'

const root = process.cwd()
const args = process.argv.slice(2)
const dry = args.includes('--dry')
const vocab = loadVocabulary(root)
const checks = compileChecks(vocab)
const labels = captionLabels(vocab)
const allow = loadList(root, 'quality/vocabulary-allow.json')?.allow ?? []
// A roadmap name or title is also the address of its anchor, and addresses are stable identifiers.
const allowed = (f, line) => (f.kind === 'json' && /^\s*"(name|title|short)"\s*:/.test(line)) || allow.some((a) => (f.path === a.path || (a.path.endsWith('/') && f.path.startsWith(a.path))) && (!a.rule || a.rule === f.rule) && (!a.contains || line.includes(a.contains)))
const only = args.filter((a) => !a.startsWith('--'))
// VOCAB_WORD=project limits the fixer to the rules of one word (a key of the table), for a staged rename.
const onlyWord = process.env.VOCAB_WORD || ''

if (args.includes('--docs')) {
  const page = 'docs/reference/vocabulary.md'
  let md = readFileSync(join(root, page), 'utf8')
  const groupTitle = Object.fromEntries(vocab.words.groups.map((g) => [g.ID, g.Title]))
  const W = vocab.words.words
  const forms = (f) => (f.many && f.many !== f.one + 's' ? `${f.one} / ${f.many}` : f.one)
  const row = (t) => `| \`${t.key}\` | **${forms(t.aviation)}** | ${forms(t.plain)} | ${t.meaning} | ${t.renameable ? 'yes' : 'no'} | ${(t.mustNotBeCalled || []).length ? t.mustNotBeCalled.join(', ') : 'none'} |`
  const head = ['| Key (stable) | Default word | Plain word | Meaning | Renameable | Never call it |', '|---|---|---|---|---|---|']
  const terms = vocab.words.groups.map((g) => `### ${g.Title}\n\n` + [...head, ...W.filter((w) => w.group === g.ID).map(row)].join('\n')).join('\n\n')
  const rules = Object.entries(vocab.rules).filter(([k]) => k !== 'disclaimer').map(([k, v]) => `- **${k}.** ${v}`).join('\n')
  const sub = (name, body) => { md = md.replace(new RegExp(`(<!-- vocabulary:${name} -->)[\\s\\S]*?(<!-- /vocabulary:${name} -->)`), `$1\n${body}\n$2`) }
  const levels = ['| Stable name (API, flags, files) | Default word | Plain word | What it is |', '|---|---|---|---|', ...W.filter((w) => w.group === 'levels').map((l) => `| \`${l.identifier || l.key}\` | **${l.aviation.one}** | ${l.plain.one} | ${l.meaning.replace(/\.$/, '')} |`)].join('\n')
  sub('levels', levels); sub('terms', terms); sub('rules', rules)
  const checks = ['| Rule | Kind | Use instead |', '|---|---|---|', ...vocab.checks.map((c) => `| \`${c.id}\` | ${c.severity === 'error' ? 'fails the check' : 'review list'} | ${c.use} |`)].join('\n')
  sub('checks', checks)
  if (!dry) writeFileSync(join(root, page), md)
  const gpage = 'docs/guide/words.md'
  let gmd = readFileSync(join(root, gpage), 'utf8')
  const grow = (t) => `| \`${t.key}\` | **${forms(t.aviation)}** | ${forms(t.plain)} | ${t.meaning} |`
  const ghead = ['| Key | Default word | Plain word | What it means |', '|---|---|---|---|']
  const gbody = vocab.words.groups.map((g) => `### ${g.Title}\n\n` + [...ghead, ...W.filter((w) => w.group === g.ID).map(grow)].join('\n')).join('\n\n')
  gmd = gmd.replace(/(<!-- vocabulary:guide -->)[\s\S]*?(<!-- \/vocabulary:guide -->)/, `$1\n${gbody}\n$2`)
  if (!dry) writeFileSync(join(root, gpage), gmd)
  console.log('vocabulary-fix: rewrote the tables in ' + page)
  process.exit(0)
}
let fixed = 0
const human = []
for (const { path, kind } of listFiles(root, vocab)) {
  if (only.length && !only.some((o) => path.startsWith(o))) continue
  const text = readFileSync(join(root, path), 'utf8')
  const lines = text.split('\n')
  if (kind === 'web' && args.includes('--web')) {
    if (allow.some((a) => a.path === path && !a.rule && !a.contains)) continue
    const next = themify(text, path, kind, vocab, checks)
    if (next !== text) { if (!dry) writeFileSync(join(root, path), next); fixed++; console.log('themified ' + path) }
    continue
  }
  if (args.includes('--web')) continue
  const found = fileFindings(path, kind, text, checks, labels).filter((f) => f.severity === 'error' && (!onlyWord || f.word === onlyWord) && !allowed(f, lines[f.line - 1]))
  // Longest first at the same place, and no two replacements overlap (runner groups before runners).
  found.sort((a, b) => a.start - b.start || b.end - a.end)
  const fs = []
  let lastEnd = -1
  for (const f of found) { if (f.start < lastEnd) continue; fs.push(f); lastEnd = f.end }
  let out = text, shift = 0
  const introduced = new Set()
  for (const f of fs) {
    let to = mechanicalFix(f)
    if (to === null) { human.push(`${f.path}:${f.line}: "${f.match}" (${f.rule}): use ${f.use}`); continue }
    const nameForm = !!f.themed && (to === f.themed.one || to === f.themed.many)
    if (f.word && kind === 'docs' && !introduced.has(f.word) && nameForm && !/^['\u2019]s/.test(text.slice(f.end, f.end + 2))) {
      // The first use on a page carries the plain word, once: "Station (runner)".
      introduced.add(f.word)
      if (!/^\s*\(/.test(text.slice(f.end, f.end + 3))) to = `${to} (${(f.themed.plain || f.match).toLowerCase()})`
    }
    // "a" or "an" before the new word.
    let from = f.start + shift
    const art = /\b(a|an|A|An)\s$/.exec(out.slice(Math.max(0, from - 4), from))
    if (art && f.word) {
      const wantAn = /^[aeiou]/i.test(to)
      const cur = art[1]
      const next = wantAn ? (cur === 'A' || cur === 'An' ? 'An' : 'an') : (cur === 'A' || cur === 'An' ? 'A' : 'a')
      if (next !== cur) { const at = from - art[0].length; out = out.slice(0, at) + next + ' ' + out.slice(from); shift += next.length - cur.length; from = f.start + shift }
    }
    out = out.slice(0, from) + to + out.slice(f.end + shift); shift += to.length - (f.end - f.start); fixed++
  }
  // A heading that changed keeps its old address: addresses are stable identifiers.
  if (kind === 'docs' && out !== text) {
    const slug = (h) => h.toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, '').trim().replace(/\s+/g, '-')
    const before = text.split('\n'), after = out.split('\n')
    if (before.length === after.length) {
      let fence = false
      for (let i = 0; i < after.length; i++) {
        if (/^(```|~~~)/.test(after[i])) fence = !fence
        if (fence || after[i] === before[i] || !/^#{1,6} /.test(before[i]) || /\{#[^}]+\}\s*$/.test(after[i])) continue
        after[i] = after[i] + ' {#' + slug(before[i].replace(/^#+\s+/, '')) + '}'
      }
      out = after.join('\n')
    }
  }
  out = out.replace(/\ban (Wings?|Base)\b/g, 'a $1').replace(/\bA (Wings?|Squadrons?)\b/g, 'A $1')
  if (out !== text && !dry) writeFileSync(join(root, path), out)
}
console.log(`vocabulary-fix: ${fixed} replacement(s)${dry ? ' (dry run)' : ''}; ${human.length} for a person:`)
for (const h of human) console.log('  ' + h)
