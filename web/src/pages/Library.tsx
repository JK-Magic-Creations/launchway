import { useEffect, useState } from "react";
import { configApi, type LibraryInfo, type LibraryTemplates } from "../configApi";
import { EntityLink } from "../components/EntityLink";
import { Empty, ErrorBox, Link } from "../components/ui";
import { ago } from "../format";
import { useInstallation } from "../guided/facts";
import { terms } from "../terms";
import { prose } from "../prose";
import { DivPage, PSub, SpanFaint } from "../kit/bits";
const P = await prose("pages.Library");

/** The library's front page: the building blocks that have something to show. */
export function LibraryPage() {
  const inst = useInstallation();
  const t = inst.facts.libraries ?? 0;
  return (
    <DivPage>
      <header className="page-head">
        <div>
          <h1>Library</h1>
          <PSub>{P.t0}</PSub>
        </div>
      </header>
      <div className="guided-tiles">
        {t > 0 && (
          <Link href="/library/templates" className="guided-tile">
            <span className="guided-tile-n">{t}</span>
            <span>{t === 1 ? "library of templates" : "libraries of templates"}<br /><SpanFaint>{P.t1}</SpanFaint></span>
          </Link>
        )}
        <Link href="/depot" className="guided-tile">
          <span className="guided-tile-n" aria-hidden="true">▣</span>
          <span>Images<br /><SpanFaint>{terms().say(P.t2)}</SpanFaint></span>
        </Link>
        <Link href="/depot/components" className="guided-tile">
          <span className="guided-tile-n" aria-hidden="true">◫</span>
          <span>Components<br /><SpanFaint>{terms().say(P.t3)}</SpanFaint></span>
        </Link>
      </div>
    </DivPage>
  );
}

/** Every template of every library the server reads, one row each. */
export function TemplatesPage() {
  const inst = useInstallation();
  const libs = inst.libraries;
  return (
    <DivPage>
      <header className="page-head">
        <div>
          <h1>Templates</h1>
          <PSub>{P.t4}</PSub>
        </div>
      </header>
      {inst.loaded && libs.length === 0 && (
        <Empty title="No libraries yet">{P.t5}</Empty>
      )}
      {libs.map((l) => <LibraryCard key={l.name} lib={l} />)}
    </DivPage>
  );
}

function LibraryCard({ lib }: { lib: LibraryInfo }) {
  const [data, setData] = useState<LibraryTemplates | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { configApi.templates(lib.name).then(setData).catch(setError); }, [lib.name]);
  return (
    <section className="card" aria-labelledby={`lib-${lib.name}`}>
      <div className="card-head">
        <h2 id={`lib-${lib.name}`} className="h-sm">{lib.name}{data && <SpanFaint> {data.version}</SpanFaint>}</h2>
        {lib.last_fetch > 0 && <SpanFaint>read {ago(lib.last_fetch)}</SpanFaint>}
      </div>
      <ErrorBox error={error} />
      {!data && !error && <p className="faint">Loading…</p>}
      {data && data.templates.length === 0 && <p className="faint">{P.t6}{data.version}.</p>}
      {data && data.templates.length > 0 && (
        <ul className="guided-rows guided-rows-flat">
          {data.templates.map((t) => (
            <li key={t.name} className="guided-row">
              <div className="guided-row-main">
                <EntityLink kind="template" name={t.name} library={lib.name} className="guided-row-name" />
                <SpanFaint>{t.description || "No description."}</SpanFaint>
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
