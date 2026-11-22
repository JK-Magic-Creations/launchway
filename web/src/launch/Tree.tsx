import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { LNode } from "../launchApi";
import { buildItems, flatten, HEALTH, parents, type TreeItem } from "./model";
import { SpanHidden } from "../kit/bits";

/** Health as a glyph and a word: never color alone. */
export function HealthTag({ h, label }: { h: LNode["health"]; label?: string }) {
  const m = HEALTH[h] ?? HEALTH.Unknown;
  return <span className={`lw-health lw-${m.tone}`}><span aria-hidden="true">{m.glyph}</span> {label ?? h}</span>;
}

export function ChangeTag({ c }: { c?: LNode["change"] }) {
  if (!c || c === "unchanged") return null;
  const words = { new: "new in this Launch", changed: "changed in this Launch", leftover: "left over" } as const;
  return <span className={`lw-change lw-change-${c}`}>{words[c]}</span>;
}

/** The objects as a tree: arrow keys move and open, Home and End jump, Enter or Space selects. */
export function ObjectTree({ nodes, selected, onSelect, showLeftovers }: { nodes: LNode[]; selected: string; onSelect: (id: string) => void; showLeftovers: boolean }) {
  const items = useMemo(() => buildItems(nodes, showLeftovers), [nodes, showLeftovers]);
  const [open, setOpen] = useState<Set<string>>(() => parents(items));
  const seen = useRef(new Set<string>());
  // New parents open as they appear; what a person closed stays closed.
  useEffect(() => {
    const all = parents(items);
    setOpen((o) => {
      const n = new Set(o);
      all.forEach((id) => { if (!seen.current.has(id)) { seen.current.add(id); n.add(id); } });
      return n;
    });
  }, [items]);
  const rows = useMemo(() => flatten(items, open), [items, open]);
  const [focus, setFocus] = useState(selected || rows[0]?.id || "");
  const box = useRef<HTMLUListElement>(null);
  const cur = rows.find((r) => r.id === focus) ?? rows[0];

  const move = (to: TreeItem | undefined) => {
    if (!to) return;
    setFocus(to.id);
    requestAnimationFrame(() => box.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(to.id)}"]`)?.focus());
  };
  const toggle = (i: TreeItem, on: boolean) => setOpen((o) => { const n = new Set(o); if (on) n.add(i.id); else n.delete(i.id); return n; });
  const key = (e: KeyboardEvent, i: TreeItem) => {
    const at = rows.findIndex((r) => r.id === i.id);
    switch (e.key) {
      case "ArrowDown": move(rows[at + 1]); break;
      case "ArrowUp": move(rows[at - 1]); break;
      case "Home": move(rows[0]); break;
      case "End": move(rows[rows.length - 1]); break;
      case "ArrowRight":
        if (i.children.length && !open.has(i.id)) toggle(i, true); else move(i.children.length ? rows[at + 1] : undefined);
        break;
      case "ArrowLeft":
        if (i.children.length && open.has(i.id)) toggle(i, false);
        else move([...rows.slice(0, at)].reverse().find((r) => r.level < i.level));
        break;
      case "Enter": case " ":
        if (i.node) onSelect(i.node.id); else toggle(i, !open.has(i.id));
        break;
      default: return;
    }
    e.preventDefault();
  };
  return (
    <ul className="lw-tree" role="tree" aria-label="Objects of this Launch" ref={box}>
      {rows.map((i) => {
        const n = i.node;
        const expandable = i.children.length > 0;
        return (
          <li key={i.id} role="none">
            <div role="treeitem" data-id={i.id} aria-level={i.level} aria-expanded={expandable ? open.has(i.id) : undefined}
              aria-selected={n ? n.id === selected : undefined} tabIndex={cur?.id === i.id ? 0 : -1}
              className={"lw-row" + (n && n.id === selected ? " on" : "") + (n ? "" : " lw-sec")} style={{ paddingLeft: 8 + (i.level - 1) * 18 }}
              onKeyDown={(e) => key(e, i)} onFocus={() => setFocus(i.id)}
              onClick={() => { setFocus(i.id); if (n) onSelect(n.id); else toggle(i, !open.has(i.id)); }}>
              <span className="lw-caret" aria-hidden="true">{expandable ? (open.has(i.id) ? "▾" : "▸") : ""}</span>
              {n ? (
                <>
                  <span className="lw-kind">{n.kind}</span>{" "}
                  <span className="lw-name">{n.name}</span>{" "}
                  {n.role && <><span className={`lw-role lw-role-${n.role}`}>{n.role}</span>{" "}</>}
                  {n.replicas && <><span className="lw-reps" title="ready of wanted">{n.replicas.ready}/{n.replicas.desired}</span><SpanHidden> ready</SpanHidden>{" "}</>}
                  {n.change && n.change !== "unchanged" && <><ChangeTag c={n.change} />{" "}</>}
                  <HealthTag h={n.health} />
                </>
              ) : <span className="lw-name">{i.label}</span>}
            </div>
          </li>
        );
      })}
    </ul>
  );
}
