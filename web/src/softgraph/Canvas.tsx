import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "../kit/layout";
import { layout, MAX_SHOWN, type Layout, type LEdge, type LNode } from "./layout";

/** The picture of the graph. It is an extra: everything it shows is also in the table beside it, which a keyboard
 * and a screen reader use. Layout is computed in a worker where there is one. */
export default function Canvas({ nodes, edges, focus, highlight, selected, onSelect }: {
  nodes: LNode[]; edges: LEdge[]; focus: string; highlight: Set<string>; selected?: string; onSelect: (id: string) => void;
}) {
  const [placed, setPlaced] = useState<Layout | null>(null);
  const [view, setView] = useState({ k: 1, x: 0, y: 0 });
  const drag = useRef<{ x: number; y: number } | null>(null);
  const seq = useRef(0);
  const worker = useRef<Worker | null>(null);

  useEffect(() => {
    const id = ++seq.current;
    const sync = () => setPlaced(layout(nodes, edges, focus));
    if (typeof Worker === "undefined") { sync(); return; }
    try {
      worker.current ??= new Worker(new URL("./layout.worker.ts", import.meta.url), { type: "module" });
      const w = worker.current;
      const on = (e: MessageEvent<{ id: number; layout: Layout }>) => { if (e.data.id === seq.current) setPlaced(e.data.layout); };
      w.addEventListener("message", on);
      w.postMessage({ id, nodes, edges, focus });
      return () => w.removeEventListener("message", on);
    } catch { sync(); }
  }, [nodes, edges, focus]);
  useEffect(() => () => { worker.current?.terminate(); worker.current = null; }, []);

  const at = useMemo(() => new Map((placed?.nodes ?? []).map((p) => [p.id, p])), [placed]);
  const byId = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  if (!placed) return <p className="faint" role="status">Drawing the picture…</p>;
  const zoom = (f: number) => setView((v) => ({ ...v, k: Math.min(4, Math.max(0.1, v.k * f)) }));
  const move = (dx: number, dy: number) => setView((v) => ({ ...v, x: v.x + dx, y: v.y + dy }));
  return (
    <div className="sg-canvas">
      <div className="sg-tools" role="group" aria-label="Move the picture">
        <Button sm onClick={() => zoom(1.25)}>Zoom in</Button>
        <Button sm onClick={() => zoom(0.8)}>Zoom out</Button>
        <Button sm onClick={() => move(80, 0)}>Move left</Button>
        <Button sm onClick={() => move(-80, 0)}>Move right</Button>
        <Button sm onClick={() => move(0, 80)}>Move up</Button>
        <Button sm onClick={() => move(0, -80)}>Move down</Button>
        <Button sm onClick={() => setView({ k: 1, x: 0, y: 0 })}>Reset the view</Button>
      </div>
      {placed.shown < placed.total && <p className="sub" role="status">The picture shows {MAX_SHOWN} of {placed.total} nodes, nearest the focus first. Filter by kind, or follow the table, to see the rest.</p>}
      <svg role="img" aria-label={`Picture of the graph: ${placed.shown} nodes. The table beside it lists the same things.`}
        viewBox={`0 0 ${Math.max(600, placed.width)} ${Math.max(200, placed.height)}`} className="sg-svg"
        onWheel={(e) => { if (e.ctrlKey || e.metaKey) { e.preventDefault(); zoom(e.deltaY < 0 ? 1.1 : 0.9); } }}
        onPointerDown={(e) => { drag.current = { x: e.clientX, y: e.clientY }; }}
        onPointerMove={(e) => { if (drag.current && e.buttons) { move(e.clientX - drag.current.x, e.clientY - drag.current.y); drag.current = { x: e.clientX, y: e.clientY }; } }}
        onPointerUp={() => { drag.current = null; }}>
        <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
          {edges.map((e, i) => {
            const a = at.get(e.from), b = at.get(e.to);
            if (!a || !b) return null;
            const on = highlight.has(e.from) && highlight.has(e.to);
            return <line key={i} x1={a.x + 150} y1={a.y} x2={b.x} y2={b.y} className={"sg-edge" + (on ? " on" : "")} />;
          })}
          {placed.nodes.map((p) => {
            const n = byId.get(p.id)!;
            const cls = ["sg-node", "k-" + n.kind, p.id === focus && "focus", highlight.has(p.id) && "on", p.id === selected && "sel"].filter(Boolean).join(" ");
            return (
              <g key={p.id} className={cls} transform={`translate(${p.x} ${p.y})`} onClick={() => onSelect(p.id)}>
                <rect x={0} y={-12} width={150} height={24} rx={4} />
                <text x={8} y={4}>{n.label.length > 22 ? n.label.slice(0, 21) + "…" : n.label}</text>
                <title>{`${n.kind}: ${n.label}`}</title>
              </g>
            );
          })}
        </g>
      </svg>
    </div>
  );
}
