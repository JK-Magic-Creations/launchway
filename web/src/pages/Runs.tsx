import { useCallback, useEffect, useState } from "react";
import { api, type LaneInfo, type Project, type RunStatus, type RunSummary } from "../api";
import { ago, elapsed, refLabel, shortSha, starterLabel } from "../format";
import { useNow, useStream } from "../live";
import { BranchIcon, CommitIcon, Empty, ErrorBox, Link, StatusIcon, StepStrip, statusLabel } from "../components/ui";
import { TestCounts } from "../components/Tests";
import { ArtifactCount, FindingCountBadge } from "../components/Security";

const FILTERS: { key: string; label: string }[] = [
  { key: "", label: "All" },
  { key: "running", label: "Running" },
  { key: "failed", label: "Failed" },
  { key: "succeeded", label: "Succeeded" },
];

export function RunsPage() {
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [project, setProject] = useState("");
  const [status, setStatus] = useState("");
  const [lane, setLane] = useState("");
  const [laneNames, setLaneNames] = useState<string[]>([]);
  const [error, setError] = useState<unknown>(null);
  const now = useNow(1000, !!runs?.some((r) => !isDone(r.status)));

  const load = useCallback((background = false) => {
    api.runs({ project, status, lane, limit: 100 }, background).then((r) => {
      setRuns(r); setError(null);
      // The lanes seen so far, for the filter, which stays while one is chosen.
      setLaneNames((prev) => Array.from(new Set([...prev, ...r.flatMap((x) => (x.lane ? [x.lane.name] : []))])).sort());
    }).catch(setError);
  }, [project, status, lane]);

  useEffect(() => load(), [load]);
  useEffect(() => { api.projects().then(setProjects).catch(() => {}); }, []);

  const live = useStream("/api/v1/stream", {
    run: (d) => {
      const r = d as RunSummary;
      if (project && r.project !== project) return;
      if (lane && r.lane?.name !== lane) return;
      setRuns((prev) => {
        if (!prev) return prev;
        const i = prev.findIndex((x) => x.id === r.id);
        if (status && r.status !== status) return i >= 0 ? prev.filter((x) => x.id !== r.id) : prev;
        if (i >= 0) { const next = prev.slice(); next[i] = r; return next; }
        return [r, ...prev];
      });
    },
  }, () => load(true));

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Runs</h1>
          <p className="sub">Every run on every project, newest first. Updates live.</p>
        </div>
        <span className={"live " + (live ? "live-on" : "")}><span className="dot" />{live ? "Live" : "Connecting…"}</span>
      </header>

      <div className="toolbar">
        <div className="chips" role="group" aria-label="Filter by status">
          {FILTERS.map((f) => (
            <button key={f.key} type="button" className={"chip" + (status === f.key ? " chip-on" : "")}
              aria-pressed={status === f.key} onClick={() => setStatus(f.key)}>{f.label}</button>
          ))}
        </div>
        {laneNames.length > 0 && (
          <label className="select">
            <span className="visually-hidden">Lane</span>
            <select value={lane} onChange={(e) => setLane(e.target.value)}>
              <option value="">All lanes</option>
              {laneNames.map((n) => <option key={n} value={n}>lane {n}</option>)}
            </select>
          </label>
        )}
        {projects.length > 1 && (
          <label className="select">
            <span className="visually-hidden">Project</span>
            <select value={project} onChange={(e) => setProject(e.target.value)}>
              <option value="">All projects</option>
              {projects.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
            </select>
          </label>
        )}
      </div>

      <ErrorBox error={error} />
      {runs === null && !error && <div className="loading">Loading…</div>}
      {runs && runs.length === 0 && (
        <Empty title={status || project || lane ? "No runs match" : "No runs yet"}>
          {status || project || lane ? "Try another filter." : <>Start one from <Link href="/projects">Projects</Link>, push to a watched branch, or try <span className="mono">launchway run</span> in a repository.</>}
        </Empty>
      )}
      {runs && runs.length > 0 && (
        <ul className="runlist">
          {runs.map((r) => <RunRow key={r.id} r={r} now={now} />)}
        </ul>
      )}
    </div>
  );
}

/** A run's place in its lane, short, for a list. */
function LaneMeta({ lane }: { lane: LaneInfo }) {
  const behind = lane.behind ? ` run #${lane.behind.number}` : "";
  switch (lane.state) {
    case "waiting": return <span className="meta wait" title={lane.reason}>lane {lane.label}: number {lane.position ?? 1} in line, behind{behind}</span>;
    case "replaced": return <span className="meta" title={lane.reason}>replaced in lane {lane.label} by{behind}</span>;
    case "skipped": return <span className="meta" title="The lane was busy, so this run was skipped">skipped: lane {lane.label} busy ({behind.trim()})</span>;
    default: return <span className="meta" title="The run holds its lane: other runs of it wait">lane {lane.label}</span>;
  }
}

function isDone(s: RunStatus) { return s === "succeeded" || s === "failed" || s === "canceled"; }

function RunRow({ r, now }: { r: RunSummary; now: number }) {
  const title = r.message || r.pipeline;
  const who = starterLabel(r);
  const where = refLabel(r);
  const waits = !isDone(r.status) && (r.waiting?.length ?? 0) > 0;
  const shown = waits ? "waiting" : r.status;
  return (
    <li>
      <Link href={waits ? `/runs/${r.id}?step=${encodeURIComponent(r.waiting[0])}` : `/runs/${r.id}`} className={`runrow t-${r.status}`}>
        <span className="runrow-status" title={waits ? "Waiting for approval" : statusLabel(shown)}><StatusIcon status={shown} size={20} /></span>
        <span className="runrow-main">
          <span className="runrow-title">
            <span className="runrow-project">{r.project}</span>
            <span className="runrow-num">#{r.number}</span>
            {r.draft && <span className="badge-draft" title="A draft run: pipeline text from the editor, not from Git">Draft</span>}
            <span className="runrow-msg">{title}</span>
          </span>
          <span className="runrow-meta">
            {where && <span className="meta"><BranchIcon />{where}</span>}
            {r.sha && <span className="meta mono"><CommitIcon />{shortSha(r.sha)}</span>}
            <span className="meta">{who}</span>
            {waits && <span className="meta wait">waiting for approval: {r.waiting.join(", ")}</span>}
            {r.lane && <LaneMeta lane={r.lane} />}
            {r.cancel_requested && !isDone(r.status) && <span className="meta warn">canceling…</span>}
            {r.cancel_requested && r.cancel_reason && <span className="meta">{r.cancel_reason}</span>}
          </span>
        </span>
        {r.tests && <span className="runrow-tests"><TestCounts t={r.tests} /></span>}
        {(r.findings || !!r.artifacts) && <span className="runrow-sec">{r.findings && <FindingCountBadge c={r.findings} />}{!!r.artifacts && <ArtifactCount n={r.artifacts} />}</span>}
        <span className="runrow-strip"><StepStrip steps={r.steps} /></span>
        <span className="runrow-time">
          <span>{elapsed(r.started_at || r.created_at, r.finished_at, now) || "—"}</span>
          <span className="faint">{ago(r.created_at, now)}</span>
        </span>
      </Link>
    </li>
  );
}
