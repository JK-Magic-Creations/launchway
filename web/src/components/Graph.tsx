import { useMemo, useState } from "react";
import type { GraphGroup, StepStatus, StepView } from "../api";
import { elapsed } from "../format";
import { ImageIcon } from "./Images";
import { shownStatus, StatusIcon, statusLabel, stepLabel, tone } from "./ui";

const PAD = 12;
const SIZES = {
  normal: { W: 196, H: 64, GX: 64, GY: 16 },
  compact: { W: 150, H: 58, GX: 36, GY: 12 },
};
// Room above a template's steps for the group's label, and around them for its frame.
const LABEL = 26;
const FRAME = 8;

interface Props {
  levels: string[][];
  steps: StepView[];
  /** Steps that used a template, once expanded: drawn as one frame each, which can be collapsed. */
  groups?: GraphGroup[];
  selected: string | null;
  onSelect: (id: string) => void;
  now: number;
  compact?: boolean;      // smaller nodes, for narrow places
  describe?: (s: StepView) => string; // replaces the status line under each name
  phases?: Record<string, string>; // the status line of a deploy or rollback step at work, by step
}

// The group a template's step is drawn in: the outermost, so a template that
// uses templates is one frame.
const topGroup = (g: string) => g.split("/")[0];

// groupStatus sums up the steps of a group in one status: what is happening
// now if anything is, else how it ended.
export function groupStatus(statuses: StepStatus[]): StepStatus {
  const any = (...s: StepStatus[]) => statuses.some((x) => s.includes(x));
  if (any("running", "leased")) return "running";
  if (any("waiting")) return "waiting";
  if (any("failed")) return "failed";
  if (any("queued")) return "queued";
  if (any("pending")) return statuses.some((x) => x === "succeeded") ? "running" : "pending";
  if (any("canceled")) return "canceled";
  if (statuses.length > 0 && statuses.every((x) => x === "skipped")) return "skipped";
  return "succeeded";
}

function lowerFirst(t: string): string { return t.slice(0, 1).toLowerCase() + t.slice(1); }

interface Node { key: string; step?: StepView; group?: GraphGroup; members?: StepView[] }

// Graph draws the pipeline as columns: each step sits to the right of
// everything it needs. Edges are drawn underneath in SVG. The steps a
// template became are framed together under the uses step's name; a frame
// can be collapsed into one node that shows how its steps are doing.
export function Graph({ levels, steps, groups, selected, onSelect, now, compact, describe, phases }: Props) {
  const { W, H, GX, GY } = compact ? SIZES.compact : SIZES.normal;
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const toggle = (g: string) => setCollapsed((prev) => {
    const next = new Set(prev);
    if (next.has(g)) next.delete(g); else next.add(g);
    return next;
  });
  const byGroup = useMemo(() => new Map((groups ?? []).map((g) => [g.id, g])), [groups]);
  const grouped = steps.some((s) => s.group);

  const layout = useMemo(() => {
    // The nodes drawn: steps, and one node for each collapsed group.
    const nodes: Node[] = [];
    const nodeOf = new Map<string, string>();
    const groupNodes = new Map<string, Node>();
    for (const s of steps) {
      const g = s.group ? topGroup(s.group) : "";
      if (g && collapsed.has(g)) {
        let n = groupNodes.get(g);
        if (!n) {
          n = { key: "group:" + g, group: byGroup.get(g) ?? { id: g, uses: "", library: "", template: "", steps: [] }, members: [] };
          groupNodes.set(g, n);
          nodes.push(n);
        }
        n.members!.push(s);
        nodeOf.set(s.id, n.key);
      } else {
        nodes.push({ key: s.id, step: s });
        nodeOf.set(s.id, s.id);
      }
    }
    // Edges between nodes; inside a collapsed group there are none.
    const deps = new Map<string, string[]>();
    for (const s of steps) {
      const to = nodeOf.get(s.id)!;
      for (const n of s.needs ?? []) {
        const from = nodeOf.get(n);
        if (!from || from === to) continue;
        const list = deps.get(to) ?? [];
        if (!list.includes(from)) list.push(from);
        deps.set(to, list);
      }
    }
    // Columns: the server's levels when nothing is grouped, else worked out
    // the same way for the nodes drawn.
    const level = new Map<string, number>();
    if (!grouped) {
      levels.forEach((col, i) => col.forEach((id) => level.set(id, i)));
    } else {
      const depth = (key: string, seen: Set<string>): number => {
        const known = level.get(key);
        if (known !== undefined) return known;
        if (seen.has(key)) return 0; // a loop; the server refuses those
        seen.add(key);
        let d = 0;
        for (const dep of deps.get(key) ?? []) d = Math.max(d, depth(dep, seen) + 1);
        level.set(key, d);
        return d;
      };
      nodes.forEach((n) => depth(n.key, new Set()));
    }
    // Bands: the steps of each expanded group get rows of their own, so that
    // their frame holds nothing else.
    const bandOf = (n: Node) => (n.step?.group ? topGroup(n.step.group) : "");
    const bands: string[] = [];
    for (const n of nodes) if (!bands.includes(bandOf(n))) bands.push(bandOf(n));
    const F = grouped ? FRAME : 0; // nothing is framed: draw as before
    const pos = new Map<string, { x: number; y: number }>();
    const frames: { group: string; x: number; y: number; w: number; h: number }[] = [];
    let y = PAD;
    for (const band of bands) {
      const top = band ? LABEL : 0;
      const rows = new Map<number, number>();
      let minL = Infinity, maxL = 0, height = 0;
      for (const n of nodes) {
        if (bandOf(n) !== band) continue;
        const l = level.get(n.key) ?? 0;
        const row = rows.get(l) ?? 0;
        rows.set(l, row + 1);
        pos.set(n.key, { x: PAD + F + l * (W + GX), y: y + top + row * (H + GY) });
        minL = Math.min(minL, l); maxL = Math.max(maxL, l);
        height = Math.max(height, (row + 1) * (H + GY) - GY);
      }
      if (band) {
        frames.push({ group: band, x: PAD + FRAME + minL * (W + GX) - FRAME, y: y, w: (maxL - minL) * (W + GX) + W + 2 * FRAME, h: top + height + FRAME });
        y += top + height + FRAME + GY + 6;
      } else {
        y += height + GY;
      }
    }
    // A node that needs every step of an expanded group needs the group:
    // one edge from its frame says so, where an edge from each step would
    // only add lines.
    const members = new Map<string, string[]>();
    for (const n of nodes) {
      const b = bandOf(n);
      if (b) members.set(b, [...(members.get(b) ?? []), n.key]);
    }
    const fromFrames = new Map<string, string[]>();
    for (const [to, froms] of deps) {
      const toNode = nodes.find((n) => n.key === to);
      for (const [g, keys] of members) {
        if (keys.length < 2 || (toNode && bandOf(toNode) === g) || !keys.every((k) => froms.includes(k))) continue;
        deps.set(to, (deps.get(to) ?? []).filter((k) => !keys.includes(k)));
        fromFrames.set(to, [...(fromFrames.get(to) ?? []), g]);
      }
    }
    const maxLevel = Math.max(0, ...[...level.values()]);
    return {
      nodes, pos, frames, deps, fromFrames, members,
      width: PAD * 2 + 2 * F + (maxLevel + 1) * W + maxLevel * GX,
      height: Math.max(y - GY + PAD, PAD * 2 + H),
    };
  }, [steps, levels, collapsed, byGroup, grouped, W, H, GX, GY]);

  const nodeStatus = (n: Node): StepStatus => n.step ? n.step.status : groupStatus(n.members!.map((m) => m.status));
  const statusByKey = new Map(layout.nodes.map((n) => [n.key, nodeStatus(n)]));
  const selectedKey = selected && layout.nodes.find((n) => n.key === selected || n.members?.some((m) => m.id === selected))?.key;

  const edges: { from: string; to: string; d: string; active: boolean; hl: boolean }[] = [];
  for (const [to, groupsFrom] of layout.fromFrames) {
    const b = layout.pos.get(to);
    if (!b) continue;
    for (const g of groupsFrom) {
      const f = layout.frames.find((x) => x.group === g);
      if (!f) continue;
      const x1 = f.x + f.w, y1 = f.y + f.h / 2, x2 = b.x, y2 = b.y + H / 2;
      const mx = (x1 + x2) / 2;
      const keys = layout.members.get(g) ?? [];
      const st = statusByKey.get(to);
      edges.push({
        from: "frame:" + g, to,
        d: `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`,
        active: keys.every((k) => statusByKey.get(k) === "succeeded" || statusByKey.get(k) === "skipped") && (st === "running" || st === "leased" || st === "queued"),
        hl: !!selectedKey && (selectedKey === to || keys.includes(selectedKey)),
      });
    }
  }
  for (const [to, froms] of layout.deps) {
    const b = layout.pos.get(to);
    if (!b) continue;
    for (const from of froms) {
      const a = layout.pos.get(from);
      if (!a) continue;
      const x1 = a.x + W, y1 = a.y + H / 2, x2 = b.x, y2 = b.y + H / 2;
      const mx = (x1 + x2) / 2;
      const st = statusByKey.get(to);
      edges.push({
        from, to,
        d: `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`,
        active: statusByKey.get(from) === "succeeded" && (st === "running" || st === "leased" || st === "queued"),
        hl: !!selectedKey && (selectedKey === from || selectedKey === to),
      });
    }
  }

  return (
    <div className="graph-scroll">
      <div className="graph" style={{ width: layout.width, height: layout.height }}>
        {layout.frames.map((f) => {
          const g = byGroup.get(f.group);
          return (
            <div key={f.group} className="group-frame" style={{ left: f.x, top: f.y, width: f.w, height: f.h }}>
              <button type="button" className="group-label" onClick={() => toggle(f.group)} aria-expanded="true"
                title={`Collapse ${f.group}${g?.description ? `: ${g.description}` : ""}`}>
                <span aria-hidden="true">▾</span> <strong>{f.group}</strong>
                {g && <span className="faint"> · {g.uses}</span>}
              </button>
            </div>
          );
        })}
        <svg className="graph-edges" width={layout.width} height={layout.height} aria-hidden="true">
          {edges.map((e) => (
            <path key={e.from + ">" + e.to} d={e.d}
              className={"edge" + (e.active ? " edge-active" : "") + (e.hl ? " edge-hl" : "")} />
          ))}
        </svg>
        {layout.nodes.map((n) => {
          const p = layout.pos.get(n.key);
          if (!p) return null;
          if (n.group) return <GroupNode key={n.key} node={n} pos={p} size={{ W, H }} status={nodeStatus(n)} now={now}
            selected={selectedKey === n.key} onToggle={() => toggle(n.group!.id)} />;
          const s = n.step!;
          const t = elapsed(s.started_at, s.finished_at, now);
          const label = s.group ? s.id.slice(topGroup(s.group).length + 1) : s.id;
          return (
            <button key={s.id} type="button"
              className={`node t-${tone(shownStatus(s))}` + (selected === s.id ? " node-selected" : "") + (s.uses ? " node-template" : "") + (s.reused_from ? " node-reused" : "")}
              style={{ left: p.x, top: p.y, width: W, height: H }}
              onClick={() => onSelect(s.id)}
              aria-pressed={selected === s.id}
              title={s.description || s.id}
              aria-label={`${s.id}${s.kind === "image" ? ", builds an image" : ""}: ${phases?.[s.id] ?? stepLabel(s)}${s.reused_from ? `, reused from run #${s.reused_from_number}` : ""}${s.description ? `. ${s.description}` : ""}`}>
              <span className="node-top">
                <StatusIcon status={shownStatus(s)} />
                <span className="node-name">{label}</span>
                {s.kind === "image" && <span className="node-kind" title="Builds a container image"><ImageIcon size={11} />image</span>}
                {s.kind === "verify" && <span className="node-kind" title="Watches an environment's metrics and logs">verify</span>}
                {s.kind === "ai" && <span className="node-kind" title="Asks a model">ai</span>}
                {s.attempt > 1 && <span className="node-attempt" title={`Attempt ${s.attempt}`}>#{s.attempt}</span>}
              </span>
              <span className="node-sub">
                {describe ? describe(s) : s.reused_from ? `reused from #${s.reused_from_number}`
                  : s.status === "skipped" ? lowerFirst(stepLabel(s))
                  : s.status === "pending" ? "waiting"
                  : s.status === "queued" ? "waiting for a runner"
                  : s.status === "waiting" ? phases?.[s.id] ?? lowerFirst(stepLabel(s))
                  : s.kind === "approval" && s.status === "succeeded" ? `approved by ${s.decided_by}`
                  : s.kind === "approval" && s.status === "failed" ? (s.decided_by ? `rejected by ${s.decided_by}` : "nobody decided")
                  : t || statusLabel(s.status)}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

// GroupNode is a collapsed group: its name, how its steps are doing, and how
// many there are. Choosing it opens the group again.
function GroupNode({ node, pos, size, status, now, selected, onToggle }: {
  node: Node; pos: { x: number; y: number }; size: { W: number; H: number }; status: StepStatus; now: number;
  selected: boolean; onToggle: () => void;
}) {
  const g = node.group!, members = node.members!;
  const done = members.filter((m) => m.status === "succeeded" || m.status === "skipped").length;
  const started = members.filter((m) => m.started_at > 0).map((m) => m.started_at);
  const finished = members.every((m) => m.finished_at > 0) ? Math.max(...members.map((m) => m.finished_at)) : 0;
  const t = started.length ? elapsed(Math.min(...started), finished, now) : "";
  const count = `${members.length} step${members.length === 1 ? "" : "s"}`;
  return (
    <button type="button" className={`node node-group t-${tone(status)}` + (selected ? " node-selected" : "")}
      style={{ left: pos.x, top: pos.y, width: size.W, height: size.H }}
      onClick={onToggle} aria-expanded="false"
      title={`${g.uses}${g.description ? `: ${g.description}` : ""}. Choose to show its steps.`}
      aria-label={`${g.id}, template ${g.uses}: ${statusLabel(status)}, ${done} of ${count} done. Show its steps.`}>
      <span className="node-top">
        <StatusIcon status={status} />
        <span className="node-name">{g.id}</span>
        <span className="node-count" aria-hidden="true">▸ {members.length}</span>
      </span>
      <span className="node-sub">
        {status === "succeeded" || status === "pending" || status === "skipped" ? count : `${done}/${count}`}{t ? ` · ${t}` : ""}
      </span>
    </button>
  );
}
