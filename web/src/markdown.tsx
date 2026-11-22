import type { ReactNode } from "react";

// A small Markdown renderer for descriptions and comments. It builds React elements from the text, so nothing
// the author wrote is ever interpreted as HTML: a <script> tag in a description is shown as text. Links are
// kept only for http, https and mailto addresses, and open without handing the page to the target.

function safeHref(h: string): string | null {
  try {
    const u = new URL(h, "https://launchway.invalid/");
    return u.protocol === "http:" || u.protocol === "https:" || u.protocol === "mailto:" ? h : null;
  } catch { return null; }
}

/** An address the page's own code made for a relative link or picture must be on this site (a path, or a #fragment):
 * whatever Context hands back is checked once more here, so no scheme (javascript:, data:) and no other site can get through it. */
function onSite(a: string | null): string | null {
  return a !== null && /^(\/(?![/\\])|#)/.test(a) ? a : null;
}

/** Where a document's relative links and pictures point: a repository's README names files of the repository.
 * link turns "docs/a.md" into an address of the product or null (dropped); image does the same for a picture, whose
 * address is the repository's raw file, shown inside an image element and never as a page. */
export interface Context { link: (rel: string) => string | null; image: (rel: string) => string | null }

/** Inline Markdown: `code`, **bold**, *italic*, [text](address) and, in a repository document, ![alt](picture). */
export function inline(text: string, ctx?: Context): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /`([^`]+)`|\*\*([^*]+)\*\*|\*([^*\s][^*]*)\*|(!?)\[([^\]]*)\]\(([^)\s]+)\)/g;
  let last = 0, k = 0, m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    if (m[1] !== undefined) out.push(<code key={k++}>{m[1]}</code>);
    else if (m[2] !== undefined) out.push(<strong key={k++}>{m[2]}</strong>);
    else if (m[3] !== undefined) out.push(<em key={k++}>{m[3]}</em>);
    else if (m[4] === "!") {
      const src = ctx && !/^[a-z][a-z0-9+.-]*:|^\/\//i.test(m[6]) ? onSite(ctx.image(m[6])) : null;
      out.push(src ? <img key={k++} src={src} alt={m[5]} loading="lazy" referrerPolicy="no-referrer" className="md-img" /> : m[5] ? `[picture: ${m[5]}]` : "");
    } else {
      const external = safeHref(m[6]);
      const rel = ctx && !/^[a-z][a-z0-9+.-]*:|^\/\//i.test(m[6]) ? onSite(ctx.link(m[6])) : null;
      if (rel) out.push(<a key={k++} href={rel}>{m[5]}</a>);
      else out.push(external && /^(https?:|mailto:)/i.test(m[6]) ? <a key={k++} href={external} rel="noopener noreferrer nofollow" target="_blank">{m[5]}</a> : m[5]);
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** The cells of a table row: split at the pipes that are not escaped (\|) or inside `code`; the pipes at the edges are not cells. */
function cells(line: string): string[] {
  const out: string[] = [];
  let cur = "", code = false;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (c === "\\" && line[i + 1] === "|") { cur += "|"; i++; continue; }
    if (c === "`") code = !code;
    if (c === "|" && !code) { out.push(cur); cur = ""; continue; }
    cur += c;
  }
  out.push(cur);
  if (out[0].trim() === "") out.shift();
  if (out.length && out[out.length - 1].trim() === "") out.pop();
  return out.map((c) => c.trim());
}

const DIVIDER = /^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$/;

/** A table starts at a row with a pipe whose next line is the divider row (--- or :---: between pipes), with as many cells. */
function tableAt(lines: string[], i: number): boolean {
  return lines[i].includes("|") && i + 1 < lines.length && lines[i + 1].includes("-") && DIVIDER.test(lines[i + 1]) && cells(lines[i]).length === cells(lines[i + 1]).length;
}

/** Markdown blocks: paragraphs, headings, lists, quotes, tables and fenced code (a suggestion block is shown as a suggestion). */
export function Markdown({ text, ctx }: { text: string; ctx?: Context }) {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const blocks: ReactNode[] = [];
  let i = 0, k = 0;
  while (i < lines.length) {
    const line = lines[i];
    const fence = /^```(\w*)\s*$/.exec(line);
    if (fence) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^```\s*$/.test(lines[i])) body.push(lines[i++]);
      i++;
      blocks.push(fence[1] === "suggestion"
        ? <div key={k++} className="md-suggestion"><div className="md-suggestion-head">Suggested change</div><pre><code>{body.join("\n")}</code></pre></div>
        : <pre key={k++}><code>{body.join("\n")}</code></pre>);
    } else if (/^#{1,6}\s/.test(line)) {
      const level = Math.min(line.match(/^#+/)![0].length + 2, 6);
      const Tag = `h${level}` as "h3";
      blocks.push(<Tag key={k++}>{inline(line.replace(/^#+\s+/, ""), ctx)}</Tag>);
      i++;
    } else if (/^\s*[-*]\s+/.test(line) || /^\s*\d+\.\s+/.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: ReactNode[] = [];
      while (i < lines.length && (ordered ? /^\s*\d+\.\s+/.test(lines[i]) : /^\s*[-*]\s+/.test(lines[i]))) {
        items.push(<li key={items.length}>{inline(lines[i].replace(/^\s*(?:[-*]|\d+\.)\s+/, ""), ctx)}</li>);
        i++;
      }
      blocks.push(ordered ? <ol key={k++}>{items}</ol> : <ul key={k++}>{items}</ul>);
    } else if (/^>\s?/.test(line)) {
      const q: string[] = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) q.push(lines[i++].replace(/^>\s?/, ""));
      blocks.push(<blockquote key={k++}>{inline(q.join(" "), ctx)}</blockquote>);
    } else if (tableAt(lines, i)) {
      const head = cells(line);
      const align = cells(lines[i + 1]).map((d) => (d.startsWith(":") && d.endsWith(":") ? "md-center" : d.endsWith(":") ? "md-right" : d.startsWith(":") ? "md-left" : undefined));
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && lines[i].trim() !== "" && lines[i].includes("|")) rows.push(cells(lines[i++]));
      blocks.push(
        // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- a scrolling region must be reachable with the keyboard
        <div key={k++} className="md-table" role="region" aria-label={`Table: ${head.filter(Boolean).join(", ") || "rows"}`} tabIndex={0}>
          <table>
            <thead><tr>{head.map((h, c) => <th key={c} scope="col" className={align[c]}>{inline(h, ctx)}</th>)}</tr></thead>
            <tbody>{rows.map((r, ri) => <tr key={ri}>{head.map((_, c) => <td key={c} className={align[c]}>{inline(r[c] ?? "", ctx)}</td>)}</tr>)}</tbody>
          </table>
        </div>);
    } else if (line.trim() === "") {
      i++;
    } else {
      const p: string[] = [];
      while (i < lines.length && lines[i].trim() !== "" && !/^(```|#{1,6}\s|>\s?|\s*[-*]\s+|\s*\d+\.\s+)/.test(lines[i]) && !tableAt(lines, i)) p.push(lines[i++]);
      blocks.push(<p key={k++}>{inline(p.join(" "), ctx)}</p>);
    }
  }
  return <div className="md">{blocks}</div>;
}
