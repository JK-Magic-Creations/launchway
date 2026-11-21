import { useCallback, useState } from "react";
import { useMe } from "../auth";
import { useInstallation } from "../guided/facts";
import { registryApi, type ProjectRegistry, type RegistryImage, type RegistryPolicy, type RetentionCandidate } from "../registryApi";
import { bytes, CopyValue } from "../components/Security";
import { EntityLink } from "../components/EntityLink";
import { ErrorBox, Mono } from "../components/ui";
import { ago, clock } from "../format";
import { useLoad } from "../guided/useLoad";
import { useCrumbLabel } from "../nav/crumbs";
import { Chip } from "../kit/Status";
import { Button, EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { navigate, onLink } from "../router";
import type { Me } from "../api";
import { terms } from "../terms";

// The Images tab of the built-in registry: a project's repositories, each repository's images (tags, digest, size,
// platforms, who pushed it from which run, whether it is signed, has a bill of materials, what the scan found) with a
// pull reference to copy, and the rules that keep, protect and limit them. The server decides every request: the buttons
// that delete or change a rule are shown only to someone who may, and the rest are the same for everyone who can see.

const isAdmin = (me: Me | null, project: string) => !!me && (me.admin || me.roles[project] === "admin");

function quotaText(r: ProjectRegistry): string {
  return r.quota_bytes < 0 ? "no quota" : `${bytes(r.used_bytes)} of ${bytes(r.quota_bytes)}`;
}

/** The registry's start page: how to log in and push, and a way to a project's images. */
export function RegistryPage({ project }: { project?: string }) {
  useCrumbLabel(project ?? terms().say("{Registry}"));
  const statusFn = useCallback(() => registryApi.status(), []);
  const status = useLoad(statusFn);
  const inst = useInstallation();
  const mine = inst.projects.map((p) => p.name).sort();
  const off = (status.error as { status?: number } | null)?.status === 404 || (status.error as { code?: string } | null)?.code === "feature_off";

  if (status.loading) return <div className="page"><div className="loading">Loading…</div></div>;
  if (off || (status.error && !status.data)) {
    return (
      <div className="page">
        <PageHead eyebrow="Images" title={terms().say("{Registry}")} sentence="Launchway's own place for container images and Helm charts." />
        {!off && <ErrorBox error={status.error} />}
        <Sheet><EmptyState title={terms().say("The built-in {registry} is off")} action={<LinkButton href="/images" icon="arrow">{terms().say("Find images your {runs} built")}</LinkButton>}>
          {terms().say("It needs sign-in and an {artifact} store, and")} <Mono>registry.enabled</Mono> must not be false. Run <Mono>launchway doctor registry</Mono> on the server to see what is missing. Pushing to any other registry keeps working.
        </EmptyState></Sheet>
      </div>
    );
  }
  const s = status.data!;
  return (
    <div className="page">
      <PageHead eyebrow="Images" title={terms().say("{Registry}")} sentence={<>Images and Helm charts pushed to <Mono>{s.host}</Mono>. A repository is named <Mono>project/name</Mono>; people with a role on the project see it, and nobody else does.</>} />
      <div className="grid g2">
        <Sheet title="Push and pull">
          <dl className="kvlist">
            <dt>Address</dt><dd><CopyValue value={s.host} label={terms().say("the {registry} address")} keep={40} /></dd>
            <dt>Log in</dt><dd><CopyValue value={s.login.split("   ")[0]} label="the login command" keep={48} /> <span className="faint small">with an API token as the password (<a href="/settings/access/keys" onClick={onLink}>make one</a>)</span></dd>
            <dt>Push</dt><dd><Mono>docker push {s.host}/{project ?? "project"}/image:tag</Mono></dd>
            {s.insecure && <><dt>Plain HTTP</dt><dd>{terms().say("This {registry} is not behind HTTPS: clients need")} <Mono>--tls-verify=false</Mono> (podman, skopeo) or <Mono>--plain-http</Mono> (helm).</dd></>}
            <dt>In a pipeline</dt><dd>An image step with no <Mono>registry</Mono> {terms().say("pushes here, with a credential made for the {run}.")}</dd>
          </dl>
        </Sheet>
        <Sheet title="How it is kept">
          <dl className="kvlist">
            <dt>Stored in</dt><dd>{terms().say("the {artifact} store (")}{s.store})</dd>
            <dt>At rest</dt><dd><Chip tone={s.encryption.blobs_encrypted_by_launchway ? "ok" : "warn"}>{s.encryption.blobs_encrypted_by_launchway ? "Encrypted by Launchway" : "Not encrypted by Launchway"}</Chip> <span className="faint small">{s.encryption.note}</span></dd>
            <dt>Limits</dt><dd>a blob up to {bytes(s.limits.max_blob_bytes)}; a project {s.limits.default_quota_bytes < 0 ? "has no quota by default" : `may store ${bytes(s.limits.default_quota_bytes)} by default`}</dd>
          </dl>
        </Sheet>
      </div>
      <h2 className="h-sm">Project</h2>
      {mine.length === 0 && <p className="faint">You have no role on any project, so there is nothing here to see.</p>}
      <nav className="chips" aria-label="Projects">
        {mine.map((p) => (
          <a key={p} className={"chip" + (p === project ? " chip-on" : "")} href={`/images/registry/${encodeURIComponent(p)}`} onClick={onLink} aria-current={p === project ? "page" : undefined}>{p}</a>
        ))}
      </nav>
      {project && <ProjectRegistryView key={project} project={project} />}
    </div>
  );
}

function ProjectRegistryView({ project }: { project: string }) {
  const me = useMe();
  const repoParam = new URLSearchParams(location.search).get("repository") ?? "";
  const fn = useCallback((bg: boolean) => registryApi.project(project, bg), [project]);
  const data = useLoad(fn, 10000);
  const admin = isAdmin(me, project);
  const r = data.data;
  const share = r && r.quota_bytes > 0 ? r.used_bytes / r.quota_bytes : 0;
  return (
    <>
      <ErrorBox error={data.error} />
      {data.loading && <div className="loading">Loading…</div>}
      {r && (
        <Sheet title={<>Repositories of {project}</>} actions={<span className="faint small">{quotaText(r)}</span>}>
          {r.quota_bytes > 0 && (
            <div className="store-meter" role="meter" aria-label={terms().say("{Registry} quota used")} aria-valuemin={0} aria-valuemax={r.quota_bytes} aria-valuenow={r.used_bytes} aria-valuetext={quotaText(r)}>
              <span className={share >= 0.8 ? "store-meter-fill store-meter-high" : "store-meter-fill"} style={{ width: `${Math.min(100, Math.max(share * 100, r.used_bytes > 0 ? 1 : 0))}%` }} />
            </div>
          )}
          {r.repositories.length === 0 ? (
            <EmptyState title="Nothing is pushed yet">Push an image or a chart to <Mono>{r.push_example}</Mono>, or let a pipeline's image step push it: with no <Mono>registry</Mono> named it comes here.</EmptyState>
          ) : (
            <div className="table-scroll">
              <table className="table" aria-label={`Repositories of ${project}`}>
                <thead><tr><th>Repository</th><th>Images</th><th>Tags</th><th>Size</th><th>Last push</th></tr></thead>
                <tbody>
                  {r.repositories.map((p) => (
                    <tr key={p.name}>
                      <td><a href={`/images/registry/${encodeURIComponent(project)}?repository=${encodeURIComponent(p.name)}`} onClick={onLink} aria-current={p.name === repoParam ? "true" : undefined}><Mono>{p.name}</Mono></a></td>
                      <td>{p.images}</td><td>{p.tags}</td><td>{bytes(p.bytes)}</td>
                      <td title={clock(p.pushed_at)}>{p.pushed_at ? ago(p.pushed_at) : <span className="faint">never</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Sheet>
      )}
      {repoParam && <ImagesView key={repoParam} project={project} repository={repoParam} admin={admin} />}
      {r && <PolicyView project={project} registry={r} admin={admin} onChanged={data.reload} />}
    </>
  );
}

function vulnText(v: NonNullable<RegistryImage["vulnerabilities"]>): string {
  const total = v.critical + v.high + v.medium + v.low;
  if (total === 0) return "no vulnerabilities found";
  return [v.critical && `${v.critical} critical`, v.high && `${v.high} high`, v.medium && `${v.medium} medium`, v.low && `${v.low} low`].filter(Boolean).join(", ");
}

function ImagesView({ project, repository, admin }: { project: string; repository: string; admin: boolean }) {
  const fn = useCallback((bg: boolean) => registryApi.images(project, repository, bg), [project, repository]);
  const data = useLoad(fn, 10000);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [said, setSaid] = useState("");
  const remove = async (im: RegistryImage) => {
    const what = im.tags.length ? im.tags.join(", ") : im.digest.slice(0, 19);
    if (!window.confirm(`Delete ${what} from ${repository}?\n\nIts tags, signatures and bills of materials go with it. Pulls of it stop working. The bytes are removed at the next garbage collection.`)) return;
    setBusy(true); setError(null); setSaid("");
    try {
      const out = await registryApi.deleteImage(project, repository, im.digest);
      setSaid(`Deleted ${what}${out.deleted.length > 1 ? ` and ${out.deleted.length - 1} attached item${out.deleted.length > 2 ? "s" : ""}` : ""}.`);
      data.reload();
    } catch (e) { setError(e); } finally { setBusy(false); }
  };
  const images = data.data?.images ?? [];
  return (
    <Sheet title={<>Images in <Mono>{repository}</Mono></>}>
      <ErrorBox error={data.error ?? error} />
      {said && <div className="callout callout-info" role="status">{said}</div>}
      {data.loading && <div className="loading">Loading…</div>}
      {data.data && images.length === 0 && <EmptyState title="No images here">The repository has none left, or nobody has pushed to it.</EmptyState>}
      {images.length > 0 && (
        <div className="table-scroll">
          <table className="table" aria-label={`Images in ${repository}`}>
            <thead><tr><th>Tags</th><th>Digest</th><th>Size</th><th>Platforms</th><th>Pushed</th><th>Checks</th><th>Pull</th>{admin && <th><span className="visually-hidden">Actions</span></th>}</tr></thead>
            <tbody>
              {images.map((im) => (
                <tr key={im.digest}>
                  <td>{im.tags.length ? im.tags.map((t) => <div key={t}><Mono>{t}</Mono></div>) : <span className="faint">untagged</span>}
                    {im.artifact_type && <div className="faint small">{im.artifact_type}</div>}</td>
                  <td><CopyValue value={im.digest} label="digest" keep={19} /></td>
                  <td>{bytes(im.total_size)}</td>
                  <td>{im.platforms?.length ? im.platforms.join(", ") : <span className="faint">{im.children?.length ? "" : "—"}</span>}</td>
                  <td title={clock(im.created_at)}>{ago(im.created_at)}{im.created_by && <> by {im.created_by}</>}
                    {im.run_id && <div className="small"><EntityLink kind="run" name="its run" id={im.run_id} /></div>}</td>
                  <td>
                    {im.signed ? <Chip tone="ok">Signed</Chip> : <span className="faint small">Not signed</span>}{" "}
                    {im.has_sbom && <Chip>SBOM</Chip>}{" "}{im.has_provenance && <Chip>Provenance</Chip>}
                    <div className="small">{im.vulnerabilities ? vulnText(im.vulnerabilities) : <span className="faint">No scan result attached</span>}</div>
                  </td>
                  <td><CopyValue value={im.pull} label={`the pull reference of ${im.tags[0] ?? im.digest.slice(0, 19)}`} keep={26} /></td>
                  {admin && <td className="td-actions"><Button kind="bad" sm disabled={busy} onClick={() => remove(im)} aria-label={`Delete ${im.tags[0] ?? im.digest.slice(0, 19)}`}>Delete</Button></td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {!admin && images.length > 0 && <p className="faint small">Deleting an image needs the admin role on {project}.</p>}
    </Sheet>
  );
}

function PolicyView({ project, registry, admin, onChanged }: { project: string; registry: ProjectRegistry; admin: boolean; onChanged: () => void }) {
  const eff = registry.policy;
  const [preview, setPreview] = useState<RetentionCandidate[] | null>(null);
  const [deleted, setDeleted] = useState<number | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState({ keepLast: String(eff.retention.keep_last ?? 0), keepTags: (eff.retention.keep_tags ?? []).join(", "), untagged: String(eff.retention.untagged_days ?? 0), immutable: (eff.immutable_tags ?? []).join(", "), quotaGiB: eff.quota_bytes > 0 ? String(Math.round((eff.quota_bytes / 2 ** 30) * 100) / 100) : "" });
  const list = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);
  const run = async (what: () => Promise<void>) => { setBusy(true); setError(null); try { await what(); } catch (e) { setError(e); } finally { setBusy(false); } };
  const save = (e: React.FormEvent) => {
    e.preventDefault();
    const policy: RegistryPolicy = { retention: { keep_last: Number(form.keepLast) || 0, keep_tags: list(form.keepTags), untagged_days: Number(form.untagged) || 0 }, immutable_tags: list(form.immutable) };
    if (form.quotaGiB !== "") policy.quota_bytes = Math.round(Number(form.quotaGiB) * 2 ** 30);
    run(async () => { await registryApi.setPolicy(project, policy); setEditing(false); setPreview(null); onChanged(); });
  };
  const retention = eff.has_retention ? [eff.retention.keep_last ? `keep the newest ${eff.retention.keep_last} tagged images` : "", eff.retention.keep_tags?.length ? `keep tags ${eff.retention.keep_tags.join(", ")}` : "", eff.retention.untagged_days ? `delete untagged images older than ${eff.retention.untagged_days} days` : ""].filter(Boolean).join("; ") : "";
  return (
    <Sheet title="Rules" actions={admin && !editing ? <Button sm onClick={() => setEditing(true)}>Change the rules</Button> : undefined}>
      <ErrorBox error={error} />
      <dl className="kvlist">
        <dt>Quota</dt><dd>{eff.quota_bytes < 0 ? "none" : bytes(eff.quota_bytes)} <span className="faint small">(set at {eff.quota_from} level; each distinct layer counts once)</span></dd>
        <dt>Retention</dt><dd>{retention || <span className="faint">no rule: nothing is deleted automatically</span>}</dd>
        <dt>Immutable tags</dt><dd>{eff.immutable_tags?.length ? eff.immutable_tags.map((t) => <Mono key={t}>{t} </Mono>) : <span className="faint">none: any tag may be pushed again</span>}</dd>
        <dt>Pulled by runs of</dt><dd>{eff.pull_from?.length ? eff.pull_from.join(", ") : <span className="faint">only this project</span>}</dd>
      </dl>
      {editing && (
        <form className="card compform" onSubmit={save}>
          <label className="field">Quota in GiB (empty: leave as it is)<input value={form.quotaGiB} onChange={(e) => setForm({ ...form, quotaGiB: e.target.value })} inputMode="decimal" /></label>
          <label className="field">Keep the newest N tagged images (0: no limit)<input value={form.keepLast} onChange={(e) => setForm({ ...form, keepLast: e.target.value })} inputMode="numeric" /></label>
          <label className="field">Always keep tags matching (comma separated)<input value={form.keepTags} onChange={(e) => setForm({ ...form, keepTags: e.target.value })} placeholder="v*, release-*" className="mono" /></label>
          <label className="field">Delete untagged images older than N days (0: never)<input value={form.untagged} onChange={(e) => setForm({ ...form, untagged: e.target.value })} inputMode="numeric" /></label>
          <label className="field">Tags that cannot be overwritten or deleted once pushed<input value={form.immutable} onChange={(e) => setForm({ ...form, immutable: e.target.value })} placeholder="v*" className="mono" /></label>
          <div className="acts"><Button kind="pri" type="submit" disabled={busy}>Save the rules</Button><Button type="button" onClick={() => setEditing(false)}>Cancel</Button></div>
        </form>
      )}
      <div className="acts">
        <Button disabled={busy || !eff.has_retention} onClick={() => run(async () => { setDeleted(null); setPreview((await registryApi.retentionPreview(project)).candidates); })}>Preview what retention would delete</Button>
        {!eff.has_retention && <span className="faint small">Set a retention rule first.</span>}
      </div>
      {preview && (
        <div role="status">
          {preview.length === 0 ? <p>Nothing would be deleted: the rules keep every image.</p> : (
            <>
              <p><strong>{preview.length}</strong> {preview.length === 1 ? "image" : "images"} would be deleted ({bytes(preview.reduce((n, c) => n + c.size, 0))}{terms().say("). Images an {environment} runs are never deleted.")}</p>
              <ul className="plain">{preview.map((c) => <li key={c.repository + c.digest}><Mono>{c.repository}</Mono> {c.tags.length ? c.tags.join(", ") : c.digest.slice(0, 19)} <span className="faint small">{c.reason}</span></li>)}</ul>
              {admin
                ? <Button kind="bad" disabled={busy} onClick={() => { if (window.confirm(`Delete these ${preview.length} images? This cannot be undone.`)) run(async () => { const out = await registryApi.retentionRun(project); setDeleted(out.deleted.length); setPreview(null); onChanged(); }); }}>Delete these {preview.length}</Button>
                : <p className="faint small">Running retention needs the admin role on {project}.</p>}
            </>
          )}
        </div>
      )}
      {deleted !== null && <div className="callout callout-info" role="status">Deleted {deleted} {deleted === 1 ? "image" : "images"}. The bytes go at the next garbage collection.</div>}
    </Sheet>
  );
}

export function openRegistry(project?: string) { navigate(project ? `/images/registry/${encodeURIComponent(project)}` : "/images/registry"); }
