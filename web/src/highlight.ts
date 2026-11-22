
import { prose } from "./prose";
const P = await prose("highlight");// A small syntax highlighter. It is its own chunk, loaded only when a file is opened, and it does one job:
// split each line into runs of comment, string, number, keyword and plain text. It builds data, never markup,
// so what a file contains cannot become HTML. It does not understand a language, only its lexical shapes.

export type Kind = "c" | "s" | "n" | "k" | "t" | "";
export interface Run { k: Kind; t: string }

interface Lang { line: string[]; block?: [string, string]; str: string; kw: Set<string>; tags?: boolean; multi?: string[] }
const words = (s: string) => new Set(s.split(" "));
const C = (kw: string, extra: Partial<Lang> = {}): Lang => ({ line: ["//"], block: ["/*", "*/"], str: "\"'`", kw: words(kw), ...extra });
const HASH = (kw: string, extra: Partial<Lang> = {}): Lang => ({ line: ["#"], str: "\"'", kw: words(kw), ...extra });
const JS = P.t0;
const LANGS: Record<string, Lang> = {
  go: C(P.t1),
  js: C(JS), ts: C(JS), jsx: C(JS), tsx: C(JS), mjs: C(JS), cjs: C(JS),
  java: C(P.t2),
  c: C(P.t3),
  rs: C(P.t4, { str: "\"" }),
  cs: C(P.t5),
  php: C(P.t6, { line: ["//", "#"] }),
  py: HASH(P.t7, { multi: ['"""', "'''"] }),
  rb: HASH(P.t8),
  sh: HASH(P.t9),
  yaml: HASH("true false null yes no"), yml: HASH("true false null yes no"), toml: HASH("true false"),
  mk: HASH(P.t10), dockerfile: HASH(P.t11),
  json: { line: [], str: "\"", kw: words("true false null") },
  sql: { line: ["--"], block: ["/*", "*/"], str: "'\"", kw: words(P.t12) },
  css: { line: [], block: ["/*", "*/"], str: "\"'", kw: words("important") },
  html: { line: [], block: ["<!--", "-->"], str: "\"'", kw: words(""), tags: true }, xml: { line: [], block: ["<!--", "-->"], str: "\"'", kw: words(""), tags: true },
  svg: { line: [], block: ["<!--", "-->"], str: "\"'", kw: words(""), tags: true },
};
const ALIAS: Record<string, string> = { bash: "sh", zsh: "sh", h: "c", cc: "c", cpp: "c", hpp: "c", kt: "java", swift: "java", scala: "java", makefile: "mk", gnumakefile: "mk", htm: "html", vue: "html", tf: "toml", ini: "toml", conf: "toml", scss: "css", less: "css" };

/** languageOf names the language of a file by its name, or "" when it is shown plain. */
export function languageOf(path: string): string {
  const base = path.split("/").pop()!.toLowerCase();
  const ext = base.includes(".") ? base.split(".").pop()! : base;
  const l = ALIAS[ext] ?? ext;
  return LANGS[l] ? l : "";
}

const ident = /[A-Za-z_$][\w$]*/y;
const num = /(?:0[xX][0-9a-fA-F_]+|\d[\d_]*(?:\.\d+)?(?:[eE][+-]?\d+)?)/y;

/** highlight splits lines into runs. State (an open block comment, a long string) carries from line to line. */
export function highlight(lines: string[], lang: string): Run[][] {
  const L = LANGS[lang];
  if (!L) return lines.map((t) => [{ k: "", t }]);
  let block = false, multi = "";
  return lines.map((line) => {
    const out: Run[] = [];
    const push = (k: Kind, t: string) => { if (!t) return; const last = out[out.length - 1]; if (last && last.k === k) last.t += t; else out.push({ k, t }); };
    let i = 0;
    while (i < line.length) {
      if (block) {
        const e = line.indexOf(L.block![1], i);
        if (e < 0) { push("c", line.slice(i)); i = line.length; } else { push("c", line.slice(i, e + L.block![1].length)); i = e + L.block![1].length; block = false; }
        continue;
      }
      if (multi) {
        const e = line.indexOf(multi, i);
        if (e < 0) { push("s", line.slice(i)); i = line.length; } else { push("s", line.slice(i, e + 3)); i = e + 3; multi = ""; }
        continue;
      }
      const rest = line.slice(i);
      const lc = L.line.find((p) => rest.startsWith(p));
      if (lc) { push("c", rest); break; }
      if (L.block && rest.startsWith(L.block[0])) { block = true; push("c", L.block[0]); i += L.block[0].length; continue; }
      const m = L.multi?.find((p) => rest.startsWith(p));
      if (m) { multi = m; push("s", m); i += 3; continue; }
      const ch = line[i];
      if (L.tags && (ch === "<" || ch === ">")) {
        const e = ch === "<" ? line.indexOf(">", i) : i;
        const t = line.slice(i, e < 0 ? line.length : e + 1);
        push("t", t); i += t.length; continue;
      }
      if (L.str.includes(ch)) {
        let j = i + 1;
        while (j < line.length && line[j] !== ch) j += line[j] === "\\" ? 2 : 1;
        push("s", line.slice(i, Math.min(j + 1, line.length))); i = j + 1; continue;
      }
      ident.lastIndex = i; num.lastIndex = i;
      const w = ident.exec(line);
      if (w && w.index === i) { push(L.kw.has(w[0]) ? "k" : "", w[0]); i += w[0].length; continue; }
      const n = num.exec(line);
      if (n && n.index === i) { push("n", n[0]); i += n[0].length; continue; }
      push("", ch); i++;
    }
    return out;
  });
}
