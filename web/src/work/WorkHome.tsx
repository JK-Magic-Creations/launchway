import { useState } from "react";
import { forgeApi, type HostedRepo } from "../forgeApi";
import { Link } from "../components/ui";
import { Button, Sheet } from "../kit/layout";
import { announce } from "../kit/announce";
import { Blank, ErrorPanel, itemHref, Loading, Prio, scopeHref, StateTag, useLoad, when, WorkPage } from "./common";
import { workApi, type HomeWork, type Sample } from "./workApi";

// /work: what is assigned to you, what waits for you, and where to go next (every project's items, board and backlog).

export default function WorkHome() {
  const home = useLoad<HomeWork>("work-home", () => workApi.home());
  const repos = useLoad<HostedRepo[]>("work-repos", () => forgeApi.repositories());
  const h = home.data;
  return (
    <WorkPage title="Work" sentence="Your items, what agents did for you, and every project's board and backlog."
      actions={<><Link href="/work/inbox" className="btn">Inbox{h && h.unread > 0 ? ` (${h.unread} unread)` : ""}</Link><Link href="/work/agents" className="btn">Agents</Link></>}>
      <ErrorPanel error={home.error} onRetry={home.reload} />
      {home.loading && <Loading what="your work" />}
      {h && (
        <div className="wk-cols">
          <div className="wk-main">
            <Sheet title="Assigned to you" actions={h.assigned_total > 0 ? <span className="faint num">{h.assigned_total} open</span> : undefined} pad={h.assigned.length === 0}>
              {h.assigned.length === 0
                ? <Blank title="Nothing is assigned to you">When an item is assigned to you it appears here, most urgent first. Pick a project to the right to find work.</Blank>
                : <ul className="wk-rows tight">{h.assigned.map((it) => <li key={it.id}><span className="mono">{it.ref}</span> <Link href={itemHref(it.ref)}>{it.title}</Link> <StateTag item={it} /> <Prio p={it.priority} /></li>)}</ul>}
            </Sheet>
            <Sheet title="Agents and your review" pad>
              <h3 className="h-sm">Waiting for your review</h3>
              {h.agents.review.length === 0 ? <p className="faint">No agent is waiting for you.</p>
                : <ul className="wk-rows tight">{h.agents.review.map((it) => <li key={it.id}><span className="mono">{it.ref}</span> <Link href={itemHref(it.ref)}>{it.title}</Link></li>)}</ul>}
              <h3 className="h-sm">What agents did for you</h3>
              {h.agents.recent.length === 0 ? <p className="faint">No recent agent activity.</p>
                : <ul className="wk-rows tight">{h.agents.recent.slice(0, 8).map((a, i) => <li key={i}>{a.action.replace(/_/g, " ")}{a.item && <> <Link href={itemHref(a.item)}>{a.item}</Link></>} <span className="faint small">{when(a.at)}</span></li>)}</ul>}
            </Sheet>
            <Samples samples={h.samples} onDone={home.reload} />
          </div>
          <aside className="wk-side" aria-label="Projects">
            <Sheet title="Projects" pad={false}>
              <ErrorPanel error={repos.error} onRetry={repos.reload} />
              <ul className="wk-rows projects">
                <li><strong>Everything</strong> <Link href={scopeHref("installation")}>Items</Link> <Link href={scopeHref("installation", "board")}>Board</Link></li>
                {(repos.data ?? []).map((r) => (
                  <li key={r.id}><strong>{r.path}</strong>
                    <Link href={scopeHref("project:" + r.path)}>Items</Link> <Link href={scopeHref("project:" + r.path, "board")}>Board</Link> <Link href={scopeHref("project:" + r.path, "backlog")}>Backlog</Link>
                  </li>
                ))}
              </ul>
              {repos.data && repos.data.length === 0 && <p className="faint pad">No projects yet. Create a repository first, then track its work here.</p>}
            </Sheet>
          </aside>
        </div>
      )}
    </WorkPage>
  );
}

function Samples({ samples, onDone }: { samples: Sample[]; onDone: () => void }) {
  const [note, setNote] = useState<Record<string, string>>({});
  const [error, setError] = useState<unknown>(null);
  if (samples.length === 0) return null;
  const done = async (s: Sample) => {
    try { await workApi.completeSample(s.id, note[s.id] ?? ""); announce("Marked as reviewed."); onDone(); } catch (e) { setError(e); }
  };
  return (
    <Sheet title="Audit samples waiting for you" pad>
      <p className="sub">A share of the merges an agent approved is picked for a person to look at afterwards.</p>
      <ErrorPanel error={error} />
      <ul className="wk-rows">
        {samples.map((s) => (
          <li key={s.id}>
            <Link href={`/repositories/${s.repo}/pulls/${s.pull}`}>{s.repo} #{s.pull}</Link>{s.agent && <> by agent {s.agent}</>} <span className="faint small">picked {when(s.flagged_at)}</span>
            <span className="wk-inline">
              <input aria-label={`Note on ${s.repo} #${s.pull}`} value={note[s.id] ?? ""} onChange={(e) => setNote({ ...note, [s.id]: e.target.value })} placeholder="What you found" />
              <Button sm onClick={() => done(s)}>Mark reviewed</Button>
            </span>
          </li>
        ))}
      </ul>
    </Sheet>
  );
}
