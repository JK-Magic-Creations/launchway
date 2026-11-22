import { lazy, Suspense, useCallback, useEffect, useMemo, useState, type ReactElement } from "react";
import { useTab } from "../components/PageTabs";
import { SignatureBadge, useSignatures } from "../components/Signature";
import { int, oneOf, useUrlState } from "../urlState";
import { ago } from "../format";
import { navigate } from "../router";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { Button, PageHead, Sheet } from "../kit/layout";
import { Chip } from "../kit/Status";
import { LaunchLine, type LineStation } from "../kit/LaunchLine";
import { Tabs } from "../kit/Tabs";
import { Markdown } from "../markdown";
import { parsePatch, type Row } from "../components/DiffTable";
import { pullsApi, type Comment, type Conversation, type FileChange, type Patch, type Pull, type Thread } from "../pullsApi";
import { pullHref, stateChip } from "./PullRequests";
import { PullList, PullRunLine } from "./PullPeople";
import "../styles/pulls.css";
import { terms } from "../terms";
import { prose } from "../prose";
const P = await prose("pages.PullRequest");

// What agents said about the change: its own chunk, fetched only when this tab is open.
const AgentReviews = lazy(() => import("../work/AgentReviews"));

/** The pull request as a change on the launch line: opened, checked, reviewed, merged. */
function stations(p: Pull): LineStation[] {
  const rd = p.readiness;
  const checks = rd?.checks ?? [];
  const failed = checks.some((c) => c.required && (c.status === "failed" || c.status === "canceled"));
  const running = checks.some((c) => c.required && (c.status === "running" || c.status === "pending" || c.status === "missing"));
  const review = rd?.blockers.find((b) => b.code === "approvals" || b.code === "changes_requested");
  return [
    { key: "opened", label: "Opened", state: "ok", detail: `#${p.number}` },
    { key: "checks", label: "Checks", state: p.state === "merged" ? "ok" : failed ? "bad" : running ? "run" : checks.length ? "ok" : "idle", detail: p.state === "merged" ? "before the merge" : checks.length ? `${checks.filter((c) => c.status === "passed").length}/${checks.length}` : "none yet" },
    { key: "review", label: "Review", state: p.state === "merged" ? "ok" : review ? "warn" : (rd?.approvals ?? 0) > 0 ? "ok" : "idle", detail: p.state === "merged" ? "before the merge" : review ? review.why : `${rd?.approvals ?? 0} approval${rd?.approvals === 1 ? "" : "s"}` },
    { key: "merge", label: p.state === "merged" ? "Merged" : "Merge", state: p.state === "merged" ? "ok" : rd?.can_merge ? "warn" : "idle", detail: p.state === "merged" ? p.merge_method : rd?.can_merge ? "ready" : "not yet" },
  ];
}

export { parsePatch };

function ThreadView({ t, path, n, me, canApply, onChange, onError }: { t: Thread; path: string; n: number; me: string; canApply: boolean; onChange: () => void; onError: (e: unknown) => void }) {
  const [reply, setReply] = useState("");
  const [open, setOpen] = useState(false);
  const act = async (f: () => Promise<unknown>) => { try { await f(); onChange(); } catch (e) { onError(e); } };
  return (
    <div className={"pr-comment" + (t.resolved ? " thread-resolved" : "")} data-thread={t.id} tabIndex={-1}>
      <header>
        <strong>{t.comments[0]?.author}</strong>
        <span>on {t.start_line && t.start_line !== t.line ? `lines ${t.start_line} to ${t.line}` : `line ${t.line}`}</span>
        {t.outdated && <Chip tone="warn">Outdated: the code changed</Chip>}
        {t.resolved && <Chip tone="ok">Resolved{t.resolved_by ? ` by ${t.resolved_by}` : ""}</Chip>}
      </header>
      {t.comments.map((c) => (
        <div key={c.id}>
          <div className="faint small">{c.author}, {ago(Date.parse(c.created_at))}</div>
          {c.deleted ? <em>Deleted.</em> : <Markdown text={c.body} />}
          {c.has_suggestion && !c.applied_commit && canApply && !t.outdated && (
            <button type="button" className="btn btn-sm" onClick={() => act(() => pullsApi.applySuggestion(path, n, c.id))}>Apply suggestion as a commit</button>
          )}
          {c.applied_commit && <p className="faint small">Applied in <Mono>{c.applied_commit.slice(0, 10)}</Mono>.</p>}
        </div>
      ))}
      <div className="pr-actions pr-note">
        <button type="button" className="btn btn-sm" onClick={() => setOpen(!open)} aria-expanded={open}>Reply</button>
        <button type="button" className="btn btn-sm" onClick={() => act(() => pullsApi.updateComment(path, n, t.comments[0].id, { resolved: !t.resolved }))}>{t.resolved ? "Open again" : "Resolve"}</button>
        {me === "" && null}
      </div>
      {open && (
        <form className="pr-note" onSubmit={(e) => { e.preventDefault(); act(async () => { await pullsApi.comment(path, n, { body: reply, reply_to: t.comments[0].id }); setReply(""); setOpen(false); }); }}>
          <label className="field"><span className="sr-only">Reply</span><textarea value={reply} rows={3} aria-label="Reply" onChange={(e) => setReply(e.target.value)} /></label>
          <button type="submit" className="btn btn-primary btn-sm" disabled={!reply.trim()}>Send reply</button>
        </form>
      )}
    </div>
  );
}

function FileView({ f, path, n, conv, mode, since, me, canApply, reload, onError, startOpen, owners, viewed, onViewed }: {
  f: FileChange; path: string; n: number; conv: Conversation | null; mode: "unified" | "split"; since?: number; me: string; canApply: boolean; reload: () => void; onError: (e: unknown) => void; startOpen: boolean;
  owners?: string[]; viewed: boolean; onViewed: (on: boolean) => void;
}) {
  const [open, setOpen] = useState(startOpen && !viewed);
  const [patch, setPatch] = useState<Patch | null>(null);
  const [comment, setComment] = useState<{ line: number; text: string } | null>(null);
  const load = useCallback((full: boolean) => { pullsApi.patch(path, n, f.path, full, since).then(setPatch).catch(onError); }, [path, n, f.path, since, onError]);
  useEffect(() => { if (open && !patch && !f.binary) load(false); }, [open, patch, f.binary, load]);
  const rows = useMemo(() => (patch ? parsePatch(patch.diff) : []), [patch]);
  const threads = (conv?.threads ?? []).filter((t) => t.path === f.path);
  const at = (line?: number) => threads.filter((t) => !t.outdated && t.side === "new" && t.line === line);
  const outdated = threads.filter((t) => t.outdated || t.side !== "new");
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!comment) return;
    try { await pullsApi.comment(path, n, { body: comment.text, path: f.path, line: comment.line }); setComment(null); reload(); } catch (err) { onError(err); }
  };
  const lineRow = (r: Row, i: number) => (
    <tbody key={i}>
      <tr className={r.kind}>
        <td className="num">{r.old ?? ""}</td><td className="num">{r.neu ?? ""}</td>
        <td className="sign" aria-hidden="true">{r.kind === "add" ? "+" : r.kind === "del" ? "-" : ""}</td>
        <td>{r.text}{r.neu !== undefined && r.kind !== "del" && (
          <button type="button" className="btn btn-sm cbtn" aria-label={`Comment on ${f.path} line ${r.neu}`} onClick={() => setComment({ line: r.neu!, text: "" })}>Comment</button>
        )}</td>
      </tr>
      {at(r.neu).map((t) => <tr key={t.id} className="thread-row"><td colSpan={4}><ThreadView t={t} path={path} n={n} me={me} canApply={canApply} onChange={reload} onError={onError} /></td></tr>)}
      {comment && r.neu === comment.line && r.kind !== "del" && (
        <tr className="thread-row"><td colSpan={4}>
          <form className="pr-note" onSubmit={submit} aria-label={`Comment on line ${comment.line}`}>
            <label className="field"><span className="sr-only">Comment</span><textarea ref={(el) => el?.focus()} rows={3} aria-label="Comment" value={comment.text} onChange={(e) => setComment({ line: comment.line, text: e.target.value })} placeholder={P.t0} /></label>
            <div className="pr-actions"><button type="submit" className="btn btn-primary btn-sm" disabled={!comment.text.trim()}>Comment</button><button type="button" className="btn btn-sm" onClick={() => setComment(null)}>Cancel</button></div>
          </form>
        </td></tr>
      )}
    </tbody>
  );
  const split = () => {
    const out: ReactElement[] = [];
    for (let i = 0; i < rows.length; i++) {
      const r = rows[i];
      if (r.kind === "hunk") { out.push(<tbody key={i}><tr className="hunk"><td colSpan={4}>{r.text}</td></tr></tbody>); continue; }
      if (r.kind === "del" && rows[i + 1]?.kind === "add") {
        const a = rows[i + 1];
        out.push(<tbody key={i}><tr><td className="num">{r.old}</td><td className="half" style={{ background: "var(--bad-wash)" }}>{r.text}</td><td className="num">{a.neu}</td><td className="half" style={{ background: "var(--ok-wash)" }}>{a.text}</td></tr></tbody>);
        i++;
      } else if (r.kind === "del") out.push(<tbody key={i}><tr><td className="num">{r.old}</td><td className="half" style={{ background: "var(--bad-wash)" }}>{r.text}</td><td className="num" /><td className="half" /></tr></tbody>);
      else out.push(<tbody key={i}><tr><td className="num">{r.old ?? ""}</td><td className="half" style={r.kind === "add" ? undefined : undefined}>{r.kind === "add" ? "" : r.text}</td><td className="num">{r.neu ?? ""}</td><td className="half" style={r.kind === "add" ? { background: "var(--ok-wash)" } : undefined}>{r.text}</td></tr></tbody>);
    }
    return out;
  };
  return (
    <section className="pr-file" aria-label={f.path} data-file>
      <header>
        <h3 tabIndex={-1}>{f.old_path ? `${f.old_path} → ${f.path}` : f.path}</h3>
        <Chip>{f.status}</Chip>
        {!f.binary && <span className="small"><span className="pr-add">+{f.additions}</span> <span className="pr-del">-{f.deletions}</span></span>}
        {owners && owners.length > 0 && <span className="small faint" data-owners>Owned by {owners.map((o) => (o.startsWith("group:") ? `group ${o.slice(6)}` : `@${o}`)).join(", ")}</span>}
        <label className="check small"><input type="checkbox" checked={viewed} aria-label={`Viewed ${f.path}`} data-viewed onChange={(e) => { onViewed(e.target.checked); if (e.target.checked) setOpen(false); }} /> Viewed</label>
        <button type="button" className="btn btn-sm" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? "Hide" : "Show"}</button>
      </header>
      {open && f.binary && <p className="pr-note faint">{P.t1}</p>}
      {open && patch?.held && <p className="pr-note">{P.t2}<button type="button" className="btn btn-sm" onClick={() => load(true)}>Load anyway</button></p>}
      {open && patch && !patch.held && (
        <>
          <table className={"diff" + (mode === "split" ? " diff-split" : "")} aria-label={`Changes to ${f.path}`}>
            <caption className="sr-only">Changes to {f.path}</caption>
            <colgroup>{mode === "split" ? <><col style={{ width: 48 }} /><col /><col style={{ width: 48 }} /><col /></> : <><col style={{ width: 48 }} /><col style={{ width: 48 }} /><col style={{ width: 20 }} /><col /></>}</colgroup>
            {mode === "split" ? split() : rows.map(lineRow)}
          </table>
          {patch.truncated && <p className="pr-note faint">{P.t3}</p>}
        </>
      )}
      {open && outdated.map((t) => <ThreadView key={t.id} t={t} path={path} n={n} me={me} canApply={false} onChange={reload} onError={onError} />)}
    </section>
  );
}

/** Where the pull request is in the branch's merge queue, and the run that is testing it. */
function QueueBox({ pull }: { pull: Pull }) {
  const why = pull.readiness?.can_merge ? "" : pull.readiness?.why;
  const q = pull.queue;
  const live = q && q.state !== "removed" && q.state !== "merged";
  return (
    <div className="pr-queue" role="group" aria-label="Merge queue">
      <h3>Merge queue</h3>
      {live && <p role="status">Number {q.position} in the merge queue for {pull.target_branch}, {q.state === "testing" ? P.t4 : "waiting its turn"}.{" "}
        {(q.runs ?? []).map((id) => <Link key={id} href={`/runs/${id}`}>{terms().say("the {run} testing it")}</Link>)}
        {q.position === 1 && why ? ` It merges when this is met: ${why}.` : ""}</p>}
      {q?.state === "removed" && <p role="status">{P.t5}{q.reason}.</p>}
      {!q && <p className="faint">{pull.target_branch} {P.t6}</p>}
    </div>
  );
}

/** One pull request: the conversation, its commits and its files, and the box that says what is needed to merge it. */
export function PullRequestPage({ path, number }: { path: string; number: number }) {
  const [pull, setPull] = useState<Pull | null>(null);
  const [conv, setConv] = useState<Conversation | null>(null);
  const [files, setFiles] = useState<FileChange[] | null>(null);
  const [commits, setCommits] = useState<{ sha: string; author: string; subject: string }[] | null>(null);
  const sigs = useSignatures(path, (commits ?? []).map((c) => c.sha));
  // The tab, the diff layout and the revision compared against are in the address, so a review can be linked: ?tab=files&mode=split&since=2
  const [tab, setTab] = useTab(["conversation", "commits", "files"], "conversation");
  const [mode, setModeU] = useUrlState("mode", "unified", oneOf<"unified" | "split">("unified", "split"));
  const setMode = (m: "unified" | "split") => setModeU(m);
  const [sinceN, setSinceN] = useUrlState("since", 0, int(1, 100000));
  const since = sinceN || undefined;
  const setSince = (n: number | undefined) => setSinceN(n ?? 0);
  const [error, setError] = useState<unknown>(null);
  const [text, setText] = useState("");
  const [verdict, setVerdict] = useState("comment");
  const [picked, setMethod] = useState("");
  const [del, setDel] = useState(false);
  const [bypass, setBypass] = useState("");
  const [busy, setBusy] = useState("");
  const [owners, setOwners] = useState<Record<string, string[]>>({});
  const [ownerNote, setOwnerNote] = useState("");
  const [viewed, setViewed] = useState<string[]>([]);
  const [keys, setKeys] = useState(false);
  const load = useCallback(() => {
    pullsApi.get(path, number).then((p) => { setPull(p); setError(null); }).catch(setError);
    pullsApi.conversation(path, number).then(setConv).catch(setError);
  }, [path, number]);
  useEffect(load, [load]);
  // The judgement of mergeability is made in the background: look again until it is made.
  useEffect(() => {
    if (pull?.state !== "open" || (pull.mergeability.state !== "checking" && pull.mergeability.state !== "unknown")) return;
    const t = setTimeout(() => pullsApi.get(path, number).then(setPull).catch(() => {}), 1200);
    return () => clearTimeout(t);
  }, [pull, path, number]);
  useEffect(() => { pullsApi.files(path, number, since).then((r) => setFiles(r.files)).catch(setError); }, [path, number, since, pull?.head_sha]);
  useEffect(() => {
    if (tab !== "files") return;
    pullsApi.owners(path, number).then((r) => {
      setOwners(Object.fromEntries(r.owners.files.map((f) => [f.path, f.owners])));
      setOwnerNote(r.owners.problems.length ? `The owners file has a problem on line ${r.owners.problems[0].line}: ${r.owners.problems[0].message}.` : "");
    }).catch(() => {});
    pullsApi.viewed(path, number).then(setViewed).catch(() => {});
  }, [tab, path, number, pull?.head_sha]);
  const markViewed = (file: string, on: boolean) => {
    setViewed((v) => (on ? [...v, file] : v.filter((x) => x !== file)));
    pullsApi.setViewed(path, number, [file], on).then(setViewed).catch((e) => { setError(e); pullsApi.viewed(path, number).then(setViewed).catch(() => {}); });
  };
  useEffect(() => { if (tab === "commits") pullsApi.commits(path, number).then(setCommits).catch(setError); }, [tab, path, number, pull?.head_sha]);

  // j and k move between files, n and p between comments.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      if (el?.closest?.("input, textarea, select") || e.metaKey || e.ctrlKey || e.altKey) return;
      const move = (sel: string, dir: 1 | -1) => {
        const all = [...document.querySelectorAll<HTMLElement>(sel)];
        if (!all.length) return;
        const i = all.findIndex((x) => x === document.activeElement || x.contains(document.activeElement));
        const next = all[Math.min(Math.max(i + dir, 0), all.length - 1)];
        next.scrollIntoView({ block: "center" });
        (next.querySelector<HTMLElement>("h3") ?? next).focus();
      };
      if (e.key === "j") move("[data-file]", 1);
      else if (e.key === "k") move("[data-file]", -1);
      else if (e.key === "n") move("[data-thread]", 1);
      else if (e.key === "p") move("[data-thread]", -1);
      else if (e.key === "v") (document.activeElement?.closest("[data-file]")?.querySelector<HTMLInputElement>("[data-viewed]"))?.click();
      else if (e.key === "?") { e.preventDefault(); setKeys((k) => !k); } // this page's own list: the global one stands down
      else if (e.key === "Escape") setKeys(false);
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, []);

  const can = (name: string) => pull?.actions?.find((a) => a.name === name)?.available === true;
  const run = async (what: string, f: () => Promise<unknown>) => { setBusy(what); setError(null); try { await f(); load(); } catch (e) { setError(e); load(); } finally { setBusy(""); } };
  if (!pull) return <div className="page"><PageHead title={`Pull request #${number}`} /><ErrorBox error={error} /></div>;
  const rd = pull.readiness;
  const method = picked || rd?.default_method || "merge";
  const send = (e: React.FormEvent) => {
    e.preventDefault();
    run("review", async () => { if (verdict === "comment" && !text.trim()) throw new Error(P.t7); await pullsApi.review(path, number, { verdict, body: text }); setText(""); });
  };
  return (
    <div className="page">
      <PageHead title={<>{pull.title} <span className="faint">#{pull.number}</span></>} sentence={<>{path}</>} />
      <div className="pr-head-meta">
        {stateChip(pull)}
        <span><strong>{pull.author}</strong> wants to merge <Mono>{pull.source_branch}</Mono> into <Mono>{pull.target_branch}</Mono></span>
        {pull.reverts ? <span>It reverts <Link href={pullHref(path, pull.reverts)}>#{pull.reverts}</Link>.</span> : null}
        <Link href={pullHref(path)}>All pull requests</Link>
      </div>
      <LaunchLine stations={stations(pull)} label={`Where pull request #${pull.number} is on the launch line`} />
      <PullRunLine runId={pull.linked_runs[pull.linked_runs.length - 1]} head={pull.head_sha} />
      <ErrorBox error={error} />
      <div className="pr-grid">
        <div>
          <section className={"pr-mergebox " + (pull.state === "open" ? (rd?.can_merge ? "ready" : "blocked") : "")} aria-label="Merge">
            {pull.state === "merged" && <><h2>Merged</h2><p className="why">{pull.merged_by ? `${pull.merged_by} merged it` : "It was merged"} by {pull.merge_method} as <Mono>{pull.merge_commit?.slice(0, 10)}</Mono>, {pull.merged_at ? ago(Date.parse(pull.merged_at)) : ""}.</p></>}
            {pull.state === "closed" && <><h2>Closed</h2><p className="why">{P.t8}</p></>}
            {pull.state === "open" && (
              <>
                <h2>{rd?.can_merge ? "Ready to merge" : "Cannot merge yet"}</h2>
                <p className="why" role="status">
                  {pull.mergeability.state === "conflicts" ? <>It has conflicts in {(pull.mergeability.conflicting_files ?? []).map((f) => <Mono key={f}>{f}</Mono>)}. </> : null}
                  {rd?.can_merge ? "Nothing is in the way." : rd?.why ? rd.why.charAt(0).toUpperCase() + rd.why.slice(1) + "." : "Looking at it…"}
                </p>
                {rd && rd.checks.length > 0 && (
                  <ul className="pr-checks" aria-label="Checks">
                    {rd.checks.map((c) => (
                      <li key={c.name}><Chip tone={c.status === "passed" ? "ok" : c.status === "failed" || c.status === "canceled" ? "bad" : c.status === "missing" ? "warn" : "run"}>{c.status}</Chip> <Mono>{c.name}</Mono>{c.required ? " (required)" : ""} {c.run_id && <Link href={`/runs/${c.run_id}`}>run {c.run_number}</Link>}</li>
                    ))}
                  </ul>
                )}
                {(can("merge") && rd) && (
                  <div className="pr-actions">
                    <label className="field"><span className="sr-only">How to merge</span>
                      <select value={method} aria-label="How to merge" onChange={(e) => setMethod(e.target.value)}>
                        {rd.methods.map((m) => <option key={m} value={m}>{m === "merge" ? "Merge commit" : m === "squash" ? "Squash and merge" : "Rebase and merge"}</option>)}
                      </select></label>
                    <label className="check"><input type="checkbox" checked={del} onChange={(e) => setDel(e.target.checked)} /> Delete the branch after</label>
                    {!rd.can_merge && <label className="field"><span className="sr-only">Why you may merge anyway</span><input value={bypass} aria-label="Why you may merge anyway" placeholder={P.t9} onChange={(e) => setBypass(e.target.value)} /></label>}
                    <button type="button" className={"btn " + (rd.can_merge ? "btn-primary" : "btn-danger-outline")} disabled={busy !== "" || (!rd.can_merge && !bypass.trim())}
                      onClick={() => run("merge", () => pullsApi.merge(path, number, { method, delete_branch: del, expect_head: pull.head_sha, bypass_reason: bypass || undefined }))}>
                      {busy === "merge" ? "Merging…" : rd.can_merge ? "Merge" : "Merge past the rules"}
                    </button>
                  </div>
                )}
              </>
            )}
            {pull.state === "open" && rd?.queue && <QueueBox pull={pull} />}
            <div className="pr-actions" style={{ marginTop: 10 }}>
              {can("queue") && <Button sm kind="pri" onClick={() => run("queue", () => pullsApi.enqueue(path, number, method))}>Add to the merge queue</Button>}
              {can("dequeue") && <Button sm onClick={() => run("queue", () => pullsApi.dequeue(path, number))}>Take out of the merge queue</Button>}
              {can("update_branch") && <Button sm onClick={() => run("update", () => pullsApi.updateBranch(path, number))}>Update the branch</Button>}
              {can("mark_ready") && <Button sm onClick={() => run("ready", () => pullsApi.update(path, number, { draft: false }))}>Mark ready for review</Button>}
              {can("mark_draft") && <Button sm onClick={() => run("draft", () => pullsApi.update(path, number, { draft: true }))}>Convert to draft</Button>}
              {can("auto_merge") && pull.state === "open" && !(rd?.queue && pull.queue && pull.queue.state !== "removed") && <Button sm onClick={() => run("auto", () => pullsApi.update(path, number, { auto_merge: method || "merge" }))}>Merge when the rules pass</Button>}
              {can("cancel_auto_merge") && <Button sm onClick={() => run("auto", () => pullsApi.update(path, number, { auto_merge: "" }))}>Turn off auto-merge</Button>}
              {can("close") && <Button sm onClick={() => run("close", () => pullsApi.update(path, number, { state: "closed" }))}>Close</Button>}
              {can("reopen") && <Button sm onClick={() => run("reopen", () => pullsApi.update(path, number, { state: "open" }))}>Reopen</Button>}
              {can("revert") && <Button sm onClick={() => run("revert", async () => { const r = await pullsApi.revert(path, number); navigate(pullHref(path, r.number)); })}>Revert</Button>}
            </div>
            {pull.auto_merge && <p className="faint small">It merges by {pull.auto_merge} as soon as the rules pass.</p>}
          </section>

          <Tabs label="Pull request" on={tab} onChange={setTab} tabs={[
            { id: "conversation", label: "Conversation" }, { id: "commits", label: "Commits" }, { id: "files", label: `Files changed${files ? ` (${files.length})` : ""}` }]} />
          <div role="tabpanel" aria-labelledby={`tab-${tab}`}>
            {tab === "conversation" && (
              <>
                <Sheet title="Description" pad>{pull.body.trim() ? <Markdown text={pull.body} /> : <p className="faint">No description.</p>}</Sheet>
                <Suspense fallback={null}><AgentReviews repo={path} pull={number} /></Suspense>
                {conv && conv.reviews.map((r) => (
                  <div key={r.id} className="pr-comment">
                    <header><strong>{r.author}</strong> {r.verdict === "approve" ? <Chip tone="ok">Approved</Chip> : r.verdict === "request_changes" ? <Chip tone="bad">Asked for changes</Chip> : <Chip>Reviewed</Chip>}
                      {r.state === "dismissed" && <Chip tone="warn">Dismissed: {r.dismissed_reason}</Chip>}{r.stale && r.state !== "dismissed" && <Chip tone="warn">Of an older version</Chip>}<span className="faint">{ago(Date.parse(r.created_at))}</span></header>
                    {r.body && <div><Markdown text={r.body} /></div>}
                  </div>
                ))}
                {conv && conv.comments.map((c: Comment) => (
                  <div key={c.id} className="pr-comment"><header><strong>{c.author}</strong><span className="faint">{ago(Date.parse(c.created_at))}</span></header><div>{c.deleted ? <em>Deleted.</em> : <Markdown text={c.body} />}</div></div>
                ))}
                {conv && conv.threads.length > 0 && <Sheet title="Threads on the code">
                  {conv.threads.map((t) => <div key={t.id} className="pr-note"><div className="small"><Mono>{t.path}</Mono>:{t.line}</div><ThreadView t={t} path={path} n={number} me="" canApply={can("merge")} onChange={load} onError={setError} /></div>)}
                </Sheet>}
                {conv && conv.revisions.length > 1 && (
                  <Sheet title="Revisions">
                    {conv.revisions.map((r) => <div key={r.seq} className="pr-row"><span>Revision {r.seq}: <Mono>{r.head_sha.slice(0, 10)}</Mono>{r.forced ? " (force-pushed)" : ""}</span><span className="faint small">{ago(Date.parse(r.created_at))}{r.actor ? ` by ${r.actor}` : ""}</span></div>)}
                  </Sheet>
                )}
                {can("review") && (
                  <Sheet title="Review or comment" pad className="pr-reviewbox">
                    <form onSubmit={send} className="inputs-form" aria-label="Submit a review">
                      <label className="field"><span>Your review (Markdown)</span><textarea rows={4} value={text} onChange={(e) => setText(e.target.value)} /></label>
                      <fieldset className="events"><legend>Verdict</legend>
                        {[["comment", "Comment"], ["approve", "Approve"], ["request_changes", "Request changes"]].map(([v, l]) => (
                          <label key={v} className="check"><input type="radio" name="verdict" value={v} checked={verdict === v} onChange={() => setVerdict(v)} /> {l}</label>
                        ))}
                      </fieldset>
                      <div><button type="submit" className="btn btn-primary" disabled={busy !== ""}>Submit review</button></div>
                    </form>
                  </Sheet>
                )}
              </>
            )}
            {tab === "commits" && (
              <Sheet title="Commits">
                {commits && commits.length === 0 && <Empty title="No commits" />}
                {commits && commits.map((c) => <div key={c.sha} className="pr-row"><span>{c.subject}<SignatureBadge result={sigs[c.sha]} quiet /></span><span className="faint small"><Mono>{c.sha.slice(0, 10)}</Mono> {c.author}</span></div>)}
              </Sheet>
            )}
            {tab === "files" && (
              <>
                <div className="seg" role="group" aria-label="How to show the changes">
                  <button type="button" className={"btn btn-sm" + (mode === "unified" ? " btn-on" : "")} aria-pressed={mode === "unified"} onClick={() => setMode("unified")}>Unified</button>
                  <button type="button" className={"btn btn-sm" + (mode === "split" ? " btn-on" : "")} aria-pressed={mode === "split"} onClick={() => setMode("split")}>Side by side</button>
                  {pull.revisions > 1 && (
                    <label className="field"><span className="sr-only">Compare with</span>
                      <select aria-label="Show changes since" value={since ?? ""} onChange={(e) => setSince(e.target.value ? Number(e.target.value) : undefined)}>
                        <option value="">All changes</option>
                        {(conv?.revisions ?? []).slice(0, -1).map((r) => <option key={r.seq} value={r.seq}>Changes since revision {r.seq}</option>)}
                      </select></label>
                  )}
                </div>
                <p className="faint small">{P.t10}</p>
                {keys && <div role="dialog" aria-label="Keyboard shortcuts" className="pr-note pr-keys"><h3>Keyboard shortcuts</h3>
                  <dl><dt>j, k</dt><dd>next and previous file</dd><dt>n, p</dt><dd>{P.t11}</dd><dt>v</dt><dd>{P.t12}</dd><dt>?</dt><dd>show or hide this list</dd><dt>Escape</dt><dd>close it</dd></dl>
                  <button type="button" className="btn btn-sm" onClick={() => setKeys(false)}>Close</button></div>}
                {ownerNote && <p className="pr-note" role="note">{ownerNote}</p>}
                {files && files.length > 0 && <p className="faint small" aria-live="polite">{files.filter((f) => viewed.includes(f.path)).length} of {files.length} files viewed.</p>}
                {files && files.length === 0 && <Empty title="No changes">{since ? P.t13 : P.t14}</Empty>}
                {files && files.map((f, i) => <FileView key={f.path + (since ?? "")} f={f} path={path} n={number} conv={conv} mode={mode} since={since} me="" canApply={can("merge")} reload={load} onError={setError} startOpen={i < 5 && !f.too_large} owners={owners[f.path]} viewed={viewed.includes(f.path)} onViewed={(on) => markViewed(f.path, on)} />)}
              </>
            )}
          </div>
        </div>
        <aside className="pr-side" aria-label="Details">
          <PullList title="Reviewers" noun="reviewer" none="None asked" items={pull.requested_reviewers} editable={can("request_review")}
            onAdd={(n) => run("people", () => pullsApi.requestReview(path, number, [n]))} onRemove={(n) => run("people", () => pullsApi.unrequestReview(path, number, n))} />
          {((pull.requested_reviewer_groups?.length ?? 0) > 0 || can("request_review")) && <PullList title="Reviewer groups" noun="group" none="None asked" items={pull.requested_reviewer_groups ?? []} editable={can("request_review")}
            onAdd={(n) => run("people", () => pullsApi.requestReviewGroup(path, number, n))} onRemove={(n) => run("people", () => pullsApi.unrequestReviewGroup(path, number, n))} />}
          <PullList title="Assignees" noun="assignee" none="None" items={pull.assignees} editable={can("edit")}
            onAdd={(n) => run("people", () => pullsApi.update(path, number, { assignees: [...pull.assignees, n] }))} onRemove={(n) => run("people", () => pullsApi.update(path, number, { assignees: pull.assignees.filter((a) => a !== n) }))} />
          <PullList title="Labels" noun="label" none="None" chip items={pull.labels} editable={can("edit")}
            onAdd={(n) => run("people", () => pullsApi.update(path, number, { labels: [...pull.labels, n] }))} onRemove={(n) => run("people", () => pullsApi.update(path, number, { labels: pull.labels.filter((l) => l !== n) }))} />
          <div><h3>Fetch</h3><Mono>git fetch origin {pull.fetch_ref}</Mono></div>
          <div><h3>Behind the target</h3>{pull.mergeability.behind} commit{pull.mergeability.behind === 1 ? "" : "s"}</div>
        </aside>
      </div>
    </div>
  );
}
