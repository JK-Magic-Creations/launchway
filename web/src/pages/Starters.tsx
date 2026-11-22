import { useEffect, useMemo, useState } from "react";
import { api } from "../api";
import { configApi, type LibraryTemplates, type TemplateInfo, type TemplateStep } from "../configApi";
import { Empty, ErrorBox, Mono } from "../components/ui";
import { useInstallation } from "../guided/facts";
import { PageLearn } from "../learn/LearnLink";
import { useTerms, terms } from "../terms";
import "../styles/starters.css";
import { prose } from "../prose";
import { BtnSm, DivLoading, DivPage, PSub, SpanFaint, ThCol, ThRow } from "../kit/bits";
const P = await prose("pages.Starters");

const FLIGHT = "flight plan";

/** The starters that ship with Launchway: ready-made pipelines (flight plans) and building blocks (step
 * templates), each with its inputs, an example file, what it runs, and a way to use it. */
export function StartersPage() {
  const words = useTerms();
  const [data, setData] = useState<LibraryTemplates | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [kind, setKind] = useState<"all" | "flight" | "step">("all");
  const [q, setQ] = useState("");
  useEffect(() => { configApi.templates("launchway").then(setData).catch(setError); }, []);
  const shown = useMemo(() => (data?.templates ?? []).filter((t) =>
    (kind === "all" || (kind === "flight") === (t.kind === FLIGHT)) &&
    (q.trim() === "" || (t.name + " " + (t.description ?? "")).toLowerCase().includes(q.trim().toLowerCase()))), [data, kind, q]);
  const flights = shown.filter((t) => t.kind === FLIGHT);
  const steps = shown.filter((t) => t.kind !== FLIGHT);
  return (
    <DivPage>
      <header className="page-head">
        <div>
          <h1>{words.say("{Hangar}")}</h1>
          <PSub>{P.t0}<Mono>uses: launchway/go-service</Mono> {P.t1}</PSub>
          <PageLearn />
        </div>
      </header>
      <ErrorBox error={error} />
      {!data && !error && <DivLoading>Loading…</DivLoading>}
      {data && (
        <>
          <div className="starters-filter" role="search">
            <label>Find a starter <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="go, python, image…" /></label>
            <label>Show{" "}
              <select value={kind} onChange={(e) => setKind(e.target.value as typeof kind)}>
                <option value="all">Everything</option>
                <option value="flight">{words.say("{Flight_plans}")} (whole pipelines)</option>
                <option value="step">{words.say("{Maneuvers}")} (building blocks)</option>
              </select>
            </label>
          </div>
          {shown.length === 0 && <Empty title="Nothing matches">{P.t2}</Empty>}
          {flights.length > 0 && <Group title={words.say("{Flight_plans}")} blurb={P.t3} items={flights} />}
          {steps.length > 0 && <Group title={words.say("{Maneuvers}")} blurb={P.t4} items={steps} />}
        </>
      )}
    </DivPage>
  );
}

function Group({ title, blurb, items }: { title: string; blurb: string; items: TemplateInfo[] }) {
  return (
    <section className="starters-group" aria-label={title}>
      <h2 className="h-sm">{title}</h2>
      <p className="faint">{blurb}</p>
      <div className="starters-list">{items.map((t) => <Starter key={t.name} t={t} />)}</div>
    </section>
  );
}

function plain(desc?: string) {
  const d = (desc ?? "").replace(/^(Flight plan|Maneuver|Step template):\s*/, "");
  return d.charAt(0).toUpperCase() + d.slice(1);
}

function Starter({ t }: { t: TemplateInfo }) {
  const [open, setOpen] = useState(false);
  const id = `starter-${t.name}`;
  return (
    <article className="card starter" aria-labelledby={id}>
      <div className="starter-head">
        <div>
          <h3 id={id} className="h-sm"><Mono>launchway/{t.name}</Mono></h3>
          <p>{plain(t.description)}</p>
        </div>
        <BtnSm aria-expanded={open} aria-controls={`${id}-more`} onClick={() => setOpen(!open)}>
          {open ? "Hide details" : "Show details"}
        </BtnSm>
      </div>
      {open && <Details t={t} id={`${id}-more`} />}
    </article>
  );
}

function Details({ t, id }: { t: TemplateInfo; id: string }) {
  return (
    <div id={id} className="starter-more">
      <h4 className="h-xs">Inputs</h4>
      {t.params.length === 0 ? <p className="faint">It takes no inputs.</p> : (
        <table className="starter-params">
          <thead><tr><ThCol>Name</ThCol><ThCol>Type</ThCol><ThCol>Default</ThCol><ThCol>What it does</ThCol></tr></thead>
          <tbody>{t.params.map((p) => (
            <tr key={p.name}>
              <ThRow><Mono>{p.name}</Mono></ThRow><td>{p.type}</td>
              <td>{p.default === undefined ? <em>required</em> : <Mono>{p.default === "" ? "(empty)" : p.default}</Mono>}</td>
              <td>{p.description}{p.options && p.options.length > 0 && <>{p.description && !p.description.endsWith(".") ? "." : ""} One of {p.options.map((o, i) => <span key={o}>{i > 0 && ", "}<Mono>{o}</Mono></span>)}.</>}</td>
            </tr>
          ))}</tbody>
        </table>
      )}
      {t.example && <Example text={t.example} />}
      {t.graph && t.graph.length > 0 && <WhatItRuns steps={t.graph} />}
      <UseThis t={t} />
    </div>
  );
}

function Example({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try { await navigator.clipboard.writeText(text); setCopied(true); setTimeout(() => setCopied(false), 2000); } catch { /* the text is selectable */ }
  };
  return (
    <>
      <h4 className="h-xs">An example file <SpanFaint>(.launchway/pipeline.yaml)</SpanFaint></h4>
      <pre className="starter-code">{text}</pre>
      <BtnSm onClick={copy}>{copied ? "Copied" : "Copy"}</BtnSm>
    </>
  );
}

/** The steps, left to right by what they wait for. */
function WhatItRuns({ steps }: { steps: TemplateStep[] }) {
  const depth = new Map<string, number>();
  const byId = new Map(steps.map((s) => [s.id, s]));
  const d = (id: string, seen = new Set<string>()): number => {
    if (depth.has(id)) return depth.get(id)!;
    if (seen.has(id)) return 0;
    seen.add(id);
    const v = Math.max(-1, ...(byId.get(id)?.needs ?? []).filter((n) => byId.has(n)).map((n) => d(n, seen))) + 1;
    depth.set(id, v);
    return v;
  };
  const cols: TemplateStep[][] = [];
  for (const s of steps) (cols[d(s.id)] ??= []).push(s);
  return (
    <>
      <h4 className="h-xs">What it runs <SpanFaint>{P.t5}</SpanFaint></h4>
      <ol className="starter-graph" aria-label={P.t6}>
        {cols.filter(Boolean).map((col, i) => (
          <li key={i} className="starter-col">
            <ul>{col.map((s) => <li key={s.id} title={s.description}><Mono>{s.id.replace(/^ci\//, "")}</Mono>{s.kind === "image" && <SpanFaint> image</SpanFaint>}<span className="faint starter-step-d">{s.description}</span></li>)}</ul>
          </li>
        ))}
      </ol>
    </>
  );
}

function UseThis({ t }: { t: TemplateInfo }) {
  const inst = useInstallation();
  const projects = inst.projects.filter((p) => p.editor?.may_save);
  const [project, setProject] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [done, setDone] = useState<{ branch: string; url?: string } | null>(null);
  if (!t.example) return null;
  const go = async () => {
    setBusy(true); setErr(null);
    try {
      const file = await api.pipeline(project, "");
      if (file.exists && !window.confirm(`${project} already has a pipeline file. Replace it on a new branch, for a pull request?`)) { setBusy(false); return; }
      const res = await api.savePipeline(project, {
        base_ref: file.ref, base_sha: file.sha, pipeline: t.example!.replace("my-project", project), branch: "",
        message: `Use the launchway/${t.name} starter`,
      });
      setDone({ branch: res.branch, url: res.pull_request_url });
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  return (
    <div className="starter-use">
      <h4 className="h-xs">Use this</h4>
      {projects.length === 0 ? (
        <p className="faint">Copy the example into <Mono>.launchway/pipeline.yaml</Mono> {terms().say(P.t7)}</p>
      ) : (
        <div className="starter-use-row">
          <label>Add it to <select value={project} onChange={(e) => setProject(e.target.value)}>
            <option value="">{terms().say("choose a {project}")}</option>
            {projects.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
          </select></label>
          <button type="button" className="btn btn-primary btn-sm" disabled={!project || busy} onClick={go}>{busy ? "Adding…" : P.t8}</button>
        </div>
      )}
      {done && <p role="status">Saved on the branch <Mono>{done.branch}</Mono>. {done.url ? <a href={done.url} target="_blank" rel="noreferrer">Open the pull request</a> : P.t9}</p>}
      <ErrorBox error={err} />
    </div>
  );
}
