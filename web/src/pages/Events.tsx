import { lazy, Suspense, useCallback, useState } from "react";
import { ErrorBox } from "../components/ui";
import { useLoad } from "../guided/useLoad";
import { clock } from "../format";
import { Button, EmptyState, PageHead, Sheet } from "../kit/layout";
import { announce } from "../kit/announce";
import { DECISIONS, SOURCES, eventsApi, type EventRecord } from "../eventsApi";
import { terms } from "../terms";

const EventSources = lazy(() => import("./EventSources"));

interface Filters { project: string; source: string; decision: string; verified: string }
const NONE: Filters = { project: "", source: "", decision: "", verified: "" };

/** The event log: what reached the server and, for each trigger, what was decided and why. */
export function EventsPage() {
  const [draft, setDraft] = useState<Filters>(NONE);
  const [filters, setFilters] = useState<Filters>(NONE);
  const list = useLoad(useCallback(() => eventsApi.list({ ...filters, limit: 100 }), [filters]));
  const [err, setErr] = useState<unknown>(null);
  const [done, setDone] = useState("");
  const set = (k: keyof Filters) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setDraft({ ...draft, [k]: e.target.value });

  const replay = (ev: EventRecord) => eventsApi.replay(ev.id).then((r) => {
    setErr(null);
    const msg = `Replayed: run #${r.run.number} of ${r.run.project} started.`;
    setDone(msg); announce(msg); list.reload();
  }).catch(setErr);

  return (
    <div className="page page-wide">
      <PageHead eyebrow="Activity" title="Events"
        sentence={terms().say("Everything the server received, and what it decided for each trigger, with the reason. Use it to see why a {run} started, or why it did not.")} />
      <form className="card auditfilters" onSubmit={(e) => { e.preventDefault(); setFilters(draft); }}>
        <label className="field"><span>{terms().say("{Project}")}</span><input value={draft.project} onChange={set("project")} /></label>
        <label className="field"><span>Source</span>
          <select value={draft.source} onChange={set("source")}><option value="">Any</option>{SOURCES.map((s) => <option key={s} value={s}>{s}</option>)}</select>
        </label>
        <label className="field"><span>Decision</span>
          <select value={draft.decision} onChange={set("decision")}><option value="">Any</option>{DECISIONS.map((s) => <option key={s} value={s}>{s}</option>)}</select>
        </label>
        <label className="field"><span>Sender verified</span>
          <select value={draft.verified} onChange={set("verified")}><option value="">Either</option><option value="yes">Verified</option><option value="no">Not verified</option></select>
        </label>
        <div className="auditfilters-actions">
          <button type="submit" className="btn btn-primary">Show</button>
          <button type="button" className="btn" onClick={() => { setDraft(NONE); setFilters(NONE); }}>Clear</button>
        </div>
      </form>
      <Suspense fallback={null}><EventSources /></Suspense>
      <ErrorBox error={err ?? list.error} />
      {done && <p role="status" className="faint">{done}</p>}
      {list.loading && !list.data && <div className="loading">Loading…</div>}
      {list.data && list.data.length === 0 && <EmptyState title="No events match">Pushes, pull requests, schedules, webhook calls and runs started by hand appear here as they arrive.</EmptyState>}
      {list.data && list.data.map((ev) => (
        <Sheet key={ev.id} title={<>{ev.summary}{!ev.verified && <> <span className="outcome outcome-denied">not verified</span></>}</>}
          actions={<>
            <span className="faint small">{ev.project} · {ev.source}{ev.source_name ? ` ${ev.source_name}` : ""} · {clock(ev.at)}</span>
            {ev.verified && ev.replay && <Button sm onClick={() => replay(ev)} aria-label={`Replay ${ev.summary}`}>Replay</Button>}
          </>}>
          <ul className="sh-b">
            {ev.decisions.map((d) => (
              <li key={d.id} data-decision={d.decision}>
                <b>{d.trigger}</b>: <span className="mono">{d.decision}</span>
                {d.run_id && <> (<a href={`/runs/${d.run_id}`}>{terms().say("{run} #")}{d.run_number}</a>)</>}: {d.reason}
              </li>
            ))}
            {ev.decisions.length === 0 && <li className="faint">Nothing was decided: no trigger judged this event.</li>}
          </ul>
          {ev.replay_of && <p className="faint small">A replay of <span className="mono">{ev.replay_of}</span>.</p>}
          {ev.payload != null && (
            <details><summary>The body as kept</summary><pre className="mono small">{JSON.stringify(ev.payload, null, 2)}</pre></details>
          )}
        </Sheet>
      ))}
    </div>
  );
}
