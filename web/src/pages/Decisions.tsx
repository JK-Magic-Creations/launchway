import { useCallback, useState } from "react";
import { api, type Approval, type Me, type Project, type RunDetail } from "../api";
import { v2bApi, type ApprovalSettings } from "../api/v2b";
import { useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { ErrorBox } from "../components/ui";
import { ago, clock, duration, refLabel } from "../format";
import { approvalRefusal, commitLink, consequences, succeededBefore, type Refusal } from "../guided/decide";
import { useInstallation } from "../guided/facts";
import { useLoad } from "../guided/useLoad";
import { Button, Decision, EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { Icon } from "../kit/Icon";
import { onLink } from "../router";
import { terms } from "../terms";
import { progressText, whoMayDecide, type ApprovalProgress } from "../api/approvals";
import { useNow, type Approvals } from "../live";

/** The approvals waiting for a decision, each as a card: what is asked, the evidence available today,
 * what happens next, and the decision with a reason. A person who may not decide is told exactly why. */
export function DecisionsPage({ approvals }: { approvals: Approvals }) {
  const t = terms();
  const me = useMe();
  const inst = useInstallation();
  const now = useNow(1000, !!approvals.list?.length);
  const settingsFn = useCallback(() => v2bApi.approvalSettings(), []);
  const settings = useLoad<ApprovalSettings>(settingsFn);
  const list = approvals.list;
  return (
    <div className="page">
      <PageHead eyebrow="Activity" title="Waiting for a decision"
        sentence={list === null ? "Loading." : list.length === 0 ? "Nothing is waiting for a person." : <><b>{list.length} {list.length === 1 ? "run is" : "runs are"} waiting.</b> Each card says what is asked and what you can see before you say {t.label("go")} or {t.label("no_go")}.</>} />
      <ErrorBox error={approvals.error} />
      {list === null && !approvals.error && <div className="loading">Loading…</div>}
      {list && list.length === 0 && (
        <Sheet><EmptyState title="All clear" action={<LinkButton href="/activity" icon="arrow">See the runs</LinkButton>}>
          A run waits here when it reaches a step that needs {t.lower("clearance")}. Nothing is waiting now.
        </EmptyState></Sheet>
      )}
      {list && list.length > 0 && (
        <div className="cardlist">
          {list.map((a) => <ApprovalCard key={a.run_id + "/" + a.step_id} a={a} me={me} project={inst.projects.find((p) => p.name === a.project)} settings={settings.data} now={now} />)}
        </div>
      )}
    </div>
  );
}

export function ApprovalCard({ a, me, project, settings, now }: { a: Approval; me: Me | null; project?: Project; settings: ApprovalSettings | null; now: number }) {
  const runFn = useCallback(() => api.run(a.run_id), [a.run_id]);
  const run = useLoad<RunDetail>(runFn);
  const detail = run.data;
  const step = detail?.step_list.find((s) => s.id === a.step_id);
  const t = terms();
  const refusal = approvalRefusal(a, me, settings);
  const next = consequences(detail, a.step_id);
  const diff = commitLink(project, detail?.sha ?? "");
  const tests = detail?.tests;
  const finds = detail?.findings;
  const serious = finds ? finds.critical + finds.high : 0;
  const before = detail ? succeededBefore(detail, step) : [];
  const left = a.deadline - now;
  const evidence = [
    { key: "tests", label: "Tests", value: tests ? `${tests.passed} of ${tests.total}` : "none", note: tests ? (tests.failed > 0 ? `${tests.failed} failed` : tests.flaky > 0 ? `all passed, ${tests.flaky} flaky` : "all passed") : "no test report in this run" },
    { key: "scan", label: "Findings", value: finds ? finds.total : "none", note: finds ? (serious > 0 ? `${serious} critical or high` : finds.total > 0 ? "none critical or high" : "nothing found") : "no scan in this run" },
    { key: "who", label: "Started by", value: a.started_by || "unknown", note: detail ? (detail.event === "push" ? "a push" : detail.event) : undefined },
    { key: "left", label: "Expires", value: left > 0 ? duration(left) : "now", note: `asked ${ago(a.waiting_since, now)}` },
  ];
  const several = progressText(a as unknown as ApprovalProgress);
  const may = whoMayDecide(a as unknown as ApprovalProgress);
  if (several || may) evidence.push({ key: "several", label: "Decided by", value: several ? several.split(";")[0] : "one person", note: may ? `may decide: ${may}` : undefined });
  return (
    <Decision icon="gate" tone="warn" title={a.message || `Run #${a.run_number} of ${a.project} needs ${t.lower("clearance")}`} evidence={evidence}
      children={`Run #${a.run_number} of ${a.project} stopped at ${a.step_id} and waits for a person.`}
      actions={
        <div className="decide-form">
          <div className="then">
            <div><b>If you say {t.label("go")}</b>{next.approve}</div>
            <div><b>If you say {t.label("no_go")}</b>{next.reject}</div>
          </div>
          <p className="small faint" style={{ margin: 0 }}>
            <EntityLink kind="project" name={a.project} /> · <EntityLink kind="run" name={`run #${a.run_number}`} id={a.run_id} />
            {refLabel(a) ? ` · ${refLabel(a)}` : ""}{a.short_sha ? ` · ${a.short_sha}` : ""}{a.commit_subject ? ` · ${a.commit_subject}` : ""}
            {before.length > 0 ? ` · passed so far: ${before.join(", ")}` : ""}
          </p>
          <div className="row wrap">
            {diff ? <a className="btn btn-sm" href={diff} target="_blank" rel="noreferrer noopener"><Icon name="code" size="s" />See the change in the repository</a>
              : <span className="faint small">{project ? "This repository has no web address, so there is no link to the change." : ""}</span>}
            <a className="btn btn-sm" href={`/runs/${a.run_id}?step=${encodeURIComponent(a.step_id)}`} onClick={onLink}>Open the run</a>
            {left > 0 && <span className="faint small" title={clock(a.deadline)}>expires {clock(a.deadline)}</span>}
          </div>
          <Decide a={a} refusal={refusal} />
        </div>
      } />
  );
}

function Decide({ a, refusal }: { a: Approval; refusal: Refusal | null }) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState("");
  const t = terms();
  const decide = async (approve: boolean) => {
    setBusy(true); setError(null);
    try {
      await (approve ? api.approve : api.reject)(a.run_id, a.step_id, reason.trim());
      setDone(approve ? `${t.label("go")}. The run goes on.` : `${t.label("no_go")}. The run stopped here.`);
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  if (done) return <div className="note" role="status"><Icon name="check" />{done}</div>;
  const noApprove = refusal !== null;
  const noReject = refusal !== null && !refusal.mayReject;
  return (
    <>
      {refusal && (
        <div className="note warn" role="note" data-why="cannot-approve">
          <Icon name="lock" />
          <div>{refusal.text}{refusal.link && <> <a href={refusal.link.href} onClick={onLink}>{refusal.link.label}</a></>}</div>
        </div>
      )}
      {!noReject && (
        <>
          <label htmlFor={`why-${a.run_id}-${a.step_id}`}>Reason <span className="faint">(kept with the decision{noApprove ? "" : `; needed for ${t.lower("no_go")}`})</span></label>
          <textarea id={`why-${a.run_id}-${a.step_id}`} value={reason} maxLength={2000} onChange={(e) => setReason(e.target.value)} placeholder="For example: tests pass and the release notes are ready." />
          <ErrorBox error={error} />
          <div className="acts decide">
            <Button kind="ok" icon="check" disabled={busy || noApprove} onClick={() => decide(true)} title={noApprove ? `You cannot say ${t.label("go")} to this: see the note above` : `${t.label("go")}: ${t.lower("clearance")} is given and the run goes on`}>{t.label("go")}</Button>
            <Button kind="bad" icon="x" disabled={busy || reason.trim() === ""} onClick={() => decide(false)} title={reason.trim() === "" ? `Say why, then ${t.lower("no_go")}` : `${t.label("no_go")}: ${t.lower("clearance")} is refused and the run stops here`}>{t.label("no_go")}</Button>
          </div>
        </>
      )}
    </>
  );
}
