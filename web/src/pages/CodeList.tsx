import { lazy, Suspense, useCallback, useMemo } from "react";
import { useHosting } from "./SshKeys";
import { api, type Project, type RunSummary } from "../api";
import { useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { WizardButton, WizardEmpty } from "../components/WizardButton";
import { ago } from "../format";
import { hostName } from "../guided/code";
import { trendOf } from "../guided/decide";
import { useInstallation } from "../guided/facts";
import { useLoad } from "../guided/useLoad";
import { Chip } from "../kit/Status";
import { Band, LinkButton, PageHead, Sheet } from "../kit/layout";
import { Status, stateOf } from "../kit/Status";
import { TrendStrip } from "../kit/Trend";
import { useNow } from "../live";

// The grouped, searchable list loads after the first paint; the flat list is what shows meanwhile.
const Explorer = lazy(() => import("../explorer/PipelinesExplorer"));

/** Code: every project as a repository, with where it lives and how its last pipeline run went. */
export function CodeListPage() {
  const inst = useInstallation();
  const me = useMe();
  const now = useNow(15000);
  const hosting = useHosting();
  const runsFn = useCallback((bg: boolean) => api.runs({ limit: 100 }, bg), []);
  const all = useLoad(runsFn, 15000).data;
  const by = useMemo(() => {
    const m = new Map<string, RunSummary[]>();
    for (const r of all ?? []) m.set(r.project, [...(m.get(r.project) ?? []), r]);
    return m;
  }, [all]);
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  const rows = inst.projects;
  const flat = flatList(rows, by, now);
  const unreachable = rows.filter((p) => p.error).length;
  return (
    <div className="page">
      <PageHead eyebrow="Code" title="Repositories"
        sentence={rows.length === 0 ? "Your code lives in Git repositories. None is connected yet." : unreachable > 0 ? <><b>{unreachable} {unreachable === 1 ? "repository cannot" : "repositories cannot"} be reached.</b> The others are read normally.</> : `${rows.length} ${rows.length === 1 ? "repository" : "repositories"} connected, all reachable.`}
        actions={<>{hosting === "on" && <LinkButton href="/repositories" sm>Hosted by Launchway</LinkButton>}{rows.length > 0 && me?.admin ? <WizardButton wizard="connect-repository" label="Add a project" /> : null}</>} />
      {rows.length > 0 && (
        <Band cells={[
          { key: "n", label: "Repositories", value: rows.length, foot: "connected from Git" },
          { key: "bad", label: "Cannot be reached", value: unreachable, foot: unreachable ? "look at the red rows" : "none", loud: unreachable ? "bad" : undefined },
          { key: "fail", label: "Last run failed", value: rows.filter((p) => p.last_run?.status === "failed").length, foot: "see Pipelines" },
        ]} />
      )}
      {rows.length === 0 ? (
        <WizardEmpty title="No projects yet" wizard="connect-repository" label="Add a project"
          text="A project is a Git repository. Pick one and Launchway reads its pipeline from Git and runs it each time you push." />
      ) : (
        <Suspense fallback={flat}><Explorer as="projects" projects={rows} runs={all ?? []} now={now} me={me?.user ?? ""} /></Suspense>
      )}
    </div>
  );
}

function flatList(rows: Project[], by: Map<string, RunSummary[]>, now: number) {
  return (
        <Sheet title="Every repository" actions={<LinkButton href="/code/files" sm>Files and reviews</LinkButton>}>
          <ul className="rowlist">
            {rows.map((p) => {
              const r = p.last_run;
              const host = hostName(p.url);
              return (
                <li key={p.name} className={p.error ? "bad" : ""}>
                  <div className="main">
                    <div className="nm"><EntityLink kind="project" name={p.name} /> <Chip>{host ? `on ${host}` : "local repository"}</Chip></div>
                    <div className="sb mono">{p.url}</div>
                    {p.error && <div className="sb" style={{ color: "var(--bad-ink)" }}>Cannot reach the repository: {p.error}</div>}
                  </div>
                  <div className="cell"><span className="faint">default</span> <b className="mono">{p.default_branch}</b></div>
                  <div className="cell" style={{ minWidth: 190 }}>
                    {r ? <><Status state={stateOf(r.status)} /><span><EntityLink kind="run" name={`#${r.number}`} id={r.id} /> {ago(r.created_at, now)}</span></> : <span className="faint">No runs yet</span>}
                  </div>
                  <div className="cell"><TrendStrip trend={trendOf(by.get(p.name) ?? [])} label={`The last runs of ${p.name}`} /></div>
                </li>
              );
            })}
          </ul>
        </Sheet>
  );
}
