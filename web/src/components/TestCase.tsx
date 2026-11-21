import { useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, attachmentURL, type HTTPExchange, type RunDetail, type TestAttachment, type TestCase, type TestHeader, type TestHistory, type TestStep } from "../api";
import { ErrorBox, Link } from "./ui";
import { bytes } from "./Security";
import { Badge, CopyBlock, HistoryStrip, Sparkline, TagPills, TestIcon, VariantPill, testDuration, testStatusLabel } from "./Tests";
import { terms } from "../terms";

// The whole of one test result: what it is, why it failed, the steps it
// took (with the HTTP calls an API test made), what it attached, its
// earlier tries and its history on the run's branch.

/** At most this many steps are drawn at first; the rest on request. */
const STEPS_SHOWN = 250;
/** Text longer than this is cut, never parsed or drawn whole. */
const BODY_MAX = 100_000;
/** The first bytes of a text attachment that are fetched and shown. */
const TEXT_MAX = 64 * 1024;

export function CaseDetail({ c, run, full, error, permalink }: {
  c: TestCase; run: RunDetail; full: TestCase | null; error: unknown; permalink?: boolean;
}) {
  const x = full ?? c;
  const bad = x.status === "failed" || x.status === "errored";
  const attachments = x.attachments ?? [];
  // Attachments a step points at are shown with that step.
  const inSteps = useMemo(() => new Set((x.steps ?? []).flatMap((s) => s.attachments ?? [])), [x.steps]);
  const loose = attachments.map((a, i) => [a, i] as const).filter(([, i]) => !inSteps.has(i));
  const loading = c.has_details && !full && !error;
  const caseURL = `/runs/${run.id}?tab=tests&case=${c.id}`;
  return (
    <div className="tcd">
      <dl className="tcase-facts">
        <dt>Status</dt>
        <dd className="tcd-status">
          <span className={"tstatus tstatus-" + x.status}><TestIcon status={x.status} size={14} />{testStatusLabel(x.status)}</span>
          {x.flaky && <Badge kind="flaky" title="Failed, then passed when run again">Flaky</Badge>}
          {x.new_failure && <Badge kind="new" title="Passed in the previous run of the same branch">New failure</Badge>}
          <span className="faint">{testDuration(x.duration_ms)}</span>
        </dd>
        {x.suite && <><dt>Suite</dt><dd className="mono">{x.suite}</dd></>}
        {x.file && <><dt>File</dt><dd className="mono">{x.file}</dd></>}
        {x.variant && <><dt>Ran on</dt><dd><VariantPill variant={x.variant} /></dd></>}
        {x.tags && x.tags.length > 0 && <><dt>Tags</dt><dd><TagPills tags={x.tags} max={20} /></dd></>}
        <dt>Step</dt><dd>{x.step}{x.attempt > 1 ? `, attempt ${x.attempt}` : ""}{x.run_id !== run.id ? " (reused from an earlier run)" : ""}</dd>
        {x.report_file && <><dt>Report</dt><dd className="mono">{x.report_file}</dd></>}
        {(x.retries ?? 0) > 0 && <><dt>Retries</dt><dd>{terms().say("{Run}")} {(x.retries ?? 0) + 1} times by the test tool{x.status === "passed" ? ", passed on the last" : ""}</dd></>}
      </dl>

      {x.properties && x.properties.length > 0 && <Properties props={x.properties} />}

      {x.message && <CopyBlock label={x.status === "skipped" ? "Reason" : "Message"} text={x.message} tone={bad ? "bad" : undefined} />}
      <ErrorBox error={error} />
      {loading && <div className="loading">Loading…</div>}
      {full?.details && full.details.trim() !== (full.message ?? "").trim() && <CopyBlock label="Stack trace" text={full.details} />}

      {full?.attempts && full.attempts.length > 0 && <Attempts attempts={full.attempts} final={x} />}

      {full?.steps && full.steps.length > 0 && (
        <Steps steps={full.steps} cut={!!full.steps_cut} attachments={attachments} run={run} caseId={c.id} />
      )}

      {full && loose.length > 0 && (
        <section className="tcd-section" aria-label="Attachments">
          <h4 className="tcd-h">Attachments <span className="faint">{loose.length}</span></h4>
          <Attachments items={loose} run={run} caseId={c.id} />
        </section>
      )}

      {full?.output && <CopyBlock label="Captured output" text={full.output} />}
      {!x.message && !c.has_details && <p className="sub">Nothing more was recorded for this test.</p>}

      <CaseHistory run={run} c={x} />

      <div className="tcd-links">
        {!permalink && <Link href={caseURL} className="tcase-history">Open this result on its own</Link>}
        <Link href={`/projects/${encodeURIComponent(run.project)}/tests?test=${c.key}${run.ref ? `&ref=${encodeURIComponent(run.ref)}` : ""}`} className="tcase-history">
          Full history of this test
        </Link>
      </div>
    </div>
  );
}

function Properties({ props }: { props: { name: string; value: string }[] }) {
  return (
    <details className="tcd-props" open={props.length <= 6}>
      <summary>Properties <span className="faint">{props.length}</span></summary>
      <dl className="tcase-facts tcd-props-list">
        {props.map((p, i) => <PropRow key={p.name + i} name={p.name} value={p.value} />)}
      </dl>
    </details>
  );
}

function PropRow({ name, value }: { name: string; value: string }) {
  return <><dt className="mono">{name}</dt><dd className="mono">{value.length > 600 ? value.slice(0, 600) + "…" : value}</dd></>;
}

// ---- earlier tries ----

function Attempts({ attempts, final }: { attempts: { status: TestCase["status"]; duration_ms: number; message?: string }[]; final: TestCase }) {
  const all = [...attempts, { status: final.status, duration_ms: final.duration_ms, message: undefined, last: true }];
  return (
    <section className="tcd-section" aria-label="Tries">
      <h4 className="tcd-h">Tries <span className="faint">{all.length}</span></h4>
      <ol className="tcd-tries">
        {all.map((a, i) => (
          <li key={i} className={"tcd-try tcd-try-" + a.status}>
            <div className="tcd-try-head">
              <TestIcon status={a.status} size={14} />
              <strong>Try {i + 1}</strong>
              <span>{testStatusLabel(a.status)}{"last" in a ? " (the result)" : ""}</span>
              <span className="faint">{testDuration(a.duration_ms)}</span>
            </div>
            {a.message && (
              <details className="tcd-more">
                <summary>{firstLine(a.message)}</summary>
                <pre className="tcd-pre tcd-pre-bad">{a.message}</pre>
              </details>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}

// ---- steps ----

function Steps({ steps, cut, attachments, run, caseId }: {
  steps: TestStep[]; cut: boolean; attachments: TestAttachment[]; run: RunDetail; caseId: number;
}) {
  const [all, setAll] = useState(false);
  const shown = all ? steps : steps.slice(0, STEPS_SHOWN);
  const http = steps.filter((s) => s.http).length;
  const failedAt = steps.findIndex((s) => s.status === "failed" || s.status === "errored");
  // The call just before the failing step is the one to look at first.
  let openHTTP = -1;
  if (failedAt >= 0) for (let i = failedAt; i >= 0; i--) if (steps[i].http) { openHTTP = i; break; }
  return (
    <section className="tcd-section" aria-label="Steps">
      <h4 className="tcd-h">Steps <span className="faint">{steps.length}{http ? `, ${http} HTTP call${http > 1 ? "s" : ""}` : ""}</span></h4>
      <ol className="tsteps">
        {shown.map((s, i) => (
          <li key={i} className={"tstep tstep-" + s.status} style={{ ["--depth" as string]: Math.min(s.depth ?? 0, 8) }}>
            <div className="tstep-row">
              <TestIcon status={s.status} size={14} />
              <span className="tstep-text">
                {s.keyword && <strong className="tstep-kw">{s.keyword}</strong>}{" "}
                <span className="tstep-name">{s.name}</span>
                <span className="visually-hidden">, {testStatusLabel(s.status)}</span>
              </span>
              {s.duration_ms !== undefined && s.duration_ms > 0 && <span className="tstep-time">{testDuration(s.duration_ms)}</span>}
            </div>
            {(s.error || s.text || s.http || (s.attachments?.length ?? 0) > 0) && (
              <div className="tstep-body">
                {s.error && <pre className="tcd-pre tcd-pre-bad">{s.error}</pre>}
                {s.http && <Exchange x={s.http} open={i === openHTTP || http <= 2} />}
                {s.text && (
                  <details className="tcd-more">
                    <summary>{s.text.includes("\n") ? `Text, ${s.text.split("\n").length} lines` : "Text"}</summary>
                    <pre className="tcd-pre">{cap(s.text)}</pre>
                  </details>
                )}
                {s.attachments && s.attachments.length > 0 && (
                  <Attachments compact run={run} caseId={caseId}
                    items={s.attachments.filter((n) => attachments[n]).map((n) => [attachments[n], n] as const)} />
                )}
              </div>
            )}
          </li>
        ))}
      </ol>
      {steps.length > STEPS_SHOWN && (
        <button type="button" className="linkbtn pstat-more" onClick={() => setAll(!all)}>
          {all ? "Show fewer steps" : `Show all ${steps.length} steps`}
        </button>
      )}
      {cut && <p className="sub small">The report had more steps than a result keeps; the rest were left out.</p>}
    </section>
  );
}

// ---- HTTP ----

function statusTone(code?: number): string {
  if (!code) return "idle";
  if (code < 300) return "ok";
  if (code < 400) return "run";
  if (code < 500) return "warn";
  return "bad";
}

const REASON: Record<number, string> = {
  200: "OK", 201: "Created", 202: "Accepted", 204: "No Content", 301: "Moved", 302: "Found", 304: "Not Modified",
  400: "Bad Request", 401: "Unauthorized", 403: "Forbidden", 404: "Not Found", 405: "Method Not Allowed", 409: "Conflict",
  415: "Unsupported Media Type", 422: "Unprocessable", 429: "Too Many Requests", 500: "Server Error", 502: "Bad Gateway",
  503: "Unavailable", 504: "Gateway Timeout",
};

export function Exchange({ x, open }: { x: HTTPExchange; open?: boolean }) {
  const tone = statusTone(x.status);
  return (
    <details className="http" open={open}>
      <summary className="http-line">
        <span className={"http-method http-method-" + x.method.toLowerCase()}>{x.method}</span>
        <span className="http-url mono">{x.url}</span>
        {x.status ? (
          <span className={"pill pill-" + tone + " http-status"}>
            <span aria-hidden="true">{tone === "ok" ? "✓" : tone === "bad" || tone === "warn" ? "!" : "→"}</span>
            {x.status}{REASON[x.status] ? ` ${REASON[x.status]}` : ""}
          </span>
        ) : <span className="pill pill-idle http-status">no answer</span>}
        {x.duration_ms !== undefined && x.duration_ms > 0 && <span className="http-time">{testDuration(x.duration_ms)}</span>}
      </summary>
      <div className="http-body">
        <HTTPPart title="Request" headers={x.request_headers} body={x.request_body} />
        <HTTPPart title="Response" headers={x.response_headers} body={x.response_body} />
      </div>
    </details>
  );
}

function HTTPPart({ title, headers, body }: { title: string; headers?: TestHeader[]; body?: string }) {
  const type = headers?.find((h) => h.name.toLowerCase() === "content-type")?.value ?? "";
  return (
    <div className="http-part">
      <h5 className="http-h">{title}</h5>
      {headers && headers.length > 0 ? (
        <details className="http-headers">
          <summary>Headers <span className="faint">{headers.length}</span></summary>
          <div className="table-scroll">
            <table className="inputs-table http-table">
              <tbody>
                {headers.map((h, i) => <tr key={h.name + i}><th scope="row" className="mono">{h.name}</th><td className="mono">{h.value}</td></tr>)}
              </tbody>
            </table>
          </div>
        </details>
      ) : <p className="faint small http-none">No headers recorded.</p>}
      {body ? <Body text={body} type={type} label={`${title} body`} /> : <p className="faint small http-none">No body.</p>}
    </div>
  );
}

/** A request or response body: JSON is pretty-printed, everything is shown as text, never as markup. */
function Body({ text, type, label }: { text: string; type: string; label: string }) {
  const shown = useMemo(() => prettyBody(text, type), [text, type]);
  return (
    <div className="http-bodytext">
      <CopyBlock label={shown.json ? `${label} (JSON)` : label} text={shown.text} />
      {shown.cut && <p className="faint small">Showing the first {bytes(BODY_MAX)} of {bytes(text.length)}.</p>}
    </div>
  );
}

function prettyBody(text: string, type: string): { text: string; json: boolean; cut: boolean } {
  if (text.length > BODY_MAX) return { text: text.slice(0, BODY_MAX), json: false, cut: true };
  const t = text.trim();
  if (/json/i.test(type) || t.startsWith("{") || t.startsWith("[")) {
    try { return { text: JSON.stringify(JSON.parse(t), null, 2), json: true, cut: false }; } catch { /* not JSON after all */ }
  }
  return { text, json: false, cut: false };
}

// ---- attachments ----

type Item = readonly [TestAttachment, number];

function Attachments({ items, run, caseId, compact }: { items: Item[]; run: RunDetail; caseId: number; compact?: boolean }) {
  const [box, setBox] = useState<number | null>(null);
  const images = items.filter(([a]) => a.kind === "image" && !a.problem);
  const order: Record<string, number> = { image: 0, video: 1, trace: 2, text: 3, html: 4, file: 5 };
  const sorted = [...items].sort(([a], [b]) => (a.problem ? 9 : order[a.kind] ?? 6) - (b.problem ? 9 : order[b.kind] ?? 6));
  return (
    <>
      <ul className={"atts" + (compact ? " atts-compact" : "")}>
        {sorted.map(([a, n]) => (
          <li key={n} className={"att att-" + (a.problem ? "problem" : a.kind)}>
            <Attachment a={a} n={n} run={run} caseId={caseId} onOpen={() => setBox(images.findIndex(([, m]) => m === n))} />
          </li>
        ))}
      </ul>
      {box !== null && box >= 0 && (
        <Lightbox images={images} at={box} run={run} caseId={caseId} onMove={setBox} onClose={() => setBox(null)} />
      )}
    </>
  );
}

function Attachment({ a, n, run, caseId, onOpen }: { a: TestAttachment; n: number; run: RunDetail; caseId: number; onOpen: () => void }) {
  const url = attachmentURL(run.id, caseId, n);
  const dl = attachmentURL(run.id, caseId, n, true);
  const size = a.size ? bytes(a.size) : "";
  const file = a.path ? a.path.split("/").pop() ?? a.name : a.name;
  if (a.problem) {
    return (
      <div className="att-card att-card-off">
        <AttIcon kind={a.kind} />
        <div className="att-meta">
          <span className="att-name">{a.name}</span>
          <span className="att-problem">{a.problem}</span>
        </div>
      </div>
    );
  }
  switch (a.kind) {
    case "image":
      return (
        <figure className="att-figure">
          <button type="button" className="att-thumb" onClick={onOpen} aria-label={`Open ${a.name} larger`}>
            <img src={url} alt={a.name} loading="lazy" />
          </button>
          <figcaption><span className="att-name">{a.name}</span> <span className="faint">{size}</span> <a href={dl} className="att-dl">Download</a></figcaption>
        </figure>
      );
    case "video":
      return (
        <figure className="att-figure att-video">
          <video controls preload="metadata" src={url} aria-label={a.name}>
            <a href={dl}>Download {a.name}</a>
          </video>
          <figcaption><span className="att-name">{a.name}</span> <span className="faint">{size}</span> <a href={dl} className="att-dl">Download</a></figcaption>
        </figure>
      );
    case "text":
      return <TextAttachment a={a} url={url} dl={dl} />;
    case "trace":
      return (
        <div className="att-card">
          <AttIcon kind="trace" />
          <div className="att-meta">
            <span className="att-name">{a.name} <span className="faint">{size}</span></span>
            <span className="faint small">Open with <code className="mono">npx playwright show-trace {file}</code></span>
          </div>
          <a href={dl} className="btn btn-sm" download>Download trace</a>
        </div>
      );
    default:
      return (
        <div className="att-card">
          <AttIcon kind={a.kind} />
          <div className="att-meta">
            <span className="att-name">{a.name} <span className="faint">{size}</span></span>
            <span className="faint small">{a.kind === "html" ? "An HTML file: downloaded, never shown here." : a.content_type || "A file"}</span>
          </div>
          <a href={dl} className="btn btn-sm" download>Download</a>
        </div>
      );
  }
}

function TextAttachment({ a, url, dl }: { a: TestAttachment; url: string; dl: string }) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  useEffect(() => {
    if (!open || text !== null) return;
    let alive = true;
    fetch(url, { headers: { Range: `bytes=0-${TEXT_MAX - 1}` }, credentials: "same-origin" })
      .then(async (r) => {
        if (!r.ok && r.status !== 206) throw new ApiError(r.status, (await r.text()) || r.statusText);
        const buf = await r.arrayBuffer();
        return new TextDecoder("utf-8", { fatal: false }).decode(buf.slice(0, TEXT_MAX));
      })
      .then((t) => alive && setText(t))
      .catch((e) => alive && setErr(e));
    return () => { alive = false; };
  }, [open, text, url]);
  const cut = text !== null && (a.size ?? 0) > TEXT_MAX;
  return (
    <details className="att-text" onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary>
        <AttIcon kind="text" />
        <span className="att-name">{a.name}</span>
        <span className="faint">{a.size ? bytes(a.size) : ""}{a.content_type ? ` · ${a.content_type}` : ""}</span>
        <a href={dl} className="att-dl" onClick={(e) => e.stopPropagation()}>Download</a>
      </summary>
      {err ? <ErrorBox error={err} /> : text === null ? <div className="loading">Loading…</div> : (
        <>
          <pre className="tcd-pre">{text}</pre>
          {cut && <p className="faint small">Showing the first {bytes(TEXT_MAX)} of {bytes(a.size ?? 0)}; download it for the rest.</p>}
        </>
      )}
    </details>
  );
}

function AttIcon({ kind }: { kind: string }) {
  const p = { width: 18, height: 18, viewBox: "0 0 16 16", "aria-hidden": true, className: "ic-line att-icon" } as const;
  switch (kind) {
    case "trace":
      return <svg {...p}><path d="M1.5 9h3l1.5-4 2.5 7 1.5-3h4.5" strokeLinejoin="round" /></svg>;
    case "text":
      return <svg {...p}><path d="M4 1.5h5.5L12.5 4.5v10h-8.5z" strokeLinejoin="round" /><path d="M6 7.5h4.5M6 9.7h4.5M6 11.9h3" /></svg>;
    case "video":
      return <svg {...p}><rect x="1.5" y="3.5" width="9.5" height="9" rx="1.5" /><path d="M11 7l3.5-2v6L11 9" strokeLinejoin="round" /></svg>;
    case "image":
      return <svg {...p}><rect x="1.5" y="2.5" width="13" height="11" rx="1.5" /><circle cx="5.5" cy="6" r="1.2" /><path d="M2 12l4-4 3 3 2-2 3.5 3.5" strokeLinejoin="round" /></svg>;
    default:
      return <svg {...p}><path d="M4 1.5h5.5L12.5 4.5v10h-8.5z" strokeLinejoin="round" /><path d="M9.5 1.5v3h3" /></svg>;
  }
}

/** Images shown large, one at a time: a modal dialog, so focus stays in it; Escape closes it and focus returns. */
function Lightbox({ images, at, run, caseId, onMove, onClose }: {
  images: Item[]; at: number; run: RunDetail; caseId: number; onMove: (i: number) => void; onClose: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const back = useRef<Element | null>(null);
  useEffect(() => {
    back.current = document.activeElement;
    const d = ref.current;
    d?.showModal();
    return () => {
      d?.close();
      if (back.current instanceof HTMLElement) back.current.focus();
    };
  }, []);
  const [a, n] = images[at];
  const many = images.length > 1;
  const key = (e: React.KeyboardEvent) => {
    if (!many) return;
    if (e.key === "ArrowRight") { e.preventDefault(); onMove((at + 1) % images.length); }
    if (e.key === "ArrowLeft") { e.preventDefault(); onMove((at - 1 + images.length) % images.length); }
  };
  return (
    <dialog ref={ref} className="lightbox" aria-label={`${a.name}, image ${at + 1} of ${images.length}`}
      onCancel={(e) => { e.preventDefault(); onClose(); }} onKeyDown={key}
      onClick={(e) => { if (e.target === ref.current) onClose(); }}>
      <div className="lightbox-bar">
        <span className="lightbox-title">{a.name}{many && <span className="faint"> · {at + 1} of {images.length}</span>}</span>
        <span className="lightbox-actions">
          {many && <button type="button" className="btn btn-sm" onClick={() => onMove((at - 1 + images.length) % images.length)} aria-label="Previous image">←</button>}
          {many && <button type="button" className="btn btn-sm" onClick={() => onMove((at + 1) % images.length)} aria-label="Next image">→</button>}
          <a className="btn btn-sm" href={attachmentURL(run.id, caseId, n, true)}>Download</a>
          <button type="button" className="btn btn-sm" onClick={onClose} autoFocus>Close</button>
        </span>
      </div>
      <div className="lightbox-img">
        <img src={attachmentURL(run.id, caseId, n)} alt={a.name} />
      </div>
    </dialog>
  );
}

// ---- history ----

function CaseHistory({ run, c }: { run: RunDetail; c: TestCase }) {
  const [h, setH] = useState<TestHistory | null>(null);
  const [none, setNone] = useState(false);
  useEffect(() => {
    let alive = true;
    api.testHistory(run.project, c.key, run.ref || undefined)
      .then((x) => alive && setH(x))
      .catch((e) => { if (alive && e instanceof ApiError && e.status === 404) setNone(true); });
    return () => { alive = false; };
  }, [run.project, run.ref, c.key]);
  if (none || !h || h.results.length === 0) return null;
  const list = [...h.results].reverse();
  const durations = list.filter((r) => r.status !== "skipped").map((r) => r.duration_ms);
  const fails = h.results.filter((r) => r.status === "failed" || r.status === "errored").length;
  return (
    <section className="tcd-section tcd-history" aria-label="History on this branch">
      <h4 className="tcd-h">On {run.branch || run.tag || "this branch"} <span className="faint">the last {h.results.length} runs, {fails} failed</span></h4>
      <div className="tcd-history-row">
        <HistoryStrip results={h.results} current={run.id} />
        <Sparkline values={durations} label="Time this test took" format={testDuration} />
      </div>
    </section>
  );
}

function firstLine(s: string): string {
  const line = s.split("\n").find((l) => l.trim()) ?? "";
  return line.length > 160 ? line.slice(0, 160) + "…" : line;
}

function cap(s: string): string {
  return s.length > BODY_MAX ? s.slice(0, BODY_MAX) + "\n… (cut)" : s;
}
