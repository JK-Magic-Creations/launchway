import "../releases.css";
import { useEffect, useMemo, useState } from "react";
import { canOperate, useMe } from "../auth";
import { useInstallation } from "../guided/facts";
import { Dialog } from "../kit/Dialog";
import { Button } from "../kit/layout";
import { Markdown } from "../markdown";
import { navigate } from "../router";
import { releasesApi, type NextAnswer } from "../releasesApi";
import { terms } from "../terms";
import { ErrorBox } from "./ui";
import { prose } from "../prose";
import { BtnPrimary, DivActions, LabelField } from "../kit/bits";
const P = await prose("components.CutRelease");

const PATTERNS: Record<string, { re: RegExp; say: string }> = {
  semver: { re: /^\d+\.\d+\.\d+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/, say: P.t0 },
  calver: { re: /^\d{4}(\.\d+)+$/, say: P.t1 },
  natural: { re: /^[A-Za-z0-9][A-Za-z0-9._+-]*$/, say: P.t2 },
  created: { re: /^[A-Za-z0-9][A-Za-z0-9._+-]*$/, say: P.t2 },
};

/** The message that says why a version cannot be used, or "" when it can. */
export function versionProblem(scheme: string, version: string): string {
  const v = version.trim();
  if (!v) return "Say which version to release.";
  const pat = PATTERNS[scheme] ?? PATTERNS.natural;
  return pat.re.test(v) ? "" : pat.say;
}

/** The button that opens the dialog, shown only to someone who may release the Flight. */
export function CutReleaseButton({ project, kind }: { project: string; kind?: "pri" }) {
  const me = useMe();
  const [open, setOpen] = useState(false);
  if (!canOperate(me, project)) return null;
  return (
    <>
      <Button kind={kind} icon="plus" onClick={() => setOpen(true)}>Cut a release</Button>
      {open && <CutReleaseDialog project={project} onClose={() => setOpen(false)} />}
    </>
  );
}

/** The same button on a hosted repository's page: it finds the Flight that reads this repository, and shows nothing when none does. */
export function CutReleaseForRepository({ cloneUrl }: { cloneUrl?: string }) {
  const inst = useInstallation();
  const norm = (a: string) => a.trim().toLowerCase().replace(/\/+$/, "").replace(/\.git$/, "");
  const flight = cloneUrl ? inst.projects.find((p) => norm(p.url) === norm(cloneUrl)) : undefined;
  if (!flight) return null;
  return <CutReleaseButton project={flight.name} />;
}

/** Cut a release: the version (the next one proposed, checked as it is typed), what will be tagged, the notes as they will be
 * written and the files it will carry; then one step makes the tag and publishes it. */
export function CutReleaseDialog({ project, onClose }: { project: string; onClose: () => void }) {
  const [next, setNext] = useState<NextAnswer | null>(null);
  const [version, setVersion] = useState("");
  const [notes, setNotes] = useState<{ markdown: string; count: number } | null>(null);
  const [attach, setAttach] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let live = true;
    releasesApi.next(project).then((n) => {
      if (!live) return;
      setNext(n); setLoading(false);
      if (n.proposal.next) setVersion(n.proposal.next);
      setAttach(!!n.build && n.build.files.length > 0);
    }, (e) => { if (live) { setError(e); setLoading(false); } });
    return () => { live = false; };
  }, [project]);
  useEffect(() => {
    let live = true;
    releasesApi.notes(project, { version: version.trim() || undefined }).then((r) => { if (live) setNotes(r.notes); }, () => { if (live) setNotes(null); });
    return () => { live = false; };
  }, [project, version]);
  const scheme = next?.scheme ?? "semver";
  const problem = useMemo(() => (version ? versionProblem(scheme, version) : ""), [scheme, version]);
  const tag = (next?.tag_prefix ?? "v") + version.trim();
  const cut = async () => {
    setBusy(true); setError(null);
    try {
      const v = await releasesApi.create(project, { version: version.trim(), generate_notes: true, attach_cargo: attach && !!next?.build, publish: true });
      onClose(); navigate(releasesApi.pageUrl(v.release.project, v.release.version));
    } catch (e) { setError(e); setBusy(false); }
  };
  const files = next?.build?.files ?? [];
  const size = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KiB` : `${(n / (1 << 20)).toFixed(1)} MiB`);
  return (
    <Dialog label={`Cut a release of ${project}`} onClose={onClose}>
      <form className="rel-form" onSubmit={(e) => { e.preventDefault(); if (!problem && version.trim() && next?.can_tag) cut(); }}>
        <h2>Cut a release of {project}</h2>
        {loading && <p className="rel-hint" role="status">{P.t3}</p>}
        <LabelField><span>Version</span>
          <input type="text" value={version} onChange={(e) => setVersion(e.target.value)} required placeholder="1.4.0" data-autofocus
            aria-invalid={problem ? true : undefined} aria-describedby="cut-version-help" autoComplete="off" autoCapitalize="none" spellCheck={false} /></LabelField>
        <p id="cut-version-help" className={problem ? "" : "rel-hint"} role={problem ? "alert" : undefined}>
          {problem || (next?.proposal.summary ?? P.t4)}
          {!problem && next?.highest ? ` The highest so far is ${next.highest}.` : ""}
        </p>
        {(next?.proposal.warnings ?? []).map((w) => <p key={w} className="rel-hint">{w}</p>)}
        {next && (
          <dl className="rel-cut-tag" aria-label="What will be tagged">
            <dt>Tag</dt><dd className="rel-sha">{version.trim() && !problem ? tag : "(set a valid version first)"}</dd>
            <dt>At</dt><dd><span className="rel-sha">{next.commit.slice(0, 8)}</span>{next.branch ? <> on {next.branch}</> : null}</dd>
            <dt>Kept in</dt><dd>{next.hosted ? P.t5 : "the Flight's repository"}</dd>
          </dl>
        )}
        {next && !next.can_tag && <p className="callout callout-wait" role="alert">{P.t6}</p>}
        <div><b>Notes</b>
          <div className="rel-cut-notes">
            {notes && notes.markdown.trim() ? <Markdown text={notes.markdown} /> : <span className="rel-hint">{P.t7}</span>}
          </div>
        </div>
        <div>
          <label className="rel-check">
            <input type="checkbox" checked={attach && !!next?.build} disabled={!next?.build} onChange={(e) => setAttach(e.target.checked)} />
            {next?.build ? <>Attach the {files.length} {files.length === 1 ? "file" : "files"} built at this commit ({terms().say("{Run}")} #{next.build.run_number})</> : "No files to attach"}
          </label>
          {next?.build && files.length > 0 && <ul className="rel-hint" style={{ margin: "6px 0 0", paddingLeft: 22 }}>{files.map((f) => <li key={f.path}>{f.name} ({size(f.size)})</li>)}</ul>}
          {next && !next.build && <p className="rel-hint">No {terms().say("{run}")} of this {terms().say("{project}")} {P.t8}</p>}
        </div>
        <p className="rel-hint">{P.t9}</p>
        <ErrorBox error={error} />
        <DivActions>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <BtnPrimary disabled={busy || loading || !version.trim() || !!problem || !next?.can_tag}>{busy ? "Cutting…" : `Cut ${version.trim() || "the release"}`}</BtnPrimary>
        </DivActions>
      </form>
    </Dialog>
  );
}
