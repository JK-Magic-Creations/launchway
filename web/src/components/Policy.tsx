import { useState } from "react";
import { api, type EvidenceRef, type PolicyDef, type PolicyVerdict, type RuleResult, type RuleVerdict, type Waiver } from "../api";
import { clock } from "../format";
import { ErrorBox, Link, Mono } from "./ui";
import "../policy.css";

/** The words for an evaluation's verdict, and the tone it is drawn in. */
export const VERDICT: Record<PolicyVerdict, { label: string; tone: string }> = {
  pass: { label: "Passed", tone: "ok" },
  warn: { label: "Warning", tone: "warn" },
  block: { label: "Blocked", tone: "bad" },
};

export const RESULT: Record<RuleVerdict, { label: string; tone: string }> = {
  pass: { label: "Pass", tone: "ok" },
  fail: { label: "Fail", tone: "bad" },
  waived: { label: "Waived", tone: "warn" },
};

export function ShieldIcon({ size = 14 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <path d="M8 1.5l5.5 2v4.2c0 3.3-2.3 5.7-5.5 6.8-3.2-1.1-5.5-3.5-5.5-6.8V3.5z" />
    </svg>
  );
}

export function VerdictPill({ verdict }: { verdict: PolicyVerdict }) {
  const v = VERDICT[verdict] ?? { label: verdict, tone: "idle" };
  return <span className={`pill pill-${v.tone}`}><ShieldIcon />{v.label}</span>;
}

export function ResultBadge({ verdict, mode }: { verdict: RuleVerdict | "pass" | "fail" | "waived"; mode?: string }) {
  const r = RESULT[verdict as RuleVerdict] ?? { label: verdict, tone: "idle" };
  // A failure in report mode only warns.
  const tone = verdict === "fail" && mode === "report" ? "warn" : r.tone;
  const label = verdict === "fail" && mode === "report" ? "Fail (report)" : r.label;
  return <span className={`pol-badge pol-${tone}`}>{label}</span>;
}

export function ModeBadge({ mode }: { mode: string }) {
  return <span className={"pol-mode " + (mode === "enforce" ? "pol-mode-enforce" : "pol-mode-report")}
    title={mode === "enforce" ? "A failure stops the step or the run" : "A failure is shown and recorded, and changes nothing"}>{mode}</span>;
}

/** Scope in words: which projects, environments, branches and events. */
export function ScopeText({ p }: { p: PolicyDef }) {
  const s = p.scope ?? {};
  const parts: [string, string[] | undefined][] = [["projects", s.projects], ["environments", s.environments], ["branches", s.branches], ["events", s.events]];
  const set = parts.filter(([, v]) => v && v.length);
  if (!set.length) return <span className="faint">every run</span>;
  return (
    <span className="pol-scope">
      {set.map(([k, v]) => <span key={k} className="pol-scope-item"><span className="faint">{k}</span> {v!.map((x) => <Mono key={x}>{x}</Mono>)}</span>)}
    </span>
  );
}

/** Where a piece of evidence is shown: a run's tab, or a step. */
export function evidenceHref(e: EvidenceRef, currentRun: string): string | null {
  const run = e.run || currentRun;
  if (!run) return null;
  const step = e.step ? encodeURIComponent(e.step) : "";
  switch (e.kind) {
    case "tests": return `/runs/${run}?tab=tests${step ? `&tests_step=${step}` : ""}`;
    case "findings": return `/runs/${run}?tab=security`;
    case "image": return `/runs/${run}?tab=images`;
    case "sbom": return `/runs/${run}?tab=artifacts`;
    case "approval": case "step": return step ? `/runs/${run}?step=${step}` : null;
  }
  return null;
}

const EVIDENCE_WORD: Record<string, string> = { tests: "Tests", findings: "Security", image: "Images", sbom: "Artifacts", approval: "Approval", step: "Step", library: "Library" };

export function EvidenceList({ list, run }: { list: EvidenceRef[]; run: string }) {
  return (
    <ul className="pol-evidence">
      {list.map((e, i) => {
        const href = evidenceHref(e, run);
        const other = e.run && e.run !== run;
        return (
          <li key={i}>
            <span className="pol-ev-kind">{EVIDENCE_WORD[e.kind] ?? e.kind}</span>
            {href ? <Link href={href}>{e.text}</Link> : <span>{e.text}</span>}
            {other && <span className="faint"> (in run #{e.run_number})</span>}
          </li>
        );
      })}
    </ul>
  );
}

/** One rule's result: what was found, what would satisfy it, the evidence,
 * and the waiver that passed it, if one did. */
export function RuleRow({ r, mode, run, onWaive }: { r: RuleResult; mode: string; run: string; onWaive?: () => void }) {
  return (
    <li className={"pol-rule pol-rule-" + r.verdict}>
      <div className="pol-rule-head">
        <ResultBadge verdict={r.verdict} mode={mode} />
        <Mono>{r.rule}</Mono>
        {onWaive && r.verdict === "fail" && r.rule !== "source" && (
          <button type="button" className="btn btn-sm pol-waive-btn" onClick={onWaive}>Waive…</button>
        )}
      </div>
      <div className="pol-found"><span className="faint">Found:</span> {r.found}</div>
      {r.verdict !== "pass" && <div className="pol-want"><span className="faint">Needs:</span> {r.want}</div>}
      {r.waiver && <WaiverNote w={r.waiver} />}
      {r.waivers && r.waivers.length > 0 && <div className="pol-note">Findings waived one by one: {r.waivers.map((id) => <Mono key={id}>{id} </Mono>)}</div>}
      {r.notes?.map((n, i) => <div key={i} className="pol-note">{n}</div>)}
      {r.evidence && r.evidence.length > 0 && (
        <details className="pol-ev-details">
          <summary>Evidence ({r.evidence.length})</summary>
          <EvidenceList list={r.evidence} run={run} />
        </details>
      )}
    </li>
  );
}

export function WaiverNote({ w }: { w: Waiver }) {
  return (
    <div className="pol-waiver-note">
      Waived by <strong>{w.granted_by}</strong> until {clock(w.expires_at)}: “{w.reason}” <Mono>{w.id}</Mono>
    </div>
  );
}

/** waiverScope says what a waiver covers, in words. */
export function waiverScope(w: Waiver): string {
  const parts = [w.project];
  if (w.run) parts.push(w.run_number ? `run #${w.run_number}` : `run ${w.run}`);
  else parts.push("every run");
  if (w.environment) parts.push(`in ${w.environment}`);
  if (w.finding) parts.push(`finding ${w.finding}`);
  return parts.join(", ");
}

const HOURS = [1, 4, 24, 72, 168, 336, 720];

export interface WaiverDraft { project: string; policy: string; rule: string; run?: string; runNumber?: number; environment?: string; deployRule?: boolean; findings?: string[] }

/** A form to waive one rule: a reason and how long are required; it may
 * be for this run only, one environment, or one finding. */
export function WaiverForm({ draft, maxDays, onDone, onCancel }: {
  draft: WaiverDraft; maxDays: number; onDone: (w: Waiver) => void; onCancel: () => void;
}) {
  const [reason, setReason] = useState("");
  const [hours, setHours] = useState(24);
  const [thisRun, setThisRun] = useState(!!draft.run);
  const [envOnly, setEnvOnly] = useState(!!draft.environment);
  const [finding, setFinding] = useState("");
  const [envText, setEnvText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const w = await api.grantWaiver(draft.project, {
        policy: draft.policy, rule: draft.rule, reason: reason.trim(), expires_in_hours: hours,
        run: thisRun ? draft.run : undefined, environment: draft.environment ? (envOnly ? draft.environment : undefined) : (envText.trim() || undefined),
        finding: finding || undefined,
      });
      onDone(w);
    } catch (err) { setError(err); } finally { setBusy(false); }
  };
  return (
    <form className="pol-waiver-form" onSubmit={submit} aria-label={`Waive ${draft.policy} ${draft.rule}`}>
      <div className="pol-waiver-title">Waive <Mono>{draft.policy}</Mono> / <Mono>{draft.rule}</Mono> for <strong>{draft.project}</strong></div>
      <label className="field">
        <span>Reason (recorded, and shown wherever the verdict is)</span>
        <textarea required maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)}
          placeholder="for example: CVE-2026-1234 has no fix yet; the service is not exposed; tracked in SEC-88" />
      </label>
      <div className="pol-waiver-row">
        <label className="field">
          <span>Expires after</span>
          <select value={hours} onChange={(e) => setHours(Number(e.target.value))}>
            {HOURS.filter((h) => h <= maxDays * 24).map((h) => <option key={h} value={h}>{h < 24 ? `${h} hours` : `${h / 24} days`}</option>)}
          </select>
        </label>
        {draft.run && (
          <label className="pol-check"><input type="checkbox" checked={thisRun} onChange={(e) => setThisRun(e.target.checked)} />
            Only run #{draft.runNumber}</label>
        )}
        {draft.environment && (
          <label className="pol-check"><input type="checkbox" checked={envOnly} onChange={(e) => setEnvOnly(e.target.checked)} />
            Only in {draft.environment}</label>
        )}
        {draft.deployRule && !draft.environment && (
          <label className="field">
            <span>Environment (optional; every one if empty)</span>
            <input value={envText} maxLength={63} placeholder="prod" onChange={(e) => setEnvText(e.target.value)} />
          </label>
        )}
        {draft.findings && draft.findings.length > 0 && (
          <label className="field">
            <span>One finding only (optional)</span>
            <select value={finding} onChange={(e) => setFinding(e.target.value)}>
              <option value="">The whole rule</option>
              {draft.findings.map((f) => <option key={f} value={f}>{f}</option>)}
            </select>
          </label>
        )}
      </div>
      {draft.deployRule && <p className="faint small">On a protected environment, a waiver granted by the person who started the run does not count.</p>}
      <ErrorBox error={error} />
      <div className="pol-waiver-actions">
        <button type="submit" className="btn btn-primary" disabled={busy || !reason.trim()}>{busy ? "Waiving…" : "Grant waiver"}</button>
        <button type="button" className="btn" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  );
}

/** A list of waivers, with Revoke for those in force when permitted. */
export function WaiverList({ waivers, mayRevoke, onChange, showProject = true }: {
  waivers: Waiver[]; mayRevoke: (w: Waiver) => boolean; onChange: () => void; showProject?: boolean;
}) {
  const [error, setError] = useState<unknown>(null);
  const revoke = async (w: Waiver) => {
    const reason = window.prompt(`Revoke waiver ${w.id} (${w.policy} / ${w.rule})? Say why (optional):`);
    if (reason === null) return;
    setError(null);
    try { await api.revokeWaiver(w.project, w.id, reason); onChange(); } catch (err) { setError(err); }
  };
  return (
    <>
      <ErrorBox error={error} />
      <ul className="pol-waivers">
        {waivers.map((w) => (
          <li key={w.id} className={"pol-waiver pol-waiver-" + (w.state ?? "active")}>
            <div className="pol-waiver-head">
              <span className={`pol-badge pol-${w.state === "active" ? "warn" : "idle"}`}>{w.state ?? "active"}</span>
              <Mono>{w.policy}</Mono> / <Mono>{w.rule}</Mono>
              <span className="faint">{showProject ? waiverScope(w) : waiverScope(w).split(", ").slice(1).join(", ")}</span>
              {w.state === "active" && mayRevoke(w) && <button type="button" className="btn btn-sm btn-danger-outline pol-revoke" onClick={() => revoke(w)}>Revoke</button>}
            </div>
            <div className="pol-waiver-reason">“{w.reason}”</div>
            <div className="faint small">
              by {w.granted_by} on {clock(w.granted_at)}, {w.state === "expired" ? "expired" : "expires"} {clock(w.expires_at)}
              {w.revoked_at ? ` · revoked by ${w.revoked_by} on ${clock(w.revoked_at)}${w.revoke_reason ? `: ${w.revoke_reason}` : ""}` : ""}
              {" · "}<Mono>{w.id}</Mono>
            </div>
          </li>
        ))}
      </ul>
    </>
  );
}
