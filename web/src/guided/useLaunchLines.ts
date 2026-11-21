import { useEffect, useState } from "react";
import { launchApi, type LaunchLineView } from "../api/launch";
import { api } from "../api";
import type { Project } from "../api";

/** The launch lines of the latest runs, kept fresh. null until the first answer. */
export function useLaunchLines(limit = 12, every = 15000): LaunchLineView[] | null {
  const [lines, setLines] = useState<LaunchLineView[] | null>(null);
  useEffect(() => {
    let alive = true;
    const load = (bg: boolean) => launchApi.lines(limit, bg).then((l) => { if (alive) setLines(l); }).catch(() => { if (alive) setLines((p) => p ?? []); });
    load(false);
    const t = window.setInterval(() => load(true), every);
    return () => { alive = false; window.clearInterval(t); };
  }, [limit, every]);
  return lines;
}

/** How many different images the projects' runs built, or null while it is not known. Looks at the
 * first ten projects, newest images first; "10+" is never claimed, the count is of what was found. */
export function useImageCount(projects: Project[]): number | null {
  const [n, setN] = useState<number | null>(null);
  const key = projects.map((p) => p.name).join("\n");
  useEffect(() => {
    let alive = true;
    Promise.all(projects.slice(0, 10).map((p) => api.images({ project: p.name, limit: 100 }).then((d) => d.hits ?? []).catch(() => [])))
      .then((all) => {
        if (!alive) return;
        const digests = new Set(all.flat().filter((h) => h.kind !== "platform").map((h) => h.digest));
        setN(digests.size);
      });
    return () => { alive = false; };
  }, [key]); // eslint-disable-line react-hooks/exhaustive-deps
  return n;
}
