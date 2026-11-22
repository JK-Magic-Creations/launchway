import { useState } from "react";
import { ApiError } from "../../api";
import { configApi, type Spec } from "../../configApi";
import { LinkButton } from "../../kit/layout";
import { actionOf, type ChecklistItem, type GuideStep } from "../../guideApi";
import { Unavailable, Wingman } from "./Frame";
import { Link } from "../../components/ui";
import { ADD_FLIGHT } from "../squadrons/shared";
import { terms } from "../../terms";
import { prose } from "../../prose";
const P = await prose("pages.guide.OtherSteps");

/** Step 5: is a runner connected, and how to add another. */
export function RunnerStep({ step }: { step: GuideStep }) {
  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: terms().say("Learn more about {runners}"), slug: "guide/runner-identity" }}>
        {terms().say(P.t0)} {step.state === "done" ? P.t1 : P.t2}
      </Wingman>
      <div className="gd-choices">
        <LinkButton href="/config/wizard/add-runner" kind={step.state === "done" ? undefined : "pri"}>{terms().say("Add a {runner}")}</LinkButton>
        <LinkButton href="/settings/runners">{terms().say("See the {runners}")}</LinkButton>
      </div>
      <p className="faint">{terms().say(P.t3)} <code>{P.t4}</code> {terms().say(P.t5)}</p>
    </>
  );
}

/** Step 6: turn on Git hosting. It is read when the server starts, so a restart is said plainly. */
export function HostingStep({ step, kubernetes, onChange }: { step: GuideStep; kubernetes: boolean; onChange: () => void }) {
  const on = actionOf(step, "turn_on_hosting");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [saved, setSaved] = useState(false);
  const turnOn = async () => {
    setBusy(true); setErr("");
    try {
      const o = await configApi.get("installation", "installation");
      const spec: Spec = { ...o.spec, forge: { ...((o.spec as { forge?: object }).forge ?? {}), hosting: true } } as Spec;
      await configApi.apply([{ kind: "installation", name: "installation", spec, version: o.version }]);
      setSaved(true); onChange();
    } catch (e) { setErr(e instanceof ApiError ? e.message : P.t6); } finally { setBusy(false); }
  };
  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: "Learn more about Git over SSH", slug: "guide/git-over-ssh" }}>
        {P.t7}<code>forge.ssh.listen</code>) and your people's SSH keys; <code>launchway doctor ssh</code> checks it afterwards.
      </Wingman>
      {on?.available && !saved && <div><button type="button" className="btn btn-primary" onClick={() => void turnOn()} disabled={busy}>{busy ? "Saving…" : "Turn on Git hosting"}</button></div>}
      {step.state !== "done" && <Unavailable action={on} />}
      {saved && (
        <div className="callout callout-info" role="status">
          {P.t8}{kubernetes ? <>{P.t9}<code>{P.t10}</code>.</> : <>Restart the service (<code>systemctl restart launchway</code>) or start the program again.</>} Then come back to this step.
        </div>
      )}
      {err && <div className="callout callout-bad" role="alert">{err}</div>}
    </>
  );
}

/** Step 8: a first pipeline from a starter for one repository: Add a Flight, which finds the repository (a hosted one first), names the Flight and shows the pipeline before anything is made. */
export function PipelineStep({ step }: { step: GuideStep }) {
  const start = actionOf(step, "start_wizard");
  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: "Learn more about pipelines", slug: "guide/pipelines" }}>
        {P.t11}</Wingman>
      {!start?.available && <Unavailable action={start} />}
      {start?.available && <p><LinkButton href={ADD_FLIGHT} kind="pri" icon="plus">{terms().say("Add a {project}")}</LinkButton></p>}
    </>
  );
}

/** Step 9: the security checklist and what to do next. */
export function FinishStep({ items }: { items: ChecklistItem[] }) {
  const attention = items.filter((i) => i.state === "attention").length;
  const mark = { ok: "ok", attention: "needs attention", unknown: "not known" } as const;
  return (
    <>
      <p>{attention === 0 ? P.t12 : `${attention} ${attention === 1 ? "thing needs" : "things need"} your attention.`}</p>
      <Wingman learn={{ label: "Read the hardening checklist", slug: "security/hardening" }}>
        {P.t13}<code>launchway doctor security</code> {P.t14}</Wingman>
      <ul className="gd-check">
        {items.map((i) => (
          <li key={i.id} data-state={i.state}>
            <div><strong>{i.title}</strong> <span className={"gd-badge gd-" + i.state}>{mark[i.state]}</span></div>
            <div className="faint">{i.detail}</div>
          </li>
        ))}
      </ul>
      <h3 className="h-sm">What to do next</h3>
      <ul className="cf-next">
        <li><Link href="/settings/access">{P.t15}</Link></li>
        <li><Link href="/deployments/environments">{terms().say(P.t16)}</Link></li>
        <li><Link href="/settings/rules">{P.t17}</Link></li>
        <li><Link href={P.t18}>{P.t19}</Link></li>
      </ul>
    </>
  );
}

