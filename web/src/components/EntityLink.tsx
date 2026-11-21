import type { ReactNode } from "react";
import { entityHref, KIND_WORD, type EntityRef } from "../entities";
import { onLink } from "../router";

/** EntityLink writes the name of a thing as a link to that thing's own page.
 * Every name on every screen goes through it: a name that is plain text is a
 * dead end (docs/37 section g). Children replace the name as the link's text. */
export function EntityLink({ children, className, mono, ...e }: EntityRef & { children?: ReactNode; className?: string; mono?: boolean }) {
  const cls = [className, mono ? "mono" : "", "entity-link"].filter(Boolean).join(" ");
  return (
    <a href={entityHref(e)} onClick={onLink} className={cls} data-entity={e.kind} title={`Open the ${KIND_WORD[e.kind]} ${e.name}`}>
      {children ?? e.name}
    </a>
  );
}

/** Linked writes a sentence that mentions things by name, with each name a link to its page.
 * Names are found as whole words, longest first, so "shop-api-dev" is not cut at "shop-api". */
export function Linked({ text, entities }: { text: string; entities: EntityRef[] }) {
  return <>{linkify(text, entities).map((p, i) => typeof p === "string" ? p : <EntityLink key={i} {...p} />)}</>;
}

export function linkify(text: string, entities: EntityRef[]): (string | EntityRef)[] {
  const byName = [...entities].filter((e) => e.name).sort((a, b) => b.name.length - a.name.length);
  if (byName.length === 0) return [text];
  const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(`(?<![\\w-])(${byName.map((e) => esc(e.name)).join("|")})(?![\\w-])`, "g");
  const out: (string | EntityRef)[] = [];
  let last = 0;
  for (const m of text.matchAll(re)) {
    const e = byName.find((x) => x.name === m[1])!;
    if (m.index! > last) out.push(text.slice(last, m.index));
    out.push(e);
    last = m.index! + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
