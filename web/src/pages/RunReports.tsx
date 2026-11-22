import { useUrlState } from "../urlState";
import { useEffect, useState } from "react";
import { api, type HostedReport, type RunDetail, type RunReports } from "../api";
import { Empty, ErrorBox, Link } from "../components/ui";
import { bytes } from "../components/Security";
import { terms } from "../terms";

const ENDED = ["succeeded", "failed", "skipped", "canceled"];
/** Links to reports work for four hours; ask for new ones well before. */
const RELINK_MS = 3 * 60 * 60 * 1000;

// ReportsTab shows the HTML reports the run's steps published (Playwright's,
// Karate's, coverage HTML, an Allure site), one at a time, in a sandboxed
// frame. The list is fetched each time the tab opens, since its links expire.
export function ReportsTab({ run, version }: { run: RunDetail; version: string }) {
  const [data, setData] = useState<RunReports | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [tick, setTick] = useState(0);
  const [chosen, setChosen] = useUrlState("report", "");
  useEffect(() => {
    let alive = true;
    api.runReports(run.id, true).then((d) => { if (alive) { setData(d); setError(null); } }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [run.id, version, tick]);
  useEffect(() => {
    const id = window.setInterval(() => setTick((n) => n + 1), RELINK_MS);
    return () => window.clearInterval(id);
  }, []);

  const choose = (key: string) => setChosen(key, "push");

  if (error) return <ErrorBox error={error} />;
  if (!data) return <div className="loading">Loading…</div>;
  const kept = new Set(data.reports.map((r) => `${r.step}/${r.name}`));
  const missing = data.declared.filter((d) => !kept.has(d));
  if (data.reports.length === 0 && data.declared.length === 0) {
    return <Empty title="This run has no HTML reports"><HTMLHowTo /></Empty>;
  }
  const order = run.step_list.map((s) => s.id);
  const reports = [...data.reports].sort((a, b) => (order.indexOf(a.step) - order.indexOf(b.step)) || a.name.localeCompare(b.name));
  const current = reports.find((r) => `${r.step}/${r.name}` === chosen) ?? reports.find((r) => r.url) ?? reports[0];

  return (
    <div className="hreports">
      <div className="hlist-wrap">
        <ul className="hlist" aria-label="HTML reports">
          {reports.map((r) => {
            const key = `${r.step}/${r.name}`;
            const on = current === r;
            return (
              <li key={key}>
                <button type="button" className={"hitem" + (on ? " hitem-on" : "")} aria-pressed={on} onClick={() => choose(key)}>
                  <ReportIcon />
                  <span className="hitem-main">
                    <span className="hitem-name">{r.name}</span>
                    <span className="hitem-sub">{r.step} · {r.files} file{r.files === 1 ? "" : "s"} · {bytes(r.bytes)}</span>
                  </span>
                </button>
              </li>
            );
          })}
          {missing.map((d) => {
            const [step, name] = d.split("/");
            const st = run.step_list.find((s) => s.id === step);
            const going = st && !ENDED.includes(st.status);
            return (
              <li key={d}>
                <div className="hitem hitem-off">
                  <ReportIcon />
                  <span className="hitem-main">
                    <span className="hitem-name">{name}</span>
                    <span className="hitem-sub">
                      {step} · {going ? "kept when the step ends" : <>not kept: see <Link href={`/runs/${run.id}?step=${encodeURIComponent(step)}`}>the step's log</Link></>}
                    </span>
                  </span>
                </div>
              </li>
            );
          })}
        </ul>
      </div>
      {/* Keyed by the report and the relink tick, so a list fetched again as other steps end does not reload the page. */}
      {current ? <ReportFrame key={current.artifact + ":" + tick} r={current} separate={data.origin === "separate"} /> : (
        <div className="empty">
          <div className="empty-title">No report was kept</div>
          <div className="empty-body">The steps that declare HTML reports did not keep them. Each step's log says why, for example a directory that did not exist.</div>
        </div>
      )}
    </div>
  );
}

function ReportFrame({ r: latest, separate }: { r: HostedReport; separate: boolean }) {
  const [r] = useState(latest);
  return (
    <section className="card card-flush hframe" aria-label={`Report ${r.name}`}>
      <div className="hframe-bar">
        <div className="hframe-title">
          <strong>{r.name}</strong>
          <span className="faint small">from {r.step}{r.attempt > 1 ? `, attempt ${r.attempt}` : ""}</span>
        </div>
        {r.url && (
          <a className="btn btn-sm" href={r.url} target="_blank" rel="noopener noreferrer">
            Open in a new tab
            <svg width="13" height="13" viewBox="0 0 16 16" aria-hidden="true" className="ic-line"><path d="M9 2.5h4.5V7M13.5 2.5L7 9M11.5 9.5v4h-9v-9h4" /></svg>
          </a>
        )}
      </div>
      <p className="hframe-note">
        <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true" className="ic-line"><path d="M8 1.5l5.5 2v4c0 3.5-2.4 6-5.5 7-3.1-1-5.5-3.5-5.5-7v-4z" strokeLinejoin="round" /></svg>
        <span>
          The step wrote this report, so it is shown sandboxed{separate ? ", from a separate address," : ""}: its scripts run, but it cannot act as you,
          read Launchway's pages or reach anything outside the report. Its link works for a few hours.
        </span>
      </p>
      {r.url ? (
        <iframe className="hframe-frame" src={r.url} title={`HTML report ${r.name} from ${r.step}`}
          sandbox="allow-scripts allow-popups allow-modals allow-downloads" referrerPolicy="no-referrer" loading="lazy" />
      ) : (
        <div className="empty empty-sm hframe-missing">
          This report has no <span className="mono">{r.index}</span> page, so there is nothing to open. Set <span className="mono">index:</span> to the report's start page.
        </div>
      )}
    </section>
  );
}

function ReportIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 16 16" aria-hidden="true" className="ic-line hitem-ic">
      <rect x="2" y="1.5" width="12" height="13" rx="1.5" /><path d="M4.8 11.5V9M7.3 11.5V6.5M9.8 11.5V8M4.5 4.2h4" />
    </svg>
  );
}

/** How to publish an HTML report, for empty states. */
export function HTMLHowTo() {
  return (
    <div className="howto">
      <p>{terms().say("Name the directory a tool writes its HTML report to, and Launchway keeps it with the {run} and shows it here, sandboxed:")}</p>
      <pre className="code">{`steps:
  e2e:
    image: mcr.microsoft.com/playwright:v1.56.0
    run: npx playwright test
    reports:
      html:
        name: playwright
        path: playwright-report`}</pre>
      <p className="sub">Any static site works: Playwright's and Karate's reports, an Allure site, coverage HTML. Add <span className="mono">index:</span> when the start page is not index.html.</p>
    </div>
  );
}
