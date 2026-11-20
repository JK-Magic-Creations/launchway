import { useEffect, useState } from "react";
import { api, type ImageHit, type ImageQuery } from "../api";
import { ago, clock } from "../format";
import { navigate } from "../router";
import { Empty, ErrorBox, Link, StatusPill } from "../components/ui";
import { CopyValue } from "../components/Security";
import { DIGEST_KEEP, shortDigest } from "../components/Images";

type By = "digest" | "repository" | "project";

const BY: [By, string][] = [["digest", "Digest"], ["repository", "Repository"], ["project", "Project"]];

interface Asked { by: By; value: string; tag: string }

function params() { return new URLSearchParams(location.search); }

function askedFromURL(): Asked {
  const p = params();
  if (p.get("digest")) return { by: "digest", value: p.get("digest") ?? "", tag: "" };
  if (p.get("repository")) return { by: "repository", value: p.get("repository") ?? "", tag: p.get("tag") ?? "" };
  if (p.get("project")) return { by: "project", value: p.get("project") ?? "", tag: "" };
  return { by: "digest", value: "", tag: "" };
}

function refName(ref: string): string { return ref.replace(/^refs\/(heads|tags)\//, ""); }

// tidy reads what was typed the way people paste it: an image reference
// host/repo@sha256:… is its digest, host/repo:tag a repository and tag.
function tidy(by: By, value: string, tag: string): { value: string; tag: string } {
  let v = value.trim(), t = tag.trim();
  if (by === "digest") {
    const at = v.indexOf("@sha256:");
    if (at >= 0) v = v.slice(at + 1);
    return { value: v.toLowerCase(), tag: "" };
  }
  if (by === "repository") {
    const at = v.indexOf("@");
    if (at >= 0) v = v.slice(0, at);
    const colon = v.lastIndexOf(":");
    if (colon > v.lastIndexOf("/") && v.lastIndexOf("/") >= 0 && !t) { t = v.slice(colon + 1); v = v.slice(0, colon); }
    return { value: v, tag: t };
  }
  return { value: v, tag: "" };
}

const PLACEHOLDER: Record<By, string> = {
  digest: "sha256:… or host/repo@sha256:…",
  repository: "registry.example.com/team/shop",
  project: "shop",
};

// ImagesPage finds the container images runs built: which run built a
// digest, the latest image of a repository, or a project's images.
export function ImagesPage() {
  const [asked, setAsked] = useState<Asked>(askedFromURL);
  const [by, setBy] = useState<By>(asked.by);
  const [value, setValue] = useState(asked.value);
  const [tag, setTag] = useState(asked.tag);
  const [hits, setHits] = useState<ImageHit[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const on = () => {
      const a = askedFromURL();
      setAsked(a); setBy(a.by); setValue(a.value); setTag(a.tag);
    };
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  useEffect(() => {
    if (!asked.value) { setHits(null); setError(null); return; }
    let alive = true;
    setBusy(true);
    const q: ImageQuery = asked.by === "repository" ? { repository: asked.value, tag: asked.tag || undefined } : { [asked.by]: asked.value };
    api.images(q)
      .then((d) => { if (alive) { setHits(d.hits ?? []); setError(null); } })
      .catch((e) => { if (alive) { setError(e); setHits(null); } })
      .finally(() => alive && setBusy(false));
    return () => { alive = false; };
  }, [asked]);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const t = tidy(by, value, tag);
    if (by === "digest" && !t.value.startsWith("sha256:")) {
      setProblem("A digest starts with sha256:, such as sha256:1972…, or paste the whole image reference.");
      return;
    }
    setProblem("");
    setValue(t.value); setTag(t.tag);
    const q = new URLSearchParams();
    q.set(by, t.value);
    if (by === "repository" && t.tag) q.set("tag", t.tag);
    navigate(`/library/images?${q.toString()}`);
  };

  const choose = (b: By) => { setBy(b); setProblem(""); if (b !== by) { setValue(""); setTag(""); } };
  const latest = asked.by === "repository" && hits && hits.length > 0 ? hits[0] : null;

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Images</h1>
          <p className="sub">Find the container images your runs built: which run built a digest, the latest image of a repository, or every image of a project.</p>
        </div>
      </header>

      <form className="card compform imgform" role="search" onSubmit={submit} noValidate>
        <div className="imgform-by">
          <span className="imgform-by-label" id="img-by">Find by</span>
          <div className="chips" role="group" aria-labelledby="img-by">
            {BY.map(([k, label]) => (
              <button key={k} type="button" className={"chip" + (by === k ? " chip-on" : "")} aria-pressed={by === k} onClick={() => choose(k)}>{label}</button>
            ))}
          </div>
        </div>
        <label className="field imgform-value">
          {by === "digest" ? "Digest" : by === "repository" ? "Repository" : "Project"}
          <input value={value} onChange={(e) => { setValue(e.target.value); setProblem(""); }} placeholder={PLACEHOLDER[by]}
            required maxLength={500} spellCheck={false} autoCapitalize="off" autoComplete="off" autoFocus={!asked.value}
            className={by === "project" ? undefined : "mono"} aria-invalid={problem ? true : undefined} aria-describedby={problem ? "img-problem" : undefined} />
        </label>
        {by === "repository" && (
          <label className="field imgform-tag">
            <span>Tag <span className="faint">(optional)</span></span>
            <input value={tag} onChange={(e) => setTag(e.target.value)} placeholder="any tag" maxLength={200} spellCheck={false} autoCapitalize="off" autoComplete="off" className="mono" />
          </label>
        )}
        <button type="submit" className="btn btn-primary" disabled={!value.trim() || busy}>{busy ? "Searching…" : "Search"}</button>
        {problem && <p className="tfile-err compform-note" id="img-problem" role="alert">{problem}</p>}
        <p className="sub small compform-note">
          {by === "digest" ? "The digest of an image or of one of its platforms; newest runs first."
            : by === "repository" ? "The repository without a tag, such as host/team/app; the newest image first."
            : "The project's name; its newest images first."}
        </p>
      </form>

      <ErrorBox error={error} />
      {!asked.value && !error && (
        <Empty title="Search the images your runs built">
          Paste a digest to find the run that built it, or give a repository to see its latest image.
        </Empty>
      )}
      {hits && hits.length === 0 && (
        <Empty title="Not found">
          {asked.by === "digest" ? <>No run built an image with the digest <span className="mono imgref">{shortDigest(asked.value)}</span>.</>
            : asked.by === "repository" ? <>No run built an image for <span className="mono imgref">{asked.value}{asked.tag ? `:${asked.tag}` : ""}</span>. The repository must match exactly, registry host included.</>
            : <>The project <strong>{asked.value}</strong> has built no images, or does not exist.</>}
        </Empty>
      )}
      {hits && hits.length > 0 && (
        <>
          {latest && (
            <section className="card" aria-label="Latest image">
              <h2 className="h-sm">Latest image of <span className="mono imgref">{latest.repository}{asked.tag ? `:${asked.tag}` : ""}</span></h2>
              <p className="imglatest">
                <CopyValue value={latest.digest} label="digest" keep={DIGEST_KEEP} />
                <span className="faint">built by <Link href={runHref(latest)}>{latest.project} #{latest.run_number}</Link> ({latest.step}) on {refName(latest.ref)}, {ago(latest.created_at)}</span>
              </p>
            </section>
          )}
          {asked.by === "digest" && <DigestSummary hits={hits} />}
          <section aria-label="Images found">
            <div className="table-scroll card card-flush">
              <table className="inputs-table comptable imgtable">
                <thead><tr><th>Project</th><th>Run</th><th>Step</th><th>Branch</th><th>Image</th><th>Digest</th><th>State</th><th>When</th></tr></thead>
                <tbody>
                  {hits.map((h, i) => (
                    <tr key={h.run_id + h.step + h.kind + (h.tag ?? "") + (h.platform ?? "") + i}>
                      <td>{h.project}</td>
                      <td><Link href={runHref(h)}>#{h.run_number}</Link>{h.attempt > 1 && <span className="faint small"> attempt {h.attempt}</span>}</td>
                      <td>{h.step}</td>
                      <td>{refName(h.ref)}</td>
                      <td className="imgtable-ref">
                        <span className="mono imgref">{h.repository}{h.kind === "tag" && h.tag ? `:${h.tag}` : ""}</span>
                        {h.kind === "platform" && <span className="faint small"> platform {h.platform}</span>}
                      </td>
                      <td><CopyValue value={h.digest} label={`digest of #${h.run_number} ${h.step}`} keep={DIGEST_KEEP} /></td>
                      <td>
                        <span className="imgbadges">
                          <StatusPill status={h.outcome} />
                          {h.pushed ? <span className="artst artst-kept">Pushed</span> : <span className="artst">Not pushed</span>}
                          {h.signed ? <span className="artst artst-kept">Signed</span> : <span className="artst">Not signed</span>}
                        </span>
                      </td>
                      <td title={clock(h.created_at)}>{ago(h.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </div>
  );
}

function runHref(h: ImageHit): string {
  return `/runs/${h.run_id}?tab=images&step=${encodeURIComponent(h.step)}`;
}

// DigestSummary answers a digest search in a sentence: which run built it.
function DigestSummary({ hits }: { hits: ImageHit[] }) {
  const runs = [...new Map(hits.map((h) => [h.run_id + "/" + h.step, h])).values()];
  const h = runs[runs.length - 1]; // the oldest: the run that first built it
  const platform = hits.find((x) => x.kind === "platform");
  return (
    <section className="card" aria-label="Who built it">
      <h2 className="h-sm">
        {runs.length === 1 ? "Built by one run" : `Built by ${runs.length} runs`}
        {platform ? <>, as the <span className="mono">{platform.platform}</span> part of an image</> : null}
      </h2>
      <p className="sub">
        First by <Link href={runHref(h)}>{h.project} #{h.run_number}</Link>, step {h.step}, on {refName(h.ref)}, {ago(h.created_at)}
        {runs.length > 1 && <>; later runs built the same image again</>}.
      </p>
    </section>
  );
}
