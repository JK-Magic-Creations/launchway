// A small syntax highlighter. It is its own chunk, loaded only when a file is opened, and it does one job:
// split each line into runs of comment, string, number, keyword and plain text. It builds data, never markup,
// so what a file contains cannot become HTML. It does not understand a language, only its lexical shapes.

export type Kind = "c" | "s" | "n" | "k" | "t" | "";
export interface Run { k: Kind; t: string }

interface Lang { line: string[]; block?: [string, string]; str: string; kw: Set<string>; tags?: boolean; multi?: string[] }
const words = (s: string) => new Set(s.split(" "));
const C = (kw: string, extra: Partial<Lang> = {}): Lang => ({ line: ["//"], block: ["/*", "*/"], str: "\"'`", kw: words(kw), ...extra });
const HASH = (kw: string, extra: Partial<Lang> = {}): Lang => ({ line: ["#"], str: "\"'", kw: words(kw), ...extra });
const JS = "const let var function return if else for while do switch case break continue new this class extends import export from default async await try catch finally throw typeof instanceof in of null undefined true false void delete interface type enum implements public private protected readonly static as";
const LANGS: Record<string, Lang> = {
  go: C("package import func return if else for range switch case default break continue go defer select chan map struct interface type var const fallthrough goto nil true false iota"),
  js: C(JS), ts: C(JS), jsx: C(JS), tsx: C(JS), mjs: C(JS), cjs: C(JS),
  java: C("package import class interface extends implements public private protected static final abstract void int long double float boolean char byte short new return if else for while do switch case default break continue try catch finally throw throws this super null true false enum instanceof synchronized"),
  c: C("auto break case char const continue default do double else enum extern float for goto if inline int long register return short signed sizeof static struct switch typedef union unsigned void volatile while NULL true false"),
  rs: C("fn let mut const static struct enum impl trait pub use mod crate self super match if else for while loop break continue return as in ref move async await dyn where type unsafe true false", { str: "\"" }),
  cs: C("namespace using class struct interface public private protected static void int long string bool new return if else for foreach while do switch case break continue try catch finally throw null true false var async await"),
  php: C("function class public private protected static return if else elseif foreach for while echo new use namespace null true false array", { line: ["//", "#"] }),
  py: HASH("def class return if elif else for while in not and or is import from as with try except finally raise pass break continue lambda None True False yield global nonlocal async await", { multi: ['"""', "'''"] }),
  rb: HASH("def class module end if elsif else unless while until for in do begin rescue ensure return yield require include nil true false self then case when"),
  sh: HASH("if then else elif fi for while do done case esac function in echo export local return exit set unset source true false"),
  yaml: HASH("true false null yes no"), yml: HASH("true false null yes no"), toml: HASH("true false"),
  mk: HASH("ifeq ifneq else endif include define endef"), dockerfile: HASH("FROM RUN CMD LABEL EXPOSE ENV ADD COPY ENTRYPOINT VOLUME USER WORKDIR ARG ONBUILD"),
  json: { line: [], str: "\"", kw: words("true false null") },
  sql: { line: ["--"], block: ["/*", "*/"], str: "'\"", kw: words("select from where insert into values update set delete create table drop alter index primary key not null and or join left right inner outer on group by order limit as in is distinct union all case when then else end") },
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
