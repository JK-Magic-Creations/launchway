import { useEffect, useState } from "react";
import { api, type ArtifactView, type ImageView, type RunDetail, type StepView } from "../api";
import { ago, clock } from "../format";
import { Empty, ErrorBox, Mono, StatusPill } from "../components/ui";
import { CopyValue, SeverityBar, SeverityLegend, bytes, copyText } from "../components/Security";
import { terms } from "../terms";
import {
  DIGEST_KEEP, ImageIcon, artifactDownload, findArtifact, keySourceText, levelWords, scanCounts, scanPolicy, shortImageRef,
} from "../components/Images";
import { BtnLink, DivFaintSmall, DivLoading, SpanFaint, SpanFaintSmall, SpanMono } from "../kit/bits";

type Show = (tab: "security" | "artifacts") => void;

/** The run's image steps, in the pipeline's order. */
export function imageSteps(run: RunDetail): StepView[] {
  return run.step_list.filter((s) => s.kind === "image");
}

// ImagesTab shows the container image each image step built: where it
// went, what it is made of, and what was checked and signed.
export function ImagesTab({ run, version, onStep, onShow }: {
  run: RunDetail; version: string; onStep: (step: string) => void; onShow: Show;
}) {
  const [images, setImages] = useState<ImageView[] | null>(null);
  const [arts, setArts] = useState<ArtifactView[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let alive = true;
    api.runImages(run.id, true).then((d) => { if (alive) { setImages(d.images ?? []); setError(null); } }).catch((e) => alive && setError(e));
    api.runArtifacts(run.id, true).then((d) => alive && setArts(d.artifacts)).catch(() => alive && setArts([]));
    return () => { alive = false; };
  }, [run.id, version]);

  if (error && !images) return <ErrorBox error={error} />;
  if (!images) return <DivLoading>Loading…</DivLoading>;
  const steps = imageSteps(run);
  if (steps.length === 0) {
    return <Empty title={terms().say("This {run} builds no images")}>No step of its pipeline is an image step.</Empty>;
  }
  const pushed = images.filter((i) => i.pushed).length;
  return (
    <div className="imgs">
      <p className="sub imgs-intro">
        {images.length === 0 ? "No image built yet." : <>{images.length === 1 ? `1 image built${pushed ? " and pushed" : ", not pushed"}.` : `${images.length} images built, ${pushed === images.length ? "all pushed" : pushed === 0 ? "none pushed" : `${pushed} pushed`}.`}</>}
        {" "}Each shows its latest attempt.
      </p>
      {steps.map((s) => {
        const img = images.find((i) => i.step === s.id);
        return img
          ? <ImageCard key={s.id} img={img} run={run} arts={arts} onStep={onStep} onShow={onShow} />
          : <NoImage key={s.id} step={s} onStep={onStep} />;
      })}
    </div>
  );
}

// NoImage stands for an image step that has no image (yet), and says why.
function NoImage({ step, onStep }: { step: StepView; onStep: (s: string) => void }) {
  const why = step.status === "pending" ? `Builds once ${step.needs?.length ? step.needs.join(", ") : "the steps it needs"} ${step.needs && step.needs.length > 1 ? "have" : "has"} succeeded.`
    : step.status === "queued" ? terms().say("Waiting for a {runner}.")
    : step.status === "running" || step.status === "leased" ? "Building now; the image shows here when the step ends."
    : step.status === "skipped" ? `${step.error || "Skipped"}: nothing was built.`
    : step.status === "canceled" ? "Canceled before an image was built."
    : step.status === "failed" ? `It failed before an image was built${step.error ? `: ${step.error}` : "."}`
    : "No image was recorded for this step.";
  return (
    <section className="card imgcard" aria-labelledby={`img-${step.id}`}>
      <div className="imgcard-head">
        <h3 id={`img-${step.id}`} className="imgcard-title">
          <ImageIcon size={16} />
          <BtnLink onClick={() => onStep(step.id)}>{step.id}</BtnLink>
        </h3>
        <StatusPill status={step.status} />
      </div>
      <p className={step.status === "failed" ? "imgcard-why tfile-err" : "imgcard-why sub"}>{why}</p>
      {step.status === "failed" && <p className="sub small">Its log says more: <BtnLink onClick={() => onStep(step.id)}>open {step.id}</BtnLink>.</p>}
    </section>
  );
}

function ImageCard({ img, run, arts, onStep, onShow }: {
  img: ImageView; run: RunDetail; arts: ArtifactView[] | null; onStep: (s: string) => void; onShow: Show;
}) {
  const platforms = img.platforms ?? [];
  const tags = img.tags ?? [];
  const sbomArt = findArtifact(arts, img.step, img.sbom_artifact);
  const provArt = findArtifact(arts, img.step, img.provenance_artifact);
  const description = run.step_list.find((s) => s.id === img.step)?.description;
  return (
    <section className={"card imgcard" + (img.outcome === "failed" ? " imgcard-bad" : "")} aria-labelledby={`img-${img.step}`}>
      <div className="imgcard-head">
        <h3 id={`img-${img.step}`} className="imgcard-title">
          <ImageIcon size={16} />
          <BtnLink onClick={() => onStep(img.step)}>{img.step}</BtnLink>
        </h3>
        <StatusPill status={img.outcome} />
        <span className="imgbadges">
          {img.pushed ? <span className="artst artst-kept">Pushed</span> : <span className="artst">Not pushed</span>}
          {img.signed ? <span className="artst artst-kept">Signed</span> : <span className="artst">Not signed</span>}
        </span>
        <span className="faint small imgcard-when" title={clock(img.created_at)}>
          {img.attempt > 1 ? `attempt ${img.attempt}, ` : ""}{ago(img.created_at)}
        </span>
      </div>
      {description && <p className="sub imgcard-desc">{description}</p>}

      <div className="imgdigest">
        <span className="imgdigest-label">Digest</span>
        <CopyValue value={img.digest} label={`digest of ${img.step}`} keep={DIGEST_KEEP} />
        <SpanFaintSmall>{platforms.length > 1 ? `an index of ${platforms.length} platforms` : "one platform"}, {bytes(img.size)}</SpanFaintSmall>
      </div>

      {img.scan?.failed && (
        <div className="callout callout-bad">
          <strong>{terms().say("The {scan} failed the step.")}</strong> The image was built but has findings at {levelWords(img.scan.fail_on)}, and the scan is enforced.
        </div>
      )}

      <dl className="kv imgfacts">
        <dt>Image</dt>
        <dd>
          {img.pushed && img.image
            ? <span className="copyval"><span className="mono imgref" title={img.image}>{shortImageRef(img.image)}</span><CopyButton value={img.image} label={`image reference of ${img.step}`} /></span>
            : <span>Not pushed{img.not_pushed ? `: ${img.not_pushed}` : ""}.</span>}
        </dd>
        <dt>Tags</dt>
        <dd>
          {tags.length ? (
            <ul className="imgtags">
              {tags.map((t) => <li key={t}><SpanMono>{t}</SpanMono></li>)}
            </ul>
          ) : <SpanFaint>none</SpanFaint>}
          {tags.length > 0 && !img.pushed && <DivFaintSmall>{terms().say("Not pushed, so no {registry} has these tags.")}</DivFaintSmall>}
        </dd>
        <dt>Size</dt><dd>{bytes(img.size)} compressed{platforms.length > 1 ? ", every platform together" : ""}</dd>
        <dt>Signature</dt>
        <dd>
          {img.signed
            ? <>Signed with key <Mono>{img.key_id}</Mono>, {keySourceText(img.key_source)}</>
            : "Not signed"}
          {img.signatures && img.signatures.length > 0 && (
            <details className="imgsigs">
              <summary>{img.signatures.length} signature tag{img.signatures.length === 1 ? "" : "s"} written</summary>
              <ul className="imgtags">{img.signatures.map((s) => <li key={s}><SpanMono>{s}</SpanMono></li>)}</ul>
            </details>
          )}
        </dd>
        <dt>Provenance</dt>
        <dd>
          {img.provenance === "attached" ? terms().say("Signed and attached to the image, and kept as an {artifact}")
            : img.provenance === "stored" ? terms().say("Kept as an {artifact} only")
            : "None"}
          {img.provenance && <ArtifactLinks art={provArt} name={img.provenance_artifact} arts={arts} onShow={onShow} />}
        </dd>
        <dt>SBOM</dt>
        <dd>
          {img.sbom ? "Made from the image" : "None"}
          {img.sbom && <ArtifactLinks art={sbomArt} name={img.sbom_artifact} arts={arts} onShow={onShow} />}
          {img.sbom && <> · <BtnLink onClick={() => onShow("security")}>See its components</BtnLink></>}
        </dd>
      </dl>

      {platforms.length > 0 && (
        <div className="imgsec">
          <h4 className="finding-h">Platforms</h4>
          <div className="table-scroll">
            <table className="inputs-table imgplat">
              <thead><tr><th>Platform</th><th>Digest</th><th>Size</th></tr></thead>
              <tbody>
                {platforms.map((p) => (
                  <tr key={p.digest + p.platform}>
                    <td className="mono">{p.platform}</td>
                    <td><CopyValue value={p.digest} label={`digest of ${p.platform}`} keep={DIGEST_KEEP} /></td>
                    <td className="art-size">{bytes(p.size)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {img.scan && <ScanSummary img={img} onShow={onShow} />}

      <div className="imgsec">
        <h4 className="finding-h">Build</h4>
        <dl className="kv imgfacts">
          {img.builders && img.builders.length > 0 && <><dt>Built by</dt><dd>{img.builders.join(", ")}</dd></>}
          {img.steps !== undefined && img.steps > 0 && (
            <><dt>Steps</dt><dd>{img.steps} build step{img.steps === 1 ? "" : "s"}, {img.cached ?? 0} of {img.steps} from the cache</dd></>
          )}
          <dt>Cache</dt><dd>{img.cache ? <span className="mono imgref">{img.cache}</span> : <SpanFaint>none</SpanFaint>}</dd>
          <dt>Base images</dt>
          <dd>
            {img.base_images?.length ? (
              <ul className="imgtags">{img.base_images.map((b) => <li key={b}><SpanMono title={b}>{baseImage(b)}</SpanMono></li>)}</ul>
            ) : <SpanFaint>not recorded</SpanFaint>}
          </dd>
        </dl>
      </div>
    </section>
  );
}

// ScanSummary shows the image's vulnerabilities by severity, and what the
// scan's policy did with them.
function ScanSummary({ img, onShow }: { img: ImageView; onShow: Show }) {
  const s = img.scan!;
  const c = scanCounts(s);
  return (
    <div className={"imgsec imgscan" + (s.failed ? " imgscan-bad" : "")}>
      <div className="imgscan-head">
        <h4 className="finding-h">Vulnerabilities</h4>
        {s.failed
          ? <span className="artst artst-bad">Failed the step</span>
          : s.mode === "enforce" ? <span className="artst artst-kept">Passed</span> : null}
        <SpanFaintSmall>{scanPolicy(s)}</SpanFaintSmall>
      </div>
      {c.total > 0 ? (
        <>
          <SeverityBar c={c} />
          <SeverityLegend c={c} />
        </>
      ) : <p className="sub imgscan-none">The scan found nothing.</p>}
      {c.total > 0 && (
        <p className="sub small">
          Every finding is on the <BtnLink onClick={() => onShow("security")}>Security tab</BtnLink>, found by Trivy.
        </p>
      )}
    </div>
  );
}

// ArtifactLinks offers the artifact that keeps a file: download it, or see it on the Artifacts tab.
function ArtifactLinks({ art, name, arts, onShow }: { art?: ArtifactView; name?: string; arts: ArtifactView[] | null; onShow: Show }) {
  if (!name || !arts) return null;
  if (!art) return <SpanFaint>, not kept</SpanFaint>;
  if (art.status !== "kept") return <SpanFaint> {terms().say("· the {artifact} is")} {art.status === "failed" ? "not kept" : art.status}</SpanFaint>;
  return (
    <> · <a href={artifactDownload(art)} download aria-label={terms().say(`Download the ${name} {artifact}`)}>Download</a>
      {" "}· <BtnLink onClick={() => onShow("artifacts")}>{terms().say("See in {Artifacts}")}</BtnLink></>
  );
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await copyText(value);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };
  return <button type="button" className="linkbtn copyval-btn" onClick={copy} aria-label={`Copy ${label}`}>{copied ? "Copied" : "Copy"}</button>;
}

/** "pkg:docker/alpine@3.22?platform=linux%2Famd64" as "alpine 3.22 (linux/amd64)". */
function baseImage(purl: string): string {
  const m = /^pkg:docker\/([^@?]+)(?:@([^?]+))?(?:\?(.*))?$/.exec(purl);
  if (!m) return purl;
  const name = decodeURIComponent(m[1]);
  const version = m[2] ? decodeURIComponent(m[2]) : "";
  const platform = new URLSearchParams(m[3] ?? "").get("platform");
  return `${name}${version ? ` ${version}` : ""}${platform ? ` (${platform})` : ""}`;
}
