import { OrganizeCallout } from "../components/OrganizeCallout";
import { useHostedRepos } from "./hostedRepos";
import { lazy, Suspense, useState, type ReactNode } from "react";
import type { Approval, Me, Project, RunSummary, Runner } from "../api";
import type { LaunchLineView } from "../api/launch";
import { useMe } from "../auth";
import { EntityLink, Linked } from "../components/EntityLink";
import { ErrorBoundary } from "../components/ErrorBoundary";
import { Link, StatusPill } from "../components/ui";
import { ago } from "../format";
import { checklist, nextStep, type CheckItem, type AppRow } from "../guided/derive";
import { useInstallation } from "../guided/facts";
import { headline, needsYou, type Need } from "../guided/needs";
import { pullNeeds, usePullInbox, type InboxItem } from "../guided/pullNeeds";
import { useImageCount, useLaunchLines } from "../guided/useLaunchLines";
import { Icon } from "../kit/Icon";
import { LaunchLine, toStations } from "../kit/LaunchLine";
import { LinkButton, PageHead, Sheet } from "../kit/layout";
import { Status, StateChip, type State } from "../kit/Status";
import { useNow } from "../live";
import { onLink } from "../router";
import { Explore } from "./HomeExplore";
import { AddButtons } from "./squadrons/shared";
import { terms } from "../terms";

// Loaded after the first paint: Home draws without it.
const HomeWork = lazy(() => import("../work/HomeWork"));
// What needs you, what runs, the projects by category: loaded after the first paint (one call, see HomeSections).
const HomeSections = lazy(() => import("./HomeSections"));

/** Home answers one question: what should I do next? A new installation sees what to set up, in order;
 * a busy one sees what needs it today, then the changes on their way. Everything on it comes from the server's own state. */
export function HomePage() {
  const me = useMe();
  const inst = useInstallation();
  const now = useNow(30000);
  const lines = useLaunchLines();
  const images = useImageCount(inst.projects);
  const pullInbox = usePullInbox();
  if (!me) return null;
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  return <Home me={me} projects={inst.projects} runs={inst.runs} runners={inst.runners} environments={inst.environments}
    applications={inst.applications} approvals={inst.approvals} now={now} lines={lines ?? []} images={images} pullInbox={pullInbox} />;
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
  /** The launch lines of the latest runs; empty until they are known. */
  lines?: LaunchLineView[];
  /** Different images found, or null while it is not known. */
  images?: number | null;
  /** Pull requests waiting on this person (reviews asked, changes asked, a queue that failed). */
  pullInbox?: InboxItem[];
}

export function Home({ me, projects, runs, runners, environments, applications, approvals, now, lines = [], images = null, pullInbox = [] }: HomeProps) {
  const items = me.admin ? checklist(me, { projects, runners, runs, environments }) : [];
  const next = nextStep(items);
  const legacy = [...needsYou({ projects, approvals, runners, applications, me }, lines, now), ...pullNeeds(pullInbox)];
  // Failed runs and decisions are the server's to say (a failure a newer run is retrying needs nobody); the rest stay here.
  const extra = legacy.filter((n) => !/^(run|approval):/.test(n.key));
  const [serverNeeds, setServerNeeds] = useState<number | null>(null);
  const needCount = serverNeeds === null ? legacy.length : serverNeeds + extra.length;
  const finished = items.length > 0 && next.item === null;
  const fresh = items.length > 0 && next.done === 0 && runs.length === 0;
  const onTheirWay = lines.filter((l) => l.tone !== "ok" && l.tone !== "idle").length;
  const h = headline(needCount, onTheirWay);
  return (
    <div className="page home">
      <PageHead eyebrow={me.user ? `Hello, ${me.user}` : undefined} title={fresh ? "Welcome" : h.title}
        sentence={fresh ? terms().say("Launchway builds, tests and {deploy:does} your software. Let us get you started.") : h.sentence}
        actions={projects.length > 0 ? <AddButtons flightFirst /> : undefined} />

      <ErrorBoundary name="The repositories without a pipeline"><HostedWithoutPipelines projects={projects.length} admin={!!me.admin} /></ErrorBoundary>
      <ErrorBoundary name="The organize suggestion"><OrganizeCallout /></ErrorBoundary>
      {next.item && <ErrorBoundary name="The next step" resetKey={next.item.href}><NextStepCard item={next.item} done={next.done} total={next.total} /></ErrorBoundary>}
      {finished && <div className="callout callout-ok guided-allset" role="status"><strong>You are all set up.</strong> {terms().say("{Projects}, a {runner}, a first {run} and an {environment} are in place.")}</div>}

      <ErrorBoundary name="The Tower cards"><Suspense fallback={<div aria-busy="true" style={{ minHeight: 420 }} />}><HomeSections onCount={setServerNeeds} extra={extra} /></Suspense></ErrorBoundary>
      <ErrorBoundary name="The work in progress"><Suspense fallback={null}><HomeWork /></Suspense></ErrorBoundary>

      <ErrorBoundary name="Changes on their way"><ChangesOnTheirWay lines={lines} runs={runs} now={now} /></ErrorBoundary>

      {items.length > 0 && next.item && <ErrorBoundary name="The checklist"><Checklist items={items} done={next.done} /></ErrorBoundary>}
      {!me.admin && projects.length === 0 && (
        <div className="card"><h2 className="h-sm">Nothing to show yet</h2><p className="sub">{terms().say("No {project} has been added. An administrator adds the first one.")}</p></div>
      )}

      <ErrorBoundary name="The Explore strip"><Explore projects={projects} applications={applications} environments={environments} approvals={approvals} images={images} /></ErrorBoundary>
    </div>
  );
}

/** Repositories Launchway hosts but that no project reads a pipeline from: said plainly, with the way to add one. */
export function HostedWithoutPipelines({ projects, admin }: { projects: number; admin: boolean }) {
  const hosted = useHostedRepos();
  if (projects > 0 || !hosted || hosted.length === 0) return null;
  const n = hosted.length;
  return (
    <section className="card" aria-labelledby="hosted-h">
      <h2 id="hosted-h" className="h-sm">{n} {n === 1 ? "repository" : "repositories"}, none has a pipeline yet</h2>
      <p className="sub">Launchway hosts {n === 1 ? "this repository" : "these repositories"}, and you can browse {n === 1 ? "it" : "them"} {terms().say("now. Add a {project} to read a pipeline from one and run it on each push.")}</p>
      <div className="guided-next-actions">
        {admin && <Link href="/projects/new" className="btn btn-primary">Add a pipeline</Link>}
        <Link href="/projects" className="btn">Browse the code</Link>
      </div>
    </section>
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
      <h2 id="check-h" className="h-sm">{terms().say("Setup {checklist}")} <span className="faint">({done} of {items.length} done)</span></h2>
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

/** The things that need a person: one plain sentence and one action each. Quiet when there are none. */
export function NeedsSheet({ list }: { list: Need[] }) {
  return (
    <section className="sheet" aria-label="Needs your attention">
      <div className="sh-h"><h2>Needs you</h2><div className="r">{list.length > 0 && <StateChip state="wait">{String(list.length)}</StateChip>}</div></div>
      {list.length === 0 ? <p className="sh-b faint">Nothing needs you.</p> : list.map((a) => (
        <div key={a.key} className={`need ${a.tone}`}>
          <div className={`ico-tile ${a.tone}`}><Icon name={a.icon} /></div>
          <div className="tx">
            <div className="ti"><Linked text={a.title} entities={a.entities} /></div>
            <div className="ds"><Linked text={a.detail} entities={a.detailEntities ?? a.entities} /></div>
          </div>
          <div className="acts"><LinkButton href={a.href} sm kind={a.tone === "warn" ? "pri" : undefined}>{a.button}</LinkButton></div>
        </div>
      ))}
    </section>
  );
}

const TONE: Record<string, State> = { bad: "bad", wait: "wait", run: "run", ok: "ok", idle: "skip" };

/** The changes on their way: each recent run as a launch line from the commit to the places it goes.
 * Before the lines are known, the latest runs are listed instead. */
export function ChangesOnTheirWay({ lines, runs, now }: { lines: LaunchLineView[]; runs: RunSummary[]; now: number }): ReactNode {
  if (lines.length === 0) return <LatestRuns runs={runs} now={now} />;
  return (
    <Sheet title="Changes on their way" actions={<span className="faint small">Each dot is a place a change has to pass</span>}>
      {lines.map((l) => (
        <a key={l.run} href={`/runs/${l.run}`} onClick={onLink} className={`brow ${l.tone === "bad" ? "bad" : l.tone === "wait" ? "wait" : ""}`}>
          <div className="ct"><b>{l.message || terms().say(`{Run} #${l.number}`)}</b><span>{l.project} · run #{l.number}{l.actor ? ` · ${l.actor}` : ""} · {ago(l.created_at, now)}</span></div>
          <LaunchLine stations={toStations(l.stations)} compact />
          <div className="now"><Status state={TONE[l.tone] ?? "skip"} /><span>{l.now}</span></div>
        </a>
      ))}
    </Sheet>
  );
}

export function LatestRuns({ runs, now }: { runs: RunSummary[]; now: number }): ReactNode {
  if (runs.length === 0) return null;
  return (
    <section className="card" aria-labelledby="latest-h">
      <div className="card-head">
        <h2 id="latest-h" className="h-sm">{terms().say("Latest {runs}")}</h2>
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
