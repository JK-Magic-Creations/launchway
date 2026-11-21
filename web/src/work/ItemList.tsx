import { useEffect, useId, useState } from "react";
import { Link } from "../components/ui";
import { Button } from "../kit/layout";
import { announce } from "../kit/announce";
import { Markdown } from "../markdown";
import { navigate } from "../router";
import { Blank, csv, dueClass, ErrorPanel, itemHref, Labels, Loading, Prio, scopeLabel, StateTag, useLoad, Who, WorkPage } from "./common";
import { workApi, scopeObject, type Item, type Page, type View } from "./workApi";

// The items of one project: a query box in the query language, filters, saved views, sorting, paging and a form for a new item.

const LIMIT = 25;
const TYPES = ["issue", "bug", "story", "task", "epic"];
const PRIORITIES = ["urgent", "high", "normal", "low"];
const SORTS: [string, string][] = [["rank", "Rank"], ["updated", "Last changed"], ["created", "Created"], ["priority", "Priority"], ["due", "Due date"], ["number", "Number"]];
const EXAMPLES = ["is:open", "assignee:me", "is:blocked", "type:bug priority:high", "updated:<7d", "is:unassigned"];

/** Puts key:value in a query, replacing any term with the same key; an empty value removes it. */
export function setTerm(q: string, key: string, value: string): string {
  const parts = (q.match(/(?:[^\s"]+:"[^"]*"|"[^"]*"|\S+)/g) ?? []).filter((t) => !t.toLowerCase().startsWith(key + ":"));
  if (value) parts.push(`${key}:${/\s/.test(value) ? `"${value}"` : value}`);
  return parts.join(" ");
}
export const termOf = (q: string, key: string) => new RegExp(`(?:^|\\s)${key}:("[^"]*"|\\S+)`).exec(q)?.[1]?.replace(/"/g, "") ?? "";

export default function ItemList({ scope }: { scope: string }) {
  const first = new URLSearchParams(location.search);
  const [q, setQ] = useState(first.get("q") ?? "is:open");
  const [typed, setTyped] = useState(q);
  const [sort, setSort] = useState(first.get("sort") ?? "rank");
  const [desc, setDesc] = useState(first.get("desc") === "true");
  const [offset, setOffset] = useState(Number(first.get("offset") ?? 0) || 0);
  const [adding, setAdding] = useState(false);
  const qid = useId();
  const help = useId();

  const list = useLoad<Page>(`${scope}|${q}|${sort}|${desc}|${offset}`, () => workApi.list(scope, { q, sort, desc, limit: LIMIT, offset }));
  const views = useLoad<View[]>(`views|${scope}`, () => workApi.views(scope));

  useEffect(() => {
    const p = new URLSearchParams();
    if (q !== "is:open") p.set("q", q);
    if (sort !== "rank") p.set("sort", sort);
    if (desc) p.set("desc", "true");
    if (offset) p.set("offset", String(offset));
    const s = p.toString();
    history.replaceState(null, "", location.pathname + (s ? "?" + s : ""));
  }, [q, sort, desc, offset]);

  const run = (next: string) => { setQ(next); setTyped(next); setOffset(0); };
  const total = list.data?.total ?? 0;
  const from = total ? offset + 1 : 0;
  const to = Math.min(offset + LIMIT, total);

  return (
    <WorkPage scope={scope} on="items" title="Items" sentence={`Everything tracked in ${scopeLabel(scope)}: issues, bugs, stories, tasks and epics.`}
      actions={<Button kind="pri" icon="plus" onClick={() => setAdding(!adding)} aria-expanded={adding}>New item</Button>}>
      {adding && <NewItem scope={scope} onClose={() => setAdding(false)} />}
      <section className="sheet wk-tools" aria-label="Find items">
        <form role="search" className="wk-search" onSubmit={(e) => { e.preventDefault(); run(typed.trim()); }}>
          <div className="wk-f grow">
            <label htmlFor={qid}>Query</label>
            <input id={qid} type="search" value={typed} onChange={(e) => setTyped(e.target.value)} aria-describedby={help} maxLength={500} spellCheck={false} autoComplete="off" />
          </div>
          <Button type="submit" kind="pri">Search</Button>
        </form>
        <p className="faint small" id={help}>
          Terms are separated by spaces: <code>state:in_progress</code> <code>type:bug</code> <code>label:ui</code> <code>-label:wontfix</code> <code>assignee:me</code> <code>due:&lt;today</code> and plain words. A leading minus negates a term.
        </p>
        <div className="wk-examples" aria-label="Examples">
          {EXAMPLES.map((x) => <button key={x} type="button" className="wk-ex" onClick={() => run(x)}>{x}</button>)}
        </div>
        <div className="wk-filters">
          <Select label="Type" value={termOf(q, "type")} options={TYPES} onChange={(v) => run(setTerm(q, "type", v))} any="Any type" />
          <Select label="Priority" value={termOf(q, "priority")} options={PRIORITIES} onChange={(v) => run(setTerm(q, "priority", v))} any="Any priority" />
          <Select label="Status" value={/\bis:closed\b/.test(q) ? "closed" : /\bis:open\b/.test(q) ? "open" : ""} options={["open", "closed"]}
            onChange={(v) => run(setTerm(setTerm(q, "is", ""), "is", v))} any="Open and closed" />
          <label className="wk-f"><span>Sort by</span>
            <select value={sort} onChange={(e) => { setSort(e.target.value); setOffset(0); }}>{SORTS.map(([k, n]) => <option key={k} value={k}>{n}</option>)}</select>
          </label>
          <label className="wk-check"><input type="checkbox" checked={desc} onChange={(e) => { setDesc(e.target.checked); setOffset(0); }} />Reverse order</label>
        </div>
        <SavedViews scope={scope} q={q} views={views.data ?? []} onPick={run} onChanged={views.reload} />
      </section>

      <ErrorPanel error={list.error} onRetry={list.reload} />
      {list.loading && <Loading what="items" />}
      {list.data && list.data.items.length === 0 && (
        q.trim() && q !== "is:open"
          ? <Blank title="Nothing matches this query" action={<Button onClick={() => run("is:open")}>Show open items</Button>}>Try fewer terms, or start from one of the examples above.</Blank>
          : <Blank title="No open items yet" action={<Button kind="pri" icon="plus" onClick={() => setAdding(true)}>New item</Button>}>Add the first issue to start tracking work in {scopeLabel(scope)}. Pull requests that name an item link to it on their own.</Blank>
      )}
      {list.data && list.data.items.length > 0 && (
        <>
          <div className="sheet tablewrap">
            <table className="table wk-table">
              <caption className="visually-hidden">Items in {scopeLabel(scope)}, {from} to {to} of {total}</caption>
              <thead><tr><th scope="col">Item</th><th scope="col">State</th><th scope="col">Priority</th><th scope="col">Assigned</th><th scope="col">Estimate</th><th scope="col">Due</th></tr></thead>
              <tbody>{list.data.items.map((it) => <Row key={it.id} it={it} />)}</tbody>
            </table>
          </div>
          <nav className="wk-pager" aria-label="Pages of items">
            <span className="faint num">{from} to {to} of {total}</span>
            <Button sm disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - LIMIT))}>Previous</Button>
            <Button sm disabled={offset + LIMIT >= total} onClick={() => setOffset(offset + LIMIT)}>Next</Button>
          </nav>
        </>
      )}
    </WorkPage>
  );
}

function Select({ label, value, options, onChange, any }: { label: string; value: string; options: string[]; onChange: (v: string) => void; any: string }) {
  return (
    <label className="wk-f"><span>{label}</span>
      <select value={value} onChange={(e) => onChange(e.target.value)}><option value="">{any}</option>{options.map((o) => <option key={o} value={o}>{o}</option>)}</select>
    </label>
  );
}

function Row({ it }: { it: Item }) {
  return (
    <tr>
      <td>
        <div className="wk-title">
          <span className="ref mono">{it.ref}</span> <span className="faint">{it.type}</span>
          {it.blocked && <span className="wk-flag">blocked</span>}
          {it.claim && !it.claim.expired && <span className="wk-agent-badge">agent working</span>}
        </div>
        <Link href={itemHref(it.ref)} className="wk-link">{it.title}</Link>
        <Labels list={it.labels} />
      </td>
      <td><StateTag item={it} /></td>
      <td><Prio p={it.priority} /></td>
      <td>{it.assignees.length ? <ul className="wk-people">{it.assignees.map((a) => <li key={a.name}><Who name={a.name} kind={a.kind} /></li>)}</ul> : <span className="faint">Unassigned</span>}</td>
      <td className="num">{it.estimate ? it.estimate : <span className="faint">None</span>}</td>
      <td className={"num " + dueClass(it)}>{it.due_at ? it.due_at.slice(0, 10) : <span className="faint">None</span>}</td>
    </tr>
  );
}

function SavedViews({ scope, q, views, onPick, onChanged }: { scope: string; q: string; views: View[]; onPick: (q: string) => void; onChanged: () => void }) {
  const [name, setName] = useState("");
  const [shared, setShared] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [chosen, setChosen] = useState("");
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    try { await workApi.saveView(scope, name.trim(), q, shared); setName(""); setError(null); onChanged(); announce("View saved."); } catch (x) { setError(x); }
  };
  const remove = async () => {
    try { await workApi.deleteView(chosen); setChosen(""); onChanged(); announce("View deleted."); } catch (x) { setError(x); }
  };
  return (
    <div className="wk-views">
      <label className="wk-f"><span>Saved views</span>
        <select value={chosen} onChange={(e) => { setChosen(e.target.value); const v = views.find((x) => x.id === e.target.value); if (v) onPick(v.query); }}>
          <option value="">Choose a view</option>
          {views.map((v) => <option key={v.id} value={v.id}>{v.name}{v.shared ? " (shared)" : ""}</option>)}
        </select>
      </label>
      {chosen && <Button sm kind="bad" onClick={remove}>Delete view</Button>}
      <form className="wk-inline" onSubmit={save}>
        <label className="wk-f"><span>Save this query as</span><input value={name} onChange={(e) => setName(e.target.value)} maxLength={80} required /></label>
        <label className="wk-check"><input type="checkbox" checked={shared} onChange={(e) => setShared(e.target.checked)} />Share with everyone who reads this project</label>
        <Button type="submit" sm disabled={!name.trim()}>Save view</Button>
      </form>
      <ErrorPanel error={error} />
    </div>
  );
}

/** The writing area for a Markdown body with a preview drawn by the same safe renderer as every other text. */
export function BodyField({ value, onChange, label = "Description" }: { value: string; onChange: (v: string) => void; label?: string }) {
  const [preview, setPreview] = useState(false);
  const id = useId();
  return (
    <div className="wk-f">
      <div className="wk-f-head">
        <label htmlFor={id}>{label}</label>
        <button type="button" className="wk-textbtn" aria-pressed={preview} onClick={() => setPreview(!preview)}>{preview ? "Edit" : "Preview"}</button>
      </div>
      {preview
        ? <div className="wk-preview" aria-label="Preview">{value.trim() ? <Markdown text={value} /> : <span className="faint">Nothing to preview.</span>}</div>
        : <textarea id={id} rows={6} value={value} onChange={(e) => onChange(e.target.value)} maxLength={20000} placeholder="Markdown: **bold**, `code`, lists, links." />}
      <span className="faint small">Markdown is allowed. Nothing you write is run as a page.</span>
    </div>
  );
}

function NewItem({ scope, onClose }: { scope: string; onClose: () => void }) {
  const [f, setF] = useState({ type: "issue", title: "", body: "", priority: "normal", labels: "", assignees: "", estimate: "", parent: "", milestone: "" });
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setF({ ...f, [k]: e.target.value });
  const ids = { title: useId(), type: useId(), prio: useId(), labels: useId(), who: useId(), est: useId(), parent: useId(), ms: useId() };
  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const it = await workApi.create({
        scope: scopeObject(scope), type: f.type, title: f.title.trim(), body: f.body, priority: f.priority, labels: csv(f.labels), assignees: csv(f.assignees),
        estimate: f.estimate ? Number(f.estimate) : undefined, parent: f.parent.trim() || undefined, milestone: f.milestone.trim() || undefined,
      });
      announce(`Created ${it.ref}.`);
      navigate(itemHref(it.ref));
    } catch (x) { setError(x); setBusy(false); }
  };
  return (
    <form className="sheet pad wk-new" onSubmit={create} aria-label="New item">
      <h2 className="h-sm">New item</h2>
      <div className="wk-grid">
        <div className="wk-f"><label htmlFor={ids.type}>Type</label><select id={ids.type} value={f.type} onChange={set("type")}>{TYPES.map((t) => <option key={t}>{t}</option>)}</select></div>
        <div className="wk-f span"><label htmlFor={ids.title}>Title</label><input id={ids.title} value={f.title} onChange={set("title")} required maxLength={300} /></div>
        <div className="wk-f"><label htmlFor={ids.prio}>Priority</label><select id={ids.prio} value={f.priority} onChange={set("priority")}>{PRIORITIES.map((t) => <option key={t}>{t}</option>)}</select></div>
      </div>
      <BodyField value={f.body} onChange={(v) => setF({ ...f, body: v })} />
      <div className="wk-grid">
        <div className="wk-f"><label htmlFor={ids.labels}>Labels</label><input id={ids.labels} value={f.labels} onChange={set("labels")} placeholder="ui, docs" /></div>
        <div className="wk-f"><label htmlFor={ids.who}>Assignees</label><input id={ids.who} value={f.assignees} onChange={set("assignees")} placeholder="names, separated by commas" /></div>
        <div className="wk-f"><label htmlFor={ids.est}>Estimate</label><input id={ids.est} type="number" min="0" step="0.5" value={f.estimate} onChange={set("estimate")} /></div>
        <div className="wk-f"><label htmlFor={ids.parent}>Parent</label><input id={ids.parent} value={f.parent} onChange={set("parent")} placeholder="PAY-3" /></div>
        <div className="wk-f"><label htmlFor={ids.ms}>Milestone</label><input id={ids.ms} value={f.milestone} onChange={set("milestone")} /></div>
      </div>
      <ErrorPanel error={error} />
      <div className="acts"><Button type="submit" kind="pri" disabled={busy || !f.title.trim()}>Create item</Button><Button onClick={onClose}>Cancel</Button></div>
    </form>
  );
}
