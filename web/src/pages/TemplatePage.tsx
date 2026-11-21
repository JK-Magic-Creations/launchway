import { useEffect, useState } from "react";
import { configApi, type LibraryTemplates, type TemplateInfo } from "../configApi";
import { EntityLink } from "../components/EntityLink";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { useInstallation } from "../guided/facts";
import { useCrumbLabel } from "../nav/crumbs";
import { PageTabs, useTab } from "../components/PageTabs";

const TABS = [
  { id: "overview", label: "Overview" },
  { id: "interface", label: "Interface" },
  { id: "versions", label: "Versions" },
  { id: "used", label: "Used by" },
];

/** One template: what it does, the inputs and outputs it promises, the versions that exist, and who uses it. */
export function TemplatePage({ library, name }: { library: string; name: string }) {
  const [version, setVersion] = useState("");
  const [data, setData] = useState<LibraryTemplates | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [tab, setTab] = useTab(TABS.map((t) => t.id), "overview");
  const inst = useInstallation();
  useCrumbLabel(name);
  useEffect(() => { configApi.templates(library, version || undefined).then((d) => { setData(d); setError(null); }).catch(setError); }, [library, version]);

  const tpl: TemplateInfo | undefined = data?.templates.find((t) => t.name === name);
  const users = inst.projects.filter((p) => p.pipeline?.library === library && p.pipeline.template === name);
  if (error) return <div className="page"><ErrorBox error={error} /></div>;
  if (!data) return <div className="page"><div className="loading">Loading…</div></div>;
  if (!tpl) return <div className="page"><Empty title={`${library} has no template called ${name}${version ? ` at ${version}` : ""}`}><Link href="/library/templates">See the templates</Link></Empty></div>;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{tpl.name}</h1>
          <p className="sub">A template of the library <Mono>{library}</Mono>, at <Mono>{data.version}</Mono> (commit <Mono>{data.commit.slice(0, 8)}</Mono>).</p>
        </div>
      </header>
      <section className="card glance" aria-label="At a glance">
        <dl className="glance-grid">
          <div><dt>What is this?</dt><dd>{tpl.description || "A template with no description."}</dd></div>
          <div><dt>Where does it come from?</dt><dd>The Git repository of the library <Mono>{library}</Mono>, version <Mono>{data.version}</Mono>.</dd></div>
          <div><dt>Who uses it?</dt><dd>{users.length === 0 ? "No project on this server uses it yet." : users.map((p, i) => <span key={p.name}>{i > 0 && ", "}<EntityLink kind="project" name={p.name} /></span>)}</dd></div>
          <div><dt>What happened to it recently?</dt><dd>{data.versions.length > 0 ? <>{data.versions.length} version{data.versions.length > 1 ? "s" : ""} exist; this page shows <Mono>{data.version}</Mono>.</> : "It has no tagged versions."}</dd></div>
          <div><dt>What can I do with it?</dt><dd className="glance-actions"><button type="button" className="linkbtn" onClick={() => setTab("interface")}>See what it takes</button><button type="button" className="linkbtn" onClick={() => setTab("versions")}>See its versions</button></dd></div>
        </dl>
      </section>
      <PageTabs tabs={TABS} current={tab} onChange={setTab} />
      {tab === "overview" && (
        <section className="card" aria-label="Overview">
          <h2 className="h-sm">How a pipeline uses it</h2>
          <p>A step in a pipeline says <Mono>uses: {library}/{tpl.name}@{data.version}</Mono> and gives the inputs below. Launchway reads the template from Git at that version, so the same pipeline always means the same steps.</p>
          {tpl.problems && tpl.problems.length > 0 && (
            <div className="callout callout-bad">{tpl.problems.map((p) => p.message).join("\n")}</div>
          )}
        </section>
      )}
      {tab === "interface" && (
        <>
          <section className="card" aria-label="Inputs">
            <h2 className="h-sm">Inputs</h2>
            {tpl.params.length === 0 ? <p className="faint">It takes no inputs.</p> : (
              <div className="tablewrap"><table className="guided-table">
                <thead><tr><th>Name</th><th>Kind</th><th>Default</th><th>What it means</th></tr></thead>
                <tbody>{tpl.params.map((p) => (
                  <tr key={p.name}><td className="mono">{p.name}</td><td>{p.type}{p.options?.length ? ` (${p.options.join(", ")})` : ""}</td>
                    <td>{p.default !== undefined && p.default !== "" ? <Mono>{p.default}</Mono> : <span className="faint">required</span>}</td><td>{p.description}</td></tr>
                ))}</tbody>
              </table></div>
            )}
          </section>
          <section className="card" aria-label="Outputs">
            <h2 className="h-sm">Outputs</h2>
            {tpl.outputs.length === 0 ? <p className="faint">It gives nothing to later steps.</p> : <ul className="guided-list">{tpl.outputs.map((o) => <li key={o}><Mono>{o}</Mono></li>)}</ul>}
            <p className="faint">A pipeline that asks for an input that does not exist is refused before it runs.</p>
          </section>
        </>
      )}
      {tab === "versions" && (
        <section className="card" aria-label="Versions">
          <h2 className="h-sm">Versions</h2>
          {data.versions.length === 0 ? <p className="faint">This library has no tags yet, so pipelines pin a branch or a commit.</p> : (
            <ul className="guided-list">
              {data.versions.map((v) => (
                <li key={v}>
                  <Mono>{v}</Mono>
                  <span className="guided-grow faint">{v === data.version ? "shown now" : ""}</span>
                  {v !== data.version && <button type="button" className="linkbtn" onClick={() => setVersion(v)}>Show this version</button>}
                </li>
              ))}
            </ul>
          )}
          <p className="faint">What changed between two versions will appear here once releases carry notes.</p>
        </section>
      )}
      {tab === "used" && (
        <section className="card" aria-label="Used by">
          <h2 className="h-sm">Used by</h2>
          {users.length === 0 ? <p className="faint">No project on this server pins this template. A project that does is listed here with the version it uses.</p> : (
            <ul className="guided-list">{users.map((p) => (
              <li key={p.name}><EntityLink kind="project" name={p.name} /><span className="guided-grow faint">pinned to <Mono>{p.pipeline?.version}</Mono></span></li>
            ))}</ul>
          )}
        </section>
      )}
    </div>
  );
}
