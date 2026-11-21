import { useEffect, useState } from "react";
import { api, verifyApi, type AIUsage, type ExplanationView, type ModelView, type Models, type RunDetail, type StepView } from "../api";
import { ago, clock } from "../format";
import { canOperate, useMe } from "../auth";
import { ErrorBox, Mono } from "./ui";
import { WizardButton } from "./WizardButton";
import { costText, usageText } from "./usageText";
import { terms } from "../terms";

// Models: asking one to explain a failed step, what an ai step's model
// said, and the models the server knows. A model's words are always shown
// as text, labelled as model output: they may be wrong, and they decide
// nothing here.

let modelsOnce: Promise<Models> | null = null;

/** The server's models, asked once per page load. */
export function useModels(): { models: Models | null; error: unknown } {
  const [models, setModels] = useState<Models | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    modelsOnce ??= verifyApi.models();
    modelsOnce.then((m) => alive && setModels(m)).catch((e) => { modelsOnce = null; if (alive) setError(e); });
    return () => { alive = false; };
  }, []);
  return { models, error };
}

/** Whether a project matches one of a model's patterns (* and ? only, as the server's path.Match). */
export function allowsProject(patterns: string[], project: string): boolean {
  return patterns.some((p) => new RegExp("^" + p.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*").replace(/\?/g, "[^/]") + "$").test(project));
}

// ExplainFailure offers to ask a model why a failed step failed, and lists
// the answers already given.
export function ExplainFailure({ run, step }: { run: RunDetail; step: StepView }) {
  const { models, error: modelsErr } = useModels();
  const me = useMe();
  const [earlier, setEarlier] = useState<ExplanationView[] | null>(null);
  const [chosen, setChosen] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [answer, setAnswer] = useState<ExplanationView | null>(null);

  useEffect(() => {
    let alive = true;
    setAnswer(null); setError(null);
    verifyApi.explanations(run.id, step.id).then((l) => alive && setEarlier(l)).catch(() => alive && setEarlier([]));
    return () => { alive = false; };
  }, [run.id, step.id, step.attempt]);

  if (modelsErr) return null; // a server without models' endpoint: nothing to offer
  if (!models) return null;
  const usable = models.models.filter((m) => allowsProject(m.projects ?? [], run.project));
  const model = chosen && usable.some((m) => m.name === chosen) ? chosen : usable[0]?.name ?? "";
  const mayAsk = canOperate(me, run.project);
  const list = [...(answer ? [answer] : []), ...(earlier ?? []).filter((x) => x.id !== answer?.id)];

  const ask = async () => {
    setBusy(true); setError(null);
    try {
      const x = await verifyApi.explain(run.id, step.id, model);
      setAnswer(x);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };

  let offer;
  if (!models.server_can_explain || models.models.length === 0) {
    offer = (
      <div className="xform">
        <p className="sub small">Launchway can read the log and suggest a cause. It is optional and off until you choose a model.</p>
        {me?.admin ? <div><WizardButton wizard="add-model" label="Set up an AI model" primary={false} /></div> : <p className="sub small">An administrator can turn this on.</p>}
      </div>
    );
  } else if (usable.length === 0) {
    offer = <p className="sub small">No AI model is open to {run.project}; an administrator can choose which projects may use a model.</p>;
  } else {
    const why = !mayAsk ? `You need the operator role on ${run.project} to ask a model.` : "";
    offer = (
      <div className="xform">
        {usable.length > 1 && (
          <label className="field xmodel">
            <span>Model</span>
            <select value={model} onChange={(e) => setChosen(e.target.value)} disabled={busy}>
              {usable.map((m) => <option key={m.name} value={m.name}>{m.name} ({m.local ? "local" : m.provider})</option>)}
            </select>
          </label>
        )}
        <div className="approval-actions">
          <button type="button" className="btn" disabled={!mayAsk || busy || !model} aria-describedby={why ? `xwhy-${step.id}` : undefined} onClick={ask}>
            <SparkIcon />{busy ? `Asking ${model}…` : "Explain this failure"}
          </button>
        </div>
        {why && <p id={`xwhy-${step.id}`} className="dctl-why">{why}</p>}
        <p className="sub small">
          Sends {usable.length === 1 ? <>model <strong>{model}</strong></> : "the model"} {terms().say("the step's error, the last 200 lines of its log (secrets already masked by the {runner}) and its failed tests.")}
          {(() => { const m = usable.find((x) => x.name === model); return m ? <> {m.local ? "It runs on a server of the operator's own." : `It is a ${m.provider} model, reached over the network.`}</> : null; })()}
        </p>
      </div>
    );
  }

  return (
    <section className="card xcard" aria-label="Explain this failure">
      <h3 className="h-sm">Explain this failure</h3>
      {offer}
      <ErrorBox error={error} />
      {list.length > 0 && (
        <ol className="xlist">
          {list.map((x, i) => (
            <li key={x.id}>
              {i === 0 ? <ModelAnswer x={x} /> : (
                <details>
                  <summary>Earlier: {x.model}, asked by {x.actor} <span className="faint" title={clock(x.created_at)}>{ago(x.created_at)}</span></summary>
                  <ModelAnswer x={x} />
                </details>
              )}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function ModelAnswer({ x }: { x: ExplanationView }) {
  return (
    <aside className="vexpl" aria-label={`Model output from ${x.model}`}>
      <div className="vexpl-head">
        <span className="vexpl-badge">Model output</span>
        <span>From <strong>{x.model}</strong>, which may be wrong · asked by {x.actor}, <span title={clock(x.created_at)}>{ago(x.created_at)}</span>{x.attempt > 1 ? ` · attempt ${x.attempt}` : ""}</span>
      </div>
      <p className="vexpl-text">{x.text}</p>
      <div className="vexpl-usage faint">{usageText(x.usage)}</div>
    </aside>
  );
}

function SparkIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <path d="M8 1.5l1.5 4 4 1.5-4 1.5L8 12.5 6.5 8.5l-4-1.5 4-1.5zM13 11l.6 1.4 1.4.6-1.4.6L13 15l-.6-1.4L11 13l1.4-.6z" />
    </svg>
  );
}

// AIStepResult shows what an ai step asked and what the model answered.
export function AIStepResult({ run, step }: { run: RunDetail; step: StepView }) {
  const def = run.graph.steps.find((s) => s.id === step.id)?.ai;
  const o = step.outputs ?? {};
  const [usage, setUsage] = useState<{ usage: AIUsage; model_id?: string; provider?: string; stop_reason?: string } | null>(null);
  const finished = step.status === "succeeded" || step.status === "failed";
  useEffect(() => {
    if (!finished) { setUsage(null); return; }
    let alive = true;
    api.events(run.id).then((evs) => {
      const last = [...evs].reverse().find((e) => e.type === "step.finished" && e.data.step === step.id && e.data.ai);
      if (alive) setUsage((last?.data.ai as typeof usage) ?? null);
    }).catch(() => {});
    return () => { alive = false; };
  }, [run.id, step.id, step.attempt, finished]);
  if (!def) return null;
  const inTok = usage?.usage.input_tokens ?? (o.input_tokens ? Number(o.input_tokens) : undefined);
  const outTok = usage?.usage.output_tokens ?? (o.output_tokens ? Number(o.output_tokens) : undefined);
  return (
    <section className="card dep" aria-label="What the model was asked">
      <div className="dep-head">
        <h3 className="h-sm">Asks model {def.model}</h3>
        {(usage?.model_id || o.model) && <span className="faint small">answered by <Mono>{usage?.model_id || o.model}</Mono>{usage?.provider ? ` (${usage.provider})` : ""}</span>}
      </div>
      <details className="vreadings">
        <summary>The prompt</summary>
        {def.instructions && <pre className="code xprompt">{def.instructions}</pre>}
        <pre className="code xprompt">{def.prompt}</pre>
      </details>
      {o.text !== undefined && (
        <aside className="vexpl" aria-label={`Model output from ${def.model}`}>
          <div className="vexpl-head"><span className="vexpl-badge">Model output</span><span>From <strong>{def.model}</strong>, which may be wrong. It runs nothing by itself.</span></div>
          <p className="vexpl-text">{o.text}</p>
        </aside>
      )}
      {(inTok !== undefined || outTok !== undefined) && (
        <div className="vexpl-usage">
          {inTok !== undefined && <span>{inTok.toLocaleString()} tokens in</span>}
          {outTok !== undefined && <span>{outTok.toLocaleString()} out</span>}
          {usage?.usage.cost_usd ? <span>about {costText(usage.usage.cost_usd)}</span> : usage ? <span className="faint">no price known for this model</span> : <span className="faint">the cost is in the step's log</span>}
          {usage?.stop_reason && usage.stop_reason !== "end_turn" && <span className="tfile-warn">stopped: {usage.stop_reason.replace(/_/g, " ")}</span>}
        </div>
      )}
    </section>
  );
}

// ModelsSection lists the models the server knows: read-only.
export function ModelsSection() {
  const { models, error } = useModels();
  return (
    <section className="card">
      <h2 className="h-sm">AI models</h2>
      <p className="sub">
        Models that ai steps and verify steps may ask, and that people may ask to explain a failed step.
        With none, nothing is ever sent to a model and everything else works.
      </p>
      <ErrorBox error={error} />
      {models && models.models.length === 0 && <p className="sub"><strong>No AI model is set up.</strong></p>}
      <div><WizardButton wizard="add-model" label="Add an AI model" primary={!models?.models.length} small /></div>
      {models && models.models.length > 0 && (
        <div className="table-scroll models-wrap">
          <table className="inputs-table models-table">
            <thead><tr><th>Model</th><th>Where</th><th>Projects</th><th>Limits</th><th>Used today (UTC)</th></tr></thead>
            <tbody>
              {models.models.map((m) => <ModelRow key={m.name} m={m} />)}
            </tbody>
          </table>
        </div>
      )}
      {models && models.models.length > 0 && !models.server_can_explain && (
        <p className="sub small">This server does not ask models to explain failed steps.</p>
      )}
    </section>
  );
}

function ModelRow({ m }: { m: ModelView }) {
  const used = m.used_today ?? [];
  const tokens = used.reduce((n, u) => n + u.input_tokens + u.output_tokens, 0);
  const cost = used.reduce((n, u) => n + u.cost_usd, 0);
  return (
    <tr>
      <td data-label="Model"><strong>{m.name}</strong><div className="faint small mono">{m.model}</div></td>
      <td data-label="Where">{m.local ? "local, no key" : <>remote, {m.provider}</>}</td>
      <td data-label="Projects">{m.projects?.length ? m.projects.map((p, i) => <span key={p}>{i > 0 && ", "}<Mono>{p}</Mono></span>) : <span className="faint">none</span>}</td>
      <td data-label="Limits" className="small">
        <div>{m.max_tokens.toLocaleString()} tokens out per call</div>
        <div>{Math.round(m.max_input_bytes / 1024).toLocaleString()} KiB in per call</div>
        <div>{m.calls_per_run} {terms().say("calls per {run}")}</div>
        <div>{m.tokens_per_day ? `${m.tokens_per_day.toLocaleString()} tokens a day per project` : "no daily limit"}</div>
      </td>
      <td data-label="Used today" className="small">
        {used.length === 0 ? <span className="faint">nothing yet</span> : (
          <>
            <div>{used.reduce((n, u) => n + u.calls, 0)} calls, {tokens.toLocaleString()} tokens{cost ? `, about ${costText(cost)}` : ""}</div>
            {used.map((u) => (
              <div key={u.project} className="faint">
                {u.project}: {u.calls} call{u.calls === 1 ? "" : "s"}, {(u.input_tokens + u.output_tokens).toLocaleString()} tokens
                {m.tokens_per_day ? ` (${Math.round(((u.input_tokens + u.output_tokens) / m.tokens_per_day) * 100)}% of its day)` : ""}
              </div>
            ))}
          </>
        )}
      </td>
    </tr>
  );
}

// HealthSourcesSection lists the health sources verify steps may read: read-only.
export function HealthSourcesSection() {
  const [list, setList] = useState<{ name: string; type: string; projects: string[] }[] | null>(null);
  useEffect(() => { verifyApi.healthSources().then(setList).catch(() => setList([])); }, []);
  if (!list) return null;
  return (
    <section className="card">
      <h2 className="h-sm">Health sources</h2>
      <p className="sub">{terms().say("Monitoring that verify steps may read, by name, to judge a deployment. Where each is and its credentials stay with the {deploy:do} {runners}.")}</p>
      {list.length === 0 ? <p className="sub"><strong>None yet</strong>: verify steps are refused.</p> : (
        <ul className="hsources">
          {list.map((h) => (
            <li key={h.name}><strong className="mono">{h.name}</strong> <span className="faint">{h.type}</span> <span className="small">for {h.projects.map((p, i) => <span key={p}>{i > 0 && ", "}<Mono>{p}</Mono></span>)}</span></li>
          ))}
        </ul>
      )}
      <div><WizardButton wizard="add-health-source" label="Add a health source" primary={list.length === 0} small /></div>
    </section>
  );
}
