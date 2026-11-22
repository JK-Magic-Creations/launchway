import { useCallback, useEffect, useRef, useState } from "react";
import { api, type ArtifactView, type RunArtifacts, type RunDetail } from "../api";
import { ago, clock } from "../format";
import { canOperate, useMe } from "../auth";
import { Empty, ErrorBox, Link } from "../components/ui";
import { ArtifactsHowTo, BoxIcon, CopyValue, artifactArchiveURL, artifactFileURL, bytes, CaretIcon } from "../components/Security";
import { terms } from "../terms";
import { prose } from "../prose";
import { DivActions, DivLoading, PSub, SpanFaint, SpanFaintSmall, SpanHidden } from "../kit/bits";
const P = await prose("pages.RunArtifacts");

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
  if (!data) return <DivLoading>Loading…</DivLoading>;
  if (!data.available) {
    return (
      <Empty title={terms().say(P.t0)}>
        <p>{data.problem ? data.problem.charAt(0).toUpperCase() + data.problem.slice(1).replace(/([^.])$/, "$1.") : terms().say(P.t1)}</p>
        <p>{terms().say(P.t2)} <Link href="/settings/runners">{terms().say("{Runners}")}</Link> {P.t3}</p>
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
            : <>{data.declared.join(", ")} {data.declared.length > 1 ? "declare" : "declares"} {terms().say(P.t4)}</>}
        </Empty>
      );
    }
    return (
      <Empty title={terms().say(P.t5)}>
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
        {" "}{P.t6}</p>
      {steps.map((step) => {
        const list = data.artifacts.filter((a) => a.step === step);
        const st = run.step_list.find((s) => s.id === step);
        return (
          <section key={step} className="artstep" aria-label={terms().say(`{Artifacts} of ${step}`)}>
            <h3 className="tgroup-head"><span>{step}</span><SpanFaint>{list.length ? terms().say(`${list.length} {artifact}${list.length > 1 ? "s" : ""}`) : ""}</SpanFaint></h3>
            {list.length === 0 ? (
              <div className="empty empty-sm">
                {st && !ENDED.includes(st.status)
                  ? <>Files are kept when {step} ends.</>
                  : <>{step} {terms().say(P.t7)}</>}
              </div>
            ) : (
              <ul className="tlist">
                {list.map((a) => <ArtifactRow key={a.id} a={a} run={run} onChange={reload} open={data.artifacts.length === 1} />)}
              </ul>
            )}
          </section>
        );
      })}
      {data.max_retain_ms > 0 && <p className="sub small">{terms().say(P.t8)} {Math.round(data.max_retain_ms / 86_400_000)} days.</p>}
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
          {a.reused && <span className="badge-reused" title={P.t9}>Reused from #{a.run_number}</span>}
          <ArtifactStatus a={a} />
        </span>
        <CaretIcon />
      </button>
      {open && (
        <div className="tcase-detail art-detail" id={detailId}>
          {a.note && <p className={a.status === "failed" ? "tfile-err" : "sub"}>{a.note}</p>}
          {a.status === "deleted" && <PSub>Deleted{a.ended_by ? ` by ${a.ended_by}` : ""}{a.ended_at ? `, ${ago(a.ended_at)}` : ""}{P.t10}</PSub>}
          {a.status === "expired" && <PSub>Its time ran out{a.ended_at ? ` ${ago(a.ended_at)}` : ""}{P.t11}</PSub>}
          {a.reused && <PSub>Kept by <Link href={`/runs/${a.run_id}?tab=artifacts`}>{terms().say("{run} #")}{a.run_number}</Link>{P.t12}</PSub>}
          <div className="table-scroll">
            <table className="inputs-table art-files">
              <thead><tr><th>File</th><th>Size</th><th>SHA-256</th>{kept && <th><SpanHidden>Download</SpanHidden></th>}</tr></thead>
              <tbody>
                {a.files.map((f) => (
                  <tr key={f.path}>
                    <td className="mono art-path">{f.path}{(f.mode & 0o111) !== 0 && <SpanFaintSmall title={P.t13}> (executable)</SpanFaintSmall>}</td>
                    <td className="art-size">{bytes(f.size)}</td>
                    <td><CopyValue value={f.sha256} label={`SHA-256 of ${f.path}`} /></td>
                    {kept && <td className="art-dl"><a href={artifactFileURL(a, f.path)} download aria-label={`Download ${f.path}`}>Download</a></td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {a.files_truncated && <p className="sub small">Only the first {a.files.length} of its {a.file_count} {P.t14}</p>}
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
            <PSub>
              Its {a.file_count} file{a.file_count === 1 ? "" : "s"} ({bytes(a.bytes)}{P.t15}{a.reused && <> It belongs to run #{a.run_number}{P.t16}</>}
              {" "}{terms().say(P.t17)}
            </PSub>
            <ErrorBox error={error} />
            <DivActions>
              <button type="button" className="btn" onClick={() => dialog.current?.close()}>Cancel</button>
              <button type="submit" className="btn btn-danger-outline" disabled={busy}>{busy ? "Deleting…" : "Delete"}</button>
            </DivActions>
          </form>
        </dialog>
      )}
    </>
  );
}
