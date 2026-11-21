import { useEffect, useState } from "react";
import { configApi, type LibraryInfo, type LibraryTemplates } from "../configApi";
import { EntityLink } from "../components/EntityLink";
import { Empty, ErrorBox, Link } from "../components/ui";
import { ago } from "../format";
import { useInstallation } from "../guided/facts";
import { terms } from "../terms";

/** The library's front page: the building blocks that have something to show. */
export function LibraryPage() {
  const inst = useInstallation();
  const t = inst.facts.libraries ?? 0;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Library</h1>
          <p className="sub">The building blocks, to look things up. Every name opens its own page.</p>
        </div>
      </header>
      <div className="guided-tiles">
        {t > 0 && (
          <Link href="/library/templates" className="guided-tile">
            <span className="guided-tile-n">{t}</span>
            <span>{t === 1 ? "library of templates" : "libraries of templates"}<br /><span className="faint">Ready-made pipelines, each in Git and pinned by version.</span></span>
          </Link>
        )}
        <Link href="/images" className="guided-tile">
          <span className="guided-tile-n" aria-hidden="true">▣</span>
          <span>Images<br /><span className="faint">{terms().say("What your {runs} built, found by digest, repository or project.")}</span></span>
        </Link>
        <Link href="/images/components" className="guided-tile">
          <span className="guided-tile-n" aria-hidden="true">◫</span>
          <span>Components<br /><span className="faint">{terms().say("What is inside your software, found by name across every {run}.")}</span></span>
        </Link>
      </div>
    </div>
  );
}

/** Every template of every library the server reads, one row each. */
export function TemplatesPage() {
  const inst = useInstallation();
  const libs = inst.libraries;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Templates</h1>
          <p className="sub">A template is a ready-made part of a pipeline, kept in a library repository in Git. A pipeline pins the version it uses, so a run can always be repeated exactly.</p>
        </div>
      </header>
      {inst.loaded && libs.length === 0 && (
        <Empty title="No libraries yet">A library is a Git repository of templates. Once the server reads one, its templates are listed here with their versions.</Empty>
      )}
      {libs.map((l) => <LibraryCard key={l.name} lib={l} />)}
    </div>
  );
}

function LibraryCard({ lib }: { lib: LibraryInfo }) {
  const [data, setData] = useState<LibraryTemplates | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { configApi.templates(lib.name).then(setData).catch(setError); }, [lib.name]);
  return (
    <section className="card" aria-labelledby={`lib-${lib.name}`}>
      <div className="card-head">
        <h2 id={`lib-${lib.name}`} className="h-sm">{lib.name}{data && <span className="faint"> {data.version}</span>}</h2>
        {lib.last_fetch > 0 && <span className="faint">read {ago(lib.last_fetch)}</span>}
      </div>
      <ErrorBox error={error} />
      {!data && !error && <p className="faint">Loading…</p>}
      {data && data.templates.length === 0 && <p className="faint">This library has no templates at {data.version}.</p>}
      {data && data.templates.length > 0 && (
        <ul className="guided-rows guided-rows-flat">
          {data.templates.map((t) => (
            <li key={t.name} className="guided-row">
              <div className="guided-row-main">
                <EntityLink kind="template" name={t.name} library={lib.name} className="guided-row-name" />
                <span className="faint">{t.description || "No description."}</span>
              </div>
              <div className="guided-row-side faint">
                {t.params.length} {t.params.length === 1 ? "input" : "inputs"}
                {t.problems && t.problems.length > 0 && <span className="guided-warn">{t.problems.length} problem{t.problems.length > 1 ? "s" : ""}</span>}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
