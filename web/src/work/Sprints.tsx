import { useId, useState } from "react";
import { Link } from "../components/ui";
import { Chip } from "../kit/Status";
import { Button, Sheet } from "../kit/layout";
import { announce } from "../kit/announce";
import { navigate } from "../router";
import { Blank, ErrorPanel, Loading, Progress, scopeHref, scopeLabel, useLoad, WorkPage } from "./common";
import { Legend, Lines } from "./chartParts";
import { workApi, type Burndown, type Sprint } from "./workApi";

// Sprints: the list with create, start and complete (with a choice of where unfinished work goes), and one sprint's page with what was
// committed against what was done, each person's capacity, the advice when the plan is over capacity, and the burndown as a chart and a table.

const day = (s: string) => s.slice(0, 10);
const tone = (s: string) => (s === "active" ? "run" : s === "completed" ? "ok" : undefined);

export default function Sprints({ scope }: { scope: string }) {
  const list = useLoad<Sprint[]>("sprints|" + scope, () => workApi.sprints(scope));
  const [adding, setAdding] = useState(false);
  const [completing, setCompleting] = useState<Sprint | null>(null);
  const [error, setError] = useState<unknown>(null);
  const sprints = list.data ?? [];
  const start = async (s: Sprint) => {
    try { await workApi.startSprint(s.id); setError(null); announce(`${s.name} started.`); list.reload(); } catch (e) { setError(e); }
  };
  return (
    <WorkPage scope={scope} on="sprints" title="Sprints" sentence={`Time-boxed plans for ${scopeLabel(scope)}: what the team commits to, and what it finishes.`}
      actions={<Button kind="pri" icon="plus" aria-expanded={adding} onClick={() => setAdding(!adding)}>New sprint</Button>}>
      {adding && <NewSprint scope={scope} onDone={() => { setAdding(false); list.reload(); }} onCancel={() => setAdding(false)} />}
      <ErrorPanel error={error ?? list.error} onRetry={list.reload} />
      {list.loading && <Loading what="sprints" />}
      {list.data && sprints.length === 0 && <Blank title="No sprints yet" action={<Button kind="pri" icon="plus" onClick={() => setAdding(true)}>New sprint</Button>}>A sprint is a short, fixed period with a goal. Create one, add items from the backlog, then start it.</Blank>}
      {completing && <Complete sprint={completing} others={sprints.filter((s) => s.id !== completing.id && s.state !== "completed")} onClose={() => setCompleting(null)} onDone={() => { setCompleting(null); list.reload(); }} />}
      {sprints.length > 0 && (
        <div className="sheet tablewrap">
          <table className="table wk-table">
            <caption className="visually-hidden">Sprints of {scopeLabel(scope)}</caption>
            <thead><tr><th scope="col">Sprint</th><th scope="col">State</th><th scope="col">Dates</th><th scope="col">Committed</th><th scope="col">Completed</th><th scope="col">Items</th><th scope="col"><span className="visually-hidden">Actions</span></th></tr></thead>
            <tbody>
              {sprints.map((s) => (
                <tr key={s.id}>
                  <th scope="row"><Link href={`/work/${encodeURIComponent(scope)}/sprints/${encodeURIComponent(s.id)}`}>{s.name}</Link>{s.goal && <div className="faint small">{s.goal}</div>}</th>
                  <td><Chip tone={tone(s.state)}>{s.state}</Chip></td>
                  <td className="num">{day(s.start_at)} to {day(s.end_at)}</td>
                  <td className="num">{s.committed} {s.unit}</td>
                  <td className="num">{s.completed} {s.unit}</td>
                  <td className="num">{s.items_done} of {s.items}</td>
                  <td className="acts">
                    {s.state === "planned" && <Button sm onClick={() => start(s)}>Start</Button>}
                    {s.state === "active" && <Button sm onClick={() => setCompleting(s)}>Complete</Button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </WorkPage>
  );
}

function NewSprint({ scope, onDone, onCancel }: { scope: string; onDone: () => void; onCancel: () => void }) {
  const [f, setF] = useState({ name: "", goal: "", start: "", end: "", capacity: "" });
  const [error, setError] = useState<unknown>(null);
  const ids = { name: useId(), goal: useId(), s: useId(), e: useId(), cap: useId() };
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => setF({ ...f, [k]: e.target.value });
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const capacity: Record<string, number> = {};
    for (const part of f.capacity.split(",")) {
      const [n, v] = part.split("=").map((x) => x.trim());
      if (n && v && !Number.isNaN(Number(v))) capacity[n] = Number(v);
    }
    try {
      await workApi.createSprint(scope, { name: f.name.trim(), goal: f.goal, start_at: f.start, end_at: f.end, capacity: Object.keys(capacity).length ? capacity : undefined });
      announce("Sprint created."); onDone();
    } catch (x) { setError(x); }
  };
  return (
    <form className="sheet pad wk-new" onSubmit={submit} aria-label="New sprint">
      <h2 className="h-sm">New sprint</h2>
      <div className="wk-grid">
        <div className="wk-f"><label htmlFor={ids.name}>Name</label><input id={ids.name} value={f.name} onChange={set("name")} required maxLength={80} /></div>
        <div className="wk-f span"><label htmlFor={ids.goal}>Goal</label><input id={ids.goal} value={f.goal} onChange={set("goal")} maxLength={300} /></div>
        <div className="wk-f"><label htmlFor={ids.s}>Starts</label><input id={ids.s} type="date" value={f.start} onChange={set("start")} required /></div>
        <div className="wk-f"><label htmlFor={ids.e}>Ends</label><input id={ids.e} type="date" value={f.end} onChange={set("end")} required /></div>
        <div className="wk-f span"><label htmlFor={ids.cap}>Capacity of each person</label><input id={ids.cap} value={f.capacity} onChange={set("capacity")} placeholder="ada=8, sam=5" /><span className="faint small">One name, an equals sign and a number, separated by commas. Leave empty to plan without capacity.</span></div>
      </div>
      <ErrorPanel error={error} />
      <div className="acts"><Button type="submit" kind="pri" disabled={!f.name.trim() || !f.start || !f.end}>Create sprint</Button><Button onClick={onCancel}>Cancel</Button></div>
    </form>
  );
}

function Complete({ sprint, others, onClose, onDone }: { sprint: Sprint; others: Sprint[]; onClose: () => void; onDone: () => void }) {
  const [carry, setCarry] = useState("");
  const [error, setError] = useState<unknown>(null);
  const id = useId();
  const go = async (e: React.FormEvent) => {
    e.preventDefault();
    try { await workApi.completeSprint(sprint.id, carry); announce(`${sprint.name} completed.`); onDone(); } catch (x) { setError(x); }
  };
  return (
    <form className="sheet pad wk-new" onSubmit={go} aria-label={`Complete ${sprint.name}`}>
      <h2 className="h-sm">Complete {sprint.name}</h2>
      <p>{sprint.items - sprint.items_done} of {sprint.items} items are not done. Choose where they go.</p>
      <div className="wk-f"><label htmlFor={id}>Carry unfinished items to</label>
        <select id={id} value={carry} onChange={(e) => setCarry(e.target.value)}>
          <option value="">Back to the backlog</option>
          {others.map((s) => <option key={s.id} value={s.name}>{s.name} ({s.state})</option>)}
        </select>
      </div>
      <ErrorPanel error={error} />
      <div className="acts"><Button type="submit" kind="pri">Complete sprint</Button><Button onClick={onClose}>Cancel</Button></div>
    </form>
  );
}

export function SprintPage({ scope, id }: { scope: string; id: string }) {
  const sp = useLoad<Sprint>("sprint|" + id, () => workApi.sprint(id));
  const bd = useLoad<Burndown>("burndown|" + id, () => workApi.burndown(id));
  const s = sp.data;
  if (sp.error) return <WorkPage scope={scope} on="sprints" title="Sprint"><ErrorPanel error={sp.error} onRetry={sp.reload} /></WorkPage>;
  if (!s) return <WorkPage scope={scope} on="sprints" title="Sprint"><Loading what="sprint" /></WorkPage>;
  const people = Object.entries(s.capacity ?? {});
  return (
    <WorkPage scope={scope} on="sprints" title={s.name} sentence={s.goal || `${day(s.start_at)} to ${day(s.end_at)}`} actions={<Button onClick={() => navigate(scopeHref(scope, "sprints"))}>All sprints</Button>}>
      <div className="wk-stats">
        <Stat label="Committed" value={`${s.committed} ${s.unit}`} />
        <Stat label="Completed" value={`${s.completed} ${s.unit}`} />
        <Stat label="Added after start" value={`${s.added} ${s.unit}`} />
        <Stat label="Carried over" value={String(s.carried_over)} />
        <Stat label="Capacity" value={s.total_capacity ? `${s.total_capacity} ${s.unit}` : "Not set"} />
      </div>
      <Progress done={s.items_done} total={s.items} label={`Items of ${s.name} done`} />
      {s.warning && <p className="callout callout-wait" role="note">{s.warning}</p>}
      <Sheet title="Commitment against what is done" pad>
        <div className="tablewrap"><table className="table wk-table">
          <caption className="visually-hidden">Commitment and completion of {s.name}</caption>
          <thead><tr><th scope="col">Measure</th><th scope="col">{s.unit[0]!.toUpperCase() + s.unit.slice(1)}</th></tr></thead>
          <tbody>
            <tr><th scope="row">Committed</th><td className="num">{s.committed}</td></tr>
            <tr><th scope="row">Completed</th><td className="num">{s.completed}</td></tr>
            <tr><th scope="row">Added after the sprint started</th><td className="num">{s.added}</td></tr>
            <tr><th scope="row">Items done</th><td className="num">{s.items_done} of {s.items}</td></tr>
          </tbody>
        </table></div>
      </Sheet>
      <Sheet title="Capacity" pad>
        {people.length === 0 ? <p className="faint">No capacity was set for this sprint, so there is nothing to compare the commitment with. Edit the sprint to say how much each person can take on.</p> : (
          <div className="tablewrap"><table className="table wk-table">
            <caption className="visually-hidden">Capacity of each person in {s.name}</caption>
            <thead><tr><th scope="col">Person</th><th scope="col">Capacity ({s.unit})</th></tr></thead>
            <tbody>{people.map(([n, v]) => <tr key={n}><th scope="row">{n}</th><td className="num">{v}</td></tr>)}
              <tr><th scope="row">Total</th><td className="num">{s.total_capacity}</td></tr></tbody>
          </table></div>
        )}
      </Sheet>
      <Sheet title="Burndown" pad>
        <ErrorPanel error={bd.error} onRetry={bd.reload} />
        {bd.loading && <Loading />}
        {bd.data && bd.data.points.length === 0 && <p className="faint">The burndown starts when the sprint does.</p>}
        {bd.data && bd.data.points.length > 0 && (
          <>
            <Lines name={`Burndown of ${s.name}`} unit={bd.data.unit} days={bd.data.points.map((p) => p.date)}
              series={[{ label: "Ideal", cls: "ln-ideal", values: bd.data.points.map((p) => p.ideal) }, { label: "Remaining", cls: "ln-main", values: bd.data.points.map((p) => p.remaining) }]} />
            <Legend items={[{ label: "Remaining", cls: "s-a" }, { label: "Ideal", cls: "s-ideal" }]} />
            <div className="tablewrap"><table className="table wk-table">
              <caption>Burndown of {s.name}, in {bd.data.unit}</caption>
              <thead><tr><th scope="col">Date</th><th scope="col">Remaining</th><th scope="col">Ideal</th><th scope="col">Done</th></tr></thead>
              <tbody>{bd.data.points.map((p) => <tr key={p.date}><th scope="row">{p.date}</th><td className="num">{p.remaining}</td><td className="num">{p.ideal}</td><td className="num">{p.done}</td></tr>)}</tbody>
            </table></div>
          </>
        )}
      </Sheet>
    </WorkPage>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return <div className="wk-stat"><div className="eyebrow">{label}</div><div className="big num">{value}</div></div>;
}
