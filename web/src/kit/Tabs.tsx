import { useRef, type KeyboardEvent, type ReactNode } from "react";

export interface TabDef { id: string; label: ReactNode; soon?: boolean }

/** Tabs switch between views of one page. Arrow keys move between them, Home and End jump, and only the chosen
 * tab is in the tab order. The panel is the caller's: give it role="tabpanel" and aria-labelledby={`tab-${id}`}. */
export function Tabs({ tabs, on, onChange, label }: { tabs: TabDef[]; on: string; onChange: (id: string) => void; label: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const key = (e: KeyboardEvent) => {
    const i = tabs.findIndex((t) => t.id === on);
    let j = i;
    if (e.key === "ArrowRight") j = (i + 1) % tabs.length;
    else if (e.key === "ArrowLeft") j = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") j = 0;
    else if (e.key === "End") j = tabs.length - 1;
    else return;
    e.preventDefault();
    onChange(tabs[j].id);
    ref.current?.querySelectorAll<HTMLElement>('[role="tab"]')[j]?.focus();
  };
  return (
    <div className="vtabs" role="tablist" aria-label={label} ref={ref}>
      {tabs.map((t) => (
        <button key={t.id} type="button" role="tab" id={`tab-${t.id}`} aria-selected={on === t.id} tabIndex={on === t.id ? 0 : -1} onClick={() => onChange(t.id)} onKeyDown={key}>
          {t.label}{t.soon && <span className="soon-tag">coming</span>}
        </button>
      ))}
    </div>
  );
}
