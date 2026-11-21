import { useEffect, useId, useState } from "react";
import { archiveApi, type ArchivePage, type ArchivedIssue } from "../archive";
import { Empty, ErrorBox } from "../components/ui";
import "../styles/archive.css";

// The Imported history tab of a repository: the issues and comments brought
// from another server, read-only. Everything here was written by other people,
// so it is drawn as text, never as markup. It is its own chunk: nothing of it
// is in the first download.

const day = (s?: string) => (s ? s.slice(0, 10) : "");

export default function ImportedHistory({ path }: { path: string }) {
  const [state, setState] = useState("all");
  const [text, setText] = useState("");
  const [typed, setTyped] = useState("");
  const [page, setPage] = useState(1);
  const [data, setData] = useState<ArchivePage | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [open, setOpen] = useState<number | null>(null);
  const searchId = useId();

  useEffect(() => {
    let live = true;
    archiveApi.list(path, { state, text, page })
      .then((d) => { if (live) { setData(d); setError(null); } })
      .catch((e) => { if (live) { setError(e); setData(null); } });
    return () => { live = false; };
  }, [path, state, text, page]);

  if (open !== null) return <Detail path={path} number={open} onBack={() => setOpen(null)} />;

  const pages = data ? Math.max(1, Math.ceil(data.total / Math.max(data.per_page, 1))) : 1;
  const search = (e: React.FormEvent) => { e.preventDefault(); setPage(1); setText(typed); };
  return (
    <section aria-labelledby="imported-history-h">
      <h2 className="h-sm" id="imported-history-h">Imported history</h2>
      <p className="sub">Issues and comments brought from another server. This is a read-only record: nothing here can be changed.</p>
      <form className="inputs-form" role="search" aria-label="Search imported issues" onSubmit={search}>
        <label className="field" htmlFor={searchId}><span>Search titles, text and comments</span>
          <input id={searchId} type="search" value={typed} onChange={(e) => setTyped(e.target.value)} maxLength={200} />
        </label>
        <label className="field"><span>Show</span>
          <select value={state} onChange={(e) => { setPage(1); setState(e.target.value); }}>
            <option value="all">All</option>
            <option value="open">Open{data ? ` (${data.open})` : ""}</option>
            <option value="closed">Closed{data ? ` (${data.closed})` : ""}</option>
          </select>
        </label>
        <div className="actions"><button type="submit" className="btn">Search</button></div>
      </form>
      <ErrorBox error={error} />
      {!data && !error && <div className="loading">Loading…</div>}
      {data && data.total === 0 && <Empty title="No imported issues">{text || state !== "all" ? "Nothing matches. Clear the search or show all." : "Nothing has been imported into this repository. An administrator can run launchway repos import-archive."}</Empty>}
      {data && data.total > 0 && (
        <>
          <div className="card card-flush tablewrap">
            <table className="table">
              <caption className="archive-sr">Imported issues, newest first</caption>
              <thead><tr><th scope="col">Number</th><th scope="col">Title</th><th scope="col">State</th><th scope="col">Author</th><th scope="col">Comments</th><th scope="col">Opened</th></tr></thead>
              <tbody>
                {data.items.map((i) => (
                  <tr key={i.number}>
                    <td className="nowrap">#{i.number}</td>
                    <td><button type="button" className="btn btn-ghost archive-title" onClick={() => setOpen(i.number)}>{i.title || "(no title)"}</button>{i.is_pull_request && <span className="faint"> (pull request record)</span>}</td>
                    <td>{i.state}</td>
                    <td>{i.author}</td>
                    <td>{i.comment_count}</td>
                    <td className="nowrap">{day(i.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <nav aria-label="Pages of imported issues" className="actions archive-pages">
            <button type="button" className="btn" disabled={page <= 1} onClick={() => setPage(page - 1)}>Newer</button>
            <span aria-live="polite">Page {page} of {pages}, {data.total} issues</span>
            <button type="button" className="btn" disabled={page >= pages} onClick={() => setPage(page + 1)}>Older</button>
          </nav>
        </>
      )}
    </section>
  );
}

function Detail({ path, number, onBack }: { path: string; number: number; onBack: () => void }) {
  const [issue, setIssue] = useState<ArchivedIssue | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let live = true;
    archiveApi.get(path, number).then((i) => { if (live) setIssue(i); }).catch((e) => { if (live) setError(e); });
    return () => { live = false; };
  }, [path, number]);
  return (
    <article aria-labelledby="imported-issue-h">
      <button type="button" className="btn" onClick={onBack}>Back to the list</button>
      <ErrorBox error={error} />
      {!issue && !error && <div className="loading">Loading…</div>}
      {issue && (
        <>
          <h2 className="h-sm" id="imported-issue-h">#{issue.number} {issue.title}</h2>
          <p className="sub">
            {issue.state}, opened by {issue.author}{issue.created_at ? ` on ${day(issue.created_at)}` : ""}{issue.closed_at ? `, closed ${day(issue.closed_at)}` : ""}.
            {issue.labels.length > 0 && <> Labels: {issue.labels.join(", ")}.</>}
            {issue.milestone && <> Milestone: {issue.milestone}.</>}
            {issue.assignees.length > 0 && <> Assigned to {issue.assignees.join(", ")}.</>}
            {issue.url && <> It was at <span className="mono">{issue.url}</span>.</>}
          </p>
          <pre className="card imported-text">{issue.body || "(no text)"}</pre>
          <h3 className="h-sm">{issue.comments?.length ?? 0} comments</h3>
          <ol className="stack" aria-label="Comments">
            {(issue.comments ?? []).map((c, n) => (
              <li key={c.id || n} className="card">
                <p className="sub">{c.author}{c.created_at ? `, ${day(c.created_at)}` : ""}</p>
                <pre className="imported-text">{c.body}</pre>
              </li>
            ))}
          </ol>
        </>
      )}
    </article>
  );
}
