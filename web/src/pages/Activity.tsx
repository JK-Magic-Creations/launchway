import { lazy, Suspense, useCallback, useMemo, useState } from "react";
import { api, type Project, type RunSummary } from "../api";
import { launchApi, type LaunchLineView } from "../api/launch";
import { EntityLink } from "../components/EntityLink";
import { ErrorBox, Link } from "../components/ui";
import { ago, elapsed, refLabel, shortSha, starterLabel } from "../format";
import { runSentence, trendOf } from "../guided/decide";
import { useLoad } from "../guided/useLoad";
import { LaunchLine, toStations, type LineStation } from "../kit/LaunchLine";
import { EmptyState, PageHead, Sheet } from "../kit/layout";
import { Status, stateOf, type State } from "../kit/Status";
import { RateText, TrendStrip } from "../kit/Trend";
import { useNow, useStream } from "../live";
import { approx } from "../estimate";
import { onLink } from "../router";

const QueuePanel = lazy(() => import("./QueuePanel"));

const FILTERS: { key: string; label: string }[] = [
  { key: "", label: "All" }, { key: "running", label: "Running" }, { key: "failed", label: "Failed" }, { key: "succeeded", label: "Passed" },
];

const isDone = (s: string) => s === "succeeded" || s === "failed" || s === "canceled";
const TONE: Record<string, State> = { bad: "bad", wait: "wait", run: "run", ok: "ok", idle: "skip" };

/** The stations of a run that has no launch line yet: its own steps. */
function stepsAsStations(r: RunSummary): LineStation[] {
  return r.steps.map((s) => ({ key: s.id, label: s.id, state: r.waiting?.includes(s.id) ? "wait" : stateOf(s.status) }));
}

function toneOf(r: RunSummary): "bad" | "wait" | "run" | "ok" | "idle" {
  if (r.status === "failed") return "bad";
  if (!isDone(r.status) && (r.waiting?.length ?? 0) > 0) return "wait";
  if (r.status === "running") return "run";
  return r.status === "succeeded" ? "ok" : "idle";
}

/** Activity answers: what has happened, and what is happening. Each run is one row with its launch line,
 * one sentence about where it is and who started it; a chosen project shows how its runs have been going. */
export function ActivityPage() {
  const [project, setProject] = useState("");
  const [status, setStatus] = useState("");
  const [lane, setLane] = useState("");
  const now = useNow(1000, true);
  const runsFn = useCallback((bg: boolean) => api.runs({ project, status, lane, limit: 100 }, bg), [project, status, lane]);
  const runs = useLoad(runsFn);
  const trendFn = useCallback((bg: boolean) => (project ? api.runs({ project, limit: 30 }, bg) : Promise.resolve<RunSummary[]>([])), [project]);
  const trendRuns = useLoad(trendFn);
  const linesFn = useCallback((bg: boolean) => launchApi.lines(50, bg, project), [project]);
  const lines = useLoad(linesFn);
  const projectsFn = useCallback(() => api.projects(), []);
  const projects = useLoad<Project[]>(projectsFn);
  const live = useStream("/api/v1/stream", { run: () => { runs.reload(); lines.reload(); trendRuns.reload(); } }, () => { runs.reload(); lines.reload(); });

  const byRun = useMemo(() => new Map((lines.data ?? []).map((l) => [l.run, l])), [lines.data]);
  const list = runs.data;
  const laneNames = useMemo(() => Array.from(new Set((list ?? []).flatMap((r) => (r.lane ? [r.lane.name] : [])))).sort(), [list]);
  const counts = useMemo(() => {
    const l = list ?? [];
    return { running: l.filter((r) => !isDone(r.status) && (r.waiting?.length ?? 0) === 0).length, waiting: l.filter((r) => !isDone(r.status) && (r.waiting?.length ?? 0) > 0).length, failed: l.filter((r) => r.status === "failed").length };
  }, [list]);
  const filtered = !!(status || project || lane);
  const trend = trendOf(trendRuns.data ?? []);

  const sentence = list === null ? "Loading the runs." : list.length === 0 ? (filtered ? "No run matches these filters." : "Nothing has run yet.")
    : <>{counts.waiting > 0 && <><b>{counts.waiting} {counts.waiting === 1 ? "run is" : "runs are"} waiting for a person.</b> </>}
      {counts.failed > 0 ? <><b>{counts.failed} failed</b> in this list. </> : "No failures in this list. "}
      {counts.running > 0 ? `${counts.running} running now.` : "Nothing is running now."}</>;

  return (
    <div className="page">
      <PageHead eyebrow="Activity" title="Runs" sentence={sentence}
        actions={<span className={"vchip " + (live ? "run" : "")}><span className={live ? "st run" : "st idle"} aria-hidden="true" style={{ width: 12, height: 12 }} />{live ? "Live" : "Connecting"}</span>} />

      <div className="toolbar2">
        <div className="pillbtns" role="group" aria-label="Filter by state">
          {FILTERS.map((f) => <button key={f.key} type="button" aria-pressed={status === f.key} onClick={() => setStatus(f.key)}>{f.label}</button>)}
        </div>
        {(projects.data?.length ?? 0) > 1 && (
          <label className="select"><span className="visually-hidden">Project</span>
            <select value={project} onChange={(e) => setProject(e.target.value)}>
              <option value="">All projects</option>
              {projects.data!.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
            </select>
          </label>
        )}
        {laneNames.length > 0 && (
          <label className="select"><span className="visually-hidden">Lane</span>
            <select value={lane} onChange={(e) => setLane(e.target.value)}>
              <option value="">All lanes</option>
              {laneNames.map((n) => <option key={n} value={n}>lane {n}</option>)}
            </select>
          </label>
        )}
      </div>

      {project && trendRuns.data && trendRuns.data.length > 0 && (
        <Sheet title={<>How <EntityLink kind="project" name={project} /> has been doing</>} actions={<RateText trend={trend} />}>
          <div className="sh-b row wrap">
            <TrendStrip trend={trend} label={`The last ${trend.ticks.length} runs of ${project}`} />
            <span className="faint small">oldest to newest{trend.median ? `, a typical run takes ${durationShort(trend.median)}` : ""}</span>
          </div>
        </Sheet>
      )}

      <Suspense fallback={null}><QueuePanel project={project} /></Suspense>
      <ErrorBox error={runs.error} />
      {list === null && !runs.error && <div className="loading">Loading…</div>}
      {list && list.length === 0 && (
        <Sheet><EmptyState title={filtered ? "No runs match" : "No runs yet"}>
          {filtered ? "Try another filter." : <>Start one from <Link href="/projects">Code</Link>, push to a watched branch, or try <span className="mono">launchway run</span> in a repository.</>}
        </EmptyState></Sheet>
      )}
      {list && list.length > 0 && (
        <Sheet title="Runs" actions={<span className="faint small">Newest first. Each dot is a place the change has to pass.</span>}>
          {list.map((r) => <RunRow key={r.id} r={r} line={byRun.get(r.id)} now={now} />)}
        </Sheet>
      )}
    </div>
  );
}

function durationShort(ms: number): string {
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

export function RunRow({ r, line, now }: { r: RunSummary; line?: LaunchLineView; now: number }) {
  const tone = line?.tone ?? toneOf(r);
  const stations = line ? toStations(line.stations) : stepsAsStations(r);
  const waits = !isDone(r.status) && (r.waiting?.length ?? 0) > 0;
  const where = refLabel(r);
  const took = elapsed(r.started_at || r.created_at, r.finished_at, now);
  const sentence = line?.now && r.status !== "succeeded" ? line.now : runSentence(r, took);
  return (
    <a href={waits ? `/runs/${r.id}?step=${encodeURIComponent(r.waiting[0])}` : `/runs/${r.id}`} onClick={onLink} className={`brow ${tone === "bad" ? "bad" : tone === "wait" ? "wait" : ""}`}>
      <div className="ct">
        <b>{r.message || r.pipeline}</b>
        <span>{r.project} · run #{r.number}{where ? ` · ${where}` : ""}{r.sha ? ` · ${shortSha(r.sha)}` : ""}</span>
        <span>{starterLabel(r)} · {ago(r.created_at, now)}{r.status === "running" && r.estimate?.known && (r.estimate.remaining_ms ?? 0) > 0 ? ` · about ${approx(Math.max(0, r.estimate.remaining_ms! - (now - r.estimate.as_of)))} left` : ""}{r.draft ? " · draft" : ""}{r.rerun_of_number ? ` · re-run of #${r.rerun_of_number}` : ""}</span>
      </div>
      <LaunchLine stations={stations} compact />
      <div className="now"><Status state={TONE[tone] ?? "skip"} /><span>{sentence}</span>
        {r.tests && <span className="faint small">{r.tests.failed > 0 ? `${r.tests.failed} of ${r.tests.total} tests failed` : `${r.tests.total} tests`}</span>}
        {r.findings && r.findings.total > 0 && <span className="faint small">{r.findings.critical + r.findings.high > 0 ? `${r.findings.critical + r.findings.high} serious findings` : `${r.findings.total} findings`}</span>}
      </div>
    </a>
  );
}
