import { type CacheResult } from "../cacheApi";
import { bytes } from "./Security";
import { Link, Mono } from "./ui";
import "../caches.css";

const WORDS: Record<CacheResult["result"], string> = {
  hit: "Restored", fallback: "Restored an older entry", miss: "No entry", saved: "Saved", not_saved: "Not saved",
};

/** What happened in words, from the result alone. */
function sentence(c: CacheResult): string {
  const size = c.bytes ? bytes(c.bytes) : "";
  const took = c.seconds ? ` in ${c.seconds < 10 ? c.seconds.toFixed(1) : Math.round(c.seconds)} s` : "";
  switch (c.result) {
    case "hit": return `Restored ${size} from the entry with this step's key${took}.`;
    case "fallback": return `No entry had this step's key, so it started from the newest entry, ${size}${took}.`;
    case "miss": return "There was no entry to restore, so the step ran without it.";
    case "saved": return `Saved ${size} as a new entry${c.scope ? ` in ${c.scope}` : ""}${took}${c.restored === "fallback" ? "; it had started from an older entry" : ""}.`;
    case "not_saved": return "Nothing was saved.";
  }
}

// CacheResults shows what each of a step's build caches did in its latest
// attempt: the result, its size, and, when nothing was restored or saved,
// the reason the server or runner gave.
export function CacheResults({ project, caches }: { project: string; caches: CacheResult[] }) {
  return (
    <section className="callout callout-info cc-step" aria-label="Caches">
      <div><strong>Caches</strong> <Link href={`/projects/${encodeURIComponent(project)}/caches`}>All of {project}'s caches</Link></div>
      <ul>
        {caches.map((c) => (
          <li key={c.name}>
            <Mono>{c.name}</Mono>
            <span className={`cc-result cc-${c.result}`}>{WORDS[c.result] ?? c.result}</span>
            <span>{sentence(c)}</span>
            {c.key && <span className="faint mono small" title={c.key}>key {c.key.slice(0, 8)}…</span>}
            {c.reason && <span className="cc-reason">{c.result === "not_saved" ? "Why: " : ""}{c.reason}</span>}
          </li>
        ))}
      </ul>
    </section>
  );
}
