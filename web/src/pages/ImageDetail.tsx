import { useCallback } from "react";
import { api, type ImageHit, type ImageView } from "../api";
import { CopyValue } from "../components/Security";
import { EntityLink } from "../components/EntityLink";
import { ErrorBox, Mono } from "../components/ui";
import { ago, clock, shortSha } from "../format";
import { useInstallation } from "../guided/facts";
import { useLoad } from "../guided/useLoad";
import { Chip, StateChip } from "../kit/Status";
import { EmptyState, LinkButton, PageHead, Sheet } from "../kit/layout";
import { useCrumbLabel } from "../nav/crumbs";
import { onLink } from "../router";
import { terms } from "../terms";
import { refName } from "../refs";

const LEVELS: { key: "critical" | "high" | "medium" | "low" | "unknown"; label: string }[] = [
  { key: "critical", label: "Critical" }, { key: "high", label: "High" }, { key: "medium", label: "Medium" }, { key: "low", label: "Low" }, { key: "unknown", label: "Unknown" },
];

/** One image, found by its digest: which run and commit built it, what the scan found by severity, whether it has a
 * bill of materials and a signature, and where it is deployed when a deployment names it. */
export function ImageDetailPage({ digest }: { digest: string }) {
  useCrumbLabel(digest.slice(0, 19));
  const inst = useInstallation();
  const hitsFn = useCallback(() => api.images({ digest }), [digest]);
  const hits = useLoad<{ hits: ImageHit[] }>(hitsFn);
  const first = hits.data?.hits.slice().sort((a, b) => a.created_at - b.created_at)[0];
  const runFn = useCallback(() => (first ? api.runImages(first.run_id) : Promise.resolve(null)), [first]);
  const imgs = useLoad<{ images: ImageView[] } | null>(runFn);
  const iv = imgs.data?.images.find((i) => i.step === first?.step && i.digest === digest) ?? imgs.data?.images.find((i) => i.step === first?.step);
  const project = inst.projects.find((p) => p.name === first?.project);
  const tags = [...new Set([...(iv?.tags ?? []), ...(hits.data?.hits ?? []).flatMap((h) => (h.tag ? [`${h.repository}:${h.tag}`] : []))])];
  const tagNames = tags.map((t) => t.slice(t.lastIndexOf(":") + 1));
  const where = inst.environments.flatMap((e) => e.services.filter((u) => u.current && (tagNames.includes(u.current.version) || u.current.version === digest)).map((u) => ({ env: e.name, u })));

  if (hits.loading) return <div className="page"><div className="loading">Loading…</div></div>;
  if (!first) {
    return <div className="page"><PageHead eyebrow={terms().say("{Registry}")} title="Image" /><ErrorBox error={hits.error} />
      <Sheet><EmptyState title={terms().say("No {run} built this digest")} action={<LinkButton href="/depot" icon="arrow">Look for another image</LinkButton>}>{terms().say("Launchway knows an image when one of its {runs} built it. Check the digest, or search by repository or {project}.")}</EmptyState></Sheet></div>;
  }
  const scan = iv?.scan;
  const worst = scan ? Math.max(scan.critical + scan.high, 0) : 0;
  const total = scan ? scan.critical + scan.high + scan.medium + scan.low + scan.unknown : 0;
  return (
    <div className="page">
      <PageHead eyebrow={<>Images · {first.repository}</>} title={<span className="mono" style={{ fontSize: "0.72em" }}>{digest.slice(0, 19)}…</span>}
        sentence={<>Built by <EntityLink kind="project" name={first.project} /> <EntityLink kind="run" name={terms().say(`{run} #${first.run_number}`)} id={first.run_id} />, step {first.step}, {ago(first.created_at)}. {scan ? (worst > 0 ? <b>{worst} critical or high findings.</b> : total > 0 ? "No critical or high findings." : "The scan found nothing.") : "No scan reported for it."}</>}
        actions={<LinkButton href={`/runs/${first.run_id}?tab=images&step=${encodeURIComponent(first.step)}`} icon="arrow">{terms().say("Open the {run}")}</LinkButton>} />
      <div className="grid g2">
        <Sheet title="Where it came from">
          <dl className="kvlist">
            <dt>Digest</dt><dd><CopyValue value={digest} label="digest" keep={22} /></dd>
            <dt>Built by</dt><dd><EntityLink kind="run" name={`${first.project} #${first.run_number}`} id={first.run_id} /> on {refName(first.ref)}</dd>
            <dt>Commit</dt><dd>{project?.last_run?.id === first.run_id && project.last_run.sha ? <Mono>{shortSha(project.last_run.sha)}</Mono> : <span className="faint">{terms().say("see the {run}")}</span>}</dd>
            <dt>When</dt><dd title={clock(first.created_at)}>{ago(first.created_at)}</dd>
            <dt>Tags</dt><dd>{tags.length ? tags.map((t) => <div key={t} className="mono">{t}</div>) : <span className="faint">none recorded</span>}</dd>
            <dt>Built again by</dt><dd>{new Set(hits.data!.hits.map((h) => h.run_id)).size - 1} later runs</dd>
          </dl>
        </Sheet>
        <Sheet title="What protects it">
          <dl className="kvlist">
            <dt>Pushed</dt><dd>{first.pushed ? <StateChip state="ok">{terms().say("Pushed to the {registry}")}</StateChip> : <StateChip state="skip">Not pushed{iv?.not_pushed ? `: ${iv.not_pushed}` : ""}</StateChip>}</dd>
            <dt>Signature</dt><dd>{iv ? (iv.signed ? <StateChip state="ok">Signed{iv.key_source ? ` (${iv.key_source})` : ""}</StateChip> : <StateChip state="warn">Not signed</StateChip>) : <span className="faint">not known</span>}</dd>
            <dt>Provenance</dt><dd>{iv?.provenance === "attached" ? "Attached to the image" : iv?.provenance === "stored" ? terms().say("Kept as an {artifact}") : "None"}</dd>
            <dt>SBOM</dt><dd>{iv?.sbom ? <>Yes{iv.sbom_artifact ? <>, kept as <Mono>{iv.sbom_artifact}</Mono></> : null}. <a href="/depot/components" onClick={onLink}>Search components</a></> : "No bill of materials was made."}</dd>
          </dl>
        </Sheet>
      </div>
      <Sheet title="Findings by severity" actions={scan ? <Chip tone={worst > 0 ? "bad" : "ok"}>{worst > 0 ? `${worst} to fix first` : "none critical or high"}</Chip> : undefined}>
        {!scan ? <p className="sh-b faint">The run did not scan this image. Add a scan to the image step and the findings appear here.</p> : (
          <div className="sh-b bars">
            {LEVELS.map((l) => (
              <div className="bar" key={l.key}><span>{l.label}</span><span className="track"><i className={`fill ${l.key}`} style={{ display: "block", width: `${total ? Math.max(scan[l.key] ? 4 : 0, (scan[l.key] / total) * 100) : 0}%` }} /></span><b>{scan[l.key]}</b></div>
            ))}
            {scan.mode && <p className="faint small" style={{ margin: "6px 0 0" }}>The scan {scan.mode === "enforce" ? "blocks the step" : "only reports"}{scan.fail_on ? ` at ${scan.fail_on} and above` : ""}.</p>}
          </div>
        )}
      </Sheet>
      <Sheet title={terms().say("Where it is {deploy:did}")}>
        {where.length === 0 ? <p className="sh-b faint">{terms().say("No deployment is known to run this image. Launchway matches a deployment's version to the image's tags, so an image {deploy:did} under another version name is not found.")}</p> : (
          <ul className="rowlist">{where.map(({ env, u }) => <li key={env + u.service}><div className="main"><div className="nm"><EntityLink kind="environment" name={env} /></div><div className="sb">{u.service} of <EntityLink kind="project" name={u.project} /> runs {u.current?.version}</div></div></li>)}</ul>
        )}
      </Sheet>
    </div>
  );
}
