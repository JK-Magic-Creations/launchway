import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { announce } from "../kit/announce";
import { Dialog } from "../kit/Dialog";
import { Icon } from "../kit/Icon";
import { Status, type State } from "../kit/Status";
import { onLink } from "../router";
import { terms } from "../terms";
import { bool, oneOf, setUrl, useUrlState } from "../urlState";
import { ago } from "../format";
import { pinToHome } from "./pin";
import { buildTree, highlight, isFlightSeg, itemScore, nodeKey, ROOT, visibleNodes, worst, type Seg, type Tone, type XItem, type XNode } from "./tree";
import { FlightCheck, MoveFlightsBar, useFlightSelection, type FlightSelection } from "../components/MoveFlights";
import "./explorer.css";

// The Explorer: one component for every long list (pipelines, repositories, projects, Sorties). A tree of
// places on the left (levels, then folders), the selected place's items on the right as cards or a table, a
// search box that filters both as you type, filters, sort and grouping, all kept in the address so a link opens
// the same view. See docs/guide/organizing.md and docs/contributing/explorer.md.

export interface Collection { id: string; name: string; items: string[] }
const MINE: Seg = { key: "mine", label: "My collections" };

export interface ExplorerProps {
  /** What is listed, for the heading and the empty states: "pipelines", "repositories". */
  noun: { singular: string; plural: string };
  items: XItem[];
  /** Folders with nothing in them yet (so they show in the tree and can be filed into). */
  emptyFolders?: { path: Seg[]; id: string }[];
  /** The person's own collections: groups that reference items without moving them for anyone else. */
  collections?: Collection[];
  onCollections?: (next: Collection[]) => Promise<void> | void;
  /** The text of the last result's status for the filter. */
  kinds?: string[];
  /** Filing an item in a folder; absent when the person may not arrange anything. */
  onMove?: (item: XItem, folderId: string) => Promise<void> | void;
  onFavorite?: (item: XItem, on: boolean) => void;
  /** Folders the person may file into, with their path in words. */
  folders?: { id: string; path: string }[];
  /** Makes a folder at the current place. */
  onCreateFolder?: (parent: Seg[] | null, name: string) => Promise<void> | void;
  /** Where the preference "cards or table" is saved; null keeps it in the address only. */
  savePreference?: (key: string, value: string) => void;
  initialView?: "cards" | "table";
  now: number;
  /** Whether items have run results (pipelines do, repositories do not): without them the status filter, the result columns and the status icon are left out. */
  results?: boolean;
  /** Whether each item shows a strip of recent results (a pipeline does; a single Sortie has none). */
  strip?: boolean;
  /** Extra controls in the toolbar. */
  toolbar?: ReactNode;
  /** When the items are Flights (the id is the Flight's name): each gets a checkbox and a toolbar offers "Move to Squadron…". onMoved reloads the list. */
  flights?: { onMoved?: () => void };
  /** Shown above the tree: an explanation of what the folders are. */
  emptyHint?: ReactNode;
  /** Shown when there are no items at all. */
  empty: ReactNode;
}

const TONE_WORD: Record<Tone, string> = { bad: "failed", wait: "waiting for a person", run: "running", ok: "passed", none: "no result yet" };
const TONE_STATE: Record<Tone, State> = { bad: "bad", wait: "wait", run: "run", ok: "ok", none: "idle" };
const SORTS = ["attention", "name", "recent"] as const;
const GROUPS = ["none", "folder", "label", "status"] as const;
const STATUSES = ["all", "bad", "run", "wait", "ok", "none"] as const;

export function Explorer(p: ExplorerProps) {
  const [q, setQ] = useUrlState("q", "");
  const [node, setNode] = useUrlState("node", ROOT);
  const [view, setView] = useUrlState("view", p.initialView ?? "cards", oneOf<"cards" | "table">("cards", "table"));
  const [status, setStatus] = useUrlState("result", "all", oneOf(...STATUSES));
  const [label, setLabel] = useUrlState("label", "");
  const [kind, setKind] = useUrlState("kind", "");
  const [sort, setSort] = useUrlState("sort", "attention", oneOf(...SORTS));
  const [group, setGroup] = useUrlState("group", "none", oneOf(...GROUPS));
  const [favOnly, setFavOnly] = useUrlState("fav", false, bool);
  const [mineOnly, setMineOnly] = useUrlState("mine", false, bool);
  // The first level of places (the Wing, or the Squadrons) starts open, so the tree shows something to choose.
  const [open, setOpen] = useState<Set<string>>(() => new Set([...buildTree(p.items).values()].filter((n) => n.depth === 1 && n.children.length > 0).map((n) => n.key)));
  // "everyone" files items in shared folders (needs the right); "me" groups them in the person's own collections, which move nothing for anyone else.
  const [arrangeMode, setArrangeMode] = useState<"off" | "everyone" | "me">("off");
  const arranging = arrangeMode !== "off";
  const meMode = arrangeMode === "me";
  const collections = useMemo(() => p.collections ?? [], [p.collections]);
  const itemsAll = useMemo(() => (collections.length === 0 ? p.items : p.items.map((it) => {
    const cs = collections.filter((c) => c.items.includes(it.id));
    return cs.length === 0 ? it : { ...it, also: cs.map((c) => [MINE, { key: "c:" + c.id, label: c.name }]) };
  })), [p.items, collections]);
  const [moving, setMoving] = useState<XItem | null>(null);
  const [last, setLast] = useState<{ item: XItem; from: string } | null>(null);
  const [creating, setCreating] = useState<false | "folder" | "collection">(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const hasResults = p.results !== false;
  const [filtersOpen, setFiltersOpen] = useState(false);

  useEffect(() => {
    const on = (e: globalThis.KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (e.key === "f" && !e.ctrlKey && !e.metaKey && !/^(input|textarea|select)$/i.test(t.tagName) && !t.isContentEditable) { e.preventDefault(); searchRef.current?.focus(); }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, []);

  const tree = useMemo(() => buildTree(itemsAll, [...(p.emptyFolders ?? []).map((f) => ({ path: f.path })), ...collections.map((c) => ({ path: [MINE, { key: "c:" + c.id, label: c.name }] }))]), [itemsAll, p.emptyFolders, collections]);
  const labels = useMemo(() => [...new Set(p.items.flatMap((i) => i.labels))].sort(), [p.items]);

  // Items that pass the filters (not yet narrowed to the selected place, so the tree can show matching counts).
  const scored = useMemo(() => {
    const out: { it: XItem; s: number }[] = [];
    for (const it of itemsAll) {
      if (status !== "all" && it.tone !== status) continue;
      if (label && !it.labels.includes(label)) continue;
      if (kind && it.kind !== kind) continue;
      if (favOnly && !it.favorite) continue;
      if (mineOnly && !it.mine) continue;
      const s = itemScore(q, it);
      if (s > 0) out.push({ it, s });
    }
    return out;
  }, [itemsAll, q, status, label, kind, favOnly, mineOnly]);
  const filtering = q.trim() !== "" || status !== "all" || label !== "" || kind !== "" || favOnly || mineOnly;

  // The tree counts what matches while filtering, and hides branches with nothing in them.
  const shownTree = useMemo(() => (filtering ? buildTree(scored.map((x) => x.it)) : tree), [filtering, scored, tree]);
  const nodeKnown = shownTree.has(node);
  const selected = nodeKnown ? node : ROOT;

  const inNode = useMemo(() => scored.filter((x) => [x.it.path, ...(x.it.also ?? [])].some((path) => { const k = nodeKey(path); return selected === ROOT || k === selected || k.startsWith(selected + "/"); })), [scored, selected]);
  const list = useMemo(() => {
    const r = [...inNode];
    const rank: Record<Tone, number> = { bad: 0, wait: 1, run: 2, none: 3, ok: 4 };
    r.sort((a, b) => {
      if (q.trim() && a.s !== b.s) return b.s - a.s;
      if (sort === "attention" && rank[a.it.tone] !== rank[b.it.tone]) return rank[a.it.tone] - rank[b.it.tone];
      if (sort === "recent" && (b.it.last?.at ?? 0) !== (a.it.last?.at ?? 0)) return (b.it.last?.at ?? 0) - (a.it.last?.at ?? 0);
      return a.it.name.localeCompare(b.it.name, undefined, { numeric: true });
    });
    return r.map((x) => x.it);
  }, [inNode, sort, q]);

  const selection = useFlightSelection(list.map((i) => i.id));
  const toggle = useCallback((k: string, on?: boolean) => setOpen((o) => { const n = new Set(o); if (on === undefined ? !n.has(k) : on) n.add(k); else n.delete(k); return n; }), []);
  const openAll = (on: boolean) => setOpen(on ? new Set([...shownTree.keys()].filter((k) => k !== ROOT && shownTree.get(k)!.children.length > 0)) : new Set());
  // While filtering every branch with a match is open, so a match is never hidden.
  const effectiveOpen = useMemo(() => {
    if (filtering) return new Set([...shownTree.keys()]);
    const o = new Set(open);
    for (let k = shownTree.get(selected)?.parent; k; k = shownTree.get(k)?.parent) o.add(k); // the way to the chosen place stays open
    return o;
  }, [filtering, shownTree, open, selected]);

  const choose = (k: string) => { setNode(k, "push"); };
  const rootLabel = terms().label("installation");
  const crumbs = selected === ROOT ? [] : selected.split("/").map((_, i, a) => { const k = a.slice(0, i + 1).join("/"); return { key: k, label: shownTree.get(k)?.label ?? a[i] }; });

  const move = async (it: XItem, folderId: string) => {
    if (!p.onMove) return;
    const from = it.path.filter((x) => !isFlightSeg(x)).at(-1)?.folderId ?? "";
    try {
      await p.onMove(it, folderId);
      setLast({ item: it, from });
      announce(`${it.name} moved. Undo is available.`);
    } catch (e) { announce(`Could not move ${it.name}: ${e instanceof Error ? e.message : "refused"}`, "assertive"); }
    setMoving(null);
  };

  // Adding an item to a collection, or taking it out: nothing moves for anyone else.
  const collect = async (it: XItem, id: string, name?: string) => {
    if (!p.onCollections) return;
    let next = collections;
    let to = id;
    if (!id) { to = "c" + Math.random().toString(36).slice(2, 8); next = [...next, { id: to, name: (name ?? "").trim() || "Collection", items: [] }]; }
    next = next.map((c) => (c.id === to && !c.items.includes(it.id) ? { ...c, items: [...c.items, it.id] } : c));
    await p.onCollections(next);
    announce(`${it.name} added to ${next.find((c) => c.id === to)?.name}. Nothing changed for anyone else.`);
    setMoving(null);
  };
  const uncollect = async (it: XItem) => {
    const cid = selected.startsWith("mine/c:") ? selected.slice("mine/c:".length) : "";
    if (!cid || !p.onCollections) return;
    await p.onCollections(collections.map((c) => (c.id === cid ? { ...c, items: c.items.filter((x) => x !== it.id) } : c)));
    announce(`${it.name} removed from the collection.`);
  };
  const newCollection = async (name: string) => { await p.onCollections?.([...collections, { id: "c" + Math.random().toString(36).slice(2, 8), name: name.trim() || "Collection", items: [] }]); };
  if (p.items.length === 0) return <div className="xp-empty">{p.empty}</div>;

  const dropFolder = (n: XNode) => (e: React.DragEvent) => {
    e.preventDefault();
    const id = e.dataTransfer.getData("text/plain");
    const it = p.items.find((i) => i.id === id);
    if (!it) return;
    if (meMode && n.key.startsWith("mine/c:")) void collect(it, n.key.slice("mine/c:".length));
    else if (!meMode && n.folderId) void move(it, n.folderId);
  };

  return (
    <section className="xp" aria-label={`${p.noun.plural} explorer`}>
      <div className="xp-bar">
        <div className="xp-search">
          <Icon name="search" />
          <input ref={searchRef} type="search" role="searchbox" aria-label={`Filter ${p.noun.plural}`} placeholder={`Filter ${p.noun.plural}, press f to jump here`} value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <button type="button" className="btn btn-sm xp-ftoggle" aria-expanded={filtersOpen} onClick={() => setFiltersOpen(!filtersOpen)}>{filtersOpen ? "Hide filters" : "Filters and sorting"}</button>
        <div className={"xp-filters" + (filtersOpen ? " show" : "")}>
        {hasResults && <label className="xp-sel">Status
          <select value={status} onChange={(e) => setStatus(e.target.value as typeof status)}>
            <option value="all">Any</option><option value="bad">Failed</option><option value="run">Running</option><option value="wait">Waiting for a person</option><option value="ok">Passed</option><option value="none">No result yet</option>
          </select>
        </label>}
        {labels.length > 0 && <label className="xp-sel">Label
          <select value={label} onChange={(e) => setLabel(e.target.value)}><option value="">Any</option>{labels.map((l) => <option key={l}>{l}</option>)}</select>
        </label>}
        {(p.kinds?.length ?? 0) > 0 && <label className="xp-sel">Kind
          <select value={kind} onChange={(e) => setKind(e.target.value)}><option value="">Any</option>{p.kinds!.map((l) => <option key={l}>{l}</option>)}</select>
        </label>}
        <label className="xp-sel">Sort
          <select value={sort} onChange={(e) => setSort(e.target.value as typeof sort)}>{hasResults && <option value="attention">Needs a look first</option>}<option value="name">Name</option>{hasResults && <option value="recent">Latest result</option>}</select>
        </label>
        <label className="xp-sel">Group by
          <select value={group} onChange={(e) => setGroup(e.target.value as typeof group)}><option value="none">Nothing</option><option value="folder">Folder</option><option value="label">Label</option><option value="status">Status</option></select>
        </label>
        <label className="xp-chk"><input type="checkbox" checked={favOnly} onChange={(e) => setFavOnly(e.target.checked)} /> Favorites</label>
        <label className="xp-chk"><input type="checkbox" checked={mineOnly} onChange={(e) => setMineOnly(e.target.checked)} /> Mine</label>
        <div className="xp-seg" role="group" aria-label="Show as">
          {(["cards", "table"] as const).map((v) => (
            <button key={v} type="button" className="btn btn-sm" aria-pressed={view === v} onClick={() => { setView(v); p.savePreference?.("view", v); }}>{v === "cards" ? "Cards" : "Table"}</button>
          ))}
        </div>
        {p.toolbar}
        </div>
      </div>

      <div className="xp-body">
        <nav className="xp-side" aria-label={`${p.noun.plural} by place`}>
          <div className="xp-side-h">
            <button type="button" className="btn btn-sm" onClick={() => openAll(true)}>Expand all</button>
            <button type="button" className="btn btn-sm" onClick={() => openAll(false)}>Collapse all</button>
          </div>
          <Tree nodes={shownTree} open={effectiveOpen} selected={selected} rootLabel={rootLabel} onSelect={choose} onToggle={toggle} q={q} onDrop={arranging ? dropFolder : undefined} />
          {p.onCollections && <div className="xp-side-f"><button type="button" className="btn btn-sm" onClick={() => setCreating("collection")}>New collection</button></div>}
          {p.onCreateFolder && <div className="xp-side-f"><button type="button" className="btn btn-sm" onClick={() => setCreating("folder")}>New folder</button></div>}
          {p.emptyHint && <p className="faint small xp-hint">{p.emptyHint}</p>}
        </nav>

        <div className="xp-main">
          <div className="xp-crumbs-row">
            <nav aria-label="Where you are" className="xp-crumbs">
              <ol>
                <li>{selected === ROOT ? <span aria-current="page">{rootLabel}</span> : <button type="button" className="xp-crumb" onClick={() => choose(ROOT)}>{rootLabel}</button>}</li>
                {crumbs.map((c, i) => <li key={c.key}>{i === crumbs.length - 1 ? <span aria-current="page">{c.label}</span> : <button type="button" className="xp-crumb" onClick={() => choose(c.key)}>{c.label}</button>}</li>)}
              </ol>
            </nav>
            <span className="faint small" role="status">{list.length} of {p.items.length} {p.noun.plural}{filtering ? " match" : ""}</span>
            <span className="xp-grow" />
            <button type="button" className="btn btn-sm" onClick={() => { void navigator.clipboard?.writeText(location.href); announce("Link to this view copied."); }}>Copy link to this view</button>
            <button type="button" className="btn btn-sm" onClick={() => { const t = `${p.noun.plural[0].toUpperCase()}${p.noun.plural.slice(1)}${filtering ? ": " + (q.trim() || [status !== "all" && status, label, kind, favOnly && "favorites", mineOnly && "mine"].filter(Boolean).join(", ")) : ""}`; pinToHome(t, location.pathname + location.search).then(() => announce("Pinned to your Home."), () => announce("Could not pin this view.", "assertive")); }}>Pin to Home</button>
            {p.onMove && <button type="button" className="btn btn-sm" aria-pressed={arrangeMode === "everyone"} onClick={() => { const on = arrangeMode !== "everyone"; setArrangeMode(on ? "everyone" : "off"); announce(on ? "Arranging for everyone is on: drag an item onto a folder, or use Move to." : "Arranging is off."); }}>{arrangeMode === "everyone" ? "Done arranging" : "Arrange for everyone"}</button>}
            {p.onCollections && <button type="button" className="btn btn-sm" aria-pressed={meMode} onClick={() => { const on = !meMode; setArrangeMode(on ? "me" : "off"); announce(on ? "Arranging for you is on: drag an item onto a collection, or use Add to collection. Nothing changes for anyone else." : "Arranging is off."); }}>{meMode ? "Done arranging" : "Arrange for me"}</button>}
          </div>
          {meMode && <p className="callout" role="note">Arranging for you: collections are yours alone. An item shows in a collection without moving for anyone else; drag it onto a collection, or use <b>Add to collection…</b> on it.</p>}
          {arrangeMode === "everyone" && <p className="callout" role="note">Arranging for everyone: what you move here changes the folders for everyone who can see these {p.noun.plural}. Drag an item onto a folder, or use <b>Move to…</b> on it.</p>}
          {p.flights && <MoveFlightsBar selection={selection} onMoved={p.flights.onMoved} />}
          {last && <p role="status" className="xp-undo"><b>{last.item.name}</b> was moved. <button type="button" className="btn btn-sm" onClick={() => { void p.onMove?.(last.item, last.from); announce("Move undone."); setLast(null); }}>Undo</button></p>}

          {list.length === 0 ? (
            <div className="xp-empty" role="status">
              <b>No {p.noun.singular} matches.</b>
              <p className="faint">{filtering ? "Nothing fits the filters. " : "Nothing is in this place yet. "}Try fewer words, or clear the filters.</p>
              {filtering && <button type="button" className="btn btn-sm" onClick={() => setUrl({ q: null, result: null, label: null, kind: null, fav: null, mine: null }, "push")}>Clear filters</button>}
            </div>
          ) : (
            <Results sel={p.flights ? selection : undefined} strip={p.strip !== false} results={hasResults} list={list} view={view} group={group} q={q} now={p.now} arranging={arranging} meMode={meMode} inCollection={selected.startsWith("mine/c:")} onRemove={uncollect} onMove={(it) => setMoving(it)} onFavorite={p.onFavorite} />
          )}
        </div>
      </div>

      {moving && meMode && (
        <Dialog label={`Add ${moving.name} to a collection`} onClose={() => setMoving(null)}>
          <CollectForm item={moving} collections={collections} onAdd={collect} onClose={() => setMoving(null)} />
        </Dialog>
      )}
      {moving && !meMode && (
        <Dialog label={`Move ${moving.name}`} onClose={() => setMoving(null)}>
          <MoveForm item={moving} folders={p.folders ?? []} onMove={move} onClose={() => setMoving(null)} />
        </Dialog>
      )}
      {creating === "collection" && (
        <Dialog label="New collection" onClose={() => setCreating(false)}>
          <NewFolder what="collection" where="your collections" onCreate={async (name) => { await newCollection(name); setCreating(false); }} onClose={() => setCreating(false)} />
        </Dialog>
      )}
      {creating === "folder" && (
        <Dialog label="New folder" onClose={() => setCreating(false)}>
          <NewFolder where={crumbs.map((c) => c.label).join(" / ") || rootLabel} onCreate={async (name) => { await p.onCreateFolder?.(selected === ROOT ? null : selected.split("/").map((k) => ({ key: k, label: shownTree.get(nodeKeyUpTo(selected, k))?.label ?? k, folderId: shownTree.get(nodeKeyUpTo(selected, k))?.folderId })), name); setCreating(false); }} onClose={() => setCreating(false)} />
        </Dialog>
      )}
    </section>
  );
}

function nodeKeyUpTo(selected: string, k: string): string { const parts = selected.split("/"); return parts.slice(0, parts.indexOf(k) + 1).join("/"); }

// ---- the tree ----

function Dot({ tones }: { tones: Record<Tone, number> }) {
  const t = worst(tones);
  const n = tones[t];
  if (t === "none" || (t === "ok" && n === 0)) return null;
  return <span className={`xp-dot ${t}`} title={`${n} ${TONE_WORD[t]}`}><Status state={TONE_STATE[t]} label={`${n} ${TONE_WORD[t]}`} /></span>;
}

function Tree({ nodes, open, selected, rootLabel, onSelect, onToggle, q, onDrop }: {
  nodes: Map<string, XNode>; open: Set<string>; selected: string; rootLabel: string; onSelect: (k: string) => void; onToggle: (k: string, on?: boolean) => void; q: string;
  onDrop?: (n: XNode) => (e: React.DragEvent) => void;
}) {
  const rows = useMemo(() => [nodes.get(ROOT)!, ...visibleNodes(nodes, open)], [nodes, open]);
  const [active, setActive] = useState(selected);
  const ref = useRef<HTMLUListElement>(null);
  const buf = useRef({ text: "", at: 0 });
  const activeKey = rows.some((r) => r.key === active) ? active : selected;
  const focusKey = (k: string) => { setActive(k); const el = ref.current?.querySelector<HTMLElement>(`[data-key="${CSS.escape(k)}"]`); if (el) el.focus(); else requestAnimationFrame(() => ref.current?.querySelector<HTMLElement>(`[data-key="${CSS.escape(k)}"]`)?.focus()); };
  const label = (n: XNode) => (n.key === ROOT ? rootLabel : n.label);
  const key = (e: KeyboardEvent<HTMLUListElement>) => {
    const i = rows.findIndex((r) => r.key === activeKey);
    const n = rows[i];
    if (!n) return;
    const hasKids = n.children.length > 0;
    switch (e.key) {
      case "ArrowDown": e.preventDefault(); if (rows[i + 1]) focusKey(rows[i + 1].key); break;
      case "ArrowUp": e.preventDefault(); if (rows[i - 1]) focusKey(rows[i - 1].key); break;
      case "Home": e.preventDefault(); focusKey(rows[0].key); break;
      case "End": e.preventDefault(); focusKey(rows[rows.length - 1].key); break;
      case "ArrowRight": e.preventDefault(); if (hasKids && !open.has(n.key) && n.key !== ROOT) onToggle(n.key, true); else if (hasKids && rows[i + 1]) focusKey(rows[i + 1].key); break;
      case "ArrowLeft": e.preventDefault(); if (hasKids && open.has(n.key) && n.key !== ROOT) onToggle(n.key, false); else if (n.parent !== null) focusKey(n.parent); break;
      case "Enter": case " ": e.preventDefault(); onSelect(n.key); break;
      case "*": e.preventDefault(); for (const s of nodes.get(n.parent ?? ROOT)!.children) onToggle(s, true); break;
      default:
        if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
          const now = Date.now();
          buf.current = { text: (now - buf.current.at < 700 ? buf.current.text : "") + e.key.toLowerCase(), at: now };
          const from = buf.current.text.length === 1 ? i + 1 : i;
          const order = [...rows.slice(from), ...rows.slice(0, from)];
          const hit = order.find((r) => label(r).toLowerCase().startsWith(buf.current.text));
          if (hit) { e.preventDefault(); focusKey(hit.key); }
        }
    }
  };
  const renderNode = (n: XNode): ReactNode => {
    const isOpen = n.key === ROOT || open.has(n.key);
    const kids = n.children.map((c) => nodes.get(c)!);
    return (
      <li key={n.key || "root"} role="none">
        {/* The keys are handled by the tree that holds it (arrows, Enter, type-ahead). */}
        {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events */}
        <div role="treeitem" aria-level={n.depth + 1} aria-selected={selected === n.key} aria-expanded={kids.length > 0 ? isOpen : undefined}
          aria-label={`${label(n)} ${n.count} ${n.count === 1 ? "item" : "items"}${worst(n.tones) !== "none" && worst(n.tones) !== "ok" ? `, ${n.tones[worst(n.tones)]} ${TONE_WORD[worst(n.tones)]}` : ""}`}
          data-key={n.key} tabIndex={activeKey === n.key ? 0 : -1} className={"xp-node" + (selected === n.key ? " sel" : "")} style={{ paddingLeft: 8 + n.depth * 14 }}
          onClick={() => { setActive(n.key); onSelect(n.key); }} onFocus={() => setActive(n.key)}
          onDragOver={onDrop && (n.folderId || n.key.startsWith("mine/c:")) ? (e) => e.preventDefault() : undefined} onDrop={onDrop && (n.folderId || n.key.startsWith("mine/c:")) ? onDrop(n) : undefined}>
          {kids.length > 0 && n.key !== ROOT ? (
            <button type="button" tabIndex={-1} className="xp-tw" aria-hidden="true" onClick={(e) => { e.stopPropagation(); onToggle(n.key); }}>{isOpen ? "▾" : "▸"}</button>
          ) : <span className="xp-tw" aria-hidden="true" />}
          <span className="xp-nl"><Hl text={label(n)} q={q} /></span>
          <span aria-hidden="true"><Dot tones={n.tones} /></span>
          <span className="xp-cnt" aria-hidden="true" data-count={n.count} />
        </div>
        {kids.length > 0 && isOpen && <ul role="group">{kids.map(renderNode)}</ul>}
      </li>
    );
  };
  return <ul role="tree" aria-label="Places" className="xp-tree" ref={ref} onKeyDown={key}>{renderNode(nodes.get(ROOT)!)}</ul>;
}

export function Hl({ text, q }: { text: string; q: string }) {
  return <>{highlight(text, q).map(([t, m], i) => (m ? <mark key={i}>{t}</mark> : <span key={i}>{t}</span>))}</>;
}

// ---- results, virtualized ----

interface Row { kind: "head" | "items"; title?: string; count?: number; items?: XItem[] }

function Results({ sel, strip, results, list, view, group, q, now, arranging, meMode, inCollection, onRemove, onMove, onFavorite }: {
  sel?: FlightSelection; strip: boolean; meMode: boolean; inCollection: boolean; onRemove: (it: XItem) => void; results: boolean; list: XItem[]; view: "cards" | "table"; group: (typeof GROUPS)[number]; q: string; now: number; arranging: boolean; onMove: (it: XItem) => void; onFavorite?: (it: XItem, on: boolean) => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [geom, setGeom] = useState({ w: 0, h: 0, top: 0 });
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const measure = () => setGeom((g) => ({ ...g, w: el.clientWidth, h: el.clientHeight }));
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const cols = view === "table" ? 1 : Math.max(1, Math.floor((geom.w || 1) / 300));
  const rowH = view === "table" ? 44 : 176;
  const groups = useMemo(() => {
    if (group === "none") return [{ title: "", items: list }];
    const m = new Map<string, XItem[]>();
    for (const it of list) {
      const keys = group === "folder" ? [it.path.filter((x) => !isFlightSeg(x)).at(-1)?.label ?? "Not in a folder"] : group === "label" ? (it.labels.length ? it.labels : ["No label"]) : [TONE_WORD[it.tone]];
      for (const k of keys) m.set(k, [...(m.get(k) ?? []), it]);
    }
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([title, items]) => ({ title, items }));
  }, [list, group]);
  const rows = useMemo(() => {
    const out: Row[] = [];
    for (const g of groups) {
      if (g.title) out.push({ kind: "head", title: g.title, count: g.items.length });
      for (let i = 0; i < g.items.length; i += cols) out.push({ kind: "items", items: g.items.slice(i, i + cols) });
    }
    return out;
  }, [groups, cols]);
  const total = rows.length;
  const virtual = geom.h > 0 && total * rowH > 1200;
  const first = virtual ? Math.max(0, Math.floor(geom.top / rowH) - 3) : 0;
  const last = virtual ? Math.min(total, Math.ceil((geom.top + geom.h) / rowH) + 3) : Math.min(total, 400);
  const head = first * rowH;
  return (
    <div className={"xp-results" + (virtual ? " virtual" : "")} ref={box} onScroll={(e) => virtual && setGeom((g) => ({ ...g, top: (e.target as HTMLElement).scrollTop }))} tabIndex={virtual ? 0 : undefined} role={virtual ? "region" : undefined} aria-label={virtual ? "Results, scrollable" : undefined}>
      {view === "table" && <div className="xp-th" aria-hidden="true"><span>Name</span><span>Where</span><span>{results ? "Last result" : "About"}</span><span>{results ? "Recent" : ""}</span><span /></div>}
      <div style={virtual ? { height: total * rowH, position: "relative" } : undefined}>
        <ul className={"xp-list " + view} style={virtual ? { position: "absolute", top: head, left: 0, right: 0 } : undefined}>
          {rows.slice(first, last).map((r, i) => r.kind === "head" ? (
            <li key={"h" + first + i} className="xp-group" style={{ height: virtual ? rowH : undefined }}><h2>{r.title} <span className="faint">({r.count})</span></h2></li>
          ) : (
            <li key={"r" + first + i} className="xp-row" style={{ height: virtual ? rowH : undefined, gridTemplateColumns: view === "cards" ? `repeat(${cols}, 1fr)` : undefined }}>
              {r.items!.map((it) => view === "cards" ? <Card key={it.id} sel={sel} strip={strip} results={results} it={it} q={q} now={now} arranging={arranging} meMode={meMode} inCollection={inCollection} onRemove={onRemove} onMove={onMove} onFavorite={onFavorite} /> : <TableRow key={it.id} sel={sel} strip={strip} results={results} it={it} q={q} now={now} arranging={arranging} meMode={meMode} inCollection={inCollection} onRemove={onRemove} onMove={onMove} onFavorite={onFavorite} />)}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

interface ItemProps { sel?: FlightSelection; strip: boolean; meMode: boolean; inCollection: boolean; onRemove: (it: XItem) => void; results: boolean; it: XItem; q: string; now: number; arranging: boolean; onMove: (it: XItem) => void; onFavorite?: (it: XItem, on: boolean) => void }

function Strip({ history, name }: { history: Tone[]; name: string }) {
  if (history.length === 0) return <span className="faint small">no results yet</span>;
  const c = history.reduce<Record<string, number>>((m, t) => ({ ...m, [t]: (m[t] ?? 0) + 1 }), {});
  const text = `Recent results of ${name}: ` + Object.entries(c).map(([t, n]) => `${n} ${TONE_WORD[t as Tone]}`).join(", ");
  return <span className="trend" role="img" aria-label={text} title={text}>{history.map((t, i) => <i key={i} className={t === "none" ? "skip" : t} />)}</span>;
}

/** Where an item lives in words. The Flight a pipeline belongs to is left out: the card's own name says it. */
function Place({ it }: { it: XItem }) { const segs = it.path.filter((x) => !isFlightSeg(x)); return <span className="faint small xp-place">{segs.length ? segs.map((p) => p.label).join(" / ") : "Top level"}</span>; }

function Card({ sel, strip, results, it, q, now, arranging, meMode, inCollection, onRemove, onMove, onFavorite }: ItemProps) {
  return (
    <article className={"xp-card " + it.tone} draggable={arranging} onDragStart={(e) => e.dataTransfer.setData("text/plain", it.id)} aria-label={it.name}>
      <header>
        {sel && <FlightCheck selection={sel} name={it.id} />}
        {results && <Status state={TONE_STATE[it.tone]} label={TONE_WORD[it.tone][0].toUpperCase() + TONE_WORD[it.tone].slice(1)} />}
        <a href={it.href} onClick={onLink} className="xp-name"><Hl text={it.name} q={q} /></a>
        {onFavorite && <button type="button" className="xp-star" aria-pressed={it.favorite} aria-label={`${it.favorite ? "Remove" : "Add"} ${it.name} ${it.favorite ? "from" : "to"} favorites`} onClick={() => onFavorite(it, !it.favorite)}>{it.favorite ? "★" : "☆"}</button>}
      </header>
      <Place it={it} />
      {it.description && <p className="small xp-desc"><Hl text={it.description} q={q} /></p>}
      {results && <p className="small">{it.last ? <>{it.last.href ? <a href={it.last.href} onClick={onLink}>{it.last.text}</a> : it.last.text} <span className="faint">{ago(it.last.at, now)}</span></> : <span className="faint">Not run yet</span>}{it.running > 0 && <> · <b>{it.running} running</b></>}</p>}
      <div className="xp-foot">{results && strip && <Strip history={it.history} name={it.name} />}
        {it.labels.length > 0 && <span className="xp-labels">{it.labels.slice(0, 3).map((l) => <span key={l} className="vchip"><Hl text={l} q={q} /></span>)}</span>}
        <span className="xp-grow" />
        {(it.actions ?? []).slice(0, 2).map((a) => <a key={a.label} className="btn btn-sm" href={a.href} onClick={onLink}>{a.label}</a>)}
        {arranging && <button type="button" className="btn btn-sm" onClick={() => onMove(it)} aria-label={meMode ? `Add ${it.name} to a collection` : `Move ${it.name} to…`}>{meMode ? "Add to collection…" : "Move to…"}</button>}
        {meMode && inCollection && <button type="button" className="btn btn-sm" onClick={() => onRemove(it)} aria-label={`Remove ${it.name} from this collection`}>Remove</button>}
      </div>
    </article>
  );
}

function TableRow({ sel, strip, results, it, q, now, arranging, meMode, inCollection, onRemove, onMove, onFavorite }: ItemProps) {
  return (
    <div className={"xp-trow " + it.tone} role="group" aria-label={it.name} draggable={arranging} onDragStart={(e) => e.dataTransfer.setData("text/plain", it.id)}>
      <span className="xp-tname">{sel && <FlightCheck selection={sel} name={it.id} />}{results && <Status state={TONE_STATE[it.tone]} label={TONE_WORD[it.tone][0].toUpperCase() + TONE_WORD[it.tone].slice(1)} />}
        <a href={it.href} onClick={onLink}><Hl text={it.name} q={q} /></a>
        {onFavorite && <button type="button" className="xp-star" aria-pressed={it.favorite} aria-label={`${it.favorite ? "Remove" : "Add"} ${it.name} ${it.favorite ? "from" : "to"} favorites`} onClick={() => onFavorite(it, !it.favorite)}>{it.favorite ? "★" : "☆"}</button>}</span>
      <Place it={it} />
      {results ? <span className="small">{it.last ? <>{it.last.text} <span className="faint">{ago(it.last.at, now)}</span></> : <span className="faint">Not run yet</span>}{it.running > 0 && <b> · {it.running} running</b>}</span> : <span className="small faint">{it.description}</span>}
      {results && strip ? <Strip history={it.history} name={it.name} /> : <span />}
      <span className="xp-acts">{(it.actions ?? []).slice(0, 1).map((a) => <a key={a.label} className="btn btn-sm" href={a.href} onClick={onLink}>{a.label}</a>)}
        {arranging && <button type="button" className="btn btn-sm" onClick={() => onMove(it)} aria-label={meMode ? `Add ${it.name} to a collection` : `Move ${it.name} to…`}>{meMode ? "Add to collection…" : "Move to…"}</button>}
        {meMode && inCollection && <button type="button" className="btn btn-sm" onClick={() => onRemove(it)} aria-label={`Remove ${it.name} from this collection`}>Remove</button>}</span>
    </div>
  );
}

function MoveForm({ item, folders, onMove, onClose }: { item: XItem; folders: { id: string; path: string }[]; onMove: (it: XItem, folder: string) => void; onClose: () => void }) {
  const [to, setTo] = useState(item.path.filter((x) => !isFlightSeg(x)).at(-1)?.folderId ?? "");
  return (
    <form onSubmit={(e) => { e.preventDefault(); onMove(item, to); }} className="xp-dialog">
      <h2 className="h-sm">Move {item.name}</h2>
      <p className="faint small">This changes the folders for everyone who can see it.</p>
      <label>Folder
        <select data-autofocus value={to} onChange={(e) => setTo(e.target.value)}>
          <option value="">Not in a folder</option>
          {folders.map((f) => <option key={f.id} value={f.id}>{f.path}</option>)}
        </select>
      </label>
      <div className="xp-dialog-f"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn-primary">Move</button></div>
    </form>
  );
}

function NewFolder({ where, onCreate, onClose, what = "folder" }: { where: string; onCreate: (name: string) => Promise<void>; onClose: () => void; what?: "folder" | "collection" }) {
  const [name, setName] = useState("");
  const [err, setErr] = useState("");
  return (
    <form onSubmit={async (e) => { e.preventDefault(); try { await onCreate(name); } catch (x) { setErr(x instanceof Error ? x.message : "Could not make the folder"); } }} className="xp-dialog">
      <h2 className="h-sm">New {what} in {where}</h2>
      <label>Name<input data-autofocus value={name} onChange={(e) => setName(e.target.value)} maxLength={80} required /></label>
      {err && <p role="alert" className="err">{err}</p>}
      <div className="xp-dialog-f"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn-primary">Make {what}</button></div>
    </form>
  );
}

function CollectForm({ item, collections, onAdd, onClose }: { item: XItem; collections: Collection[]; onAdd: (it: XItem, id: string, name?: string) => void; onClose: () => void }) {
  const [to, setTo] = useState(collections[0]?.id ?? "");
  const [name, setName] = useState("");
  return (
    <form onSubmit={(e) => { e.preventDefault(); onAdd(item, to, name); }} className="xp-dialog">
      <h2 className="h-sm">Add {item.name} to a collection</h2>
      <p className="faint small">A collection is yours alone. {item.name} stays where it is for everyone else.</p>
      <label>Collection
        <select data-autofocus value={to} onChange={(e) => setTo(e.target.value)}>
          {collections.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          <option value="">A new collection…</option>
        </select>
      </label>
      {to === "" && <label>Name<input value={name} maxLength={60} onChange={(e) => setName(e.target.value)} required /></label>}
      <div className="xp-dialog-f"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn-primary">Add</button></div>
    </form>
  );
}
