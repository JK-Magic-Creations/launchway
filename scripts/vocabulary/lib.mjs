// Shared by scripts/vocabulary-fix and the vocabulary check (make vocabulary). Plain Node, no
// dependencies, no network. It reads docs/vocabulary.json, finds the text a person reads in each
// kind of file, and matches the rules against it. Offsets always point into the original file, so
// the fixer can edit exactly what the check found.
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join } from 'node:path'

export function loadVocabulary(root) {
  return JSON.parse(readFileSync(join(root, 'docs/vocabulary.json'), 'utf8'))
}

function globToRe(g) {
  let re = ''
  for (let i = 0; i < g.length; i++) {
    const c = g[i]
    if (c === '*' && g[i + 1] === '*') { re += '.*'; i++; if (g[i + 1] === '/') i++ }
    else if (c === '*') re += '[^/]*'
    else re += c.replace(/[.+?^${}()|[\]\\]/g, '\\$&')
  }
  return new RegExp('^' + re + '$')
}

function walk(root, dir, out) {
  for (const name of readdirSync(join(root, dir))) {
    if (name === 'node_modules' || name === '.git' || name === 'dist' || name === '.claude') continue
    const rel = dir ? dir + '/' + name : name
    if (statSync(join(root, rel)).isDirectory()) walk(root, rel, out)
    else out.push(rel)
  }
}

/** The files to scan, each with its kind: docs, web, go, helm or json. */
export function listFiles(root, vocab) {
  const all = []
  for (const top of ['README.md', 'CONTRIBUTING.md', 'SECURITY.md', 'SUPPORT.md']) if (existsSync(join(root, top))) all.push(top)
  for (const d of ['docs', 'web/src', 'cmd', 'internal', 'deploy/helm']) if (existsSync(join(root, d))) walk(root, d, all)
  const kinds = []
  for (const [kind, globs] of Object.entries({ docs: vocab.scan.docs, web: vocab.scan.web, go: vocab.scan.go, helm: vocab.scan.helm, json: vocab.scan.json })) {
    const res = globs.map(globToRe)
    for (const f of all) {
      if (vocab.scan.skipPaths.some((s) => f.includes(s))) continue
      if (vocab.scan.generated.some((g) => globToRe(g).test(f))) continue
      if (res.some((r) => r.test(f))) kinds.push({ path: f, kind })
    }
  }
  return kinds
}

const blank = (s) => s.replace(/[^\n]/g, ' ')

/** Mask every character that a person does not read as prose, keeping the length and the newlines. */
export function maskMarkdown(text) {
  let t = text
  t = t.replace(/^---\n[\s\S]*?\n---\n/, blank)
  t = t.replace(/^(```|~~~)[^\n]*\n[\s\S]*?^\1[^\n]*$/gm, blank)
  t = t.replace(/<!--[\s\S]*?-->/g, blank)
  t = t.replace(/`[^`\n]+`/g, blank)
  t = t.replace(/\]\([^)\n]*\)/g, blank)
  t = t.replace(/^\[[^\]\n]+\]:.*$/gm, blank)
  t = t.replace(/https?:\/\/[^\s)>]+/g, blank)
  t = t.replace(/<[^>\n]+>/g, blank)
  t = t.replace(/\{#[^}\n]+\}/g, blank)
  return t
}

/** Mask all but the string literals of Go or TypeScript code that hold a phrase (they contain a space). */
export function maskCode(text, lang, path = '') {
  const out = text.replace(/[^\n]/g, ' ').split('')
  const n = text.length
  let i = 0
  const keep = (a, b) => {
    const s = text.slice(a, b)
    if (/className\s*[=:]\s*\{?\s*["'`]?$/.test(text.slice(Math.max(0, a - 24), a))) return // a CSS class list
    if (!/[A-Za-z]{2,}\s+\S/.test(s)) return // a key, a path or a single word is not a sentence
    if (/^(\w+:"[^"]*"\s*)+$/.test(s.trim()) && !/jsonschema:/.test(s)) return // a struct tag
    if (/^\s*(SELECT|INSERT|UPDATE|DELETE|CREATE|WITH|ALTER|DROP|PRAGMA)\b/i.test(s)) return // SQL
    if (/\b(FROM|WHERE|VALUES|SET)\b[^\n]*[=(?]/.test(s) && /\?/.test(s)) return // a SQL fragment
    if (/jsonschema:"/.test(s)) { // only the jsonschema part of a tag is read
      const m = /jsonschema:"((?:[^"\\]|\\.)*)"/.exec(s)
      if (m) { const a2 = a + m.index + 'jsonschema:"'.length; for (let k = a2; k < a2 + m[1].length; k++) out[k] = text[k]; }
      return
    }
    for (let k = a; k < b; k++) out[k] = text[k]
    for (let k = a; k < b - 1; k++) if (text[k] === '$' && text[k + 1] === '{') { let depth = 0, q = k + 1; for (; q < b; q++) { if (text[q] === '{') depth++; else if (text[q] === '}' && --depth === 0) break } for (let z = k; z <= q && z < b; z++) if (text[z] !== '\n') out[z] = ' '; k = q }
  }
  while (i < n) {
    const c = text[i], d = text[i + 1]
    if (c === '/' && d === '/') { while (i < n && text[i] !== '\n') i++; continue }
    if (c === '/' && d === '*') { const e = text.indexOf('*/', i + 2); i = e < 0 ? n : e + 2; continue }
    if (lang === 'go' && c === "'") { i += text[i + 1] === '\\' ? 4 : 3; continue }
    if (c === '"' || c === "'" || c === '`') {
      let j = i + 1
      while (j < n && text[j] !== c) { if (text[j] === '\\' && c !== '`') j++; else if (c === '`' && lang !== 'go' && text[j] === '$' && text[j + 1] === '{') { let depth = 1; j += 2; while (j < n && depth) { if (text[j] === '{') depth++; else if (text[j] === '}') depth--; j++ } continue } if (text[j] === '\n' && c !== '`') break; j++ }
      if (text[j] === c) keep(i + 1, j)
      i = j + 1; continue
    }
    if (lang === 'ts' && path.endsWith('.tsx') && c === '>' ) { // JSX text between tags
      const m = /^>([^<>{}\n]*[A-Za-z]{2,}[^<>{}\n]*)</.exec(text.slice(i, i + 400))
      if (m && /\s/.test(m[1].trim()) && !/[=();:]/.test(m[1])) for (let k = 0; k < m[1].length; k++) out[i + 1 + k] = text[i + 1 + k]
    }
    i++
  }
  return out.join('')
}

export function maskHelm(text) {
  return text.split('\n').map((l) => { const k = l.indexOf('#'); return k < 0 ? ' '.repeat(l.length) : ' '.repeat(k + 1) + l.slice(k + 1) }).join('\n')
}

export function maskJson(text) {
  return text.replace(/"((?:[^"\\\n]|\\.)*)"|[^"\n]|\n/g, (m, s) => {
    if (s === undefined) return m === '\n' ? '\n' : ' '
    return /[A-Za-z]{2,}\s+\S/.test(s) ? ' ' + s + ' ' : blank(m)
  })
}

export function readableText(kind, path, text) {
  if (kind === 'docs') return maskMarkdown(text)
  if (kind === 'web') return maskCode(text, 'ts', path)
  if (kind === 'go') return maskCode(text, 'go')
  if (kind === 'helm') return maskHelm(text)
  return maskJson(text)
}

const BRITISH_OK = /\b(license[ds]?|licensed|colors?|colored|coloring|behaviors?|favors?|favorite|cancelled|labelled|modelling|travelling|analyse[sd]?|catalogue[sd]?|centre[sd]?|defence|grey|artefacts?)\b/i

export function compileChecks(vocab) {
  return vocab.checks.map((c) => ({ ...c, re: new RegExp(c.pattern, c.caseSensitive ? 'g' : 'gi') }))
}

// Which matches of the broad spelling pattern are British (the pattern also catches American words
// so one list serves; this keeps only the British ones).
function isBritish(w) {
  const l = w.toLowerCase()
  return /(organis|colour|behaviour|favour|licence|licenc|initialis|authoris|customis|summaris|recognis|normalis|synchronis|catalogue|centre|defence|artefact|^grey$|^analyse|cancelled|labelled|modelling|travelling)/.test(l)
}

export function fileFindings(path, kind, text, checks) {
  const masked = readableText(kind, path, text)
  const lineStarts = [0]
  for (let i = 0; i < text.length; i++) if (text[i] === '\n') lineStarts.push(i + 1)
  const lineOf = (off) => { let lo = 0, hi = lineStarts.length - 1; while (lo < hi) { const m = (lo + hi + 1) >> 1; if (lineStarts[m] <= off) lo = m; else hi = m - 1 } return lo + 1 }
  const out = []
  for (const c of checks) {
    if (c.scope && !c.scope.includes(kind)) continue
    c.re.lastIndex = 0
    let m
    while ((m = c.re.exec(masked))) {
      if (m[0] === '') { c.re.lastIndex++; continue }
      if (c.britishOnly && !isBritish(m[0])) continue
      out.push({ pre: text.slice(Math.max(0, m.index - 12), m.index), rule: c.id, severity: c.severity, path, line: lineOf(m.index), start: m.index, end: m.index + m[0].length, match: m[0], fix: c.fix, use: c.use })
    }
  }
  return out
}

/** What the fixer may replace mechanically. Returns the new word or null. */
export function mechanicalFix(f) {
  const w = f.match
  const keepCase = (to) => (w[0] === w[0].toUpperCase() ? to[0].toUpperCase() + to.slice(1) : to)
  if (f.fix === 'lowercase') return w.toLowerCase()
  if (f.fix === 'logbook') return /(^|[|.:!?]\s*|\n\s*(?:[-*]\s+)?)$/.test(f.pre) ? 'The Logbook' : 'the Logbook'
  if (f.fix === 'capitalize-feature') return w[0].toUpperCase() + w.slice(1)
  if (f.fix === 'capitalize-level') return w.replace(/(?<=\s|^)(wings?|squadrons?|bases?)$/, (x) => x[0].toUpperCase() + x.slice(1))
  if (f.fix === 'wing' || f.fix === 'squadron' || f.fix === 'base') {
    const plural = /s$/i.test(w) && !/settings?$/i.test(w) && f.fix !== 'base'
    if (f.fix === 'base') return w.replace(/^installation/i, 'Base')
    if (f.fix === 'wing') return w.replace(/organi[sz]ations?$/i, plural ? 'Wings' : 'Wing')
    return w.replace(/units?$/i, plural ? 'Squadrons' : 'Squadron')
  }
  if (f.fix === 'audit-trail') return keepCase('audit trail')
  if (f.fix === 'american') {
    const map = [[/organis/i, 'organiz'], [/colour/i, 'color'], [/behaviour/i, 'behavior'], [/favour/i, 'favor'], [/licence/i, 'license'], [/catalogue/i, 'catalog'], [/centre/i, 'center'], [/defence/i, 'defense'], [/artefact/i, 'artifact'], [/initialis/i, 'initializ'], [/authoris/i, 'authoriz'], [/customis/i, 'customiz'], [/summaris/i, 'summariz'], [/recognis/i, 'recogniz'], [/normalis/i, 'normaliz'], [/synchronis/i, 'synchroniz']]
    for (const [re, to] of map) if (re.test(w)) return w.replace(re, (x) => (x[0] === x[0].toUpperCase() ? to[0].toUpperCase() + to.slice(1) : to))
  }
  return null
}

export function loadList(root, rel) {
  const p = join(root, rel)
  return existsSync(p) ? JSON.parse(readFileSync(p, 'utf8')) : null
}
