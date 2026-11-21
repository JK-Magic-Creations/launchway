import { useCallback, useEffect, useMemo, useState } from "react";
import { canOperate, useMe } from "../auth";
import { ago, clock } from "../format";
import { standardsApi, type Inspection, type InspectionFinding, type Profile, type Standing, type Tracked } from "../standardsApi";
import { SeverityBadge, safeURL } from "./Security";
import { CopyBlock } from "./Tests";
import { ErrorBox, Link } from "./ui";
import "../standards.css";
import { terms } from "../terms";

const STANDING_LABEL: Record<Standing, string> = {
  new: "New in this change", existing: "Already on the default branch", accepted: "Accepted risk", "false-positive": "False positive", unscored: "Not compared yet",
};

/** The finding as both the Inspection and the backlog show it. */
export interface Shown {
  fingerprint: string; check?: string; tool: string; rule: string; severity: InspectionFinding["severity"]; kind: string; title: string; file?: string; line?: number;
  package?: string; version?: string; fixed_in?: string; advisories?: string[]; reachable?: string; fix?: string; help_uri?: string;
  decision?: InspectionFinding["decision"]; standing?: Standing; since?: number; status?: string;
}

export function fromTracked(t: Tracked): Shown { return { ...t, line: t.line, standing: undefined, since: t.first_seen, status: t.status }; }
export function fromInspection(f: InspectionFinding): Shown { return { ...f, standing: f.standing }; }

function whereOf(f: Shown): string {
  if (!f.file) return "";
  return f.line ? `${f.file}:${f.line}` : f.file;
}

function inDays(n: number): string {
  const d = new Date(Date.now() + n * 86400000);
  return d.toISOString().slice(0, 10);
}

/** How long ago a Unix-millisecond time was, for a title. */
function sinceText(at: number): string { return `${ago(at)} (${clock(at)})`; }

/** One finding: what it is, where, why it matters, how to fix it, and the decision about it. The body opens on demand. */
export function FindingCard({ f, project, profile, onChanged }: { f: Shown; project: string; profile: Profile | null; onChanged: () => void }) {
  const me = useMe();
  const may = canOperate(me, project);
  const check = profile?.checks.find((c) => c.id === f.check);
  const help = safeURL(f.help_uri);
  const [reason, setReason] = useState("");
  const [expires, setExpires] = useState(inDays(30));
  const [falsePositive, setFalsePositive] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const accepted = f.decision?.active;

  const accept = async () => {
    setBusy(true); setError(null);
    try {
      await standardsApi.accept(project, f.fingerprint, falsePositive ? "false-positive" : "accepted-risk", reason, expires);
      setReason("");
      onChanged();
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const reopen = async () => {
    setBusy(true); setError(null);
    try { await standardsApi.reopen(project, f.fingerprint); onChanged(); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const id = "f-" + f.fingerprint.slice(0, 12);
  return (
    <li className={"sf sf-" + f.severity + (f.standing === "new" ? " sf-new" : "")}>
      <details>
        <summary className="sf-sum">
          <SeverityBadge s={f.severity} />
          <span className="sf-main">
            <span className="sf-title">{f.title}</span>
            <span className="sf-meta">
              <span className="sf-rule">{f.rule}</span>
              {f.package ? <span>{f.package}{f.version ? "@" + f.version : ""}</span> : whereOf(f) && <span>{whereOf(f)}</span>}
              {f.fixed_in && <span className="sf-fix">fix: {f.fixed_in}</span>}
              {f.reachable === "yes" && <span className="sf-reach">your code calls it</span>}
            </span>
          </span>
          {f.standing && <span className={"sf-tag sf-tag-" + f.standing}>{STANDING_LABEL[f.standing]}</span>}
          {!f.standing && f.status && f.status !== "open" && <span className={"sf-tag sf-tag-" + f.status}>{f.status === "accepted-risk" ? "Accepted risk" : f.status === "false-positive" ? "False positive" : f.status === "fixed" ? "Fixed" : f.status}</span>}
        </summary>
        <div className="sf-body">
          <dl className="sf-dl">
            <div><dt>What</dt><dd>{f.title}</dd></div>
            {(f.file || f.package) && <div><dt>Where</dt><dd>{f.package ? <>{f.package}{f.version ? " " + f.version : ""}{f.file ? <> in <span className="mono">{f.file}</span></> : null}</> : <span className="mono">{whereOf(f)}</span>}</dd></div>}
            <div><dt>Found by</dt><dd>{f.tool}{check ? <> for the check “{check.title}”</> : null}, rule <span className="mono">{f.rule}</span>{help && <> · <a href={help} target="_blank" rel="noreferrer noopener">the tool’s description</a></>}</dd></div>
            {check && <div><dt>Why it matters</dt><dd>{check.why}</dd></div>}
            {(f.fix || f.fixed_in) && (
              <div><dt>How to fix</dt><dd>{f.fix || `Update to ${f.fixed_in} or later.`}{f.package && f.fixed_in && (
                <CopyBlock label="Ask the dependency updater for the pull request" text={`launchway deps update ${f.package}`} />
              )}</dd></div>
            )}
            {f.advisories && f.advisories.length > 0 && <div><dt>Also known as</dt><dd>{f.advisories.join(", ")}</dd></div>}
            {f.since !== undefined && <div><dt>First seen</dt><dd>{sinceText(f.since)}</dd></div>}
            {f.decision && (
              <div><dt>Decision</dt><dd>
                {f.decision.status === "false-positive" ? "False positive" : "Accepted risk"} by {f.decision.by} until {clock(f.decision.expires_at).slice(0, 10)}: {f.decision.reason}
              </dd></div>
            )}
          </dl>
          <ErrorBox error={error} />
          {may && accepted && <button type="button" className="btn btn-sm" disabled={busy} onClick={reopen}>End this decision</button>}
          {may && !accepted && f.status !== "fixed" && (
            <form className="sf-accept" onSubmit={(e) => { e.preventDefault(); accept(); }} aria-label={`Accept the risk of ${f.rule}`}>
              <h4 className="h-xs">Accept the risk</h4>
              <p className="faint small">It stops counting on the date, is recorded with your name and your reason, and can be ended earlier.</p>
              <label htmlFor={id + "-r"}>Why is this safe to live with?</label>
              <textarea id={id + "-r"} value={reason} onChange={(e) => setReason(e.target.value)} rows={2} minLength={10} maxLength={500} required />
              <div className="sf-row">
                <label htmlFor={id + "-d"}>Until</label>
                <input id={id + "-d"} type="date" value={expires} min={inDays(1)} max={inDays(365)} onChange={(e) => setExpires(e.target.value)} required />
                <label className="sf-check"><input type="checkbox" checked={falsePositive} onChange={(e) => setFalsePositive(e.target.checked)} /> It is not a problem (a false positive)</label>
                <button type="submit" className="btn btn-sm" disabled={busy || reason.trim().length < 10}>Record the decision</button>
              </div>
            </form>
          )}
        </div>
      </details>
    </li>
  );
}

/** useProfile loads what the checks are, once. */
export function useProfile(): Profile | null {
  const [p, setP] = useState<Profile | null>(null);
  useEffect(() => { let on = true; standardsApi.profile().then((x) => on && setP(x)).catch(() => undefined); return () => { on = false; }; }, []);
  return p;
}

const FILTERS: { id: "" | Standing; label: string }[] = [
  { id: "new", label: "New" }, { id: "existing", label: "Existing" }, { id: "accepted", label: "Accepted" }, { id: "", label: "All" },
];

/** InspectionPanel opens a run's Security tab with the Inspection that Standing orders added: the verdict, the checks, and the findings by whether this change introduced them. It shows nothing for a run without one. */
export function InspectionPanel({ runId, project, version }: { runId: string; project: string; version: string }) {
  const [data, setData] = useState<Inspection | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [picked, setPicked] = useState<"" | Standing | null>(null);
  const [tick, setTick] = useState(0);
  const profile = useProfile();
  const load = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    let on = true;
    standardsApi.inspection(runId, { limit: 200 }, true).then((d) => { if (on) { setData(d); setError(null); } }).catch((e) => on && setError(e));
    return () => { on = false; };
  }, [runId, version, tick]);

  // Until someone chooses, a run that introduced something shows just that.
  const filter: "" | Standing = picked ?? (data && data.counts["new"] > 0 ? "new" : "");
  const shown = useMemo(() => (data?.findings ?? []).filter((f) => filter === "" || f.standing === filter || (filter === "accepted" && f.standing === "false-positive")), [data, filter]);

  if (error && !data) return <ErrorBox error={error} />;
  if (!data || !data.present) return null;
  const verdict = data.pending ? "waiting" : data.blocked ? "blocked" : "passed";
  const counts = (s: Standing) => data.counts[s] ?? 0;
  return (
    <section className={"card insp insp-" + verdict} aria-labelledby="insp-h">
      <div className="insp-head">
        <h2 id="insp-h" className="h-sm">Inspection</h2>
        <span className={"insp-verdict insp-v-" + verdict} role="status">
          {verdict === "blocked" ? "Blocked" : verdict === "passed" ? "Passed" : "Waiting for the checks"}
        </span>
      </div>
      <p className="insp-sum">{data.summary}</p>
      <p className="faint small insp-why">
        {terms().say("Added by Standing orders: this repository’s pipeline did not ask for these steps; the server adds them to every {run}")}{data.profile_version ? ` (profile ${data.profile_version})` : ""}.{" "}
        <Link href={`/projects/${encodeURIComponent(project)}/standards`}>Why each check runs, and how to change it</Link>.
      </p>
      {data.notices && data.notices.length > 0 && (
        <ul className="insp-notices">{data.notices.map((n) => <li key={n}>{n}</li>)}</ul>
      )}
      {data.checks.length > 0 && (
        <div className="table-scroll">
          <table className="table insp-checks">
            <caption className="visually-hidden">The checks of this Inspection</caption>
            <thead><tr><th scope="col">Check</th><th scope="col">What it does</th><th scope="col">Ran</th><th scope="col" className="num">New</th><th scope="col" className="num">Findings</th></tr></thead>
            <tbody>
              {data.checks.map((c) => (
                <tr key={c.id}>
                  <th scope="row">{c.title}</th>
                  <td>{c.state === "block" ? "Blocks a change that introduces a serious finding" : "Advises"}</td>
                  <td>{c.ran ? "yes" : <strong className="sec-bad">did not run</strong>}</td>
                  <td className="num">{c.new}</td>
                  <td className="num">{c.total}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {data.findings.length > 0 || data.matched > 0 ? (
        <>
          <div className="chips insp-filter" role="group" aria-label="Show findings that are">
            {FILTERS.map((f) => (
              <button key={f.label} type="button" className={"chip" + (filter === f.id ? " chip-on" : "")} aria-pressed={filter === f.id} onClick={() => setPicked(f.id)}>
                {f.label}{f.id === "" ? "" : ` (${f.id === "accepted" ? counts("accepted") + counts("false-positive") : counts(f.id)})`}
              </button>
            ))}
          </div>
          {shown.length === 0 ? <p className="faint">Nothing here.</p> : (
            <ul className="sf-list">
              {shown.map((f) => <FindingCard key={f.fingerprint + f.step} f={fromInspection(f)} project={project} profile={profile} onChanged={load} />)}
            </ul>
          )}
          {data.truncated && <p className="faint small">{terms().say("Not every finding was kept for this {run}; the counts include them all.")}</p>}
        </>
      ) : <p className="faint">The checks found nothing.</p>}
    </section>
  );
}
