import { useCallback, useMemo, useState } from "react";
import { announce } from "../../kit/announce";
import { ago } from "../../format";
import { useLoad } from "../../guided/useLoad";
import { onLink } from "../../router";
import { organizeApi, type TrailEntry } from "../../organizeApi";
import { score } from "../../explorer/tree";
import type { PanelProps } from "./registry";
import { BtnSm, SpanFaintSmall } from "../../kit/bits";

const ACTION: Record<string, string> = { viewed: "Opened", started: "Started", approved: "Approved", commented: "Commented", edited: "Edited" };

/** Pick up where you left off: what you started or changed lately (with what changed since), then your recent history, searchable and
 * grouped by day. The trail is yours alone: kept on the server so it follows you, never shown to anyone else, and you can pause or clear it. */
export default function ContinuePanel({ title, now }: PanelProps) {
  const fn = useCallback(() => organizeApi.trail(150), []);
  const { data, error, reload } = useLoad(fn, 30000);
  const [q, setQ] = useState("");
  const [all, setAll] = useState(false);
  const days = useMemo(() => {
    const list = (data?.recent ?? []).filter((e) => !q.trim() || score(q.trim(), e.title) > 0 || score(q.trim(), e.kind) > 0);
    const by = new Map<string, TrailEntry[]>();
    for (const e of list) {
      const d = new Date(e.at);
      const t = new Date(now);
      const key = d.toDateString() === t.toDateString() ? "Today" : new Date(now - 86400000).toDateString() === d.toDateString() ? "Yesterday" : d.toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" });
      by.set(key, [...(by.get(key) ?? []), e]);
    }
    return [...by.entries()];
  }, [data, q, now]);
  if (error && !data) return <section className="pn" aria-label={title}><h2>{title}</h2><div className="pn-b pn-err" role="alert">Your history could not be loaded.</div></section>;
  if (!data) return <section className="pn" aria-label={title} aria-busy="true"><h2>{title}</h2><div className="pn-b"><div className="pn-skel" /></div></section>;
  const act = async (fn2: () => Promise<unknown>, said: string) => { await fn2(); announce(said); reload(); };
  return (
    <section className="pn" aria-label={title}>
      <h2>{title}</h2>
      <div className="pn-b">
        {data.paused && <p role="status" className="callout">Your history is paused: nothing you open is being kept.</p>}
        {(data.continue ?? []).length === 0 ? <p className="faint">Nothing to pick up yet. What you start or open appears here.</p> : (
          <ul className="pn-list" aria-label="Continue">
            {(data.continue ?? []).map((e) => (
              <li key={e.kind + e.obj}>
                <a href={e.href} onClick={onLink}><b>{e.title}</b></a>
                <SpanFaintSmall>{ACTION[e.action] ?? e.action} {ago(e.at, now)}</SpanFaintSmall>
                {e.changed && <span className="pn-changed small">{e.changed}</span>}
              </li>
            ))}
          </ul>
        )}
        <details className="pn-recent" open={all} onToggle={(ev) => setAll((ev.target as HTMLDetailsElement).open)}>
          <summary className="btn btn-sm">Recent history ({(data.recent ?? []).length})</summary>
          <p><input type="search" role="searchbox" aria-label="Search your history" placeholder="Search your history" value={q} onChange={(e) => setQ(e.target.value)} /></p>
          {days.length === 0 && <p className="faint">{q ? "Nothing in your history matches." : "Your history is empty."}</p>}
          {days.map(([day, list]) => (
            <div key={day}>
              <h3 className="pn-day">{day}</h3>
              <ul className="pn-list">{list.map((e) => <li key={e.kind + e.obj}><a href={e.href} onClick={onLink}>{e.title}</a><SpanFaintSmall>{e.kind} · {ACTION[e.action] ?? e.action} {ago(e.at, now)}</SpanFaintSmall></li>)}</ul>
            </div>
          ))}
        </details>
        <div className="pn-ctl">
          <BtnSm onClick={() => void act(() => organizeApi.pauseTrail(!data.paused), data.paused ? "History resumed." : "History paused.")}>{data.paused ? "Resume history" : "Pause history"}</BtnSm>
          <BtnSm onClick={() => void act(() => organizeApi.clearTrail(), "Your history was cleared.")}>Clear my history</BtnSm>
          <SpanFaintSmall>Only you can see this. Kept {data.retention_days} days.</SpanFaintSmall>
        </div>
      </div>
    </section>
  );
}
