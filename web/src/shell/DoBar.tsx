import { useMemo, useState } from "react";
import type { Project } from "../api";
import { Dialog } from "../kit/Dialog";
import { Icon } from "../kit/Icon";
import { navigate } from "../router";
import type { Ctx } from "../nav/model";
import { doItems, searchItems } from "./doItems";

/** DoBarButton is the field in the top bar. It opens the overlay; the slash key does too. */
export function DoBarButton({ onOpen }: { onOpen: () => void }) {
  return (
    <button type="button" className="dobar" onClick={onOpen} aria-label="What do you want to do?" aria-keyshortcuts="/">
      <Icon name="search" />
      <span className="t">What do you want to do?</span>
      <kbd>/</kbd>
    </button>
  );
}

/** DoBar is the overlay: type what you want, or pick from the list. Wizards, actions and every page are in it. */
export function DoBar({ ctx, projects, onClose }: { ctx: Ctx; projects: Project[]; onClose: () => void }) {
  const [q, setQ] = useState("");
  const [at, setAt] = useState(0);
  const items = useMemo(() => doItems(ctx, projects), [ctx, projects]);
  const found = useMemo(() => searchItems(items, q), [items, q]);
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
        {found.length === 0 && <p className="faint nores">Nothing matches "{q}". Try a project's name, "add", "approve" or the name of a page.</p>}
        {found.map((it, i) => (
          <a key={it.id} id={"do-" + it.id} role="option" aria-selected={i === at} href={it.href} className={i === at ? "sel" : ""}
            onClick={(e) => { e.preventDefault(); go(it.href); }} onMouseMove={() => setAt(i)}>
            <Icon name={it.icon} /><span><b>{it.label}</b><span className="faint"> {it.hint}</span></span>
          </a>
        ))}
      </div>
    </Dialog>
  );
}
