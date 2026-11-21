import { useCallback, useMemo } from "react";
import type { EnvironmentView, UnitView } from "../api";
import { rollbackApi, type ReviewItem } from "../apiRollback";
import { EntityLink } from "../components/EntityLink";
import { NeedsReview } from "../components/RollbackRun";
import { useLoad } from "../guided/useLoad";
import { WizardButton, WizardEmpty } from "../components/WizardButton";
import { ago } from "../format";
import { diagnose, type Diagnosis } from "../guided/diagnose";
import { useInstallation } from "../guided/facts";
import { Status, type State } from "../kit/Status";
import { Band, EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { useNow } from "../live";
import { terms } from "../terms";

const STATE: Record<string, State> = { bad: "bad", wait: "wait", run: "run", ok: "ok", quiet: "skip" };

interface Row { env: EnvironmentView; unit: UnitView; dg: Diagnosis }

/** Deployments overview: problems first, then each environment with what runs in it. All of it comes from what
 * deploy runners observed; nothing is shown that was not seen. */
export function DeploymentsOverviewPage() {
  const inst = useInstallation();
  const now = useNow(5000);
  const reviewFn = useCallback((bg: boolean) => rollbackApi.reviews(bg), []);
  const reviews = useLoad<ReviewItem[]>(reviewFn, 15000);
  const rows = useMemo<Row[]>(() => inst.environments.flatMap((env) => env.services.map((unit) => ({ env, unit, dg: diagnose(unit.last, unit.live, undefined, now) }))), [inst.environments, now]);
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  const trouble = rows.filter((r) => r.dg.tone === "bad" || r.dg.tone === "wait").sort((a, b) => (a.dg.tone === "bad" ? 0 : 1) - (b.dg.tone === "bad" ? 0 : 1));
  const moving = rows.filter((r) => r.dg.tone === "run").length;
  const envs = inst.environments;
  const sentence = envs.length === 0 ? terms().say("No {environment} exists yet, so nothing can be {deploy:did}.")
    : rows.length === 0 ? terms().say("{Environments} are set up, but nothing has been {deploy:did} to them yet.")
    : trouble.length === 0 ? terms().say("Everything Launchway {deploy:did} is running as expected.") : <><b>{trouble.length} {trouble.length === 1 ? "service needs" : "services need"} a look.</b> The rest is fine.</>;
  return (
    <div className="page">
      <PageHead eyebrow={terms().say("{Deploys}")} title="What is running, and where" sentence={sentence}
        actions={envs.length > 0 ? <WizardButton wizard="add-environment" label={terms().say("Add an {environment}")} /> : undefined} />
      {envs.length === 0 ? (
        <WizardEmpty title={terms().say("No {environments} yet")} wizard="add-environment" label={terms().say("Add an {environment}")}
          text={terms().say("An {environment} is a named place your software goes, like staging or production. A pipeline says {deploy:do} to staging; the {environment} says where that is. It takes one minute and {deploy:does} nothing.")} />
      ) : (
        <>
          <Band cells={[
            { key: "env", label: terms().say("{Environments}"), value: envs.length, foot: envs.filter((e) => e.protected).length ? `${envs.filter((e) => e.protected).length} protected` : "none protected", href: "/deployments/environments" },
            { key: "app", label: "Applications seen", value: inst.applications.length, foot: inst.applications.length ? terms().say("by a {deploy:do} {runner}") : "none yet", href: "/deployments/applications" },
            { key: "look", label: "Need a look", value: trouble.length, foot: trouble.length ? "failed or waiting" : "none", loud: trouble.some((r) => r.dg.tone === "bad") ? "bad" : trouble.length ? "warn" : undefined },
            { key: "mov", label: "Moving now", value: moving, foot: moving ? terms().say("being {deploy:did}") : "quiet" },
          ]} />
          <NeedsReview items={reviews.data} onChanged={reviews.reload} />
          {trouble.length > 0 && (
            <Sheet title="Needs a look">
              {trouble.map((r) => {
                const a = r.unit.last?.application;
                return (
                  <div key={r.env.name + r.unit.project + r.unit.service} className={`need ${r.dg.tone === "bad" ? "bad" : "warn"}`}>
                    <div className={`ico-tile ${r.dg.tone === "bad" ? "bad" : "warn"}`}><Status state={STATE[r.dg.tone]} /></div>
                    <div className="tx">
                      <div className="ti">{r.dg.headline}</div>
                      <div className="ds">{r.unit.service} of <EntityLink kind="project" name={r.unit.project} /> in <EntityLink kind="environment" name={r.env.name} />{r.dg.detail ? `. ${r.dg.detail}` : ""}</div>
                    </div>
                    {a && <div className="acts"><LinkButton href={`/deployments/applications/${encodeURIComponent(a)}`} sm kind={r.dg.tone === "bad" ? "pri" : undefined}>Why?</LinkButton></div>}
                  </div>
                );
              })}
            </Sheet>
          )}
          <Sheet title={terms().say("{Environments}")} actions={<LinkButton href="/deployments/environments" sm>{terms().say("All {environments}")}</LinkButton>}>
            <ul className="rowlist">
              {envs.map((e) => (
                <li key={e.name}>
                  <div className="main">
                    <div className="nm"><EntityLink kind="environment" name={e.name} />{e.protected && <> <span className="vchip warn">protected</span></>}</div>
                    <div className="sb">{e.services.length === 0 ? terms().say("Nothing {deploy:did} yet") : e.services.map((u) => u.service + (u.current ? ` ${u.current.version}` : "")).join(", ")}</div>
                  </div>
                  <div className="cell">
                    <Status state={e.observer && (!e.observer_expires || e.observer_expires > now) ? "ok" : "warn"} />
                    {e.observer && (!e.observer_expires || e.observer_expires > now) ? `watched by ${e.observer}` : terms().say(`no {deploy:do} {runner} watches ${e.target}`)}
                  </div>
                  <div className="cell faint">{e.services.reduce((t, u) => Math.max(t, u.current?.since ?? 0), 0) ? `last change ${ago(e.services.reduce((t, u) => Math.max(t, u.current?.since ?? 0), 0), now)}` : ""}</div>
                </li>
              ))}
            </ul>
          </Sheet>
          {rows.length === 0 && <Sheet><EmptyState title={terms().say("Nothing has been {deploy:did} yet")}>{terms().say("A {run} {deploy:does} to an {environment} when it reaches a deploy step. The applications and their diagnoses will appear here once a {deploy:do} {runner} has looked at one.")}</EmptyState></Sheet>}
        </>
      )}
    </div>
  );
}
