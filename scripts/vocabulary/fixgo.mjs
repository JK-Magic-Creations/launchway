#!/usr/bin/env node
// Applies the mechanical word replacements to the sentences in Go source: the same rules as the
// documentation (a rule marked auto, in the form it names), at the places the check reads as prose
// (string literals that read as sentences, comments are not read). Re-runnable.
//   node scripts/vocabulary/fixgo.mjs [--dry] [path-prefix ...]
// Tests that assert on the old words are changed with them: run the packages' tests after.
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { loadVocabulary, listFiles, compileChecks, fileFindings, themedReplacement, loadList, captionLabels } from './lib.mjs'

const root = process.cwd()
const args = process.argv.slice(2)
const dry = args.includes('--dry')
const only = args.filter((a) => !a.startsWith('--'))
const vocab = loadVocabulary(root)
// Only the words the product has chosen to say in sentences: the others (registry, artifact, rollback, ...) are often
// the name of a key or a kind in what they sit in, and a person decides.
const WORDS = new Set(['toggle', 'deploy', 'run', 'runner', 'environment', 'queue'])
const checks = compileChecks(vocab).filter((c) => c.word && WORDS.has(c.word))
const labels = captionLabels(vocab)
const allow = loadList(root, 'quality/vocabulary-allow.json')?.allow ?? []
const allowed = (f, line) => allow.some((a) => (f.path === a.path || (a.path.endsWith('/') && f.path.startsWith(a.path))) && (!a.rule || a.rule === f.rule) && (!a.contains || line.includes(a.contains)))
// A match is changed only inside a double-quoted one-line string literal that reads as a sentence, where the word is
// a plain word of it: not a command or a flag (launchway runner, --run), not a key of a file (run:, environment=),
// not code in backticks, not a path or an address.
function prose(text, f) {
  const ls = text.lastIndexOf('\n', f.start) + 1
  let le = text.indexOf('\n', f.start); if (le < 0) le = text.length
  const line = text.slice(ls, le)
  const col = f.start - ls
  // inside a "..." literal that starts before and ends after the match, on this line
  let inStr = false, open = -1
  for (let i = 0; i < col; i++) {
    const c = line[i]
    if (c === '\\' && inStr) { i++; continue }
    if (c === '"') { inStr = !inStr; open = inStr ? i : -1 }
    else if (c === '`' && !inStr) return false // a raw string: usage blocks and templates, left to a person
    else if (c === "'" && !inStr) { /* a rune literal */ }
  }
  if (!inStr) return false
  let close = -1
  for (let i = col; i < line.length; i++) { if (line[i] === '\\') { i++; continue } if (line[i] === '"') { close = i; break } }
  if (close < 0) return false
  const lit = line.slice(open + 1, close)
  if (!/\s/.test(lit) || /[`]/.test(lit) || /--[a-z]/.test(lit) || /https?:\/\//.test(lit)) return false
  const before = line.slice(Math.max(open + 1, col - 14), col), after = line.slice(col + f.match.length, Math.min(close, col + f.match.length + 3))
  if (/launchway\s+$/i.test(before) || /(^|[\s(])[-\/.]$/.test(before) || /[\/.\-_]$/.test(before)) return false
  if (/^[:=_\/.\-{]/.test(after) && !/^\.\s|^\.$|^-\s/.test(after)) return false
  if (/^[a-z]+:/.test(lit.slice(0, 12))) return false
  return true
}
let changed = 0, files = 0
const per = {}
for (const { path, kind } of listFiles(root, vocab)) {
  if (kind !== 'go') continue
  if (only.length && !only.some((o) => path.startsWith(o))) continue
  const text = readFileSync(join(root, path), 'utf8')
  const lines = text.split('\n')
  const found = fileFindings(path, kind, text, checks, labels).filter((f) => f.severity === 'error' && f.auto && !allowed(f, lines[f.line - 1]) && prose(text, f))
  found.sort((a, b) => a.start - b.start || b.end - a.end)
  let out = '', pos = 0, last = -1, n = 0
  for (const f of found) {
    if (f.start < last) continue
    let to = themedReplacement(f)
    if (to === null) continue
    const art = /\b(a|an|A|An)\s$/.exec(text.slice(Math.max(0, f.start - 4), f.start))
    out += text.slice(pos, f.start)
    if (art) { const want = /^[aeiou]/i.test(to) ? (art[1][0] === 'A' ? 'An' : 'an') : (art[1][0] === 'A' ? 'A' : 'a'); if (art[1] !== want) out = out.slice(0, out.length - art[0].length) + want + ' ' }
    out += to; pos = f.end; last = f.end; n++
  }
  if (!n) continue
  out += text.slice(pos)
  per[path] = n; changed += n; files++
  if (!dry) writeFileSync(join(root, path), out)
}
console.log(`fixgo: ${changed} replacement(s) in ${files} file(s)${dry ? ' (dry run)' : ''}`)
if (dry) for (const [p, n] of Object.entries(per).sort((a, b) => b[1] - a[1]).slice(0, 25)) console.log(String(n).padStart(4), p)
