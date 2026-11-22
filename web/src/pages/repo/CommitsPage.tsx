import { useEffect, useState } from "react";
import { browseApi, codeHref, commitHref, commitsHref, type CommitEntry } from "../../browseApi";
import { Link } from "../../components/ui";
import { EmptyState } from "../../kit/layout";
import { navigate } from "../../router";
import { SignatureBadge, useSignatures } from "../../components/Signature";
import { Breadcrumb, CopyButton, IconAction, MsgLink, RefSwitcher, RepoFrame, useQuery, useRepo, when } from "./common";
import { BtnSm, DivLoading, SpanMono } from "../../kit/bits";

/** The commits of a branch, tag or commit, or only those that changed one path (a file's history follows renames). */
export function CommitsPage({ path }: { path: string }) {
  const q = useQuery();
  const { repo } = useRepo(path);
  const ref = q.get("ref") ?? undefined;
  const p = q.get("path") ?? "";
  const [limit, setLimit] = useState(50);
  const [items, setItems] = useState<CommitEntry[] | null>(null);
  const [shownRef, setShownRef] = useState(ref ?? "");
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    browseApi.commits(path, ref, p || undefined, limit).then((r) => { if (alive) { setItems(r.items); setShownRef(r.ref); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [path, ref, p, limit]);
  const sigs = useSignatures(path, (items ?? []).map((c) => c.sha));
  const current = shownRef || repo?.default_branch || "main";
  return (
    <RepoFrame path={path} tab="commits" title={p ? `History of ${p.split("/").pop()}` : "Commits"} error={error}
      sentence={p ? <>The commits that changed <SpanMono>{p}</SpanMono>{" "}on <SpanMono>{current}</SpanMono>.</> : <>The newest commits on <SpanMono>{current}</SpanMono>.</>}
      actions={<RefSwitcher repo={path} current={current} onPick={(r) => navigate(commitsHref(path, r, p || undefined))} />}>
      {p && <div className="code-bar"><Breadcrumb repo={path} refName={current} path={p} /></div>}
      {items && items.length === 0 && <EmptyState title="No commits here">{p ? `Nothing named ${p} has been changed on ${current}.` : "This branch has no commits yet."}</EmptyState>}
      {items && items.length > 0 && (
        <section className="card" aria-label="Commits">
          <ul className="commit-list">
            {items.map((c) => (
              <li key={c.sha}>
                <span><MsgLink href={commitHref(path, c.sha)} text={c.subject || "(no message)"} /><SignatureBadge result={sigs[c.sha]} quiet /></span>
                <span className="commit-who faint small">{c.author}{c.committer !== c.author ? ` (committed by ${c.committer})` : ""}</span>
                <time className="commit-when faint small" dateTime={c.authored_at}>{when(c.authored_at)}{c.parents > 1 ? ", a merge" : ""}</time>
                <span className="commit-side">
                  <Link href={commitHref(path, c.sha)} className="mono">{c.sha.slice(0, 8)}</Link>
                  <CopyButton text={c.sha} what={`the commit name ${c.sha.slice(0, 8)}`} label="Copy the commit name" icon="copy" />
                  <IconAction icon="raw" label={`Browse files at ${c.sha.slice(0, 8)}`} href={codeHref(path, c.sha, p || undefined)} />
                </span>
              </li>
            ))}
          </ul>
          {items.length >= limit && limit < 200 && <p className="code-more"><BtnSm onClick={() => setLimit(Math.min(limit * 2, 200))}>Show older commits</BtnSm></p>}
          {items.length >= 200 && <p className="faint small code-note">The newest 200 commits are shown. To go further back, clone the repository or open an older commit and look there.</p>}
        </section>
      )}
      {!items && !error && <DivLoading>Loading…</DivLoading>}
    </RepoFrame>
  );
}
