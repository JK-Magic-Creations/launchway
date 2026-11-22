import { useEffect, useId, useRef, useState } from "react";
import { Link } from "../components/ui";
import { Button } from "../kit/layout";
import { announce } from "../kit/announce";
import { ActionMenu, Blank, ErrorPanel, itemHref, Labels, Loading, Prio, scopeHref, scopeLabel, useLoad, WorkPage } from "./common";
import { workApi, type Item, type Page, type Sprint } from "./workApi";
import { navigate } from "../router";

// The backlog: open work in the order it will be done. Each row has a menu to move it to the top, the bottom, up, down or
// before another item, and a control to put it in a sprint. All of it works with the keyboard alone.

export default function Backlog({ scope }: { scope: string }) {
  const list = useLoad<Page>("backlog|" + scope, () => workApi.list(scope, { q: "is:open", sort: "rank", limit: 200 }));
  const sprints = useLoad<Sprint[]>("sprints|" + scope, () => workApi.sprints(scope));
  const [message, setMessage] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [before, setBefore] = useState<string | null>(null);
  const say = (t: string) => { setMessage(""); window.setTimeout(() => setMessage(t), 20); announce(t); };
  const items = list.data?.items ?? [];

  const rank = async (it: Item, where: "top" | "bottom" | "before" | "after", other: Item | undefined, said: string) => {
    try { await workApi.rank(it.ref, where, other?.ref); setError(null); say(said); list.reload(); } catch (e) { setError(e); say(`That did not work: ${e instanceof Error ? e.message : String(e)}`); }
  };
  const toSprint = async (it: Item, name: string) => {
    try { await workApi.update(it.ref, { sprint: name }); setError(null); say(name ? `${it.ref} added to ${name}.` : `${it.ref} taken out of its sprint.`); list.reload(); } catch (e) { setError(e); }
  };
  const open = (sprints.data ?? []).filter((s) => s.state !== "completed");

  return (
    <WorkPage scope={scope} on="backlog" title="Backlog" sentence={`Open work in ${scopeLabel(scope)}, in the order it will be picked up. The first item is next.`}>
      <div className="visually-hidden" role="status" aria-live="polite" aria-atomic="true" data-testid="backlog-live">{message}</div>
      <ErrorPanel error={error ?? list.error} onRetry={list.reload} />
      {list.loading && <Loading what="backlog" />}
      {list.data && items.length === 0 && <Blank title="The backlog is empty" action={<Button kind="pri" onClick={() => navigate(scopeHref(scope))}>Add an item</Button>}>Nothing is waiting. New items land here in the order they are ranked.</Blank>}
      {items.length > 0 && (
        <ol className="sheet wk-backlog" aria-label={`Backlog of ${scopeLabel(scope)}, in rank order`}>
          {items.map((it, i) => (
            <li key={it.id}>
              <div className="wk-bl-row">
                <span className="pos num" aria-label={`Position ${i + 1}`}>{i + 1}</span>
                <div className="what">
                  <div className="wk-title"><span className="mono ref">{it.ref}</span> <span className="faint">{it.type}</span> {it.blocked && <span className="wk-flag">blocked</span>}</div>
                  <Link href={itemHref(it.ref)} className="wk-link">{it.title}</Link>
                  <Labels list={it.labels} />
                </div>
                <Prio p={it.priority} />
                <span className="num est" title="Estimate">{it.estimate || ""}</span>
                <label className="wk-f sprintpick"><span className="visually-hidden">Sprint for {it.ref}</span>
                  <select value={it.sprint ?? ""} onChange={(e) => void toSprint(it, e.target.value)}>
                    <option value="">No sprint</option>
                    {open.map((s) => <option key={s.id} value={s.name}>{s.name}</option>)}
                    {it.sprint && !open.some((s) => s.name === it.sprint) && <option value={it.sprint}>{it.sprint}</option>}
                  </select>
                </label>
                <ActionMenu label="Move…" ariaLabel={`Move… (${it.ref})`} entries={[
                  { id: "top", label: "Move to top", disabled: i === 0, onChoose: () => void rank(it, "top", undefined, `${it.ref} moved to the top of the backlog.`) },
                  { id: "up", label: "Move up", disabled: i === 0, onChoose: () => void rank(it, "before", items[i - 1], `${it.ref} moved up to position ${i}.`) },
                  { id: "down", label: "Move down", disabled: i === items.length - 1, onChoose: () => void rank(it, "after", items[i + 1], `${it.ref} moved down to position ${i + 2}.`) },
                  { id: "bottom", label: "Move to bottom", disabled: i === items.length - 1, onChoose: () => void rank(it, "bottom", undefined, `${it.ref} moved to the bottom of the backlog.`) },
                  { id: "before", label: "Move before…", onChoose: () => setBefore(it.ref) },
                ]} />
              </div>
              {before === it.ref && (
                <BeforePicker it={it} items={items} onCancel={() => setBefore(null)}
                  onPick={(other) => { setBefore(null); void rank(it, "before", other, `${it.ref} moved before ${other.ref}.`); }} />
              )}
            </li>
          ))}
        </ol>
      )}
    </WorkPage>
  );
}

function BeforePicker({ it, items, onPick, onCancel }: { it: Item; items: Item[]; onPick: (o: Item) => void; onCancel: () => void }) {
  const id = useId();
  const others = items.filter((x) => x.id !== it.id);
  const [ref, setRef] = useState(others[0]?.ref ?? "");
  const pick = useRef<HTMLSelectElement>(null);
  useEffect(() => { pick.current?.focus(); }, []);
  return (
    <form className="wk-inline wk-before" onSubmit={(e) => { e.preventDefault(); const o = others.find((x) => x.ref === ref); if (o) onPick(o); }}>
      <div className="wk-f grow">
        <label htmlFor={id}>Move {it.ref} before</label>
        <select id={id} value={ref} onChange={(e) => setRef(e.target.value)} ref={pick}>{others.map((o) => <option key={o.id} value={o.ref}>{o.ref} {o.title}</option>)}</select>
      </div>
      <Button type="submit" sm kind="pri" disabled={!ref}>Move</Button>
      <Button sm onClick={onCancel}>Cancel</Button>
    </form>
  );
}
