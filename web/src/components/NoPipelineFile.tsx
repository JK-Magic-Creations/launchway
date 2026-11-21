import { useState } from "react";
import { api, type RunDetail } from "../api";
import { ErrorBox, Mono } from "./ui";

/** The notice for a run of a repository with no pipeline file: which starter ran and why, and a way to
 * make it the repository's own file, as a pull request. */
export function NoPipelineFile({ run, onSee }: { run: RunDetail; onSee: () => void }) {
  const d = run.graph.origin?.detected;
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [url, setUrl] = useState<string | null>(null);
  const [branch, setBranch] = useState("");
  if (!d) return null;
  const add = async () => {
    setBusy(true); setErr(null);
    try {
      const ref = run.branch || "";
      const file = await api.pipeline(run.project, ref);
      const res = await api.savePipeline(run.project, {
        base_ref: file.ref, base_sha: file.sha, pipeline: file.text, branch: "",
        message: `Add .launchway/pipeline.yaml: ${d.flight}`,
      });
      setUrl(res.pull_request_url ?? ""); setBranch(res.branch);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  return (
    <div className="callout callout-info" role="status">
      <div>
        <strong>No pipeline file:</strong> using <Mono>{d.flight}</Mono> because {d.because}.{" "}
        <button type="button" className="linkbtn" onClick={onSee}>See what it runs</button>
      </div>
      {url === null ? (
        <div>
          <button type="button" className="btn btn-sm" disabled={busy} onClick={add}>{busy ? "Adding…" : "Add this file to the repository"}</button>
          <span className="faint"> as a pull request, so you can change it and own it</span>
        </div>
      ) : (
        <div>
          Saved on the branch <Mono>{branch}</Mono>.{" "}
          {url ? <a href={url} target="_blank" rel="noreferrer">Open the pull request</a> : "Open a pull request from it in your Git host."}
        </div>
      )}
      <ErrorBox error={err} />
    </div>
  );
}
