import { Chip } from "../kit/Status";
import { ErrorPanel, useLoad, when } from "./common";
import { workApi, type AgentReview } from "./workApi";
import { SpanFaintSmall, ThCol, ThRow } from "../kit/bits";

// What agents said about a pull request, as structured verdicts: each criterion with its evidence, the risks and how sure the
// agent was. A review that does not count as an approval is marked advice, with the reason.

const DECISION: Record<string, string> = { approve: "Approves", request_changes: "Asks for changes", comment: "Comments" };

export default function AgentReviews({ repo, pull }: { repo: string; pull: number | string }) {
  const r = useLoad<AgentReview[]>(`pr-reviews|${repo}|${pull}`, () => workApi.pullReviews(repo, pull));
  if (r.error) return <ErrorPanel error={r.error} onRetry={r.reload} />;
  if (!r.data || r.data.length === 0) return null;
  return (
    <section className="sheet wk-reviews" aria-labelledby="agent-reviews-h">
      <div className="sh-h"><h2 id="agent-reviews-h">Agent reviews</h2></div>
      <ul className="wk-review-list">
        {r.data.map((rv) => <Review key={rv.id} rv={rv} />)}
      </ul>
    </section>
  );
}

function Review({ rv }: { rv: AgentReview }) {
  const v = rv.verdict;
  return (
    <li className="wk-review">
      <div className="wk-chips">
        <strong>{rv.agent}</strong>
        <Chip tone={v.decision === "approve" ? "ok" : v.decision === "request_changes" ? "bad" : undefined}>{DECISION[v.decision] ?? v.decision}</Chip>
        {rv.counts_as_approval ? <Chip tone="ok">Counts as an approval</Chip> : <Chip tone="warn">Advice only</Chip>}
        <SpanFaintSmall>{when(rv.at)}</SpanFaintSmall>
      </div>
      {!rv.counts_as_approval && <p className="small">This review does not count toward the approvals a merge needs{rv.advice ? `: ${rv.advice}` : "."}</p>}
      {v.summary && <p>{v.summary}</p>}
      {v.criteria.length > 0 && (
        <table className="table wk-table">
          <caption className="visually-hidden">Criteria checked by {rv.agent}</caption>
          <thead><tr><ThCol>Criterion</ThCol><ThCol>Met</ThCol><ThCol>Evidence</ThCol></tr></thead>
          <tbody>{v.criteria.map((c, i) => <tr key={i}><ThRow>{c.criterion}</ThRow><td>{c.met ? "Met" : "Not met"}</td><td>{c.evidence}</td></tr>)}</tbody>
        </table>
      )}
      {v.risks.length > 0 && <><h3 className="h-sm">Risks it named</h3><ul>{v.risks.map((x, i) => <li key={i}>{x}</li>)}</ul></>}
      <p className="small faint">Confidence {Math.round(v.confidence * 100)}%. {v.scope_ok ? "The change stays within its item." : "The change goes beyond its item."}</p>
    </li>
  );
}
