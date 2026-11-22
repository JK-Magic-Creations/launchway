import { useCallback, useEffect, useRef, useState } from "react";
import { canOperate, useMe } from "../auth";
import { ReleasesNav } from "../components/ReleasesNav";
import { PromoteDialog } from "../components/ReleasePromote";
import { CutReleaseButton } from "../components/CutRelease";
import { OutsideSheet } from "../components/ReleaseOutside";
import { QrCode } from "../components/QrCode";
import { EntityLink } from "../components/EntityLink";
import { ErrorBox } from "../components/ui";
import { ago, clock } from "../format";
import { useInstallation } from "../guided/facts";
import { useLoad } from "../guided/useLoad";
import { Dialog } from "../kit/Dialog";
import { Chip, type Tone } from "../kit/Status";
import { Button, EmptyState, PageHead, Sheet } from "../kit/layout";
import { useNow } from "../live";
import { Markdown } from "../markdown";
import { useCrumbLabel } from "../nav/crumbs";
import { navigate, onLink } from "../router";
import { terms } from "../terms";
import { releasesApi, type Release, type ReleaseAsset, type ReleaseView, type ReleaseState, type Proposal } from "../releasesApi";
import "../releases.css";

const TONE: Record<ReleaseState, Tone | undefined> = { draft: undefined, candidate: "warn", published: "ok", yanked: "bad" };
const STATE_WORD: Record<ReleaseState, string> = { draft: "Draft", candidate: "Candidate", published: "Published", yanked: "Withdrawn" };

export function StateChip({ state }: { state: ReleaseState }) { return <Chip tone={TONE[state]}>{STATE_WORD[state] ?? state}</Chip>; }

/** The notes sit under the page's "Notes" heading, so a heading of the notes is one level lower than it was written. */
const underNotes = (md: string) => md.replace(/^#{2,6}(?=\s)/gm, (h) => h.slice(1));

const size = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KiB` : n < 1 << 30 ? `${(n / (1 << 20)).toFixed(1)} MiB` : `${(n / (1 << 30)).toFixed(1)} GiB`);
const href = (r: { project: string; version: string }) => `/deployments/releases/${encodeURIComponent(r.project)}/${encodeURIComponent(r.version)}`;

/** Every release the person may see, newest first, with a way to make one. */
export function ReleasesPage() {
  const me = useMe();
  const inst = useInstallation();
  const [project, setProject] = useState("");
  const [state, setState] = useState("");
  const [making, setMaking] = useState(false);
  const fn = useCallback((b: boolean) => releasesApi.list({ project, state }, b), [project, state]);
  const list = useLoad<Release[]>(fn, 15000);
  const now = useNow(30000);
  const operable = (inst.projects ?? []).filter((p) => canOperate(me, p.name)).map((p) => p.name);
  return (
    <div className="page">
      <PageHead eyebrow="Deployments · Releases" title="Releases"
        sentence="A release is the thing that is versioned, cleared, promoted from Airfield to Airfield and reported on: a commit, a tag, notes, Cargo and the image digests its Sortie built."
        actions={operable.length > 0 ? <>
          {(project && operable.includes(project)) ? <CutReleaseButton project={project} kind="pri" /> : operable.length === 1 ? <CutReleaseButton project={operable[0]} kind="pri" /> : null}
          <Button icon="plus" onClick={() => setMaking(true)}>New release</Button></> : undefined} />
      <ReleasesNav path="/deployments/releases" />
      <div className="toolbar2">
        <label className="select"><span className="visually-hidden">{terms().say("{Project}")}</span>
          <select value={project} onChange={(e) => setProject(e.target.value)}>
            <option value="">{terms().say("Every {project}")}</option>
            {(inst.projects ?? []).map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
          </select></label>
        <label className="select"><span className="visually-hidden">State</span>
          <select value={state} onChange={(e) => setState(e.target.value)}>
            <option value="">Every state</option>
            {(Object.keys(STATE_WORD) as ReleaseState[]).map((s) => <option key={s} value={s}>{STATE_WORD[s]}</option>)}
          </select></label>
      </div>
      <ErrorBox error={list.error} />
      {list.loading && !list.data && <div className="loading">Loading…</div>}
      {list.data && list.data.length === 0 && (
        <Sheet><EmptyState title="No releases yet" action={operable.length > 0 ? <Button kind="pri" onClick={() => setMaking(true)}>Make the first one</Button> : undefined}>
          A release starts as a draft of a commit, with notes written from the changes since the last one. Publishing it makes the tag and fixes it: from then on only withdrawing it is possible.
        </EmptyState></Sheet>
      )}
      {list.data && list.data.length > 0 && (
        <Sheet>
          <div className="rel-table-wrap">
            <table className="rel-table">
              <caption className="visually-hidden">Releases, newest first</caption>
              <thead><tr><th scope="col">Release</th><th scope="col">State</th><th scope="col">Commit</th><th scope="col">Tag</th><th scope="col">By</th><th scope="col">When</th></tr></thead>
              <tbody>
                {list.data.map((r) => (
                  <tr key={r.id}>
                    <td><a href={href(r)} onClick={onLink}><b>{r.project}</b> {r.version}</a>{r.prerelease && <> <Chip>pre-release</Chip></>}</td>
                    <td><StateChip state={r.state} /></td>
                    <td className="rel-sha">{r.sha.slice(0, 8)}</td>
                    <td className="rel-sha">{r.tag ?? "none yet"}</td>
                    <td>{r.published_by || r.created_by}</td>
                    <td title={clock(r.published_at || r.created_at)}>{ago(r.published_at || r.created_at, now)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Sheet>
      )}
      {making && <NewReleaseDialog projects={operable} initial={project} onClose={() => setMaking(false)} />}
    </div>
  );
}

/** Make a draft: the version (with the next one proposed and why), where it is made from, and the notes. */
function NewReleaseDialog({ projects, initial, onClose }: { projects: string[]; initial: string; onClose: () => void }) {
  const [project, setProject] = useState(projects.includes(initial) ? initial : projects[0] ?? "");
  const [version, setVersion] = useState("");
  const [from, setFrom] = useState("");
  const [generate, setGenerate] = useState(true);
  const [prop, setProp] = useState<Proposal | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const propose = async () => {
    setBusy(true); setError(null);
    try { const r = await releasesApi.next(project, { from: from || undefined }); setProp(r.proposal); if (r.proposal.next) setVersion(r.proposal.next); } catch (e) { setError(e); }
    setBusy(false);
  };
  const make = async () => {
    setBusy(true); setError(null);
    try {
      const v = await releasesApi.create(project, { version: version.trim(), from: from.trim() || undefined, generate_notes: generate });
      onClose(); navigate(href(v.release));
    } catch (e) { setError(e); setBusy(false); }
  };
  return (
    <Dialog label="New release" onClose={onClose}>
      <form className="rel-form" onSubmit={(e) => { e.preventDefault(); if (version.trim()) make(); }}>
        <h2>New release</h2>
        <label className="field"><span>{terms().say("{Project}")}</span>
          <select value={project} onChange={(e) => { setProject(e.target.value); setProp(null); }}>{projects.map((p) => <option key={p}>{p}</option>)}</select></label>
        <label className="field"><span>Make it from (a branch, tag or commit; empty is the default branch)</span>
          <input type="text" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="main" /></label>
        <label className="field"><span>Version</span>
          <input type="text" value={version} onChange={(e) => setVersion(e.target.value)} required placeholder="1.4.0" data-autofocus /></label>
        <div className="rel-actions" style={{ padding: 0 }}>
          <button type="button" className="btn btn-sm" onClick={propose} disabled={busy || !project}>Propose the next version</button>
          <span className="rel-hint">Read from the changes since the last release. You choose.</span>
        </div>
        {prop && (
          <div className="callout" role="status">
            <p style={{ margin: "0 0 6px" }}><b>{prop.summary}</b></p>
            {prop.reasons.length > 0 && <ul style={{ margin: 0, paddingLeft: 18 }}>{prop.reasons.slice(0, 8).map((r, i) => <li key={i}>{r.bump}: {r.change.title} <span className="faint">({r.why})</span></li>)}</ul>}
            {(prop.warnings ?? []).map((w) => <p key={w} className="rel-hint">{w}</p>)}
          </div>
        )}
        <label className="rel-check"><input type="checkbox" checked={generate} onChange={(e) => setGenerate(e.target.checked)} /> Write the notes from the pull requests and commits since the last release</label>
        <ErrorBox error={error} />
        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !version.trim() || !project}>{busy ? "Working…" : "Make the draft"}</button>
        </div>
      </form>
    </Dialog>
  );
}

/** One release. */
export function ReleasePage({ project, version }: { project: string; version: string }) {
  useCrumbLabel(`${project} ${version}`);
  const me = useMe();
  const fn = useCallback((b: boolean) => releasesApi.get(project, version, b), [project, version]);
  const data = useLoad<ReleaseView>(fn, 10000);
  const now = useNow(30000);
  const [dialog, setDialog] = useState<null | "publish" | "yank" | "delete">(null);
  const [promote, setPromote] = useState("");
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState("");
  const matrix = useLoad(useCallback((b: boolean) => releasesApi.matrix(b), []), 0);
  const v = data.data;
  const r = v?.release;
  const act = async (f: () => Promise<unknown>, done?: string) => {
    setError(null);
    try { await f(); if (done) setNotice(done); data.reload(); } catch (e) { setError(e); }
  };
  if (data.loading && !v) return <div className="page"><div className="loading">Loading…</div></div>;
  if (!v || !r) return <div className="page"><PageHead eyebrow="Deployments · Releases" title={`${project} ${version}`} /><ErrorBox error={data.error} /></div>;
  const airfields = matrix.data?.airfields ?? [];
  const sentence = r.state === "draft" ? "A draft: nothing is tagged yet, and the notes and files can still change." : r.state === "candidate" ? "A candidate: it may go to Airfields that are not protected, for testing, but it is not released." :
    r.state === "published" ? "Published: its version, commit, tag, Cargo and image digests never change. A correction is a new release." : `Withdrawn by ${r.yanked_by}: ${r.yank_reason}`;
  return (
    <div className="page">
      <PageHead eyebrow={<><a href="/deployments/releases" onClick={onLink}>Releases</a> · <EntityLink kind="project" name={r.project} /></>} title={`${r.project} ${r.version}`} badge={<> <StateChip state={r.state} /></>} sentence={sentence}
        actions={<>
          {v.may.publish && <Button kind="pri" onClick={() => setDialog("publish")}>Publish</Button>}
          {v.may.edit && r.state === "draft" && <Button onClick={() => act(() => releasesApi.candidate(r.project, r.version), "It is a candidate now.")}>Make it a candidate</Button>}
          {v.may.yank && <Button kind="bad" onClick={() => setDialog("yank")}>Withdraw</Button>}
          {v.may.edit && r.state === "draft" && <Button onClick={() => setDialog("delete")}>Delete the draft</Button>}
        </>} />
      <ReleasesNav path="" />
      {notice && <div className="callout" role="status">{notice}</div>}
      <ErrorBox error={error} />
      {(v.warnings ?? []).map((w) => <div key={w} className="callout callout-wait">{w}</div>)}
      <Sheet title="At a glance">
        <dl className="rel-dl">
          <dt>Commit</dt><dd className="rel-sha">{r.sha}</dd>
          <dt>Tag</dt><dd>{r.tag ? <><span className="rel-sha">{r.tag}</span> <span className="faint">({r.tag_created ? "made by Launchway, annotated" : "adopted, annotated"})</span></> : "none yet; publishing makes it"}</dd>
          <dt>Built by</dt><dd>{r.run_id ? <EntityLink kind="run" name={`Sortie #${r.run_number}`} id={r.run_id} /> : "no Sortie recorded"}</dd>
          <dt>Image digests</dt><dd>{r.digests.length === 0 ? "none: this release carries no image" : <ul style={{ margin: 0, padding: 0, listStyle: "none" }}>{r.digests.map((d) => <li key={d.digest} className="rel-sha">{d.digest}</li>)}</ul>}</dd>
          <dt>Made</dt><dd>{r.created_by} {ago(r.created_at, now)} <span className="faint">({clock(r.created_at)})</span></dd>
          {r.published_at ? <><dt>Published</dt><dd>{r.published_by} {ago(r.published_at, now)} <span className="faint">({clock(r.published_at)})</span></dd></> : null}
          {r.previous ? <><dt>After</dt><dd><a href={href({ project: r.project, version: r.previous })} onClick={onLink}>{r.previous}</a></dd></> : null}
          {r.public ? <><dt>Downloads</dt><dd>public, without signing in</dd></> : null}
        </dl>
      </Sheet>
      <Sheet title="Notes" actions={v.may.edit ? <>
        <Button sm onClick={() => setEditing((e) => !e)}>{editing ? "Stop editing" : "Edit the notes"}</Button>
        <Button sm onClick={() => act(() => releasesApi.regenerate(r.project, r.version), "The notes were written again from the changes.")}>Write them again from the changes</Button></> : undefined}>
        {editing && v.may.edit ? <NotesEditor release={r} onSaved={() => { setEditing(false); data.reload(); }} /> :
          r.notes.trim() ? <div className="rel-notes"><Markdown text={underNotes(r.notes)} /></div> : <p className="rel-hint" style={{ padding: "14px 16px" }}>No notes yet.</p>}
        {r.notes_source && <p className="rel-hint" style={{ padding: "0 16px 12px" }}>Source: {r.notes_source}.</p>}
      </Sheet>
      {r.state === "published" && (r.assets ?? []).some((a) => a.kind === "file") && <PhoneSheet v={v} />}
      <AssetsSheet v={v} onChanged={data.reload} />
      <OutsideSheet v={v} onChanged={data.reload} />
      {v.may.promote && airfields.length > 0 && (
        <Sheet title="Promote">
          <p className="rel-hint" style={{ padding: "12px 16px 0" }}>Build once, promote the same thing: a promotion starts the pipeline at this tag with this version and image digest. Each Airfield has its own checklist.</p>
          <div className="rel-actions">{airfields.map((a) => <Button key={a} sm onClick={() => setPromote(a)}>Check and promote to {a}</Button>)}</div>
        </Sheet>
      )}
      <Sheet title="Promotions">
        {v.promotions.length === 0 ? <p className="rel-hint" style={{ padding: "14px 16px" }}>It has not been promoted yet.</p> : (
          <div className="rel-table-wrap"><table className="rel-table">
            <caption className="visually-hidden">Promotions of this release</caption>
            <thead><tr><th scope="col">Airfield</th><th scope="col">Result</th><th scope="col">By</th><th scope="col">When</th><th scope="col">Sortie</th><th scope="col">Override</th></tr></thead>
            <tbody>{v.promotions.map((p) => (
              <tr key={p.id}><td>{p.airfield}</td><td>{p.state}{p.detail ? ` (${p.detail})` : ""}</td><td>{p.requested_by}</td><td title={clock(p.requested_at)}>{ago(p.requested_at, now)}</td>
                <td>{p.run_id ? <EntityLink kind="run" name={`#${p.run_number}`} id={p.run_id} /> : "none"}</td><td>{p.override ? `${p.override} (${p.overridden})` : "none"}</td></tr>
            ))}</tbody></table></div>)}
      </Sheet>
      {(v.clearances ?? []).length > 0 && (
        <Sheet title={terms().plural("clearance")}><ul className="rowlist">{(v.clearances ?? []).map((c) => <li key={c.id}><div className="main"><div className="nm">{c.who} <span className="faint">({c.role}) for {c.airfield}</span></div>{c.comment && <div className="sb">{c.comment}</div>}</div><div className="cell faint">{ago(c.at, now)}</div></li>)}</ul></Sheet>
      )}
      <CompareSheet project={r.project} version={r.version} />
      {r.state === "published" && <ChangelogNote release={r} may={canOperate(me, r.project)} />}
      {dialog === "publish" && <PublishDialog release={r} may={v.may.administer} onClose={() => setDialog(null)} onDone={data.reload} />}
      {dialog === "yank" && <YankDialog release={r} onClose={() => setDialog(null)} onDone={data.reload} />}
      {dialog === "delete" && <Dialog label="Delete the draft" onClose={() => setDialog(null)}>
        <div className="rel-form"><h2>Delete the draft {r.version}?</h2><p className="rel-hint">Its notes and files are removed. A candidate or published release is never deleted.</p>
          <div className="dialog-actions"><button className="btn" onClick={() => setDialog(null)}>Keep it</button>
            <button className="btn btn-danger-outline" onClick={() => act(async () => { await releasesApi.remove(r.project, r.version); navigate("/deployments/releases"); })}>Delete the draft</button></div></div>
      </Dialog>}
      {promote && <PromoteDialog project={r.project} version={r.version} airfield={promote} onClose={() => setPromote("")} onDone={data.reload} />}
    </div>
  );
}

function ChangelogNote({ release, may }: { release: Release; may: boolean }) {
  const [out, setOut] = useState<string>("");
  const [error, setError] = useState<unknown>(null);
  if (!may) return null;
  return (
    <Sheet title="Changelog">
      <div className="rel-actions"><Button sm onClick={async () => {
        setError(null);
        try { const r = await releasesApi.changelog(release.project, release.version); setOut(r.pull_request ? `Opened pull request #${r.pull_request.number}.` : `Pushed the branch ${r.proposal.branch}; open a pull request into ${r.proposal.base}.`); } catch (e) { setError(e); }
      }}>Propose the changelog as a pull request</Button></div>
      {out && <p className="rel-hint" style={{ padding: "0 16px 12px" }} role="status">{out}</p>}
      <ErrorBox error={error} />
    </Sheet>
  );
}

function NotesEditor({ release, onSaved }: { release: Release; onSaved: () => void }) {
  const [text, setText] = useState(release.notes);
  const [error, setError] = useState<unknown>(null);
  const save = async () => { try { await releasesApi.edit(release.project, release.version, { notes: text }); onSaved(); } catch (e) { setError(e); } };
  return (
    <div className="rel-edit">
      <label className="field"><span className="visually-hidden">Notes, in Markdown</span>
        <textarea value={text} onChange={(e) => setText(e.target.value)} aria-label="Notes, in Markdown" /></label>
      <ErrorBox error={error} />
      <div><Button kind="pri" onClick={save}>Save the notes</Button></div>
    </div>
  );
}

function AssetsSheet({ v, onChanged }: { v: ReleaseView; onChanged: () => void }) {
  const r = v.release;
  const [error, setError] = useState<unknown>(null);
  const [run, setRun] = useState("");
  const [busy, setBusy] = useState(false);
  const assets = r.assets ?? [];
  const upload = async (f: File | undefined) => {
    if (!f) return;
    setBusy(true); setError(null);
    try { await releasesApi.upload(r.project, r.version, f); onChanged(); } catch (e) { setError(e); }
    setBusy(false);
  };
  return (
    <Sheet title="Cargo and images">
      {assets.length === 0 ? <p className="rel-hint" style={{ padding: "14px 16px" }}>No files or images are attached.</p> : (
        <div className="rel-table-wrap"><table className="rel-table rel-assets">
          <caption className="visually-hidden">Files and images of this release, with where each came from</caption>
          <thead><tr><th scope="col">Name</th><th scope="col" className="hide-narrow">Kind</th><th scope="col" className="n">Size</th><th scope="col">SHA-256 or digest</th><th scope="col" className="hide-narrow">Where it came from</th><th scope="col">Get it</th></tr></thead>
          <tbody>{assets.map((a: ReleaseAsset) => (
            <tr key={a.id}>
              <td>{a.kind === "file" ? <a href={releasesApi.downloadUrl(r.project, r.version, a.name)} download>{a.name}</a> : a.name}</td>
              <td className="hide-narrow">{a.kind}</td><td className="n">{a.kind === "file" ? size(a.size) : ""}</td>
              <td className="rel-sha">{a.sha256 ?? a.digest ?? ""}{a.detail ? <span className="faint"> · {a.detail}</span> : null}</td>
              <td className="hide-narrow">{a.run_id ? <>Sortie #{a.run_number} at {a.source_sha?.slice(0, 8)}, added by {a.added_by}</> : <>uploaded by {a.added_by}</>}</td>
              <td>
                {a.kind === "file" && <a className="btn btn-sm" href={releasesApi.downloadUrl(r.project, r.version, a.name)} download>Download<span className="visually-hidden"> {a.name}</span></a>}
                {a.kind === "file" && r.state === "published" && <AssetQr release={r} asset={a} />}
                {v.may.edit && <Button sm onClick={async () => { try { await releasesApi.removeAsset(r.project, r.version, a.name); onChanged(); } catch (e) { setError(e); } }}>Remove<span className="visually-hidden"> {a.name}</span></Button>}
              </td>
            </tr>))}</tbody></table></div>
      )}
      {v.may.edit && (
        <div className="rel-inline">
          <label>Upload a file<input type="file" onChange={(e) => upload(e.target.files?.[0])} disabled={busy} /></label>
          <label>{terms().say("Attach what a Sortie built (its {run} id)")}<input type="text" value={run} onChange={(e) => setRun(e.target.value)} placeholder="run_…" /></label>
          <Button sm disabled={!run.trim() || busy} onClick={async () => { setError(null); try { await releasesApi.attach(r.project, r.version, { run_id: run.trim(), images: true }); setRun(""); onChanged(); } catch (e) { setError(e); } }}>Attach its image digests</Button>
        </div>
      )}
      <ErrorBox error={error} />
      {r.immutable && <p className="rel-hint" style={{ padding: "0 16px 12px" }}>Files and images are fixed once a release is published, each with the checksum it had.</p>}
    </Sheet>
  );
}

/** The address a phone should open for one file: the public link when anyone may download, else the page that signs in first. */
function assetAddress(r: Release, a: ReleaseAsset): string {
  return location.origin + (r.public && r.state === "published" ? releasesApi.publicUrl(r.project, r.version, a.name) : releasesApi.getUrl(r.project, r.version, a.name));
}

function AssetQr({ release, asset }: { release: Release; asset: ReleaseAsset }) {
  const addr = assetAddress(release, asset);
  return (
    <details className="rel-asset-qr">
      <summary>Open on a phone<span className="visually-hidden"> ({asset.name})</span></summary>
      <QrCode value={addr} label={`QR code that downloads ${asset.name} on a phone`} size={132} />
      <span className="rel-hint">Scan it with the camera. {release.public ? "Anyone may download it." : "A phone that is not signed in is asked to sign in first."}</span>
    </details>
  );
}

/** For a published release: a code that opens its page on a phone, where each file has its own download. */
function PhoneSheet({ v }: { v: ReleaseView }) {
  const r = v.release;
  const files = (r.assets ?? []).filter((a) => a.kind === "file");
  const page = location.origin + releasesApi.pageUrl(r.project, r.version);
  const apk = files.find((a) => /\.apk$/i.test(a.name));
  const main = apk ?? (files.length === 1 ? files[0] : undefined);
  const addr = main ? assetAddress(r, main) : page;
  return (
    <Sheet title="Get it on a phone">
      <div className="rel-phone">
        <QrCode value={addr} label={main ? `QR code that downloads ${main.name} on a phone` : `QR code that opens ${r.project} ${r.version} on a phone`} />
        <div>
          <p>{main ? <>Point the phone's camera at the code to download <b>{main.name}</b>{apk ? ", the Android app" : ""}.</> : <>Point the phone's camera at the code to open this release, where each file has its own download.</>} {r.public ? "Anyone with the address may download it." : "A phone that is not signed in is sent to sign in and then back to the download."}</p>
          {apk && <p className="rel-hint">Android asks once to allow installing from the browser it was downloaded with.</p>}
          <p className="addr">{addr}</p>
          {main && <p><a href={page} onClick={onLink}>All files of this release</a></p>}
        </div>
      </div>
    </Sheet>
  );
}

function PublishDialog({ release, may, onClose, onDone }: { release: Release; may: boolean; onClose: () => void; onDone: () => void }) {
  const [pub, setPub] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const go = async () => { setBusy(true); setError(null); try { await releasesApi.publish(release.project, release.version, { public: pub }); onClose(); onDone(); } catch (e) { setError(e); setBusy(false); } };
  return (
    <Dialog label={`Publish ${release.project} ${release.version}`} onClose={onClose}>
      <form className="rel-form" onSubmit={(e) => { e.preventDefault(); go(); }}>
        <h2>Publish {release.project} {release.version}?</h2>
        <p className="rel-hint">Publishing makes the annotated tag {release.tag || "for this version"} at commit {release.sha.slice(0, 8)} (an existing tag at that commit is adopted) and fixes the release. Its notes, files, tag and image digests can never change; if something is wrong, withdraw it and make a new release.</p>
        {may && <label className="rel-check"><input type="checkbox" checked={pub} onChange={(e) => setPub(e.target.checked)} /> {terms().say("Allow downloading its files without signing in (only where the installation and the {project} allow it)")}</label>}
        <ErrorBox error={error} />
        <div className="dialog-actions"><button type="button" className="btn" onClick={onClose}>Not yet</button><button type="submit" className="btn btn-primary" disabled={busy}>{busy ? "Publishing…" : "Publish"}</button></div>
      </form>
    </Dialog>
  );
}

function YankDialog({ release, onClose, onDone }: { release: Release; onClose: () => void; onDone: () => void }) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const go = async () => { setBusy(true); setError(null); try { await releasesApi.yank(release.project, release.version, reason.trim()); onClose(); onDone(); } catch (e) { setError(e); setBusy(false); } };
  return (
    <Dialog label={`Withdraw ${release.project} ${release.version}`} onClose={onClose}>
      <form className="rel-form" onSubmit={(e) => { e.preventDefault(); if (reason.trim()) go(); }}>
        <h2>Withdraw {release.project} {release.version}?</h2>
        <p className="rel-hint">It stays in the record, marked as withdrawn with your reason, and is no longer promoted or offered for download. What already runs is not touched: return that Airfield to base if it should go back.</p>
        <label className="field"><span>Why? (required)</span><textarea value={reason} onChange={(e) => setReason(e.target.value)} maxLength={1000} required data-autofocus /></label>
        <ErrorBox error={error} />
        <div className="dialog-actions"><button type="button" className="btn" onClick={onClose}>Keep it</button><button type="submit" className="btn btn-danger-outline" disabled={busy || !reason.trim()}>{busy ? "Working…" : "Withdraw"}</button></div>
      </form>
    </Dialog>
  );
}

function CompareSheet({ project, version }: { project: string; version: string }) {
  const list = useLoad<Release[]>(useCallback((b: boolean) => releasesApi.list({ project }, b), [project]), 0);
  const [other, setOther] = useState("");
  const [res, setRes] = useState<Awaited<ReturnType<typeof releasesApi.compare>> | null>(null);
  const [error, setError] = useState<unknown>(null);
  const others = (list.data ?? []).filter((x) => x.version !== version && x.state !== "draft");
  if (others.length === 0) return null;
  return (
    <Sheet title="Compare with another release">
      <div className="rel-inline">
        <label>Compare {version} with
          <select value={other} onChange={(e) => { setOther(e.target.value); setRes(null); }}>
            <option value="">Choose a release</option>{others.map((o) => <option key={o.id} value={o.version}>{o.version}</option>)}</select></label>
        <Button sm disabled={!other} onClick={async () => { setError(null); try { setRes(await releasesApi.compare(project, other, version)); } catch (e) { setError(e); } }}>Compare</Button>
      </div>
      <ErrorBox error={error} />
      {res && (
        <div className="rel-notes">
          <p><b>{res.older}</b> to <b>{res.newer}</b>: {res.changes.length} {res.changes.length === 1 ? "change" : "changes"}{res.files.stat ? `; ${res.files.stat}` : ""}.</p>
          <ul>{res.changes.slice(0, 40).map((c, i) => <li key={i}>{c.title}{c.ref ? ` (#${c.ref})` : ""}{c.author ? ` by ${c.author}` : ""}</li>)}</ul>
          {res.files.manifests.length > 0 && <><p><b>Deployment configuration changed:</b></p><ul>{res.files.manifests.map((m) => <li key={m} className="rel-sha">{m}</li>)}</ul></>}
          <p className="rel-hint">Image digests: {res.digests.older.length} before, {res.digests.newer.length} after.</p>
        </div>
      )}
    </Sheet>
  );
}

/** The page a download's QR code opens: it needs a signed-in person (the sign-in page comes first and returns here), then starts the download. */
export function ReleaseGetPage({ project, version, asset }: { project: string; version: string; asset: string }) {
  useCrumbLabel(`${project} ${version}`);
  const fn = useCallback((b: boolean) => releasesApi.get(project, version, b), [project, version]);
  const data = useLoad<ReleaseView>(fn, 0);
  const url = releasesApi.downloadUrl(project, version, asset);
  const a = data.data?.release.assets?.find((x) => x.name === asset);
  const started = useRef(false);
  useEffect(() => {
    if (!a || started.current) return;
    started.current = true;
    const link = document.createElement("a");
    link.href = url; link.download = asset; link.rel = "noopener";
    document.body.appendChild(link); link.click(); link.remove();
  }, [a, url, asset]);
  return (
    <div className="page">
      <PageHead eyebrow={<><a href="/deployments/releases" onClick={onLink}>Releases</a> · {project}</>} title={asset} sentence={`From ${project} ${version}.`} />
      <ErrorBox error={data.error} />
      {a ? (
        <Sheet title="Your download">
          <div className="rel-actions" role="status">
            <span>The download should start now. If it does not, use the button.</span>
            <a className="btn btn-primary" href={url} download>Download {asset} ({size(a.size)})</a>
            <a className="btn" href={href({ project, version })} onClick={onLink}>Back to the release</a>
          </div>
          <p className="rel-hint" style={{ padding: "0 16px 12px" }}>SHA-256 <span className="rel-sha">{a.sha256}</span></p>
        </Sheet>
      ) : !data.loading && !data.error ? <Sheet><EmptyState title="There is no such file in this release">Open <a href={href({ project, version })} onClick={onLink}>the release</a> to see its files.</EmptyState></Sheet> : <div className="loading">Loading…</div>}
    </div>
  );
}
