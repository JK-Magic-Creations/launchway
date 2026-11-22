import { useCallback, useEffect, useMemo, useState } from "react";
import { forgeApi } from "../forgeApi";
import { api, type Project, type Ref, type RunSummary } from "../api";
import { canOperate, useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { ProjectTriggers } from "../components/ProjectTriggers";
import { StartDialog } from "../components/StartRun";
import { ErrorBox, Mono } from "../components/ui";
import { ago, shortSha } from "../format";
import { branchLink, hostBase, hostName, refShort } from "../guided/code";
import { trendOf } from "../guided/decide";
import { useInstallation } from "../guided/facts";
import { useLoad } from "../guided/useLoad";
import { Chip, Status, StateChip, stateOf } from "../kit/Status";
import { CutReleaseButton } from "../components/CutRelease";
import { BadgeBox } from "../components/BadgeBox";
import { Button, EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { Tabs } from "../kit/Tabs";
import { useTab } from "../components/PageTabs";
import { RateText, TrendStrip } from "../kit/Trend";
import { useCrumbLabel } from "../nav/crumbs";
import { useNow } from "../live";
import { terms } from "../terms";

/** One repository: where it lives, its branches with the last pipeline result on each, its tags, and what starts
 * runs. Files open in the code browser; reviews are not built yet and say so. */
export function RepoPage({ name }: { name: string }) {
  const me = useMe();
  const inst = useInstallation();
  const now = useNow(5000);
  const [tab, setTab] = useTab(["overview", "files", "reviews"], "overview");
  const [starting, setStarting] = useState(false);
  useCrumbLabel(name);
  const pFn = useCallback((bg: boolean) => (bg ? api.project(name) : api.project(name)), [name]);
  const project = useLoad<Project>(pFn, 5000);
  const rFn = useCallback((bg: boolean) => api.runs({ project: name, limit: 100 }, bg), [name]);
  const runs = useLoad<RunSummary[]>(rFn, 5000);
  const refsFn = useCallback(() => api.refs(name), [name]);
  const refs = useLoad<Ref[]>(refsFn);
  const p = project.data;
  if (project.error && !p) return <div className="page"><ErrorBox error={project.error} /></div>;
  if (!p) return <div className="page"><div className="loading">Loading…</div></div>;

  const enc = encodeURIComponent(p.name);
  const base = hostBase(p.url);
  const host = hostName(p.url);
  const trend = trendOf(runs.data ?? []);
  const envs = inst.environments.filter((e) => e.projects.includes(p.name));
  return (
    <div className="page">
      <PageHead eyebrow="Code" title={<>{p.name} <Chip icon="code">{host ? `on ${host}` : "local repository"}</Chip></>}
        sentence={p.error ? <><b>Launchway cannot reach this repository.</b> {p.error}</> : <>A repository Launchway reads from Git, on the branch <Mono>{p.default_branch}</Mono>. Checked every {p.poll_seconds}s{p.last_fetch > 0 ? `, last ${ago(p.last_fetch, now)}` : ""}.</>}
        actions={<>
          {canOperate(me, p.name) && <Button kind="pri" icon="play" onClick={() => setStarting(true)}>{terms().say("Start a {run}")}</Button>}
          <CutReleaseButton project={p.name} />
          {base && <a className="btn" href={base} target="_blank" rel="noreferrer noopener">Open on {host}</a>}
          <LinkButton href={`/projects/${enc}/pipeline`} icon="graph">Pipeline</LinkButton>
        </>} />
      <Tabs label="Parts of the repository" on={tab} onChange={setTab}
        tabs={[{ id: "overview", label: "Overview" }, { id: "files", label: "Files" }, { id: "reviews", label: "Reviews", soon: true }]} />
      <div role="tabpanel" aria-labelledby={`tab-${tab}`} className="stack">
        {tab === "files" && <FilesTab project={p} base={base ?? ""} host={host ?? ""} />}
        {tab === "reviews" && <Sheet><EmptyState title="Reviews are not built yet" action={<LinkButton href="/activity" kind="pri" icon="arrow">See runs of pull requests</LinkButton>}>
          Proposed changes will be read, commented and merged here once built-in Git hosting is merged. Runs started by pull requests exist today and are listed under Activity.
        </EmptyState></Sheet>}
        {tab === "overview" && (
          <>
            <Sheet title="Branches" actions={<><TrendStrip trend={trend} label={`The last runs of ${p.name}`} /><RateText trend={trend} /></>}>
              <Branches refs={refs.data} error={refs.error} runs={runs.data ?? []} project={p} now={now} kind="branch" />
            </Sheet>
            <Sheet title="Tags"><Branches refs={refs.data} error={refs.error} runs={runs.data ?? []} project={p} now={now} kind="tag" /></Sheet>
            <Sheet title="About">
              <dl className="kvlist">
                <dt>Repository</dt><dd className="mono">{p.url}</dd>
                <dt>Pipeline</dt><dd>{p.pipeline ? <>The template <Mono>{p.pipeline.template}</Mono> of the library <Mono>{p.pipeline.library}@{p.pipeline.version}</Mono>, unless the repository has its own file.</> : "A file in the repository, reviewed and versioned like code."}</dd>
                <dt>{terms().say("{Deploy:did} to")}</dt><dd>{envs.length > 0 ? envs.map((e, i) => <span key={e.name}>{i > 0 && ", "}<EntityLink kind="environment" name={e.name} /></span>) : "Nowhere yet."}</dd>
                <dt>Also</dt><dd className="row wrap"><LinkButton href={`/projects/${enc}/tests`} sm>Tests and coverage</LinkButton><LinkButton href={`/projects/${enc}/security`} sm>Security</LinkButton><LinkButton href={`/projects/${enc}/caches`} sm>Build caches</LinkButton>{me?.admin && <LinkButton href={`/config/projects/${enc}`} sm>Change its settings</LinkButton>}</dd>
              </dl>
            </Sheet>
            <ProjectTriggers project={p.name} />
            <BadgeBox project={p.name} canSwitch={!!me && (me.admin || me.roles[p.name] === "admin")} />
            <Sheet title={`Recent pushes (${p.decisions.length})`}>
              {p.decisions.length === 0 ? <p className="sh-b faint">No pushes seen since the server started. Launchway notes where every branch is the first time it looks, and reacts to changes after that.</p> : (
                <ul className="rowlist">
                  {p.decisions.map((d, i) => (
                    <li key={i}>
                      <StateChip state={d.outcome === "started" ? "ok" : d.outcome === "error" ? "bad" : d.outcome === "waiting" ? "wait" : "skip"}>{d.outcome === "started" ? "Started" : d.outcome === "ignored" ? "Ignored" : d.outcome === "waiting" ? "Waiting" : "Problem"}</StateChip>
                      <span className="mono">{refShort(d.ref)}</span><span className="mono faint">{shortSha(d.sha)}</span>
                      <span className="main">{d.run_id ? <EntityLink kind="run" name={d.detail} id={d.run_id} /> : d.detail}</span>
                      <span className="faint small">{ago(d.at, now)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </Sheet>
          </>
        )}
      </div>
      {starting && <StartDialog project={p} onClose={() => setStarting(false)} />}
    </div>
  );
}

/** The files of a project whose repository this server hosts open in the code browser; one on another server opens there. */
function FilesTab({ project, base, host }: { project: Project; base: string; host: string }) {
  const hosted = /\/git\/([^/]+\/[^?#]+?)(?:\.git)?\/?$/.exec(project.url)?.[1];
  const [known, setKnown] = useState<boolean | null>(hosted ? null : false);
  useEffect(() => {
    if (!hosted) return;
    const [o, ...n] = hosted.split("/");
    forgeApi.repository(o, n.join("/")).then(() => setKnown(true)).catch(() => setKnown(false));
  }, [hosted]);
  if (known === null) return <Sheet><p className="sh-b faint">Looking for the repository…</p></Sheet>;
  if (known && hosted) return (
    <Sheet><EmptyState title="Browse the files" action={<LinkButton href={`/repositories/${hosted}/code`} kind="pri" icon="code">Open the files of {hosted}</LinkButton>}>
      This repository is hosted here, so its files, commits, branches and search are in the code browser.
    </EmptyState></Sheet>
  );
  return (
    <Sheet><EmptyState title="This repository is on another server" action={base ? <a className="btn btn-primary" href={base} target="_blank" rel="noreferrer noopener">Open on {host}</a> : undefined}>
      Launchway reads the pipeline file of a repository it does not host, not the rest of it. {base ? `${host} shows the files.` : "It is read from a local path, so use your own tools."} To read code here, move the repository to this server with an import or a mirror.
    </EmptyState></Sheet>
  );
}

function Branches({ refs, error, runs, project, now, kind }: { refs: Ref[] | null; error: unknown; runs: RunSummary[]; project: Project; now: number; kind: "branch" | "tag" }) {
  const list = useMemo(() => (refs ?? []).filter((r) => r.kind === kind), [refs, kind]);
  if (error) return <div className="sh-b"><ErrorBox error={error} /></div>;
  if (refs === null) return <p className="sh-b faint">Loading…</p>;
  if (list.length === 0) return <p className="sh-b faint">{kind === "branch" ? "No branches were found." : "No tags were found."}</p>;
  return (
    <ul className="rowlist">
      {list.map((r) => {
        const last = runs.find((x) => (kind === "branch" ? x.branch === r.name : x.tag === r.name));
        const link = branchLink(project.url, r.name);
        return (
          <li key={r.name}>
            <div className="main">
              <div className="nm mono">{link && kind === "branch" ? <a href={link} target="_blank" rel="noreferrer noopener">{r.name}</a> : r.name}{r.name === project.default_branch && <> <Chip>default</Chip></>}</div>
              <div className="sb">{r.subject || "no message"} · {r.author}{r.committed_at ? ` · ${ago(r.committed_at, now)}` : ""} · <span className="mono">{shortSha(r.sha)}</span></div>
            </div>
            <div className="cell" style={{ minWidth: 190 }}>
              {last ? <><Status state={stateOf(last.status)} /><span><EntityLink kind="run" name={`#${last.number}`} id={last.id} /> {last.status === "succeeded" ? "passed" : last.status}, {ago(last.created_at, now)}</span></> : <span className="faint">no pipeline run yet</span>}
            </div>
          </li>
        );
      })}
    </ul>
  );
}
