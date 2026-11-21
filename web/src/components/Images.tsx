import type { ArtifactView, FindingCounts, ImageBuild, ImageScan, ImageValue, ScanLevel, SecretRef } from "../api";
import { Mono } from "./ui";
import { artifactArchiveURL, artifactFileURL } from "./Security";
import { terms } from "../terms";

// Pieces shared by the run's Images tab, an image step's panel, the graph
// and the image search.

/** A container image: a stack of layers. */
export function ImageIcon({ size = 13 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true" className="ic-line">
      <path d="M8 1.8l6 3-6 3-6-3z" /><path d="M2 8l6 3 6-3" /><path d="M2 11.2l6 3 6-3" />
    </svg>
  );
}

/** How many characters of a digest are shown: "sha256:" and 12 hex digits. */
export const DIGEST_KEEP = 19;

/** "sha256:197268899c7f…". */
export function shortDigest(d: string): string {
  return d.length > DIGEST_KEEP ? d.slice(0, DIGEST_KEEP) + "…" : d;
}

/** host/repo@sha256:… with the digest shortened. */
export function shortImageRef(ref: string): string {
  const at = ref.indexOf("@");
  return at < 0 ? ref : ref.slice(0, at + 1) + shortDigest(ref.slice(at + 1));
}

const RUN_FACT: Record<string, string> = {
  sha: "the commit id", short_sha: "the short commit id", branch: "the branch", tag: "the Git tag", number: "the run number",
};

/** A value as the pipeline writes it: text as it is, anything else in words. */
export function ImageValueText({ v }: { v: ImageValue }) {
  if (v.literal !== undefined) return <Mono>{v.literal}</Mono>;
  if (v.output) return <span className="imgval">output <Mono>{v.output.key}</Mono> of {v.output.step}</span>;
  if (v.run) return <span className="imgval">{RUN_FACT[v.run] ?? v.run}</span>;
  if (v.input !== undefined) return <span className="imgval">input <Mono>{v.input}</Mono></span>;
  return <span className="faint">empty</span>;
}

/** "critical", "high or worse". */
export function levelWords(l?: ScanLevel): string {
  if (!l) return "";
  return l === "critical" ? "critical" : `${l} or worse`;
}

/** What the scan does with what it finds, in words. */
export function scanPolicy(s: { fail_on?: ScanLevel; mode?: string }): string {
  if (s.mode === "enforce" && s.fail_on) return `Fails the step on ${levelWords(s.fail_on)}, enforced`;
  if (s.fail_on) return `Report only: ${levelWords(s.fail_on)} would fail the step if enforced`;
  return "Report only";
}

export function scanCounts(s: ImageScan): FindingCounts {
  return { total: s.critical + s.high + s.medium + s.low + s.unknown, critical: s.critical, high: s.high, medium: s.medium, low: s.low, unknown: s.unknown };
}

/** Where a signing key comes from, in words. */
export function keySourceText(src?: string): string {
  if (!src || src === "runner") return terms().say("the {runner}'s key");
  return `from the secret ${src}`;
}

function refText(r: SecretRef): string {
  return `${r.store}:${r.key}${r.field ? `#${r.field}` : ""}`;
}

/** The artifact of a run's step by name, if listed. */
export function findArtifact(arts: ArtifactView[] | null, step: string, name?: string): ArtifactView | undefined {
  if (!arts || !name) return undefined;
  return arts.find((a) => a.step === step && a.name === name);
}

/** Where to download an artifact: its file if it has one, else the .zip. */
export function artifactDownload(a: ArtifactView): string {
  return a.files.length === 1 && a.file_count === 1 ? artifactFileURL(a, a.files[0].path) : artifactArchiveURL(a);
}

/** What an image step builds, as its pipeline says: a list of facts for a <dl>. */
export function ImageBuildFacts({ b }: { b: ImageBuild }) {
  const args = Object.entries(b.build_args ?? {}).sort(([x], [y]) => x.localeCompare(y));
  const push = b.push !== false;
  return (
    <>
      <dt>Context</dt><dd><Mono>{b.context || "."}</Mono></dd>
      <dt>Dockerfile</dt><dd><Mono>{b.file || "Dockerfile"}</Mono>{b.target && <> · target <Mono>{b.target}</Mono></>}</dd>
      <dt>Platforms</dt><dd>{b.platforms?.length ? b.platforms.map((p, i) => <span key={p}>{i > 0 && ", "}<Mono>{p}</Mono></span>) : <span className="faint">{terms().say("the {runner}'s own")}</span>}</dd>
      {args.length > 0 && (
        <><dt>Build args</dt><dd>{args.map(([k, v]) => <div key={k}><Mono>{k}</Mono> = <ImageValueText v={v} /></div>)}</dd></>
      )}
      <dt>Tags</dt>
      <dd>
        {b.tags.length ? b.tags.map((t, i) => (
          <div key={i}><Mono>{t.registry}/{t.repository}:</Mono>{t.tag.literal !== undefined ? <Mono>{t.tag.literal}</Mono> : <> <ImageValueText v={t.tag} /></>}</div>
        )) : <span className="faint">none</span>}
      </dd>
      <dt>Push</dt><dd>{push ? "yes, except in pull-request runs" : "no: built and checked only"}</dd>
      {b.cache && <><dt>Cache</dt><dd><Mono>{b.cache.registry}/{b.cache.repository}</Mono></dd></>}
      <dt>SBOM</dt><dd>{b.sbom ? "yes" : "no"}</dd>
      <dt>Provenance</dt><dd>{b.provenance ? (b.sign ? "yes, signed and attached to the image" : terms().say("yes, kept as an {artifact}")) : "no"}</dd>
      <dt>Signing</dt>
      <dd>
        {!b.sign ? "not signed"
          : b.sign.key ? <>with the key in <Mono>{refText(b.sign.key)}</Mono>{b.sign.password && <>, password in <Mono>{refText(b.sign.password)}</Mono></>}</>
          : terms().say("with the {runner}'s key")}
      </dd>
      <dt>Scan</dt><dd>{b.scan ? scanPolicy(b.scan) : "no"}</dd>
    </>
  );
}
