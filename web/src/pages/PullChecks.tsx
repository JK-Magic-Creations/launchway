import { Delta } from "../components/Coverage";
import { VerdictPill } from "../components/Policy";
import { SeverityBar } from "../components/Security";
import { Link } from "../components/ui";
import type { PullChecks as Data } from "../pullsApi";

/** What each pipeline said about the head commit, drawn with the run page's own parts: the Delta of the coverage tab,
 * the severity bar of the security tab and the policy tab's verdict pill. The sentences come from the server in the
 * same words as the run page; a pipeline of a project the viewer may not read shows only its sentence. */
export default function PullChecks({ data }: { data: Data }) {
  if (!data.checks?.length) return null;
  return (
    <section className="pr-runline" aria-label="Results for the head commit">
      {data.checks.map((c) => {
        const t = c.tests, f = c.findings, cv = c.coverage;
        return (
          <div key={c.run_id}>
            <strong>{c.pipeline}</strong> <Link href={`/runs/${c.run_id}`}>#{c.number}</Link> <span className="faint">{c.sentence}</span>
            {c.detail && (
              <ul className="small">
                {t && <li>{t.totals.passed} of {t.totals.total} tests passed{t.totals.failed + t.totals.errored > 0 && `, ${t.totals.failed + t.totals.errored} failed`}{t.compared && `; ${t.new_failure_count} newly failing against ${data.target_branch}`}{t.new_failures.length > 0 && ` (${t.new_failures.join(", ")})`}</li>}
                {cv && <li>Coverage {cv.lines_percent}% <Delta now={cv.lines_percent} before={cv.change === undefined ? null : cv.lines_percent - cv.change} label={data.target_branch} /></li>}
                {f && <li>{f.counts.total} findings, {f.new_total} new in this change<SeverityBar c={f.counts} thin /></li>}
                {c.policy_verdict && <li><VerdictPill verdict={c.policy_verdict} /></li>}
              </ul>
            )}
          </div>
        );
      })}
      {data.deployments?.map((d) => <p key={d.environment + d.version} className="small">Deployed to {d.environment}: {d.address && /^https?:\/\//.test(d.address) ? <a href={d.address}>{d.version}</a> : d.version} ({d.phase})</p>)}
    </section>
  );
}
