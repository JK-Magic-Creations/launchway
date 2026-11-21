import { useCallback, useRef, useState } from "react";

export interface PageTab { id: string; label: string }

/** The tab a page is on, kept in the address (?tab=) so it can be linked and survives a reload. */
export function useTab(ids: string[], first: string): [string, (id: string) => void] {
  const read = () => { const t = new URLSearchParams(location.search).get("tab"); return t && ids.includes(t) ? t : first; };
  const [tab, setTabState] = useState(read);
  const setTab = useCallback((id: string) => {
    setTabState(id);
    const q = new URLSearchParams(location.search);
    if (id === first) q.delete("tab"); else q.set("tab", id);
    const s = q.toString();
    history.replaceState(null, "", location.pathname + (s ? "?" + s : "") + location.hash);
  }, [first]);
  return [tab, setTab];
}

/** PageTabs switch between views of one thing, without leaving its page. Only the chosen tab is in the Tab order; the
 * arrow keys, Home and End move between tabs and choose them (the ARIA tabs pattern). */
export function PageTabs({ tabs, current, onChange, label = "Views of this page" }: { tabs: PageTab[]; current: string; onChange: (id: string) => void; label?: string }) {
  const list = useRef<HTMLDivElement>(null);
  const key = (e: React.KeyboardEvent) => {
    const i = tabs.findIndex((t) => t.id === current);
    let j = i;
    if (e.key === "ArrowRight") j = (i + 1) % tabs.length;
    else if (e.key === "ArrowLeft") j = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") j = 0;
    else if (e.key === "End") j = tabs.length - 1;
    else return;
    e.preventDefault();
    onChange(tabs[j].id);
    list.current?.querySelectorAll<HTMLElement>('[role="tab"]')[j]?.focus();
  };
  return (
    <div className="tabs" role="tablist" aria-label={label} ref={list}>
      {tabs.map((t) => (
        <button key={t.id} role="tab" type="button" id={`pagetab-${t.id}`} aria-selected={current === t.id} tabIndex={current === t.id ? 0 : -1} className={"tab" + (current === t.id ? " tab-on" : "")}
          onClick={() => onChange(t.id)} onKeyDown={key}>{t.label}</button>
      ))}
    </div>
  );
}
