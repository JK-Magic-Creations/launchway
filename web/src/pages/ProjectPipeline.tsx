import { useEffect, useState } from "react";
import { api, type PipelineFile, type Project } from "../api";
import { PipelinePreview } from "../components/PipelinePreview";
import { EntityLink } from "../components/EntityLink";
import { ErrorBox, Link, Mono } from "../components/ui";
import { ago, shortSha } from "../format";
import { useValidation } from "../validation";
import { useCrumbLabel } from "../nav/crumbs";

/** The project's pipeline, drawn from the file in Git at the tip of a branch. Nothing here is a copy:
 * to change it, the editor saves a commit. */
export function ProjectPipelinePage({ name }: { name: string }) {
  const [project, setProject] = useState<Project | null>(null);
  const [file, setFile] = useState<PipelineFile | null>(null);
  const [error, setError] = useState<unknown>(null);
  useCrumbLabel("Pipeline");
  const ref = new URLSearchParams(location.search).get("ref") ?? "";

  useEffect(() => {
    let alive = true;
    api.project(name).then(async (p) => {
      if (!alive) return;
      setProject(p);
      const f = await api.pipeline(name, ref || p.default_branch);
      if (alive) setFile(f);
    }).catch((e) => alive && setError(e));
    return () => { alive = false; };
  }, [name, ref]);

  const { res, error: checkError } = useValidation(file ? file.text : null, { project: name });
  const enc = encodeURIComponent(name);
  const editHref = `/projects/${enc}/pipeline/edit?ref=${encodeURIComponent(file?.ref ?? project?.default_branch ?? "")}`;
  return (
    <div className="page page-wide">
      <header className="page-head">
        <div>
          <h1>Pipeline of <EntityLink kind="project" name={name} /></h1>
          <p className="sub">The steps a run does, drawn from the file in your repository. Each box is a step; choose one to see what it does.</p>
        </div>
        <div className="head-actions">
          <Link href={editHref} className="btn btn-primary">Edit the pipeline</Link>
          <Link href="/playground" className="btn">Try it without saving</Link>
        </div>
      </header>
      <ErrorBox error={error} />
      {!file && !error && <div className="loading">Loading…</div>}
      {file && (
        <section className="card glance" aria-label="Where this pipeline is kept">
          <dl className="glance-grid">
            <div><dt>Where is it kept?</dt><dd>
              {file.exists ? <>In your repository, as <Mono>{file.path}</Mono> on <Mono>{file.ref}</Mono> (commit <Mono>{shortSha(file.sha)}</Mono>, {ago(file.committed_at)}).</>
                : file.configured ? <>The repository has no pipeline file, so the pipeline configured on the server runs instead. Editing it saves a new file to the repository.</>
                : <>The repository has no pipeline yet. Press Edit the pipeline to write the first one; it is saved to Git as a commit.</>}
            </dd></div>
            <div><dt>How is it changed?</dt><dd>The editor saves your change as a Git commit: to a new branch with a pull request, or straight to the branch where the rules allow it.</dd></div>
          </dl>
        </section>
      )}
      {file && <PipelinePreview res={res} error={checkError} />}
    </div>
  );
}
