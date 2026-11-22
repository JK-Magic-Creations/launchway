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
import { setUrl, useViewTitle } from "../urlState";
import { CopyLink } from "../components/CopyLink";
import { ProjectHead } from "./PipelineChrome";
import { terms } from "../terms";
import { onLink } from "../router";
import { prose } from "../prose";
import { DivLoading, DivPage, SpanFaintSmall } from "../kit/bits";
const P = await prose("pages.PipelineView");

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
  // The view is in the address (?view=history), a history entry each, so Back returns to the one before.
  const setView = (v: string) => { setUrl({ view: v === "live" ? null : v }, "push"); window.dispatchEvent(new PopStateEvent("popstate")); };
  useViewTitle(view === "live" ? name : `${view[0].toUpperCase()}${view.slice(1)} · ${name}`);

  const sentence = view === "history" ? `Every run of ${name}, newest first.`
    : P.t0;
  return (
    <DivPage>
      <ProjectHead name={name} on={view === "history" ? "history" : "pipeline"} title={view === "history" ? <>Runs of {name}</> : <>Pipeline of {name}</>} sentence={sentence}
        actions={<><CopyLink /><LinkButton href={editHref} kind="pri" icon="edit">Edit the pipeline</LinkButton><LinkButton href="/playground" icon="play">Try it without saving</LinkButton></>} />
      <ErrorBox error={loaded.error} />
      {view !== "history" && (
        <>
          <Tabs label="Ways to look at the pipeline" on={view} onChange={setView}
            tabs={[{ id: "live", label: "Live" }, { id: "text", label: "Text" }, { id: "history", label: "History" }, { id: "draw", label: "Draw", soon: true }]} />
          {!file && !loaded.error && <DivLoading>Loading…</DivLoading>}
          {file && <FileFacts file={file} name={name} />}
          <div role="tabpanel" aria-labelledby={`tab-${view}`} className="stack">
            {file && view === "live" && <PipelinePreview res={res} error={checkError} />}
            {file && view === "text" && <TextView file={file} />}
            {view === "draw" && (
              <Sheet><EmptyState title={P.t1} action={<LinkButton href={editHref} kind="pri" icon="edit">Open the editor</LinkButton>}>
                {P.t2}</EmptyState></Sheet>
            )}
          </div>
        </>
      )}
      {view === "history" && <History name={name} now={now} />}
    </DivPage>
  );
}

function FileFacts({ file, name }: { file: PipelineFile; name: string }) {
  return (
    <p className="faint small" style={{ margin: 0 }}>
      {file.exists ? <>Kept in the repository as <Mono>{file.path}</Mono> on <Mono>{file.ref}</Mono>, at commit <Mono>{shortSha(file.sha)}</Mono> ({ago(file.committed_at)}{file.author ? `, by ${file.author}` : ""}).</>
        : file.configured ? <>{P.t3}</>
        : <>{P.t4}<a href={`/projects/${encodeURIComponent(name)}/import`} onClick={onLink}>import an existing pipeline</a> from another system's files.</>}
    </p>
  );
}

function TextView({ file }: { file: PipelineFile }) {
  const lines = file.text.replace(/\n$/, "").split("\n");
  return (
    <Sheet title={file.exists ? file.path : "Pipeline text"} actions={<SpanFaintSmall>read only</SpanFaintSmall>}>
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
      {runs.data && runs.data.length === 0 && <Sheet><EmptyState title={terms().say("No {runs} yet")} action={<LinkButton href={`/projects/${encodeURIComponent(name)}`} icon="play">{terms().say("Start one from the {project}")}</LinkButton>}>{P.t5}</EmptyState></Sheet>}
      {runs.data && runs.data.length > 0 && (
        <>
          <Sheet title="How it has been going" actions={<RateText trend={t} />}>
            <div className="sh-b row wrap"><TrendStrip trend={t} label={`The last ${t.ticks.length} runs of ${name}`} /><SpanFaintSmall>oldest to newest; {t.passed} passed, {t.failed} failed</SpanFaintSmall></div>
          </Sheet>
          <Sheet title={terms().say("{Runs}")}>{runs.data.map((r) => <RunRow key={r.id} r={r} line={by.get(r.id)} now={now} />)}</Sheet>
        </>
      )}
    </>
  );
}
