import { useState } from "react";
import { browseApi, codeHref, isProtected } from "../../browseApi";
import { ErrorBox, Mono } from "../../components/ui";
import { announce } from "../../kit/announce";
import { navigate } from "../../router";
import { BtnPrimary, LabelField, SpanFaintSmall } from "../../kit/bits";

const MAX_FILES = 100, MAX_FILE = 1 << 20, MAX_ALL = 8 << 20;
const slug = () => Math.random().toString(36).slice(2, 6);
const size = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KiB` : `${(n / (1 << 20)).toFixed(1)} MiB`);

const toBase64 = async (f: File): Promise<string> => {
  const bytes = new Uint8Array(await f.arrayBuffer());
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
};

/** Upload one or more files as one commit: straight onto the branch, or onto a new branch with a pull request opened
 * for it (the way in when the branch is protected). A file that exists is never overwritten unless asked. */
export function Uploader({ repo, refName, protectedNames, dir }: { repo: string; refName: string; protectedNames: string[]; dir: string }) {
  const [picked, setPicked] = useState<File[]>([]);
  const [folder, setFolder] = useState(dir);
  const [message, setMessage] = useState("");
  const [replace, setReplace] = useState(false);
  const locked = isProtected(protectedNames, refName);
  const [target, setTarget] = useState<"direct" | "branch">(locked ? "branch" : "direct");
  const [branch, setBranch] = useState(() => `upload-${slug()}`);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const total = picked.reduce((a, f) => a + f.size, 0);
  const tooBig = picked.find((f) => f.size > MAX_FILE);
  const problem = picked.length > MAX_FILES ? `At most ${MAX_FILES} files in one upload.` : tooBig ? `${tooBig.name} is ${size(tooBig.size)}: a file may be at most ${size(MAX_FILE)} here. Push larger files with git.` : total > MAX_ALL ? `An upload may carry at most ${size(MAX_ALL)} in all.` : "";
  const prefix = folder.trim().replace(/^\/+|\/+$/g, "");
  const names = picked.map((f) => (prefix ? prefix + "/" : "") + f.name);
  const msg = message.trim() || (picked.length === 1 ? `Add ${names[0]}` : `Add ${picked.length} files`);
  const back = () => navigate(codeHref(repo, refName, dir || undefined));
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    let into = refName, made = false;
    try {
      if (target === "branch") { await browseApi.makeBranch(repo, branch.trim(), refName); into = branch.trim(); made = true; }
      const files = await Promise.all(picked.map(async (f, i) => ({ path: names[i], content: await toBase64(f), encoding: "base64" })));
      await browseApi.upload(repo, { branch: into, message: msg, replace, files });
      announce(made ? `Committed ${picked.length} files to the new branch ${into}` : `Committed ${picked.length} files to ${into}`);
      navigate(made ? `/repositories/${repo}/pulls/new?head=${encodeURIComponent(into)}&base=${encodeURIComponent(refName)}` : codeHref(repo, into, prefix || undefined));
    } catch (err) {
      setError(made ? new Error(`${err instanceof Error ? err.message : String(err)} The new branch ${into} was made and holds nothing new; delete it on the Branches page, or try again with another name.`) : err);
      setBusy(false);
    }
  };
  return (
    <form className="card editor" onSubmit={submit} aria-label="Upload files">
      <LabelField><span>Files</span>
        <input type="file" multiple onChange={(e) => setPicked([...(e.target.files ?? [])])} aria-describedby="up-help" />
        <small id="up-help">Up to {MAX_FILES} files, {size(MAX_FILE)} each and {size(MAX_ALL)} in all, in one commit. Larger files go in with git.</small></LabelField>
      {picked.length > 0 && (
        <ul className="plain" aria-label="Files to upload">{picked.map((f, i) => <li key={f.name + i}><Mono>{names[i]}</Mono> <SpanFaintSmall>{size(f.size)}</SpanFaintSmall></li>)}</ul>
      )}
      {problem && <p className="callout callout-err" role="alert">{problem}</p>}
      <LabelField><span>Folder in the repository</span>
        <input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="docs (empty is the top)" />
        <small>A slash makes a folder inside a folder; one that does not exist is made.</small></LabelField>
      <label className="radio"><input type="checkbox" checked={replace} onChange={(e) => setReplace(e.target.checked)} /> Overwrite files that exist (without this, an existing file stops the whole upload and nothing is committed)</label>
      <LabelField><span>Commit message</span>
        <input value={message} onChange={(e) => setMessage(e.target.value)} placeholder={msg} maxLength={200} /></LabelField>
      <fieldset className="field">
        <legend>Where the commit goes</legend>
        <label className="radio"><input type="radio" name="target" checked={target === "direct"} onChange={() => setTarget("direct")} /> Commit directly to <Mono>{refName}</Mono>{locked && <SpanFaintSmall> (a protected branch: only its administrators may; others are refused)</SpanFaintSmall>}</label>
        <label className="radio"><input type="radio" name="target" checked={target === "branch"} onChange={() => setTarget("branch")} /> Commit to a new branch and open a pull request</label>
        {target === "branch" && <LabelField><span>New branch name</span><input value={branch} required onChange={(e) => setBranch(e.target.value)} /></LabelField>}
      </fieldset>
      <ErrorBox error={error} />
      <div className="actions">
        <BtnPrimary disabled={busy || picked.length === 0 || !!problem || (target === "branch" && !branch.trim())}>
          {busy ? "Committing…" : target === "branch" ? "Commit and open a pull request" : `Commit ${picked.length || ""} file${picked.length === 1 ? "" : "s"}`.replace("  ", " ")}
        </BtnPrimary>
        <button type="button" className="btn" onClick={back}>Cancel</button>
      </div>
    </form>
  );
}
