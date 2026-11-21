import type { ReactNode } from "react";
import type { Approval, Me, Project, RunSummary, Runner } from "../api";
import { useMe } from "../auth";
import { EntityLink, Linked } from "../components/EntityLink";
import { Link, StatusPill } from "../components/ui";
import { ago } from "../format";
import { attention, checklist, nextStep, type Attention, type CheckItem, type AppRow } from "../guided/derive";
import { useInstallation } from "../guided/facts";
import { useNow } from "../live";

/** Home answers one question: what should I do next? A new installation sees what to set up, in order;
 * a busy one sees what needs it today. Everything on it comes from the server's own state. */
export function HomePage() {
  const me = useMe();
  const inst = useInstallation();
  const now = useNow(30000);
  if (!me) return null;
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  return <Home me={me} projects={inst.projects} runs={inst.runs} runners={inst.runners} environments={inst.environments}
    applications={inst.applications} approvals={inst.approvals} now={now} />;
}

export interface HomeProps {
  me: Me;
  projects: Project[];
  runs: RunSummary[];
  runners: Runner[];
  environments: import("../api").EnvironmentView[];
  applications: AppRow[];
  approvals: Approval[];
  now: number;
}

export function Home({ me, projects, runs, runners, environments, applications, approvals, now }: HomeProps) {
  const items = me.admin ? checklist(me, { projects, runners, runs, environments }) : [];
  const next = nextStep(items);
  const needs = attention({ projects, approvals, runners, applications, me });
  const finished = items.length > 0 && next.item === null;
  const fresh = items.length > 0 && next.done === 0 && runs.length === 0;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Home</h1>
          <p className="sub">{fresh ? "Welcome. Launchway builds, tests and deploys your software. Let us get you started." : "What needs you today, and what to do next."}</p>
        </div>
        {me.admin && projects.length > 0 && <Link href="/config/wizard/connect-repository" className="btn btn-primary">Add a project</Link>}
      </header>

      {next.item && <NextStepCard item={next.item} done={next.done} total={next.total} />}
      {finished && <div className="callout callout-ok guided-allset" role="status"><strong>You are all set up.</strong> Projects, a runner, a first run and an environment are in place.</div>}

      <Attention list={needs} />

      {items.length > 0 && next.item && <Checklist items={items} done={next.done} />}
      {!me.admin && projects.length === 0 && (
        <div className="card"><h2 className="h-sm">Nothing to show yet</h2><p className="sub">No project has been added. An administrator adds the first one.</p></div>
      )}

      <LatestRuns runs={runs} now={now} />
    </div>
  );
}

export function NextStepCard({ item, done, total }: { item: CheckItem; done: number; total: number }) {
  return (
    <section className="card guided-next" aria-labelledby="next-h">
      <div className="guided-eyebrow">Your next step</div>
      <h2 id="next-h" className="guided-next-title">{item.title}</h2>
      <p className="guided-next-why">{item.why}</p>
      <div className="guided-next-actions">
        <Link href={item.href} className="btn btn-primary">{item.button}</Link>
        <span className="faint">{done} of {total} essentials done</span>
      </div>
      <div className="guided-bar" role="progressbar" aria-valuemin={0} aria-valuemax={total} aria-valuenow={done} aria-label="Setup progress"><i style={{ width: `${(done / Math.max(total, 1)) * 100}%` }} /></div>
    </section>
  );
}

/** The checklist shrinks: what is done folds into one line, and what is left is a list of links. */
export function Checklist({ items, done }: { items: CheckItem[]; done: number }) {
  const left = items.filter((i) => !i.done);
  const finished = items.filter((i) => i.done);
  return (
    <section className="card" aria-labelledby="check-h">
      <h2 id="check-h" className="h-sm">Setup checklist <span className="faint">({done} of {items.length} done)</span></h2>
      {finished.length > 0 && <p className="faint guided-doneline">Done: {finished.map((i) => i.title).join(", ")}.</p>}
      <ol className="guided-check">
        {left.map((i) => (
          <li key={i.id}>
            <span className="guided-dot" aria-hidden="true">{items.indexOf(i) + 1}</span>
            <span className="guided-check-text">
              <Link href={i.href}>{i.title}</Link>
              <span className="faint">{i.why}</span>
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}

export function Attention({ list }: { list: Attention[] }) {
  return (
    <section className="card" aria-labelledby="attn-h">
      <h2 id="attn-h" className="h-sm">Needs your attention</h2>
      {list.length === 0 ? <p className="faint">Nothing needs you.</p> : (
        <ul className="guided-attn">
          {list.map((a) => (
            <li key={a.key} className={`guided-attn-${a.tone}`}>
              <span className="guided-attn-text"><strong><Linked text={a.title} entities={a.entities} /></strong><span className="faint"><Linked text={a.detail} entities={a.detailEntities ?? []} /></span></span>
              <Link href={a.href} className="btn btn-sm">{a.button}</Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function LatestRuns({ runs, now }: { runs: RunSummary[]; now: number }): ReactNode {
  if (runs.length === 0) return null;
  return (
    <section className="card" aria-labelledby="latest-h">
      <div className="card-head">
        <h2 id="latest-h" className="h-sm">Latest runs</h2>
        <Link href="/activity">All activity</Link>
      </div>
      <ul className="guided-list">
        {runs.slice(0, 3).map((r) => (
          <li key={r.id}>
            <StatusPill status={r.status} />
            <EntityLink kind="project" name={r.project} />
            <EntityLink kind="run" name={`#${r.number}`} id={r.id} />
            <span className="guided-grow faint">{r.message || r.pipeline}</span>
            <span className="faint">{ago(r.created_at, now)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
