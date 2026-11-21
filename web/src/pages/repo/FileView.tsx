import { useEffect, useMemo, useRef, useState } from "react";
import { blameHref, codeHref, commitsHref, rawUrl, type Item } from "../../browseApi";
import { Chip } from "../../kit/Status";
import type { Run } from "../../highlight";
import { CopyButton, IconAction } from "./common";
import { NameIcon } from "./icons";

const IMAGES = /\.(png|jpe?g|gif|webp|avif|svg)$/i;
const CHUNK = 3000;

/** parseRange reads #L10 or #L10-L20. */
export function parseRange(hash: string): [number, number] | null {
  const m = /^#L(\d+)(?:-L?(\d+))?$/.exec(hash);
  if (!m) return null;
  const a = Number(m[1]), b = m[2] ? Number(m[2]) : a;
  return [Math.min(a, b), Math.max(a, b)];
}

function size(n?: number): string {
  if (n === undefined) return "";
  return n < 1024 ? `${n} bytes` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KiB` : `${(n / (1 << 20)).toFixed(1)} MiB`;
}

/** Lines of text with line numbers, linkable ranges and syntax colors from the lazy highlighter. */
export function CodeLines({ text, path, hash, onHash }: { text: string; path: string; hash: string; onHash: (h: string) => void }) {
  const lines = useMemo(() => { const l = text.split("\n"); if (l.length > 1 && l[l.length - 1] === "") l.pop(); return l; }, [text]);
  const sel = parseRange(hash);
  const [shown, setShown] = useState(CHUNK);
  const [runs, setRuns] = useState<Run[][] | null>(null);
  const scrolled = useRef("");
  useEffect(() => {
    let alive = true;
    import("../../highlight").then((m) => { const lang = m.languageOf(path); if (alive && lang) setRuns(m.highlight(lines, lang)); });
    return () => { alive = false; };
  }, [lines, path]);
  const need = sel ? sel[1] : 0;
  const count = Math.min(lines.length, Math.max(shown, need));
  useEffect(() => {
    if (sel && scrolled.current !== hash) { scrolled.current = hash; document.getElementById(`L${sel[0]}`)?.scrollIntoView({ block: "center" }); }
  });
  const pick = (n: number, e: React.MouseEvent) => {
    e.preventDefault();
    const from = e.shiftKey && sel ? sel[0] : n;
    const a = Math.min(from, n), b = Math.max(from, n);
    onHash(a === b ? `#L${a}` : `#L${a}-L${b}`);
  };
  return (
    <>
      <table className="lines" aria-label={`Contents of ${path}`}>
        <tbody>
          {lines.slice(0, count).map((l, i) => {
            const n = i + 1;
            const on = sel && n >= sel[0] && n <= sel[1];
            return (
              <tr key={n} id={`L${n}`} className={on ? "sel" : undefined}>
                <td className="ln"><a href={`#L${n}`} tabIndex={-1} aria-label={`Line ${n}`} onClick={(e) => pick(n, e)}>{n}</a></td>
                <td className="src">{runs ? runs[i].map((r, j) => r.k ? <span key={j} className={"hl-" + r.k}>{r.t}</span> : r.t) : l}{l === "" ? "\n" : ""}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {count < lines.length && <p className="code-more"><button type="button" className="btn btn-sm" onClick={() => setShown(count + CHUNK)}>Show the next {Math.min(CHUNK, lines.length - count)} of {lines.length - count} lines</button></p>}
    </>
  );
}

/** What a file is, shown as what it is: text with lines, a picture, a binary, a link or a submodule. */
export function FileView({ repo, item, canWrite, hash, onHash, onEdit, onDelete }: {
  repo: string; item: Item; canWrite: boolean; hash: string; onHash: (h: string) => void; onEdit: () => void; onDelete: () => void;
}) {
  const name = item.path.split("/").pop()!;
  const isText = item.type === "file" && item.encoding === "utf8" && item.content !== undefined;
  const lineCount = isText ? item.content!.split("\n").length - (item.content!.endsWith("\n") ? 1 : 0) : 0;
  const permalink = `${location.origin}${codeHref(repo, item.commit, item.path)}${hash}`;
  const [jump, setJump] = useState("");
  const body = () => {
    if (item.type === "submodule") return <p className="code-note">This is a submodule: it points at commit <span className="mono">{item.sha}</span> of another repository. Launchway shows what this repository holds, not what that one does.</p>;
    if (item.type === "symlink") return <p className="code-note">A symbolic link to <span className="mono">{item.content}</span>.</p>;
    if (item.truncated) return <p className="code-note">This file is {size(item.size)}, too large to show here. <a href={rawUrl(repo, item.commit, item.path)} download>Download it</a>, or clone the repository.</p>;
    if (IMAGES.test(name)) return <div className="code-image"><img src={rawUrl(repo, item.commit, item.path, true)} alt={name} /></div>;
    if (item.encoding === "base64") return <p className="code-note">This file is not text ({size(item.size)}), so there is nothing to show. <a href={rawUrl(repo, item.commit, item.path)} download>Download it</a>.</p>;
    if (item.size === 0) return <p className="code-note">This file is empty.</p>;
    return <CodeLines text={item.content ?? ""} path={item.path} hash={hash} onHash={onHash} />;
  };
  return (
    <section className="card code-file" aria-label={`File ${name}`}>
      <header className="code-file-head">
        <NameIcon name={name} type={item.type} />
        <h2 className="h-sm mono">{name}</h2>
        <span className="faint small file-meta">{isText ? `${lineCount} lines · ` : ""}{size(item.size)}{item.mode === "100755" ? " · executable" : ""}</span>
        {item.type === "symlink" && <Chip>link</Chip>}{item.type === "submodule" && <Chip>submodule</Chip>}
        <span className="grow" />
        {isText && (
          <form className="jump" onSubmit={(e) => { e.preventDefault(); const m = /^\s*(\d+)(?:\s*-\s*(\d+))?\s*$/.exec(jump); if (m) onHash(m[2] ? `#L${m[1]}-L${m[2]}` : `#L${m[1]}`); }}>
            <label><span className="sr-only">Jump to line</span><input value={jump} onChange={(e) => setJump(e.target.value)} placeholder="Line, or 10-20" aria-label="Jump to line" inputMode="numeric" size={10} /></label>
          </form>
        )}
        <div className="file-actions" role="group" aria-label="Actions for this file">
          <CopyButton text={item.path} what="the path" label="Copy path" icon="copy" />
          <CopyButton text={permalink} what="the permalink to this commit" label="Copy permalink" icon="link" />
          {item.type === "file" && <IconAction icon="raw" label="Raw" href={rawUrl(repo, item.commit, item.path)} download={name} />}
          {isText && <IconAction icon="blame" label="Blame" href={blameHref(repo, item.ref, item.path)} />}
          <IconAction icon="history" label="History" href={commitsHref(repo, item.ref, item.path)} />
          {canWrite && item.type === "file" && <IconAction icon="edit" label="Edit" onClick={onEdit} />}
          {canWrite && item.type === "file" && <IconAction icon="delete" label="Delete" onClick={onDelete} />}
        </div>
      </header>
      {body()}
    </section>
  );
}
