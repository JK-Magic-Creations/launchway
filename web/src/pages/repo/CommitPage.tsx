import { useEffect, useState } from "react";
import { browseApi, codeHref, commitHref, commitsHref, type CommitEntry, type RunBrief } from "../../browseApi";
import { Link } from "../../components/ui";
import { Status, stateOf } from "../../kit/Status";
import type { FileChange } from "../../pullsApi";
import { ChangedFiles } from "./ChangedFiles";
import { CopyButton, RepoFrame, when } from "./common";
import type { RunStatus } from "../../api";

/** One commit: its message, who made it, its parents, the runs started for it and the files it changed. */
export function CommitPage({ path, sha }: { path: string; sha: string }) {
  const [c, setC] = useState<CommitEntry | null>(null);
  const [runs, setRuns] = useState<RunBrief[]>([]);
  const [diff, setDiff] = useState<{ files: FileChange[]; total: number; additions: number; deletions: number; truncated: boolean } | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    browseApi.commit(path, sha).then((r) => {
      if (!alive) return;
      setC(r.commit); setRuns(r.runs);
      return browseApi.diff(path, r.commit.parent_shas[0] ?? "root", r.commit.sha).then((d) => alive && setDiff(d));
    }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [path, sha]);
  const [subject, ...rest] = (c?.message ?? "").split("\n");
  const body = rest.join("\n").trim();
  return (
    <RepoFrame path={path} tab="commits" title={c ? subject || "(no message)" : `Commit ${sha.slice(0, 8)}`} error={error}
      sentence={c ? <>{c.author} committed <time dateTime={c.authored_at}>{when(c.authored_at)}</time>.</> : undefined}
      actions={c && <><Link href={codeHref(path, c.sha)} className="btn">Browse files</Link><Link href={commitsHref(path, c.sha)} className="btn">History</Link></>}>
      {c && (
        <section className="card commit-head" aria-label="Commit">
          {body && <pre className="commit-body">{body}</pre>}
          <dl className="kvlist">
            <dt>Commit</dt><dd><span className="mono">{c.sha}</span> <CopyButton text={c.sha} what="the commit name" /></dd>
            <dt>Author</dt><dd>{c.author}, <time dateTime={c.authored_at}>{new Date(c.authored_at).toUTCString()}</time></dd>
            {c.committer !== c.author && <><dt>Committed by</dt><dd>{c.committer}, <time dateTime={c.committed_at}>{new Date(c.committed_at).toUTCString()}</time></dd></>}
            <dt>{c.parent_shas.length > 1 ? "Parents" : "Parent"}</dt>
            <dd>{c.parent_shas.length === 0 ? "None: the first commit" : c.parent_shas.map((p, i) => <span key={p}>{i > 0 && ", "}<Link href={commitHref(path, p)} className="mono">{p.slice(0, 8)}</Link></span>)}</dd>
          </dl>
        </section>
      )}
      {c && (
        <section className="card" aria-label="Runs for this commit">
          <h2 className="h-sm">Runs for this commit</h2>
          {runs.length === 0 ? <p className="faint small">No run was started for this commit.</p> : (
            <ul className="rowlist">
              {runs.map((r) => <li key={r.id}><Status state={stateOf(r.status as RunStatus)} /> <Link href={`/runs/${r.id}`}>{r.project} #{r.number}</Link> <span className="faint small">{r.pipeline}, {r.status}</span></li>)}
            </ul>
          )}
        </section>
      )}
      {c && diff && (
        <>
          <h2 className="h-sm">{diff.total} {diff.total === 1 ? "file" : "files"} changed <span className="pr-add">+{diff.additions}</span> <span className="pr-del">-{diff.deletions}</span></h2>
          <ChangedFiles repo={path} from={c.parent_shas[0] ?? "root"} to={c.sha} files={diff.files} total={diff.total} truncated={diff.truncated} />
        </>
      )}
      {!c && !error && <div className="loading">Loading…</div>}
    </RepoFrame>
  );
}
