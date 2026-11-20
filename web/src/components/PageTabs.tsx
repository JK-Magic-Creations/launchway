import { useCallback, useState } from "react";

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

/** PageTabs switch between views of one thing, without leaving its page. */
export function PageTabs({ tabs, current, onChange }: { tabs: PageTab[]; current: string; onChange: (id: string) => void }) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => (
        <button key={t.id} role="tab" type="button" aria-selected={current === t.id} className={"tab" + (current === t.id ? " tab-on" : "")} onClick={() => onChange(t.id)}>{t.label}</button>
      ))}
    </div>
  );
}
