import { useMe } from "../auth";
import { EntityLink } from "../components/EntityLink";
import { StepStrip, StatusPill } from "../components/ui";
import { ago } from "../format";
import { useInstallation } from "../guided/facts";
import { Band, EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { useNow } from "../live";

/** The pipelines: one row for each project's pipeline, with how its last run went. Everything here
 * is read from the projects and runs the server holds. */
export function PipelinesPage() {
  const inst = useInstallation();
  const me = useMe();
  const now = useNow(15000);
  if (!inst.loaded) return <div className="page"><div className="loading">Loading…</div></div>;
  const rows = inst.projects;
  const failing = rows.filter((p) => p.last_run?.status === "failed").length;
  const running = rows.filter((p) => p.last_run?.status === "running" || p.last_run?.status === "queued").length;
  return (
    <div className="page">
      <PageHead title="Pipelines"
        sentence={rows.length === 0 ? "A pipeline is a set of steps that runs when something happens. None exists yet." :
          failing > 0 ? <><b>{failing} {failing === 1 ? "pipeline stopped" : "pipelines stopped"}</b> on its last run. The others are fine.</> : "Every pipeline's last run is fine or still going."}
        actions={me?.runners ? <LinkButton href="/playground" icon="play">Try a pipeline without saving</LinkButton> : undefined} />
      {rows.length > 0 && (
        <Band cells={[
          { key: "n", label: "Pipelines", value: rows.length, foot: "one for each project" },
          { key: "run", label: "Running now", value: running, foot: running ? "work is moving" : "quiet" },
          { key: "bad", label: "Stopped", value: failing, foot: failing ? "last run failed" : "none", loud: failing ? "bad" : undefined },
        ]} />
      )}
      {rows.length === 0 ? (
        <Sheet><EmptyState title="No pipelines yet" action={me?.admin ? <LinkButton href="/config/wizard/connect-repository" kind="pri" icon="plus">Add a project</LinkButton> : undefined}>
          Each project has a pipeline: its steps are read from the project's Git repository. Add a project and its pipeline appears here, with every run's result.
        </EmptyState></Sheet>
      ) : (
        <Sheet title="Every pipeline">
          <ul className="list">
            {rows.map((p) => {
              const r = p.last_run;
              return (
                <li key={p.name} className="li">
                  <div className="main">
                    <div className="nm"><EntityLink kind="project" name={p.name} /></div>
                    <div className="sb">{r ? `${r.pipeline} · run #${r.number}${r.message ? " · " + r.message : ""}` : "Not run yet"}</div>
                  </div>
                  {r ? <><StepStrip steps={r.steps} /><StatusPill status={r.status} /><span className="faint small">{ago(r.created_at, now)}</span></> : <span className="faint">No runs yet</span>}
                  <LinkButton href={`/projects/${encodeURIComponent(p.name)}/pipeline`} sm>Open the pipeline</LinkButton>
                </li>
              );
            })}
          </ul>
        </Sheet>
      )}
    </div>
  );
}
