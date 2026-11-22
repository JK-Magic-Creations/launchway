import { useEffect, useMemo, useState } from "react";
import { browseApi, blameHref, codeHref, commitHref, type Blame } from "../../browseApi";
import { Link } from "../../components/ui";
import type { Run } from "../../highlight";
import { parseRange } from "./FileView";
import { Breadcrumb, MsgLink, RepoFrame, useFetched, useQuery, when } from "./common";
import { BtnSm, DivLoading, SpanMono, ThCol } from "../../kit/bits";

/** Who last changed each line of a file. A long file is read a range at a time; the lines of one commit are
 * grouped, and "Blame before this change" opens the file as it was just before that commit. */
export function BlamePage({ path }: { path: string }) {
  const q = useQuery();
  const ref = q.get("ref") ?? "";
  const file = q.get("path") ?? "";
  const key = `${path}|${ref}|${file}`;
  const { data: first, error: loadError, set: setB } = useFetched<Blame>(key, () => browseApi.blame(path, ref, file));
  const [failed, setFailed] = useState<unknown>(null);
  const error = loadError ?? failed;
  const b = first;
  const [more, setMore] = useState(false);
  const [runs, setRuns] = useState<Run[][] | null>(null);
  const [hash, setHash] = useState(location.hash);
  useEffect(() => {
    const on = () => setHash(location.hash);
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  useEffect(() => {
    if (!b) return;
    let alive = true;
    import("../../highlight").then((m) => { const lang = m.languageOf(file); if (alive && lang) setRuns(m.highlight(b.lines.map((l) => l.text), lang)); });
    return () => { alive = false; };
  }, [b, file]);
  const loadMore = async () => {
    if (!b?.next) return;
    setMore(true);
    try {
      const r = await browseApi.blame(path, ref, file, b.next);
      setB({ ...b, lines: [...b.lines, ...r.lines], commits: { ...b.commits, ...r.commits }, to: r.to, next: r.next });
    } catch (e) { setFailed(e); } finally { setMore(false); }
  };
  const sel = parseRange(hash);
  // Each run of lines from one commit is a group; groups alternate in tone, and a group is shaded by how recent its
  // commit is among the commits of this file (the newest strongest), so what changed lately stands out.
  const rows = useMemo(() => {
    const times = Object.values(b?.commits ?? {}).map((c) => Date.parse(c.at)).filter((t) => t > 0);
    const lo = Math.min(...times), hi = Math.max(...times);
    let group = -1;
    const lines = b?.lines ?? [];
    return lines.map((l, i) => {
      const first = i === 0 || lines[i - 1].sha !== l.sha;
      let size = 1;
      if (first) { group++; while (i + size < lines.length && lines[i + size].sha === l.sha) size++; }
      const t = Date.parse(b!.commits[l.sha]?.at ?? "");
      const age = hi > lo && t > 0 ? Math.round(((t - lo) / (hi - lo)) * 100) / 100 : 0;
      return { l, first, size, odd: group % 2 === 1, age };
    });
  }, [b]);
  const shown = ref.length === 40 ? ref.slice(0, 10) : ref;
  return (
    <RepoFrame path={path} tab="code" title={`Blame of ${file.split("/").pop()}`} sentence={<>Who last changed each line of <SpanMono>{file}</SpanMono> at <SpanMono>{shown}</SpanMono>.</>} error={error}>
      <div className="code-bar">
        <Breadcrumb repo={path} refName={ref} path={file} />
        <span className="grow" />
        <Link href={codeHref(path, ref, file)} className="btn btn-sm">Normal view</Link>
      </div>
      {!b && !error && <DivLoading>Loading…</DivLoading>}
      {b && (
        <section className="card code-file" aria-label="Blame">
          <table className="lines blame" aria-label={`Who changed each line of ${file}`}>
            <thead className="sr-only"><tr><ThCol>Last changed by</ThCol><ThCol>Line</ThCol><ThCol>Code</ThCol></tr></thead>
            <tbody>
              {rows.map(({ l, first, size, odd, age }, i) => {
                const c = b.commits[l.sha];
                const on = sel && l.n >= sel[0] && l.n <= sel[1];
                return (
                  <tr key={l.n} id={`L${l.n}`} className={(first ? "bl-first " : "") + (odd ? "bl-odd " : "") + (on ? "sel" : "")}>
                    {first && (
                      <td className="bl-who" rowSpan={size} style={{ "--age": age } as React.CSSProperties}>{c && (
                        <div className="bl-group">
                          <span className="bl-line1"><Link href={commitHref(path, c.sha)} className="mono">{c.sha.slice(0, 8)}</Link><span className="bl-author">{c.author}</span><time className="faint" dateTime={c.at}>{when(c.at)}</time></span>
                          <span className="bl-line2"><MsgLink href={commitHref(path, c.sha)} text={c.subject} /></span>
                          {c.previous && <Link href={blameHref(path, c.previous, c.previous_path || file)} className="bl-prev">Blame before this change<span className="sr-only"> ({c.sha.slice(0, 8)})</span></Link>}
                        </div>
                      )}</td>
                    )}
                    <td className="ln"><a href={`#L${l.n}`} tabIndex={-1} aria-label={`Line ${l.n}`}>{l.n}</a></td>
                    <td className="src">{runs?.[i] ? runs[i].map((r, j) => r.k ? <span key={j} className={"hl-" + r.k}>{r.t}</span> : r.t) : l.text}{l.text === "" ? "\n" : ""}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {b.next ? <p className="code-more"><BtnSm disabled={more} onClick={loadMore}>{more ? "Loading…" : `Blame the next lines (${b.to} of ${b.total} so far)`}</BtnSm></p> : <p className="faint small code-note">{b.total} lines.</p>}
        </section>
      )}
    </RepoFrame>
  );
}
