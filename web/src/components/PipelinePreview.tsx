import { Fragment, useMemo, useState, type ReactNode } from "react";
import type { Problem, StepView, ValidateResult } from "../api";
import { shortDuration } from "../format";
import { Graph } from "./Graph";
import { LibraryList, TemplateFacts } from "./Libraries";
import { ErrorBox, Mono } from "./ui";
import { terms } from "../terms";

interface Props {
  res: ValidateResult | null;
  error: unknown;
  /** Called when a problem's line is chosen, to show it in the editor. */
  onJump?: (line: number) => void;
  /** Extra notes shown under the summary of a valid pipeline. */
  notes?: ReactNode;
}

// PipelinePreview shows what the server's parser made of pipeline text: its
// problems, or the graph it becomes, with the chosen step's details.
export function PipelinePreview({ res, error, onJump, notes }: Props) {
  const [sel, setSel] = useState<string | null>(null);
  const steps: StepView[] = useMemo(() => (res?.graph?.steps ?? []).map((s, i) => ({
    run_id: "", id: s.id, position: i, status: "pending", attempt: 0, queued_at: 0, started_at: 0, finished_at: 0,
    exit_code: null, error: "", excerpt: "", skip_kind: "", runner: "", outputs: {}, inputs: [], container: s.container, image: s.image, run: s.run,
    needs: s.needs, when: s.when, retry: s.retry, timeout: s.timeout, description: s.description,
    kind: s.kind, message: s.message, deadline: 0, decided_by: "", decided_at: 0, comment: "", group: s.group, uses: s.uses,
  })), [res]);
  const unexpanded = (res?.graph?.steps ?? []).filter((s) => s.uses).length;
  const chosenGroup = sel ? res?.graph?.groups?.find((g) => g.id === res.graph?.steps.find((s) => s.id === sel)?.group) : undefined;
  const chosen = res?.graph?.steps.find((s) => s.id === sel);

  return (
    <div className="play-right">
      <ErrorBox error={error} />
      {res && !res.ok && <ProblemList problems={res.problems ?? []} onJump={onJump} />}
      {res?.ok && res.graph && res.levels && (
        <>
          <section className="card">
            <h2 className="h-sm ok">Valid: {res.graph.name}, {res.graph.steps.length} steps in {res.levels.length} stages</h2>
            <p className="sub">
              {res.graph.on.push
                ? <>Starts on pushes to {[...(res.graph.on.push.branches ?? []), ...(res.graph.on.push.tags ?? []).map((t) => "tag " + t)].join(", ")}{res.graph.on.pull_request ? ", on pull requests" : ""}, and by hand.</>
                : res.graph.on.pull_request ? <>Starts on pull requests, and by hand.</>
                : <>Starts only by hand: there is no push or pull request trigger.</>}
              {res.graph.supersede && <> A new run of a push or pull request cancels the older ones of the same ref that are still going.</>}
            </p>
            {res.graph.libraries && res.graph.libraries.length > 0 && (
              <p className="sub libs">Libraries: <LibraryList libraries={res.graph.libraries} /></p>
            )}
            {res.expanded && (res.graph.groups?.length ?? 0) > 0 && (
              <p className="sub">{terms().say("Templates are expanded from the libraries' current versions, as a {run} started now would expand them.")}</p>
            )}
            {unexpanded > 0 && (
              <p className="sub">{unexpanded === 1 ? "One step uses a template; it is" : `${unexpanded} steps use templates; they are`} drawn
                as one node{unexpanded === 1 ? "" : " each"}, not expanded. Templates come from the server's libraries, which are read when a pipeline is
                checked for a project, or here with Expand templates.</p>
            )}
            {notes}
          </section>
          <section className="card card-flush">
            <Graph steps={steps} groups={res.graph.groups} selected={sel} onSelect={setSel} now={0} compact
              describe={(s) => s.uses ? "template (not expanded)" : (s.when ? "conditional · " : "") +
                (s.kind === "approval" ? "approval" : s.kind === "rollout" ? "rollout" : s.container ?? (s.image ?? "").replace(/^docker\.io\/library\//, ""))} />
          </section>
          {res.graph.containers && res.graph.containers.length > 0 && (
            <section className="card">
              <h2 className="h-sm">{terms().say("Containers in each {run}")}</h2>
              <dl className="kv">
                {res.graph.containers.map((c) => (
                  <Fragment key={c.name}>
                    <dt>{c.name}{c.service ? " (service)" : ""}</dt>
                    <dd className="mono">{c.image}{c.memory ? ` · ${c.memory}` : ""}{c.cpu ? ` · ${c.cpu} cpu` : ""}</dd>
                  </Fragment>
                ))}
              </dl>
            </section>
          )}
          {chosen && (
            <section className="card">
              <h2 className="h-sm">{chosen.id}</h2>
              {chosen.description && <p className="sub step-desc">{chosen.description}</p>}
              <dl className="kv">
                {chosenGroup && <TemplateFacts group={chosenGroup} libraries={res.graph.libraries} />}
                {chosen.uses && <><dt>Uses</dt><dd><Mono>{chosen.uses}</Mono> (template, not expanded here)</dd></>}
                {chosen.uses && chosen.with && <><dt>With</dt><dd>{Object.entries(chosen.with).sort(([a], [b]) => a.localeCompare(b))
                  .map(([k, v]) => <div key={k}><Mono>{k}</Mono> = <Mono>{v}</Mono></div>)}</dd></>}
                {chosen.kind === "approval" && <><dt>Approval</dt><dd>{chosen.message}</dd></>}
                {chosen.container && <><dt>Container</dt><dd>{chosen.container}</dd></>}
                {chosen.kind !== "approval" && !chosen.uses && <><dt>Image</dt><dd className="mono">{chosen.image}</dd></>}
                {chosen.needs && <><dt>Needs</dt><dd>{chosen.needs.join(", ")}</dd></>}
                {chosen.when && <><dt>When</dt><dd className="mono">{JSON.stringify(chosen.when)}</dd></>}
                {chosen.secrets && <><dt>Secrets</dt><dd className="mono">{Object.keys(chosen.secrets).sort().join(", ")}</dd></>}
                {chosen.kind !== "approval" && !chosen.uses && <><dt>Retry</dt><dd>{chosen.retry}</dd></>}
                {!chosen.uses && <><dt>{chosen.kind === "approval" ? "Expires after" : "Timeout"}</dt><dd>{shortDuration(chosen.timeout)}</dd></>}
              </dl>
              {chosen.kind !== "approval" && !chosen.uses && <pre className="code">{chosen.run}</pre>}
            </section>
          )}
        </>
      )}
    </div>
  );
}

export function ProblemList({ problems, onJump, title }: { problems: Problem[]; onJump?: (line: number) => void; title?: string }) {
  return (
    <section className="card" role="status">
      <h2 className="h-sm bad">{title ?? `${problems.length} problem${problems.length === 1 ? "" : "s"}`}</h2>
      <ul className="problems">
        {problems.map((p, i) => (
          <li key={i}>
            {p.file ? (
              <span className="problem-file" title={`In the template file ${p.file} of the library ${p.library}, not in this pipeline`}>
                {p.library}: {p.file}{p.line ? `:${p.line}` : ""}
              </span>
            ) : p.line ? (onJump
              ? <button type="button" className="linkbtn mono" onClick={() => onJump(p.line!)}>line {p.line}</button>
              : <span className="mono faint">line {p.line}</span>) : null}{" "}
            {p.path && <span className="mono">{p.path}</span>} {p.message}
          </li>
        ))}
      </ul>
    </section>
  );
}
