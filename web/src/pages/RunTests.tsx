import { useEffect, useMemo, useRef, useState } from "react";
import { api, type CasePage, type RunDetail, type RunTests, type StepTests, type TestCase } from "../api";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { Badge, CopyBlock, ReportsHowTo, TestBar, TestIcon, failing, passRate, testDuration, testStatusLabel } from "../components/Tests";

type Filter = "" | "failed" | "passed" | "skipped" | "flaky";
type Grouping = "none" | "step" | "suite";

const FILTERS: [Filter, string][] = [["", "All"], ["failed", "Failed"], ["passed", "Passed"], ["skipped", "Skipped"], ["flaky", "Flaky"]];
const PAGE = 200;

// TestsTab shows a run's test results: a summary, then every result,
// failures first, each opening to its message, trace and output.
export function TestsTab({ run, version, step, onStep }: {
  run: RunDetail; version: string; step: string; onStep: (step: string) => void;
}) {
  const [tests, setTests] = useState<RunTests | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    api.runTests(run.id, true).then((t) => { if (alive) { setTests(t); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [run.id, version]);

  if (error) return <ErrorBox error={error} />;
  if (!tests) return <div className="loading">Loading…</div>;
  if (!tests.totals) {
    const running = run.step_list.filter((s) => tests.declared.includes(s.id) && !["succeeded", "failed", "skipped", "canceled"].includes(s.status));
    if (tests.declared.length > 0) {
      return (
        <Empty title={running.length ? "No test results yet" : "No test results"}>
          {running.length
            ? <>Results appear when {running.map((s) => s.id).join(", ")} {running.length > 1 ? "end" : "ends"}.</>
            : <>{tests.declared.join(", ")} {tests.declared.length > 1 ? "declare" : "declares"} test reports, but none were collected. The step's log says why, for example a glob that matched no files.</>}
        </Empty>
      );
    }
    return (
      <Empty title="This run has no test results">
        <ReportsHowTo />
      </Empty>
    );
  }
  return <TestsView run={run} tests={tests} version={version} step={step} onStep={onStep} />;
}

function TestsView({ run, tests, version, step, onStep }: {
  run: RunDetail; tests: RunTests; version: string; step: string; onStep: (step: string) => void;
}) {
  const t = tests.totals!;
  const bad = failing(t);
  const [filter, setFilter] = useState<Filter>(bad > 0 ? "failed" : "");
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [grouping, setGrouping] = useState<Grouping>(tests.steps.length > 1 ? "step" : "none");
  const [page, setPage] = useState<CasePage | null>(null);
  const [more, setMore] = useState(false);
  const [error, setError] = useState<unknown>(null);

  // Typing waits a moment before asking the server.
  useEffect(() => {
    const id = window.setTimeout(() => setQuery(search.trim()), 200);
    return () => window.clearTimeout(id);
  }, [search]);

  const ask = useRef(0);
  useEffect(() => {
    const n = ++ask.current;
    api.testCases(run.id, { status: filter, q: query, step, limit: PAGE }, true)
      .then((p) => { if (ask.current === n) { setPage(p); setError(null); } })
      .catch((e) => ask.current === n && setError(e));
  }, [run.id, filter, query, step, version]);

  const loadMore = async () => {
    if (!page) return;
    setMore(true);
    try {
      const next = await api.testCases(run.id, { status: filter, q: query, step, limit: PAGE, offset: page.cases.length });
      setPage({ ...next, cases: page.cases.concat(next.cases) });
    } catch (e) { setError(e); } finally { setMore(false); }
  };

  const groups = useMemo(() => {
    const cases = page?.cases ?? [];
    if (grouping === "none") return [{ key: "", label: "", cases }];
    const by = new Map<string, TestCase[]>();
    for (const c of cases) {
      const k = grouping === "step" ? c.step : (c.suite || "(no suite)");
      by.set(k, [...(by.get(k) ?? []), c]);
    }
    return [...by.entries()].map(([key, cs]) => ({ key, label: key, cases: cs }));
  }, [page, grouping]);

  const counts = page?.counts ?? {};
  const shownStep = tests.steps.find((s) => s.step === step);

  return (
    <div className="tests">
      <section className="card tsum" aria-label="Test summary">
        <div className="tsum-top">
          <div className={"tsum-rate " + (bad > 0 ? "tsum-rate-bad" : "tsum-rate-ok")}>
            <span className="tsum-rate-num">{passRate(t)}</span>
            <span className="tsum-rate-label">passed</span>
          </div>
          <dl className="tstats">
            <Stat label="Tests" value={t.total} />
            <Stat label="Passed" value={t.passed} icon={<TestIcon status="passed" />} tone="ok" />
            <Stat label="Failed" value={t.failed} icon={<TestIcon status="failed" />} tone={t.failed ? "bad" : undefined} />
            {t.errored > 0 && <Stat label="Errors" value={t.errored} icon={<TestIcon status="errored" />} tone="bad" />}
            <Stat label="Skipped" value={t.skipped} icon={<TestIcon status="skipped" />} />
            <Stat label="Time" value={testDuration(t.duration_ms)} />
          </dl>
        </div>
        <TestBar t={t} />
        <div className="tsum-notes">
          {tests.new_failures > 0 && tests.previous && (
            <Badge kind="new" title="These passed in the previous run of the same branch">
              {tests.new_failures} new failure{tests.new_failures > 1 ? "s" : ""} since <Link href={`/runs/${tests.previous.id}?tab=tests`}>#{tests.previous.number}</Link>
            </Badge>
          )}
          {tests.flaky > 0 && (
            <button type="button" className="linkbtn" onClick={() => setFilter("flaky")}>
              <Badge kind="flaky" title="Failed, then passed when run again">{tests.flaky} flaky</Badge>
            </button>
          )}
          {t.truncated && <span className="faint small">Not every result was kept; see the notes under each step.</span>}
        </div>
        {tests.steps.length > 0 && <StepReports steps={tests.steps} run={run} active={step} onStep={onStep} />}
      </section>

      <div className="toolbar ttools">
        <div className="chips" role="group" aria-label="Show results">
          {FILTERS.map(([k, label]) => {
            const n = k === "" ? counts.all : counts[k];
            if (k === "flaky" && !n) return null;
            return (
              <button key={k} type="button" aria-pressed={filter === k} className={"chip" + (filter === k ? " chip-on" : "") + (k === "failed" && n ? " chip-bad" : "")}
                onClick={() => setFilter(k)}>
                {label} <span className="chip-n">{n ?? 0}</span>
              </button>
            );
          })}
        </div>
        <div className="ttools-right">
          <label className="search">
            <span className="visually-hidden">Search tests</span>
            <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true" className="ic-line"><circle cx="7" cy="7" r="4.6" /><path d="M10.5 10.5L14 14" /></svg>
            <input type="search" placeholder="Search names, suites, messages" value={search} onChange={(e) => setSearch(e.target.value)} />
          </label>
          <label className="select">
            <span className="visually-hidden">Group by</span>
            <select value={grouping} onChange={(e) => setGrouping(e.target.value as Grouping)}>
              <option value="none">No grouping</option>
              <option value="step">Group by step</option>
              <option value="suite">Group by suite</option>
            </select>
          </label>
        </div>
      </div>
      {step && (
        <div className="tfilter-note">
          Showing {step}{shownStep?.reused_from ? <> (reused from #{shownStep.reused_from_number})</> : null} only.{" "}
          <button type="button" className="linkbtn" onClick={() => onStep("")}>Show every step</button>
        </div>
      )}

      <ErrorBox error={error} />
      {!page ? <div className="loading">Loading…</div> : page.cases.length === 0 ? (
        <div className="empty empty-sm">
          {query ? <>No results match <strong>{query}</strong>.</> : filter === "failed" ? "No failures. Every test that ran passed." : "Nothing to show."}
        </div>
      ) : (
        <div className="tgroups">
          {groups.map((g) => (
            <section key={g.key} className="tgroup" aria-label={g.label || "Results"}>
              {g.label && (
                <h3 className="tgroup-head">
                  <span>{g.label}</span>
                  <span className="faint">{g.cases.length}{g.cases.some((c) => c.status === "failed" || c.status === "errored")
                    ? `, ${g.cases.filter((c) => c.status === "failed" || c.status === "errored").length} failing` : ""}</span>
                </h3>
              )}
              <ul className="tlist">
                {g.cases.map((c, i) => <CaseRow key={c.id} c={c} run={run} grouped={grouping} open={i === 0 && g === groups[0] && bad > 0 && bad <= 3 && filter === "failed"} />)}
              </ul>
            </section>
          ))}
          {page.cases.length < page.matching && (
            <button type="button" className="btn tmore" disabled={more} onClick={loadMore}>
              {more ? "Loading…" : `Show ${Math.min(PAGE, page.matching - page.cases.length)} more of ${page.matching - page.cases.length}`}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function Stat({ label, value, icon, tone }: { label: string; value: number | string; icon?: React.ReactNode; tone?: "ok" | "bad" }) {
  return (
    <div className={"tstat" + (tone ? " tstat-" + tone : "")}>
      <dt>{label}</dt>
      <dd>{icon}{typeof value === "number" ? value.toLocaleString() : value}</dd>
    </div>
  );
}

// StepReports lists each step's results and report files, with what went
// wrong reading them.
function StepReports({ steps, run, active, onStep }: { steps: StepTests[]; run: RunDetail; active: string; onStep: (s: string) => void }) {
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
              <button type="button" className={"linkbtn" + (active === s.step ? " linkbtn-on" : "")} aria-pressed={active === s.step}
                onClick={() => onStep(active === s.step ? "" : s.step)}><strong>{s.step}</strong></button>
              <span className="faint">{s.total} tests{failing(s) ? `, ${failing(s)} failing` : ""}{s.attempt > 1 ? `, attempt ${s.attempt}` : ""}</span>
              {s.reused_from && <span className="badge-reused">Reused from <Link href={`/runs/${s.reused_from}?tab=tests`}>#{s.reused_from_number}</Link></span>}
              {s.attempts.length > 1 && (
                <span className="faint small">earlier: {s.attempts.slice(0, -1).map((a) => `attempt ${a.attempt} ${failing(a)} failing`).join(", ")}</span>
              )}
              <div className="treport-bar"><TestBar t={s} thin /></div>
            </div>
            <ul className="treport-files">
              {s.files.map((f) => (
                <li key={f.path} className={f.error ? "tfile-bad" : undefined}>
                  <Mono>{f.path}</Mono>
                  {f.error ? <span className="tfile-err">{f.error}</span>
                    : <span className="faint">{f.cases} tests, {f.format === "go-test-json" ? "go test -json" : "JUnit XML"}</span>}
                  {f.warning && <span className="tfile-warn">{f.warning}</span>}
                </li>
              ))}
              {s.notes?.map((n, i) => <li key={"n" + i} className="tfile-note">{n}</li>)}
              {s.pruned && <li className="tfile-note">The individual results were removed to keep the project's storage within its limit; the totals remain.</li>}
            </ul>
          </li>
        ))}
      </ul>
      {run.reruns.length > 0 && <p className="sub small">Re-runs of this run show these results for the steps they reuse.</p>}
    </details>
  );
}

function CaseRow({ c, run, grouped, open: openFirst }: { c: TestCase; run: RunDetail; grouped: Grouping; open: boolean }) {
  const [open, setOpen] = useState(openFirst);
  const [full, setFull] = useState<TestCase | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (!open || full || !c.has_details) return;
    api.testCase(run.id, c.id).then(setFull).catch(setError);
  }, [open, full, c.has_details, c.id, run.id]);
  const bad = c.status === "failed" || c.status === "errored";
  const detailId = `tcase-${c.id}`;
  return (
    <li className={"tcase tcase-" + c.status + (open ? " tcase-open" : "")}>
      <button type="button" className="tcase-row" aria-expanded={open} aria-controls={detailId} onClick={() => setOpen(!open)}>
        <TestIcon status={c.status} />
        <span className="tcase-main">
          <span className="tcase-name">{c.name}</span>
          <span className="tcase-sub">
            {grouped !== "suite" && c.suite && <span className="tcase-suite">{c.suite}</span>}
            {grouped !== "step" && run.step_list.length > 1 && <span className="tcase-step">in {c.step}</span>}
            {bad && c.message && !open && <span className="tcase-msg">{firstLine(c.message)}</span>}
          </span>
        </span>
        <span className="tcase-badges">
          {c.new_failure && <Badge kind="new" title="Passed in the previous run of the same branch">New failure</Badge>}
          {c.flaky && <Badge kind="flaky" title="Failed, then passed when run again">Flaky</Badge>}
          <span className="visually-hidden">{testStatusLabel(c.status)}</span>
        </span>
        <span className="tcase-time">{testDuration(c.duration_ms)}</span>
        <svg className="tcase-caret" width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M5 6l3 3 3-3" fill="none" /></svg>
      </button>
      {open && (
        <div className="tcase-detail" id={detailId}>
          <dl className="tcase-facts">
            <dt>Status</dt><dd>{testStatusLabel(c.status)}{c.flaky ? ", flaky" : ""}</dd>
            {c.suite && <><dt>Suite</dt><dd className="mono">{c.suite}</dd></>}
            {c.file && <><dt>File</dt><dd className="mono">{c.file}</dd></>}
            <dt>Step</dt><dd>{c.step}{c.attempt > 1 ? `, attempt ${c.attempt}` : ""}{c.run_id !== run.id ? " (reused from an earlier run)" : ""}</dd>
            {c.report_file && <><dt>Report</dt><dd className="mono">{c.report_file}</dd></>}
          </dl>
          {c.message && <CopyBlock label={c.status === "skipped" ? "Reason" : "Message"} text={(full ?? c).message ?? ""} tone={bad ? "bad" : undefined} />}
          <ErrorBox error={error} />
          {c.has_details && !full && !error && <div className="loading">Loading…</div>}
          {full?.details && <CopyBlock label="Stack trace" text={full.details} />}
          {full?.output && <CopyBlock label="Captured output" text={full.output} />}
          {!c.message && !c.has_details && <p className="sub">Nothing more was recorded for this test.</p>}
          <Link href={`/projects/${encodeURIComponent(run.project)}/tests?test=${c.key}${run.ref ? `&ref=${encodeURIComponent(run.ref)}` : ""}`} className="tcase-history">
            History of this test
          </Link>
        </div>
      )}
    </li>
  );
}

function firstLine(s: string): string {
  const line = s.split("\n").find((l) => l.trim()) ?? "";
  return line.length > 160 ? line.slice(0, 160) + "…" : line;
}
