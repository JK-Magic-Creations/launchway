import { useEffect, useMemo, useState } from "react";
import { browseApi, codeHref, rawUrl, type Entry } from "../../browseApi";
import { Markdown, type Context } from "../../markdown";
import { NameIcon } from "./icons";

/** resolve joins a relative address to the folder it is written in, inside the repository: null when it climbs out of it. */
export function resolveInside(dir: string, rel: string): string | null {
  const path = rel.split("#")[0].split("?")[0];
  if (path === "") return "";
  const parts = path.startsWith("/") ? [] : dir ? dir.split("/") : [];
  for (const p of path.split("/")) {
    if (p === "" || p === ".") continue;
    if (p === "..") { if (parts.length === 0) return null; parts.pop(); } else parts.push(p);
  }
  return parts.join("/");
}

export const READMES = /^readme(\.(md|markdown|txt))?$/i;
export const findReadme = (entries: Entry[]) => entries.find((e) => e.type === "file" && READMES.test(e.name));

/** The README of a folder, rendered below its tree. The text is turned into elements, never into HTML; a relative
 * link goes to the file in this repository, a relative picture is the repository's raw file inside an image. */
export function Readme({ repo, commit, entry, dir, refName }: { repo: string; commit: string; entry: Entry; dir: string; refName: string }) {
  const [text, setText] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    browseApi.read(repo, commit, entry.path).then((it) => { if (alive && it.encoding === "utf8") setText(it.content ?? ""); }).catch(() => {});
    return () => { alive = false; };
  }, [repo, commit, entry.path]);
  const ctx: Context = useMemo(() => ({
    link: (rel) => { if (rel.startsWith("#")) return rel; const p = resolveInside(dir, rel); return p === null ? null : codeHref(repo, refName, p || undefined) + (rel.includes("#") ? "#" + rel.split("#")[1] : ""); },
    image: (rel) => { const p = resolveInside(dir, rel); return p ? rawUrl(repo, commit, p, true) : null; },
  }), [repo, commit, dir, refName]);
  if (text === null) return null;
  return (
    <section className="card readme" aria-label={entry.name}>
      <header className="readme-head"><NameIcon name={entry.name} type="file" /><h2 className="h-sm mono">{entry.name}</h2></header>
      <div className="readme-body">{/\.txt$/i.test(entry.name) ? <pre className="code-plain">{text}</pre> : <Markdown text={text} ctx={ctx} />}</div>
    </section>
  );
}
