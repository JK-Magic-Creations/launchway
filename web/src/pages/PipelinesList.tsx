import { lazy, Suspense, useCallback, useMemo } from "react";
import { api, type Project, type RunSummary } from "../api";
import { useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { ago, elapsed } from "../format";
import { trendOf } from "../guided/decide";
import { useInstallation } from "../guided/facts";
import { startsSentence } from "../guided/pipelines";
import { useLoad } from "../guided/useLoad";
import { Band, EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { Status, stateOf } from "../kit/Status";
import { RateText, TrendStrip } from "../kit/Trend";
import { useNow } from "../live";
import { terms } from "../terms";

// The grouped, searchable view loads after the first paint; the flat list below is what shows meanwhile (and keeps the page height).
const PipelinesExplorer = lazy(() => import("../explorer/PipelinesExplorer"));

/** What starts each project's pipeline, read from the pipeline file at the tip of its default branch. */
function useStarts(projects: Project[]): Record<string, string> | null {
  const key = JSON.stringify(projects.map((p) => [p.name, p.default_branch]));
  const fn = useCallback(async () => {
    const out: Record<string, string> = {};
    await Promise.all((JSON.parse(key) as [string, string][]).map(async ([name, branch]) => {
      try {
        const f = await api.pipeline(name, branch);
        const v = await api.validate(f.text, { project: name });
        out[name] = v.ok ? startsSentence(v.graph) : "The pipeline file has a problem: open it to see where.";
      } catch { out[name] = "The pipeline file could not be read now."; }
    }));
    return out;
  }, [key]);
  return useLoad(fn).data;
}

/** The pipelines: one row for each project's pipeline, with what starts it, how its last run went and how its
 * recent runs trend. Everything here is read from the projects, their pipeline files and their runs. */
export function PipelinesPage() {
  const inst = useInstallation();
  const me = useMe();
  const now = useNow(15000);
  const starts = useStarts(inst.projects);
  const runsFn = useCallback((bg: boolean) => api.runs({ limit: 100 }, bg), []);
  const all = useLoad(runsFn, 15000).data;
  const byProject = useMemo(() => {
    const m = new Map<string, RunSummary[]>();
    for (const r of all ?? []) m.set(r.project, [...(m.get(r.project) ?? []), r]);
    return m;
  }, [all]);
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  const rows = inst.projects;
  const flat = flatList(rows, byProject, starts, now);
  const failing = rows.filter((p) => p.last_run?.status === "failed").length;
  const running = rows.filter((p) => p.last_run?.status === "running" || p.last_run?.status === "queued").length;
  return (
    <div className="page">
      <PageHead eyebrow="Pipelines" title="Pipelines"
        sentence={rows.length === 0 ? "A pipeline is a set of steps that runs when something happens. None exists yet." :
          failing > 0 ? <><b>{failing} {failing === 1 ? "pipeline stopped" : "pipelines stopped"}</b> {terms().say("on its last {run}. The others are fine.")}</> : "Every pipeline's last run is fine or still going."}
        actions={me?.runners ? <LinkButton href="/playground" icon="play">Try a pipeline without saving</LinkButton> : undefined} />
      {rows.length > 0 && (
        <Band cells={[
          { key: "n", label: "Pipelines", value: rows.length, foot: "one for each project" },
          { key: "run", label: "Running now", value: running, foot: running ? "work is moving" : "quiet" },
          { key: "bad", label: "Stopped", value: failing, foot: failing ? terms().say("last {run} failed") : "none", loud: failing ? "bad" : undefined },
        ]} />
      )}
      {rows.length === 0 ? (
        <Sheet><EmptyState title="No pipelines yet" action={me?.admin ? <LinkButton href="/config/wizard/connect-repository" kind="pri" icon="plus">Add a project</LinkButton> : undefined}>
          {terms().say("Each project has a pipeline: its steps are read from the project's Git repository. Add a project and its pipeline appears here, with what starts it and every {run}'s result.")}
        </EmptyState></Sheet>
      ) : (
        <Suspense fallback={flat}><PipelinesExplorer projects={rows} runs={all ?? []} now={now} me={me?.user ?? ""} /></Suspense>
      )}
    </div>
  );
}

/* kept as the placeholder while the Explorer loads */
function flatList(rows: Project[], byProject: Map<string, RunSummary[]>, starts: Record<string, string> | null, now: number) {
  return (
        <Sheet title="Every pipeline">
          <ul className="rowlist">
            {rows.map((p) => {
              const r = p.last_run;
              const t = trendOf(byProject.get(p.name) ?? []);
              return (
                <li key={p.name} className={r?.status === "failed" ? "bad" : ""}>
                  <div className="main">
                    <div className="nm"><EntityLink kind="pipeline" name={p.name} /></div>
                    <div className="sb">{starts === null ? "Reading the file…" : starts[p.name] ?? ""}</div>
                  </div>
                  <div className="cell" style={{ minWidth: 200 }}>
                    {r ? <><Status state={stateOf(r.status)} /><span><EntityLink kind="run" name={`#${r.number}`} id={r.id} /> {r.status === "failed" ? "failed" : r.status === "succeeded" ? "passed" : r.status}, {ago(r.created_at, now)}<br />
                      <span className="faint small">{elapsed(r.started_at || r.created_at, r.finished_at, now) ? `took ${elapsed(r.started_at || r.created_at, r.finished_at, now)}` : ""}</span></span></> : <span className="faint">Not run yet</span>}
                  </div>
                  <div className="cell" style={{ flexDirection: "column", alignItems: "flex-start", gap: 2 }}><TrendStrip trend={t} label={`The last runs of ${p.name}`} /><RateText trend={t} /></div>
                  <LinkButton href={`/projects/${encodeURIComponent(p.name)}/pipeline`} sm>Open</LinkButton>
                </li>
              );
            })}
          </ul>
        </Sheet>
  );
}
