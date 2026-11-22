import { useState } from "react";
import { ApiError } from "../../api";
import { configApi, type Spec } from "../../configApi";
import { LinkButton } from "../../kit/layout";
import { actionOf, type ChecklistItem, type GuideStep } from "../../guideApi";
import { Unavailable, Wingman } from "./Frame";
import { useOverview } from "../../configApi";
import { ConnectRepoWizard } from "../wizards/ConnectRepo";
import { Empty, Link } from "../../components/ui";
import { terms } from "../../terms";

/** Step 5: is a runner connected, and how to add another. */
export function RunnerStep({ step }: { step: GuideStep }) {
  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: terms().say("Learn more about {runners}"), slug: "guide/runner-identity" }}>
        {terms().say("A {runner} is the machine that runs the steps of your pipelines.")} {step.state === "done" ? "One is already connected, which is all you need to start; add more for capacity or for other kinds of machine." : "Until one is connected, no pipeline step can run."}
      </Wingman>
      <div className="gd-choices">
        <LinkButton href="/config/wizard/add-runner" kind={step.state === "done" ? undefined : "pri"}>{terms().say("Add a {runner}")}</LinkButton>
        <LinkButton href="/settings/runners">{terms().say("See the {runners}")}</LinkButton>
      </div>
      <p className="faint">{terms().say("With the Helm chart, {runners} come with the installation; on another machine,")} <code>launchway runner --server ADDRESS --join TOKEN</code> {terms().say("joins one with a short-lived join token made under {Runners}.")}</p>
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
    } catch (e) { setErr(e instanceof ApiError ? e.message : "Launchway could not be reached. Try again shortly."); } finally { setBusy(false); }
  };
  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: "Learn more about Git over SSH", slug: "guide/git-over-ssh" }}>
        With Git hosting on, Launchway keeps your repositories itself: pushing, pull requests and mirrors. Git over HTTPS needs nothing more. Git over SSH needs a port for it
        (<code>forge.ssh.listen</code>) and your people's SSH keys; <code>launchway doctor ssh</code> checks it afterwards.
      </Wingman>
      {on?.available && !saved && <div><button type="button" className="btn btn-primary" onClick={() => void turnOn()} disabled={busy}>{busy ? "Saving…" : "Turn on Git hosting"}</button></div>}
      {step.state !== "done" && <Unavailable action={on} />}
      {saved && (
        <div className="callout callout-info" role="status">
          Saved. A restart is needed: this server reads the setting only when it starts. {kubernetes ? <>In the Launchway namespace, roll the pods with <code>kubectl rollout restart deploy -l app.kubernetes.io/component=server</code>.</> : <>Restart the service (<code>systemctl restart launchway</code>) or start the program again.</>} Then come back to this step.
        </div>
      )}
      {err && <div className="callout callout-bad" role="alert">{err}</div>}
    </>
  );
}

/** Step 8: a first pipeline from a starter for one repository: the existing connect-a-repository wizard, inside the guide. */
export function PipelineStep({ step }: { step: GuideStep }) {
  const { overview } = useOverview();
  const start = actionOf(step, "start_wizard");
  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: "Learn more about pipelines", slug: "guide/pipelines" }}>
        Pick one repository (for example one you just imported; its address is on its page under Repositories) and a starter. Launchway shows the pipeline before anything is created, and offers it as a file
        to commit; it never changes your application behind your back.
      </Wingman>
      {!start?.available && <Unavailable action={start} />}
      {start?.available && overview && overview.available && <ConnectRepoWizard overview={overview} />}
      {start?.available && !overview && <p className="loading">Loading…</p>}
      {start?.available && overview && !overview.available && <Empty title="This server keeps its configuration in its file">{terms().say("Add the {project} to the settings file.")}</Empty>}
    </>
  );
}

/** Step 9: the security checklist and what to do next. */
export function FinishStep({ items }: { items: ChecklistItem[] }) {
  const attention = items.filter((i) => i.state === "attention").length;
  const mark = { ok: "ok", attention: "needs attention", unknown: "not known" } as const;
  return (
    <>
      <p>{attention === 0 ? "Everything this check can see is in order." : `${attention} ${attention === 1 ? "thing needs" : "things need"} your attention.`}</p>
      <Wingman learn={{ label: "Read the hardening checklist", slug: "security/hardening" }}>
        This is the same plain checklist <code>launchway doctor security</code> prints. It looks at sign-in, encryption in transit and at rest and who can reach you. It changes nothing.
      </Wingman>
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
        <li><Link href="/settings/access">Invite the people who will use it</Link></li>
        <li><Link href="/deployments/environments">{terms().say("Add an {environment} to {deploy:do} to")}</Link></li>
        <li><Link href="/settings/rules">Choose the rules every pipeline is held to</Link></li>
        <li><Link href="/learn/howto/install-in-ten-minutes">Read what to do after the first sign-in</Link></li>
      </ul>
    </>
  );
}

