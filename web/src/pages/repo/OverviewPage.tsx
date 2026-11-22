import { useEffect, useState } from "react";
import { browseApi, codeHref, commitHref, type CommitEntry, type Item } from "../../browseApi";
import { CutReleaseForRepository } from "../../components/CutRelease";
import { Mono } from "../../components/ui";
import { EmptyState, Sheet } from "../../kit/layout";
import { CloneBox } from "../Repositories";
import { RepoFrame, useRepo, when } from "./common";
import { findReadme, Readme } from "./Readme";
import { PSub } from "../../kit/bits";

interface Facts { last: CommitEntry | null; branches: number; tags: number }

/** The front page of a repository: what it is (its README), how to clone it, and a line of facts. */
export function OverviewPage({ path }: { path: string }) {
  const { repo, error } = useRepo(path);
  const [facts, setFacts] = useState<Facts | null>(null);
  const [root, setRoot] = useState<Item | null>(null);
  const [empty, setEmpty] = useState(false);
  useEffect(() => {
    let live = true;
    Promise.all([
      browseApi.commits(path, undefined, undefined, 1).then((r) => r.items[0] ?? null).catch(() => null),
      browseApi.branches(path, "", 1).then((r) => r.total).catch(() => 0),
      browseApi.tags(path, "", 1).then((r) => r.total).catch(() => 0),
    ]).then(([last, branches, tags]) => { if (live) setFacts({ last, branches, tags }); });
    browseApi.read(path, undefined, "").then((i) => { if (live) setRoot(i); }).catch(() => { if (live) setEmpty(true); });
    return () => { live = false; };
  }, [path]);
  const readme = Array.isArray(root?.entries) ? findReadme(root.entries) : undefined;
  const branch = repo?.default_branch ?? "main";
  return (
    <RepoFrame path={path} tab="overview" title={path} actions={<CutReleaseForRepository cloneUrl={repo?.clone.https} />}
      sentence={repo ? <>{repo.visibility} repository{repo.mirror ? ", a mirror: it takes no pushes" : ""}{repo.archived ? ", archived" : ""}.</> : undefined} error={error}>
      {facts && (
        <PSub aria-label="Facts about the repository">
          Default branch <Mono>{branch}</Mono>
          {facts.last ? <> · last commit <a href={commitHref(path, facts.last.sha)}>{facts.last.subject}</a> by {facts.last.author}, {when(facts.last.committed_at)}</> : " · no commits yet"}
          {" · "}<a href={`/repositories/${path}/branches`}>{facts.branches} {facts.branches === 1 ? "branch" : "branches"}</a>
          {" · "}<a href={`/repositories/${path}/tags`}>{facts.tags} {facts.tags === 1 ? "tag" : "tags"}</a>
          {" · "}<a href={codeHref(path)}>Browse the files</a>
        </PSub>
      )}
      {repo && <CloneBox repo={repo} />}
      {readme && root ? <Readme repo={path} commit={root.commit} entry={readme} dir="" refName={branch} />
        : root ? <Sheet><EmptyState title="No README">Add a file called README.md at the top of the repository and it is shown here. <a href={codeHref(path)}>Browse the files</a>.</EmptyState></Sheet>
        : empty ? <Sheet><EmptyState title="This repository is empty">Push a first commit to it with the clone address above: <Mono>git remote add origin &lt;address&gt;</Mono>, then <Mono>git push -u origin {branch}</Mono>. A README.md will be shown here.</EmptyState></Sheet>
        : null}
    </RepoFrame>
  );
}
