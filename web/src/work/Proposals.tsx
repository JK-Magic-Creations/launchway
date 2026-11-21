import { useId, useState } from "react";
import { Chip, type Tone } from "../kit/Status";
import { Dialog } from "../kit/Dialog";
import { Button } from "../kit/layout";
import { announce } from "../kit/announce";
import { Link } from "../components/ui";
import { Blank, ErrorPanel, itemHref, Loading, scopeLabel, useLoad, WorkPage } from "./common";
import { workApi, type ProposalGroup, type ProposeResult } from "./workApi";

// Proposals: findings that tools send in (the repository basics check, security findings, code quality). They arrive grouped, most severe
// first, and wait for a person. Accepting a group makes one item; dismissing it is remembered, so the same finding is not proposed again.

const SEV = ["None", "Low", "Medium", "High", "Critical"];
const SEV_TONE: (Tone | undefined)[] = [undefined, undefined, "warn", "bad", "bad"];
const STATES: [string, string][] = [["", "All"], ["pending", "Waiting for a decision"], ["accepted", "Accepted"], ["folded", "In the summary"], ["dismissed", "Dismissed"], ["fixed", "Fixed"]];
const key = (g: ProposalGroup) => g.source + "\u0000" + g.group;
const shown = (g: ProposalGroup) => g.title.replace("{n}", String(g.unresolved));

type Act = { kind: "accept" | "dismiss" | "merge" | "all"; groups: ProposalGroup[] } | null;

export default function Proposals({ scope }: { scope: string }) {
  const [state, setState] = useState("pending");
  const list = useLoad<ProposalGroup[]>(`proposals|${scope}|${state}`, () => workApi.proposals(scope, state));
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [act, setAct] = useState<Act>(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [check, setCheck] = useState<ProposeResult | null>(null);
  const groups = list.data ?? [];
  const pending = groups.filter((g) => g.state === "pending");
  const say = (t: string) => { setMessage(""); window.setTimeout(() => setMessage(t), 20); announce(t); };
  const done = (t: string) => { setAct(null); setPicked(new Set()); setError(null); say(t); list.reload(); };
  const chosen = groups.filter((g) => picked.has(key(g)) && g.state === "pending");
  const toggle = (g: ProposalGroup) => { const n = new Set(picked); if (n.has(key(g))) n.delete(key(g)); else n.add(key(g)); setPicked(n); };
  const isProject = scope.startsWith("project:");

  const health = async () => {
    try { const r = await workApi.health(scope.slice(8)); setCheck(r); setError(null); say(`Check finished: ${r.new} new, ${r.updated} already known, ${r.fixed} fixed.`); list.reload(); } catch (e) { setError(e); say(`The check did not work: ${e instanceof Error ? e.message : String(e)}`); }
  };

  return (
    <WorkPage scope={scope} on="proposals" title="Proposals" sentence={`Problems that tools found in ${scopeLabel(scope)}, waiting for you to decide what becomes work.`}
      actions={<>
        {isProject && <Button onClick={health}>Check this repository's basics</Button>}
        <Button kind="pri" disabled={pending.length === 0} onClick={() => setAct({ kind: "all", groups: pending })}>Accept all</Button>
      </>}>
      <div className="visually-hidden" role="status" aria-live="polite" aria-atomic="true" data-testid="proposals-live">{message}</div>
      {check && (
        <p className="callout callout-ok" role="status">
          Check finished. {check.received} {check.received === 1 ? "finding" : "findings"}: {check.new} new, {check.updated} already known, {check.fixed} fixed, {check.dismissed} dismissed before.
          {check.received === 0 && " The basics are in place."}
        </p>
      )}
      <section className="sheet wk-tools" aria-label="Show proposals">
        <div className="wk-filters">
          <label className="wk-f"><span>Show</span>
            <select value={state} onChange={(e) => { setState(e.target.value); setPicked(new Set()); }}>{STATES.map(([k, n]) => <option key={k} value={k}>{n}</option>)}</select>
          </label>
          {chosen.length > 0 && (
            <div className="acts" role="group" aria-label="For the selected proposals">
              <span className="faint num">{chosen.length} selected</span>
              <Button sm kind="pri" onClick={() => setAct({ kind: "accept", groups: chosen })}>Accept selected</Button>
              <Button sm onClick={() => setAct({ kind: "dismiss", groups: chosen })}>Dismiss selected</Button>
            </div>
          )}
        </div>
      </section>
      <ErrorPanel error={error ?? list.error} onRetry={list.reload} />
      {list.loading && <Loading what="proposals" />}
      {list.data && groups.length === 0 && (
        <Blank title={state === "pending" || state === "" ? "Nothing is waiting for you" : "Nothing in this state"}
          action={isProject ? <Button onClick={health}>Check this repository's basics</Button> : state ? <Button onClick={() => setState("")}>Show all</Button> : undefined}>
          A proposal is a problem a tool found, such as a missing README or a weak dependency, offered to you as work instead of filed on its own. Launchway points out issues from day one without flooding the board: each tool opens at most 10 new items in a run, and the rest wait together in one summary item.
        </Blank>
      )}
      <ul className="wk-props">
        {groups.map((g) => <Card key={key(g)} g={g} all={groups} checked={picked.has(key(g))} onCheck={() => toggle(g)} onAct={(kind) => setAct({ kind, groups: [g] })} />)}
      </ul>
      {act && <Decide scope={scope} act={act} all={groups} onClose={() => setAct(null)} onDone={done} onError={setError} />}
    </WorkPage>
  );
}

function Card({ g, checked, onCheck, onAct, all }: { g: ProposalGroup; all: ProposalGroup[]; checked: boolean; onCheck: () => void; onAct: (k: "accept" | "dismiss" | "merge") => void }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const pend = g.state === "pending";
  const others = all.filter((o) => o.source === g.source && o.group !== g.group && o.state === "pending");
  return (
    <li className="sheet wk-prop">
      <div className="head">
        {pend && <input type="checkbox" checked={checked} onChange={onCheck} aria-label={`Select ${shown(g)}`} />}
        <h2>{shown(g)}</h2>
        <Chip tone={SEV_TONE[g.severity]}>{SEV[g.severity] ?? "Severity " + g.severity}</Chip>
        <Chip>{g.state}</Chip>
      </div>
      <p className="faint small">proposed by {g.source} · {g.count} {g.count === 1 ? "occurrence" : "occurrences"}, {g.unresolved} open
        {g.item && <> · <Link href={itemHref(g.item)}>{g.item}</Link></>}{g.report_url && <> · <a href={g.report_url} rel="noopener noreferrer" target="_blank">Full report</a></>}</p>
      <div className="acts">
        <button type="button" className="wk-textbtn" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>{open ? "Hide" : "Show"} occurrences</button>
        {pend && <>
          <Button sm kind="pri" aria-label={`Accept ${shown(g)}`} onClick={() => onAct("accept")}>Accept</Button>
          <Button sm aria-label={`Dismiss ${shown(g)}`} onClick={() => onAct("dismiss")}>Dismiss</Button>
          <Button sm aria-label={`Merge ${shown(g)} into another`} disabled={others.length === 0} onClick={() => onAct("merge")}>Merge into…</Button>
        </>}
      </div>
      {open && (
        <ul id={id} className="wk-rows">
          {g.occurrences.map((o) => (
            <li key={o.dedupe_key}>
              <strong>{o.title}</strong> <span className="faint small">{SEV[o.severity]}</span>
              {o.body && <span className="block small">{o.body}</span>}
              {(o.evidence ?? []).map((u, i) => /^https?:\/\//.test(u) ? <a key={i} className="small" href={u} rel="noopener noreferrer" target="_blank">Evidence {i + 1}</a> : <code key={i} className="small">{u}</code>)}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function Decide({ scope, act, all, onClose, onDone, onError }: { scope: string; act: NonNullable<Act>; all: ProposalGroup[]; onClose: () => void; onDone: (t: string) => void; onError: (e: unknown) => void }) {
  const g = act.groups;
  const first = g[0]!;
  const [title, setTitle] = useState(shown(first));
  const [priority, setPriority] = useState("");
  const [reason, setReason] = useState("");
  const [into, setInto] = useState("");
  const [busy, setBusy] = useState(false);
  const ids = { t: useId(), p: useId(), r: useId(), i: useId() };
  const targets = all.filter((o) => o.source === first.source && o.state === "pending" && !g.some((x) => x.group === o.group));
  const bySource = (): [string, string[]][] => {
    const m = new Map<string, string[]>();
    for (const x of g) m.set(x.source, [...(m.get(x.source) ?? []), x.group]);
    return [...m];
  };
  const go = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true);
    try {
      if (act.kind === "dismiss") {
        let n = 0;
        for (const [src, gs] of bySource()) n += (await workApi.dismissProposals(scope, src, gs, reason)).dismissed;
        onDone(`Dismissed ${n} findings. They will not be proposed again.`);
      } else if (act.kind === "merge") {
        await workApi.mergeProposals(scope, first.source, [first.group], into);
        onDone(`Merged into ${targets.find((x) => x.group === into)?.title ?? into}.`);
      } else {
        let made = 0;
        for (const [src, gs] of bySource()) {
          const edits = g.length === 1 && act.kind === "accept" ? { [first.group]: { title: title !== shown(first) ? title : undefined, priority: priority || undefined } } : undefined;
          made += (await workApi.acceptProposals(scope, src, gs, edits)).opened;
        }
        onDone(`Made ${made} ${made === 1 ? "item" : "items"}.`);
      }
    } catch (x) { onError(x); setBusy(false); onClose(); }
  };
  const label = act.kind === "dismiss" ? "Dismiss proposals" : act.kind === "merge" ? "Merge a proposal" : act.kind === "all" ? "Accept all proposals" : "Accept proposals";
  return (
    <Dialog label={label} onClose={onClose} className="wk-dialog">
      <form onSubmit={go} className="wk-form">
        <h2>{label}</h2>
        {act.kind === "all" && <p>This makes <strong>{g.length}</strong> {g.length === 1 ? "item" : "items"}, one for each waiting group. Continue?</p>}
        {act.kind === "accept" && g.length > 1 && <p>This makes {g.length} items, one for each selected group.</p>}
        {act.kind === "accept" && g.length === 1 && (
          <>
            <p>This makes one item. You can change how it starts.</p>
            <div className="wk-f"><label htmlFor={ids.t}>Title</label><input id={ids.t} value={title} onChange={(e) => setTitle(e.target.value)} /></div>
            <div className="wk-f"><label htmlFor={ids.p}>Priority</label><select id={ids.p} value={priority} onChange={(e) => setPriority(e.target.value)}><option value="">As proposed</option><option>urgent</option><option>high</option><option>normal</option><option>low</option></select></div>
          </>
        )}
        {act.kind === "dismiss" && (
          <>
            <p>Dismissed findings are not proposed again, even when the tool reports them in a later run.</p>
            <div className="wk-f"><label htmlFor={ids.r}>Why</label><input id={ids.r} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Not a problem for us" /></div>
          </>
        )}
        {act.kind === "merge" && (
          <div className="wk-f"><label htmlFor={ids.i}>Merge "{shown(first)}" into</label>
            <select id={ids.i} value={into} onChange={(e) => setInto(e.target.value)}><option value="">Choose a group</option>{targets.map((x) => <option key={x.group} value={x.group}>{shown(x)}</option>)}</select></div>
        )}
        <div className="acts">
          <Button type="submit" kind="pri" disabled={busy || (act.kind === "merge" && !into) || (act.kind === "dismiss" && !reason.trim())}>
            {act.kind === "dismiss" ? "Dismiss" : act.kind === "merge" ? "Merge" : act.kind === "all" ? `Make ${g.length} ${g.length === 1 ? "item" : "items"}` : "Accept"}
          </Button>
          <Button onClick={onClose}>Cancel</Button>
        </div>
      </form>
    </Dialog>
  );
}
