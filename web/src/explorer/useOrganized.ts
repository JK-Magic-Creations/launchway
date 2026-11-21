import { useCallback, useEffect, useMemo, useState } from "react";
import { organizeApi, type Area, type FolderView, type OrganizeAnswer } from "../organizeApi";
import { useOverview } from "../configApi";
import { terms } from "../terms";
import { derivePath, type Seg } from "./tree";

/** The organization of a list: the folders people made, which item is in which, labels, favorites, and the
 * levels above (Wing, Squadron). Folders that people made win; a path-like name ("payments/api/build") is
 * shown as folders when the item is in none. Everything here is read from the server. */
export function useOrganized(area: Area) {
  const [data, setData] = useState<OrganizeAnswer | null>(null);
  const [failed, setFailed] = useState(false);
  const { overview } = useOverview();
  const load = useCallback(() => { organizeApi.organize(area).then((d) => { setData(d); setFailed(false); }).catch(() => setFailed(true)); }, [area]);
  useEffect(load, [load]);

  const unitOf = useMemo(() => {
    const m = new Map<string, { key: string; label: string }>();
    for (const u of overview?.units ?? []) for (const p of u.projects) m.set(p, { key: "unit:" + u.name, label: u.display_name || u.name });
    return m;
  }, [overview]);
  const wingName = overview?.organisation?.display_name;
  const wing = useMemo<Seg | null>(() => (wingName ? { key: "wing", label: wingName } : null), [wingName]);

  const folderById = useMemo(() => new Map((data?.folders ?? []).map((f) => [f.id, f])), [data]);
  /** The segments of a folder's chain, outermost first. */
  const folderSegs = useCallback((id: string): Seg[] => {
    const out: Seg[] = [];
    for (let f: FolderView | undefined = folderById.get(id), n = 0; f && n < 32; f = folderById.get(f.parent), n++) out.unshift({ key: "f:" + f.id, label: f.name, folderId: f.id });
    return out;
  }, [folderById]);

  /** Where an item lives, and the name to show for it. */
  const place = useCallback((name: string, unitName?: string): { path: Seg[]; leaf: string; labels: string[]; folder: string } => {
    const org = data?.items[name];
    const top: Seg[] = [];
    const u = unitName ? { key: "unit:" + unitName, label: unitName } : unitOf.get(name);
    if (wing && u) top.push(wing);
    if (u) top.push(u);
    if (org?.folder && folderById.has(org.folder)) return { path: [...top, ...folderSegs(org.folder)], leaf: name, labels: org.labels ?? [], folder: org.folder };
    const d = derivePath(name);
    return { path: [...top, ...d.path], leaf: name, labels: org?.labels ?? [], folder: "" };
  }, [data, wing, unitOf, folderById, folderSegs]);

  const emptyFolders = useMemo(() => (data?.folders ?? []).filter((f) => f.count === 0).map((f) => ({ id: f.id, path: folderSegs(f.id) })), [data, folderSegs]);
  const folders = useMemo(() => (data?.folders ?? []).filter((f) => f.may_arrange).map((f) => ({ id: f.id, path: f.path })), [data]);
  const mayArrange = !!data?.may_arrange_base || (data?.folders ?? []).some((f) => f.may_arrange);
  const favorites = useMemo(() => new Set(data?.favorites ?? []), [data]);

  const move = useCallback(async (item: string, folder: string) => { await organizeApi.move(area, item, folder); load(); }, [area, load]);
  const createFolder = useCallback(async (parent: Seg[] | null, name: string) => {
    const p = parent?.at(-1)?.folderId;
    // A level above has no folder: the folder is made at the Base (shared by everyone who may arrange there).
    await organizeApi.createFolder(area, { scope: "base", name, parent: p ?? "" });
    load();
  }, [area, load]);
  const favorite = useCallback(async (kind: "project" | "repository", name: string, on: boolean) => { await organizeApi.favorite(`${kind}:${name}`, on); load(); }, [load]);
  const rootLabel = terms().label("installation");
  return { data, failed, place, emptyFolders, folders, mayArrange, favorites, move, createFolder, favorite, reload: load, rootLabel };
}
