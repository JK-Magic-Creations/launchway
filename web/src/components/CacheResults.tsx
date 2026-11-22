import { type CacheResult } from "../cacheApi";
import { bytes } from "./Security";
import { Link, Mono } from "./ui";
import "../caches.css";
import { SpanFaintSmall, SpanMono } from "../kit/bits";

const WORDS: Record<CacheResult["result"], string> = {
  hit: "Restored", fallback: "Restored an older entry", miss: "No entry", expired: "Too old", saved: "Saved", not_saved: "Not saved",
};

/** A count of seconds as days, hours or minutes. */
function span(s: number): string {
  if (s >= 48 * 3600) return `${Math.floor(s / 86400)} days`;
  if (s >= 2 * 3600) return `${Math.floor(s / 3600)} hours`;
  if (s >= 120) return `${Math.floor(s / 60)} minutes`;
  return "under 2 minutes";
}

/** What happened in words, from the result alone. */
function sentence(c: CacheResult): string {
  const size = c.bytes ? bytes(c.bytes) : "";
  const took = c.seconds ? ` in ${c.seconds < 10 ? c.seconds.toFixed(1) : Math.round(c.seconds)} s` : "";
  switch (c.result) {
    case "hit": return `Restored ${size} from the entry with this step's key${took}.`;
    case "fallback": return `No entry had this step's key, so it started from the newest entry, ${size}${took}.`;
    case "miss": return "There was no entry to restore, so the step ran without it.";
    case "expired": return `The entry with this key was ${span(c.age_seconds ?? 0)} old, more than the ${span(c.max_age_seconds ?? 0)} this cache may be, so it was not restored and the step rebuilt it.`;
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
            {c.entry_key && (
              <SpanFaintSmall>
                {c.result === "expired" ? "entry found" : "entry restored"}: key <SpanMono title={c.entry_key}>{c.entry_key.slice(0, 8)}…</SpanMono>
                {c.generation ? `, generation ${c.generation}` : ""}{c.age_seconds !== undefined ? `, ${span(c.age_seconds)} old` : ""}
                {c.max_age_seconds ? ` (a cache may be ${span(c.max_age_seconds)})` : ""}
              </SpanFaintSmall>
            )}
            {c.saved_generation ? <SpanFaintSmall>saved generation {c.saved_generation}</SpanFaintSmall> : null}
            {c.key_parts && c.key_parts.length > 0 && (
              <details className="cc-key">
                <summary>What the key is made of</summary>
                <dl>
                  {c.key_parts.map((p, i) => (
                    <div key={i}><dt className="mono small">{p.label}</dt><dd className="mono small">{p.value}</dd></div>
                  ))}
                </dl>
              </details>
            )}
            {c.reason && <span className="cc-reason">{c.result === "not_saved" ? "Why: " : ""}{c.reason}</span>}
          </li>
        ))}
      </ul>
    </section>
  );
}
