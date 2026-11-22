import { useCallback, useState } from "react";
import { ProjectHead } from "./PipelineChrome";
import { InsightChart } from "../components/InsightChart";
import { ErrorBox } from "../components/ui";
import { useLoad } from "../guided/useLoad";
import { PageHead, Sheet } from "../kit/layout";
import { useCrumbLabel } from "../nav/crumbs";
import { terms } from "../terms";
import { insightsApi, type Insights, type InsightsAnswer, type InsightsAsk, type InsightStepRow } from "../insightsApi";
import "../releases.css";
import "../insights.css";

// The Insights pages: how delivery is going, per Flight, per Squadron and for the installation,
// worked out from Launchway's own database. Nothing here needs a monitoring system.

const secs = (s: number) => (s <= 0 ? "0 s" : s < 90 ? `${Math.round(s)} s` : s < 5400 ? `${(s / 60).toFixed(1)} min` : `${(s / 3600).toFixed(1)} h`);
const pct = (x: number) => `${Math.round(x * 100)}%`;
const num = (n: number) => String(Math.round(n));
const BLUE = "#1f5fbf", ORANGE = "#b45309", PLUM = "#7c3aed";

function Tile({ k, v, f }: { k: string; v: string; f: string }) {
  return <div><div className="k">{k}</div><div className="v">{v}</div><div className="f">{f}</div></div>;
}

function StepTable({ title, rows, why }: { title: string; rows: InsightStepRow[]; why: string }) {
  return (
    <Sheet title={title}>
      {rows.length === 0 ? <p className="rel-hint" style={{ padding: "14px 16px" }}>{why}</p> : (
        <div className="rel-table-wrap"><table className="rel-table">
          <caption className="visually-hidden">{title}</caption>
          <thead><tr><th scope="col">Step</th><th scope="col" className="n">Runs</th><th scope="col" className="n">Failed</th><th scope="col" className="n">Failure rate</th><th scope="col" className="n">Median</th><th scope="col" className="n">95th percentile</th></tr></thead>
          <tbody>{rows.map((r) => <tr key={r.step}><th scope="row">{r.step}</th><td className="n">{r.runs}</td><td className="n">{r.failures}</td><td className="n">{pct(r.failure_rate)}</td><td className="n">{secs(r.p50_s)}</td><td className="n">{secs(r.p95_s)}</td></tr>)}</tbody>
        </table></div>
      )}
    </Sheet>
  );
}

function Body({ d }: { d: Insights }) {
  const xs = d.series.map((p) => p.at);
  const col = <T,>(f: (p: Insights["series"][number]) => T) => d.series.map(f);
  const hasRuns = d.runs > 0;
  const del = d.delivery;
  return (
    <>
      {d.truncated && <p className="rel-hint" role="status">This period holds more than the 50,000 rows counted; the oldest are shown. Choose a shorter period for exact numbers.</p>}
      <Sheet title="The period at a glance">
        <div className="rel-measures">
          <Tile k={terms().say("{Runs}")} v={String(d.runs)} f={`${d.succeeded} succeeded, ${d.failed} failed, ${d.canceled} canceled`} />
          <Tile k="Success rate" v={d.succeeded + d.failed > 0 ? pct(d.success_rate) : "none yet"} f="of the runs that finished" />
          <Tile k="Duration" v={secs(d.duration_p50_s)} f={`median; 95th percentile ${secs(d.duration_p95_s)}`} />
          <Tile k="Queue wait" v={secs(d.queue_wait_p50_s)} f={`median; 95th percentile ${secs(d.queue_wait_p95_s)}`} />
        </div>
      </Sheet>
      {!hasRuns && <p className="rel-hint" style={{ padding: "6px 2px" }}>No {terms().say("{run}")} started in this period{d.branch ? ` on ${d.branch}` : ""}. Choose a longer period.</p>}
      <Sheet title="Runs over time" pad>
        <InsightChart id="ins-success" title="Run success rate" xs={xs} format={pct} max={1} empty="No finished runs in this period."
          series={[{ name: "Success rate", values: col((p) => (p.succeeded + p.failed > 0 ? p.success_rate : null)), marker: "circle", color: BLUE }]} />
        <InsightChart id="ins-duration" title="Run duration" xs={xs} format={secs} empty="No finished runs in this period."
          series={[{ name: "Median", values: col((p) => (p.duration_p50_s > 0 ? p.duration_p50_s : null)), marker: "circle", color: BLUE },
            { name: "95th percentile", values: col((p) => (p.duration_p95_s > 0 ? p.duration_p95_s : null)), dash: "6 4", marker: "triangle", color: ORANGE }]} />
        <InsightChart id="ins-wait" title="Queue wait" xs={xs} format={secs} empty="No step waited in this period."
          series={[{ name: "Median", values: col((p) => (p.queue_wait_p50_s > 0 ? p.queue_wait_p50_s : null)), marker: "circle", color: BLUE },
            { name: "95th percentile", values: col((p) => (p.queue_wait_p95_s > 0 ? p.queue_wait_p95_s : null)), dash: "6 4", marker: "triangle", color: ORANGE }]} />
      </Sheet>
      <StepTable title="Slowest steps" rows={d.slowest_steps} why="No step finished in this period." />
      <StepTable title="Flakiest steps" rows={d.flakiest_steps} why="No step failed in this period." />
      <Sheet title="Tests" pad>
        <p className="rel-hint">{d.tests_total} results reported, {d.tests_failed} failed, {d.tests_flaky} passed only on a retry.</p>
        <InsightChart id="ins-tests" title="Test failure rate" xs={xs} format={pct} empty="No test results were reported in this period."
          series={[{ name: "Failed results", values: col((p) => (p.tests_total > 0 ? p.test_failure_rate : null)), marker: "square", color: ORANGE }]} />
      </Sheet>
      {del && (
        <Sheet title="Delivery" pad>
          <div className="rel-measures">
            <Tile k="Frequency" v={del.frequency_per_week.toFixed(2)} f={`successful launches a week; ${del.launches} launches in all`} />
            <Tile k="Lead time (median)" v={secs(del.lead_time_median_s)} f="from merge to the Airfield" />
            <Tile k="Change failure rate" v={pct(del.change_failure_rate)} f={`${del.failures} of ${del.launches} launches failed or were returned to base`} />
            <Tile k="Time to restore (median)" v={secs(del.time_to_restore_median_s)} f={`${del.unrestored} not yet restored`} />
          </div>
          <InsightChart id="ins-launches" title="Launches" xs={del.series.map((p) => p.at)} format={num} empty="Nothing was launched in this period."
            series={[{ name: "Launches", values: del.series.map((p) => p.launches), marker: "circle", color: BLUE }, { name: "Failed or returned", values: del.series.map((p) => p.failures), dash: "6 4", marker: "square", color: ORANGE }]} />
          <InsightChart id="ins-lead" title="Lead time and time to restore" xs={del.series.map((p) => p.at)} format={secs} empty="No launch had a merge time in this period."
            series={[{ name: "Lead time (median)", values: del.series.map((p) => (p.lead_time_median_s > 0 ? p.lead_time_median_s : null)), marker: "circle", color: BLUE },
              { name: "Time to restore (median)", values: del.series.map((p) => (p.time_to_restore_median_s > 0 ? p.time_to_restore_median_s : null)), dash: "2 4", marker: "triangle", color: PLUM }]} />
          <p className="rel-hint">Deliveries are not of a branch, so the branch filter does not apply to them.</p>
        </Sheet>
      )}
    </>
  );
}

function Filters({ ask, set, csv }: { ask: InsightsAsk; set: (a: InsightsAsk) => void; csv: string }) {
  const [branch, setBranch] = useState(ask.branch ?? "");
  return (
    <div className="toolbar2">
      <label className="select"><span className="visually-hidden">Period</span>
        <select value={ask.days} onChange={(e) => set({ ...ask, days: Number(e.target.value) })}>
          <option value={7}>Last 7 days</option><option value={30}>Last 30 days</option><option value={90}>Last 90 days</option><option value={365}>Last year</option>
        </select></label>
      <form onSubmit={(e) => { e.preventDefault(); set({ ...ask, branch: branch.trim() || undefined }); }} className="ins-branch">
        <label>Branch <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="every branch" /></label>
        <button type="submit" className="btn">Apply</button>
      </form>
      <a className="btn" href={csv} download>Download as CSV</a>
    </div>
  );
}

/** One Insights page. scope says what it counts; the server limits it to what the person may read. */
export function InsightsView({ base, head }: { base: Pick<InsightsAsk, "flight" | "squadron">; head: React.ReactNode }) {
  const [ask, setAsk] = useState<InsightsAsk>({ ...base, days: 30 });
  const fn = useCallback((b: boolean) => insightsApi.get(ask, b), [ask]);
  const data = useLoad<InsightsAnswer>(fn, 120000);
  return (
    <>
      {head}
      <Filters ask={ask} set={setAsk} csv={insightsApi.csvUrl(ask)} />
      <ErrorBox error={data.error} />
      {data.loading && !data.data && <div className="loading">Loading…</div>}
      {data.data && <Body d={data.data.insights} />}
    </>
  );
}

/** A Flight's Insights. */
export function FlightInsightsPage({ name }: { name: string }) {
  useCrumbLabel("Insights");
  return <div className="page page-wide"><InsightsView base={{ flight: name }} head={<ProjectHead name={name} on="insights" title={<>Insights of {name}</>} sentence="How its runs, steps, tests and deliveries are going, from what Launchway already records." />} /></div>;
}

/** A Squadron's Insights: its Flights you may read. */
export function SquadronInsightsPage({ name }: { name: string }) {
  useCrumbLabel("Insights");
  return <div className="page page-wide"><InsightsView base={{ squadron: name }} head={<PageHead eyebrow={terms().say("{Units}")} title={<>Insights of {name}</>} sentence={terms().say("How the {projects} in it are doing together. Only the {projects} you may read are counted.")} />} /></div>;
}

/** The installation's Insights: every Flight you may read. */
export function InstallationInsightsPage() {
  useCrumbLabel("Insights");
  return <div className="page page-wide"><InsightsView base={{}} head={<PageHead eyebrow="Activity" title="Insights" sentence={terms().say("How delivery is going across every {project} you may read, from what Launchway already records.")} />} /></div>;
}
