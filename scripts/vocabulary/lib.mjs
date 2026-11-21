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
  for (const top of ['README.md', 'CONTRIBUTING.md', 'SECURITY.md', 'SUPPORT.md', 'deploy/README.md']) if (existsSync(join(root, top))) all.push(top)
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
export function maskCode(text, lang, path = '', labels = new Set(), ranges = null) {
  const out = text.replace(/[^\n]/g, ' ').split('')
  const n = text.length
  let i = 0
  const keep = (a, b) => {
    const s = text.slice(a, b)
    if (/className\s*[=:]\s*\{?\s*["'`]?$/.test(text.slice(Math.max(0, a - 24), a))) return // a CSS class list
    if (!/[A-Za-z]{2,}\s+\S/.test(s) && !labels.has(s)) return // a key, a path or a single word is not a sentence, unless it is exactly a caption the vocabulary supersedes
    if (/^(\w+:"[^"]*"\s*)+$/.test(s.trim()) && !/jsonschema:/.test(s)) return // a struct tag
    if (/^\s*(SELECT|INSERT|UPDATE|DELETE|CREATE|WITH|ALTER|DROP|PRAGMA)\b/i.test(s)) return // SQL
    if (/\b(FROM|WHERE|VALUES|SET)\b[^\n]*[=(?]/.test(s) && /\?/.test(s)) return // a SQL fragment
    if (/jsonschema:"/.test(s)) { // only the jsonschema part of a tag is read
      const m = /jsonschema:"((?:[^"\\]|\\.)*)"/.exec(s)
      if (m) { const a2 = a + m.index + 'jsonschema:"'.length; for (let k = a2; k < a2 + m[1].length; k++) out[k] = text[k]; }
      return
    }
    if (ranges) ranges.push({ a, b, quote: text[a - 1], jsx: false })
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
      if (m && (/\s/.test(m[1].trim()) || labels.has(m[1].trim())) && !/[=();:]/.test(m[1])) { if (ranges) ranges.push({ a: i + 1, b: i + 1 + m[1].length, quote: '', jsx: true }); for (let k = 0; k < m[1].length; k++) out[i + 1 + k] = text[i + 1 + k] }
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

export function readableText(kind, path, text, labels) {
  if (kind === 'docs') return maskMarkdown(text)
  if (kind === 'web') return maskCode(text, 'ts', path, labels)
  if (kind === 'go') return maskCode(text, 'go', '', labels)
  if (kind === 'helm') return maskHelm(text)
  return maskJson(text)
}

const BRITISH_OK = /\b(license[ds]?|licensed|colors?|colored|coloring|behaviors?|favors?|favorite|cancelled|labelled|modelling|travelling|analyse[sd]?|catalogue[sd]?|centre[sd]?|defence|grey|artefacts?)\b/i

const pluralize = (w) => (/[^aeiou]y$/i.test(w) ? w.slice(0, -1) + 'ies' : /(s|x|ch|sh)$/i.test(w) ? w + 'es' : w + 's')
const thirdPerson = (v) => (/(s|x|ch|sh)$/.test(v) ? v + 'es' : v + 's')

/** The themed word's forms (aviation set), from the table. */
export function themedForms(vocab, key) {
  const w = vocab.words.words.find((x) => x.key === key)
  if (!w) return null
  const f = w.aviation
  return { one: f.one, many: f.many || pluralize(f.one), do: f.do, did: f.did, doing: f.doing, does: f.do ? thirdPerson(f.do) : undefined, plain: w.plain.one }
}

/** The caption words (single words a button or a heading shows) that the vocabulary supersedes. */
export function captionLabels(vocab) {
  return new Set((vocab.superseded || []).flatMap((s) => Object.keys(s.labels || {})))
}

/** A caption word and the placeholder the themifier puts in its place ("" when it has none and a person must choose). */
export function captionPlaceholders(vocab) {
  return Object.assign({}, ...(vocab.superseded || []).map((s) => s.labels || {}))
}

export function compileChecks(vocab) {
  const base = vocab.checks.map((c) => ({ ...c, re: new RegExp(c.pattern, c.caseSensitive ? 'g' : 'gi') }))
  const words = []
  for (const sup of vocab.superseded || []) {
    const t = themedForms(vocab, sup.key)
    sup.rules.forEach((r, i) => words.push({
      id: 'word:' + sup.key + (sup.rules.length > 1 ? '/' + (i + 1) : ''), word: sup.key, severity: 'error', re: new RegExp('(?<![\\w\\-./{])(?:' + r.pattern + ')(?![\\w-]|\\.\\w|/)', 'gi'),
      to: r.to, auto: !!r.auto, web: !!r.web, themed: t,
      use: `the product word "${t.one}" through the terms helper ({${sup.key}}) or, in documentation, "${t.one}" with "(${t.plain.toLowerCase()})" where it is introduced`,
    }))
  }
  return [...base, ...words]
}

/** The replacement for a word rule's match: the themed word in the form the rule names, keeping a verb's capital. */
export function themedReplacement(f) {
  const t = f.themed, w = f.match
  const verb = (v) => (w[0] === w[0].toUpperCase() ? v[0].toUpperCase() + v.slice(1) : v)
  switch (f.to) {
    case 'one': return t.one
    case 'many': return t.many
    case 'number': return /s$/i.test(w) && !/(ss|us|is)$/i.test(w) ? t.many : t.one
    case 'do': return t.do ? verb(t.do) : null
    case 'does': return t.does ? verb(t.does) : null
    case 'did': return t.did ? verb(t.did) : null
    case 'doing': return t.doing ? verb(t.doing) : null
    case 'does_rb': return t.do ? verb(/^rolls/i.test(w) ? thirdPerson(t.do) : t.do) : null
  }
  return null
}

// Which matches of the broad spelling pattern are British (the pattern also catches American words
// so one list serves; this keeps only the British ones).
function isBritish(w) {
  const l = w.toLowerCase()
  return /(organis|colour|behaviour|favour|licence|licenc|initialis|authoris|customis|summaris|recognis|normalis|synchronis|catalogue|centre|defence|artefact|^grey$|^analyse|cancelled|labelled|modelling|travelling)/.test(l)
}

/** Documentation introduces a themed word with its plain word beside it: "Station (runner)". That is the one place the plain word belongs. */
function introduced(text, at, t) {
  const before = text.slice(Math.max(0, at - 60), at)
  const forms = [t.one, t.many, t.do, t.did, t.doing, t.does].filter(Boolean).map((x) => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
  return new RegExp('(?:' + forms.join('|') + ')[^.\\n]{0,4}\\(\\s*(?:also |plain: |plain word: |a |an |the |called |formerly |the plain word )?$', 'i').test(before)
}

export function fileFindings(path, kind, text, checks, labels = new Set()) {
  const masked = readableText(kind, path, text, labels)
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
      if (c.word && introduced(text, m.index, c.themed)) continue
      out.push({ pre: text.slice(Math.max(0, m.index - 12), m.index), rule: c.id, word: c.word, to: c.to, auto: c.auto, themed: c.themed, severity: c.severity, path, kind, line: lineOf(m.index), start: m.index, end: m.index + m[0].length, match: m[0], fix: c.fix, use: c.use })
    }
  }
  return out
}

/** What the fixer may replace mechanically. Returns the new word or null. */
export function mechanicalFix(f) {
  if (f.word) return f.auto && (f.kind === 'docs' || f.kind === 'json' || f.kind === 'helm') ? themedReplacement(f) : null
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
