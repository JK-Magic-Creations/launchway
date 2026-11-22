import { organizeApi } from "./organizeApi";

// Notes in the person's trail (kept on the server, theirs alone) what they have been looking at, so Home can offer to pick up
// where they left off. It waits a moment on a page before noting it, writes in the background and ignores any failure:
// navigation never waits for it. See docs/guide/home-page.md.

let timer = 0;

/** What a path is, as a trail entry; null for the pages that are not worth remembering (Home, the lists). */
export function entryFor(path: string, title: string): { kind: string; obj: string; project: string; title: string; href: string } | null {
  const href = path + location.search;
  const m = (re: RegExp) => re.exec(path);
  let x: RegExpExecArray | null;
  if ((x = m(/^\/runs\/([^/]+)$/))) return { kind: "run", obj: decodeURIComponent(x[1]), project: "", title, href };
  if ((x = m(/^\/projects\/([^/]+)\/pipeline(?:\/edit)?$/))) return { kind: "pipeline", obj: decodeURIComponent(x[1]), project: decodeURIComponent(x[1]), title, href };
  if ((x = m(/^\/projects\/([^/]+)(?:\/.*)?$/))) return { kind: "project", obj: decodeURIComponent(x[1]), project: decodeURIComponent(x[1]), title, href };
  if ((x = m(/^\/repositories\/([^/]+)\/([^/]+)\/pulls\/(\d+)$/))) return { kind: "pull", obj: `${decodeURIComponent(x[1])}/${decodeURIComponent(x[2])}#${x[3]}`, project: `${decodeURIComponent(x[1])}/${decodeURIComponent(x[2])}`, title, href };
  if ((x = m(/^\/repositories\/([^/]+)\/([^/]+)(?:\/.*)?$/))) return { kind: "repository", obj: `${decodeURIComponent(x[1])}/${decodeURIComponent(x[2])}`, project: `${decodeURIComponent(x[1])}/${decodeURIComponent(x[2])}`, title, href };
  if (/^\/deployments\//.test(path)) return { kind: "deployment", obj: path, project: "", title, href };
  if (/^\/settings\//.test(path)) return { kind: "settings", obj: path, project: "", title, href };
  return null;
}

/** Called when the page changes. */
export function noteView(path: string): void {
  window.clearTimeout(timer);
  timer = window.setTimeout(() => {
    const title = document.title.replace(/ · Launchway$/, "").slice(0, 190);
    const e = entryFor(path, title);
    if (e && title) void organizeApi.note({ ...e, action: "viewed" }).catch(() => {});
  }, 1500);
}
