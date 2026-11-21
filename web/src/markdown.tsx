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

/** Inline Markdown: `code`, **bold**, *italic* and [text](address). */
export function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /`([^`]+)`|\*\*([^*]+)\*\*|\*([^*\s][^*]*)\*|\[([^\]]+)\]\(([^)\s]+)\)/g;
  let last = 0, k = 0, m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    if (m[1] !== undefined) out.push(<code key={k++}>{m[1]}</code>);
    else if (m[2] !== undefined) out.push(<strong key={k++}>{m[2]}</strong>);
    else if (m[3] !== undefined) out.push(<em key={k++}>{m[3]}</em>);
    else {
      const href = safeHref(m[5]);
      out.push(href ? <a key={k++} href={href} rel="noopener noreferrer nofollow" target="_blank">{m[4]}</a> : m[4]);
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** Markdown blocks: paragraphs, headings, lists, quotes and fenced code (a suggestion block is shown as a suggestion). */
export function Markdown({ text }: { text: string }) {
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
      blocks.push(<Tag key={k++}>{inline(line.replace(/^#+\s+/, ""))}</Tag>);
      i++;
    } else if (/^\s*[-*]\s+/.test(line) || /^\s*\d+\.\s+/.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: ReactNode[] = [];
      while (i < lines.length && (ordered ? /^\s*\d+\.\s+/.test(lines[i]) : /^\s*[-*]\s+/.test(lines[i]))) {
        items.push(<li key={items.length}>{inline(lines[i].replace(/^\s*(?:[-*]|\d+\.)\s+/, ""))}</li>);
        i++;
      }
      blocks.push(ordered ? <ol key={k++}>{items}</ol> : <ul key={k++}>{items}</ul>);
    } else if (/^>\s?/.test(line)) {
      const q: string[] = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) q.push(lines[i++].replace(/^>\s?/, ""));
      blocks.push(<blockquote key={k++}>{inline(q.join(" "))}</blockquote>);
    } else if (line.trim() === "") {
      i++;
    } else {
      const p: string[] = [];
      while (i < lines.length && lines[i].trim() !== "" && !/^(```|#{1,6}\s|>\s?|\s*[-*]\s+|\s*\d+\.\s+)/.test(lines[i])) p.push(lines[i++]);
      blocks.push(<p key={k++}>{inline(p.join(" "))}</p>);
    }
  }
  return <div className="md">{blocks}</div>;
}
