import { useCallback, useEffect, useRef, useState } from "react";
import { api, type ArtifactView, type RunArtifacts, type RunDetail } from "../api";
import { ago, clock } from "../format";
import { canOperate, useMe } from "../auth";
import { Empty, ErrorBox, Link } from "../components/ui";
import { ArtifactsHowTo, BoxIcon, CopyValue, artifactArchiveURL, artifactFileURL, bytes, CaretIcon } from "../components/Security";
import { terms } from "../terms";

const ENDED = ["succeeded", "failed", "skipped", "canceled"];

// ArtifactsTab lists the files the run's steps kept, step by step, each
// artifact with its files to download.
export function ArtifactsTab({ run, version }: { run: RunDetail; version: string }) {
  const [data, setData] = useState<RunArtifacts | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [again, setAgain] = useState(0);
  useEffect(() => {
    let alive = true;
    api.runArtifacts(run.id, true).then((d) => { if (alive) { setData(d); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [run.id, version, again]);
  const reload = useCallback(() => setAgain((n) => n + 1), []);

  if (error) return <ErrorBox error={error} />;
  if (!data) return <div className="loading">Loading…</div>;
  if (!data.available) {
    return (
      <Empty title={terms().say("This server does not keep {artifacts}")}>
        <p>{data.problem ? data.problem.charAt(0).toUpperCase() + data.problem.slice(1).replace(/([^.])$/, "$1.") : terms().say("{Artifacts} are not set up on this server.")}</p>
        <p>{terms().say("With nothing configured, a server on PostgreSQL keeps {artifacts} in its database, shared by every server. The")} <Link href="/settings/runners">{terms().say("{Runners}")}</Link> page says where this one keeps them.</p>
      </Empty>
    );
  }
  if (data.artifacts.length === 0) {
    if (data.declared.length > 0) {
      const going = run.step_list.filter((s) => data.declared.includes(s.id) && !ENDED.includes(s.status));
      return (
        <Empty title={going.length ? terms().say("No {artifacts} yet") : terms().say("No {artifacts} kept")}>
          {going.length
            ? <>Files are kept when {going.map((s) => s.id).join(", ")} {going.length > 1 ? "end" : "ends"}.</>
            : <>{data.declared.join(", ")} {data.declared.length > 1 ? "declare" : "declares"} {terms().say("{artifacts}, but none were kept. The step's log says why, for example a step that did not succeed or a glob that matched no files.")}</>}
        </Empty>
      );
    }
    return (
      <Empty title={terms().say("This {run} kept no {artifacts}")}>
        <ArtifactsHowTo />
      </Empty>
    );
  }

  // Step by step, in the pipeline's order; steps that declare artifacts but kept none are listed too.
  const order = run.step_list.map((s) => s.id);
  const steps = [...new Set([...data.artifacts.map((a) => a.step), ...data.declared])]
    .sort((a, b) => (order.indexOf(a) + 1 || 1e9) - (order.indexOf(b) + 1 || 1e9));
  const kept = data.artifacts.filter((a) => a.status === "kept");
  const total = kept.reduce((n, a) => n + a.bytes, 0);

  return (
    <div className="arts">
      <p className="sub arts-intro">
        {kept.length} artifact{kept.length === 1 ? "" : "s"} kept, {bytes(total)} in all.
        {" "}Files are kept as the step wrote them: secrets are masked in logs, not in files.
      </p>
      {steps.map((step) => {
        const list = data.artifacts.filter((a) => a.step === step);
        const st = run.step_list.find((s) => s.id === step);
        return (
          <section key={step} className="artstep" aria-label={terms().say(`{Artifacts} of ${step}`)}>
            <h3 className="tgroup-head"><span>{step}</span><span className="faint">{list.length ? terms().say(`${list.length} {artifact}${list.length > 1 ? "s" : ""}`) : ""}</span></h3>
            {list.length === 0 ? (
              <div className="empty empty-sm">
                {st && !ENDED.includes(st.status)
                  ? <>Files are kept when {step} ends.</>
                  : <>{step} {terms().say("declares {artifacts} but kept none. The step's log says why.")}</>}
              </div>
            ) : (
              <ul className="tlist">
                {list.map((a) => <ArtifactRow key={a.id} a={a} run={run} onChange={reload} open={data.artifacts.length === 1} />)}
              </ul>
            )}
          </section>
        );
      })}
      {data.max_retain_ms > 0 && <p className="sub small">{terms().say("This server keeps {artifacts} for at most")} {Math.round(data.max_retain_ms / 86_400_000)} days.</p>}
    </div>
  );
}

function ArtifactRow({ a, run, onChange, open: openFirst }: { a: ArtifactView; run: RunDetail; onChange: () => void; open: boolean }) {
  const [open, setOpen] = useState(openFirst && a.status === "kept");
  const me = useMe();
  const sbom = a.kind === "sbom";
  const detailId = `art-${a.id}`;
  const kept = a.status === "kept";
  return (
    <li className={"tcase art art-" + a.status + (open ? " tcase-open" : "")}>
      <button type="button" className="tcase-row" aria-expanded={open} aria-controls={detailId} onClick={() => setOpen(!open)}>
        <span className="art-icon"><BoxIcon size={18} /></span>
        <span className="tcase-main">
          <span className="tcase-name">{sbom ? "SBOM" : a.name}{sbom && <span className="faint art-kind"> (software bill of materials)</span>}</span>
          <span className="tcase-sub">
            <span>{a.file_count} file{a.file_count === 1 ? "" : "s"}, {bytes(a.bytes)}</span>
            {kept && a.expires_at ? <span title={clock(a.expires_at)}>kept until {new Date(a.expires_at).toLocaleDateString()} ({until(a.expires_at)})</span> : null}
            {a.attempt > 1 && <span>attempt {a.attempt}</span>}
          </span>
        </span>
        <span className="tcase-badges">
          {a.reused && <span className="badge-reused" title="The re-run reused this step and its files">Reused from #{a.run_number}</span>}
          <ArtifactStatus a={a} />
        </span>
        <CaretIcon />
      </button>
      {open && (
        <div className="tcase-detail art-detail" id={detailId}>
          {a.note && <p className={a.status === "failed" ? "tfile-err" : "sub"}>{a.note}</p>}
          {a.status === "deleted" && <p className="sub">Deleted{a.ended_by ? ` by ${a.ended_by}` : ""}{a.ended_at ? `, ${ago(a.ended_at)}` : ""}. Its files are gone; the list shows what it held.</p>}
          {a.status === "expired" && <p className="sub">Its time ran out{a.ended_at ? ` ${ago(a.ended_at)}` : ""}, so its files were removed; the list shows what it held.</p>}
          {a.reused && <p className="sub">Kept by <Link href={`/runs/${a.run_id}?tab=artifacts`}>{terms().say("{run} #")}{a.run_number}</Link>, whose step this re-run reused.</p>}
          <div className="table-scroll">
            <table className="inputs-table art-files">
              <thead><tr><th>File</th><th>Size</th><th>SHA-256</th>{kept && <th><span className="visually-hidden">Download</span></th>}</tr></thead>
              <tbody>
                {a.files.map((f) => (
                  <tr key={f.path}>
                    <td className="mono art-path">{f.path}{(f.mode & 0o111) !== 0 && <span className="faint small" title="Marked as a file someone may run"> (executable)</span>}</td>
                    <td className="art-size">{bytes(f.size)}</td>
                    <td><CopyValue value={f.sha256} label={`SHA-256 of ${f.path}`} /></td>
                    {kept && <td className="art-dl"><a href={artifactFileURL(a, f.path)} download aria-label={`Download ${f.path}`}>Download</a></td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {a.files_truncated && <p className="sub small">Only the first {a.files.length} of its {a.file_count} files are listed; the .zip has them all.</p>}
          {kept && (
            <div className="art-actions">
              <a className="btn btn-sm" href={artifactArchiveURL(a)} download>Download all (.zip)</a>
              {canOperate(me, run.project) && <DeleteArtifact a={a} onDone={onChange} />}
            </div>
          )}
        </div>
      )}
    </li>
  );
}

function ArtifactStatus({ a }: { a: ArtifactView }) {
  switch (a.status) {
    case "kept": return <span className="artst artst-kept">Kept</span>;
    case "uploading": return <span className="artst artst-run">Uploading</span>;
    case "expired": return <span className="artst">Expired</span>;
    case "deleted": return <span className="artst" title={a.ended_by ? `Deleted by ${a.ended_by}` : undefined}>Deleted{a.ended_by ? ` by ${a.ended_by}` : ""}</span>;
    case "failed": return <span className="artst artst-bad" title={a.note}>Not kept</span>;
    default: return <span className="artst">{a.status}</span>;
  }
}

/** "in 13 days", "in 5 hours". */
function until(at: number): string {
  const ms = at - Date.now();
  if (ms <= 0) return "due now";
  const h = Math.round(ms / 3_600_000);
  if (h < 1) return "in less than an hour";
  if (h < 48) return `in ${h} hour${h === 1 ? "" : "s"}`;
  const d = Math.round(h / 24);
  return `in ${d} days`;
}

function DeleteArtifact({ a, onDone }: { a: ArtifactView; onDone: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { if (asking) dialog.current?.showModal(); }, [asking]);
  const name = a.kind === "sbom" ? "the SBOM" : a.name;
  const remove = async () => {
    setBusy(true); setError(null);
    try {
      // A reused step's artifact belongs to the earlier run.
      await api.deleteArtifact(a.run_id, a.id);
      dialog.current?.close();
      onDone();
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  return (
    <>
      <button type="button" className="btn btn-sm btn-danger-outline" onClick={() => { setError(null); setAsking(true); }}>Delete</button>
      {asking && (
        <dialog ref={dialog} className="dialog" onClose={() => setAsking(false)} aria-labelledby={`del-${a.id}`}>
          <form method="dialog" onSubmit={(e) => { e.preventDefault(); remove(); }}>
            <h2 id={`del-${a.id}`}>Delete {name}?</h2>
            <p className="sub">
              Its {a.file_count} file{a.file_count === 1 ? "" : "s"} ({bytes(a.bytes)}) are removed now and cannot be downloaded or fetched again.
              {a.reused && <> It belongs to run #{a.run_number}, so it goes from that run too.</>}
              {" "}{terms().say("The {run} keeps a note of who deleted it.")}
            </p>
            <ErrorBox error={error} />
            <div className="dialog-actions">
              <button type="button" className="btn" onClick={() => dialog.current?.close()}>Cancel</button>
              <button type="submit" className="btn btn-danger-outline" disabled={busy}>{busy ? "Deleting…" : "Delete"}</button>
            </div>
          </form>
        </dialog>
      )}
    </>
  );
}
