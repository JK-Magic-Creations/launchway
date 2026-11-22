import { useId, useState } from "react";
import { Link } from "../components/ui";
import { Button } from "../kit/layout";
import { navigate } from "../router";
import { announce } from "../kit/announce";
import { ActionMenu, Blank, dueClass, ErrorPanel, itemHref, Labels, Loading, Prio, scopeHref, scopeLabel, useLoad, Who, WorkPage } from "./common";
import { workApi, type Board as BoardData, type Column, type Item, type Sprint } from "./workApi";
import { terms } from "../terms";
import { SpanFaintSmall } from "../kit/bits";

// The board: one column for each state of the workflow. Dragging a card is a convenience; every move can also be made with the
// keyboard through the card's "Move to…" menu, which also reorders. What was done is said in a live region.

export default function BoardPage({ scope }: { scope: string }) {
  const [q, setQ] = useState("");
  const [typed, setTyped] = useState("");
  const [lanes, setLanes] = useState("");
  const [sprint, setSprint] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState<unknown>(null);
  const qid = useId();
  const board = useLoad<BoardData>(`board|${scope}|${q}|${lanes}|${sprint}`, () => workApi.board(scope, { q, lanes, sprint }));
  const sprints = useLoad<Sprint[]>("sprints|" + scope, () => workApi.sprints(scope));

  const say = (text: string) => { setMessage(""); window.setTimeout(() => setMessage(text), 20); announce(text); };
  const fail = (e: unknown) => { setError(e); say(`That did not work: ${e instanceof Error ? e.message : String(e)}`); };

  const moveTo = async (it: Item, col: Column) => {
    if (it.state === col.state) return;
    try { await workApi.move(it.ref, col.state); setError(null); say(`Moved ${it.ref} to ${col.name}.`); board.reload(); } catch (e) { fail(e); }
  };
  const reorder = async (it: Item, other: Item, where: "before" | "after") => {
    try { await workApi.rank(it.ref, where, other.ref); setError(null); say(`Moved ${it.ref} ${where === "before" ? "up" : "down"} in ${it.state_name ?? it.state}.`); board.reload(); } catch (e) { fail(e); }
  };

  const b = board.data;
  const empty = b && b.columns.every((c) => c.items.length === 0);
  return (
    <WorkPage scope={scope} on="board" title="Board" wide sentence={`Work in ${scopeLabel(scope)}, by state. Open a card's "Move to…" menu to move it with the keyboard.`}
      actions={<Button onClick={board.reload}>Refresh</Button>}>
      <section className="sheet wk-tools" aria-label="Board filters">
        <form role="search" className="wk-search" onSubmit={(e) => { e.preventDefault(); setQ(typed.trim()); }}>
          <div className="wk-f grow"><label htmlFor={qid}>Filter</label><input id={qid} type="search" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="assignee:me type:bug" spellCheck={false} /></div>
          <label className="wk-f"><span>Swimlanes</span>
            <select value={lanes} onChange={(e) => setLanes(e.target.value)}>
              <option value="">None</option><option value="assignee">By assignee</option><option value="epic">By epic</option><option value="priority">By priority</option>
            </select>
          </label>
          <label className="wk-f"><span>Sprint</span>
            <select value={sprint} onChange={(e) => setSprint(e.target.value)}>
              <option value="">All work</option><option value="active">Active sprint</option>
              {(sprints.data ?? []).map((s) => <option key={s.id} value={s.name}>{s.name}</option>)}
            </select>
          </label>
          <Button type="submit">Apply</Button>
        </form>
      </section>
      <div className="visually-hidden" role="status" aria-live="polite" aria-atomic="true" data-testid="board-live">{message}</div>
      <ErrorPanel error={error ?? board.error} onRetry={board.reload} />
      {board.loading && <Loading what="board" />}
      {empty && <Blank title="The board is empty" action={<Button kind="pri" onClick={() => navigate(scopeHref(scope))}>Go to items</Button>}>
        {q || sprint ? "Nothing matches this filter. Clear it to see every item." : terms().say("Add items to this {project} and they appear here, in the column of their state.")}</Blank>}
      {b && !empty && (b.lanes && b.lanes.length > 0
        ? b.lanes.map((l) => <Lane key={l.key} title={l.name} cols={b.columns} only={(it) => b.lane_of?.[it.id] === l.key} onMove={moveTo} onReorder={reorder} />)
        : <Lane cols={b.columns} onMove={moveTo} onReorder={reorder} />)}
    </WorkPage>
  );
}

function colHeading(c: Column): string {
  if (!c.wip) return `${c.name}, ${c.count}`;
  return `${c.name}, ${c.count} of ${c.wip}${c.over_wip ? ", over the limit" : ""}`;
}

function Lane({ title, cols, only, onMove, onReorder }: {
  title?: string; cols: Column[]; only?: (it: Item) => boolean; onMove: (it: Item, c: Column) => void; onReorder: (it: Item, other: Item, w: "before" | "after") => void;
}) {
  const hid = useId();
  const [over, setOver] = useState("");
  const Head = title ? "h3" : "h2";
  return (
    <section className="wk-lane" aria-labelledby={title ? hid : undefined} aria-label={title ? undefined : "Board"}>
      {title && <h2 id={hid} className="wk-lane-h">{title}</h2>}
      <div className="wk-board">
        {cols.map((c) => {
          const items = only ? c.items.filter(only) : c.items;
          return (
            <div key={c.state} role="group" className={"wk-col" + (c.over_wip ? " over" : "") + (over === c.state ? " drop" : "")} aria-label={colHeading(c)}
              onDragOver={(e) => { e.preventDefault(); if (over !== c.state) setOver(c.state); }}
              onDragLeave={() => setOver("")}
              onDrop={(e) => {
                e.preventDefault(); setOver("");
                const ref = e.dataTransfer.getData("text/plain");
                const it = cols.flatMap((x) => x.items).find((x) => x.ref === ref);
                if (it) onMove(it, c);
              }}>
              <header>
                <Head>{c.name}</Head>
                <span className="num count">{c.count}{c.wip ? ` of ${c.wip}` : ""}</span>
                {c.over_wip && <span className="wk-flag">Over the limit of {c.wip}</span>}
              </header>
              <ul>
                {items.map((it, i) => (
                  <Card key={it.id} it={it} cols={cols} first={i === 0} last={i === items.length - 1}
                    onMove={(col) => onMove(it, col)} onUp={() => onReorder(it, items[i - 1]!, "before")} onDown={() => onReorder(it, items[i + 1]!, "after")} />
                ))}
                {items.length === 0 && <li className="wk-none faint small">Nothing here</li>}
              </ul>
            </div>
          );
        })}
      </div>
    </section>
  );
}

function Card({ it, cols, first, last, onMove, onUp, onDown }: { it: Item; cols: Column[]; first: boolean; last: boolean; onMove: (c: Column) => void; onUp: () => void; onDown: () => void }) {
  return (
    <li>
      <article className={"wk-card" + (it.blocked ? " blocked" : "")} draggable onDragStart={(e) => { e.dataTransfer.setData("text/plain", it.ref); e.dataTransfer.effectAllowed = "move"; }} aria-label={`${it.ref}: ${it.title}`}>
        <div className="top"><span className="mono ref">{it.ref}</span><Prio p={it.priority} bare />{it.estimate ? <span className="num est" title="Estimate">{it.estimate}</span> : null}</div>
        <Link href={itemHref(it.ref)} className="wk-link">{it.title}</Link>
        <Labels list={it.labels} />
        <div className="foot">
          {it.assignees.length ? <span className="wk-people">{it.assignees.map((a) => <Who key={a.name} name={a.name} kind={a.kind} />)}</span> : <SpanFaintSmall>Unassigned</SpanFaintSmall>}
          {it.blocked && <span className="wk-flag">blocked</span>}
          {it.due_at && <span className={"small num " + dueClass(it)}>due {it.due_at.slice(0, 10)}</span>}
        </div>
        <ActionMenu label="Move to…" ariaLabel={`Move to… (${it.ref})`}
          entries={[
            ...cols.map((c) => ({ id: "to-" + c.state, label: c.name + (c.state === it.state ? " (here)" : ""), disabled: c.state === it.state, onChoose: () => onMove(c) })),
            { id: "up", label: "Move up", disabled: first, onChoose: onUp },
            { id: "down", label: "Move down", disabled: last, onChoose: onDown },
          ]} />
      </article>
    </li>
  );
}
