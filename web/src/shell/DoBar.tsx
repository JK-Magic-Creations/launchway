import { useEffect, useMemo, useState } from "react";
import type { Project } from "../api";
import { Dialog } from "../kit/Dialog";
import { Icon } from "../kit/Icon";
import { navigate } from "../router";
import type { Ctx } from "../nav/model";
import { doItems, searchItems, type DoItem } from "./doItems";
import { organizeApi, type SearchHit, type TrailEntry } from "../organizeApi";
import { ago } from "../format";
import { terms } from "../terms";

const KIND: Record<string, string> = { project: "Pipelines", repository: "Repositories", folder: "Folders", run: "Sorties", pull: "Pull requests", person: "People" };

type Found = Omit<DoItem, "group"> & { group: string };

/** DoBar is the overlay: type what you want, or pick from the list. Wizards, actions and every page are in it. */
export function DoBar({ ctx, projects, onClose }: { ctx: Ctx; projects: Project[]; onClose: () => void }) {
  const [q, setQ] = useState("");
  const [at, setAt] = useState(0);
  const items = useMemo(() => doItems(ctx, projects), [ctx, projects]);
  const local = useMemo(() => searchItems(items, q), [items, q]);
  // What the server finds across everything this person may see (projects, repositories, folders, Sorties by number,
  // commit or branch), and, before anything is typed, their favorites. Local pages and actions come first.
  const [remote, setRemote] = useState<SearchHit[]>([]);
  const [favs, setFavs] = useState<string[]>([]);
  const [recent, setRecent] = useState<TrailEntry[]>([]);
  const [opened] = useState(Date.now);
  const [docs, setDocs] = useState<{ title: string; detail: string; href: string }[]>([]);
  useEffect(() => {
    const text = q.trim();
    if (text.length < 2) { if (!text) organizeApi.search("").then((r) => { setFavs(r.favorites ?? []); setRecent(r.recent ?? []); }).catch(() => {}); return; }
    let live = true;
    const t = window.setTimeout(() => {
      organizeApi.search(text, 12).then((r) => { if (live) setRemote(r.hits); }).catch(() => { if (live) setRemote([]); });
      // The documentation, from the index that ships in the binary: found with no server.
      if (text.length >= 3) void import("../learn/finder").then((m) => m.learnHits(text)).then((h) => { if (live) setDocs(h); }).catch(() => { if (live) setDocs([]); });
    }, 180);
    return () => { live = false; window.clearTimeout(t); };
  }, [q]);
  const found: Found[] = useMemo(() => {
    const have = new Set(local.map((l) => l.href));
    const more = (q.trim().length >= 2 ? remote : []).filter((h) => !have.has(h.href)).map((h, i) => ({ id: `srv-${i}`, label: h.name, hint: [h.path, h.detail].filter(Boolean).join(" · ") || KIND[h.kind], href: h.href, icon: (h.kind === "run" ? "play" : h.kind === "person" ? "user" : "code") as DoItem["icon"], group: KIND[h.kind] ?? "Results" }));
    const fav = q.trim() ? [] : favs.map((f, i) => { const [kind, name] = [f.slice(0, f.indexOf(":")), f.slice(f.indexOf(":") + 1)]; return { id: `fav-${i}`, label: name, hint: "Favorite", icon: "code" as DoItem["icon"], href: kind === "repository" ? `/repositories/${name}` : `/projects/${encodeURIComponent(name)}/pipeline`, group: "Favorites" }; });
    const rec = q.trim() ? [] : recent.map((e, i) => ({ id: `rec-${i}`, label: e.title, hint: e.changed || `${e.kind} · opened ${ago(e.at, opened)}`, icon: "code" as DoItem["icon"], href: e.href, group: "Recent" }));
    const learn = (q.trim().length >= 3 ? docs : []).map((d, i) => ({ id: `doc-${i}`, label: d.title, hint: d.detail, icon: "book" as DoItem["icon"], href: d.href, group: "Documentation" }));
    return [...fav, ...rec, ...local, ...more, ...learn];
  }, [local, remote, favs, recent, docs, q, opened]);
  const go = (href: string) => { onClose(); navigate(href); };
  const key = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") { e.preventDefault(); setAt((a) => Math.min(a + 1, found.length - 1)); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setAt((a) => Math.max(a - 1, 0)); }
    else if (e.key === "Enter" && found[at]) { e.preventDefault(); go(found[at].href); }
  };
  return (
    <Dialog label="What do you want to do?" onClose={onClose}>
      <div className="mi">
        <input type="text" role="combobox" aria-expanded="true" aria-autocomplete="list" aria-controls="do-results" aria-activedescendant={found[at] ? "do-" + found[at].id : undefined}
          data-autofocus placeholder="What do you want to do?" value={q} onChange={(e) => { setQ(e.target.value); setAt(0); }} onKeyDown={key} aria-label="What do you want to do?" autoComplete="off" />
      </div>
      <div className="visually-hidden" role="status" aria-live="polite">{found.length === 0 ? "No results" : `${found.length} ${found.length === 1 ? "result" : "results"}`}</div>
      <div className="res" id="do-results" role="listbox" aria-label="Results">
        {found.length === 0 && <p className="faint nores">Nothing matches "{q}{terms().say("\". Try a {project}'s name, \"add\", \"approve\" or the name of a page.")}</p>}
        {found.map((it, i) => (
          <a key={it.id} data-group={i === 0 || found[i - 1].group !== it.group ? it.group : undefined} id={"do-" + it.id} role="option" aria-selected={i === at} href={it.href} className={i === at ? "sel" : ""}
            onClick={(e) => { e.preventDefault(); go(it.href); }} onMouseMove={() => setAt(i)}>
            <Icon name={it.icon} /><span><b>{it.label}</b><span className="faint"> {it.hint}</span></span>
          </a>
        ))}
      </div>
    </Dialog>
  );
}
