import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type Project } from "../../api";
import { browseApi } from "../../browseApi";
import { ErrorBox } from "../../components/ui";
import { TextInput } from "../../components/ConfigForm";
import { announce } from "../../kit/announce";
import { Button, LinkButton, PageHead, Sheet } from "../../kit/layout";
import { useHostedRepos } from "../hostedRepos";
import { terms } from "../../terms";
import { FORMATS, importApi, type Finding, type ImportResult, type Plan } from "../../importApi";
import "../../styles/import.css";
import { prose } from "../../prose";
const P = await prose("pages.import.ImportWizard");

// Import an existing pipeline: pick the pipeline files of another system (found in the Flight's repository, or pasted),
// read the proposal beside the old text with each finding anchored to its line, fix the common gaps, and commit the result
// as a pull request or download it. Nothing is stored by the importer; the Flight's own save does the commit.

type Source = { path: string; content: string };
const CLASS_WORDS: Record<string, string> = { exact: "Converted exactly", different: "Converted with a difference", manual: "Needs a person" };

export default function ImportPipelinePage({ name }: { name: string }) {
  const t = terms();
  const [project, setProject] = useState<Project | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { api.project(name).then(setProject).catch(setError); }, [name]);
  const repos = useHostedRepos();
  const repo = useMemo(() => repos?.find((r) => project && (project.url ?? "").length > 0 && ((project.url).endsWith("/" + r.path) || project.url.endsWith("/" + r.path + ".git") || project.url === r.path)) ?? null, [repos, project]);
  const [files, setFiles] = useState<Source[]>([]);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [dry, setDry] = useState(true);
  const [fixes, setFixes] = useState<{ templates: Record<string, string>; secretKeys: Record<string, string>; defaultImage: string }>({ templates: {}, secretKeys: {}, defaultImage: "" });
  const [format, setFormat] = useState("auto");

  const run = async (src: Source[], d: boolean, fx: typeof fixes, fmt: string) => {
    setBusy(true); setError(null);
    try {
      const res = await importApi.run({ format: fmt === "auto" ? "" : fmt, files: src, dry: d, name: project?.name, templates: fx.templates, secret_keys: fx.secretKeys, default_image: fx.defaultImage || undefined });
      setResult(res);
      announce(res.plans.length ? `The importer read ${res.plans.length} file${res.plans.length === 1 ? "" : "s"}.` : (res.message ?? "Nothing was found."));
    } catch (e) { setError(e); announce(e instanceof Error ? e.message : "That did not work.", "assertive"); }
    finally { setBusy(false); }
  };

  const head = <PageHead eyebrow={<a href={`/projects/${encodeURIComponent(name)}/pipeline`}>{name}</a>} title="Import an existing pipeline"
    sentence={t.say(P.t0)} />;
  if (error && !project) return <div className="page imp-page">{head}<ErrorBox error={error} /></div>;
  if (!project) return <div className="page"><div className="loading">Loading…</div></div>;

  const pick = (src: Source[]) => { setFiles(src); setFixes({ templates: {}, secretKeys: {}, defaultImage: "" }); void run(src, dry, { templates: {}, secretKeys: {}, defaultImage: "" }, format); };
  return (
    <div className="page imp-page">
      {head}
      <Chooser repoPath={repo?.path} branch={project.default_branch} onPick={pick} busy={busy} format={format} setFormat={setFormat} />
      <ErrorBox error={error} />
      {result && result.plans.length === 0 && <Sheet pad><p role="status">{result.message ?? "Nothing was found."}</p></Sheet>}
      {result?.plans.map((p) => (
        <PlanView key={p.files.join(",")} plan={p} sources={files} project={project} dry={dry} busy={busy}
          fixes={fixes}
          onDry={(d) => { setDry(d); void run(files, d, fixes, format); }}
          onFix={(fx) => { setFixes(fx); void run(files, dry, fx, format); }} />
      ))}
    </div>
  );
}

/** Step 1: which files. */
function Chooser({ repoPath, branch, onPick, busy, format, setFormat }: { repoPath?: string; branch: string; onPick: (s: Source[]) => void; busy: boolean; format: string; setFormat: (f: string) => void }) {
  const [pref, setMode] = useState<"repo" | "paste" | null>(null);
  const mode = !repoPath ? "paste" : pref ?? "repo";
  const [found, setFound] = useState<string[] | null>(null);
  const [chosen, setChosen] = useState<Record<string, boolean>>({});
  const [text, setText] = useState("");
  const [path, setPath] = useState(".github/workflows/ci.yml");
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (mode !== "repo" || !repoPath) return;
    let live = true;
    (async () => {
      const out: string[] = [];
      const ls = async (dir: string) => { try { const it = await browseApi.read(repoPath, branch, dir); return it.entries ?? []; } catch { return []; } };
      const top = await ls("");
      for (const e of top) {
        if (e.type === "file" && /(-ci\.ya?ml|-pipelines\.ya?ml|file|\.groovy)$/.test(e.name)) out.push(e.path);
        if (e.type === "dir" && e.name.startsWith(".")) {
          for (const sub of await ls(e.path)) {
            if (sub.type === "file" && /^config\.ya?ml$/.test(sub.name)) out.push(sub.path);
            if (sub.type === "dir" && sub.name === "workflows") for (const w of await ls(sub.path)) if (w.type === "file" && /\.ya?ml$/.test(w.name)) out.push(w.path);
          }
        }
      }
      if (live) { setFound(out); setChosen(Object.fromEntries(out.map((p) => [p, true]))); }
    })().catch((e) => live && setError(e));
    return () => { live = false; };
  }, [mode, repoPath, branch]);

  const read = async () => {
    setError(null);
    try {
      if (mode === "paste") { onPick([{ path: path.trim() || "pasted.yml", content: text }]); return; }
      const picked = (found ?? []).filter((p) => chosen[p]);
      const src: Source[] = [];
      for (const p of picked) { const it = await browseApi.read(repoPath!, branch, p); if (it.content !== undefined) src.push({ path: p, content: it.encoding === "base64" ? atob(it.content) : it.content }); }
      // YAML files the pipeline file may include are read too, so local includes resolve.
      onPick(src);
    } catch (e) { setError(e); }
  };
  return (
    <Sheet title="1. Choose the files" pad>
      <div role="group" aria-label="Where the files are" className="imp-tabs">
        <Button kind={mode === "repo" ? "pri" : undefined} sm onClick={() => setMode("repo")} disabled={!repoPath} aria-pressed={mode === "repo"}>From the repository</Button>
        <Button kind={mode === "paste" ? "pri" : undefined} sm onClick={() => setMode("paste")} aria-pressed={mode === "paste"}>Paste</Button>
        {!repoPath && <span className="faint small"> {P.t1}</span>}
      </div>
      {mode === "repo" && (found === null ? <p className="faint">Looking for pipeline files…</p> : found.length === 0 ? <p>{P.t2}<code>{branch}</code>. Paste one instead.</p> : (
        <fieldset className="imp-files"><legend>Pipeline files found on <code>{branch}</code></legend>
          {found.map((p) => <label key={p}><input type="checkbox" checked={!!chosen[p]} onChange={(e) => setChosen({ ...chosen, [p]: e.target.checked })} /> <code>{p}</code></label>)}
        </fieldset>
      ))}
      {mode === "paste" && (
        <>
          <label className="imp-field">Format{" "}
            <select value={format} onChange={(e) => { setFormat(e.target.value); const f = FORMATS.find((x) => x.id === e.target.value); if (f) setPath(f.path); }}>
              <option value="auto">Find out from the file name</option>
              {FORMATS.map((f) => <option key={f.id} value={f.id}>{f.title}</option>)}
            </select>
          </label>
          <TextInput label="File name in the repository" mono value={path} onChange={setPath} help={P.t3} />
          <label className="imp-field">The file's text
            <textarea className="mono imp-paste" rows={12} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} />
          </label>
        </>
      )}
      <ErrorBox error={error} />
      <div className="sq-actions"><Button kind="pri" onClick={read} disabled={busy || (mode === "paste" ? text.trim() === "" : !(found ?? []).some((p) => chosen[p])) } aria-busy={busy || undefined}>{busy ? "Reading…" : "Read the files"}</Button></div>
    </Sheet>
  );
}

/** Steps 2 and 3: the proposal beside the old text, fixes, and the commit. */
function PlanView({ plan, sources, project, dry, busy, fixes, onDry, onFix }: {
  plan: Plan; sources: Source[]; project: Project; dry: boolean; busy: boolean;
  fixes: { templates: Record<string, string>; secretKeys: Record<string, string>; defaultImage: string };
  onDry: (d: boolean) => void; onFix: (f: { templates: Record<string, string>; secretKeys: Record<string, string>; defaultImage: string }) => void;
}) {
  const src = sources.find((s) => s.path === plan.files[0]);
  const [mark, setMark] = useState<{ old?: number; neu?: number }>({});
  const counts = { exact: 0, different: 0, manual: 0 };
  plan.findings.forEach((f) => { counts[f.class]++; });
  const oldRef = useRef<HTMLPreElement>(null);
  const jump = (f: Finding) => {
    setMark({ old: f.line || undefined, neu: plan.steps.find((s) => s.job === f.step || s.step === f.step)?.line });
    oldRef.current?.querySelector(`[data-line="${f.line}"]`)?.scrollIntoView?.({ block: "center" });
  };
  const templated = plan.steps.filter((s) => s.template);
  const needsImage = plan.findings.some((f) => f.code === "write.image.default");
  return (
    <Sheet title={`2. Read it: ${plan.files.join(", ")}`} pad>
      <p role="status" className="imp-summary">
        <b>{counts.exact}</b> converted exactly, <b>{counts.different}</b> converted with a difference, <b className={counts.manual ? "bad" : ""}>{counts.manual}</b> need a person.
      </p>
      <label className="imp-switch"><input type="checkbox" checked={dry} onChange={(e) => onDry(e.target.checked)} disabled={busy} />{" "}
        {P.t4}<code>enable_delivery</code> is true</label>
      <div className="imp-sides">
        <Pane title="The old file" id="imp-old" text={src?.content ?? ""} marked={mark.old} preRef={oldRef} />
        <Pane title={P.t5} id="imp-new" text={plan.pipeline} marked={mark.neu} />
      </div>
      <h3 className="h-sm">Findings</h3>
      <FindingsList findings={plan.findings} onJump={jump} />
      {(plan.secrets.length > 0 || templated.length > 0 || needsImage) && (
        <fieldset className="imp-fixes"><legend>Fix the common gaps</legend>
          {plan.secrets.map((n) => (
            <TextInput key={n} label={`Secret ${n}: the name to create it under`} mono value={fixes.secretKeys[n] ?? n} onChange={(v) => onFix({ ...fixes, secretKeys: { ...fixes.secretKeys, [n]: v } })} help={P.t6} />
          ))}
          {templated.map((s) => (
            <label key={s.job} className="imp-field"><input type="checkbox" checked={fixes.templates[s.job] !== "none"} onChange={(e) => onFix({ ...fixes, templates: { ...fixes.templates, [s.job]: e.target.checked ? "" : "none" } })} />{" "}
              Step <code>{s.step}</code> uses the built-in template <code>launchway/{s.template}</code> {P.t7}</label>
          ))}
          {needsImage && <TextInput label={P.t8} mono value={fixes.defaultImage} onChange={(v) => onFix({ ...fixes, defaultImage: v })} placeholder={P.t9} help={P.t10} />}
        </fieldset>
      )}
      <Commit plan={plan} project={project} />
    </Sheet>
  );
}

function Pane({ title, id, text, marked, preRef }: { title: string; id: string; text: string; marked?: number; preRef?: React.Ref<HTMLPreElement> }) {
  const lines = text.split("\n");
  return (
    <div className="imp-pane">
      <h3 className="h-sm" id={id}>{title}</h3>
      {/* A scrollable region must be reachable by keyboard. */}
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex */}
      <pre ref={preRef} tabIndex={0} aria-labelledby={id} className="imp-code">
        {lines.map((l, i) => <span key={i} data-line={i + 1} className={"imp-line" + (marked === i + 1 ? " on" : "")}><span className="imp-no" aria-hidden="true">{i + 1}</span>{l}{"\n"}</span>)}
      </pre>
    </div>
  );
}

function FindingsList({ findings, onJump }: { findings: Finding[]; onJump: (f: Finding) => void }) {
  const [shown, setShown] = useState<"attention" | "all">("attention");
  const list = findings.filter((f) => shown === "all" || f.class !== "exact");
  return (
    <>
      <div role="group" aria-label="Which findings to show" className="imp-tabs">
        <Button sm kind={shown === "attention" ? "pri" : undefined} aria-pressed={shown === "attention"} onClick={() => setShown("attention")}>Differences and gaps</Button>
        <Button sm kind={shown === "all" ? "pri" : undefined} aria-pressed={shown === "all"} onClick={() => setShown("all")}>Everything</Button>
      </div>
      {list.length === 0 ? <p>{P.t11}</p> : (
        <ul className="imp-findings" aria-label="Findings">
          {list.map((f, i) => (
            <li key={i} className={"imp-f imp-" + f.class}>
              <b>{CLASS_WORDS[f.class]}.</b> {f.message}
              {f.next && <> <span className="imp-next">Next: {f.next}</span></>}{" "}
              {f.line > 0 && <button type="button" className="linklike" onClick={() => onJump(f)}>Show line {f.line}</button>}
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

/** Step 3: commit as a pull request, or download. */
function Commit({ plan, project }: { plan: Plan; project: Project }) {
  const [state, setState] = useState<{ busy: boolean; url?: string; branch?: string; error?: unknown }>({ busy: false });
  const may = project.editor?.may_save && project.push !== null;
  const open = async () => {
    setState({ busy: true });
    try {
      const f = await api.pipeline(project.name, project.default_branch);
      const r = await api.savePipeline(project.name, { base_ref: f.ref, base_sha: f.sha, pipeline: plan.pipeline, message: P.t12 + plan.files.join(", "), branch: "" });
      setState({ busy: false, url: r.pull_request_url, branch: r.branch });
      announce(`Saved on the branch ${r.branch}.`);
    } catch (e) { setState({ busy: false, error: e }); announce(e instanceof ApiError ? e.message : "That did not work.", "assertive"); }
  };
  const download = () => {
    const url = URL.createObjectURL(new Blob([plan.pipeline], { type: "text/yaml" }));
    const a = document.createElement("a"); a.href = url; a.download = "pipeline.yaml"; a.click(); URL.revokeObjectURL(url);
  };
  return (
    <section aria-label="3. Commit the result" className="imp-commit">
      <h3 className="h-sm">3. Commit the result</h3>
      <p className="faint">{P.t13}<code>.launchway/pipeline.yaml</code>.</p>
      <div className="sq-actions">
        <Button kind="pri" onClick={open} disabled={!may || state.busy} aria-busy={state.busy || undefined} title={may ? "" : P.t14}>{state.busy ? "Saving…" : P.t15}</Button>
        <Button onClick={download}>Download the file</Button>
        <LinkButton href={`/projects/${encodeURIComponent(project.name)}/pipeline/edit`}>Open the editor</LinkButton>
      </div>
      {!may && <p className="faint small">{P.t16}</p>}
      <ErrorBox error={state.error} />
      {state.branch && <p role="status">Saved on the branch <code>{state.branch}</code>.{state.url ? <> <a href={state.url}>Open the pull request</a>.</> : null}</p>}
    </section>
  );
}
