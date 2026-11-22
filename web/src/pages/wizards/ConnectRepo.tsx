// Connect a repository: a project, its branch, where its pipeline comes
// from and what starts its runs. The steps are shared with the first-run
// setup, which makes the first project the same way.

import { useEffect, useMemo, useState } from "react";
import { ApiError, api } from "../../api";
import { configApi, notOffered, type ChangeRequest, type ConfigOverview, type LibraryInfo, type LibraryTemplates, type RepoCheck, type Spec } from "../../configApi";
import { navigate } from "../../router";
import { Choices, CheckInput, CopyBlock, DefinedAt, ListInput, SelectInput, TextInput } from "../../components/ConfigForm";
import { AllowHost } from "../../components/AllowHost";
import { Link } from "../../components/ui";
import { CreatedList, WizardRunner, type StepDef } from "./Wizards";
import { terms } from "../../terms";
import { prose } from "../../prose";
import { SpanFaint, SpanMono } from "../../kit/bits";
const P = await prose("pages.wizards.ConnectRepo");

type Source = "template" | "file" | "sample";

export interface RepoFlow {
  steps: StepDef[];
  change: ChangeRequest;
  name: string;
  branch: string;
  source: Source;
  /** The pipeline file to commit, for the file and sample choices. */
  pipelineText: string;
}

const POLLS = [
  { value: "15s", label: "Every 15 seconds", help: P.t0 },
  { value: "30s", label: "Every 30 seconds", help: P.t1, badge: "recommended" },
  { value: "2m", label: "Every 2 minutes", help: P.t2 },
  { value: "10m", label: "Every 10 minutes", help: P.t3 },
];

/** sentence ends a template parameter's description with a full stop, so "Default: …" can follow. */
function sentence(d?: string): string {
  if (!d) return "";
  return /[.!?]$/.test(d.trim()) ? d.trim() : d.trim() + ".";
}

function nameFromRepo(url: string): string {
  const m = url.trim().replace(/\/+$/, "").replace(/\.git$/, "").split(/[/:]/).pop() ?? "";
  return m.replace(/[^A-Za-z0-9._-]/g, "-").replace(/^[^A-Za-z0-9]+/, "").slice(0, 100);
}

/** Credentials typed into a URL end up in the configuration: warn. */
function hasPassword(url: string): boolean {
  return /^[a-z+]+:\/\/[^/@\s]+:[^/@\s]+@/i.test(url.trim());
}

export function useRepoFlow(overview: ConfigOverview): RepoFlow {
  const lv = overview.levels;
  const proj = lv.project.singular.toLowerCase();
  const [repo, setRepo] = useState("");
  const [nameEdit, setNameEdit] = useState<string | null>(null);
  const [unit, setUnit] = useState("");
  const [check, setCheck] = useState<{ state: "idle" | "checking" | "unavailable" | "error"; result?: RepoCheck; error?: string; repo?: string; branch?: string }>({ state: "idle" });
  const [branch, setBranch] = useState("");
  const [sourcePick, setSourcePick] = useState<Source>("template");
  const [libs, setLibs] = useState<LibraryInfo[] | null>(null);
  const [library, setLibrary] = useState("");
  const [version, setVersion] = useState("");
  const [tmplsRes, setTmplsRes] = useState<{ key: string; state: "idle" | "error"; data?: LibraryTemplates; error?: string } | null>(null);
  const [template, setTemplate] = useState("");
  const [withVals, setWithVals] = useState<Record<string, string>>({});
  const [onPush, setOnPush] = useState(true);
  const [pushBranches, setPushBranches] = useState<string[]>([]);
  const [pushTags, setPushTags] = useState<string[]>([]);
  const [onPR, setOnPR] = useState(false);
  const [schedule, setSchedule] = useState(false);
  const [cron, setCron] = useState("0 3 * * *");
  const [poll, setPoll] = useState("30s");
  const [supersede, setSupersede] = useState(true);

  useEffect(() => { configApi.libraries().then(setLibs).catch(() => setLibs([])); }, []);
  // With no library to take a template from, the sample pipeline is the default.
  const source: Source = libs && libs.length === 0 && sourcePick === "template" ? "sample" : sourcePick;
  const setSource = setSourcePick;
  const name = nameEdit ?? nameFromRepo(repo);
  // What was read belongs to the library and version it was read for; anything else is still loading.
  const tmplsKey = `${library}\n${version}`;
  const tmpls: { state: "idle" | "loading" | "error"; data?: LibraryTemplates; error?: string } =
    !library ? { state: "idle" } : tmplsRes && tmplsRes.key === tmplsKey ? tmplsRes : { state: "loading" };
  useEffect(() => {
    if (!library) return;
    const key = `${library}\n${version}`;
    configApi.templates(library, version || undefined).then((d) => {
      setTmplsRes({ key, state: "idle", data: d });
      if (!version) setVersion(d.version);
    }).catch((e) => setTmplsRes({ key, state: "error", error: e instanceof Error ? e.message : String(e) }));
  }, [library, version]);

  const runCheck = async (b?: string): Promise<RepoCheck | null> => {
    if (!repo.trim()) return null;
    setCheck({ state: "checking" });
    try {
      const r = await configApi.checkRepository(repo.trim(), b);
      setCheck({ state: "idle", result: r, repo: repo.trim(), branch: b });
      if (r.ok && !branch && r.default_branch) setBranch(r.default_branch);
      return r;
    } catch (e) {
      if (notOffered(e)) setCheck({ state: "unavailable", repo: repo.trim() });
      else setCheck({ state: "error", error: e instanceof Error ? e.message : String(e), repo: repo.trim() });
      return null;
    }
  };

  const tmpl = tmpls.data?.templates.find((t) => t.name === template);
  const effBranch = branch || check.result?.default_branch || "main";
  const branches = check.result?.ok ? check.result.branches : [];

  const spec: Spec = useMemo(() => {
    const s: Spec = { repo: repo.trim() };
    if (branch) s.branch = branch;
    if (poll) s.poll = poll;
    if (source === "template" && (library || template)) {
      const p: Spec = { library, template };
      if (version) p.version = version;
      const w = Object.fromEntries(Object.entries(withVals).filter(([, v]) => v !== ""));
      if (Object.keys(w).length) p.with = w;
      const on: Spec = {};
      if (onPush) {
        const push: Spec = {};
        if (pushBranches.length) push.branches = pushBranches;
        if (pushTags.length) push.tags = pushTags;
        on.push = push;
      }
      if (onPR) on.pull_request = {};
      if (Object.keys(on).length) p.on = on;
      if (supersede) p.supersede = true;
      s.pipeline = p;
    }
    return s;
  }, [repo, branch, poll, source, library, template, version, withVals, onPush, pushBranches, pushTags, onPR, supersede]);

  const pipelineText = useMemo(() => {
    const on: string[] = [];
    if (onPush) {
      const parts: string[] = [];
      if (pushBranches.length) parts.push(`branches: [${pushBranches.join(", ")}]`);
      if (pushTags.length) parts.push(`tags: [${pushTags.map((t) => JSON.stringify(t)).join(", ")}]`);
      on.push(`  push: { ${parts.join(", ")} }`.replace("{  }", "{}"));
    }
    if (onPR) on.push("  pull_request: {}");
    if (schedule) on.push(`  schedule:\n    - { cron: "${cron}" }`);
    const head = `pipeline: ${name || "my-project"}\n` + (on.length ? `on:\n${on.join("\n")}\n` : "") + (supersede && (onPush || onPR) ? "supersede: true\n" : "");
    return head + P.t4;
  }, [name, onPush, pushBranches, pushTags, onPR, schedule, cron, supersede]);

  const change: ChangeRequest = { kind: "project", name: name.trim(), unit: unit || undefined, spec, version: -1 };

  const repoStep: StepDef = {
    id: "repo", title: "The repository",
    fields: [{ path: "repo", value: repo }, { path: "name", match: ["name", "names", "exists", "already"], value: name }],
    local: () => !repo.trim() ? P.t5 : !name.trim() ? `Give the ${proj} a name.` : null,
    onNext: async () => {
      if (check.repo !== repo.trim()) await runCheck(branch || undefined);
      return true;
    },
    render: (by) => (
      <>
        <p>{P.t6}</p>
        <div className="cf-grid">
          <TextInput label="Repository address" required value={repo} onChange={setRepo} mono wide focusOnMount problems={by.repo}
            placeholder={P.t7}
            help={P.t8} />
        </div>
        {hasPassword(repo) && (
          <div className="callout callout-draft" role="alert">{P.t9}</div>
        )}
        <details className="cf-explain" open={check.result?.ok === false}>
          <summary>{P.t10}</summary>
          <p>{P.t11}<strong>server's own Git access</strong>, exactly as <SpanMono>git clone</SpanMono> {terms().say(P.t12)} <SpanMono>GIT_CONFIG_COUNT=1</SpanMono>,{" "}
            <SpanMono>{P.t13}</SpanMono> and{" "}
            <SpanMono>{P.t14}</SpanMono>{P.t15}{proj}{P.t16}</p>
        </details>
        <div className="cf-row">
          <button type="button" className="btn" disabled={!repo.trim() || check.state === "checking"} onClick={() => runCheck(branch || undefined)}>
            {check.state === "checking" ? "Checking…" : P.t17}
          </button>
          <RepoCheckLine check={check} repo={repo.trim()} />
        </div>
        <div className="cf-grid">
          <TextInput label={`Name of the ${proj}`} required value={name} onChange={setNameEdit} mono problems={by.name}
            help={`How the ${proj} is named in URLs, pipelines and the command line: letters, digits, dots, hyphens and underscores. It cannot be changed later.`} />
          <DefinedAt overview={overview} value={unit} onChange={setUnit} project />
        </div>
      </>
    ),
  };

  const branchStep: StepDef = {
    id: "branch", title: "The branch",
    fields: [{ path: "branch", value: branch }],
    onNext: async () => {
      if (check.state !== "unavailable" && check.result?.ok && branch && check.branch !== branch) await runCheck(branch);
      if (!pushBranches.length && !pushTags.length) setPushBranches([branch || check.result?.default_branch || "main"]);
      return true;
    },
    render: (by) => (
      <>
        <p>{P.t18}</p>
        {branches.length > 0 && branches.length <= 8 ? (
          <Choices legend="Default branch" value={branch} onChange={setBranch} problems={by.branch}
            options={branches.map((b) => ({ value: b, label: b, badge: b === check.result?.default_branch ? "the repository's default" : undefined }))} />
        ) : branches.length > 8 ? (
          <SelectInput label="Default branch" value={branch} onChange={setBranch} problems={by.branch} help={P.t19}
            options={[{ value: "", label: "Choose a branch" }, ...branches.map((b) => ({ value: b, label: b + (b === check.result?.default_branch ? " (the repository's default)" : "") }))]} />
        ) : (
          <div className="cf-grid">
            <TextInput label="Default branch" value={branch} onChange={setBranch} mono placeholder="main" problems={by.branch}
              help={check.state === "unavailable" ? P.t20 : P.t21} />
          </div>
        )}
      </>
    ),
  };

  const sourceStep: StepDef = {
    id: "pipeline", title: "The pipeline",
    fields: [{ path: "library", match: ["pipeline.library", "library"], value: library }, { path: "template", match: ["pipeline.template", "template"], value: template },
      { path: "version", match: ["pipeline.version", "version"], value: version }, { path: "with", match: ["pipeline.with", "with", "parameter"] }],
    local: () => source === "template" && (!library || !template) ? P.t22 : null,
    render: (by) => (
      <>
        <Choices legend={P.t23} value={source} onChange={(v) => setSource(v as Source)}
          help={<>{P.t24}<SpanMono>.launchway/pipeline.yaml</SpanMono> {P.t25}</>}
          options={[
            { value: "template", label: P.t26, disabled: !libs?.length,
              help: libs?.length ? terms().say(P.t27)
                : <>{P.t28}<Link href="/config/libraries/new">Add a library</Link> first to use one.</> },
            { value: "file", label: P.t29,
              help: <>{P.t30}{" "}
                {check.result?.ok ? <PipelineFileState check={check.result} branch={effBranch} /> : null}</> },
            { value: "sample", label: `Start from a ${terms().lower("flight_plan")}`,
              help: terms().say(`A ${terms().lower("flight_plan")} is the ready-made pipeline for a kind of {project}. You get a minimal working pipeline to copy and commit to the repository as .launchway/pipeline.yaml, with the triggers you choose next. Launchway does not keep pipeline text: Git stays the only source. The pipeline editor can also save it for you, as a commit on a new branch.`) },
          ]} />
        {source === "template" && libs && libs.length > 0 && (
          <div className="cf-sub">
            <div className="cf-grid">
              <SelectInput label="Library" value={library} onChange={(v) => { setLibrary(v); setVersion(""); setTemplate(""); setWithVals({}); }} problems={by.library}
                help={terms().say(P.t31)}
                options={[{ value: "", label: "Choose a library" }, ...libs.map((l) => ({ value: l.name, label: l.name }))]} />
              {library && tmpls.data && (
                <>
                  <TextInput label="Pinned to version" value={version} onChange={setVersion} mono problems={by.version} list="cf-lib-versions"
                    help={<>A tag (such as {tmpls.data.versions[0] ?? "v1.0.0"}{P.t32}</>} />
                  <datalist id="cf-lib-versions">{tmpls.data.versions.map((v) => <option key={v} value={v} />)}</datalist>
                </>
              )}
            </div>
            {tmpls.state === "loading" && <p className="faint">{P.t33}</p>}
            {tmpls.state === "error" && <p className="cf-problem">{P.t34}{tmpls.error}</p>}
            {tmpls.data && (
              <Choices legend="Template" value={template} onChange={(v) => { setTemplate(v); setWithVals({}); }} problems={by.template}
                options={tmpls.data.templates.map((t) => ({ value: t.name, label: t.name, help: t.description || (t.params.length ? `Takes ${t.params.map((p) => p.name).join(", ")}.` : "Takes no parameters.") }))} />
            )}
            {tmpl && tmpl.params.length > 0 && (
              <fieldset className="cf-section">
                <legend className="cf-section-title">The template's parameters</legend>
                <div className="cf-grid">
                  {tmpl.params.map((p) => p.options?.length ? (
                    <SelectInput key={p.name} label={p.name} value={withVals[p.name] ?? ""} onChange={(v) => setWithVals({ ...withVals, [p.name]: v })}
                      help={sentence(p.description) + (p.default !== undefined ? ` Default: ${p.default}.` : "")}
                      options={[{ value: "", label: p.default !== undefined ? `the default (${p.default})` : "Choose" }, ...p.options.map((o) => ({ value: o, label: o }))]} />
                  ) : p.type === "boolean" ? (
                    <SelectInput key={p.name} label={p.name} value={withVals[p.name] ?? ""} onChange={(v) => setWithVals({ ...withVals, [p.name]: v })}
                      help={sentence(p.description) + (p.default !== undefined ? ` Default: ${p.default}.` : "")}
                      options={[{ value: "", label: p.default !== undefined ? `the default (${p.default})` : "Choose" }, { value: "true", label: "true" }, { value: "false", label: "false" }]} />
                  ) : (
                    <TextInput key={p.name} label={p.name} value={withVals[p.name] ?? ""} onChange={(v) => setWithVals({ ...withVals, [p.name]: v })} mono
                      placeholder={p.default ?? (p.type === "number" ? "a number" : "")} help={sentence(p.description) + (p.default !== undefined ? ` Default: ${p.default}.` : " Required.")}
                      problems={by.with?.filter((x) => x.includes(p.name))} />
                  ))}
                </div>
              </fieldset>
            )}
          </div>
        )}
      </>
    ),
  };

  const triggerStep: StepDef = {
    id: "triggers", title: terms().say("What starts a {run}"),
    fields: [{ path: "poll", value: poll }, { path: "push", match: ["on.push", "push", "branches", "tags"] }, { path: "pull_request", match: ["pull_request"] }, { path: "supersede" }],
    render: (by) => (
      <>
        <p>{P.t35}</p>
        <div className="cf-grid">
          <CheckInput label="Pushes" checked={onPush} onChange={setOnPush} problems={by.push}
            help={terms().say(P.t36)} />
          {onPush && <ListInput label="Only these branches" value={pushBranches} onChange={setPushBranches} placeholder={`${effBranch}, release/*`}
            help={P.t37} />}
          {onPush && <ListInput label="Tags" value={pushTags} onChange={setPushTags} placeholder="v*"
            help={P.t38} />}
          <CheckInput label="Pull requests" checked={onPR} onChange={setOnPR} problems={by.pull_request}
            help={P.t39} />
          {source !== "template" && (
            <CheckInput label="On a schedule" checked={schedule} onChange={setSchedule}
              help={P.t40} />
          )}
          {source !== "template" && schedule && (
            <TextInput label="When (cron, UTC)" value={cron} onChange={setCron} mono help={P.t41} />
          )}
          {(onPush || onPR) && <CheckInput label={P.t42} checked={supersede} onChange={setSupersede} problems={by.supersede}
            help={terms().say(P.t43)} />}
        </div>
        {source === "template" && <p className="cf-help">{P.t44}{proj} {P.t45}</p>}
        {source !== "template" && <p className="cf-help">{P.t46}<SpanMono>on:</SpanMono> {P.t47}</p>}
        <Choices legend={P.t48} value={poll} onChange={setPoll} problems={by.poll} options={POLLS}
          help={P.t49} />
      </>
    ),
  };

  return { steps: [repoStep, branchStep, sourceStep, triggerStep], change, name: name.trim(), branch: effBranch, source, pipelineText };
}

function RepoCheckLine({ check, repo }: { check: { state: string; result?: RepoCheck; error?: string; repo?: string; branch?: string }; repo: string }) {
  let body = null;
  if (check.state === "unavailable") body = <SpanFaint>{P.t50}{""}project.</SpanFaint>;
  else if (check.state === "error") body = <span className="cf-problem">The check failed: {check.error}</span>;
  else if (check.result && check.repo === repo) {
    body = check.result.ok
      ? <span><span className="cf-ok">The server can read it: {check.result.branches.length} branch{check.result.branches.length === 1 ? "" : "es"}, default {check.result.default_branch || "unknown"}.</span>{" "}
          <PipelineFileState check={check.result} branch={check.branch || check.result.default_branch || "the default branch"} /></span>
      : <span className="cf-problem">The server cannot read it: {check.result.problem ?? "no reason given"}{P.t51}{check.result.blocked && check.result.blocked_host && <AllowHost host={check.result.blocked_host} />}</span>;
  }
  return <span className="cf-checkresult" aria-live="polite">{body}</span>;
}

/** PipelineFileState says what the server found in the repository at the branch. */
function PipelineFileState({ check, branch }: { check: RepoCheck; branch: string }) {
  const short = (c?: string) => (c ? c.slice(0, 7) : "");
  const file = check.pipeline_file || ".launchway/pipeline.yaml";
  if (!check.has_pipeline_file) return <span>Not found on {branch} yet: commit {file} {terms().say("there before the first {run}.")}</span>;
  return (
    <span>
      Found {file}{check.commit ? ` at commit ${short(check.commit)}` : ""} on {branch}.{" "}
      {check.valid === true ? <span className="cf-ok">It is valid.</span>
        : check.problems?.length ? (
          <span className="cf-problem">It has {check.problems.length === 1 ? "a problem" : `${check.problems.length} problems`}:
            <ul className="cf-fileprobs">{check.problems.map((p, i) => (
              <li key={i}><SpanMono>{p.file}{p.line ? `:${p.line}` : ""}{p.column ? `:${p.column}` : ""}</SpanMono>{p.commit ? ` (${short(p.commit)})` : ""}: {p.message}</li>
            ))}</ul>
          </span>
        ) : null}
    </span>
  );
}

/** What to commit, for projects whose pipeline is a file. */
export function PipelineToCommit({ flow }: { flow: RepoFlow }) {
  if (flow.source === "template") return null;
  if (flow.source === "file") {
    const on = flow.pipelineText.split("\nsteps:")[0];
    return <CopyBlock title={P.t52} text={on.trim() + "\n"}
      note={<>{P.t53}<SpanMono>{flow.branch}</SpanMono> branch).</>} />;
  }
  return <CopyBlock title=".launchway/pipeline.yaml" text={flow.pipelineText}
    note={<>Commit this file to the <SpanMono>{flow.branch}</SpanMono> {terms().say(P.t54)}</>} />;
}

export function RunItNow({ name, branch }: { name: string; branch: string }) {
  const [state, setState] = useState<{ busy?: boolean; error?: string; run?: { id: string; number: number } }>({});
  const start = async () => {
    setState({ busy: true });
    // The new project reaches the server that answers within a few seconds.
    for (let i = 0; i < 8; i++) {
      try {
        const r = await api.startRun(name, branch);
        setState({ run: { id: r.id, number: r.number } });
        return;
      } catch (e) {
        if (e instanceof ApiError && e.status === 404 && i < 7) { await new Promise((ok) => window.setTimeout(ok, 1000)); continue; }
        setState({ error: e instanceof Error ? e.message : String(e) });
        return;
      }
    }
  };
  return (
    <div className="cf-row">
      {state.run ? (
        <span className="cf-ok" role="status">{terms().say("{Run} #")}{state.run.number} started. <Link href={`/runs/${state.run.id}`}>Watch it</Link>.</span>
      ) : (
        <button type="button" className="btn btn-primary" disabled={state.busy} onClick={start}>{state.busy ? "Starting…" : terms().say(`Start its first {run} (${branch})`)}</button>
      )}
      {state.error && <span className="cf-problem" role="alert">The run did not start: {state.error}</span>}
    </div>
  );
}

export function ConnectRepoWizard({ overview }: { overview: ConfigOverview }) {
  const flow = useRepoFlow(overview);
  const proj = overview.levels.project.singular.toLowerCase();
  return (
    <WizardRunner title="Connect a repository" overview={overview}
      intro={`A few questions make a ${proj}: the repository, its branch, its pipeline and what starts runs. Nothing is created until you confirm at the end.`}
      steps={flow.steps} changes={flow.name ? [flow.change] : []}
      summaryExtra={<PipelineToCommit flow={flow} />}
      done={(created) => (
        <>
          <CreatedList objects={created} overview={overview} />
          <PipelineToCommit flow={flow} />
          <h3 className="h-sm">Next</h3>
          <RunItNow name={flow.name} branch={flow.branch} />
          <ul className="cf-next">
            <li><Link href={`/config/projects/${encodeURIComponent(flow.name)}`}>See and change the {proj}'s settings</Link></li>
            <li><Link href="/projects">See it with the other {overview.levels.project.plural.toLowerCase()}</Link></li>
            <li><Link href="/config/wizard/add-runner">{terms().say("Add a {runner}")}</Link>{terms().say(P.t55)}</li>
            <li><a href={P.t56} onClick={(e) => { e.preventDefault(); navigate("/config"); window.setTimeout(() => navigate(P.t56), 0); }}>Connect another repository</a></li>
          </ul>
        </>
      )} />
  );
}

