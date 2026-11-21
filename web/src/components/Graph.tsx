import "../styles/pipeline-graph.css";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { GraphGroup, StepView } from "../api";
import { elapsed } from "../format";
import { layoutGraph, type LayoutOptions } from "../graph/layout";
import { buildNodes, foldableChains, groupStatus, nodeHolding, nodeName, stageSentences, type RNode } from "../graph/model";
import { GraphMinimap } from "./GraphMinimap";
import { GraphNode, nodeLabel, stepSub } from "./GraphNode";
import { tone, statusLabel } from "./ui";

export { groupStatus };

const SIZES: Record<"normal" | "compact", Partial<LayoutOptions>> = {
  normal: { nodeW: 200, nodeH: 64 },
  compact: { nodeW: 168, nodeH: 58, gapX: 40 },
};
// Past this many steps, long runs of steps one after another fold into one node while nothing in them needs a look.
const FOLD_ABOVE = 14;

interface Props {
  steps: StepView[];
  /** Steps that used a template, once expanded: each template can be folded into one node. */
  groups?: GraphGroup[];
  selected: string | null;
  onSelect: (id: string) => void;
  now: number;
  compact?: boolean;
  /** Replaces the state line under each name (the definition view has no states). */
  describe?: (s: StepView) => string;
  /** The status line of a deploy or rollback step at work, by step. */
  phases?: Record<string, string>;
}

/** Graph draws a pipeline as stages left to right: a step sits right of everything it needs, steps that can run at
 * the same time are stacked in one column, and a connector runs from each step to the steps that need it. Its
 * structure is a list of stages, each a list of steps, so a screen reader gets "Stage 2 (runs together): ..." and the
 * same markup lays out as a vertical, indented list on a narrow screen. Arrow keys follow the connectors. */
export function Graph({ steps, groups, selected, onSelect, now, compact, describe, phases }: Props) {
  const [folded, setFolded] = useState<Set<string>>(() => new Set(steps.length > 24 ? (groups ?? []).map((g) => g.id) : []));
  const [opened, setOpened] = useState<Set<string>>(() => new Set());
  const flip = (set: Set<string>, id: string) => { const next = new Set(set); if (next.has(id)) next.delete(id); else next.add(id); return next; };

  const chains = useMemo(() => (steps.length > FOLD_ABOVE ? foldableChains(steps) : new Map<string, string[]>()), [steps]);
  const quiet = (members: string[]) => members.every((m) => {
    const s = steps.find((x) => x.id === m)!;
    return (s.status === "succeeded" || s.status === "pending" || s.status === "skipped") && m !== selected;
  });
  const foldedChains = new Set([...chains].filter(([first, m]) => !opened.has(first) && quiet(m)).map(([first]) => first));
  const foldKey = [...foldedChains].join("\n");
  const nodes = useMemo(() => buildNodes(steps, groups, { groups: folded, chains: new Set(foldKey ? foldKey.split("\n") : []) }, chains),
    [steps, groups, folded, chains, foldKey]);

  const size = SIZES[compact ? "compact" : "normal"];
  const layout = useMemo(() => layoutGraph(nodes.map((n) => ({ id: n.id, needs: n.needs })), size), [nodes, size]);
  const byId = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const selectedKey = nodeHolding(nodes, selected);
  const nodeStatus = (n: RNode) => groupStatus(n.steps.map((s) => s.status));
  const names = useCallback((id: string) => { const n = byId.get(id); return n ? nodeName(n) : id; }, [byId]);

  // Roving focus: one node is in the tab order (the chosen one, else the first); arrows move between nodes.
  const refs = useRef(new Map<string, HTMLButtonElement | null>());
  const scroller = useRef<HTMLDivElement>(null);
  const first = layout.columns[0]?.ids[0];
  const tabId = selectedKey && byId.has(selectedKey) ? selectedKey : first;
  const go = (id: string | undefined) => {
    const el = id ? refs.current.get(id) : null;
    if (!el) return;
    el.focus();
    el.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  };
  const onKey = (e: React.KeyboardEvent) => {
    const id = (e.target as HTMLElement).closest<HTMLElement>("[data-node]")?.dataset.node;
    if (!id) return;
    const col = layout.columns.find((c) => c.ids.includes(id))!;
    const i = col.ids.indexOf(id);
    const to = e.key === "ArrowRight" ? layout.after[id]?.[0] : e.key === "ArrowLeft" ? layout.before[id]?.[0]
      : e.key === "ArrowDown" ? col.ids[i + 1] : e.key === "ArrowUp" ? col.ids[i - 1]
      : e.key === "Home" ? layout.columns[0]?.ids[0] : e.key === "End" ? layout.columns[layout.columns.length - 1]?.ids[0] : undefined;
    if (e.key === "ArrowRight" || e.key === "ArrowLeft" || e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Home" || e.key === "End") {
      e.preventDefault();
      go(to);
    }
  };

  // The minimap shows when the graph is wider than its box.
  const [view, setView] = useState({ left: 0, width: 0 });
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const read = () => setView({ left: el.scrollLeft, width: el.clientWidth });
    read();
    el.addEventListener("scroll", read, { passive: true });
    const ro = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(read);
    ro?.observe(el);
    return () => { el.removeEventListener("scroll", read); ro?.disconnect(); };
  }, []);
  const wide = view.width > 0 && layout.width > view.width * 1.25;

  const parallelStages = layout.columns.filter((c) => c.ids.length > 1).length;
  const sentences = stageSentences(nodes);
  const running = nodes.filter((n) => n.steps.some((s) => s.status === "running" || s.status === "leased")).length;
  const summary = `Pipeline: ${steps.length} step${steps.length === 1 ? "" : "s"} in ${layout.columns.length} stage${layout.columns.length === 1 ? "" : "s"}`
    + (parallelStages ? `, ${parallelStages} with steps that run together` : "") + (running > 1 ? `, ${running} running now` : "");

  const hl = (from: string, to: string) => !!selectedKey && (selectedKey === from || selectedKey === to);
  const fromDone = (id: string) => { const n = byId.get(id); return !!n && nodeStatus(n) === "succeeded"; };
  const active = (id: string) => { const st = nodeStatus(byId.get(id)!); return st === "running"; };
  const groupList = (groups ?? []).filter((g) => g.steps.length > 1 && steps.some((s) => s.group && s.group.split("/")[0] === g.id));
  const chainList = [...chains];

  const tones: Record<string, string> = {};
  for (const n of nodes) tones[n.id] = tone(nodeStatus(n));

  return (
    <div className="pg">
      {(groupList.length > 0 || chainList.length > 0) && (
        <div className="pg-tools" role="group" aria-label="Fold or unfold parts of the pipeline">
          {groupList.map((g) => (
            <button key={g.id} type="button" className="pg-tool" aria-expanded={!folded.has(g.id)} onClick={() => setFolded(flip(folded, g.id))}
              title={g.description ?? `Steps from the template ${g.uses}`}>
              <span aria-hidden="true">{folded.has(g.id) ? "▸" : "▾"}</span> {g.id} <span className="faint">· template {g.uses} · {g.steps.length} steps</span>
            </button>
          ))}
          {chainList.map(([firstId, members]) => (
            <button key={firstId} type="button" className="pg-tool" aria-expanded={!foldedChains.has(firstId)}
              onClick={() => setOpened(flip(opened, firstId))}>
              <span aria-hidden="true">{foldedChains.has(firstId) ? "▸" : "▾"}</span> {members.length} steps in a row <span className="faint">· {firstId} to {members[members.length - 1]}</span>
            </button>
          ))}
        </div>
      )}
      <div className="pg-scroll" ref={scroller}>
        <div className="pg-canvas" style={{ "--w": `${layout.width}px`, "--h": `${layout.height}px` } as React.CSSProperties}>
          <svg className="pg-edges" width={layout.width} height={layout.height} aria-hidden="true">
            <defs>
              <marker id="pg-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="8" markerHeight="8" markerUnits="userSpaceOnUse" orient="auto"><path d="M0,0.5 L7,4 L0,7.5 z" fill="context-stroke" /></marker>
            </defs>
            {layout.edges.map((e) => {
              const cls = "pg-edge" + (e.span > 1 ? " pg-edge-skip" : "") + (hl(e.from, e.to) ? " pg-edge-hl" : "")
                + (fromDone(e.from) && active(e.to) ? " pg-edge-active" : "") + (nodeStatus(byId.get(e.to)!) === "skipped" ? " pg-edge-off" : "");
              return <path key={e.from + ">" + e.to} d={e.d} className={cls} markerEnd="url(#pg-arrow)" />;
            })}
          </svg>
          {layout.columns.map((c) => {
            if (c.ids.length < 2) return null;
            const ps = c.ids.map((id) => layout.nodes.find((n) => n.id === id)!);
            const top = Math.min(...ps.map((p) => p.y)), bottom = Math.max(...ps.map((p) => p.y + p.h));
            return <div key={"band" + c.col} className="pg-band" aria-hidden="true" style={{ left: c.x - 8, width: ps[0].w + 16, top: top - 8, height: bottom - top + 16 }} />;
          })}
          <ol className="pg-stages" aria-label={summary}>
            {layout.columns.map((c) => {
              const together = c.ids.length > 1;
              const doing = c.ids.filter((id) => byId.get(id)!.steps.some((s) => s.status === "running" || s.status === "leased")).length;
              const head = together ? `${c.ids.length} at once${doing ? ` · ${doing} running` : ""}` : "";
              return (
                <li key={c.col} className={"pg-stage" + (together ? " pg-together" : "")}>
                  <div className="pg-stage-h" style={{ "--x": `${c.x}px` } as React.CSSProperties}>
                    <span aria-hidden="true"><b>Stage {c.col + 1}</b>{head ? ` · ${head}` : ""}</span>
                    <span className="visually-hidden">{sentences[c.col]}</span>
                  </div>
                  <ol className="pg-nodes" aria-label={`Stage ${c.col + 1}`}>
                    {c.ids.map((id) => {
                      const n = byId.get(id)!;
                      const p = layout.nodes.find((x) => x.id === id)!;
                      const before = layout.before[id] ?? [];
                      const beside = c.ids.filter((x) => x !== id);
                      const sub = n.kind === "step" ? stepSub(n.steps[0], now, phases, describe) : foldedSub(n, now);
                      return <GraphNode key={id} node={n} x={p.x} y={p.y} w={p.w} h={p.h} selected={selectedKey === id} tabbable={tabId === id}
                        sub={sub} label={nodeLabel(n, sub, before, beside, names)} needsText={before.length ? `after ${before.map(names).join(", ")}` : ""}
                        onKey={onKey} setRef={(el) => { if (el) refs.current.set(id, el); else refs.current.delete(id); }}
                        onChoose={() => onSelect(n.steps[0].id)}
                        onFold={() => { if (n.kind === "group") setFolded(flip(folded, n.group!.id)); else setOpened(flip(opened, n.id.slice("chain:".length))); }} />;
                    })}
                  </ol>
                </li>
              );
            })}
          </ol>
        </div>
      </div>
      {wide && <GraphMinimap layout={layout} tones={tones} left={view.left} view={view.width} onJump={(x) => scroller.current?.scrollTo({ left: Math.max(0, x), behavior: "auto" })} />}
    </div>
  );
}

/** What a folded group or chain says: how many of its steps are done, and its time. */
function foldedSub(n: RNode, now: number): string {
  const st = groupStatus(n.steps.map((s) => s.status));
  const done = n.steps.filter((s) => s.status === "succeeded" || s.status === "skipped").length;
  const started = n.steps.filter((s) => s.started_at > 0).map((s) => s.started_at);
  const finished = n.steps.every((s) => s.finished_at > 0) ? Math.max(...n.steps.map((s) => s.finished_at)) : 0;
  const t = started.length ? elapsed(Math.min(...started), finished, now) : "";
  return `${statusLabel(st)} · ${done}/${n.steps.length}${t ? ` · ${t}` : ""}`;
}

