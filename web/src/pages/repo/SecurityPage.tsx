import { useCallback, useEffect, useState } from "react";
import { browseApi, type InspectionState } from "../../browseApi";
import { ErrorBox, Link, Mono } from "../../components/ui";
import { announce } from "../../kit/announce";
import { Chip } from "../../kit/Status";
import { Button, EmptyState, Sheet } from "../../kit/layout";
import { ago } from "../../format";
import { terms } from "../../terms";
import { FindingsBacklog } from "../Standards";
import { RepoFrame } from "./common";

/** What is wrong with a repository's code, and a button to look now: the Inspection (the same checks every run gets)
 * started by hand, and the findings of the project that watches it. */
export function SecurityPage({ path }: { path: string }) {
  const [st, setSt] = useState<InspectionState | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [started, setStarted] = useState<{ id: string; number: number } | null>(null);
  const load = useCallback(() => { browseApi.inspections(path).then((s) => { setSt(s); setError(null); }).catch(setError); }, [path]);
  useEffect(load, [load]);
  // While an Inspection runs, its state is read again.
  const running = st?.runs?.some((r) => r.status === "running" || r.status === "queued" || r.status === "pending");
  useEffect(() => { if (!running) return; const t = setInterval(load, 5000); return () => clearInterval(t); }, [running, load]);
  const go = async () => {
    setBusy(true); setError(null);
    try { const r = await browseApi.inspect(path); setStarted(r.run); announce(`Started the Inspection as run ${r.run.number}`); load(); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  return (
    <RepoFrame path={path} tab="security" title="Security"
      sentence={<>What the Inspection finds in this repository: secrets, known vulnerabilities in dependencies, unsafe code patterns, licences and the rest that fit what it holds. {terms().say("Every {run} gets it; here you can ask for it now.")}</>}>
      <ErrorBox error={error} />
      {!st && !error && <div className="loading">Loading…</div>}
      {st && (
        <Sheet title="Inspect now" pad>
          <p className="sub">Starts a run of only the Inspection at <Mono>{st.branch || "the default branch"}</Mono>, whatever the repository's pipeline says, or whether it has one. It reads the files and changes nothing.</p>
          <div className="actions">
            <Button kind="pri" disabled={busy || !st.available} onClick={go}>{busy ? "Starting…" : "Inspect now"}</Button>
            {st.project && <Link href={`/projects/${encodeURIComponent(st.project)}/standards`}>Which checks apply</Link>}
          </div>
          {!st.available && st.reason && <p className="callout callout-draft" role="status">{st.reason}</p>}
          {started && <p role="status">Started: <Link href={`/runs/${started.id}?tab=security`}>run {started.number}</Link>. Findings appear below when it ends.</p>}
          {(st.runs ?? []).length > 0 && (
            <ul className="plain" aria-label="Latest Inspections">
              {(st.runs ?? []).map((r) => (
                <li key={r.id}><Link href={`/runs/${r.id}?tab=security`}>Run {r.number}</Link> <Chip>{r.status}</Chip> <span className="faint small">at {r.sha.slice(0, 8)}, {ago(r.created_at)}</span></li>
              ))}
            </ul>
          )}
        </Sheet>
      )}
      {st && st.project && <FindingsBacklog name={st.project} />}
      {st && !st.project && (
        <Sheet><EmptyState title="No findings yet">Findings are kept under a project, and no project watches this repository yet. Add a project that reads it (in the server's configuration); then an Inspection, here or on any run, records what it finds and this page lists it.</EmptyState></Sheet>
      )}
    </RepoFrame>
  );
}
