import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { ApiError } from "../../api";
import { canChange, useMe } from "../../auth";
import { ErrorBox, Link } from "../../components/ui";
import { archiveUrl, blameHref, browseApi, codeHref, commitHref, commitsHref, compareHref, type Entry, type Item } from "../../browseApi";
import { Dialog } from "../../kit/Dialog";
import { EmptyState, LinkButton } from "../../kit/layout";
import { navigate } from "../../router";
import { Breadcrumb, CopyButton, MsgLink, RefSwitcher, RepoFrame, useFetched, useQuery, useRepo, when } from "./common";
import { NameIcon } from "./icons";
import { FileView } from "./FileView";
import { findReadme, Readme } from "./Readme";
import { Uploader } from "./Uploader";
import { prose } from "../../prose";
import { DivLoading, SpanFaint, SpanFaintSmall, SpanMono, ThCol } from "../../kit/bits";
const P = await prose("pages.repo.CodePage");

const Editor = lazy(() => import("./Editor").then((m) => ({ default: m.Editor })));

const order = (a: Entry, b: Entry) => (a.type === "dir" ? 0 : 1) - (b.type === "dir" ? 0 : 1) || a.name.localeCompare(b.name);

/** The words a person needs to put the first commit in an empty repository, each with its copy button. */
function EmptyRepo({ path, https, branch }: { path: string; https?: string; branch: string }) {
  const url = https ?? `https://HOST/git/${path}.git`;
  const lines = [`git clone ${url}`, `cd ${path.split("/").pop()}`, `echo "# ${path.split("/").pop()}" > README.md`, "git add README.md", `git commit -m "First commit"`, `git push origin HEAD:${branch}`];
  return (
    <EmptyState title="This repository is empty" action={<CopyButton text={lines.join("\n")} what="the commands" label="Copy all the commands" />}>
      {P.t0}<SpanMono>{branch}</SpanMono>{P.t1}<SpanMono>git remote add origin {url}</SpanMono> then <SpanMono>git push -u origin {branch}</SpanMono>.
      <pre className="code-plain">{lines.join("\n")}</pre>
    </EmptyState>
  );
}

/** Find a file by typing part of its path: the letters in order, anywhere (a fuzzy match). The server does the
 * searching; the page puts the best match first. */
function GoToFile({ repo, refName, onClose }: { repo: string; refName: string; onClose: () => void }) {
  const [q, setQ] = useState("");
  const [paths, setPaths] = useState<string[]>([]);
  const [partial, setPartial] = useState(false);
  const [at, setAt] = useState(0);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    const t = q.trim();
    const id = setTimeout(() => {
      if (!t) { setPaths([]); return; }
      const re = [...t.replace(/\s+/g, "")].map((c) => c.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*");
      browseApi.search(repo, { q: re, ref: refName, regex: true, in: "path" }).then((r) => {
        const score = (p: string) => { const base = p.split("/").pop()!.toLowerCase(); const l = t.toLowerCase(); return (base.startsWith(l) ? 0 : base.includes(l) ? 1 : 2) * 1000 + p.length; };
        setPaths(r.files.map((f) => f.path).sort((a, b) => score(a) - score(b)).slice(0, 50)); setPartial(r.partial); setAt(0); setError(null);
      }).catch(setError);
    }, 120);
    return () => clearTimeout(id);
  }, [q, repo, refName]);
  const go = (p: string) => { onClose(); navigate(codeHref(repo, refName, p)); };
  return (
    <Dialog label="Go to file" onClose={onClose}>
      <h2 className="h-sm">Go to file</h2>
      <input role="combobox" aria-expanded="true" aria-controls="goto-list" aria-autocomplete="list" aria-activedescendant={paths[at] ? `goto-${at}` : undefined} aria-label="Part of a file's path" placeholder={P.t2} value={q}
        onChange={(e) => setQ(e.target.value)} data-autofocus
        onKeyDown={(e) => { if (e.key === "ArrowDown") { e.preventDefault(); setAt((a) => Math.min(a + 1, paths.length - 1)); } else if (e.key === "ArrowUp") { e.preventDefault(); setAt((a) => Math.max(a - 1, 0)); } else if (e.key === "Enter" && paths[at]) { e.preventDefault(); go(paths[at]); } }} />
      <ErrorBox error={error} />
      <ul id="goto-list" role="listbox" aria-label="Files" className="goto">
        {paths.map((p, i) => <li key={p} id={`goto-${i}`} role="option" aria-selected={i === at} className={i === at ? "on" : ""} onMouseDown={(e) => { e.preventDefault(); go(p); }}><SpanMono>{p}</SpanMono></li>)}
        {q.trim() && paths.length === 0 && <li role="presentation" className="faint small">No file matches.</li>}
      </ul>
      {partial && <p className="faint small" role="status">{P.t3}</p>}
      <p className="faint small">{P.t4}<SpanMono>{refName.length === 40 ? refName.slice(0, 10) : refName}</SpanMono>.</p>
    </Dialog>
  );
}

/** The code of a repository: a folder with the last commit of each entry and its README, or a file. */
export function CodePage({ path }: { path: string }) {
  const q = useQuery();
  const me = useMe();
  const { repo } = useRepo(path);
  const refName = q.get("ref") ?? undefined;
  const p = q.get("path") ?? "";
  const mode = q.get("edit") ? "edit" : q.get("new") ? "new" : q.get("upload") ? "upload" : "";
  const { data: item, error: loadError } = useFetched<Item>(`${path}|${refName ?? ""}|${p}`, () => browseApi.read(path, refName, p, true));
  const empty = loadError instanceof ApiError && loadError.status === 404 && !p && !refName;
  const error = empty ? null : loadError;
  const [hash, setHash] = useState(location.hash);
  const [finder, setFinder] = useState(false);
  const [protectedNames, setProtected] = useState<string[]>([]);
  useEffect(() => { browseApi.protected(path).then(setProtected).catch(() => setProtected([])); }, [path]);
  useEffect(() => {
    const on = () => setHash(location.hash);
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  const current = item?.ref ?? refName ?? repo?.default_branch ?? "main";
  const canWrite = canChange(me, path) && !repo?.mirror && !repo?.archived;
  const goRef = useCallback((r: string) => navigate(codeHref(path, r, item?.type === "dir" || !item ? p || undefined : p)), [path, item, p]);
  const setHashTo = (h: string) => { history.replaceState(null, "", `${location.pathname}${location.search}${h}`); setHash(h); };

  // Keys: t finds a file, j and k move through the entries, y copies the permalink, b opens blame, e edits.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      if (el.closest(P.t5) || e.metaKey || e.ctrlKey || e.altKey || document.querySelector("[role=dialog]")) return;
      if (e.key === "t") { e.preventDefault(); setFinder(true); }
      else if ((e.key === "j" || e.key === "k") && item?.type === "dir") {
        const all = [...document.querySelectorAll<HTMLElement>(".tree-name a")];
        const i = all.indexOf(document.activeElement as HTMLElement);
        all[Math.min(Math.max(i + (e.key === "j" ? 1 : -1), 0), all.length - 1)]?.focus();
      } else if (e.key === "y" && item) { navigator.clipboard?.writeText(`${location.origin}${codeHref(path, item.commit, item.path)}${location.hash}`); }
      else if (e.key === "b" && item?.type === "file") navigate(blameHref(path, item.ref, item.path));
      else if (e.key === "e" && item?.type === "file" && canWrite) navigate(codeHref(path, item.ref, item.path, { edit: "1" }));
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [item, path, canWrite]);

  const entries = useMemo(() => [...(item?.entries ?? [])].sort(order), [item]);
  const readme = item?.type === "dir" ? findReadme(entries) : undefined;
  const parent = p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "";
  const sentence = repo ? <>{repo.visibility} repository{repo.mirror ? P.t6 : ""}{repo.archived ? ", archived" : ""}. Press <kbd>t</kbd> to find a file.</> : undefined;

  if (mode === "upload" && item) {
    return (
      <RepoFrame path={path} tab="code" title="Upload files" sentence="The files become one commit." error={error}>
        <Uploader repo={path} refName={current} protectedNames={protectedNames} dir={item.type === "dir" ? p : p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : ""} />
      </RepoFrame>
    );
  }
  if (mode && mode !== "upload" && item) {
    return (
      <RepoFrame path={path} tab="code" title={mode === "new" ? "New file" : `Edit ${item.path}`} sentence={P.t7} error={error}>
        <Suspense fallback={<DivLoading>Loading…</DivLoading>}>
          <Editor repo={path} item={item} mode={mode} refName={current} protectedNames={protectedNames} dir={mode === "new" ? p : ""} />
        </Suspense>
      </RepoFrame>
    );
  }
  return (
    <RepoFrame path={path} tab="code" title={p ? p.split("/").pop()! : path.split("/").slice(1).join("/")} sentence={sentence} error={error}
      actions={<><RefSwitcher repo={path} current={current} onPick={goRef} />
        <LinkButton href={`/repositories/${path}/search`}>Search</LinkButton></>}>
      {empty && <EmptyRepo path={path} https={repo?.clone.https} branch={repo?.default_branch ?? "main"} />}
      {item && (
        <>
          <div className="code-bar">
            <Breadcrumb repo={path} refName={current} path={p} />
            <span className="grow" />
            <Link href={commitsHref(path, current, p || undefined)} className="btn btn-sm">History</Link>
            {current !== repo?.default_branch && repo?.default_branch && <Link href={compareHref(path, current, repo.default_branch)} className="btn btn-sm">Compare with {repo.default_branch}</Link>}
            {item.type === "dir" && canWrite && <Link href={codeHref(path, current, p || undefined, { new: "1" })} className="btn btn-sm">New file</Link>}
            {item.type === "dir" && canWrite && <Link href={codeHref(path, current, p || undefined, { upload: "1" })} className="btn btn-sm">Upload files</Link>}
          </div>
          {item.type === "dir" && (
            <div className="row wrap" role="group" aria-label={`Download ${current}`} style={{ marginBottom: 8 }}>
              <a href={archiveUrl(path, current, "tar.gz")} className="btn btn-sm" download>Download tar.gz</a>
              <a href={archiveUrl(path, current, "zip")} className="btn btn-sm" download>Download zip</a>
            </div>
          )}
          {item.type === "dir" ? (
            <>
              <section className="card tree" aria-label="Files">
                <table className="tree-table">
                  <caption className="sr-only">Files and folders in {p || "the top of the repository"}{P.t8}</caption>
                  <thead><tr><ThCol className="tree-name">Name</ThCol><ThCol className="tree-msg">Last commit</ThCol><ThCol className="tree-who">Author</ThCol><ThCol className="tree-when">Changed</ThCol></tr></thead>
                  <tbody>
                    {p && <tr><td className="tree-name" colSpan={4}><Link href={codeHref(path, current, parent || undefined)}><NameIcon name=".." type="dir" />..</Link> <SpanFaintSmall>up one folder</SpanFaintSmall></td></tr>}
                    {entries.map((e) => (
                      <tr key={e.name}>
                        <td className="tree-name"><Link href={codeHref(path, current, e.path)}><NameIcon name={e.name} type={e.type} />{e.type === "dir" ? e.name + "/" : e.name}</Link>{e.type === "symlink" && <SpanFaintSmall> link</SpanFaintSmall>}{e.type === "submodule" && <SpanFaintSmall> submodule</SpanFaintSmall>}</td>
                        <td className="tree-msg">{e.last ? <MsgLink href={commitHref(path, e.last.sha)} text={e.last.subject} /> : <SpanFaint title={P.t9}>older</SpanFaint>}</td>
                        <td className="tree-who faint small">{e.last?.author ?? ""}</td>
                        <td className="tree-when faint small">{e.last ? when(e.last.at) : ""}</td>
                      </tr>
                    ))}
                    {entries.length === 0 && <tr><td colSpan={4} className="faint">This folder is empty.</td></tr>}
                  </tbody>
                </table>
                {item.last_bounded && <p className="faint small code-note" role="note">{P.t10}</p>}
              </section>
              {readme ? <Readme repo={path} commit={item.commit} entry={readme} dir={p} refName={current} />
                : p === "" && entries.length > 0 && <p className="faint small code-note">{P.t11}<SpanMono>README.md</SpanMono> and it is shown here{canWrite ? <>: <Link href={codeHref(path, current, undefined, { new: "1" })}>add one now</Link></> : ""}.</p>}
            </>
          ) : (
            <FileView repo={path} item={item} canWrite={canWrite} hash={hash} onHash={setHashTo}
              onEdit={() => navigate(codeHref(path, item.ref, item.path, { edit: "1" }))}
              onDelete={() => navigate(codeHref(path, item.ref, item.path, { edit: "1", delete: "1" }))} />
          )}
        </>
      )}
      {!item && !error && !empty && <DivLoading>Loading…</DivLoading>}
      {finder && <GoToFile repo={path} refName={current} onClose={() => setFinder(false)} />}
    </RepoFrame>
  );
}
