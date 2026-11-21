import type { RunDetail, StepStatus, StepView } from "../api";
import { groupStatus } from "./Graph";
import { StatusIcon, statusLabel, tone } from "./ui";
import type { Loop, LoopMember, RunWithTriggers } from "../triggers";
import "../triggers.css";

const loopsOf = (run: RunDetail): Loop[] => (run as RunWithTriggers).graph.loops ?? [];

function inMember(m: LoopMember, id: string) { return m.id === id || id.startsWith(m.id + "/"); }

/** The outermost loop a step belongs to, if any. */
function outerLoop(loops: Loop[], id: string): Loop | undefined {
  let best: Loop | undefined;
  for (const l of loops) {
    if (l.members.some((m) => inMember(m, id)) && (!best || l.id.length < best.id.length)) best = l;
  }
  return best;
}

export function loopById(run: RunDetail, id: string | null): Loop | undefined {
  return id ? loopsOf(run).find((l) => l.id === id) : undefined;
}

/** The loop shown for a selection: the loop itself, or the loop of a member's step. */
export function loopFor(run: RunDetail, selected: string | null): Loop | undefined {
  if (!selected) return undefined;
  return loopById(run, selected) ?? outerLoop(loopsOf(run), selected);
}

function memberSteps(run: RunDetail, m: LoopMember): StepView[] {
  return run.step_list.filter((s) => inMember(m, s.id));
}

export function memberStatus(run: RunDetail, m: LoopMember): StepStatus {
  return groupStatus(memberSteps(run, m).map((s) => s.status));
}

// collapseLoops draws each loop as one node: its members' steps are
// replaced by a node named after the loop, whose status sums theirs up.
// Steps that needed a member need the loop. Levels are worked out again.
export function collapseLoops(run: RunDetail): { levels: string[][]; steps: StepView[]; nodeOf: (id: string | null) => string | null } {
  const loops = loopsOf(run);
  if (loops.length === 0) return { levels: run.levels, steps: run.step_list, nodeOf: (id) => id };
  const node = new Map<string, string>();
  const synthetic = new Map<string, StepView>();
  const steps: StepView[] = [];
  for (const s of run.step_list) {
    const l = outerLoop(loops, s.id);
    if (!l) { steps.push(s); node.set(s.id, s.id); continue; }
    node.set(s.id, l.id);
    let n = synthetic.get(l.id);
    if (!n) {
      const slash = l.id.lastIndexOf("/");
      n = { ...s, id: l.id, needs: [], group: slash > 0 ? l.id.slice(0, slash) : undefined, uses: undefined, reused_from: undefined,
        description: `${l.members.length} combinations of ${l.keys.join(", ")}`, kind: undefined, attempt: 0 } as StepView;
      synthetic.set(l.id, n);
      steps.push(n);
    }
  }
  for (const [id, n] of synthetic) {
    const members = run.step_list.filter((s) => node.get(s.id) === id);
    n.status = groupStatus(members.map((s) => s.status));
    n.started_at = Math.min(...members.map((s) => s.started_at || Infinity)) || 0;
    if (!Number.isFinite(n.started_at)) n.started_at = 0;
    n.finished_at = members.every((s) => s.finished_at) ? Math.max(...members.map((s) => s.finished_at)) : 0;
    const needs = new Set<string>();
    for (const s of members) for (const d of s.needs ?? []) if (node.get(d) !== id) needs.add(node.get(d) ?? d);
    n.needs = [...needs];
  }
  const out = steps.map((s) => (synthetic.has(s.id) ? s : { ...s, needs: [...new Set((s.needs ?? []).map((d) => node.get(d) ?? d))] }));
  // Columns: each node to the right of everything it needs.
  const depth = new Map<string, number>();
  const byId = new Map(out.map((s) => [s.id, s]));
  const d = (id: string, seen = new Set<string>()): number => {
    if (depth.has(id)) return depth.get(id)!;
    if (seen.has(id)) return 0;
    seen.add(id);
    const v = Math.max(-1, ...(byId.get(id)?.needs ?? []).filter((n) => byId.has(n)).map((n) => d(n, seen))) + 1;
    depth.set(id, v);
    return v;
  };
  const levels: string[][] = [];
  for (const s of out) (levels[d(s.id)] ??= []).push(s.id);
  return { levels: levels.filter(Boolean), steps: out, nodeOf: (id) => (id ? node.get(id) ?? id : id) };
}

// LoopPanel shows a loop's members as a grid of statuses: two lists as
// rows and columns, one as a row, more as a list. Choosing a member shows
// its step (for a loop over a template, its first step that did not
// succeed).
export function LoopPanel({ run, loop, selected, onSelect }: { run: RunDetail; loop: Loop; selected: string | null; onSelect: (id: string) => void }) {
  const counts = new Map<StepStatus, number>();
  for (const m of loop.members) { const st = memberStatus(run, m); counts.set(st, (counts.get(st) ?? 0) + 1); }
  const pick = (m: LoopMember) => {
    const steps = memberSteps(run, m);
    onSelect((steps.find((s) => s.status !== "succeeded" && s.status !== "skipped") ?? steps[0])?.id ?? m.id);
  };
  const cell = (m: LoopMember | undefined, label: string) => {
    if (!m) return <span className="loop-cell-none" title="left out by exclude">—</span>;
    const st = memberStatus(run, m);
    const on = !!selected && inMember(m, selected);
    return (
      <button type="button" className={`loop-cell t-${tone(st)}` + (on ? " loop-cell-on" : "")} aria-pressed={on}
        aria-label={`${m.id}: ${statusLabel(st)}`} title={m.id} onClick={() => pick(m)}>
        <StatusIcon status={st} size={14} />{label}
        {m.allowed_failure && <span className="badge-allowed" title="continue_for: this combination may fail without failing the run">allowed</span>}
      </button>
    );
  };
  // Combinations added outside the lists (add) do not fit the axes: they
  // are drawn as extra cells under the grid.
  const base = loop.members.filter((m) => !m.extra);
  const extra = loop.members.filter((m) => m.extra);
  const values = (k: string) => [...new Set(base.map((m) => m.each[k]))];
  let grid;
  if (loop.keys.length === 2) {
    const [rk, ck] = loop.keys;
    const find = (r: string, c: string) => base.find((m) => m.each[rk] === r && m.each[ck] === c);
    grid = (
      <table className="loop-grid">
        <thead><tr><th scope="col">{rk} \ {ck}</th>{values(ck).map((c) => <th key={c} scope="col">{c}</th>)}</tr></thead>
        <tbody>{values(rk).map((r) => (
          <tr key={r}><th scope="row">{r}</th>{values(ck).map((c) => <td key={c}>{cell(find(r, c), statusLabel(find(r, c) ? memberStatus(run, find(r, c)!) : "pending"))}</td>)}</tr>
        ))}</tbody>
      </table>
    );
  } else {
    grid = <div className="loop-list">{base.map((m) => <span key={m.id}>{cell(m, loop.keys.map((k) => m.each[k]).join(" · "))}</span>)}</div>;
  }
  const extras = extra.length > 0 && (
    <div className="loop-list loop-extra" aria-label="Extra combinations">
      <span className="loop-extra-label">extra</span>
      {extra.map((m) => <span key={m.id}>{cell(m, loop.keys.map((k) => m.each[k]).join(" · "))}</span>)}
    </div>
  );
  return (
    <section className="card loop" aria-label={`Loop ${loop.id}`}>
      <div className="loop-head">
        <span className="loop-name">{loop.id}</span>{" "}
        <span className="loop-counts">
          {loop.members.length} combinations of {loop.keys.join(", ")}{extra.length ? ` (${extra.length} added)` : ""}:{" "}
          {[...counts].map(([st, n]) => `${n} ${statusLabel(st).toLowerCase()}`).join(", ")}
          {loop.max_parallel ? ` · at most ${loop.max_parallel} at once` : ""}{loop.fail_fast ? " · stops the others when one fails" : ""}
        </span>
      </div>
      {grid}
      {extras}
    </section>
  );
}
