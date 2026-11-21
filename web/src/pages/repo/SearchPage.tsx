import { useState } from "react";
import { browseApi, codeHref, type FileMatches, type SearchResult } from "../../browseApi";
import { Link } from "../../components/ui";
import { Button } from "../../kit/layout";
import { navigate } from "../../router";
import { RefSwitcher, RepoFrame, useFetched, useQuery, useRepo } from "./common";

function Snippet({ text, ranges }: { text: string; ranges?: [number, number][] }) {
  const chars = [...text];
  const out: React.ReactNode[] = [];
  let at = 0;
  (ranges ?? []).forEach(([a, b], i) => {
    if (a < at) return;
    if (a > at) out.push(chars.slice(at, a).join(""));
    out.push(<mark key={i}>{chars.slice(a, b).join("")}</mark>);
    at = b;
  });
  if (at < chars.length) out.push(chars.slice(at).join(""));
  return <code>{out}</code>;
}

/** Search inside one repository at one ref: text or a regular expression, in file contents or paths, narrowed by
 * folder and extension. It stops at its bounds and says so. */
export function SearchPage({ path }: { path: string }) {
  const qp = useQuery();
  const { repo } = useRepo(path);
  const urlQ = qp.get("q") ?? "";
  const ref = qp.get("ref") ?? undefined;
  const [q, setQ] = useState(urlQ);
  const [regex, setRegex] = useState(qp.get("regex") === "1");
  const [matchCase, setCase] = useState(qp.get("case") === "1");
  const [where, setWhere] = useState(qp.get("in") ?? "content");
  const [dir, setDir] = useState(qp.get("dir") ?? "");
  const [ext, setExt] = useState(qp.get("ext") ?? "");
  const current = ref ?? repo?.default_branch ?? "main";
  const address = (r: string | undefined) => {
    const p = new URLSearchParams();
    if (q.trim()) p.set("q", q.trim());
    if (r) p.set("ref", r);
    if (regex) p.set("regex", "1");
    if (matchCase) p.set("case", "1");
    if (where !== "content") p.set("in", where);
    if (dir) p.set("dir", dir);
    if (ext) p.set("ext", ext);
    return `/repositories/${path}/search?${p}`;
  };
  const key = `${path}|${qp.toString()}`;
  const { data: res, error } = useFetched<SearchResult>(key, urlQ ? () => browseApi.search(path, { q: urlQ, ref, regex: qp.get("regex") === "1", matchCase: qp.get("case") === "1", in: qp.get("in") ?? undefined, dir: qp.get("dir") ?? undefined, ext: qp.get("ext") ?? undefined }) : null);
  const busy = !!urlQ && !res && !error;
  const submit = (e: React.FormEvent) => { e.preventDefault(); if (q.trim()) navigate(address(ref)); };
  const fileHref = (f: FileMatches, line?: number) => codeHref(path, res?.commit ?? current, f.path) + (line ? `#L${line}` : "");
  return (
    <RepoFrame path={path} tab="search" title="Search the code" sentence={<>Looks through the files of <span className="mono">{current}</span> in this repository. It reads the repository itself, so a result is never older than the last push.</>} error={error}
      actions={<RefSwitcher repo={path} current={current} onPick={(r) => navigate(address(r))} />}>
      <form className="card search-form" role="search" aria-label="Search this repository" onSubmit={submit}>
        <label className="field grow"><span>Search for</span><input type="search" value={q} onChange={(e) => setQ(e.target.value)} required placeholder={regex ? "A regular expression, such as func (Get|Put)" : "Text to find"} /></label>
        <label className="field"><span>Look in</span>
          <select value={where} onChange={(e) => setWhere(e.target.value)}><option value="content">File contents</option><option value="path">File names and paths</option><option value="both">Both</option></select></label>
        <label className="field"><span>Folder</span><input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="whole repository" /></label>
        <label className="field"><span>Extensions</span><input value={ext} onChange={(e) => setExt(e.target.value)} placeholder="go, ts" /></label>
        <label className="check"><input type="checkbox" checked={regex} onChange={(e) => setRegex(e.target.checked)} /> Regular expression</label>
        <label className="check"><input type="checkbox" checked={matchCase} onChange={(e) => setCase(e.target.checked)} /> Match case</label>
        <Button kind="pri" type="submit" disabled={busy || !q.trim()}>{busy ? "Searching…" : "Search"}</Button>
      </form>
      <div aria-live="polite">
        {res && (
          <p className="code-note" role="status">
            {res.matches === 0 ? `Nothing found for “${urlQ}”.` : `${res.matches} ${res.matches === 1 ? "match" : "matches"} in ${res.files.length} ${res.files.length === 1 ? "file" : "files"}`} <span className="faint small">({res.millis} ms)</span>
            {res.partial && <strong className="partial"> Partial results: {res.reason}.</strong>}
          </p>
        )}
      </div>
      {res && res.files.map((f) => (
        <section key={f.path} className="card search-file" aria-label={f.path}>
          <h2 className="h-sm mono"><Link href={fileHref(f)}>{f.path}</Link></h2>
          <ul className="search-lines">
            {f.matches.map((m) => (
              <li key={m.line}><Link href={fileHref(f, m.line)} className="ln-link"><span className="sr-only">Line </span>{m.line}</Link><Snippet text={m.text} ranges={m.ranges} /></li>
            ))}
          </ul>
        </section>
      ))}
      {res && res.matches === 0 && <p className="faint small code-note">Try fewer words, turn off “Match case”, or search file names instead. Search covers text files; binary files are skipped.</p>}
    </RepoFrame>
  );
}
