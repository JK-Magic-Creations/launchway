import { useCallback, useEffect, useState } from "react";
import { browseApi, codeHref } from "../../browseApi";
import { DiffTable } from "../../components/DiffTable";
import { ErrorBox, Link } from "../../components/ui";
import { Chip } from "../../kit/Status";
import type { FileChange, Patch } from "../../pullsApi";
import "../../styles/pulls.css";
import { BtnSm } from "../../kit/bits";

function ChangedFile({ repo, from, to, f, startOpen }: { repo: string; from: string; to: string; f: FileChange; startOpen: boolean }) {
  const [open, setOpen] = useState(startOpen);
  const [patch, setPatch] = useState<Patch | null>(null);
  const [error, setError] = useState<unknown>(null);
  const load = useCallback((full: boolean) => { browseApi.patch(repo, from, to, f.path, full).then(setPatch).catch(setError); }, [repo, from, to, f.path]);
  useEffect(() => { if (open && !patch && !f.binary) load(false); }, [open, patch, f.binary, load]);
  const id = "cf-" + f.path.replace(/[^\w-]/g, "_");
  return (
    <section className="pr-file" aria-label={f.path} data-file>
      <header>
        <h3 tabIndex={-1}>{f.old_path ? `${f.old_path} → ${f.path}` : f.path}</h3>
        <Chip>{f.status}</Chip>
        {!f.binary && <span className="small"><span className="pr-add">+{f.additions}</span> <span className="pr-del">-{f.deletions}</span></span>}
        {f.status !== "deleted" && <Link href={codeHref(repo, to, f.path)} className="btn btn-sm">View file<span className="sr-only"> {f.path}</span></Link>}
        <BtnSm aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>{open ? "Hide" : "Show"}<span className="sr-only"> changes to {f.path}</span></BtnSm>
      </header>
      <div id={id}>
        <ErrorBox error={error} />
        {open && f.binary && <p className="pr-note faint">A binary file: there is no text to show.</p>}
        {open && patch?.held && <p className="pr-note">This file's patch is large and was not loaded. <BtnSm onClick={() => load(true)}>Load anyway</BtnSm></p>}
        {open && patch && !patch.held && patch.diff && <DiffTable path={f.path} diff={patch.diff} />}
        {open && patch?.truncated && <p className="pr-note faint">The patch was cut at the size limit.</p>}
      </div>
    </section>
  );
}

/** The files changed between two commits, each opening to its patch when asked for; the first few are open. */
export function ChangedFiles({ repo, from, to, files, total, truncated }: { repo: string; from: string; to: string; files: FileChange[]; total: number; truncated?: boolean }) {
  return (
    <div className="changed-files">
      {files.map((f, i) => <ChangedFile key={f.path} repo={repo} from={from} to={to} f={f} startOpen={i < 5 && files.length <= 20} />)}
      {truncated && <p className="faint small code-note">Only {files.length} of {total} files are listed. Clone the repository to see the rest.</p>}
    </div>
  );
}
