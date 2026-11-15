import { useEffect, useRef, useState } from "react";
import { api, type ArtifactView, type FindingStatus, type FindingView, type RunDetail, type RunFindings, type Severity, type StepFindings, type StepSBOM } from "../api";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { navigate } from "../router";
import { CopyBlock } from "../components/Tests";
import {
  CopyValue, FindingStatusBadge, FindingsHowTo, LockIcon, SEVERITIES, SeverityBadge, SeverityBar, SeverityLegend,
  artifactFileURL, bytes, safeURL, serious, severityLabel, where,
} from "../components/Security";

const PAGE = 100;
const ENDED = ["succeeded", "failed", "skipped", "canceled"];

type StatusFilter = "" | FindingStatus;

// SecurityTab shows what the run's scanners found: a summary by severity,
// what changed since the previous run of the branch, every finding, most
// severe first, and the components its SBOMs list.
export function SecurityTab({ run, version }: { run: RunDetail; version: string }) {
  const [severities, setSeverities] = useState<Severity[]>([]);
  const [tool, setTool] = useState("");
  const [status, setStatus] = useState<StatusFilter>("");
  const [step, setStep] = useState("");
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [data, setData] = useState<RunFindings | null>(null);
  const [more, setMore] = useState(false);
  const [error, setError] = useState<unknown>(null);

  // Typing waits a moment before asking the server.
  useEffect(() => {
    const id = window.setTimeout(() => setQuery(search.trim()), 250);
    return () => window.clearTimeout(id);
  }, [search]);

  const q = { severity: severities.join(","), tool, status, q: query, step, limit: PAGE };
  const ask = useRef(0);
  useEffect(() => {
    const n = ++ask.current;
    api.runFindings(run.id, { severity: severities.join(","), tool, status, q: query, step, limit: PAGE }, true)
      .then((d) => { if (ask.current === n) { setData(d); setError(null); } })
      .catch((e) => ask.current === n && setError(e));
  }, [run.id, severities, tool, status, query, step, version]);

  const loadMore = async () => {
    if (!data) return;
    setMore(true);
    try {
      const next = await api.runFindings(run.id, { ...q, offset: data.findings.length });
      setData({ ...next, findings: data.findings.concat(next.findings) });
    } catch (e) { setError(e); } finally { setMore(false); }
  };

  if (error && !data) return <ErrorBox error={error} />;
  if (!data) return <div className="loading">Loading…</div>;

  const c = data.counts;
  if (!c && data.sboms.length === 0) {
    const going = run.step_list.filter((s) => data.declared.includes(s.id) && !ENDED.includes(s.status));
    if (data.declared.length > 0) {
      return (
        <Empty title={going.length ? "No findings yet" : "No security reports"}>
          {going.length
            ? <>Findings appear when {going.map((s) => s.id).join(", ")} {going.length > 1 ? "end" : "ends"}.</>
            : <>{data.declared.join(", ")} {data.declared.length > 1 ? "declare" : "declares"} security reports, but none were read. The step's log says why, for example a glob that matched no files.</>}
        </Empty>
      );
    }
    return (
      <Empty title="This run has no security findings">
        <FindingsHowTo />
      </Empty>
    );
  }

  const toggle = (s: Severity) => setSeverities((cur) => cur.includes(s) ? cur.filter((x) => x !== s) : SEVERITIES.filter((x) => x === s || cur.includes(x)));
  const filtered = severities.length > 0 || tool || status || query || step;
  const n = c ? serious(c) : 0;

  return (
    <div className="tests secview">
      {c && (
        <section className="card tsum" aria-label="Findings summary">
          <div className="tsum-top">
            <div className={"tsum-rate " + (n > 0 ? "tsum-rate-bad" : c.total ? "" : "tsum-rate-ok")}>
              <span className="tsum-rate-num">{n > 0 ? n : c.total}</span>
              <span className="tsum-rate-label">{n > 0 ? "critical or high" : c.total === 1 ? "finding" : "findings"}</span>
            </div>
            <dl className="tstats">
              <Stat label="Findings" value={c.total} />
              {SEVERITIES.map((s) => (s !== "unknown" || c.unknown > 0) && (
                <Stat key={s} label={severityLabel(s)} value={c[s]} tone={(s === "critical" || s === "high") && c[s] > 0 ? "bad" : undefined} />
              ))}
              {(c.suppressed ?? 0) > 0 && <Stat label="Suppressed" value={c.suppressed ?? 0} />}
            </dl>
          </div>
          {c.total > 0 && <SeverityBar c={c} />}
          {c.total > 0 && <SeverityLegend c={c} />}
          <div className="tsum-notes">
            {data.previous ? (
              <span className="sec-compare">
                <button type="button" className="linkbtn" onClick={() => setStatus("new")}><strong className={data.statuses.new ? "sec-new" : undefined}>{data.statuses.new} new</strong></button>,{" "}
                <button type="button" className="linkbtn" onClick={() => setStatus("existing")}>{data.statuses.existing} existing</button>,{" "}
                <button type="button" className="linkbtn" onClick={() => setStatus("fixed")}><strong className={data.statuses.fixed ? "sec-fixed" : undefined}>{data.statuses.fixed} fixed</strong></button>
                {" "}compared with the previous run of this branch (<Link href={`/runs/${data.previous.id}?tab=security`}>#{data.previous.number}</Link>)
              </span>
            ) : <span className="faint small">No earlier run of this branch has findings to compare with.</span>}
            {data.tools.length > 0 && <span className="faint small">Found by {data.tools.join(", ")}</span>}
            {c.truncated && <span className="faint small">Not every finding was kept; see the notes under each step.</span>}
          </div>
          {data.steps.length > 0 && <StepReports steps={data.steps} active={step} onStep={setStep} />}
        </section>
      )}

      {c && (
        <>
          <div className="toolbar ttools">
            <div className="chips" role="group" aria-label="Severity">
              {SEVERITIES.map((s) => (s !== "unknown" || c.unknown > 0) && (
                <button key={s} type="button" aria-pressed={severities.includes(s)} className={"chip sevchip" + (severities.includes(s) ? " chip-on" : "")}
                  onClick={() => toggle(s)}>
                  <span className={"sevdot sevfill-" + s} aria-hidden="true" />{severityLabel(s)} <span className="chip-n">{c[s]}</span>
                </button>
              ))}
            </div>
            <div className="ttools-right">
              <label className="search">
                <span className="visually-hidden">Search findings</span>
                <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true" className="ic-line"><circle cx="7" cy="7" r="4.6" /><path d="M10.5 10.5L14 14" /></svg>
                <input type="search" placeholder="Search rules, messages, files" value={search} onChange={(e) => setSearch(e.target.value)} />
              </label>
              {data.previous && (
                <label className="select">
                  <span className="visually-hidden">Status</span>
                  <select value={status} onChange={(e) => setStatus(e.target.value as StatusFilter)}>
                    <option value="">All current ({data.statuses.new + data.statuses.existing})</option>
                    <option value="new">New ({data.statuses.new})</option>
                    <option value="existing">Existing ({data.statuses.existing})</option>
                    <option value="fixed">Fixed ({data.statuses.fixed})</option>
                  </select>
                </label>
              )}
              {data.tools.length > 1 && (
                <label className="select">
                  <span className="visually-hidden">Tool</span>
                  <select value={tool} onChange={(e) => setTool(e.target.value)}>
                    <option value="">Every tool</option>
                    {data.tools.map((t) => <option key={t} value={t}>{t}</option>)}
                  </select>
                </label>
              )}
              {data.steps.length > 1 && (
                <label className="select">
                  <span className="visually-hidden">Step</span>
                  <select value={step} onChange={(e) => setStep(e.target.value)}>
                    <option value="">Every step</option>
                    {data.steps.map((s) => <option key={s.step} value={s.step}>{s.step}</option>)}
                  </select>
                </label>
              )}
            </div>
          </div>
          {status === "fixed" && data.previous && (
            <div className="tfilter-note">
              Findings of <Link href={`/runs/${data.previous.id}?tab=security`}>#{data.previous.number}</Link> that this run no longer has.{" "}
              <button type="button" className="linkbtn" onClick={() => setStatus("")}>Show current findings</button>
            </div>
          )}
          {filtered && status !== "fixed" && (
            <div className="tfilter-note">
              {data.matched} of {c.total} findings match.{" "}
              <button type="button" className="linkbtn" onClick={() => { setSeverities([]); setTool(""); setStatus(""); setStep(""); setSearch(""); }}>Clear filters</button>
            </div>
          )}

          <ErrorBox error={error} />
          {data.findings.length === 0 ? (
            <div className="empty empty-sm">
              {query ? <>No findings match <strong>{query}</strong>.</>
                : status === "fixed" ? "Nothing was fixed: every finding of the previous run is still here."
                : status === "new" ? "Nothing new since the previous run."
                : c.total === 0 ? "The scanners ran and found nothing." : "No findings match these filters."}
            </div>
          ) : (
            <div className="tgroups">
              <ul className="tlist">
                {data.findings.map((f, i) => <FindingRow key={f.id + ":" + f.status} f={f} run={run} multiStep={data.steps.length > 1} open={i === 0 && data.findings.length === 1} />)}
              </ul>
              {data.findings.length < data.matched && (
                <button type="button" className="btn tmore" disabled={more} onClick={loadMore}>
                  {more ? "Loading…" : `Show ${Math.min(PAGE, data.matched - data.findings.length)} more of ${data.matched - data.findings.length}`}
                </button>
              )}
            </div>
          )}
        </>
      )}
      {!c && (
        <div className="empty empty-sm">
          No step reported findings{data.declared.length > 0 ? "" : "; only an SBOM"}. <FindingsHowTo compact />
        </div>
      )}

      {data.sboms.length > 0 && <SBOMSection run={run} sboms={data.sboms} version={version} />}
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: number; tone?: "bad" }) {
  return (
    <div className={"tstat" + (tone ? " tstat-" + tone : "")}>
      <dt>{label}</dt>
      <dd>{value.toLocaleString()}</dd>
    </div>
  );
}

// StepReports lists each step's findings and SARIF files, with what went
// wrong reading them.
function StepReports({ steps, active, onStep }: { steps: StepFindings[]; active: string; onStep: (s: string) => void }) {
  const problems = steps.some((s) => s.files.some((f) => f.error || f.warning) || (s.notes?.length ?? 0) > 0 || s.pruned);
  return (
    <details className="treports" open={problems}>
      <summary>
        {steps.length === 1 ? "Report files" : `${steps.length} steps reported`}
        {problems && <span className="treports-warn"> · some files need a look</span>}
      </summary>
      <ul className="treport-steps">
        {steps.map((s) => (
          <li key={s.step}>
            <div className="treport-step">
              {steps.length > 1
                ? <button type="button" className={"linkbtn" + (active === s.step ? " linkbtn-on" : "")} aria-pressed={active === s.step}
                  onClick={() => onStep(active === s.step ? "" : s.step)}><strong>{s.step}</strong></button>
                : <strong>{s.step}</strong>}
              <span className="faint">{s.counts.total} finding{s.counts.total === 1 ? "" : "s"}{serious(s.counts) ? `, ${serious(s.counts)} critical or high` : ""}{s.attempt > 1 ? `, attempt ${s.attempt}` : ""}</span>
              {s.reused_from && <span className="badge-reused">Reused from <Link href={`/runs/${s.reused_from.id}?tab=security`}>#{s.reused_from.number}</Link></span>}
              {s.counts.total > 0 && <div className="treport-bar"><SeverityBar c={s.counts} thin /></div>}
            </div>
            <ul className="treport-files">
              {s.files.map((f) => (
                <li key={f.path} className={f.error ? "tfile-bad" : undefined}>
                  <Mono>{f.path}</Mono>
                  {f.error ? <span className="tfile-err">{f.error}</span>
                    : <span className="faint">{f.findings} finding{f.findings === 1 ? "" : "s"}{f.tools?.length ? ` from ${f.tools.join(", ")}` : ""}, SARIF</span>}
                  {f.warning && <span className="tfile-warn">{f.warning}</span>}
                </li>
              ))}
              {s.notes?.map((n, i) => <li key={"n" + i} className="tfile-note">{n}</li>)}
              {s.pruned && <li className="tfile-note">The individual findings were removed to keep the project's storage within its limit; the totals remain.</li>}
            </ul>
          </li>
        ))}
      </ul>
    </details>
  );
}

export function FindingRow({ f, run, multiStep, open: openFirst, extra }: {
  f: FindingView; run?: RunDetail; multiStep?: boolean; open: boolean; extra?: React.ReactNode;
}) {
  const [open, setOpen] = useState(openFirst);
  const detailId = `finding-${f.id}-${f.status ?? ""}`;
  const loc = where(f);
  const doc = safeURL(f.rule?.help_uri);
  return (
    <li className={"tcase finding finding-" + f.severity + (open ? " tcase-open" : "") + (f.status === "fixed" ? " finding-fixed" : "")}>
      <button type="button" className="tcase-row" aria-expanded={open} aria-controls={detailId} onClick={() => setOpen(!open)}>
        <SeverityBadge s={f.severity} />
        <span className="tcase-main">
          <span className="tcase-name">
            {f.secret && <span className="secret-mark" title="A secret: its value is never stored or shown"><LockIcon /><span className="visually-hidden">Secret.</span></span>}
            {f.rule?.short && f.rule.short !== f.rule_id ? <>{oneLine(f.rule.short)} <span className="finding-rule">{f.rule_id}</span></> : <span className="mono">{f.rule_id}</span>}
          </span>
          <span className="tcase-sub">
            {loc && <span className="mono finding-loc">{loc}</span>}
            <span>{f.tool}</span>
            {multiStep && <span>in {f.step}</span>}
            {extra}
          </span>
        </span>
        <span className="tcase-badges"><FindingStatusBadge s={f.status} /></span>
        <svg className="tcase-caret" width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M5 6l3 3 3-3" fill="none" /></svg>
      </button>
      {open && (
        <div className="tcase-detail" id={detailId}>
          {f.secret && (
            <div className="secret-note"><LockIcon size={15} /><span><strong>Secret</strong> — its value is never stored or shown. Remove it from the code, and change it wherever it is used: anyone who can read the repository may already know it.</span></div>
          )}
          {f.status === "fixed" && (
            <p className="sub">In the previous run of this branch (<Link href={`/runs/${f.run_id}?tab=security`}>#{f.run_number}</Link>), gone in this one.</p>
          )}
          {f.message && <CopyBlock label="Message" text={f.message} />}
          {f.rule?.full && f.rule.full !== f.message && (
            <div className="finding-text"><div className="finding-h">About the rule</div><p>{f.rule.full}</p></div>
          )}
          {f.rule?.help && f.rule.help !== f.rule.full && (
            <div className="finding-text"><div className="finding-h">How to fix it</div><p>{f.rule.help}</p></div>
          )}
          <dl className="tcase-facts">
            <dt>Severity</dt><dd>{severityLabel(f.severity)}{f.level ? ` (the tool says ${f.level})` : ""}</dd>
            <dt>Rule</dt><dd className="mono">{f.rule_id}</dd>
            {loc && <><dt>Location</dt><dd className="mono">{loc}{f.start_column ? `, column ${f.start_column}` : ""}</dd></>}
            <dt>Tool</dt><dd>{f.tool}</dd>
            <dt>Step</dt><dd>{f.step}{f.attempt > 1 ? `, attempt ${f.attempt}` : ""}{run && f.run_id !== run.id && f.status !== "fixed" ? " (reused from an earlier run)" : ""}</dd>
            {f.first_seen && <><dt>Open since</dt><dd><Link href={`/runs/${f.first_seen.id}?tab=security`}>run #{f.first_seen.number}</Link></dd></>}
            {f.rule?.tags && f.rule.tags.length > 0 && <><dt>Tags</dt><dd>{f.rule.tags.join(", ")}</dd></>}
            <dt>Fingerprint</dt><dd className="small"><CopyValue value={f.fingerprint} label="fingerprint" keep={40} /></dd>
          </dl>
          {doc && <a href={doc} target="_blank" rel="noopener noreferrer" className="tcase-history">Rule documentation <span aria-hidden="true">↗</span><span className="visually-hidden"> (opens in a new tab)</span></a>}
        </div>
      )}
    </li>
  );
}

// ComponentFinder opens the component search for a name.
function ComponentFinder() {
  const [name, setName] = useState("");
  return (
    <form className="sbom-find" role="search" onSubmit={(e) => { e.preventDefault(); if (name.trim()) navigate(`/components?name=${encodeURIComponent(name.trim())}`); }}>
      <label className="search">
        <span className="visually-hidden">Component name</span>
        <input type="search" placeholder="Component name, such as requests" value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <button type="submit" className="btn btn-sm" disabled={!name.trim()}>Find in every project</button>
    </form>
  );
}

function oneLine(s: string): string {
  const line = s.split("\n").find((l) => l.trim()) ?? "";
  return line.length > 140 ? line.slice(0, 140) + "…" : line;
}

// SBOMSection shows each step's SBOM: how many components, their licences,
// and the file to download.
function SBOMSection({ run, sboms, version }: { run: RunDetail; sboms: StepSBOM[]; version: string }) {
  const [arts, setArts] = useState<ArtifactView[] | null>(null);
  useEffect(() => {
    let alive = true;
    api.runArtifacts(run.id, true).then((d) => alive && setArts(d.artifacts)).catch(() => alive && setArts([]));
    return () => { alive = false; };
  }, [run.id, version]);
  return (
    <section className="card sbom" aria-label="Software bill of materials">
      <div className="card-head">
        <h2 className="h-sm">Software bill of materials</h2>
        <ComponentFinder />
      </div>
      {sboms.map((s) => {
        const art = arts?.find((a) => a.id === s.artifact);
        const lic = Object.entries(s.licences ?? {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
        const max = Math.max(...lic.map(([, n]) => n), 1);
        const counted = lic.reduce((n, [, v]) => n + v, 0);
        return (
          <div key={s.step} className="sbom-step">
            <div className="treport-step">
              <strong>{s.step}</strong>
              <span className="faint">{s.components.toLocaleString()} component{s.components === 1 ? "" : "s"}{s.attempt > 1 ? `, attempt ${s.attempt}` : ""}</span>
              {s.run_id !== run.id && <span className="badge-reused">Reused from an earlier run</span>}
            </div>
            <ul className="treport-files">
              {s.files.map((f) => (
                <li key={f.path} className={f.error ? "tfile-bad" : undefined}>
                  <Mono>{f.path}</Mono>
                  {f.error ? <span className="tfile-err">{f.error}</span>
                    : <span className="faint">{f.components} components, {f.format === "cyclonedx-json" ? "CycloneDX" : f.format ?? "SBOM"}{f.spec_version ? ` ${f.spec_version}` : ""}, {bytes(f.bytes)}</span>}
                  {f.warning && <span className="tfile-warn">{f.warning}</span>}
                  {art?.status === "kept" && art.files.some((x) => x.path === f.path) && (
                    <a href={artifactFileURL(art, f.path)} download aria-label={`Download ${f.path}`}>Download</a>
                  )}
                </li>
              ))}
              {s.artifact && arts && (!art || art.status !== "kept") && <li className="tfile-note">The SBOM file is no longer kept{art ? ` (${art.status})` : ""}; the component list remains.</li>}
              {s.notes?.map((n, i) => <li key={"n" + i} className="tfile-note">{n}</li>)}
              {s.truncated && <li className="tfile-note">Only the first {s.stored.toLocaleString()} components were kept for search.</li>}
              {s.pruned && <li className="tfile-note">The components were removed to keep the project's storage within its limit; the totals remain.</li>}
            </ul>
            {lic.length > 0 && (
              <div className="lics">
                <div className="finding-h">Licences{counted < s.components ? <span className="faint"> (the commonest; {s.components - counted} components not counted)</span> : null}</div>
                <ul className="licbars">
                  {lic.map(([name, n]) => (
                    <li key={name}>
                      <span className="licname mono">{name}</span>
                      <span className="licbar" aria-hidden="true"><span style={{ width: `${(n / max) * 100}%` }} /></span>
                      <span className="licn">{n}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        );
      })}
    </section>
  );
}
