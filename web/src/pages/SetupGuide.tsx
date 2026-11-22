import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api";
import { Button, PageHead, Sheet } from "../kit/layout";
import { Icon } from "../kit/Icon";
import { ErrorBox } from "../components/ui";
import { Link } from "../components/ui";
import { guideApi, loadSkipped, saveSkipped, type SetupGuide } from "../guideApi";
import { SignInStep } from "./guide/SignInStep";
import { EncryptionStep } from "./guide/EncryptionStep";
import { ImportStep } from "./guide/ImportStep";
import { FinishStep, HostingStep, PipelineStep, RunnerStep } from "./guide/OtherSteps";
import "./guide.css";
import { DivLoading, DivPage, PSub } from "../kit/bits";

// The first-run guide: what comes after the first administrator. Each step can be skipped and come back to; each asks the
// server what can be done now, so a step that cannot work says why instead of offering a button that fails.

interface Entry { id: string; title: string; blurb: string }
const STEPS: Entry[] = [
  { id: "sign-in", title: "How people sign in", blurb: "Keep built-in accounts, or connect your identity provider and test it first." },
  { id: "encryption", title: "Encryption and the root key", blurb: "What is protected, and a recovery copy of the key." },
  { id: "runner", title: "A runner", blurb: "The machine that runs your steps." },
  { id: "git-hosting", title: "Git hosting", blurb: "Let Launchway keep your repositories." },
  { id: "import", title: "Bring in your repositories", blurb: "Copy them from another Git server and prove they match." },
  { id: "pipeline", title: "Your first pipeline", blurb: "From a starter, for one repository." },
  { id: "finish", title: "Finish", blurb: "The security checklist, and what to do next." },
];

/** The setup guide page. */
export function SetupGuidePage() {
  const [g, setG] = useState<SetupGuide | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [skipped, setSkipped] = useState<string[]>(loadSkipped);
  const [at, setAt] = useState<string>(() => new URLSearchParams(location.search).get("step") ?? "sign-in");
  const reload = useCallback(() => { guideApi.get().then(setG).catch((e) => setError(e)); }, []);
  useEffect(reload, [reload]);
  useEffect(() => { document.title = "Setup guide · Launchway"; }, []);

  const go = (id: string) => { setAt(id); history.replaceState(null, "", `/setup/guide?step=${id}`); window.scrollTo?.(0, 0); };
  const skip = (id: string) => { const n = skipped.includes(id) ? skipped : [...skipped, id]; setSkipped(n); saveSkipped(n); next(id); };
  const next = (id: string) => { const i = STEPS.findIndex((s) => s.id === id); if (i < STEPS.length - 1) go(STEPS[i + 1].id); };
  const unskip = (id: string) => { const n = skipped.filter((x) => x !== id); setSkipped(n); saveSkipped(n); };

  if (error) return <DivPage>{error instanceof ApiError && error.status === 403 ? <ErrorBox error={new Error("Only administrators use the setup guide.")} /> : <ErrorBox error={error} />}</DivPage>;
  if (!g) return <DivPage><DivLoading>Loading…</DivLoading></DivPage>;
  const server = (id: string) => g.steps.find((s) => s.id === id);
  const state = (id: string): "done" | "skipped" | "todo" | "unavailable" => {
    if (id === "finish") return g.checklist.some((c) => c.state === "attention") ? "todo" : "done";
    const s = server(id);
    if (s?.state === "done") return "done";
    if (s?.state === "unavailable") return "unavailable";
    return skipped.includes(id) ? "skipped" : "todo";
  };
  const doneCount = STEPS.filter((s) => state(s.id) === "done").length;
  const cur = STEPS.find((s) => s.id === at) ?? STEPS[0];
  const step = server(cur.id);

  return (
    <DivPage>
      <PageHead eyebrow="Setup" title="Set up your installation" sentence={`${doneCount} of ${STEPS.length} steps are done. Skip any step and come back to it; nothing here is needed to run a pipeline.`}
        actions={<Link href="/config/wizard/first-run">Name your organization and choose the words</Link>} />
      <div className="gd-layout">
        <nav aria-label="Setup steps" className="gd-steps">
          <ol>
            {STEPS.map((s, i) => {
              const st = state(s.id);
              return (
                <li key={s.id}>
                  <button type="button" className="gd-step" aria-current={s.id === cur.id ? "step" : undefined} onClick={() => go(s.id)} data-state={st}>
                    <span className="gd-num" aria-hidden="true">{st === "done" ? <Icon name="check" size="s" /> : i + 1}</span>
                    <span className="gd-title">{s.title}</span>
                    <span className="gd-state">{{ done: "done", skipped: "skipped", todo: "to do", unavailable: "not available yet" }[st]}</span>
                  </button>
                </li>
              );
            })}
          </ol>
        </nav>
        <Sheet title={cur.title} pad className="gd-panel">
          <PSub>{cur.blurb}</PSub>
          {cur.id === "sign-in" && step && <SignInStep step={step} onChange={() => { reload(); next("sign-in"); }} />}
          {cur.id === "encryption" && step && <EncryptionStep step={step} onChange={reload} />}
          {cur.id === "runner" && step && <RunnerStep step={step} />}
          {cur.id === "git-hosting" && step && <HostingStep step={step} kubernetes={g.kubernetes} onChange={reload} />}
          {cur.id === "import" && step && <ImportStep step={step} onChange={reload} />}
          {cur.id === "pipeline" && step && <PipelineStep step={step} />}
          {cur.id === "finish" && <FinishStep items={g.checklist} />}
          {cur.id !== "finish" && (
            <div className="gd-foot">
              {state(cur.id) === "skipped"
                ? <Button onClick={() => unskip(cur.id)}>Take this step up again</Button>
                : state(cur.id) !== "done" && <Button onClick={() => skip(cur.id)}>Skip for now</Button>}
              <Button kind={state(cur.id) === "done" ? "pri" : undefined} onClick={() => next(cur.id)}>Next step</Button>
            </div>
          )}
        </Sheet>
      </div>
    </DivPage>
  );
}
