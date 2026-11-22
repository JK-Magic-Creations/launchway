import { useId, useState } from "react";
import { Chip } from "../kit/Status";
import { Button, Sheet } from "../kit/layout";
import { announce } from "../kit/announce";
import { Link } from "../components/ui";
import { Blank, ErrorPanel, itemHref, Loading, Progress, scopeLabel, useLoad, WorkPage } from "./common";
import { Gantt } from "./chartParts";
import { workApi, type Milestone, type RoadmapRow } from "./workApi";
import { PSub, SpanFaint, SpanFaintSmall, TdNum, ThCol, ThRow } from "../kit/bits";

// The roadmap (epics, milestones and sprints as bars between their dates, with the same rows as a table) and the milestones with progress.

const KIND: Record<string, string> = { epic: "Epic", milestone: "Milestone", sprint: "Sprint" };

export function Roadmap({ scope }: { scope: string }) {
  const r = useLoad<RoadmapRow[]>("roadmap|" + scope, () => workApi.roadmap(scope));
  const rows = r.data ?? [];
  const dated = rows.filter((x) => x.start_at || x.due_at);
  const ms = (s?: string, fallback?: string) => new Date((s || fallback)!).getTime();
  return (
    <WorkPage scope={scope} on="roadmap" title="Roadmap" sentence={`What is planned for ${scopeLabel(scope)} and when: epics, milestones and sprints between their dates.`}>
      <ErrorPanel error={r.error} onRetry={r.reload} />
      {r.loading && <Loading what="roadmap" />}
      {r.data && rows.length === 0 && <Blank title="Nothing on the roadmap yet">Epics, milestones and sprints that have dates appear here. Give an epic a start and a due date, or create a milestone.</Blank>}
      {rows.length > 0 && (
        <Sheet title="Plan" pad>
          {dated.length > 0
            ? <Gantt name="Roadmap: bars from start to due date" rows={dated.map((x) => ({ label: x.title, start: ms(x.start_at, x.due_at), end: ms(x.due_at, x.start_at), percent: x.percent }))} />
            : <p className="faint">None of these has a date yet, so there is nothing to draw. The table lists them.</p>}
          <div className="tablewrap"><table className="table wk-table">
            <caption>Roadmap rows with dates and progress</caption>
            <thead><tr><ThCol>Kind</ThCol><ThCol>Title</ThCol><ThCol>Starts</ThCol><ThCol>Due</ThCol><ThCol>Progress</ThCol></tr></thead>
            <tbody>{rows.map((x, i) => (
              <tr key={i}>
                <td>{KIND[x.kind] ?? x.kind}</td>
                <ThRow>{x.ref ? <Link href={itemHref(x.ref)}>{x.title}</Link> : x.title}{x.state && <> <Chip>{x.state}</Chip></>}</ThRow>
                <TdNum>{x.start_at?.slice(0, 10) ?? <SpanFaint>None</SpanFaint>}</TdNum>
                <TdNum>{x.due_at?.slice(0, 10) ?? <SpanFaint>None</SpanFaint>}</TdNum>
                <TdNum>{x.percent}% ({x.done} of {x.total})</TdNum>
              </tr>
            ))}</tbody>
          </table></div>
        </Sheet>
      )}
    </WorkPage>
  );
}

export function Milestones({ scope }: { scope: string }) {
  const list = useLoad<Milestone[]>("milestones|" + scope, () => workApi.milestones(scope));
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const toggle = async (m: Milestone) => {
    try { await workApi.updateMilestone(m.id, { state: m.state === "open" ? "closed" : "open" }); announce(`${m.title} ${m.state === "open" ? "closed" : "reopened"}.`); list.reload(); } catch (e) { setError(e); }
  };
  const all = list.data ?? [];
  return (
    <WorkPage scope={scope} on="milestones" title="Milestones" sentence={`Goals with a date, and how much of each is done in ${scopeLabel(scope)}.`}
      actions={<Button kind="pri" icon="plus" aria-expanded={adding} onClick={() => setAdding(!adding)}>New milestone</Button>}>
      {adding && <NewMilestone scope={scope} onDone={() => { setAdding(false); list.reload(); }} onCancel={() => setAdding(false)} />}
      <ErrorPanel error={error ?? list.error} onRetry={list.reload} />
      {list.loading && <Loading what="milestones" />}
      {list.data && all.length === 0 && <Blank title="No milestones yet" action={<Button kind="pri" icon="plus" onClick={() => setAdding(true)}>New milestone</Button>}>A milestone groups the items that must land for a goal, such as a release. Items join one by naming it.</Blank>}
      <ul className="wk-miles">
        {all.map((m) => (
          <li key={m.id} className="sheet pad">
            <div className="head"><h2>{m.title}</h2><Chip tone={m.state === "open" ? "run" : "ok"}>{m.state}</Chip>{m.due_at && <SpanFaintSmall>due {m.due_at.slice(0, 10)}</SpanFaintSmall>}</div>
            {m.description && <PSub>{m.description}</PSub>}
            <Progress done={m.done} total={m.total} label={`Progress of ${m.title}`} />
            <p className="faint small num">{m.points_done} of {m.points} points done.</p>
            <Button sm onClick={() => toggle(m)}>{m.state === "open" ? "Close milestone" : "Reopen milestone"}</Button>
          </li>
        ))}
      </ul>
    </WorkPage>
  );
}

function NewMilestone({ scope, onDone, onCancel }: { scope: string; onDone: () => void; onCancel: () => void }) {
  const [title, setTitle] = useState("");
  const [due, setDue] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<unknown>(null);
  const a = useId(), b = useId(), c = useId();
  const go = async (e: React.FormEvent) => {
    e.preventDefault();
    try { await workApi.createMilestone(scope, { title: title.trim(), due_at: due || undefined, description: description || undefined }); announce("Milestone created."); onDone(); } catch (x) { setError(x); }
  };
  return (
    <form className="sheet pad wk-new" onSubmit={go} aria-label="New milestone">
      <h2 className="h-sm">New milestone</h2>
      <div className="wk-grid">
        <div className="wk-f"><label htmlFor={a}>Title</label><input id={a} value={title} onChange={(e) => setTitle(e.target.value)} required maxLength={120} /></div>
        <div className="wk-f"><label htmlFor={b}>Due</label><input id={b} type="date" value={due} onChange={(e) => setDue(e.target.value)} /></div>
        <div className="wk-f span"><label htmlFor={c}>Description</label><input id={c} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={500} /></div>
      </div>
      <ErrorPanel error={error} />
      <div className="acts"><Button type="submit" kind="pri" disabled={!title.trim()}>Create milestone</Button><Button onClick={onCancel}>Cancel</Button></div>
    </form>
  );
}
