import { KeyHelp } from "../learn/LearnLink";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type LibraryOverrides, type PipelineFile, type Problem, type Project, type Ref, type SaveResult } from "../api";
import { ago, shortSha } from "../format";
import { navigate } from "../router";
import { useMe } from "../auth";
import { useValidation } from "../validation";
import { PipelineEditor, type PipelineEditorHandle } from "../components/PipelineEditor";
import { PipelinePreview, ProblemList } from "../components/PipelinePreview";
import { RefPicker } from "../components/RefPicker";
import { VariablesPanel } from "../components/Variables";
import { OverridesEditor } from "../components/Libraries";
import { BranchIcon, CommitIcon, ErrorBox, Link, Mono } from "../components/ui";

// Git is the only place a pipeline is kept. This page reads the file from a
// branch, lets you change it, run the change as a draft without saving it,
// and save it as a commit. Unsaved edits are kept only in this browser tab
// (sessionStorage), so leaving for a draft run and coming back keeps them.

interface Stored { sha: string; text: string }

const storeKey = (project: string, ref: string) => `launchway.edit:${project}:${ref}`;

function readStored(project: string, ref: string): Stored | null {
  try {
    const v = sessionStorage.getItem(storeKey(project, ref));
    const s = v ? (JSON.parse(v) as Stored) : null;
    return s && typeof s.sha === "string" && typeof s.text === "string" ? s : null;
  } catch { return null; }
}

function writeStored(project: string, ref: string, s: Stored | null) {
  try {
    if (s) sessionStorage.setItem(storeKey(project, ref), JSON.stringify(s));
    else sessionStorage.removeItem(storeKey(project, ref));
  } catch { /* storage unavailable: edits live only on this page */ }
}

/** Opens the editor on a branch with the given text, as if it had been typed
 * there at commit sha: used to edit the text a draft run ran. */
export function openInEditor(project: string, ref: string, sha: string, text: string) {
  writeStored(project, ref, { sha, text });
  navigate(`/projects/${encodeURIComponent(project)}/pipeline?ref=${encodeURIComponent(ref)}`);
}

function refFromURL(): string {
  return new URLSearchParams(location.search).get("ref") ?? "";
}

// globMatch is the server's path.Match, closely enough to decide what to
// offer: * and ? do not match a /. The server decides for real.
function globMatch(pattern: string, name: string): boolean {
  let re = "^";
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i];
    if (c === "*") re += "[^/]*";
    else if (c === "?") re += "[^/]";
    else if (c === "[") {
      const end = pattern.indexOf("]", i + 1);
      if (end < 0) return false;
      re += "[" + pattern.slice(i + 1, end).replace(/^\^/, "^").replace(/\\/g, "\\\\") + "]";
      i = end;
    } else re += c.replace(/[.+^${}()|\\]/g, "\\$&");
  }
  try { return new RegExp(re + "$").test(name); } catch { return false; }
}

export function EditPipelinePage({ name }: { name: string }) {
  const [ref, setRef] = useState(refFromURL);
  const [project, setProject] = useState<Project | null>(null);
  const [file, setFile] = useState<PipelineFile | null>(null);
  const [text, setText] = useState<string | null>(null);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [restored, setRestored] = useState(false);
  const [older, setOlder] = useState<Stored | null>(null); // edits made at another commit
  const [picking, setPicking] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);
  const [overrides, setOverrides] = useState<LibraryOverrides>({});
  const [copied, setCopied] = useState(false);
  const editor = useRef<PipelineEditorHandle>(null);
  const [cursorLine, setCursorLine] = useState("");
  const me = useMe();

  useEffect(() => {
    const on = () => setRef(refFromURL());
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  useEffect(() => { api.project(name).then(setProject).catch(setLoadErr); }, [name]);

  // (Re)load the file at the branch's tip. keep: the text to keep in the
  // editor whatever the branch now holds.
  const load = useCallback(async (keep?: string) => {
    setLoadErr(null); setActionErr(null);
    try {
      const f = await api.pipeline(name, ref);
      setFile(f);
      setOlder(null); setRestored(false);
      const stored = keep !== undefined ? { sha: f.sha, text: keep } : readStored(name, f.ref);
      if (stored && stored.text !== f.text) {
        if (stored.sha === f.sha) {
          setText(stored.text);
          setRestored(keep === undefined);
        } else {
          setText(f.text);
          setOlder(stored);
        }
      } else {
        setText(f.text);
      }
      if (!ref) history.replaceState(null, "", `${location.pathname}?ref=${encodeURIComponent(f.ref)}`);
    } catch (e) { setLoadErr(e); }
  }, [name, ref]);
  // The old text stays on screen until the new arrives, so that a dialog
  // that moved the editor to another branch stays open.
  useEffect(() => { load(); }, [load]);

  const dirty = file !== null && text !== null && text !== file.text;
  // Keep unsaved edits with this tab, against the commit they were made at.
  useEffect(() => {
    if (!file || text === null || older) return;
    writeStored(name, file.ref, dirty ? { sha: file.sha, text } : null);
  }, [name, file, text, dirty, older]);
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const { res, error: checkErr, checking } = useValidation(text, { project: name });
  const libraryNames = (res?.graph?.libraries ?? []).map((l) => l.name);
  const valid = !!res?.ok && !checking;
  const problems = res && !res.ok ? res.problems ?? [] : [];

  if (loadErr) {
    return (
      <div className="page">
        <ErrorBox error={loadErr instanceof ApiError && loadErr.status === 404 ? (ref ? `${name} has no branch called ${ref}, or no such project.` : "This project does not exist.") : loadErr} />
        <Link href="/projects">Back to projects</Link>
      </div>
    );
  }
  if (!file || text === null || !project) return <div className="page"><div className="loading">Reading the repository…</div></div>;

  const rights = project.editor;
  const runDraft = async () => {
    setBusy(true); setActionErr(null);
    try {
      const run = await api.draftRun(name, file.ref, text, Object.keys(overrides).length ? overrides : undefined);
      navigate(`/runs/${run.id}`);
    } catch (e) { setActionErr(e); setBusy(false); }
  };
  const copy = async () => {
    try { await navigator.clipboard.writeText(text); setCopied(true); setTimeout(() => setCopied(false), 2000); }
    catch { setActionErr(new Error("Copying did not work in this browser; select the text and copy it instead.")); }
  };
  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: "application/yaml" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = file.path.split("/").pop() || "pipeline.yaml";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const discard = () => { setOlder(null); setRestored(false); setText(file.text); writeStored(name, file.ref, null); };
  const chooseBranch = (r: Ref) => {
    setPicking(false);
    if (r.name !== file.ref) navigate(`/projects/${encodeURIComponent(name)}/pipeline?ref=${encodeURIComponent(r.name)}`);
  };
  const draftTitle = !rights.may_draft
    ? (rights.drafts_allow === "none" ? `Draft runs are turned off for ${name}` : `Draft runs of ${name} need the ${rights.drafts_allow} role`)
    : !valid ? "Fix the problems first" : "Run this text against the branch's latest commit, without saving it";

  return (
    <div className="page page-wide">
      <header className="page-head">
        <div className="run-head-main">
          <h1>Pipeline of {name}</h1>
          <div className="run-meta">
            <button type="button" className="refbtn" onClick={() => setPicking(true)} aria-label={`Branch ${file.ref}; choose another`}>
              <BranchIcon /><span className="mono">{file.ref}</span><span aria-hidden="true">▾</span>
            </button>
            <span className="meta mono" title={file.sha}><CommitIcon />{shortSha(file.sha)}</span>
            <span className="meta">“{file.subject}” by {file.author}, {ago(file.committed_at)}</span>
            <span className="meta mono faint">{file.path}</span>
            {dirty
              ? <span className="pill pill-warn" role="status">Unsaved changes</span>
              : <span className="pill pill-idle" role="status">{file.exists ? `Same as ${shortSha(file.sha)}` : "Not in the repository yet"}</span>}
          </div>
        </div>
        <div className="run-actions">
          <button type="button" className="btn" disabled={busy || !rights.may_draft || !valid} title={draftTitle} onClick={runDraft}>
            {busy ? "Starting…" : "Run this draft"}
          </button>
          <button type="button" className="btn btn-primary" disabled={!rights.may_save || (!dirty && file.exists)}
            title={!rights.may_save ? "Saving is not available; see below" : !dirty && file.exists ? "Nothing has changed" : "Commit this text to the repository"}
            onClick={() => setSaving(true)}>Save…</button>
        </div>
      </header>

      {actionErr instanceof ApiError && actionErr.problems
        ? <ProblemList problems={actionErr.problems} title="The server found problems; nothing was started" onJump={(l) => editor.current?.goToLine(l)} />
        : <ErrorBox error={actionErr} />}
      {!file.exists && !file.configured && (
        <div className="callout callout-info">
          <strong>{file.ref} has no pipeline yet.</strong> Below is a starting point. Nothing is in the repository until you save it.
        </div>
      )}
      {!file.exists && file.configured && (
        <div className="callout callout-info">
          <strong>{file.ref} has no pipeline file, so {name} runs the pipeline configured for it on the server</strong>
          {project.pipeline && <>: the template <Mono>{project.pipeline.template}</Mono> of <Mono>{project.pipeline.library}@{project.pipeline.version}</Mono></>}.
          {" "}Below is that pipeline written as a file. Saving it puts the file in the repository, where it takes the configuration's place.
        </div>
      )}
      {rights.may_draft && libraryNames.length > 0 && (
        <details className="advanced card">
          <summary>Advanced: run the draft with other library versions{Object.keys(overrides).length > 0 && ` (${Object.keys(overrides).length} set)`}</summary>
          <OverridesEditor value={overrides} onChange={setOverrides} names={libraryNames} />
        </details>
      )}
      {restored && (
        <div className="callout callout-info callout-row">
          <span>Your unsaved edits from earlier in this tab are back.</span>
          <button type="button" className="btn btn-sm" onClick={discard}>Discard them</button>
        </div>
      )}
      {older && (
        <div className="callout callout-wait callout-row">
          <span>You have unsaved edits made at <Mono>{shortSha(older.sha)}</Mono>, but {file.ref} is now at <Mono>{shortSha(file.sha)}</Mono>. Using them would undo what changed since, unless you merge by hand.</span>
          <button type="button" className="btn btn-sm" onClick={() => { setText(older.text); setOlder(null); }}>Use my edits</button>
          <button type="button" className="btn btn-sm" onClick={discard}>Discard them</button>
        </div>
      )}
      <SaveNotice project={project} me={me?.user ?? ""} />
      {res?.ok && (res.warnings?.length ?? 0) > 0 && (
        <div className="callout callout-wait" role="status">
          {res.warnings!.map((w) => <div key={w}><strong>Warning.</strong> {w}</div>)}
        </div>
      )}

      <div className="split split-play">
        <section className="card card-flush editor-card">
          <div className="editor-bar">
            <span className="faint small">{checking ? "Checking…" : valid ? "No problems" : problems.length ? `${problems.length} problem${problems.length === 1 ? "" : "s"}` : ""}</span>
            <span className="editor-bar-actions">
              <KeyHelp line={cursorLine} />
              <button type="button" className="btn btn-sm" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
              <button type="button" className="btn btn-sm" onClick={download}>Download</button>
              <button type="button" className="btn btn-sm" disabled={!dirty} onClick={discard}>Discard edits</button>
            </span>
          </div>
          <PipelineEditor ref={editor} text={text} onChange={setText} problems={problems} label={`${file.path} on ${file.ref}`} onCursorLine={setCursorLine} />
        </section>
        <PipelinePreview res={res} error={checkErr} onJump={(l) => editor.current?.goToLine(l)}
          notes={<DraftNote project={project} />} />
      </div>
      <aside className="vr-aside" aria-label="Variables in effect for this pipeline">
        <h3>Variables in effect</h3>
        <p className="sub">What a template input or a <code>from:</code> binding in this pipeline would get, and from where. A lock above beats anything you write here.</p>
        <VariablesPanel level="project" scope={name} compact />
      </aside>

      {picking && (
        <Dialog title={`Edit the pipeline on another branch of ${name}`} onClose={() => setPicking(false)}>
          <p className="sub">Unsaved edits stay with the branch they were made on, in this tab.</p>
          <RefPicker project={name} defaultBranch={project.default_branch} chosen={file.ref} onChoose={chooseBranch} branchesOnly />
        </Dialog>
      )}
      {saving && (
        <SaveDialog project={project} file={file} text={text} valid={valid} checking={checking} problems={problems}
          onClose={() => setSaving(false)}
          onSaved={(r) => {
            writeStored(name, file.ref, null);
            if (r.direct) load(); else navigate(`/projects/${encodeURIComponent(name)}/pipeline?ref=${encodeURIComponent(r.branch)}`);
          }}
          onRebase={(keep) => { setSaving(false); load(keep ? text : undefined); if (!keep) writeStored(name, file.ref, null); }} />
      )}
    </div>
  );
}

// SaveNotice says why this page cannot save, when it cannot.
function SaveNotice({ project, me }: { project: Project; me: string }) {
  if (!project.push) {
    return (
      <div className="callout callout-info">
        <strong>Saving is not set up for {project.name}.</strong> An administrator can allow it in the project's settings
        (<a href={`/config/projects/${encodeURIComponent(project.name)}`}>Saving from the editor</a>); each save is then a commit on a new branch.
        Until then, use <em>Copy</em> or <em>Download</em> and commit the file yourself.
      </div>
    );
  }
  if (!project.editor.may_save) {
    return (
      <div className="callout callout-info">
        You can try changes here, but saving needs the operator role on {project.name}{me ? `, which ${me} does not have` : ""}.
        Use <em>Copy</em> or <em>Download</em> to pass your change on.
      </div>
    );
  }
  return null;
}

// DraftNote explains what a draft run of this project does differently.
function DraftNote({ project }: { project: Project }) {
  const r = project.editor;
  if (r.drafts_allow === "none") return <p className="sub">Draft runs are turned off for {project.name}.</p>;
  return (
    <p className="sub">
      <strong>Run this draft</strong> runs this text against the branch's latest commit without saving it.
      In a draft, a step with a <Mono>when</Mono> condition runs only if it lists <Mono>event: draft</Mono>
      {r.draft_secrets ? "." : <>, and steps that declare <Mono>secrets</Mono> fail without running: drafts of {project.name} may not read secrets.</>}
      {!r.may_draft && <> Starting one needs the {r.drafts_allow} role on {project.name}.</>}
    </p>
  );
}

function Dialog({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => { ref.current?.showModal(); }, []);
  return (
    <dialog ref={ref} className="dialog" onClose={onClose} aria-label={title}>
      <form method="dialog" onSubmit={(e) => e.preventDefault()}>
        <h2>{title}</h2>
        {children}
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={() => ref.current?.close()}>Close</button>
        </div>
      </form>
    </dialog>
  );
}

interface SaveProps {
  project: Project;
  file: PipelineFile;
  text: string;
  valid: boolean;
  checking: boolean;
  problems: Problem[];
  onClose: () => void;
  onSaved: (r: SaveResult) => void;
  /** The branch moved on: load it again, keeping the edits or not. */
  onRebase: (keepEdits: boolean) => void;
}

function SaveDialog({ project, file, text, valid, checking, problems, onClose, onSaved, onRebase }: SaveProps) {
  const dialog = useRef<HTMLDialogElement>(null);
  const directOK = (project.push?.allow_direct ?? []).some((p) => globMatch(p, file.ref));
  const [direct, setDirect] = useState(false);
  const [message, setMessage] = useState(file.exists ? "Update pipeline via Launchway" : "Add a Launchway pipeline");
  const [force, setForce] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState<SaveResult | null>(null);
  const [starting, setStarting] = useState(false);
  const me = useMe();
  useEffect(() => { dialog.current?.showModal(); }, []);

  const close = () => dialog.current?.close();
  const save = async () => {
    setBusy(true); setError(null);
    try {
      const r = await api.savePipeline(project.name, {
        base_ref: file.ref, base_sha: file.sha, pipeline: text, message, branch: direct ? file.ref : "", force: force || undefined,
      });
      setDone(r);
      onSaved(r);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const runBranch = async () => {
    if (!done) return;
    setStarting(true); setError(null);
    try {
      const run = await api.startRun(project.name, done.branch);
      close();
      navigate(`/runs/${run.id}`);
    } catch (e) { setError(e); setStarting(false); }
  };
  const stale = error instanceof ApiError && error.status === 409;
  const prefix = project.push?.branch_prefix ?? "launchway/";

  return (
    <dialog ref={dialog} className="dialog" onClose={onClose} aria-labelledby="save-title">
      {done ? (
        <form method="dialog" onSubmit={(e) => e.preventDefault()}>
          <h2 id="save-title">Saved</h2>
          <div className="callout callout-ok">
            Committed {done.path} to <strong className="mono">{done.branch}</strong> at <Mono>{shortSha(done.sha)}</Mono>
            {done.direct ? "." : <>, a new branch from {done.base_ref} at <Mono>{shortSha(done.base_sha)}</Mono>.</>}
          </div>
          {!done.direct && (done.pull_request_url
            ? <p><a href={done.pull_request_url} target="_blank" rel="noopener noreferrer">Open a pull request into {done.base_ref}</a> on the Git host.</p>
            : <p className="sub">Open a pull request from <Mono>{done.branch}</Mono> into <Mono>{done.base_ref}</Mono> on your Git host to have it reviewed.</p>)}
          <p className="sub">The editor now shows {done.branch}.</p>
          <ErrorBox error={error} />
          <div className="dialog-actions">
            <button type="button" className="btn" onClick={close}>Done</button>
            {project.editor.may_save && (
              <button type="button" className="btn btn-primary" disabled={starting} onClick={runBranch}>{starting ? "Starting…" : "Run this branch"}</button>
            )}
          </div>
        </form>
      ) : (
        <form method="dialog" onSubmit={(e) => { e.preventDefault(); save(); }}>
          <h2 id="save-title">Save the pipeline of {project.name}</h2>
          <p className="sub">Commits {file.path} to the repository as you{me?.user ? ` (${me.user})` : ""}, with Launchway as the committer. Only this file changes.</p>
          <fieldset className="field choices">
            <legend>Where</legend>
            <label className="choice">
              <input type="radio" name="target" checked={!direct} onChange={() => setDirect(false)} />
              <span>A new branch, <Mono>{prefix}{(me?.user ?? "you").toLowerCase()}/…</Mono>, from {file.ref}, to open a pull request from</span>
            </label>
            <label className={"choice" + (directOK ? "" : " choice-off")}>
              <input type="radio" name="target" checked={direct} disabled={!directOK} onChange={() => setDirect(true)} />
              <span>Directly to <Mono>{file.ref}</Mono>{!directOK && <span className="faint"> (not allowed for this branch)</span>}</span>
            </label>
          </fieldset>
          <label className="field">
            <span>Commit message</span>
            <textarea rows={3} value={message} maxLength={4000} onChange={(e) => setMessage(e.target.value)} />
          </label>
          {checking && <p className="sub" role="status">Checking the pipeline…</p>}
          {!valid && !checking && (
            <div className="callout callout-bad">
              The pipeline has {problems.length || "some"} problem{problems.length === 1 ? "" : "s"}. {project.editor.may_force
                ? <label className="choice"><input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} /> <span>Save it anyway (you are an admin of {project.name})</span></label>
                : "Fix them before saving."}
            </div>
          )}
          {error instanceof ApiError && error.problems
            ? <ProblemList problems={error.problems} title="The server found problems; nothing was saved" />
            : <ErrorBox error={error} />}
          {stale && (
            <div className="dialog-actions dialog-actions-left">
              <button type="button" className="btn" onClick={() => onRebase(true)}>Load the latest {file.ref}, keep my edits</button>
              <button type="button" className="btn" onClick={() => onRebase(false)}>Discard my edits</button>
            </div>
          )}
          <div className="dialog-actions">
            <button type="button" className="btn" onClick={close}>Cancel</button>
            <button type="submit" className="btn btn-primary" disabled={busy || checking || (!valid && !force)}>{busy ? "Saving…" : direct ? `Commit to ${file.ref}` : "Commit to a new branch"}</button>
          </div>
        </form>
      )}
    </dialog>
  );
}
