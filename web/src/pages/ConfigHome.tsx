import { PageHead } from "../kit/layout";
import { useEffect, useState } from "react";
import { api } from "../api";
import { useMe } from "../auth";
import { configApi, kindLabel, kindShown, useOverview, type ConfigChange, type ConfigOverview } from "../configApi";
import { ago } from "../format";
import { usePath } from "../router";
import { ConfigNav, WIZARDS } from "../components/ConfigNav";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { levelWords } from "../configKinds";
import "../config.css";
import { terms } from "../terms";

// ConfigHomePage is where an administrator starts: what is configured, the
// guided set-ups, what things are called, and the latest changes.
export function ConfigHomePage() {
  const path = usePath();
  const { overview, error } = useOverview();
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [recent, setRecent] = useState<ConfigChange[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [now] = useState(() => Date.now());

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
          This server does not keep configuration in its database, so there is nothing to change here.
          Upgrade Launchway to a version that does, and everything but the bootstrap settings is set up here instead, with its history.
        </Empty>
      </div>
    );
  }
  if (!overview.admin) {
    return <div className="page"><h1>Configuration</h1><Empty title="Only administrators change the configuration">Ask someone with the admin role.</Empty></div>;
  }
  const lv = overview.levels;
  return (
    <div className="page cf-page">
      <ConfigNav overview={overview} path={path} />
      <PageHead eyebrow="Settings" title={<>Configuration</>} sentence={<>Everything an administrator sets up, kept in the database with the history of every change. Changes apply to every server within seconds, without a restart.</>} actions={<><a className="btn" href="/api/v1/config/export" download>Export as YAML</a></>} />
      <ErrorBox error={loadError} />
      {overview.status?.safe_mode && (
        <div className="callout callout-bad" role="alert">
          <strong>This server is in safe mode.</strong> The stored configuration could not be loaded ({overview.status.safe_mode.reason}), so it runs with {overview.status.safe_mode.using}. It tries the stored configuration again every few seconds; fix the problem named here and it leaves safe mode by itself.
        </div>
      )}
      {overview.status?.problem && !overview.status.safe_mode && (
        <div className="callout callout-bad" role="alert">
          <strong>The newest configuration is not in effect.</strong> {overview.status.problem} The servers keep using revision {overview.status.revision}; the change is stored and is tried again every few seconds.
        </div>
      )}
      {overview.status?.servers && overview.status.servers.length > 0 && (
        <details className="cf-servers">
          <summary>Which revision each server runs (stored revision {overview.status.stored_revision ?? overview.status.revision})</summary>
          <table className="table">
            <thead><tr><th>Server</th><th>Applied revision</th><th>State</th></tr></thead>
            <tbody>
              {overview.status.servers.map((s) => (
                <tr key={s.server}>
                  <td className="mono">{s.server}{s.server === overview.status?.server ? " (this one)" : ""}</td>
                  <td>{s.revision}</td>
                  <td>{s.state === "applied" && s.revision === (overview.status?.stored_revision ?? s.revision) ? "up to date" : s.state === "failed" ? `could not apply: ${s.problem ?? ""}` : "behind: it applies the stored revision within seconds"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}

      <Checklist overview={overview} counts={counts} />

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
          {(
            <li><Link href="/config/wizard/first-run" className="cf-wizcard cf-wizcard-quiet">
              <span className="cf-wizcard-title">First-run setup</span>
              <span className="cf-wizcard-text">Choose the words, choose sign-in, connect a repository: the questions a new {terms().lower("installation")} starts with.</span>
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
                  <span className="cf-kindrow-doc">{levelWords(k.doc, overview.levels)}</span>
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
                      <span className="faint"> {k ? kindLabel(overview, k, false).toLowerCase() : c.kind} · {c.actor || "someone"} · {ago(c.at, now)}</span>
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

/** Checklist is the first-run setup as five things to do, each ticked off
 * when it is done, each opening the wizard that does it. */
function Checklist({ overview, counts }: { overview: ConfigOverview; counts: Record<string, number> }) {
  const me = useMe();
  const [named, setNamed] = useState<boolean | null>(null);
  const [runner, setRunner] = useState<boolean | null>(null);
  useEffect(() => {
    configApi.get("installation", "installation").then((o) => setNamed(!!(o.spec as { levels?: unknown; terms?: unknown }).levels || !!(o.spec as { terms?: unknown }).terms || !!overview.organisation?.display_name))
      .catch(() => setNamed(!!overview.organisation?.display_name));
    api.runners(true).then((rs) => setRunner(rs.some((r) => !/\(built in\)/.test(r.name)))).catch(() => setRunner(false));
  }, [overview]);
  const lv = overview.levels;
  const items = [
    { done: named, title: "Choose the words", text: `Aviation words or plain business ones, and what your ${terms().lower("organization")}, its groups and its ${terms().pluralLower("project")} are called, so the screens use your words.`, href: "/config/wizard/first-run?step=words" },
    { done: me?.auth_mode === "oidc", title: "Choose how people sign in", text: "Your company's identity provider, so people use the accounts they have and groups decide what they may do.", href: "/config/wizard/first-run?step=signin" },
    { done: counts.project === undefined ? null : counts.project > 0, title: "Connect a repository", text: `Your first ${lv.project.singular.toLowerCase()}: its pushes start runs of its pipeline.`, href: "/config/wizard/connect-repository" },
    { done: runner, title: "Add a runner", text: "A machine, container or Kubernetes pod of your own that does the steps of runs (the server's built-in runner is for trying things out).", href: "/config/wizard/add-runner" },
    { done: counts.environment === undefined ? null : counts.environment > 0, title: "Add an environment", text: "A place runs deploy to, such as staging or prod.", href: "/config/wizard/add-environment" },
  ];
  const left = items.filter((i) => i.done === false).length;
  const all = items.every((i) => i.done);
  return (
    <section className={"card cf-checklist" + (all ? " cf-checklist-done" : "")} aria-labelledby="cf-check-h">
      <details open={!all}>
        <summary><h2 id="cf-check-h" className="cf-h">Set up {overview.organisation?.display_name || "Launchway"}</h2>
          <span className="faint">{all ? "All done." : left ? `${left} of ${items.length} to do` : "Checking…"}</span></summary>
        <ol className="cf-checkitems">
          {items.map((i, n) => (
            <li key={i.title} className={i.done ? "cf-check-on" : ""}>
              <span className="cf-checkmark" aria-hidden="true">{i.done ? "✓" : n + 1}</span>
              <span className="cf-checktext">
                <Link href={i.href}>{i.title}</Link><span className="visually-hidden">{i.done ? " (done)" : i.done === false ? " (to do)" : ""}</span>
                <span className="cf-help">{i.text}</span>
              </span>
            </li>
          ))}
        </ol>
      </details>
    </section>
  );
}

function Levels({ overview }: { overview: ConfigOverview }) {
  const lv = overview.levels;
  return (
    <section className="card" aria-labelledby="cf-levels-h">
      <h2 id="cf-levels-h" className="cf-h">What things are called</h2>
      <dl className="kv cf-levels">
        <dt>The whole {terms().lower("installation")}</dt><dd>{terms().label("installation")}</dd>
        <dt>Who owns the {terms().pluralLower("project")}</dt><dd>{terms().label("organization")}{overview.organisation?.display_name ? `: ${overview.organisation.display_name}` : ""}</dd>
        {overview.units_in_use && <><dt>Groups of {terms().pluralLower("project")}</dt><dd>{terms().plural("unit")} ({overview.units?.length ?? 0})</dd></>}
        <dt>One repository</dt><dd>{lv.project.singular}</dd>
      </dl>
      <p className="cf-help">
        {overview.units_in_use ? null : <>Groups of {lv.project.plural.toLowerCase()} with shared settings are off: nobody has made one. </>}
        <Link href="/settings/words">Choose the words</Link>
        {overview.units_in_use ? null : <> or <Link href="/config/units/new">make the first group</Link></>}.
      </p>
    </section>
  );
}
