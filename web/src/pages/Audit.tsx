import { PageHead } from "../kit/layout";
import { useCallback, useEffect, useState } from "react";
import { api, type AuditEntry, type AuditQuery } from "../api";
import { clock } from "../format";
import { useMe } from "../auth";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { terms } from "../terms";

const ACTIONS = [
  "auth.sign_in", "auth.sign_out", "run.start", "run.cancel", "run.resume",
  "approval.approve", "approval.reject", "token.create", "token.revoke",
];

interface Filters { user: string; project: string; action: string; from: string; to: string }
const EMPTY: Filters = { user: "", project: "", action: "", from: "", to: "" };

function toQuery(f: Filters): AuditQuery {
  // Dates are local days; "to" includes the whole day.
  const day = (s: string) => (s ? new Date(s + "T00:00:00").getTime() : undefined);
  const to = day(f.to);
  return { user: f.user.trim(), project: f.project.trim(), action: f.action, from: day(f.from), to: to && to + 86_400_000 };
}

// AuditPage shows who did what, for admins.
export function AuditPage() {
  const me = useMe();
  const [draft, setDraft] = useState<Filters>(EMPTY);
  const [filters, setFilters] = useState<Filters>(EMPTY);
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [next, setNext] = useState(0);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (before: number) => {
    setBusy(true);
    try {
      const page = await api.audit({ ...toQuery(filters), before, limit: 100 });
      setEntries((prev) => (before && prev ? prev.concat(page.entries) : page.entries));
      setNext(page.next_before);
      setError(null);
    } catch (e) { setError(e); } finally { setBusy(false); }
  }, [filters]);

  useEffect(() => { if (me?.admin) load(0); }, [load, me?.admin]);

  if (!me?.admin) {
    return <div className="page"><Empty title={`Only admins can see the ${terms().lower("logbook")}`} /></div>;
  }
  const set = (k: keyof Filters) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setDraft({ ...draft, [k]: e.target.value });

  return (
    <div className="page page-wide">
      <PageHead eyebrow="Settings" title={<>{terms().label("logbook")}</>} sentence={<>Every change made through the API, and every sign-in and sign-out, newest first. Entries are never changed or removed by Launchway.</>} />
      <form className="card auditfilters" onSubmit={(e) => { e.preventDefault(); setFilters(draft); }}>
        <label className="field"><span>User</span><input value={draft.user} onChange={set("user")} /></label>
        <label className="field"><span>Project</span><input value={draft.project} onChange={set("project")} /></label>
        <label className="field"><span>Action</span>
          <select value={draft.action} onChange={set("action")}>
            <option value="">Any</option>
            {ACTIONS.map((a) => <option key={a} value={a}>{a}</option>)}
          </select>
        </label>
        <label className="field"><span>From</span><input type="date" value={draft.from} onChange={set("from")} /></label>
        <label className="field"><span>To</span><input type="date" value={draft.to} onChange={set("to")} /></label>
        <div className="auditfilters-actions">
          <button type="submit" className="btn btn-primary">Show</button>
          <button type="button" className="btn" onClick={() => { setDraft(EMPTY); setFilters(EMPTY); }}>Clear</button>
        </div>
      </form>
      <ErrorBox error={error} />
      {entries === null && !error && <div className="loading">Loading…</div>}
      {entries && entries.length === 0 && <Empty title="Nothing matches" />}
      {entries && entries.length > 0 && (
        <div className="card card-flush tablewrap">
          <table className="table audit">
            <thead><tr><th>Time</th><th>Who</th><th>Action</th><th>On</th><th>Detail</th></tr></thead>
            <tbody>
              {entries.map((a) => (
                <tr key={a.id}>
                  <td className="nowrap">{clock(a.at)}</td>
                  <td>{a.user || <span className="faint">unknown</span>}<div className="faint small mono" title="Address">{a.source}</div></td>
                  <td className="audit-action"><Mono>{a.action}</Mono><div><span className={`outcome outcome-${a.outcome}`}>{a.outcome}</span></div></td>
                  <td className="audit-on">
                    {a.project && <div>{a.project}</div>}
                    {a.target.startsWith("run_") ? <Link href={`/runs/${a.target.split("/")[0]}`}><Mono>{a.target}</Mono></Link> : a.target}
                  </td>
                  <td className="audit-detail">{a.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {next > 0 && <button type="button" className="btn" disabled={busy} onClick={() => load(next)}>{busy ? "Loading…" : "Older entries"}</button>}
    </div>
  );
}
