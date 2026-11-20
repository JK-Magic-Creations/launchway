import { useEffect, useState } from "react";
import { configApi, kindLabel, kindShown, useOverview, type ConfigChange, type ConfigOverview } from "../configApi";
import { ago } from "../format";
import { usePath } from "../router";
import { ConfigNav, WIZARDS } from "../components/ConfigNav";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import "../config.css";

// ConfigHomePage is where an administrator starts: what is configured, the
// guided set-ups, what things are called, and the latest changes.
export function ConfigHomePage() {
  const path = usePath();
  const { overview, error } = useOverview();
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [recent, setRecent] = useState<ConfigChange[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);

  useEffect(() => {
    if (!overview?.available) return;
    for (const k of overview.kinds) {
      configApi.list(k.path).then((r) => setCounts((c) => ({ ...c, [k.name]: r.objects.length })), setLoadError);
    }
    configApi.allHistory(8).then((h) => setRecent(h.changes), () => setRecent([]));
  }, [overview]);

  if (error) return <div className="page"><ErrorBox error={error} /></div>;
  if (!overview) return <div className="page"><div className="loading">Loading…</div></div>;
  if (!overview.available) {
    return (
      <div className="page">
        <h1>Configuration</h1>
        <Empty title="This server keeps its configuration in its file">
          This server does not keep configuration in its database, so there is nothing to change here: edit its configuration file and restart it.
          A newer Launchway keeps everything but the bootstrap settings in the database.
        </Empty>
      </div>
    );
  }
  if (!overview.admin) {
    return <div className="page"><h1>Configuration</h1><Empty title="Only administrators change the configuration">Ask someone with the admin role.</Empty></div>;
  }
  const lv = overview.levels;
  const noProjects = counts.project === 0;
  return (
    <div className="page cf-page">
      <ConfigNav overview={overview} path={path} />
      <header className="page-head">
        <div>
          <h1>Configuration</h1>
          <p className="sub">Everything an administrator sets up, kept in the database with the history of every change. Changes apply to every server within seconds, without a restart.</p>
        </div>
        <a className="btn" href="/api/v1/config/export" download>Export as YAML</a>
      </header>
      <ErrorBox error={loadError} />
      {overview.status?.problem && (
        <div className="callout callout-bad" role="alert">
          <strong>The newest configuration is not in effect.</strong> {overview.status.problem} The servers keep using revision {overview.status.revision}.
        </div>
      )}

      {noProjects && (
        <section className="card cf-firstrun" aria-labelledby="cf-firstrun-h">
          <div>
            <h2 id="cf-firstrun-h">Set up {overview.organisation?.display_name || "Launchway"}</h2>
            <p>There are no {lv.project.plural.toLowerCase()} yet. A few questions name things the way your organisation does, choose how people sign in,
              and connect your first repository. Nothing is created until you confirm at the end.</p>
          </div>
          <Link href="/config/wizard/first-run" className="btn btn-primary">Start the first-run setup</Link>
        </section>
      )}

      <section aria-labelledby="cf-wiz-h">
        <h2 id="cf-wiz-h" className="cf-h">Guided set-ups</h2>
        <p className="sub">Each asks plain questions, explains every choice, checks your answers with the server as you go, and shows exactly what it will create before it creates anything.</p>
        <ul className="cf-wizgrid">
          {WIZARDS.map((w) => (
            <li key={w.id}>
              <Link href={`/config/wizard/${w.id}`} className="cf-wizcard">
                <span className="cf-wizcard-title">{w.id === "connect-repository" ? `Connect a repository (a ${lv.project.singular.toLowerCase()})` : w.title}</span>
                <span className="cf-wizcard-text">{w.text}</span>
              </Link>
            </li>
          ))}
          {!noProjects && (
            <li><Link href="/config/wizard/first-run" className="cf-wizcard cf-wizcard-quiet">
              <span className="cf-wizcard-title">First-run setup</span>
              <span className="cf-wizcard-text">Name the levels, choose sign-in, connect a repository: the questions a new installation starts with.</span>
            </Link></li>
          )}
        </ul>
      </section>

      <div className="cf-two">
        <section className="card" aria-labelledby="cf-kinds-h">
          <h2 id="cf-kinds-h" className="cf-h">What is configured</h2>
          <ul className="cf-kindlist">
            {overview.kinds.filter((k) => kindShown(overview, k)).map((k) => (
              <li key={k.name}>
                <Link href={`/config/${k.path}`} className="cf-kindrow">
                  <span className="cf-kindrow-name">{kindLabel(overview, k)}</span>
                  {!k.singleton && <span className="cf-count" aria-label={`${counts[k.name] ?? "unknown"} configured`}>{counts[k.name] ?? "…"}</span>}
                  <span className="cf-kindrow-doc">{k.doc}</span>
                </Link>
              </li>
            ))}
          </ul>
        </section>
        <div className="cf-col">
          <Levels overview={overview} />
          <section className="card" aria-labelledby="cf-recent-h">
            <h2 id="cf-recent-h" className="cf-h">Latest changes</h2>
            {recent === null ? <p className="faint">Loading…</p> : recent.length === 0 ? <p className="faint">Nothing has changed yet.</p> : (
              <ul className="cf-recent">
                {recent.map((c) => {
                  const k = overview.kinds.find((x) => x.name === c.kind);
                  return (
                    <li key={c.id}>
                      <span className={`cf-action cf-action-${c.action}`}>{c.action}</span>{" "}
                      {k ? <Link href={`/config/${k.path}/${encodeURIComponent(c.name)}`}><Mono>{c.name}</Mono></Link> : <Mono>{c.name}</Mono>}
                      <span className="faint"> {k ? kindLabel(overview, k, false).toLowerCase() : c.kind} · {c.actor || "someone"} · {ago(c.at, Date.now())}</span>
                    </li>
                  );
                })}
              </ul>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}

function Levels({ overview }: { overview: ConfigOverview }) {
  const lv = overview.levels;
  return (
    <section className="card" aria-labelledby="cf-levels-h">
      <h2 id="cf-levels-h" className="cf-h">What things are called</h2>
      <dl className="kv cf-levels">
        <dt>The whole installation</dt><dd>{lv.installation.singular}</dd>
        <dt>Who owns the {lv.project.plural.toLowerCase()}</dt><dd>{lv.organisation.singular}{overview.organisation?.display_name ? `: ${overview.organisation.display_name}` : ""}</dd>
        {overview.units_in_use && <><dt>Groups of {lv.project.plural.toLowerCase()}</dt><dd>{lv.unit.plural} ({overview.units?.length ?? 0})</dd></>}
        <dt>One repository</dt><dd>{lv.project.singular}</dd>
      </dl>
      <p className="cf-help">
        {overview.units_in_use ? null : <>Groups of {lv.project.plural.toLowerCase()} with shared settings are off: nobody has made one. </>}
        <Link href="/config/installation">Rename the levels</Link>
        {overview.units_in_use ? null : <> or <Link href="/config/units/new">make the first group</Link></>}.
      </p>
    </section>
  );
}
