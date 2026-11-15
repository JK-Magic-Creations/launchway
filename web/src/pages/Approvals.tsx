import type { Approvals } from "../live";
import { useNow } from "../live";
import { ago, clock, duration, refLabel } from "../format";
import { BranchIcon, CommitIcon, Empty, ErrorBox, Link, StatusIcon } from "../components/ui";

// ApprovalsPage lists every approval step waiting for someone to decide.
// Each row opens its run with that step selected.
export function ApprovalsPage({ approvals }: { approvals: Approvals }) {
  const { list, error, live } = approvals;
  const now = useNow(1000, !!list?.length);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Approvals</h1>
          <p className="sub">Runs waiting for someone to decide whether they go further, newest first. Updates live.</p>
        </div>
        <span className={"live " + (live ? "live-on" : "")}><span className="dot" />{live ? "Live" : "Connecting…"}</span>
      </header>
      <ErrorBox error={error} />
      {list === null && !error && <div className="loading">Loading…</div>}
      {list && list.length === 0 && (
        <Empty title="Nothing is waiting for approval">
          A run waits here when it reaches a step with <span className="mono">kind: approval</span>.
        </Empty>
      )}
      {list && list.length > 0 && (
        <ul className="runlist">
          {list.map((a) => (
            <li key={a.run_id + "/" + a.step_id}>
              <Link href={`/runs/${a.run_id}?step=${encodeURIComponent(a.step_id)}`} className="runrow approvalrow">
                <span className="runrow-status" title="Waiting for approval"><StatusIcon status="waiting" size={20} /></span>
                <span className="runrow-main">
                  <span className="runrow-title">
                    <span className="runrow-project">{a.project}</span>
                    <span className="runrow-num">#{a.run_number}</span>
                    <span className="mono faint">{a.step_id}</span>
                  </span>
                  <span className="runrow-msg">{a.message}</span>
                  <span className="runrow-meta">
                    {refLabel(a) && <span className="meta"><BranchIcon />{refLabel(a)}</span>}
                    {a.short_sha && <span className="meta mono"><CommitIcon />{a.short_sha}</span>}
                    {a.commit_subject && <span className="meta">{a.commit_subject}</span>}
                    {a.started_by && <span className="meta">started by {a.started_by}</span>}
                  </span>
                </span>
                <span className="runrow-time">
                  <span className="meta wait" title={`Expires ${clock(a.deadline)}`}>
                    {a.deadline > now ? `${duration(a.deadline - now)} left` : "expiring"}
                  </span>
                  <span className="faint" title={clock(a.waiting_since)}>asked {ago(a.waiting_since, now)}</span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
