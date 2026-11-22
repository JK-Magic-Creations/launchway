import { useEffect, useState } from "react";
import { browseApi } from "../browseApi";
import { ErrorBox, Mono } from "../components/ui";
import { forgeApi, type HostedRepo } from "../forgeApi";
import { Button, Sheet } from "../kit/layout";
import { navigate } from "../router";
import { prose } from "../prose";
const P = await prose("pages.RepoGeneral");

/** A destructive change asks for the repository's name, typed in, and says what is lost. */
function Confirm({ repo, what, lost, action, busy, onGo, onCancel }: { repo: HostedRepo; what: string; lost: string; action: string; busy: boolean; onGo: () => void; onCancel: () => void }) {
  const [typed, setTyped] = useState("");
  return (
    <div role="group" aria-label={what} className="callout callout-warn">
      <p><strong>{what}.</strong> {lost}</p>
      <label className="field"><span>Type <Mono>{repo.path}</Mono> to confirm</span>
        <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" data-autofocus /></label>
      <div className="actions">
        <Button kind="pri" disabled={busy || typed !== repo.path} onClick={onGo}>{busy ? "Working…" : action}</Button>
        <Button onClick={onCancel}>Cancel</Button>
      </div>
    </div>
  );
}

/** The general settings of a repository: its name, who may see it, its default branch, archiving and deleting. */
export function RepoGeneral({ repo, admin, onChanged }: { repo: HostedRepo; admin: boolean; onChanged: (r: HostedRepo) => void }) {
  const owner = repo.home ?? repo.path.split("/")[0];
  const [name, setName] = useState(repo.name);
  const [visibility, setVisibility] = useState(repo.visibility);
  const [branch, setBranch] = useState(repo.default_branch ?? "main");
  const [branches, setBranches] = useState<string[]>([]);
  const [asking, setAsking] = useState<"" | "archive" | "delete">("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState("");
  useEffect(() => { browseApi.branches(repo.path, "", 500).then((r) => setBranches(r.items.map((b) => b.name))).catch(() => setBranches([])); }, [repo.path]);

  const run = async (fn: () => Promise<HostedRepo | void>, done: string) => {
    setBusy(true); setError(null); setSaved("");
    try { const r = await fn(); setSaved(done); setAsking(""); if (r) onChanged(r); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const rename = () => run(async () => {
    const r = await forgeApi.update(owner, repo.name, { name: name.trim() });
    navigate(`/repositories/${r.path}/settings`);
    return r;
  }, "Renamed.");

  if (!admin) {
    return (
      <Sheet title="General" pad>
        <p className="sub" role="status">{P.t0}{repo.archived ? ", and it is archived" : ""}.</p>
      </Sheet>
    );
  }
  const branchChoices = branches.includes(branch) ? branches : [branch, ...branches];
  return (
    <Sheet title="General" pad>
      <ErrorBox error={error} />
      {saved && <p className="sub" role="status">{saved}</p>}
      <form className="inputs-form" aria-label="Rename the repository" onSubmit={(e) => { e.preventDefault(); void rename(); }}>
        <label className="field"><span>Name</span><input value={name} onChange={(e) => setName(e.target.value)} /></label>
        <p className="sub">{P.t1}<Mono>{owner}/{name.trim() || "…"}</Mono>{P.t2}</p>
        <div><Button type="submit" disabled={busy || !name.trim() || name.trim() === repo.name}>Rename</Button></div>
      </form>
      <form className="inputs-form" aria-label="Visibility of the repository" onSubmit={(e) => { e.preventDefault(); void run(() => forgeApi.update(owner, repo.name, { visibility }), "Visibility changed."); }}>
        <label className="field"><span>Who may see it</span>
          <select value={visibility} onChange={(e) => setVisibility(e.target.value)}>
            <option value="private">{P.t3}</option>
            <option value="internal">{P.t4}</option>
            <option value="public">{P.t5}</option>
          </select></label>
        <div><Button type="submit" disabled={busy || visibility === repo.visibility}>Change visibility</Button></div>
      </form>
      <form className="inputs-form" aria-label={P.t6} onSubmit={(e) => { e.preventDefault(); void run(() => forgeApi.update(owner, repo.name, { default_branch: branch }), "Default branch changed."); }}>
        <label className="field"><span>Default branch</span>
          <select value={branch} onChange={(e) => setBranch(e.target.value)}>{branchChoices.map((b) => <option key={b} value={b}>{b}</option>)}</select></label>
        <p className="sub">{P.t7}</p>
        <div><Button type="submit" disabled={busy || branch === (repo.default_branch ?? "main")}>Change default branch</Button></div>
      </form>
      <h3 className="h-sm">Archive and delete</h3>
      {repo.archived
        ? <p className="sub">{P.t8}<Button disabled={busy} onClick={() => run(() => forgeApi.update(owner, repo.name, { archived: false }), P.t9)}>Unarchive</Button></p>
        : asking === "archive"
          ? <Confirm repo={repo} what="Archive this repository" action="Archive" busy={busy} onCancel={() => setAsking("")}
              lost={P.t10} onGo={() => run(() => forgeApi.update(owner, repo.name, { archived: true }), "Archived.")} />
          : <p className="sub">{P.t11}<Button onClick={() => setAsking("archive")}>Archive this repository</Button></p>}
      {asking === "delete"
        ? <Confirm repo={repo} what="Delete this repository" action="Delete" busy={busy} onCancel={() => setAsking("")}
            lost={P.t12}
            onGo={() => run(async () => { await forgeApi.remove(owner, repo.name); navigate("/repositories"); }, "Deleted.")} />
        : <p className="sub">{P.t13}<Button onClick={() => setAsking("delete")}>Delete this repository</Button></p>}
    </Sheet>
  );
}
