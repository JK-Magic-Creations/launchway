import { useEffect, useState } from "react";
import { browseApi, codeHref, isProtected, type Item } from "../../browseApi";
import { ErrorBox } from "../../components/ui";
import { announce } from "../../kit/announce";
import { navigate } from "../../router";
import { Mono } from "../../components/ui";
import { LabelField, SpanFaintSmall } from "../../kit/bits";

const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 30) || "change";

/** Edit, add or remove one file. The change is one commit: straight onto the branch, or onto a new branch with a
 * pull request opened for it (the way in when the branch is protected). */
export function Editor({ repo, item, mode, refName, protectedNames, dir }: { repo: string; item: Item; mode: "edit" | "new"; refName: string; protectedNames: string[]; dir: string }) {
  const removing = mode === "edit" && new URLSearchParams(location.search).get("delete") === "1";
  const editable = mode === "new" || (item.type === "file" && item.encoding === "utf8" && !item.truncated);
  const [path, setPath] = useState(mode === "new" ? (dir ? dir + "/" : "") : item.path);
  const [text, setText] = useState(mode === "edit" ? item.content ?? "" : "");
  const [message, setMessage] = useState("");
  const locked = isProtected(protectedNames, refName);
  const [target, setTarget] = useState<"direct" | "branch">(locked ? "branch" : "direct");
  const [branch, setBranch] = useState(() => `edit-${slug(path.split("/").pop() || "file")}-${Math.random().toString(36).slice(2, 6)}`);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const dirty = (mode === "edit" ? text !== (item.content ?? "") : text !== "") || (mode === "new" && path !== (dir ? dir + "/" : ""));
  useEffect(() => {
    if (!dirty) return;
    const on = (e: BeforeUnloadEvent) => { e.preventDefault(); };
    window.addEventListener("beforeunload", on);
    return () => window.removeEventListener("beforeunload", on);
  }, [dirty]);
  const verb = removing ? "Delete" : mode === "new" ? "Add" : "Update";
  const msg = message.trim() || `${verb} ${path}`;
  const back = () => navigate(mode === "new" ? codeHref(repo, refName, dir || undefined) : codeHref(repo, refName, item.path));
  const cancel = () => { if (dirty && !window.confirm("Leave without saving? What you typed here is lost.")) return; back(); };
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    let into = refName, made = false;
    try {
      if (target === "branch") { await browseApi.makeBranch(repo, branch.trim(), refName); into = branch.trim(); made = true; }
      const body = { branch: into, path: path.trim(), message: msg, expected_sha: mode === "edit" ? item.sha : undefined };
      if (removing) await browseApi.remove(repo, { ...body, expected_sha: item.sha });
      else await browseApi.write(repo, { ...body, content: text });
      announce(made ? `Committed to the new branch ${into}` : `Committed to ${into}`);
      navigate(made ? `/repositories/${repo}/pulls/new?head=${encodeURIComponent(into)}&base=${encodeURIComponent(refName)}` : removing ? codeHref(repo, refName) : codeHref(repo, into, path.trim()));
    } catch (err) {
      setError(made ? new Error(`${err instanceof Error ? err.message : String(err)} The new branch ${into} was made and holds nothing new; delete it on the Branches page, or try again with another name.`) : err);
      setBusy(false);
    }
  };
  if (!editable) return <p className="code-note">This file is not text that fits here, so it cannot be edited in the browser. Clone the repository to change it.</p>;
  return (
    <form className="card editor" onSubmit={submit} aria-label={removing ? "Delete a file" : mode === "new" ? "Add a file" : "Edit a file"}>
      {mode === "new" && (
        <LabelField><span>File name, with its folders</span>
          <input value={path} required onChange={(e) => setPath(e.target.value)} aria-describedby="ed-path-help" placeholder="docs/guide.md" />
          <small id="ed-path-help">A slash makes a folder. The file is added to <Mono>{refName}</Mono> or to a new branch from it.</small></LabelField>
      )}
      {removing ? <p className="code-note" role="note">This removes <Mono>{item.path}</Mono> in one commit. It stays in the history, so it can be brought back.</p> : (
        <LabelField><span>{mode === "new" ? "Contents" : `Contents of ${item.path}`}</span>
          <textarea className="editor-text mono" value={text} onChange={(e) => setText(e.target.value)} rows={24} spellCheck={false} wrap="off" aria-describedby="ed-text-help" />
          <small id="ed-text-help">Plain text, saved exactly as typed, with the file's line endings. {text.split("\n").length} lines.</small></LabelField>
      )}
      <LabelField><span>Commit message</span>
        <input value={message} onChange={(e) => setMessage(e.target.value)} placeholder={`${verb} ${path}`} maxLength={200} /></LabelField>
      <fieldset className="field">
        <legend>Where the commit goes</legend>
        <label className="radio"><input type="radio" name="target" checked={target === "direct"} onChange={() => setTarget("direct")} /> Commit directly to <Mono>{refName}</Mono>{locked && <SpanFaintSmall> (a protected branch: only its administrators may; others are refused)</SpanFaintSmall>}</label>
        <label className="radio"><input type="radio" name="target" checked={target === "branch"} onChange={() => setTarget("branch")} /> Commit to a new branch and open a pull request</label>
        {target === "branch" && <LabelField><span>New branch name</span><input value={branch} required onChange={(e) => setBranch(e.target.value)} /></LabelField>}
      </fieldset>
      <ErrorBox error={error} />
      <div className="actions">
        <button type="submit" className={"btn btn-primary"} disabled={busy || !path.trim() || (target === "branch" && !branch.trim())}>
          {busy ? "Committing…" : target === "branch" ? (removing ? "Commit the deletion and open a pull request" : "Commit and open a pull request") : removing ? "Commit the deletion" : "Commit the change"}
        </button>
        <button type="button" className="btn" onClick={cancel}>Cancel</button>
      </div>
    </form>
  );
}
