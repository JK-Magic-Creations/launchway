// Pull requests that wait on a person, as entries of "Needs you" on Home. The server lists them
// (GET /api/v1/pulls/inbox: a review asked of you or your groups, changes asked on your own pull request,
// your own taken out of a merge queue); this only turns them into the same plain sentences Home shows.

import { useEffect, useState } from "react";
import { call } from "../api";
import type { Need } from "./needs";

export interface InboxItem { repository: string; number: number; title: string; kind: string; why: string }

const BUTTON: Record<string, string> = { review_requested: "Review", changes_requested: "See the changes asked", queue_failed: "See why" };

export function pullNeeds(items: InboxItem[]): Need[] {
  return items.map((it) => ({
    key: `pull:${it.repository}#${it.number}:${it.kind}`,
    tone: it.kind === "queue_failed" ? "bad" : "warn",
    icon: it.kind === "queue_failed" ? "alert" : "gate",
    title: it.kind === "review_requested" ? `Review wanted on ${it.repository}#${it.number}` : it.kind === "changes_requested" ? `Changes asked on ${it.repository}#${it.number}` : `${it.repository}#${it.number} left the merge queue`,
    entities: [],
    detail: `${it.title}: ${it.why}.`,
    href: `/repositories/${it.repository}/pulls/${it.number}`,
    button: BUTTON[it.kind] ?? "Open",
  }));
}

/** The pull requests waiting on the signed-in person; empty when pull requests are off or the call fails. */
export function usePullInbox(every = 30000): InboxItem[] {
  const [items, setItems] = useState<InboxItem[]>([]);
  useEffect(() => {
    let alive = true;
    const load = () => call<{ items: InboxItem[] }>("GET", "/api/v1/pulls/inbox").then((r) => { if (alive) setItems(r.items ?? []); }).catch(() => { if (alive) setItems([]); });
    load();
    const t = window.setInterval(load, every);
    return () => { alive = false; window.clearInterval(t); };
  }, [every]);
  return items;
}
