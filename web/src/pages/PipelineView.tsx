import { useCallback, useEffect, useState } from "react";
import { api, type PipelineFile, type Project } from "../api";
import { launchApi } from "../api/launch";
import { PipelinePreview } from "../components/PipelinePreview";
import { ErrorBox, Mono } from "../components/ui";
import { ago, shortSha } from "../format";
import { trendOf } from "../guided/decide";
import { useLoad } from "../guided/useLoad";
import { EmptyState, LinkButton, Sheet } from "../kit/layout";
import { Tabs } from "../kit/Tabs";
import { RateText, TrendStrip } from "../kit/Trend";
import { useCrumbLabel } from "../nav/crumbs";
import { useNow } from "../live";
import { useValidation } from "../validation";
import { RunRow } from "./Activity";
import { ProjectHead } from "./PipelineChrome";
import { terms } from "../terms";

type View = "live" | "text" | "draw" | "history";
const VIEWS: View[] = ["live", "text", "draw", "history"];

function useQuery(): URLSearchParams {
  const [q, setQ] = useState(location.search);
  useEffect(() => {
    const on = () => setQ(location.search);
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  return new URLSearchParams(q);
}

/** One pipeline: Live (drawn from the file in Git), Text (the file itself), Draw (not built yet) and the
 * history of its runs. Nothing here is a copy of the file: changing it saves a commit through the editor. */
export function PipelineViewPage({ name }: { name: string }) {
  const q = useQuery();
  const asked = q.get("view") as View | null;
  const view: View = asked && VIEWS.includes(asked) ? asked : "live";
  const ref = q.get("ref") ?? "";
  useCrumbLabel(name);
  const now = useNow(30000);

  const fileFn = useCallback(async () => {
    const p: Project = await api.project(name);
    const f: PipelineFile = await api.pipeline(name, ref || p.default_branch);
    return { p, f };
  }, [name, ref]);
  const loaded = useLoad(fileFn);
  const file = loaded.data?.f ?? null;
  const { res, error: checkError } = useValidation(file ? file.text : null, { project: name });
  const enc = encodeURIComponent(name);
  const editHref = `/projects/${enc}/pipeline/edit?ref=${encodeURIComponent(file?.ref ?? loaded.data?.p.default_branch ?? "")}`;
  const setView = (v: string) => {
    const n = new URLSearchParams(location.search);
    n.set("view", v);
    history.replaceState(null, "", `${location.pathname}?${n}`);
    window.dispatchEvent(new PopStateEvent("popstate"));
  };

  const sentence = view === "history" ? `Every run of ${name}, newest first.`
    : "The steps a run does, read from the file in the repository. Nothing here is a copy: the editor saves a change as a Git commit.";
  return (
    <div className="page">
      <ProjectHead name={name} on={view === "history" ? "history" : "pipeline"} title={view === "history" ? <>Runs of {name}</> : <>Pipeline of {name}</>} sentence={sentence}
        actions={<><LinkButton href={editHref} kind="pri" icon="edit">Edit the pipeline</LinkButton><LinkButton href="/playground" icon="play">Try it without saving</LinkButton></>} />
      <ErrorBox error={loaded.error} />
      {view !== "history" && (
        <>
          <Tabs label="Ways to look at the pipeline" on={view} onChange={setView}
            tabs={[{ id: "live", label: "Live" }, { id: "text", label: "Text" }, { id: "draw", label: "Draw", soon: true }]} />
          {!file && !loaded.error && <div className="loading">Loading…</div>}
          {file && <FileFacts file={file} />}
          <div role="tabpanel" aria-labelledby={`tab-${view}`} className="stack">
            {file && view === "live" && <PipelinePreview res={res} error={checkError} />}
            {file && view === "text" && <TextView file={file} />}
            {view === "draw" && (
              <Sheet><EmptyState title="Drawing a pipeline is not built yet" action={<LinkButton href={editHref} kind="pri" icon="edit">Open the editor</LinkButton>}>
                The Live tab already draws the file as boxes you can open. To change the pipeline, use the editor: it checks the text as you type and saves the change to Git as a commit.
              </EmptyState></Sheet>
            )}
          </div>
        </>
      )}
      {view === "history" && <History name={name} now={now} />}
    </div>
  );
}

function FileFacts({ file }: { file: PipelineFile }) {
  return (
    <p className="faint small" style={{ margin: 0 }}>
      {file.exists ? <>Kept in the repository as <Mono>{file.path}</Mono> on <Mono>{file.ref}</Mono>, at commit <Mono>{shortSha(file.sha)}</Mono> ({ago(file.committed_at)}{file.author ? `, by ${file.author}` : ""}).</>
        : file.configured ? <>The repository has no pipeline file, so the pipeline configured on the server runs instead. Editing it saves a new file to the repository.</>
        : <>The repository has no pipeline yet. Open the editor to write the first one; it is saved to Git as a commit.</>}
    </p>
  );
}

function TextView({ file }: { file: PipelineFile }) {
  const lines = file.text.replace(/\n$/, "").split("\n");
  return (
    <Sheet title={file.exists ? file.path : "Pipeline text"} actions={<span className="faint small">read only</span>}>
      <div className="sh-b">
        <pre className="textview" aria-label={`The text of ${file.path || "the pipeline"}, ${lines.length} lines`}>
          {lines.map((l, i) => <span key={i} className="ln"><span aria-hidden="true">{i + 1}</span><span>{l || " "}</span></span>)}
        </pre>
      </div>
    </Sheet>
  );
}

function History({ name, now }: { name: string; now: number }) {
  const runsFn = useCallback((bg: boolean) => api.runs({ project: name, limit: 50 }, bg), [name]);
  const runs = useLoad(runsFn, 10000);
  const linesFn = useCallback((bg: boolean) => launchApi.lines(50, bg, name), [name]);
  const lines = useLoad(linesFn, 10000);
  const t = trendOf(runs.data ?? [], 40);
  const by = new Map((lines.data ?? []).map((l) => [l.run, l]));
  return (
    <>
      <ErrorBox error={runs.error} />
      {runs.data && runs.data.length === 0 && <Sheet><EmptyState title={terms().say("No {runs} yet")} action={<LinkButton href={`/projects/${encodeURIComponent(name)}`} icon="play">Start one from the project</LinkButton>}>Every run of this pipeline will be listed here, with its launch line.</EmptyState></Sheet>}
      {runs.data && runs.data.length > 0 && (
        <>
          <Sheet title="How it has been going" actions={<RateText trend={t} />}>
            <div className="sh-b row wrap"><TrendStrip trend={t} label={`The last ${t.ticks.length} runs of ${name}`} /><span className="faint small">oldest to newest; {t.passed} passed, {t.failed} failed</span></div>
          </Sheet>
          <Sheet title={terms().say("{Runs}")}>{runs.data.map((r) => <RunRow key={r.id} r={r} line={by.get(r.id)} now={now} />)}</Sheet>
        </>
      )}
    </>
  );
}
