import { useEffect, useState } from "react";
import type { LibraryInfo } from "../configApi";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { useInstallation } from "../guided/facts";
import { stepsApi, type LibrarySteps, type StepChange, type StepPageData } from "../stepsApi";

/** Every custom step of every library the server reads: what each takes, gives and may do. */
export function StepsPage() {
  const inst = useInstallation();
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Steps</h1>
          <p className="sub">A step is a program in a library, in any language, with typed inputs and outputs and a list of what it may do. A pipeline pins the version it uses.</p>
        </div>
      </header>
      {inst.loaded && inst.libraries.length === 0 && <Empty title="No libraries yet">A library is a Git repository of templates and steps. Once the server reads one, its steps are listed here.</Empty>}
      {inst.libraries.map((l) => <LibrarySteps key={l.name} lib={l} />)}
    </div>
  );
}

function LibrarySteps({ lib }: { lib: LibraryInfo }) {
  const [data, setData] = useState<LibrarySteps | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { stepsApi.list(lib.name).then(setData).catch(setError); }, [lib.name]);
  return (
    <section className="card" aria-labelledby={`steps-${lib.name}`}>
      <div className="card-head"><h2 id={`steps-${lib.name}`} className="h-sm">{lib.name}{data && data.version && <span className="faint"> {data.version}</span>}</h2></div>
      <ErrorBox error={error} />
      {!data && !error && <p className="faint">Loading…</p>}
      {data && data.steps.length === 0 && <p className="faint">This library has no steps{data.version ? ` at ${data.version}` : ""}.</p>}
      {data && data.steps.length > 0 && (
        <ul className="guided-rows guided-rows-flat">
          {data.steps.map((s) => (
            <li key={s.name} className="guided-row">
              <div className="guided-row-main">
                <Link href={`/library/steps/${encodeURIComponent(lib.name)}/${encodeURIComponent(s.name)}`} className="guided-row-name">{s.name}</Link>
                <span className="faint">{s.description || "No description."}</span>
                {s.sentence && <span className="faint">{s.sentence}</span>}
                {s.problems?.map((p) => <span key={p} className="faint">Problem: {p}</span>)}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Changes({ changes }: { changes?: StepChange[] }) {
  if (!changes || changes.length === 0) return null;
  return <ul className="faint">{changes.map((c) => <li key={c.what}>{c.widening ? <strong>Asks for more: </strong> : null}{c.what}</li>)}</ul>;
}

/** One step's page: its manifest as a table, what it may do, its versions with what changed, who uses it. */
export function StepPage({ library, name }: { library: string; name: string }) {
  const [data, setData] = useState<StepPageData | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { stepsApi.page(library, name).then(setData).catch(setError); }, [library, name]);
  if (error) return <div className="page"><ErrorBox error={error} /></div>;
  if (!data) return <div className="page"><p className="faint">Loading…</p></div>;
  const s = data.step;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{s.name}</h1>
          <p className="sub">{s.description || "No description."} In {library} at {data.version}.</p>
        </div>
      </header>
      <section className="card" aria-labelledby="step-may">
        <h2 id="step-may" className="h-sm">What it may do</h2>
        <p>{s.sentence}</p>
        <ul>{s.permissions.map((p) => <li key={p.Kind}>{p.Detail}</li>)}</ul>
        <p className="faint">It runs in <Mono>{s.image}</Mono> with no more than this, in a read-only root filesystem.</p>
      </section>
      {data.newest && (
        <div className={`callout ${data.newest_widens ? "callout-bad" : ""}`} role="note">
          <strong>{data.newest} is newer.</strong> {data.newest_widens ? "It asks for more than this version does. " : ""}
          <Changes changes={data.newest_changes} />
        </div>
      )}
      <section className="card" aria-labelledby="step-inputs">
        <h2 id="step-inputs" className="h-sm">Inputs</h2>
        {s.inputs.length === 0 ? <p className="faint">None.</p> : (
          <table className="table">
            <caption className="sr-only">Inputs of {s.name}</caption>
            <thead><tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">Required</th><th scope="col">Default</th><th scope="col">What</th></tr></thead>
            <tbody>{s.inputs.map((i) => (
              <tr key={i.name}>
                <th scope="row"><Mono>{i.name}</Mono></th><td>{i.type}</td><td>{i.required ? "yes" : "no"}</td><td>{i.default ?? ""}</td>
                <td>{i.description}{i.options ? ` One of ${i.options.join(", ")}.` : ""}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </section>
      <section className="card" aria-labelledby="step-outputs">
        <h2 id="step-outputs" className="h-sm">Outputs</h2>
        {s.outputs.length === 0 ? <p className="faint">None.</p> : (
          <table className="table">
            <caption className="sr-only">Outputs of {s.name}</caption>
            <thead><tr><th scope="col">Name</th><th scope="col">Type</th><th scope="col">What</th></tr></thead>
            <tbody>{s.outputs.map((o) => <tr key={o.name}><th scope="row"><Mono>{o.name}</Mono></th><td>{o.type}</td><td>{o.description}</td></tr>)}</tbody>
          </table>
        )}
      </section>
      <section className="card" aria-labelledby="step-example">
        <h2 id="step-example" className="h-sm">Use it</h2>
        <pre className="code">{`libraries:\n  ${library}: { repo: ${library}, version: ${data.version} }\n${s.example}`}</pre>
      </section>
      <section className="card" aria-labelledby="step-versions">
        <h2 id="step-versions" className="h-sm">Versions</h2>
        {data.versions.length === 0 ? <p className="faint">No tagged versions.</p> : (
          <ul>{data.versions.map((v) => (
            <li key={v.version}><strong>{v.version}</strong> <span className="faint">{v.commit.slice(0, 8)}{v.widens ? " asks for more than the version before" : ""}</span><Changes changes={v.changes} /></li>
          ))}</ul>
        )}
      </section>
      <section className="card" aria-labelledby="step-used">
        <h2 id="step-used" className="h-sm">Who uses it</h2>
        {data.used_by.length === 0 ? <p className="faint">No Flight you may see has run it.</p> : (
          <ul>{data.used_by.map((u) => (
            <li key={u.project + u.commit}>
              <Link href={`/code/${encodeURIComponent(u.project)}`}>{u.project}</Link> at <Mono>{u.commit.slice(0, 8)}</Mono>, {u.runs} {u.runs === 1 ? "run" : "runs"}
              {u.newest && <> ({u.newest} is newer{u.widens ? " and asks for more" : ""})</>}
            </li>
          ))}</ul>
        )}
      </section>
    </div>
  );
}
