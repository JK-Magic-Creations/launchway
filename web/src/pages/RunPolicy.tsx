import { useCallback, useEffect, useState } from "react";
import { api, type PolicyDecision, type RunDetail, type RunPolicy } from "../api";
import { clock } from "../format";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { ModeBadge, ResultBadge, RuleRow, VerdictPill, WaiverForm, WaiverList, type WaiverDraft } from "../components/Policy";

const POINT_WORDS: Record<string, string> = { run: "When the run started", deploy: "Before deploying", image: "Before pushing an image" };

function pointTitle(d: PolicyDecision): string {
  if (d.point === "run") return POINT_WORDS.run;
  if (d.point === "image") return `${POINT_WORDS.image}: ${d.step}`;
  const verb = d.action && d.action !== "deploy" ? `Before ${d.action === "promote-full" ? "promoting fully" : d.action === "retry" ? "retrying" : "promoting"}` : POINT_WORDS.deploy;
  return `${verb}: ${d.step}${d.environment ? ` to ${d.environment}` : ""}`;
}

// PolicyTab shows each policy decision on the run: when it was made, every
// policy that judged it and each rule's result with its evidence. A person
// who may waive can waive a failing rule from here; the run's waivers are
// listed with Revoke.
export function PolicyTab({ run, version }: { run: RunDetail; version: string }) {
  const [data, setData] = useState<RunPolicy | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [draft, setDraft] = useState<WaiverDraft | null>(null);
  const load = useCallback((background = false) => {
    api.runPolicy(run.id, background).then((d) => { setData(d); setError(null); }).catch(setError);
  }, [run.id]);
  useEffect(() => { load(true); }, [load, version]);

  if (error) return <ErrorBox error={error} />;
  if (!data) return <div className="loading">Loading…</div>;
  const decisions = [...data.decisions].reverse();
  const blocked = decisions.find((d) => d.verdict === "block");
  return (
    <div className="pol-tab">
      {!decisions.length && <Empty title="No policy judged this run">No policy in force governs this project, branch and event.</Empty>}
      {blocked && run.status === "failed" && (
        <div className="callout callout-bad">
          <strong>Blocked by policy.</strong> Change what was found, or have someone who may grant a waiver do so, then resume the run
          {blocked.point === "deploy" && blocked.step ? <> (or re-run it from <Mono>{blocked.step}</Mono>)</> : null}.
          {!data.can_waive && data.waive_refusal && <> You cannot waive: {data.waive_refusal}.</>}
        </div>
      )}
      {draft && (
        <section className="card">
          <WaiverForm draft={draft} maxDays={data.max_waiver_days || 30} onCancel={() => setDraft(null)} onDone={() => { setDraft(null); load(); }} />
        </section>
      )}
      {decisions.map((d) => (
        <section key={d.seq} className="card pol-decision" aria-label={pointTitle(d)}>
          <div className="pol-decision-head">
            <div>
              <h3 className="h-sm">{pointTitle(d)}</h3>
              <div className="faint small">{clock(d.at)} · decision {d.seq}{d.input.protected ? " · protected environment" : ""}
                {d.input.starter ? ` · started by ${d.input.starter}` : ""}</div>
            </div>
            <VerdictPill verdict={d.verdict} />
          </div>
          {(!d.policies || d.policies.length === 0) && (
            <p className="faint">Governed by policy, but no policy has rules at this point{d.point === "run" ? "; their rules are judged later (before deploying or pushing)" : ""}.</p>
          )}
          {(d.policies ?? []).map((p) => (
            <div key={p.policy.name} className="pol-policy">
              <div className="pol-policy-head">
                <ResultBadge verdict={p.verdict} mode={p.policy.mode} />
                <strong>{p.policy.name}</strong>
                <ModeBadge mode={p.policy.mode} />
                <span className="faint small">{p.policy.source}{p.policy.version ? ` @ ${p.policy.version.replace(/^(.{12}).*$/, "$1")}` : ""}</span>
              </div>
              {p.policy.description && <p className="sub small">{p.policy.description}</p>}
              <ul className="pol-rules">
                {p.rules.map((r) => (
                  <RuleRow key={r.rule} r={r} mode={p.policy.mode} run={run.id}
                    onWaive={data.can_waive ? () => setDraft({
                      project: run.project, policy: p.policy.name, rule: r.rule, run: run.id, runNumber: run.number,
                      environment: d.point === "deploy" ? d.environment : undefined, deployRule: d.point === "deploy",
                      findings: r.rule === "findings" ? (d.input.evidence?.findings ?? []).map((f) => f.fingerprint) : undefined,
                    }) : undefined} />
                ))}
              </ul>
            </div>
          ))}
        </section>
      ))}
      <section className="card">
        <h3 className="h-sm">Waivers for this run</h3>
        {data.waivers.length
          ? <WaiverList waivers={data.waivers} mayRevoke={() => data.can_waive} onChange={() => load()} showProject={false} />
          : <p className="faint">None. Waivers of {run.project}'s rules for every run, or for this one, are listed here. See also the <Link href="/policy">Policy</Link> page.</p>}
      </section>
    </div>
  );
}
