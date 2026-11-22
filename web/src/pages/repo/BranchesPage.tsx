import { SignatureBadge, useSignatures } from "../../components/Signature";
import { useCallback, useEffect, useState } from "react";
import { useUrlState } from "../../urlState";
import { canChange, useMe } from "../../auth";
import { browseApi, codeHref, commitHref, compareHref, type RefInfo } from "../../browseApi";
import { ErrorBox, Link } from "../../components/ui";
import { announce } from "../../kit/announce";
import { Dialog } from "../../kit/Dialog";
import { EmptyState } from "../../kit/layout";
import { Chip } from "../../kit/Status";
import { CopyButton, MsgLink, RefSwitcher, RepoFrame, useRepo, when } from "./common";
import { prose } from "../../prose";
import { BtnPrimary, BtnSm, DivFaintSmall, DivLoading, LabelField, SpanFaint, SpanMono, ThCol } from "../../kit/bits";
const P = await prose("pages.repo.BranchesPage");

/** The branches of a repository: how far each is from the default branch, who changed it last, whether it is protected,
 * its open pull request; make one, delete one (and bring it back, with the commit named). */
export function BranchesPage({ path }: { path: string }) {
  const me = useMe();
  const { repo } = useRepo(path);
  const [q, setQ] = useUrlState("q", "");
  const [items, setItems] = useState<RefInfo[] | null>(null);
  const [total, setTotal] = useState(0);
  const [def, setDef] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [asking, setAsking] = useState<RefInfo | null>(null);
  const [renaming, setRenaming] = useState<RefInfo | null>(null);
  const [newName, setNewName] = useState("");
  const [notice, setNotice] = useState<{ name: string; was: string; undo: string } | null>(null);
  const [name, setName] = useState("");
  const [from, setFrom] = useState("");
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => {
    browseApi.branches(path, q, 100).then((r) => { setItems(r.items); setTotal(r.total); setDef(r.default_branch); setError(null); }).catch(setError);
  }, [path, q]);
  useEffect(() => { const t = setTimeout(load, q ? 150 : 0); return () => clearTimeout(t); }, [load, q]);
  const canWrite = canChange(me, path) && !repo?.mirror && !repo?.archived;
  const base = from || def;
  const make = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true); setError(null);
    try { const r = await browseApi.makeBranch(path, name.trim(), base); announce(`Made the branch ${r.name}`); setName(""); load(); } catch (err) { setError(err); } finally { setBusy(false); }
  };
  const del = async (b: RefInfo) => {
    setBusy(true); setError(null);
    try { const r = await browseApi.deleteBranch(path, b.name); setNotice(r); announce(`Deleted ${b.name}`); setAsking(null); load(); } catch (err) { setError(err); setAsking(null); } finally { setBusy(false); }
  };
  const rename = async (b: RefInfo) => {
    setBusy(true); setError(null);
    try { const r = await browseApi.renameBranch(path, b.name, newName.trim()); announce(`Renamed ${b.name} to ${r.name}`); setRenaming(null); load(); } catch (err) { setError(err); } finally { setBusy(false); }
  };
  const restore = async () => {
    if (!notice) return;
    setBusy(true);
    try { await browseApi.makeBranch(path, notice.name, notice.was); announce(`Brought back ${notice.name}`); setNotice(null); load(); } catch (err) { setError(err); } finally { setBusy(false); }
  };
  return (
    <RepoFrame path={path} tab="branches" title="Branches" sentence={<>How far each branch is from <SpanMono>{def || "the default branch"}</SpanMono>, counted in commits.</>} error={error}>
      {notice && (
        <div className="callout" role="status">
          <p>Deleted <SpanMono>{notice.name}</SpanMono>. It pointed at <SpanMono>{notice.was}</SpanMono>, so nothing is lost: <BtnSm disabled={busy} onClick={restore}>Bring it back</BtnSm>
            {" "}or run <span className="mono small">git push origin {notice.was.slice(0, 10)}:refs/heads/{notice.name}</span> <CopyButton text={`git push origin ${notice.was}:refs/heads/${notice.name}`} what="the command" /></p>
        </div>
      )}
      <div className="code-bar">
        <label className="field grow"><span className="sr-only">Find a branch</span><input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a branch" aria-label="Find a branch" /></label>
      </div>
      {canWrite && (
        <form className="card branch-new" onSubmit={make} aria-label="Make a branch">
          <LabelField><span>New branch name</span><input value={name} required onChange={(e) => setName(e.target.value)} placeholder="feature/short-name" /></LabelField>
          <div className="field"><span>Start from</span><RefSwitcher repo={path} current={base || "…"} onPick={setFrom} label="Start from" /></div>
          <BtnPrimary disabled={busy || !name.trim() || !base}>Make branch</BtnPrimary>
        </form>
      )}
      {items && items.length === 0 && <EmptyState title={q ? "No branch matches" : "No branches yet"}>{q ? `Nothing is called like “${q}”.` : P.t0}</EmptyState>}
      {items && items.length > 0 && (
        <section className="card" aria-label="Branches">
          <table className="tree-table branch-table">
            <caption className="sr-only">Branches, newest first</caption>
            <thead><tr><ThCol>Branch</ThCol><ThCol>Last commit</ThCol><ThCol>Behind / ahead</ThCol><ThCol><span className="sr-only">Actions</span></ThCol></tr></thead>
            <tbody>
              {items.map((b) => (
                <tr key={b.name}>
                  <td><Link href={codeHref(path, b.name)} className="mono">{b.name}</Link> {b.default && <Chip>default</Chip>} {b.protected && <Chip tone="warn">protected</Chip>}
                    {b.open_pull ? <> <Link href={`/repositories/${path}/pulls/${b.open_pull}`}>pull request #{b.open_pull}</Link></> : null}</td>
                  <td className="msg-cell"><MsgLink href={commitHref(path, b.commit)} text={b.subject || "(no message)"} /><DivFaintSmall>{b.author}, {when(b.committed_at)}</DivFaintSmall></td>
                  <td className="mono small">{b.default ? <SpanFaint>the default</SpanFaint> : b.ahead < 0 ? "unknown" : `${b.behind} behind, ${b.ahead} ahead`}</td>
                  <td className="row-actions">
                    {!b.default && <Link href={compareHref(path, b.name, def)} className="btn btn-sm">Compare<span className="sr-only"> {b.name}</span></Link>}
                    {canWrite && !b.default && <BtnSm onClick={() => { setRenaming(b); setNewName(b.name); setError(null); }}>Rename<span className="sr-only"> {b.name}</span></BtnSm>}
                    {canWrite && !b.default && <BtnSm onClick={() => setAsking(b)}>Delete<span className="sr-only"> {b.name}</span></BtnSm>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {total > items.length && <p className="faint small code-note">The newest {items.length} of {total} {P.t1}</p>}
        </section>
      )}
      {!items && !error && <DivLoading>Loading…</DivLoading>}
      {renaming && (
        <Dialog label={`Rename ${renaming.name}`} onClose={() => setRenaming(null)}>
          <h2 className="h-sm">Rename {renaming.name}</h2>
          <form onSubmit={(e) => { e.preventDefault(); void rename(renaming); }}>
            <LabelField><span>New name</span><input value={newName} onChange={(e) => setNewName(e.target.value)} data-autofocus /></LabelField>
            <p className="faint small">{renaming.protected ? P.t2 : ""}{P.t3}{renaming.name} {P.t4}<SpanMono>git branch -m</SpanMono>.</p>
            <div className="actions"><BtnPrimary disabled={busy || !newName.trim() || newName.trim() === renaming.name}>{busy ? "Renaming…" : "Rename branch"}</BtnPrimary><button type="button" className="btn" onClick={() => setRenaming(null)}>Cancel</button></div>
          </form>
          <ErrorBox error={error} />
        </Dialog>
      )}
      {asking && (
        <Dialog label={`Delete ${asking.name}`} onClose={() => setAsking(null)}>
          <h2 className="h-sm">Delete {asking.name}?</h2>
          <p>It points at <SpanMono>{asking.commit}</SpanMono>{asking.ahead > 0 ? `, which has ${asking.ahead} ${asking.ahead === 1 ? "commit" : "commits"} that ${def} lacks` : ""}.
            {asking.protected ? P.t5 : ""} {P.t6}</p>
          {asking.open_pull ? <p className="faint small">Pull request #{asking.open_pull} {P.t7}</p> : null}
          <div className="actions"><button type="button" className="btn btn-primary" disabled={busy} onClick={() => del(asking)} data-autofocus>{busy ? "Deleting…" : "Delete branch"}</button><button type="button" className="btn" onClick={() => setAsking(null)}>Cancel</button></div>
          <ErrorBox error={error} />
        </Dialog>
      )}
    </RepoFrame>
  );
}

/** The tags of a repository, with an annotated tag's message. */
export function TagsPage({ path }: { path: string }) {
  const [q, setQ] = useUrlState("q", "");
  const [items, setItems] = useState<RefInfo[] | null>(null);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState<unknown>(null);
  const me = useMe();
  const { repo } = useRepo(path);
  const [name, setName] = useState("");
  const [from, setFrom] = useState("");
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState<RefInfo | null>(null);
  const [notice, setNotice] = useState<{ name: string; was: string } | null>(null);
  const [def, setDef] = useState("");
  const sigs = useSignatures(path, (items ?? []).map((t) => t.sha));
  const load = useCallback(() => browseApi.tags(path, q, 100).then((r) => { setItems(r.items); setTotal(r.total); setError(null); }).catch(setError), [path, q]);
  useEffect(() => {
    const t = setTimeout(load, q ? 150 : 0);
    return () => clearTimeout(t);
  }, [load, q]);
  useEffect(() => { browseApi.branches(path, "", 1).then((r) => setDef(r.default_branch)).catch(() => {}); }, [path]);
  const canWrite = canChange(me, path) && !repo?.mirror && !repo?.archived;
  const base = from || def;
  const make = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true); setError(null);
    try { const r = await browseApi.makeTag(path, name.trim(), base); announce(`Made the tag ${r.name}`); setName(""); await load(); } catch (err) { setError(err); } finally { setBusy(false); }
  };
  const del = async (t: RefInfo) => {
    setBusy(true); setError(null);
    try { const r = await browseApi.deleteTag(path, t.name); setNotice(r); announce(`Deleted the tag ${t.name}`); setAsking(null); await load(); } catch (err) { setError(err); setAsking(null); } finally { setBusy(false); }
  };
  return (
    <RepoFrame path={path} tab="tags" title="Tags" sentence={P.t8} error={error}>
      {notice && (
        <div className="callout" role="status">
          <p>Deleted the tag <SpanMono>{notice.name}</SpanMono>. It pointed at <SpanMono>{notice.was}</SpanMono>: <span className="mono small">git push origin {notice.was.slice(0, 10)}:refs/tags/{notice.name}</span> brings it back.</p>
        </div>
      )}
      {canWrite && (
        <form className="card branch-new" onSubmit={make} aria-label="Make a tag">
          <LabelField><span>New tag name</span><input value={name} required onChange={(e) => setName(e.target.value)} placeholder="v1.0.0" /></LabelField>
          <div className="field"><span>Tag this commit or branch</span><RefSwitcher repo={path} current={base || "…"} onPick={setFrom} label="Tag this" /></div>
          <BtnPrimary disabled={busy || !name.trim() || !base}>Make tag</BtnPrimary>
        </form>
      )}
      <div className="code-bar"><label className="field grow"><span className="sr-only">Find a tag</span><input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a tag" aria-label="Find a tag" /></label></div>
      {items && items.length === 0 && <EmptyState title={q ? "No tag matches" : "No tags yet"}>{q ? `Nothing is called like “${q}”.` : <>{canWrite ? "Make one above, or tag " : "Tag "}a commit and push it: <SpanMono>{P.t9}</SpanMono></>}</EmptyState>}
      {items && items.length > 0 && (
        <section className="card" aria-label="Tags">
          <table className="tree-table">
            <caption className="sr-only">Tags, newest first</caption>
            <thead><tr><ThCol>Tag</ThCol><ThCol>Message</ThCol><ThCol>Commit</ThCol><ThCol>Date</ThCol><ThCol><span className="sr-only">Actions</span></ThCol></tr></thead>
            <tbody>
              {items.map((t) => (
                <tr key={t.name}>
                  <td><Link href={codeHref(path, t.name)} className="mono">{t.name}</Link> {t.annotated && <Chip>annotated</Chip>}<SignatureBadge result={sigs[t.sha]} quiet /></td>
                  <td>{t.annotated ? <span className="tag-msg">{t.message || t.subject}</span> : <SpanFaint>{t.subject}</SpanFaint>}{t.tagger && <DivFaintSmall>tagged by {t.tagger}</DivFaintSmall>}</td>
                  <td><Link href={commitHref(path, t.commit)} className="mono">{t.commit.slice(0, 8)}</Link></td>
                  <td className="faint small">{when(t.committed_at)}</td>
                  <td className="row-actions">{canWrite && <BtnSm onClick={() => setAsking(t)}>Delete<span className="sr-only"> {t.name}</span></BtnSm>}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {total > items.length && <p className="faint small code-note">The newest {items.length} of {total} {P.t1}</p>}
        </section>
      )}
      {!items && !error && <DivLoading>Loading…</DivLoading>}
      {asking && (
        <Dialog label={`Delete the tag ${asking.name}`} onClose={() => setAsking(null)}>
          <h2 className="h-sm">Delete the tag {asking.name}?</h2>
          <p>It points at <SpanMono>{asking.commit}</SpanMono>{P.t10}</p>
          <div className="actions"><button type="button" className="btn btn-primary" disabled={busy} onClick={() => del(asking)} data-autofocus>{busy ? "Deleting…" : "Delete tag"}</button><button type="button" className="btn" onClick={() => setAsking(null)}>Cancel</button></div>
        </Dialog>
      )}
    </RepoFrame>
  );
}
