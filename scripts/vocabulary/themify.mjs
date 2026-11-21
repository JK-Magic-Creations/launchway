// Turns the plain words in the text a person reads (string literals and JSX text in web/src) into
// placeholders the terms helper fills: "A failed run" becomes terms().say("A failed {run}"), a caption
// <h2>Runners</h2> becomes <h2>{terms().say("{Runners}")}</h2>. Re-runnable and idempotent: after a branch
// merges, run `scripts/vocabulary-fix --web web/src` again. It reads the code with the TypeScript parser (from
// web/node_modules, so run `npm ci` in web/ first), and only touches text inside a function: a constant at the
// top of a module is read once, before the words are known, so it is listed for a person to turn into a function.
import { createRequire } from 'node:module'
import { captionPlaceholders } from './lib.mjs'
import { dirname, relative, sep, join } from 'node:path'

let ts
function loadTs(root) {
  if (!ts) ts = createRequire(join(root, 'web/package.json'))('typescript')
  return ts
}

const SKIP_ATTR = /^(?:data-[\w-]+|id|key|href|className|class|role|type|name|kind|wizard|to|src|htmlFor|for|value|rel|target|method|autoComplete|inputMode|pattern|icon|tone|size|variant|width|height|viewBox|d|fill|stroke|style|tabIndex|min|max|step|rows|cols|accept|lang|dir|as|state|sm|label_for)$/
const CODEISH = /(?:^|\s)--[a-z]|\blaunchway [a-z]|\bpodman |\bkubectl |\bhelm |\bdocker |^\$ |=>|\bimport |\bconst |\bfunction |:\/\//

function placeholder(rule, key, w) {
  const cap = w[0] === w[0].toUpperCase()
  const K = (k) => (cap ? k[0].toUpperCase() + k.slice(1) : k)
  switch (rule.to) {
    case 'one': return `{${K(key)}}`
    case 'many': return `{${K(key)}s}`
    case 'number': return /s$/i.test(w) && !/(ss|us|is)$/i.test(w) ? `{${K(key)}s}` : `{${K(key)}}`
    case 'do': case 'did': case 'doing': case 'does': return `{${K(key)}:${rule.to}}`
    case 'does_rb': return /^rolls/i.test(w) ? `{${K(key)}:does}` : `{${K(key)}:do}`
  }
  return null
}

/** Replace the plain words of one piece of text. Returns the new text, or null when nothing changed. */
function rewrite(raw, checks, caps) {
  const masked = raw.replace(/\$\{(?:[^{}]|\{[^{}]*\})*\}/g, (m) => ' '.repeat(m.length))
  const trimmed = raw.trim()
  if (caps[trimmed]) return raw.replace(trimmed, caps[trimmed])
  if (CODEISH.test(raw) || !/[A-Za-z]{2,}\s+\S/.test(raw)) return null
  const found = []
  for (const c of checks) {
    if (!c.word || !(c.auto || c.web)) continue
    c.re.lastIndex = 0
    let m
    while ((m = c.re.exec(masked))) { if (m[0] === '') { c.re.lastIndex++; continue } found.push({ s: m.index, e: m.index + m[0].length, ph: placeholder(c, c.word, m[0]) }) }
  }
  found.sort((a, b) => a.s - b.s || b.e - a.e)
  let out = raw, last = -1, shift = 0, changed = false
  for (const f of found) {
    if (f.s < last || !f.ph) continue
    out = out.slice(0, f.s + shift) + f.ph + out.slice(f.e + shift); shift += f.ph.length - (f.e - f.s); last = f.e; changed = true
  }
  return changed ? out : null
}

export function themify(text, path, kind, vocab, checks, root = process.cwd()) {
  const T = loadTs(root)
  const sf = T.createSourceFile(path, text, T.ScriptTarget.ES2022, true, path.endsWith('x') ? T.ScriptKind.TSX : T.ScriptKind.TS)
  const caps = captionPlaceholders(vocab)
  const dataModule = (vocab.scan.webDataModules || []).includes(path)
  const edits = []
  let needTerms = false
  const inFunction = (n) => { if (dataModule) return true; for (let p = n.parent; p; p = p.parent) if (T.isFunctionLike(p) || T.isClassDeclaration(p)) return true; return false }
  const isCodePosition = (n) => {
    const p = n.parent
    if (!p) return true
    if (T.isImportDeclaration(p) || T.isExportDeclaration(p) || T.isLiteralTypeNode(p) || T.isEnumMember(p) || T.isCaseClause(p) || T.isExternalModuleReference(p)) return true
    if (T.isPropertyAssignment(p) && p.name === n) return true
    if (T.isElementAccessExpression(p) && p.argumentExpression === n) return true
    if (T.isBinaryExpression(p) && [T.SyntaxKind.EqualsEqualsEqualsToken, T.SyntaxKind.ExclamationEqualsEqualsToken, T.SyntaxKind.EqualsEqualsToken].includes(p.operatorToken.kind)) return true
    if (T.isCallExpression(p) && T.isPropertyAccessExpression(p.expression) && ['includes', 'startsWith', 'endsWith', 'test', 'has', 'get', 'set', 'push', 'indexOf', 'querySelector', 'getAttribute', 'localStorage'].includes(p.expression.name.text) && p.arguments.includes(n)) return true
    if (T.isCallExpression(p) && ['fetch', 'api', 'req', 'navigate', 'go', 'href'].includes(p.expression.getText?.(sf))) return true
    return false
  }
  const insideSay = (n) => T.isCallExpression(n.parent) && T.isPropertyAccessExpression(n.parent.expression) && n.parent.expression.name.text === 'say'
  const visit = (n) => {
    if (T.isJsxText(n)) {
      const raw = n.getText(sf)
      const out = inFunction(n) ? rewrite(raw, checks, caps) : null
      if (out && !dataModule) {
        const lead = /^\s*/.exec(out)[0], trail = /\s*$/.exec(out)[0]
        edits.push({ a: n.getStart(sf), b: n.end, to: `${lead}{terms().say(${JSON.stringify(out.trim().replace(/\s*\n\s*/g, ' '))})}${trail}` }); needTerms = true
      }
    } else if (T.isStringLiteral(n) || T.isNoSubstitutionTemplateLiteral(n) || T.isTemplateExpression(n)) {
      const raw = n.getText(sf), q = raw[0]
      const body = raw.slice(1, -1)
      const jsxAttr = T.isJsxAttribute(n.parent) && n.parent.initializer === n
      if (T.isTemplateExpression(n)) { /* the inner pieces are strings too: the whole template is rewritten at once */ }
      if (!isCodePosition(n) && inFunction(n) && !(jsxAttr && SKIP_ATTR.test(n.parent.name.getText(sf)))) {
        const out = rewrite(body, checks, caps)
        if (out !== null) {
          const lit = q + out + q
          const to = insideSay(n) || dataModule ? lit : jsxAttr ? `{terms().say(${lit})}` : `terms().say(${lit})`
          edits.push({ a: n.getStart(sf), b: n.end, to }); needTerms = true
          return // do not look inside a template that was rewritten whole
        }
      }
    }
    T.forEachChild(n, visit)
  }
  visit(sf)
  if (!edits.length) return text
  edits.sort((a, b) => b.a - a.a)
  let res = text
  for (const e of edits) res = res.slice(0, e.a) + e.to + res.slice(e.b)
  if (needTerms && !/import \{[^}]*\bterms\b[^}]*\} from ["'][./]*\/terms["']/.test(res)) {
    let rel = relative(dirname(path), 'web/src/terms').split(sep).join('/')
    if (!rel.startsWith('.')) rel = './' + rel
    const m = /^import \{([^}]*)\} from ["']([./]*\/terms)["'];?$/m.exec(res)
    if (m) res = res.replace(m[0], `import {${m[1].trimEnd()}${m[1].trimEnd().endsWith(',') ? '' : ','} terms } from "${m[2]}";`)
    else {
      const imp = /^import [^\n]*;$/gm
      let last, mm
      while ((mm = imp.exec(res))) last = mm
      const line = `import { terms } from "${rel}";`
      res = last ? res.slice(0, last.index + last[0].length) + '\n' + line + res.slice(last.index + last[0].length) : line + '\n' + res
    }
  }
  return res
}
