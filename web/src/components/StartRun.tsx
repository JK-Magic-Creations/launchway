import { useEffect, useRef, useState } from "react";
import { ApiError, type LibraryOverrides, type Project, type Ref } from "../api";
import { shortSha } from "../format";
import { navigate } from "../router";
import { triggersApi } from "../triggers";
import { ErrorBox, Mono } from "./ui";
import { RefPicker } from "./RefPicker";
import { OverridesEditor } from "./Libraries";
import { StartInputs, useStartInputs } from "./StartInputs";
import { VariablesPanel } from "./Variables";

// StartDialog asks which branch or tag to run, and the pipeline's inputs, then starts the run.
export function StartDialog({ project, onClose }: { project: Project; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [pick, setPick] = useState<Ref | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [overrides, setOverrides] = useState<LibraryOverrides>({});
  const inputs = useStartInputs(project.name, pick?.name ?? "");

  useEffect(() => { dialog.current?.showModal(); }, []);

  const start = async () => {
    if (!pick || !inputs.ready) return;
    if (!inputs.checkAll()) return;
    setBusy(true); setError(null); inputs.setServerErrors(undefined);
    try {
      const run = await triggersApi.startRun(project.name, pick.name, inputs.wire(), Object.keys(overrides).length ? overrides : undefined);
      dialog.current?.close();
      onClose();
      navigate(`/runs/${run.id}`);
    } catch (e) {
      if (e instanceof ApiError && e.fields) inputs.setServerErrors(e.fields);
      setError(e); setBusy(false);
    }
  };

  return (
    <dialog ref={dialog} className="dialog" onClose={onClose} aria-labelledby="start-title">
      <form method="dialog" onSubmit={(e) => { e.preventDefault(); start(); }}>
        <h2 id="start-title">Start a run of {project.name}</h2>
        <p className="sub">Branches and tags are read from the repository now. The pipeline file at the chosen commit is what runs{project.pipeline ? ", or, if it has none, the pipeline configured on the server" : ""}.</p>
        <RefPicker project={project.name} defaultBranch={project.default_branch} chosen={pick?.name ?? ""} onChoose={setPick} />
        {pick && (
          <div className="refpick">
            Will run <strong>{pick.name}</strong> at <Mono>{shortSha(pick.sha)}</Mono>, "{pick.subject}"
          </div>
        )}
        <StartInputs state={inputs} />
        <details className="advanced">
          <summary>Variables in effect</summary>
          <VariablesPanel level="project" scope={project.name} compact />
        </details>
        <details className="advanced">
          <summary>Advanced{Object.keys(overrides).length > 0 && `: ${Object.keys(overrides).length} library override${Object.keys(overrides).length > 1 ? "s" : ""}`}</summary>
          <OverridesEditor value={overrides} onChange={setOverrides} names={project.pipeline ? [project.pipeline.library] : undefined} />
        </details>
        <ErrorBox error={error instanceof ApiError && error.problems
          ? error.message + ":\n" + error.problems.map((p) => (p.file ? `${p.library}: ${p.file}` : "") + (p.line ? ` line ${p.line}` : "") + ": " + p.message).join("\n")
          : error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={() => { dialog.current?.close(); }}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={!pick || busy || !inputs.ready}>{busy ? "Starting…" : "Start run"}</button>
        </div>
      </form>
    </dialog>
  );
}
