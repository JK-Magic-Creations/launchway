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
    <RepoFrame path={path} tab="branches" title="Branches" sentence={<>How far each branch is from <span className="mono">{def || "the default branch"}</span>, counted in commits.</>} error={error}>
      {notice && (
        <div className="callout" role="status">
          <p>Deleted <span className="mono">{notice.name}</span>. It pointed at <span className="mono">{notice.was}</span>, so nothing is lost: <button type="button" className="btn btn-sm" disabled={busy} onClick={restore}>Bring it back</button>
            {" "}or run <span className="mono small">git push origin {notice.was.slice(0, 10)}:refs/heads/{notice.name}</span> <CopyButton text={`git push origin ${notice.was}:refs/heads/${notice.name}`} what="the command" /></p>
        </div>
      )}
      <div className="code-bar">
        <label className="field grow"><span className="sr-only">Find a branch</span><input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a branch" aria-label="Find a branch" /></label>
      </div>
      {canWrite && (
        <form className="card branch-new" onSubmit={make} aria-label="Make a branch">
          <label className="field"><span>New branch name</span><input value={name} required onChange={(e) => setName(e.target.value)} placeholder="feature/short-name" /></label>
          <div className="field"><span>Start from</span><RefSwitcher repo={path} current={base || "…"} onPick={setFrom} label="Start from" /></div>
          <button type="submit" className="btn btn-primary" disabled={busy || !name.trim() || !base}>Make branch</button>
        </form>
      )}
      {items && items.length === 0 && <EmptyState title={q ? "No branch matches" : "No branches yet"}>{q ? `Nothing is called like “${q}”.` : "Push a first commit and its branch appears here."}</EmptyState>}
      {items && items.length > 0 && (
        <section className="card" aria-label="Branches">
          <table className="tree-table branch-table">
            <caption className="sr-only">Branches, newest first</caption>
            <thead><tr><th scope="col">Branch</th><th scope="col">Last commit</th><th scope="col">Behind / ahead</th><th scope="col"><span className="sr-only">Actions</span></th></tr></thead>
            <tbody>
              {items.map((b) => (
                <tr key={b.name}>
                  <td><Link href={codeHref(path, b.name)} className="mono">{b.name}</Link> {b.default && <Chip>default</Chip>} {b.protected && <Chip tone="warn">protected</Chip>}
                    {b.open_pull ? <> <Link href={`/repositories/${path}/pulls/${b.open_pull}`}>pull request #{b.open_pull}</Link></> : null}</td>
                  <td className="msg-cell"><MsgLink href={commitHref(path, b.commit)} text={b.subject || "(no message)"} /><div className="faint small">{b.author}, {when(b.committed_at)}</div></td>
                  <td className="mono small">{b.default ? <span className="faint">the default</span> : b.ahead < 0 ? "unknown" : `${b.behind} behind, ${b.ahead} ahead`}</td>
                  <td className="row-actions">
                    {!b.default && <Link href={compareHref(path, b.name, def)} className="btn btn-sm">Compare<span className="sr-only"> {b.name}</span></Link>}
                    {canWrite && !b.default && <button type="button" className="btn btn-sm" onClick={() => { setRenaming(b); setNewName(b.name); setError(null); }}>Rename<span className="sr-only"> {b.name}</span></button>}
                    {canWrite && !b.default && <button type="button" className="btn btn-sm" onClick={() => setAsking(b)}>Delete<span className="sr-only"> {b.name}</span></button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {total > items.length && <p className="faint small code-note">The newest {items.length} of {total} are shown; type in the box to find the others.</p>}
        </section>
      )}
      {!items && !error && <div className="loading">Loading…</div>}
      {renaming && (
        <Dialog label={`Rename ${renaming.name}`} onClose={() => setRenaming(null)}>
          <h2 className="h-sm">Rename {renaming.name}</h2>
          <form onSubmit={(e) => { e.preventDefault(); void rename(renaming); }}>
            <label className="field"><span>New name</span><input value={newName} onChange={(e) => setNewName(e.target.value)} data-autofocus /></label>
            <p className="faint small">{renaming.protected ? "It is protected: only the repository's administrators can rename it. " : ""}Pull requests and runs that name {renaming.name} are not moved, and everyone with a clone renames theirs with <span className="mono">git branch -m</span>.</p>
            <div className="actions"><button type="submit" className="btn btn-primary" disabled={busy || !newName.trim() || newName.trim() === renaming.name}>{busy ? "Renaming…" : "Rename branch"}</button><button type="button" className="btn" onClick={() => setRenaming(null)}>Cancel</button></div>
          </form>
          <ErrorBox error={error} />
        </Dialog>
      )}
      {asking && (
        <Dialog label={`Delete ${asking.name}`} onClose={() => setAsking(null)}>
          <h2 className="h-sm">Delete {asking.name}?</h2>
          <p>It points at <span className="mono">{asking.commit}</span>{asking.ahead > 0 ? `, which has ${asking.ahead} ${asking.ahead === 1 ? "commit" : "commits"} that ${def} lacks` : ""}.
            {asking.protected ? " It is protected: only the repository's administrators can delete it." : ""} The commits stay until the repository is cleaned up, and you will be shown how to bring the branch back.</p>
          {asking.open_pull ? <p className="faint small">Pull request #{asking.open_pull} is open from it and will be closed.</p> : null}
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
    <RepoFrame path={path} tab="tags" title="Tags" sentence="Names for commits that matter, such as a release." error={error}>
      {notice && (
        <div className="callout" role="status">
          <p>Deleted the tag <span className="mono">{notice.name}</span>. It pointed at <span className="mono">{notice.was}</span>: <span className="mono small">git push origin {notice.was.slice(0, 10)}:refs/tags/{notice.name}</span> brings it back.</p>
        </div>
      )}
      {canWrite && (
        <form className="card branch-new" onSubmit={make} aria-label="Make a tag">
          <label className="field"><span>New tag name</span><input value={name} required onChange={(e) => setName(e.target.value)} placeholder="v1.0.0" /></label>
          <div className="field"><span>Tag this commit or branch</span><RefSwitcher repo={path} current={base || "…"} onPick={setFrom} label="Tag this" /></div>
          <button type="submit" className="btn btn-primary" disabled={busy || !name.trim() || !base}>Make tag</button>
        </form>
      )}
      <div className="code-bar"><label className="field grow"><span className="sr-only">Find a tag</span><input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Find a tag" aria-label="Find a tag" /></label></div>
      {items && items.length === 0 && <EmptyState title={q ? "No tag matches" : "No tags yet"}>{q ? `Nothing is called like “${q}”.` : <>{canWrite ? "Make one above, or tag " : "Tag "}a commit and push it: <span className="mono">git tag -a v1.0 -m "First release" &amp;&amp; git push origin v1.0</span></>}</EmptyState>}
      {items && items.length > 0 && (
        <section className="card" aria-label="Tags">
          <table className="tree-table">
            <caption className="sr-only">Tags, newest first</caption>
            <thead><tr><th scope="col">Tag</th><th scope="col">Message</th><th scope="col">Commit</th><th scope="col">Date</th><th scope="col"><span className="sr-only">Actions</span></th></tr></thead>
            <tbody>
              {items.map((t) => (
                <tr key={t.name}>
                  <td><Link href={codeHref(path, t.name)} className="mono">{t.name}</Link> {t.annotated && <Chip>annotated</Chip>}<SignatureBadge result={sigs[t.sha]} quiet /></td>
                  <td>{t.annotated ? <span className="tag-msg">{t.message || t.subject}</span> : <span className="faint">{t.subject}</span>}{t.tagger && <div className="faint small">tagged by {t.tagger}</div>}</td>
                  <td><Link href={commitHref(path, t.commit)} className="mono">{t.commit.slice(0, 8)}</Link></td>
                  <td className="faint small">{when(t.committed_at)}</td>
                  <td className="row-actions">{canWrite && <button type="button" className="btn btn-sm" onClick={() => setAsking(t)}>Delete<span className="sr-only"> {t.name}</span></button>}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {total > items.length && <p className="faint small code-note">The newest {items.length} of {total} are shown; type in the box to find the others.</p>}
        </section>
      )}
      {!items && !error && <div className="loading">Loading…</div>}
      {asking && (
        <Dialog label={`Delete the tag ${asking.name}`} onClose={() => setAsking(null)}>
          <h2 className="h-sm">Delete the tag {asking.name}?</h2>
          <p>It points at <span className="mono">{asking.commit}</span>. The commit stays; only the name goes, and you will be shown how to bring it back. Anyone who has fetched the tag keeps it until they delete it themselves.</p>
          <div className="actions"><button type="button" className="btn btn-primary" disabled={busy} onClick={() => del(asking)} data-autofocus>{busy ? "Deleting…" : "Delete tag"}</button><button type="button" className="btn" onClick={() => setAsking(null)}>Cancel</button></div>
        </Dialog>
      )}
    </RepoFrame>
  );
}
