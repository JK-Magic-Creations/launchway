import { useEffect, useState } from "react";
import { clock, duration } from "../format";
import { Link, Mono } from "./ui";
import { triggersApi, type ScheduleView, type SchedulesView } from "../triggers";
import "../triggers.css";

// ProjectTriggers shows what starts a project's runs besides pushes: the
// schedules of its default branch's pipeline, with when each fires next
// and how its last firing went, and its webhooks.
export function ProjectTriggers({ project }: { project: string }) {
  const [v, setV] = useState<SchedulesView | null>(null);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    let alive = true;
    const load = (bg: boolean) => triggersApi.schedules(project, bg).then((x) => { if (alive) { setV(x); setNow(Date.now()); } }).catch(() => {});
    load(false);
    const t = setInterval(() => load(true), 15000);
    return () => { alive = false; clearInterval(t); };
  }, [project]);
  if (!v || (v.schedules.length === 0 && v.webhooks.length === 0 && !v.git_hook && !v.problem)) return null;
  const errors = v.fires.filter((f) => f.outcome === "error").length;
  return (
    <details className="decisions" open={v.schedules.length > 0 && v.schedules.length <= 3}>
      <summary>
        Schedules ({v.schedules.length}){v.webhooks.length > 0 && ` and webhooks (${v.webhooks.length})`}
        {!v.enabled && v.schedules.length > 0 && <span className="sched-off"> · turned off on this server</span>}
        {errors > 0 && <span className="bad-ink"> · {errors} recent firing{errors > 1 ? "s" : ""} failed</span>}
      </summary>
      <p className="sub">Read from the pipeline on <Mono>{v.branch}</Mono>{v.sha ? <> at <Mono>{v.sha.slice(0, 8)}</Mono></> : null}. A schedule never fires for a time
        before it first appeared; if no server was running when it should have fired, it fires once, for the latest time missed.</p>
      {v.problem && <div className="callout callout-bad">{v.problem}</div>}
      {!v.enabled && v.schedules.length > 0 && (
        <div className="callout callout-draft">Schedules are turned off for this project in its settings (or the installation's): their firings are recorded as skipped and start nothing.</div>
      )}
      {v.schedules.length > 0 && (
        <table className="table sched-table">
          <thead><tr><th>When</th><th>Runs</th><th>Next</th><th>Last</th></tr></thead>
          <tbody>{v.schedules.map((s) => <ScheduleRow key={s.key} s={s} now={now} />)}</tbody>
        </table>
      )}
      {(v.webhooks.length > 0 || v.git_hook) && (
        <ul className="hooks">
          {v.git_hook && (
            <li>Git host webhook ({v.git_hook}): <Mono>{location.origin}/api/v1/projects/{project}/git-hook</Mono>. Pushes and pull requests are looked at at once instead of at the next poll.</li>
          )}
          {v.webhooks.map((w) => (
            <li key={w.name}>
              Webhook <Mono>{w.name}</Mono> runs <Mono>{w.ref}</Mono>
              {w.inputs && Object.keys(w.inputs).length > 0 && <> with {Object.entries(w.inputs).map(([k, p]) => `${k} ← ${p}`).join(", ")}</>}:
              {" "}{w.configured
                ? <><Mono>POST {location.origin}/api/v1/projects/{project}/webhooks/{w.name}</Mono> with {w.auth === "hmac" ? "an HMAC signature" : "a bearer token"}.</>
                : <span className="sched-off">not callable: the project's settings give it no token or secret yet.</span>}
            </li>
          ))}
        </ul>
      )}
    </details>
  );
}

function ScheduleRow({ s, now }: { s: ScheduleView; now: number }) {
  const inputs = s.inputs ? Object.entries(s.inputs) : [];
  return (
    <tr>
      <td><Mono>{s.cron}</Mono><div className="faint small">{s.timezone}</div></td>
      <td><Mono>{s.ref}</Mono>{inputs.length > 0 && <div className="faint small mono">{inputs.map(([k, v]) => `${k}=${v}`).join(" ")}</div>}</td>
      <td>{s.next ? <span title={new Date(s.next).toISOString()}>{clock(s.next)}<div className="faint small">in {duration(s.next - now)}</div></span> : "—"}</td>
      <td>
        {!s.last ? <span className="faint">not yet</span> : (
          <>
            <span className={s.last.outcome === "error" ? "bad-ink" : s.last.outcome === "skipped" ? "sched-off" : ""}>{s.last.outcome}</span>
            {" "}for {clock(s.last.fire_at)}
            {s.last.run_id ? <div className="small"><Link href={`/runs/${s.last.run_id}`}>{s.last.detail}</Link></div>
              : s.last.detail && <div className="small faint" style={{ whiteSpace: "pre-wrap" }}>{s.last.detail}</div>}
          </>
        )}
      </td>
    </tr>
  );
}
