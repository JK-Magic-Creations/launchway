import { useState } from "react";
import { announce } from "../../kit/announce";
import { Dialog } from "../../kit/Dialog";
import { PANEL_TYPES, panelType, type Field, type PanelDef, type Size } from "./registry";
import "./panels.css";

export interface HomeDoc { view?: "mine" | "all"; order?: string[]; hidden?: string[]; open?: Record<string, boolean>; panels?: PanelDef[] }
export const BUILTIN: { id: string; title: string }[] = [
  { id: "needs", title: "Needs you" }, { id: "running", title: "Running now" }, { id: "favorites", title: "Favorites" },
  { id: "continue", title: "Pick up where you left off" }, { id: "categories", title: "By category" }, { id: "recent", title: "Recently finished" },
];

/** The sections of a layout document in order: the ones it names, then any it does not know yet (a new one appears at its default place). */
export function arranged(doc: HomeDoc, panels: PanelDef[]): { id: string; title: string; size: Size; custom?: PanelDef }[] {
  const all = [...BUILTIN.map((b) => ({ id: b.id, title: b.title, size: 3 as Size })), ...panels.map((p) => ({ id: p.id, title: p.title, size: p.size, custom: p }))];
  const known = new Set(all.map((a) => a.id));
  const named = (doc.order ?? []).filter((id, i, l) => known.has(id) && l.indexOf(id) === i);
  const rest = all.filter((a) => !named.includes(a.id)).map((a) => a.id);
  return [...named, ...rest].map((id) => all.find((a) => a.id === id)!);
}

/** A short id for a new panel; it only has to differ from the ids of the panels already on the page. */
function newPanelId(): string { return "p:" + Math.random().toString(36).slice(2, 9); }

type Save = (mode: "me" | "everyone", patch: HomeDoc) => Promise<void>;

/** "Arrange": reorder, hide, resize, add and remove the panels of Home, either for yourself or, with the right, for everyone. By drag, and by
 * buttons for the keyboard and screen readers. */
export function Arrange({ docs, panelsOf, canShare, customized, onSave, onReset }: {
  docs: { me: HomeDoc; everyone: HomeDoc }; panelsOf: (d: HomeDoc) => PanelDef[]; canShare: boolean; customized: boolean; onSave: Save; onReset: () => void;
}) {
  const [mode, setMode] = useState<"me" | "everyone">("me");
  const [adding, setAdding] = useState(false);
  const [dragging, setDragging] = useState<string | null>(null);
  const [over, setOver] = useState<string | null>(null);
  const doc = docs[mode];
  const panels = panelsOf(doc);
  const items = arranged(doc, panels);
  const hidden = new Set(doc.hidden ?? []);
  const order = items.map((i) => i.id);
  const save = (patch: HomeDoc) => onSave(mode, patch);
  const move = (id: string, to: number) => {
    const next = order.filter((x) => x !== id);
    next.splice(Math.max(0, Math.min(to, next.length)), 0, id);
    void save({ order: next });
    announce(`${items.find((i) => i.id === id)?.title} moved to position ${next.indexOf(id) + 1} of ${next.length}.`);
  };
  const toggle = (id: string) => { const h = new Set(hidden); if (h.has(id)) h.delete(id); else h.add(id); void save({ hidden: [...h] }); announce(`${items.find((i) => i.id === id)?.title} ${h.has(id) ? "hidden" : "shown"}.`); };
  const resize = (p: PanelDef, size: Size) => { void save({ panels: panels.map((x) => (x.id === p.id ? { ...x, size } : x)) }); announce(`${p.title} is now ${size} ${size === 1 ? "column" : "columns"} wide.`); };
  const remove = (p: PanelDef) => { void save({ panels: panels.filter((x) => x.id !== p.id), order: order.filter((x) => x !== p.id) }); announce(`${p.title} removed.`); };
  return (
    <details className="hm-cust">
      <summary className="btn btn-sm">Arrange</summary>
      <div className="hm-cust-b" role="group" aria-label="Arrange Home">
        <fieldset className="ar-modes">
          <legend className="sr-only">Arrange for</legend>
          <label><input type="radio" name="ar-mode" checked={mode === "me"} onChange={() => setMode("me")} /> Arrange for me</label>
          {canShare && <label><input type="radio" name="ar-mode" checked={mode === "everyone"} onChange={() => setMode("everyone")} /> Arrange for everyone</label>}
        </fieldset>
        <p className="faint small">{mode === "me" ? "This changes only your Tower." : "This is the default for everyone who has not arranged their own. People who have keep their arrangement."}</p>
        <ul className="ar-list" aria-label="Sections and panels, in order">
          {items.map((it, i) => (
            <li key={it.id} draggable onDragStart={(e) => { setDragging(it.id); e.dataTransfer.setData("text/plain", it.id); }} onDragEnd={() => { setDragging(null); setOver(null); }}
              onDragOver={(e) => { e.preventDefault(); setOver(it.id); }} onDrop={(e) => { e.preventDefault(); if (dragging && dragging !== it.id) move(dragging, i); setOver(null); }} className={over === it.id && dragging !== it.id ? "over" : ""}>
              <span className={hidden.has(it.id) ? "faint" : ""}>{it.title}{hidden.has(it.id) ? " (hidden)" : ""}{it.custom ? " · panel" : ""}</span>
              <button type="button" className="btn btn-sm" disabled={i === 0} onClick={() => move(it.id, i - 1)} aria-label={`Move ${it.title} up`}>Up</button>
              <button type="button" className="btn btn-sm" disabled={i === items.length - 1} onClick={() => move(it.id, i + 1)} aria-label={`Move ${it.title} down`}>Down</button>
              <button type="button" className="btn btn-sm" onClick={() => toggle(it.id)}>{hidden.has(it.id) ? "Show" : "Hide"}<span className="sr-only"> {it.title}</span></button>
              {it.custom && <>
                <label className="small">Width <select value={it.custom.size} aria-label={`Width of ${it.title}`} onChange={(e) => resize(it.custom!, Number(e.target.value) as Size)}><option value={1}>1 column</option><option value={2}>2 columns</option><option value={3}>3 columns</option></select></label>
                <button type="button" className="btn btn-sm" onClick={() => remove(it.custom!)} aria-label={`Remove ${it.title}`}>Remove</button>
              </>}
            </li>
          ))}
        </ul>
        <div className="pn-ctl">
          <button type="button" className="btn btn-sm btn-primary" onClick={() => setAdding(true)}>Add a panel</button>
          <button type="button" className="btn btn-sm" disabled={mode === "me" && !customized} onClick={onReset}>Reset to the default</button>
        </div>
      </div>
      {adding && <Dialog label="Add a panel" onClose={() => setAdding(false)}><AddPanel onClose={() => setAdding(false)} onAdd={(p) => { void save({ panels: [...panels, p], order: [...order, p.id] }); announce(`${p.title} added.`); setAdding(false); }} /></Dialog>}
    </details>
  );
}

function AddPanel({ onAdd, onClose }: { onAdd: (p: PanelDef) => void; onClose: () => void }) {
  const [type, setType] = useState(PANEL_TYPES[0].type);
  const t = panelType(type)!;
  const [title, setTitle] = useState("");
  const [size, setSize] = useState<Size>(t.defaultSize);
  const [vals, setVals] = useState<Record<string, string>>({});
  const missing = t.fields.some((f: Field) => f.required && !(vals[f.key] ?? "").trim());
  return (
    <form className="ar-form" onSubmit={(e) => { e.preventDefault(); if (!missing) onAdd({ id: newPanelId(), type, title: title.trim() || t.label, size, settings: vals }); }}>
      <h2 className="h-sm">Add a panel</h2>
      <label>Kind of panel
        <select data-autofocus value={type} onChange={(e) => { setType(e.target.value); setSize(panelType(e.target.value)!.defaultSize); setVals({}); }}>
          {PANEL_TYPES.map((p) => <option key={p.type} value={p.type}>{p.label}</option>)}
        </select>
      </label>
      <p className="faint small">{t.description}</p>
      <label>Title<input value={title} maxLength={80} placeholder={t.label} onChange={(e) => setTitle(e.target.value)} /></label>
      {t.fields.map((f) => (
        <label key={f.key}>{f.label}
          {f.kind === "textarea" ? <textarea value={vals[f.key] ?? ""} maxLength={4000} onChange={(e) => setVals({ ...vals, [f.key]: e.target.value })} required={f.required} />
            : <input value={vals[f.key] ?? ""} maxLength={300} onChange={(e) => setVals({ ...vals, [f.key]: e.target.value })} required={f.required} />}
          {f.help && <span className="faint small">{f.help}</span>}
        </label>
      ))}
      <label>Width<select value={size} onChange={(e) => setSize(Number(e.target.value) as Size)}><option value={1}>1 column</option><option value={2}>2 columns</option><option value={3}>3 columns</option></select></label>
      <div className="xp-dialog-f"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn-primary" disabled={missing}>Add panel</button></div>
    </form>
  );
}
