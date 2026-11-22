import { useEffect, useState } from "react";
import { ProjectHead } from "./PipelineChrome";
import { debriefApi, type FlakyList, type FlakyTest } from "../debriefApi";
import { ago } from "../format";
import { Empty, ErrorBox, Link } from "../components/ui";
import { CopyBlock, TestIcon } from "../components/Tests";
import "../debrief.css";
import { DivLoading, SpanHidden } from "../kit/bits";

// The flaky tests of a project: tests whose history says they pass and fail
// without a change that explains it, with how sure that is, where, since
// when, and an entry to copy into the repository's quarantine file.

export function ProjectFlaky({ name }: { name: string }) {
  const [data, setData] = useState<FlakyList | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    debriefApi.flaky(name).then((d) => { if (alive) setData(d); }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [name]);
  const enc = encodeURIComponent(name);
  return (
    <div className="page page-wide">
      <ProjectHead name={name} on="tests" title={<>Flaky tests of {name}</>}
        sentence="Tests that passed and failed without a change that explains it. A flaky test hides real failures, so it is worth fixing or quarantining."
        actions={<Link href={`/projects/${enc}/tests`}>Back to the results</Link>} />
      <ErrorBox error={error} />
      {!data && !error && <DivLoading>Loading…</DivLoading>}
      {data && (
        <>
          <p className="flakyrules">{data.rules}</p>
          {data.tests.length === 0 ? (
            <Empty title="No flaky tests found">
              Looked at the last {data.window} runs; {data.considered} {data.considered === 1 ? "test" : "tests"} both passed and failed there, and none of them looks flaky by those rules.
            </Empty>
          ) : (
            <section aria-label="Flaky tests" className="flakylist">
              <h2 className="visually-hidden">Flaky tests, the most flaky first</h2>
              {data.tests.map((t) => <FlakyRow key={t.key} t={t} project={name} />)}
              <p className="sub small">To quarantine a test, add the entry to <code>.launchway/quarantine.yml</code> in the repository, in a pull request. Launchway never writes that file; a quarantine is a change to review. An entry ends on its expiry date and the test counts again.</p>
            </section>
          )}
        </>
      )}
    </div>
  );
}

function FlakyRow({ t, project }: { t: FlakyTest; project: string }) {
  return (
    <article className="flakyrow" aria-label={t.name}>
      <div className="flakyrow-head">
        <span className="flakyscore" title="The share of the commits it ran on that showed flakiness">{t.score}<SpanHidden> flake score</SpanHidden></span>
        <div>
          <strong>{t.name}</strong>
          {t.suite && <div className="sub small">{t.suite}</div>}
        </div>
        <span className="sub small">{t.reason}</span>
      </div>
      <ol className="flakyhist" aria-label={`Latest results, oldest first: ${t.history.join(", ")}`}>
        {t.history.map((h, i) => (
          <li key={i} className={"h-" + h} title={h === "retried" ? "passed on a retry" : h}>
            <TestIcon status={h === "failed" ? "failed" : h === "skipped" ? "skipped" : "passed"} size={13} />
            <SpanHidden>{h === "retried" ? "passed on retry" : h}</SpanHidden>
          </li>
        ))}
      </ol>
      <ul className="dfacts">
        <li>On <strong>{t.commits}</strong> commits, <strong>{t.same_commit}</strong> with both a pass and a fail</li>
        <li>Branches <strong>{t.branches.join(", ") || "—"}</strong></li>
        <li>First seen <strong>{t.first_seen ? ago(t.first_seen) : "—"}</strong></li>
        <li><Link href={`/projects/${encodeURIComponent(project)}/tests?test=${t.key}`}>History of this test</Link></li>
      </ul>
      <details className="dmore"><summary>Suggested quarantine entry</summary>
        <CopyBlock label="Quarantine entry" text={t.suggest} />
      </details>
    </article>
  );
}
