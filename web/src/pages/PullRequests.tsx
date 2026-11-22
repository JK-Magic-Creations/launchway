import { RepoTabs } from "../components/RepoTabs";
import { useCallback, useEffect, useState } from "react";
import { oneOf, useUrlState } from "../urlState";
import { ago } from "../format";
import { navigate } from "../router";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { Button, PageHead, Sheet } from "../kit/layout";
import { Chip } from "../kit/Status";
import { Markdown } from "../markdown";
import { pullsApi, type BranchComparison, type Pull } from "../pullsApi";
import "../styles/pulls.css";
import { BtnPrimary, DivFaintSmall, DivPage, LabelField, PSub, SpanFaintSmall } from "../kit/bits";

export const pullHref = (path: string, n?: number | "new") => `/repositories/${path}/pulls${n === undefined ? "" : "/" + n}`;

export function stateChip(p: Pull) {
  if (p.state === "merged") return <Chip tone="ok">Merged</Chip>;
  if (p.state === "closed") return <Chip tone="bad">Closed</Chip>;
  return p.draft ? <Chip>Draft</Chip> : <Chip tone="run">Open</Chip>;
}

const FILTERS: { id: string; label: string; query: (me: string) => Record<string, string> }[] = [
  { id: "open", label: "Open", query: () => ({ state: "open" }) },
  { id: "mine", label: "Mine", query: () => ({ state: "open", author: "@me" }) },
  { id: "review", label: "Review requested", query: () => ({ state: "open", reviewer: "@me" }) },
  { id: "merged", label: "Merged", query: () => ({ state: "merged" }) },
  { id: "closed", label: "Closed", query: () => ({ state: "closed" }) },
];

/** The pull requests of a hosted repository, with filters. */
export function PullRequestsPage({ path }: { path: string }) {
  const [filter, setFilter] = useUrlState("filter", "open", oneOf(...FILTERS.map((f) => f.id)));
  const [label, setLabel] = useUrlState("label", "");
  const [items, setItems] = useState<Pull[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const load = useCallback(() => {
    const f = FILTERS.find((x) => x.id === filter)!;
    pullsApi.list(path, { ...f.query(""), label: label.trim() || undefined }).then((r) => { setItems(r.items); setError(null); }).catch(setError);
  }, [path, filter, label]);
  useEffect(load, [load]);
  return (
    <DivPage>
      <PageHead title={`Pull requests of ${path}`} sentence="Changes on their way from one branch to another: review them, see what they need, merge them."
        actions={<Button kind="pri" onClick={() => navigate(pullHref(path, "new"))}>New pull request</Button>} />
      <RepoTabs path={path} on="pulls" />
      <div className="seg" role="group" aria-label="Filter pull requests">
        {FILTERS.map((f) => (
          <button key={f.id} type="button" className={"btn btn-sm" + (filter === f.id ? " btn-on" : "")} aria-pressed={filter === f.id} onClick={() => setFilter(f.id, "push")}>{f.label}</button>
        ))}
        <label className="pr-label-filter"><span className="sr-only">Label</span><input value={label} placeholder="Label" aria-label="Filter by label" onChange={(e) => setLabel(e.target.value)} /></label>
      </div>
      <ErrorBox error={error} />
      <Sheet title={FILTERS.find((x) => x.id === filter)!.label}>
        {items && items.length === 0 && <Empty title="No pull requests here">Push a branch, then open a pull request from it.</Empty>}
        {items && items.map((p) => (
          <div key={p.id} className="pr-row" data-testid="pull">
            <div>
              <Link href={pullHref(path, p.number)} className="pr-title">{p.title}</Link> {stateChip(p)} {p.labels.map((l) => <Chip key={l}>{l}</Chip>)}
              <DivFaintSmall>#{p.number} by {p.author}, {ago(Date.parse(p.updated_at))}: <Mono>{p.source_branch}</Mono> into <Mono>{p.target_branch}</Mono></DivFaintSmall>
            </div>
            <div className="faint small pr-row-side">
              {p.state === "open" && (p.mergeability.state === "conflicts" ? <Chip tone="bad">Conflicts</Chip> : p.mergeability.state === "clean" ? <Chip tone="ok">Merges cleanly</Chip> : <Chip>Checking</Chip>)}
              {p.requested_reviewers.length > 0 && <div>Review asked of {p.requested_reviewers.join(", ")}</div>}
            </div>
          </div>
        ))}
      </Sheet>
    </DivPage>
  );
}

/** The compare page: choose the branches, see what the pull request would hold, open it. */
export function NewPullRequestPage({ path, branches }: { path: string; branches?: string[] }) {
  const qp = new URLSearchParams(location.search);
  const [head, setHead] = useState(qp.get("head") ?? "");
  const [target, setTarget] = useState(qp.get("base") ?? "");
  const [cmp, setCmp] = useState<BranchComparison | null>(null);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [typed, setTyped] = useState(false); // once the person has written, the repository's template is not put over it
  const [templateFrom, setTemplateFrom] = useState("");
  const [draft, setDraft] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!head.trim()) return;
    const t = setTimeout(() => { pullsApi.compare(path, head.trim(), target.trim() || undefined).then((c) => { setCmp(c); setError(null); }).catch((e) => { setCmp(null); setError(e); }); }, 250);
    return () => clearTimeout(t);
  }, [path, head, target]);
  useEffect(() => {
    if (typed) return;
    const t = setTimeout(() => {
      pullsApi.template(path, target.trim() || undefined).then((r) => { setBody(r.template); setTemplateFrom(r.path); }).catch(() => setTemplateFrom(""));
    }, 250);
    return () => clearTimeout(t);
  }, [path, target, typed]);
  const shown = head.trim() ? cmp : null;
  const titled = title || (shown && shown.commits.length === 1 ? shown.commits[0].subject : "");
  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const p = await pullsApi.create(path, { title: titled, body, source_branch: head.trim(), target_branch: target.trim() || undefined, draft });
      navigate(pullHref(path, p.number));
    } catch (err) { setError(err); setBusy(false); }
  };
  return (
    <DivPage>
      <PageHead title="New pull request" sentence={`Choose the branch with the changes and the branch they go into, in ${path}.`} />
      <Sheet pad>
        <div className="tokenform-grid">
          <LabelField><span>Branch with the changes</span><input value={head} list="pr-branches" placeholder="my-change" onChange={(e) => setHead(e.target.value)} /></LabelField>
          <LabelField><span>Into (default: the default branch)</span><input value={target} list="pr-branches" placeholder="main" onChange={(e) => setTarget(e.target.value)} /></LabelField>
        </div>
        {branches && <datalist id="pr-branches">{branches.map((b) => <option key={b} value={b} />)}</datalist>}
      </Sheet>
      <ErrorBox error={error} />
      {shown && (
        <>
          <PSub role="status">
            {shown.ahead} commit{shown.ahead === 1 ? "" : "s"} ahead and {shown.behind} behind {shown.base}; {shown.changes.total} file{shown.changes.total === 1 ? "" : "s"} changed (+{shown.changes.additions} -{shown.changes.deletions}).{" "}
            {shown.mergeability.state === "conflicts" ? `It would have conflicts in ${(shown.mergeability.conflicting_files ?? []).join(", ")}.` : shown.mergeability.state === "clean" ? "It merges cleanly." : "Whether it merges is not known yet."}
          </PSub>
          {shown.existing_pull ? <p className="callout callout-allowed">Pull request <Link href={pullHref(path, shown.existing_pull)}>#{shown.existing_pull}</Link> already goes between these branches.</p> : null}
          <Sheet title="Commits">
            {shown.commits.length === 0 && <Empty title="Nothing to merge">The branch has no commits that {shown.base} lacks.</Empty>}
            {shown.commits.map((c) => <div key={c.sha} className="pr-row"><span>{c.subject}</span><SpanFaintSmall>{c.author}</SpanFaintSmall></div>)}
          </Sheet>
          {!shown.existing_pull && shown.commits.length > 0 && (
            <Sheet title="Open it" pad>
              <form onSubmit={create} className="inputs-form" aria-label="Open a pull request">
                <LabelField><span>Title</span><input value={titled} required maxLength={256} onChange={(e) => setTitle(e.target.value)} /></LabelField>
                <LabelField><span>Description (Markdown)</span><textarea value={body} rows={6} onChange={(e) => { setTyped(true); setBody(e.target.value); }} />{templateFrom && !typed && <small>Started from <Mono>{templateFrom}</Mono> in the repository.</small>}</LabelField>
                {body.trim() && <div className="pr-preview" aria-label="Preview"><Markdown text={body} /></div>}
                <label className="check"><input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} /> Open it as a draft</label>
                <div><BtnPrimary disabled={busy || !titled.trim()}>{busy ? "Opening…" : "Open pull request"}</BtnPrimary></div>
              </form>
            </Sheet>
          )}
        </>
      )}
    </DivPage>
  );
}
