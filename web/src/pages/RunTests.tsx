import { oneOf, useUrlState } from "../urlState";
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type CasePage, type CaseSort, type CompareCase, type Facet, type RunDetail, type RunTests, type StepTests, type TestCase, type TestCompare } from "../api";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { Badge, ReportsHowTo, TagPills, TestBar, TestIcon, VariantPill, failing, formatLabel, isBrowser, passRate, testDuration, testStatusLabel, BrowserIcon } from "../components/Tests";
import { CaseDetail } from "../components/TestCase";
import { terms } from "../terms";

type Filter = "" | "failed" | "passed" | "skipped" | "flaky";
type Grouping = "none" | "step" | "suite" | "variant";

const FILTERS: [Filter, string][] = [["", "All"], ["failed", "Failed"], ["passed", "Passed"], ["skipped", "Skipped"], ["flaky", "Flaky"]];
const SORTS: [CaseSort, string][] = [["failures", "Failures first"], ["slowest", "Slowest"], ["name", "Name"]];
const PAGE = 200;

function caseFromURL(): number {
  const n = Number(new URLSearchParams(location.search).get("case"));
  return Number.isFinite(n) && n > 0 ? n : 0;
}

/** The address of one result on the run page. */
export function caseHref(run: string, id: number): string {
  return `/runs/${run}?tab=tests&view=results&case=${id}`;
}

const DebriefView = lazy(() => import("./Debrief").then((m) => ({ default: m.DebriefView })));

// TestsTab shows a run's test results: a summary, how they compare with
// the previous run, then every result, failures first, each opening to
// everything recorded about it. ?case= opens one result on its own.
export function TestsTab({ run, version, step, onStep }: {
  run: RunDetail; version: string; step: string; onStep: (step: string) => void;
}) {
  const [tests, setTests] = useState<RunTests | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [caseId, setCaseId] = useState(caseFromURL);
  // The view is in the address (?view=results), so a link or a bookmark opens the same one.
  const [view, setView] = useUrlState<"debrief" | "results">("view", "debrief", oneOf("debrief", "results"));
  const chooseView = setView;
  useEffect(() => {
    let alive = true;
    api.runTests(run.id, true).then((t) => { if (alive) { setTests(t); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [run.id, version]);
  useEffect(() => {
    const on = () => setCaseId(caseFromURL());
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  const openCase = useCallback((id: number) => {
    const q = new URLSearchParams(location.search);
    q.set("tab", "tests");
    if (id) q.set("case", String(id)); else q.delete("case");
    history.pushState(null, "", location.pathname + "?" + q.toString());
    setCaseId(id);
    window.scrollTo(0, 0);
  }, []);

  if (error) return <ErrorBox error={error} />;
  if (!tests) return <div className="loading">Loading…</div>;
  if (caseId) return <CaseView run={run} id={caseId} onBack={() => openCase(0)} />;
  if (!tests.totals) {
    const running = run.step_list.filter((s) => tests.declared.includes(s.id) && !["succeeded", "failed", "skipped", "canceled"].includes(s.status));
    if (tests.declared.length > 0) {
      return (
        <Empty title={running.length ? "No test results yet" : "No test results"}>
          {running.length
            ? <>Results appear when {running.map((s) => s.id).join(", ")} {running.length > 1 ? "end" : "ends"}.</>
            : <>{tests.declared.join(", ")} {tests.declared.length > 1 ? "declare" : "declares"} {terms().say("{debriefs}, but none were collected: the tests ran (or were skipped) and no report came out of them.")} <strong>The step's log says why.</strong> A step of a built-in template prints a box that starts “TESTS RAN, BUT NO TEST REPORT WAS PRODUCED” with the one line to add; for your own step, make the test command write JUnit XML to the path it declares.</>}
        </Empty>
      );
    }
    return (
      <Empty title="This run has no test results">
        <ReportsHowTo />
      </Empty>
    );
  }
  return (
    <>
      <div className="dview chips" role="group" aria-label="How to read the results">
        <button type="button" className={"chip" + (view === "debrief" ? " chip-on" : "")} aria-pressed={view === "debrief"} onClick={() => chooseView("debrief")}>Debrief</button>
        <button type="button" className={"chip" + (view === "results" ? " chip-on" : "")} aria-pressed={view === "results"} onClick={() => chooseView("results")}>All results</button>
      </div>
      {view === "debrief"
        ? <Suspense fallback={<div className="loading">Loading the Debrief…</div>}><DebriefView runId={run.id} version={version} openCase={(id) => { chooseView("results"); openCase(id); }} /></Suspense>
        : <TestsView run={run} tests={tests} version={version} step={step} onStep={onStep} openCase={openCase} />}
    </>
  );
}

function TestsView({ run, tests, version, step, onStep, openCase }: {
  run: RunDetail; tests: RunTests; version: string; step: string; onStep: (step: string) => void; openCase: (id: number) => void;
}) {
  const t = tests.totals!;
  const bad = failing(t);
  // The filters are in the address (?tab=tests&result=failed&find=timeout&suite=api&order=slowest&group=suite), so a filtered
  // list can be linked; typing waits a moment before it asks the server.
  const [filter, setFilter] = useUrlState<Filter>("result", bad > 0 ? "failed" : "");
  const [search, setSearch] = useUrlState("find", "");
  const [query, setQuery] = useState(search.trim());
  const [tag, setTag] = useUrlState("tag", "");
  const [suite, setSuite] = useUrlState("suite", "");
  const [variant, setVariant] = useUrlState("variant", "");
  const [sort, setSort] = useUrlState<CaseSort>("order", "failures");
  const [grouping, setGrouping] = useUrlState<Grouping>("group", tests.steps.length > 1 ? "step" : "none");
  const [page, setPage] = useState<CasePage | null>(null);
  const [more, setMore] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // Facets of every result, so a choice does not hide the others.
  const [facets, setFacets] = useState<{ tags: Facet[]; variants: Facet[]; suites: Facet[] } | null>(null);

  // Typing waits a moment before asking the server.
  useEffect(() => {
    const id = window.setTimeout(() => setQuery(search.trim()), 200);
    return () => window.clearTimeout(id);
  }, [search]);

  const q = { status: filter, q: query, step, tag, suite, variant, sort: sort === "failures" ? "" as const : sort };
  const ask = useRef(0);
  useEffect(() => {
    const n = ++ask.current;
    api.testCases(run.id, { ...q, limit: PAGE }, true)
      .then((p) => {
        if (ask.current !== n) return;
        setPage(p); setError(null);
        if (!filter && !query && !tag && !suite && !variant) setFacets({ tags: p.tags ?? [], variants: p.variants ?? [], suites: p.suites ?? [] });
      })
      .catch((e) => ask.current === n && setError(e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [run.id, filter, query, step, tag, suite, variant, sort, version]);

  const loadMore = async () => {
    if (!page) return;
    setMore(true);
    try {
      const next = await api.testCases(run.id, { ...q, limit: PAGE, offset: page.cases.length });
      setPage({ ...next, cases: page.cases.concat(next.cases) });
    } catch (e) { setError(e); } finally { setMore(false); }
  };

  const groups = useMemo(() => {
    const cases = page?.cases ?? [];
    if (grouping === "none") return [{ key: "", label: "", cases }];
    const by = new Map<string, TestCase[]>();
    for (const c of cases) {
      const k = grouping === "step" ? c.step : grouping === "suite" ? (c.suite || "(no suite)") : (c.variant || "(no browser or variant)");
      by.set(k, [...(by.get(k) ?? []), c]);
    }
    return [...by.entries()].map(([key, cs]) => ({ key, label: key, cases: cs }));
  }, [page, grouping]);

  const counts = page?.counts ?? {};
  const shownStep = tests.steps.find((s) => s.step === step);
  const f = facets ?? { tags: page?.tags ?? [], variants: page?.variants ?? [], suites: page?.suites ?? [] };
  const withSel = (list: Facet[], v: string) => (v && !list.some((x) => x.value === v) ? [{ value: v, count: 0 }, ...list] : list);
  const browsers = f.variants.length > 0 && f.variants.every((v) => isBrowser(v.value));
  const narrowed = !!(tag || suite || variant || query);
  const clear = () => { setTag(""); setSuite(""); setVariant(""); setSearch(""); setQuery(""); };

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
            {tests.flaky > 0 && <Stat label="Flaky" value={tests.flaky} icon={<FlakyIcon />} tone="warn" />}
            <Stat label="Time" value={testDuration(t.duration_ms)} />
          </dl>
        </div>
        <TestBar t={t} />
        <div className="tsum-notes">
          {tests.flaky > 0 && (
            <button type="button" className="linkbtn" onClick={() => setFilter("flaky")}>
              <Badge kind="flaky" title="Failed, then passed when run again">{tests.flaky} flaky: show them</Badge>
            </button>
          )}
          {t.truncated && <span className="faint small">Not every result was kept; see the notes under each step.</span>}
        </div>
        {tests.steps.length > 0 && <StepReports steps={tests.steps} run={run} active={step} onStep={onStep} />}
      </section>

      {tests.previous && <CompareStrip run={run} version={version} openCase={openCase} />}

      <div className="toolbar ttools">
        <div className="chips" role="group" aria-label="Show results">
          {FILTERS.map(([k, label]) => {
            const n = k === "" ? counts.all : counts[k];
            if (k === "flaky" && !n && filter !== "flaky") return null;
            return (
              <button key={k} type="button" aria-pressed={filter === k} className={"chip" + (filter === k ? " chip-on" : "") + (k === "failed" && n ? " chip-bad" : "") + (k === "flaky" ? " chip-flaky" : "")}
                onClick={() => setFilter(k)}>
                {k === "failed" && <TestIcon status="failed" size={13} />}
                {k === "passed" && <TestIcon status="passed" size={13} />}
                {k === "skipped" && <TestIcon status="skipped" size={13} />}
                {k === "flaky" && <FlakyIcon size={13} />}
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
        </div>
      </div>

      <div className="tfilters">
        {f.tags.length > 0 && (f.tags.length <= 8 ? (
          <div className="tfilter" role="group" aria-label="Tag">
            <span className="tfilter-label">Tag</span>
            {f.tags.map((x) => (
              <button key={x.value} type="button" className={"chip chip-sm" + (tag === x.value ? " chip-on" : "")} aria-pressed={tag === x.value}
                onClick={() => setTag(tag === x.value ? "" : x.value)}>
                {x.value.replace(/^@/, "")} <span className="chip-n">{x.count}</span>
              </button>
            ))}
          </div>
        ) : (
          <label className="select tfilter">
            <span className="tfilter-label">Tag</span>
            <select value={tag} onChange={(e) => setTag(e.target.value)}>
              <option value="">Any tag</option>
              {withSel(f.tags, tag).map((x) => <option key={x.value} value={x.value}>{x.value} ({x.count})</option>)}
            </select>
          </label>
        ))}
        {f.variants.length > 0 && (f.variants.length <= 5 ? (
          <div className="tfilter" role="group" aria-label={browsers ? "Browser" : "Variant"}>
            <span className="tfilter-label">{browsers ? "Browser" : "Variant"}</span>
            {f.variants.map((x) => (
              <button key={x.value} type="button" className={"chip chip-sm" + (variant === x.value ? " chip-on" : "")} aria-pressed={variant === x.value}
                onClick={() => setVariant(variant === x.value ? "" : x.value)}>
                {isBrowser(x.value) && <BrowserIcon />}{x.value} <span className="chip-n">{x.count}</span>
              </button>
            ))}
          </div>
        ) : (
          <label className="select tfilter">
            <span className="tfilter-label">{browsers ? "Browser" : "Variant"}</span>
            <select value={variant} onChange={(e) => setVariant(e.target.value)}>
              <option value="">Any</option>
              {withSel(f.variants, variant).map((x) => <option key={x.value} value={x.value}>{x.value} ({x.count})</option>)}
            </select>
          </label>
        ))}
        {f.suites.length > 1 && (
          <label className="select tfilter">
            <span className="tfilter-label">Suite</span>
            <select value={suite} onChange={(e) => setSuite(e.target.value)}>
              <option value="">Every suite</option>
              {withSel(f.suites, suite).map((x) => <option key={x.value} value={x.value}>{x.value} ({x.count})</option>)}
            </select>
          </label>
        )}
        <div className="tfilter tfilter-end">
          <div className="seg" role="group" aria-label="Order">
            {SORTS.map(([k, label]) => (
              <button key={k} type="button" className={"seg-btn" + (sort === k ? " seg-on" : "")} aria-pressed={sort === k} onClick={() => setSort(k)}>{label}</button>
            ))}
          </div>
          <label className="select">
            <span className="visually-hidden">Group by</span>
            <select value={grouping} onChange={(e) => setGrouping(e.target.value as Grouping)}>
              <option value="none">No grouping</option>
              <option value="step">Group by step</option>
              <option value="suite">Group by suite</option>
              {f.variants.length > 0 && <option value="variant">Group by {browsers ? "browser" : "variant"}</option>}
            </select>
          </label>
        </div>
      </div>
      {(step || narrowed) && (
        <div className="tfilter-note">
          {step && <>Showing {step}{shownStep?.reused_from ? <> (reused from #{shownStep.reused_from_number})</> : null} only. <button type="button" className="linkbtn" onClick={() => onStep("")}>Show every step</button>{" "}</>}
          {narrowed && <>{page ? `${page.matching} result${page.matching === 1 ? "" : "s"} match.` : ""} <button type="button" className="linkbtn" onClick={clear}>Clear the filters</button></>}
        </div>
      )}

      <ErrorBox error={error} />
      {!page ? <div className="loading">Loading…</div> : page.cases.length === 0 ? (
        <div className="empty empty-sm">
          {query ? <>No results match <strong>{query}</strong>.</> : filter === "failed" && !narrowed ? "No failures. Every test that ran passed." : "Nothing to show."}
        </div>
      ) : (
        <div className="tgroups">
          {groups.map((g) => (
            <section key={g.key} className="tgroup" aria-label={g.label || "Results"}>
              {g.label && (
                <h3 className="tgroup-head">
                  {grouping === "variant" && isBrowser(g.label) && <BrowserIcon />}
                  <span>{g.label}</span>
                  <span className="faint">{g.cases.length}{g.cases.some(isBad) ? `, ${g.cases.filter(isBad).length} failing` : ""}</span>
                </h3>
              )}
              <ul className="tlist">
                {g.cases.map((c, i) => <CaseRow key={c.id} c={c} run={run} grouped={grouping}
                  open={i === 0 && g === groups[0] && bad > 0 && bad <= 3 && filter === "failed"} />)}
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

function isBad(c: { status: string }) { return c.status === "failed" || c.status === "errored"; }

export function FlakyIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true" className="ic ic-flaky">
      <circle cx="8" cy="8" r="7" /><path d="M4.2 9.6c1-2.6 2.2-2.6 3 0s2 2.6 3 0" fill="none" /><path d="M10.6 5.2l1.4.2-.3 1.4" fill="none" />
    </svg>
  );
}

function Stat({ label, value, icon, tone }: { label: string; value: number | string; icon?: React.ReactNode; tone?: "ok" | "bad" | "warn" }) {
  return (
    <div className={"tstat" + (tone ? " tstat-" + tone : "")}>
      <dt>{label}</dt>
      <dd>{icon}{typeof value === "number" ? value.toLocaleString() : value}</dd>
    </div>
  );
}

// ---- the previous run ----

function CompareStrip({ run, version, openCase }: { run: RunDetail; version: string; openCase: (id: number) => void }) {
  const [cmp, setCmp] = useState<TestCompare | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    api.testCompare(run.id, true).then((c) => { if (alive) { setCmp(c); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [run.id, version]);
  if (error) return error instanceof ApiError && error.status === 404 ? null : <ErrorBox error={error} />;
  if (!cmp?.previous) return null;
  const n = cmp.counts;
  const same = n.new_failures + n.fixed + n.still_failing === 0;
  return (
    <section className="card tcmp" aria-label={`Compared with run ${cmp.previous.number}`}>
      <div className="tcmp-head">
        <h2 className="h-sm">Since <Link href={`/runs/${cmp.previous.id}?tab=tests`}>#{cmp.previous.number}</Link></h2>
        <span className="faint small">the previous run of {run.branch || run.tag || "this ref"} with test results</span>
      </div>
      {same ? <p className="sub tcmp-same">Same results: nothing newly failed, nothing was fixed.</p> : (
        <div className="tcmp-cols">
          <CompareGroup kind="new" title="New failures" items={cmp.new_failures} count={n.new_failures} run={run} openCase={openCase}
            none="Nothing that passed before fails now." />
          <CompareGroup kind="fixed" title="Fixed" items={cmp.fixed} count={n.fixed} run={run} openCase={openCase}
            none="Nothing that failed before passes now." />
          <CompareGroup kind="still" title="Still failing" items={cmp.still_failing} count={n.still_failing} run={run} openCase={openCase}
            none={terms().say("Nothing failed in both {runs}.")} />
        </div>
      )}
    </section>
  );
}

function CompareGroup({ kind, title, items, count, run, openCase, none }: {
  kind: "new" | "fixed" | "still"; title: string; items: CompareCase[]; count: number; run: RunDetail; openCase: (id: number) => void; none: string;
}) {
  const icon = kind === "fixed" ? <TestIcon status="passed" size={18} /> : kind === "new" ? <NewIcon /> : <TestIcon status="failed" size={18} />;
  return (
    <details className={"tcmp-group tcmp-" + kind + (count ? "" : " tcmp-zero")} open={kind === "new" && count > 0 && count <= 10}>
      <summary>
        {icon}
        <span className="tcmp-n">{count}</span>
        <span className="tcmp-title">{title}</span>
      </summary>
      {items.length === 0 ? <p className="faint small tcmp-none">{none}</p> : (
        <ul className="tcmp-list">
          {items.map((x) => (
            <li key={x.key}>
              <a href={caseHref(run.id, x.id)} onClick={(e) => { if (e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey) { e.preventDefault(); openCase(x.id); } }}>
                <TestIcon status={x.status} size={13} />
                <span className="tcmp-name">{x.name}</span>
              </a>
              <span className="tcmp-sub">
                {x.suite && x.suite !== x.name && <span className="mono">{x.suite}</span>}
                {x.variant && <VariantPill variant={x.variant} />}
                <span className="faint">{x.before ? `was ${testStatusLabel(x.before as TestCase["status"]).toLowerCase()}` : "new test"}</span>
              </span>
            </li>
          ))}
          {count > items.length && <li className="faint small">and {count - items.length} more</li>}
        </ul>
      )}
    </details>
  );
}

function NewIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 16 16" aria-hidden="true" className="ic ic-bad">
      <path d="M8 1.2l1.9 2.3 2.9-.6.3 3 2.6 1.4-1.9 2.3.8 2.9-3 .3-1.4 2.6L8 14l-2.3 1.4-1.4-2.6-3-.3.8-2.9L.2 7.3l2.6-1.4.3-3 2.9.6z" strokeLinejoin="round" />
      <path d="M8 5v3.6M8 10.8v.2" fill="none" />
    </svg>
  );
}

// ---- one result on its own ----

function CaseView({ run, id, onBack }: { run: RunDetail; id: number; onBack: () => void }) {
  const [c, setC] = useState<TestCase | null>(null);
  const [error, setError] = useState<unknown>(null);
  const head = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    let alive = true;
    setC(null); setError(null);
    api.testCase(run.id, id).then((x) => alive && setC(x)).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [run.id, id]);
  useEffect(() => { if (c) head.current?.focus(); }, [c]);
  return (
    <div className="tests">
      <div>
        <button type="button" className="btn btn-sm" onClick={onBack}>
          <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true" className="ic-line"><path d="M10 3L5 8l5 5" /></svg>
          All test results
        </button>
      </div>
      {error ? <ErrorBox error={error instanceof ApiError && error.status === 404 ? "This run has no such test result." : error} />
        : !c ? <div className="loading">Loading…</div> : (
          <section className={"card tcv tcase-" + c.status} aria-label="Test result">
            <header className="tcv-head">
              <TestIcon status={c.status} size={24} />
              <div className="tcv-title">
                <h2 ref={head} tabIndex={-1}>{c.name}</h2>
                <div className="tcase-sub">
                  {c.suite && c.suite !== c.name && <span className="tcase-suite">{c.suite}</span>}
                  <VariantPill variant={c.variant} />
                  <TagPills tags={c.tags} />
                </div>
              </div>
            </header>
            <CaseDetail c={c} run={run} full={c} error={null} permalink />
          </section>
        )}
    </div>
  );
}

// ---- report files ----

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
                    : <span className="faint">{f.cases} tests, <span className="tformat">{formatLabel(f.format)}</span></span>}
                  {f.warning && <span className="tfile-warn">{f.warning}</span>}
                </li>
              ))}
              {s.notes?.map((n, i) => <li key={"n" + i} className="tfile-note">{n}</li>)}
              {s.pruned && <li className="tfile-note">The individual results were removed to keep the project's storage within its limit; the totals remain.</li>}
            </ul>
          </li>
        ))}
      </ul>
      {run.reruns.length > 0 && <p className="sub small">{terms().say("Re-runs of this {run} show these results for the steps they reuse.")}</p>}
    </details>
  );
}

// ---- one row ----

function CaseRow({ c, run, grouped, open: openFirst }: { c: TestCase; run: RunDetail; grouped: Grouping; open: boolean }) {
  const [open, setOpen] = useState(openFirst);
  const [full, setFull] = useState<TestCase | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    if (!open || full || !c.has_details) return;
    api.testCase(run.id, c.id).then(setFull).catch(setError);
  }, [open, full, c.has_details, c.id, run.id]);
  const bad = isBad(c);
  const detailId = `tcase-${c.id}`;
  const retries = full?.retries ?? 0;
  return (
    <li className={"tcase tcase-" + c.status + (open ? " tcase-open" : "")}>
      <button type="button" className="tcase-row" aria-expanded={open} aria-controls={detailId} onClick={() => setOpen(!open)}>
        <TestIcon status={c.status} />
        <span className="tcase-main">
          <span className="tcase-name">{c.name}</span>
          <span className="tcase-sub">
            {grouped !== "suite" && c.suite && c.suite !== c.name && <span className="tcase-suite">{c.suite}</span>}
            {grouped !== "step" && run.step_list.length > 1 && <span className="tcase-step">in {c.step}</span>}
            {grouped !== "variant" && <VariantPill variant={c.variant} />}
            <TagPills tags={c.tags} />
          </span>
          {bad && c.message && !open && <span className="tcase-msg">{firstLine(c.message)}</span>}
        </span>
        <span className="tcase-badges">
          {c.new_failure && <Badge kind="new" title="Passed in the previous run of the same branch">New failure</Badge>}
          {c.flaky && <Badge kind="flaky" title="Failed, then passed when run again">Flaky</Badge>}
          {retries > 0 && !c.flaky && <Badge kind="info" title="The test tool ran it again">{retries + 1} tries</Badge>}
          <span className="visually-hidden">{testStatusLabel(c.status)}</span>
        </span>
        <span className="tcase-time">{testDuration(c.duration_ms)}</span>
        <svg className="tcase-caret" width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M5 6l3 3 3-3" fill="none" /></svg>
      </button>
      {open && (
        <div className="tcase-detail" id={detailId}>
          <CaseDetail c={c} run={run} full={full} error={error} />
        </div>
      )}
    </li>
  );
}

function firstLine(s: string): string {
  const line = s.split("\n").find((l) => l.trim()) ?? "";
  return line.length > 160 ? line.slice(0, 160) + "…" : line;
}
