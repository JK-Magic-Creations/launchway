import { useMemo, useState } from "react";
import { EntityLink } from "../components/EntityLink";
import { ago } from "../format";
import { diagnose } from "../guided/diagnose";
import { needsLook, plainHealth, plainSync } from "../guided/derive";
import { useInstallation } from "../guided/facts";
import { Chip, Status, type State, type Tone } from "../kit/Status";
import { EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { useNow } from "../live";

const TONE: Record<string, Tone | undefined> = { ok: "ok", warn: "warn", bad: "bad", run: "run", idle: undefined };
const STATE: Record<string, State> = { bad: "bad", wait: "wait", run: "run", ok: "ok", quiet: "skip" };

/** Every Argo CD application a deploy runner has looked at, with a plain sentence for each. */
export function ApplicationsListPage() {
  const inst = useInstallation();
  const now = useNow(5000);
  const [filter, setFilter] = useState<"all" | "look">("all");
  const [q, setQ] = useState("");
  const units = useMemo(() => new Map(inst.environments.flatMap((e) => e.services.map((u) => [u.last?.application ?? "", u] as const))), [inst.environments]);
  const shown = useMemo(() => inst.applications.filter((a) => (filter === "all" || needsLook(a)) && (!q || a.name.toLowerCase().includes(q.toLowerCase()))), [inst.applications, filter, q]);
  const look = inst.applications.filter(needsLook).length;
  return (
    <div className="page">
      <PageHead eyebrow="Deployments" title="Applications"
        sentence="Argo CD keeps your cluster the same as what is in Git; each application is one thing it keeps in step. These are the ones a deploy runner has looked at." />
      {!inst.loaded && <div className="loading">Loading…</div>}
      {inst.loaded && inst.applications.length === 0 && (
        <Sheet><EmptyState title="No applications seen yet" action={<LinkButton href="/deployments/environments" icon="arrow">See the environments</LinkButton>}>
          An application appears here once a pipeline has deployed it and a deploy runner has observed it. Add a deploy step to a pipeline, and make sure a deploy runner serves the environment's target.
        </EmptyState></Sheet>
      )}
      {inst.applications.length > 0 && (
        <>
          <div className="toolbar2">
            <div className="pillbtns" role="group" aria-label="Filter">
              <button type="button" aria-pressed={filter === "all"} onClick={() => setFilter("all")}>All<span className="n">{inst.applications.length}</span></button>
              <button type="button" aria-pressed={filter === "look"} onClick={() => setFilter("look")}>Needs a look<span className="n">{look}</span></button>
            </div>
            <label className="select"><span className="visually-hidden">Search applications</span>
              <input type="search" placeholder="Search applications" value={q} onChange={(e) => setQ(e.target.value)} />
            </label>
          </div>
          <Sheet>
            <ul className="rowlist">
              {shown.map((a) => {
                const u = units.get(a.name);
                const dg = diagnose(u?.last, u?.live, undefined, now);
                const sync = plainSync(a.sync), health = plainHealth(a.health);
                return (
                  <li key={a.name} className={dg.tone === "bad" ? "bad" : dg.tone === "wait" ? "warn" : ""}>
                    <div className="main">
                      <div className="nm"><EntityLink kind="application" name={a.name} /></div>
                      <div className="sb">{dg.headline} · <EntityLink kind="environment" name={a.environment} /> · {a.run ? <>deployed by <EntityLink kind="project" name={a.project} /> <EntityLink kind="run" name={`#${a.run.number}`} id={a.run.id} /></> : "not by a pipeline"}</div>
                    </div>
                    <div className="cell"><Status state={STATE[dg.tone]} /><Chip tone={TONE[sync.tone]}>{sync.label}</Chip><Chip tone={TONE[health.tone]}>{health.label}</Chip></div>
                    <div className="cell mono">{a.version}</div>
                    <div className="cell faint">{a.observedAt ? ago(a.observedAt, now) : "never seen"}</div>
                  </li>
                );
              })}
              {shown.length === 0 && <li className="faint">Nothing matches.</li>}
            </ul>
          </Sheet>
          <p className="faint small">Applications Argo CD runs that no pipeline has deployed are not listed: no deploy runner has been asked to look at them.</p>
        </>
      )}
    </div>
  );
}
