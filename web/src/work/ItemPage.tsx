import { useId, useState } from "react";
import { useMe } from "../auth";
import { Link } from "../components/ui";
import { Button, Sheet } from "../kit/layout";
import { announce } from "../kit/announce";
import { Markdown } from "../markdown";
import { BodyField } from "./ItemList";
import { Blank, csv, ErrorPanel, itemHref, Labels, Loading, money, Prio, Progress, scopeHref, scopeLabel, scopeOf, StateTag, useLoad, when, Who, WorkPage } from "./common";
import { workApi, type Comment, type ItemDetail, type Link as ItemLink, type Page, type WorkEvent } from "./workApi";
import { terms } from "../terms";

// One item: its fields, description, acceptance checklist, links, code, delivery status, comments and history.

const TYPES = ["issue", "bug", "story", "task", "epic"];
const PRIORITIES = ["urgent", "high", "normal", "low"];
const STATES = [["backlog", "Backlog"], ["ready", "Ready"], ["in_progress", "In progress"], ["in_review", "In review"], ["done", "Done"], ["wont_do", "Won't do"]];

export default function ItemPage({ itemRef }: { itemRef: string }) {
  const item = useLoad<ItemDetail>("item|" + itemRef, () => workApi.get(itemRef));
  const it = item.data;
  if (item.error) return <WorkPage title={itemRef}><ErrorPanel error={item.error} onRetry={item.reload} /></WorkPage>;
  if (!it) return <WorkPage title={itemRef}><Loading what="item" /></WorkPage>;
  const can = (a: string) => it.actions.includes(a);
  const scope = scopeOf(it);
  return (
    <WorkPage scope={scope} on="items" eyebrow={<><Link href="/work">Work</Link> / <Link href={scopeHref(scope)}>{scopeLabel(scope)}</Link></>}
      title={<><span className="mono wk-ref">{it.ref}</span> {it.title}</>}
      sentence={<span className="wk-chips"><StateTag item={it} /><Prio p={it.priority} /><span className="faint">{it.type}</span>{it.blocked && <span className="wk-flag">blocked by {it.blocked_by?.join(", ")}</span>}</span>}>
      {it.labels.includes("machine-proposed") && <p className="faint small wk-proposed">Proposed by {it.labels.find((l) => l.startsWith("source:"))?.slice(7) ?? "a tool"}. See <Link href={scopeHref(scope, "proposals")}>Proposals</Link>.</p>}
      <AgentBanner it={it} onTaken={item.reload} can={can("take_over")} />
      <div className="wk-cols">
        <div className="wk-main">
          <Description it={it} editable={can("edit")} onSaved={item.reload} />
          <Criteria it={it} editable={can("edit")} onSaved={item.reload} />
          {(it.type === "epic" || it.children) && <Children it={it} />}
          <Delivery it={it} />
          <Code it={it} />
          <Comments itemRef={it.ref} canComment={can("comment")} />
          <History itemRef={it.ref} version={it.version} />
        </div>
        <aside className="wk-side" aria-label="Details">
          <Details it={it} editable={can("edit")} onSaved={item.reload} />
          <Links it={it} editable={can("link")} onChanged={item.reload} />
          <Attachments it={it} />
        </aside>
      </div>
    </WorkPage>
  );
}

function AgentBanner({ it, can, onTaken }: { it: ItemDetail; can: boolean; onTaken: () => void }) {
  const [error, setError] = useState<unknown>(null);
  const claim = it.claim && !it.claim.expired ? it.claim : null;
  const agent = claim?.agent ?? it.assignees.find((a) => a.kind === "agent")?.name;
  if (!agent && !can) return null;
  const take = async () => {
    try { await workApi.takeOver(it.ref); announce(`You took over ${it.ref}.`); onTaken(); } catch (e) { setError(e); }
  };
  return (
    <section className="wk-banner" aria-label="Agent work">
      <div>
        {claim
          ? <p><strong>Being worked by agent {claim.agent}</strong> since {when(claim.claimed_at)}, lease until {when(claim.lease_until)}.</p>
          : agent ? <p><strong>Assigned to agent {agent}</strong>. It does not hold the item right now.</p> : null}
        {it.usage && (
          <p className="small">Cost so far: <strong>{money(it.usage.cents)}</strong>, {it.usage.tokens.toLocaleString("en-US")} tokens, {Math.round(it.usage.seconds / 60)} minutes of work.
            {it.usage.exceeded && <span className="wk-flag"> {it.usage.message || "Over its limit"}</span>}</p>
        )}
        {claim?.note && <p className="small faint">Note from the agent: {claim.note}</p>}
        <ErrorPanel error={error} />
      </div>
      {can && <Button kind="pri" onClick={take}>Take over</Button>}
    </section>
  );
}

function Description({ it, editable, onSaved }: { it: ItemDetail; editable: boolean; onSaved: () => void }) {
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(it.title);
  const [body, setBody] = useState(it.body ?? "");
  const [error, setError] = useState<unknown>(null);
  const tid = useId();
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    try { await workApi.update(it.ref, { title: title.trim(), body, expected_version: it.version }); setEditing(false); setError(null); announce("Saved."); onSaved(); } catch (x) { setError(x); }
  };
  return (
    <Sheet title="Description" actions={editable && !editing ? <Button sm onClick={() => setEditing(true)}>Edit</Button> : undefined} pad>
      {editing ? (
        <form onSubmit={save} className="wk-form">
          <div className="wk-f"><label htmlFor={tid}>Title</label><input id={tid} value={title} onChange={(e) => setTitle(e.target.value)} maxLength={300} required /></div>
          <BodyField value={body} onChange={setBody} />
          <ErrorPanel error={error} />
          <div className="acts"><Button type="submit" kind="pri" disabled={!title.trim()}>Save</Button><Button onClick={() => { setEditing(false); setTitle(it.title); setBody(it.body ?? ""); }}>Cancel</Button></div>
        </form>
      ) : it.body?.trim() ? <Markdown text={it.body} /> : <p className="faint">No description yet.{editable && " Say what is wanted and how anyone can tell it is done."}</p>}
    </Sheet>
  );
}

function Criteria({ it, editable, onSaved }: { it: ItemDetail; editable: boolean; onSaved: () => void }) {
  const list = it.criteria ?? [];
  const [text, setText] = useState("");
  const [error, setError] = useState<unknown>(null);
  const id = useId();
  const put = async (next: { id: string; text: string; done: boolean }[], say: string) => {
    try { await workApi.update(it.ref, { criteria: next, expected_version: it.version }); setError(null); announce(say); onSaved(); } catch (x) { setError(x); }
  };
  const done = list.filter((c) => c.done).length;
  return (
    <Sheet title="Acceptance criteria" actions={list.length > 0 && <span className="faint num">{done} of {list.length} met</span>} pad>
      {list.length === 0 && !editable && <p className="faint">No acceptance criteria.</p>}
      {list.length > 0 && (
        <ul className="wk-checks">
          {list.map((c) => (
            <li key={c.id}>
              <label className="wk-check"><input type="checkbox" checked={c.done} disabled={!editable}
                onChange={() => put(list.map((x) => (x.id === c.id ? { ...x, done: !x.done } : x)), `${c.text}: ${c.done ? "not met" : "met"}.`)} />
                <span className={c.done ? "done" : ""}>{c.text}</span></label>
            </li>
          ))}
        </ul>
      )}
      {editable && (
        <form className="wk-inline" onSubmit={(e) => { e.preventDefault(); if (text.trim()) { void put([...list, { id: "", text: text.trim(), done: false }], "Criterion added."); setText(""); } }}>
          <div className="wk-f grow"><label htmlFor={id}>Add a criterion</label><input id={id} value={text} onChange={(e) => setText(e.target.value)} maxLength={300} /></div>
          <Button type="submit" sm disabled={!text.trim()}>Add</Button>
        </form>
      )}
      <ErrorPanel error={error} />
    </Sheet>
  );
}

function Children({ it }: { it: ItemDetail }) {
  const kids = useLoad<Page>("kids|" + it.ref, () => workApi.list(null, { parent: it.ref, q: "", limit: 50 }));
  const p = it.children;
  return (
    <Sheet title="Child items" pad>
      {p ? (
        <>
          <Progress done={p.done} total={p.total} label={`Children of ${it.ref} done`} />
          <p className="faint small num">{p.points_done} of {p.points} points done, {p.in_progress} in progress{p.blocked ? `, ${p.blocked} blocked` : ""}.</p>
        </>
      ) : <p className="faint">No child items yet. Give another item this one as its parent to roll its progress up here.</p>}
      <ErrorPanel error={kids.error} />
      {kids.data && kids.data.items.length > 0 && (
        <ul className="wk-rows">{kids.data.items.map((k) => (
          <li key={k.id}><span className="mono">{k.ref}</span> <Link href={itemHref(k.ref)}>{k.title}</Link> <StateTag item={k} /></li>
        ))}</ul>
      )}
    </Sheet>
  );
}

function Delivery({ it }: { it: ItemDetail }) {
  const d = it.delivery;
  return (
    <Sheet title="Delivery status" pad>
      {!d ? <p className="faint">Nothing merged yet, so there is nothing to deliver. When a pull request that closes this item merges, where it is running shows here.</p> : (
        <>
          <p className="wk-sentence">{d.sentence}</p>
          {d.environments.length > 0 && (
            <ul className="wk-env">
              {d.environments.map((e) => (
                <li key={e.environment} className={e.running ? "on" : ""}>
                  <strong>{e.environment}</strong>
                  <span>{e.running ? `Running${e.version ? " " + e.version : ""} since ${when(e.since)}` : "Not here yet"}</span>
                  {e.run && <Link href={`/runs/${encodeURIComponent(e.run)}`}>{terms().say("{Run}")}</Link>}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Sheet>
  );
}

function Code({ it }: { it: ItemDetail }) {
  return (
    <Sheet title="Code" pad={it.code.length === 0}>
      {it.code.length === 0 ? <p className="faint">No branches, pull requests or commits are linked. Name {it.ref} in a branch, a commit message or a pull request and it links here.</p> : (
        <div className="tablewrap"><table className="table wk-table">
          <caption className="visually-hidden">Code linked to {it.ref}</caption>
          <thead><tr><th scope="col">Kind</th><th scope="col">Where</th><th scope="col">Title</th><th scope="col">State</th></tr></thead>
          <tbody>{it.code.map((c) => (
            <tr key={c.id}>
              <td>{c.kind === "pull" ? "Pull request" : c.kind === "run" ? terms().say("{Run}") : c.kind[0]!.toUpperCase() + c.kind.slice(1)}</td>
              <td className="mono">{c.kind === "pull" && c.repo ? <Link href={`/repositories/${c.repo}/pulls/${c.ref}`}>{c.repo}#{c.ref}</Link> : <>{c.repo ? c.repo + " " : ""}{c.kind === "commit" ? c.ref.slice(0, 8) : c.ref}</>}</td>
              <td>{c.title}{c.closes && <span className="faint"> (closes this item)</span>}</td>
              <td>{c.state ? <span className={`wk-code-state s-${c.state}`}>{c.state}</span> : ""}</td>
            </tr>
          ))}</tbody>
        </table></div>
      )}
    </Sheet>
  );
}

function Details({ it, editable, onSaved }: { it: ItemDetail; editable: boolean; onSaved: () => void }) {
  const [f, setF] = useState({
    state: it.state, priority: it.priority, type: it.type, assignees: it.assignees.map((a) => a.name).join(", "), labels: it.labels.join(", "),
    estimate: it.estimate ? String(it.estimate) : "", parent: it.parent_ref ?? "", milestone: it.milestone ?? "", sprint: it.sprint ?? "", due: it.due_at?.slice(0, 10) ?? "",
  });
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const ids = { state: useId(), prio: useId(), type: useId(), who: useId(), labels: useId(), est: useId(), parent: useId(), ms: useId(), sprint: useId(), due: useId() };
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setF({ ...f, [k]: e.target.value });
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await workApi.update(it.ref, {
        expected_version: it.version, state: f.state, priority: f.priority, type: f.type, assignees: csv(f.assignees), labels: csv(f.labels),
        estimate: f.estimate ? Number(f.estimate) : 0, parent: f.parent.trim(), milestone: f.milestone.trim(), sprint: f.sprint.trim(), due_at: f.due,
      });
      setError(null); announce("Saved."); onSaved();
    } catch (x) { setError(x); } finally { setBusy(false); }
  };
  const states = STATES.some(([k]) => k === it.state) ? STATES : [...STATES, [it.state, it.state_name ?? it.state]];
  if (!editable) {
    return (
      <Sheet title="Details" pad>
        <dl className="wk-dl">
          <dt>State</dt><dd><StateTag item={it} /></dd>
          <dt>Priority</dt><dd><Prio p={it.priority} /></dd>
          <dt>Assigned</dt><dd>{it.assignees.length ? it.assignees.map((a) => <Who key={a.name} name={a.name} kind={a.kind} />) : <span className="faint">Unassigned</span>}</dd>
          <dt>Labels</dt><dd>{it.labels.length ? <Labels list={it.labels} /> : <span className="faint">None</span>}</dd>
          <dt>Estimate</dt><dd>{it.estimate || <span className="faint">None</span>}</dd>
          <dt>Milestone</dt><dd>{it.milestone || <span className="faint">None</span>}</dd>
          <dt>Sprint</dt><dd>{it.sprint || <span className="faint">None</span>}</dd>
          <dt>Reporter</dt><dd>{it.reporter}</dd>
        </dl>
      </Sheet>
    );
  }
  return (
    <Sheet title="Details" pad>
      <form onSubmit={save} className="wk-form">
        <div className="wk-f"><label htmlFor={ids.state}>State</label><select id={ids.state} value={f.state} onChange={set("state")}>{states.map(([k, n]) => <option key={k} value={k}>{n}</option>)}</select></div>
        <div className="wk-f"><label htmlFor={ids.prio}>Priority</label><select id={ids.prio} value={f.priority} onChange={set("priority")}>{PRIORITIES.map((p) => <option key={p}>{p}</option>)}</select></div>
        <div className="wk-f"><label htmlFor={ids.type}>Type</label><select id={ids.type} value={f.type} onChange={set("type")}>{TYPES.map((p) => <option key={p}>{p}</option>)}</select></div>
        <div className="wk-f"><label htmlFor={ids.who}>Assignees</label><input id={ids.who} value={f.assignees} onChange={set("assignees")} placeholder="names, separated by commas" /></div>
        <div className="wk-f"><label htmlFor={ids.labels}>Labels</label><input id={ids.labels} value={f.labels} onChange={set("labels")} /></div>
        <div className="wk-f"><label htmlFor={ids.est}>Estimate</label><input id={ids.est} type="number" min="0" step="0.5" value={f.estimate} onChange={set("estimate")} /></div>
        <div className="wk-f"><label htmlFor={ids.parent}>Parent</label><input id={ids.parent} value={f.parent} onChange={set("parent")} placeholder="PAY-3" /></div>
        <div className="wk-f"><label htmlFor={ids.ms}>Milestone</label><input id={ids.ms} value={f.milestone} onChange={set("milestone")} /></div>
        <div className="wk-f"><label htmlFor={ids.sprint}>Sprint</label><input id={ids.sprint} value={f.sprint} onChange={set("sprint")} /></div>
        <div className="wk-f"><label htmlFor={ids.due}>Due</label><input id={ids.due} type="date" value={f.due} onChange={set("due")} /></div>
        <ErrorPanel error={error} />
        <Button type="submit" kind="pri" disabled={busy}>Save changes</Button>
        <p className="faint small">Reported by {it.reporter}, {when(it.created_at)}. Changed {when(it.updated_at)}.</p>
      </form>
    </Sheet>
  );
}

const KINDS: [string, string][] = [["blocks", "blocks"], ["blocked_by", "is blocked by"], ["relates", "relates to"], ["duplicates", "duplicates"]];

function linkWords(l: ItemLink, ref: string): { word: string; other: string; title?: string; state?: string } {
  const out = l.from === ref;
  const other = out ? l.to : l.from;
  const title = out ? l.to_title : l.from_title;
  const state = out ? l.to_state : l.from_state;
  if (l.kind === "blocks") return { word: out ? "blocks" : "is blocked by", other, title, state };
  if (l.kind === "duplicates") return { word: out ? "duplicates" : "is duplicated by", other, title, state };
  return { word: "relates to", other, title, state };
}

function Links({ it, editable, onChanged }: { it: ItemDetail; editable: boolean; onChanged: () => void }) {
  const [kind, setKind] = useState("blocks");
  const [to, setTo] = useState("");
  const [error, setError] = useState<unknown>(null);
  const kid = useId(); const tid = useId();
  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      if (kind === "blocked_by") await workApi.addLink(to.trim(), "blocks", it.ref);
      else await workApi.addLink(it.ref, kind, to.trim());
      setTo(""); setError(null); announce("Link added."); onChanged();
    } catch (x) { setError(x); }
  };
  const remove = async (l: ItemLink) => {
    try { await workApi.removeLink(l.id); announce("Link removed."); onChanged(); } catch (x) { setError(x); }
  };
  return (
    <Sheet title="Links" pad>
      {it.links.length === 0 ? <p className="faint">Not linked to any other item.</p> : (
        <ul className="wk-rows">
          {it.links.map((l) => {
            const w = linkWords(l, it.ref);
            return (
              <li key={l.id}>
                <span className="faint">{w.word}</span> <Link href={itemHref(w.other)}>{w.other}</Link>{w.title && <> {w.title}</>}
                {editable && <Button sm kind="ghost" aria-label={`Remove link: ${w.word} ${w.other}`} onClick={() => remove(l)}>Remove</Button>}
              </li>
            );
          })}
        </ul>
      )}
      {editable && (
        <form className="wk-inline" onSubmit={add}>
          <div className="wk-f"><label htmlFor={kid}>Link type</label><select id={kid} value={kind} onChange={(e) => setKind(e.target.value)}>{KINDS.map(([k, n]) => <option key={k} value={k}>{"This item " + n}</option>)}</select></div>
          <div className="wk-f grow"><label htmlFor={tid}>Other item</label><input id={tid} value={to} onChange={(e) => setTo(e.target.value)} placeholder="PAY-12" required /></div>
          <Button type="submit" sm disabled={!to.trim()}>Add link</Button>
        </form>
      )}
      <ErrorPanel error={error} />
    </Sheet>
  );
}

function Attachments({ it }: { it: ItemDetail }) {
  return (
    <Sheet title="Attachments" pad>
      {it.attachments.length === 0 ? <p className="faint">No files attached.</p> : (
        <ul className="wk-rows">{it.attachments.map((a) => (
          <li key={a.id}><a href={workApi.attachmentUrl(a.id)} download={a.name}>{a.name}</a> <span className="faint small">{Math.max(1, Math.round(a.size / 1024))} KB, {a.by}, {when(a.at)}</span></li>
        ))}</ul>
      )}
    </Sheet>
  );
}

function Comments({ itemRef, canComment }: { itemRef: string; canComment: boolean }) {
  const list = useLoad<Comment[]>("comments|" + itemRef, () => workApi.comments(itemRef));
  const me = useMe();
  const [text, setText] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const post = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try { await workApi.comment(itemRef, text); setText(""); setError(null); announce("Comment added."); list.reload(); } catch (x) { setError(x); } finally { setBusy(false); }
  };
  return (
    <Sheet title="Comments" pad>
      <ErrorPanel error={list.error} onRetry={list.reload} />
      {list.loading && <Loading />}
      {list.data && list.data.length === 0 && <p className="faint">No comments yet.</p>}
      <ol className="wk-comments">
        {(list.data ?? []).map((c) => <CommentView key={c.id} c={c} mine={c.author === me?.user} onChanged={list.reload} />)}
      </ol>
      {canComment && (
        <form onSubmit={post} className="wk-form">
          <BodyField label="Add a comment" value={text} onChange={setText} />
          <ErrorPanel error={error} />
          <div><Button type="submit" kind="pri" disabled={busy || !text.trim()}>Comment</Button></div>
        </form>
      )}
    </Sheet>
  );
}

function CommentView({ c, mine, onChanged }: { c: Comment; mine: boolean; onChanged: () => void }) {
  const [editing, setEditing] = useState(false);
  const [body, setBody] = useState(c.body);
  const [edits, setEdits] = useState<Comment[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    try { await workApi.editComment(c.id, body); setEditing(false); setEdits(null); announce("Comment saved."); onChanged(); } catch (x) { setError(x); }
  };
  const history = async () => {
    if (edits) { setEdits(null); return; }
    try { setEdits(await workApi.commentEdits(c.id)); } catch (x) { setError(x); }
  };
  return (
    <li className="wk-comment">
      <div className="who"><Who name={c.author} kind={c.author_kind} /> <span className="faint small">{when(c.created_at)}</span>
        {c.edits ? <button type="button" className="wk-textbtn" aria-expanded={!!edits} onClick={history}>edited {c.edits} {c.edits === 1 ? "time" : "times"}</button> : null}
        {mine && !editing && <button type="button" className="wk-textbtn" onClick={() => setEditing(true)}>Edit</button>}
      </div>
      {editing ? (
        <form onSubmit={save} className="wk-form">
          <BodyField label="Edit comment" value={body} onChange={setBody} />
          <div className="acts"><Button type="submit" kind="pri" disabled={!body.trim()}>Save</Button><Button onClick={() => { setEditing(false); setBody(c.body); }}>Cancel</Button></div>
        </form>
      ) : <Markdown text={c.body} />}
      {edits && (
        <div className="wk-edits" role="region" aria-label="Earlier versions of this comment">
          {edits.length === 0 ? <p className="faint small">No earlier versions.</p> : edits.map((e, i) => (
            <blockquote key={i}><div className="faint small">Version {i + 1}, {when(e.created_at || e.edited_at)}</div><Markdown text={e.body} /></blockquote>
          ))}
        </div>
      )}
      <ErrorPanel error={error} />
    </li>
  );
}

function describe(e: WorkEvent): string {
  const d = e.data ?? {};
  const s = (k: string) => (d[k] === undefined || d[k] === null ? "" : String(d[k]));
  if (e.kind === "state") return `moved this from ${s("from").replace(/_/g, " ")} to ${s("to").replace(/_/g, " ")}`;
  if (e.kind === "created") return "created this item";
  if (e.kind === "comment") return "commented";
  if (e.kind === "agent.assigned") return `assigned agent ${s("agent")}`;
  const rest = Object.entries(d).filter(([k, v]) => k !== "ref" && (typeof v === "string" || typeof v === "number" || typeof v === "boolean")).map(([k, v]) => `${k.replace(/_/g, " ")}: ${v}`);
  return e.kind.replace(/[._]/g, " ") + (rest.length ? ` (${rest.join(", ")})` : "");
}

function History({ itemRef, version }: { itemRef: string; version: number }) {
  const ev = useLoad<WorkEvent[]>(`history|${itemRef}|${version}`, () => workApi.history(itemRef));
  return (
    <Sheet title="History" pad>
      <ErrorPanel error={ev.error} onRetry={ev.reload} />
      {ev.data && ev.data.length === 0 && <Blank title="No history yet">Changes to this item are recorded here, with who made them.</Blank>}
      <ol className="wk-timeline">
        {(ev.data ?? []).slice().reverse().map((e) => (
          <li key={e.seq} className={e.actor_kind === "agent" ? "by-agent" : ""}>
            <Who name={e.actor} kind={e.actor_kind} /> {describe(e)}
            <span className="faint small"> {when(e.at)}{e.via ? `, via ${e.via}` : ""}</span>
          </li>
        ))}
      </ol>
    </Sheet>
  );
}
