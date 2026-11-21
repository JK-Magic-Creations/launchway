import { browseApi, commitHref, compareHref } from "../../browseApi";
import { Link } from "../../components/ui";
import { EmptyState } from "../../kit/layout";
import { Chip } from "../../kit/Status";
import { navigate } from "../../router";
import type { BranchComparison } from "../../pullsApi";
import { ChangedFiles } from "./ChangedFiles";
import { MsgLink, RefSwitcher, RepoFrame, useFetched, useQuery, useRepo } from "./common";

/** Two refs side by side: how far apart they are, the commits and the files, and the way to open a pull request. */
export function ComparePage({ path }: { path: string }) {
  const q = useQuery();
  const { repo } = useRepo(path);
  const head = q.get("head") ?? "";
  const base = q.get("base") ?? repo?.default_branch ?? "";
  const { data: c, error } = useFetched<BranchComparison>(`${path}|${head}|${base}`, head && base ? () => browseApi.compare(path, head, base) : null);
  const go = (h: string, b: string) => navigate(compareHref(path, h, b));
  return (
    <RepoFrame path={path} tab="commits" title="Compare" error={error}
      sentence={<>What <span className="mono">{head || "a branch"}</span> has that <span className="mono">{base || "another"}</span> lacks, and the reverse.</>}
      actions={<><span className="faint small">Base</span><RefSwitcher repo={path} current={base || "choose"} label="Base branch or tag" onPick={(b) => go(head, b)} />
        <span className="faint small">Compare</span><RefSwitcher repo={path} current={head || "choose"} label="Branch or tag to compare" onPick={(h) => go(h, base)} /></>}>
      {!head && <EmptyState title="Choose what to compare">Pick the branch with the changes above. The base is the branch it would be merged into.</EmptyState>}
      {head && !c && !error && <div className="loading">Comparing…</div>}
      {c && (
        <>
          <section className="card compare-sum" aria-label="Summary">
            <p><strong>{c.head}</strong> is <Chip tone={c.ahead ? "ok" : undefined}>{c.ahead} ahead</Chip> of and <Chip tone={c.behind ? "warn" : undefined}>{c.behind} behind</Chip> <strong>{c.base}</strong>. {c.changes.total} {c.changes.total === 1 ? "file" : "files"} changed, <span className="pr-add">+{c.changes.additions}</span> <span className="pr-del">-{c.changes.deletions}</span>.</p>
            <div className="actions">
              {c.existing_pull ? <Link href={`/repositories/${path}/pulls/${c.existing_pull}`} className="btn btn-primary">View pull request #{c.existing_pull}</Link>
                : c.ahead > 0 && c.head !== c.base ? <Link href={`/repositories/${path}/pulls/new?head=${encodeURIComponent(c.head)}&base=${encodeURIComponent(c.base)}`} className="btn btn-primary">Open a pull request</Link>
                  : <span className="faint small">{c.head === c.base ? "These are the same branch." : `${c.head} has nothing that ${c.base} lacks, so there is nothing to propose.`}</span>}
            </div>
          </section>
          {c.commits.length > 0 && (
            <section className="card" aria-label="Commits">
              <h2 className="h-sm">{c.commits.length} {c.commits.length === 1 ? "commit" : "commits"}</h2>
              <ul className="rowlist">{c.commits.map((x) => <li key={x.sha}><Link href={commitHref(path, x.sha)} className="mono">{x.sha.slice(0, 8)}</Link> <span className="main msg-cell"><MsgLink href={commitHref(path, x.sha)} text={x.subject} /></span> <span className="faint small">{x.author}</span></li>)}</ul>
            </section>
          )}
          {c.changes.files.length > 0 && <ChangedFiles repo={path} from={c.merge_base || c.base_sha || c.base} to={c.head_sha || c.head} files={c.changes.files} total={c.changes.total} />}
        </>
      )}
    </RepoFrame>
  );
}
