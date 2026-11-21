import type { GraphLayout } from "../graph/layout";

/** A small map of a graph wider than its box: every node as a mark in its state's color, and the part in view as a
 * frame. Pointing at it scrolls the graph there. It is a convenience for the pointer; the keyboard moves between
 * nodes and the graph scrolls to the one focused, so the map is hidden from assistive technology. */
export function GraphMinimap({ layout, tones, left, view, onJump }: {
  layout: GraphLayout; tones: Record<string, string>; left: number; view: number; onJump: (x: number) => void;
}) {
  const W = 168, scale = W / layout.width, H = Math.max(14, Math.round(layout.height * scale));
  const at = (e: React.PointerEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    onJump(((e.clientX - r.left) / r.width) * layout.width - view / 2);
  };
  return (
    <div className="pg-map" aria-hidden="true" style={{ width: W, height: H }}
      onPointerDown={(e) => { e.currentTarget.setPointerCapture?.(e.pointerId); at(e); }}
      onPointerMove={(e) => { if (e.buttons) at(e); }}>
      <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`}>
        {layout.nodes.map((n) => <rect key={n.id} className={`pg-map-n t-${tones[n.id] ?? "idle"}`} x={n.x * scale} y={n.y * scale} width={Math.max(3, n.w * scale)} height={Math.max(2, n.h * scale)} rx="1.5" />)}
        <rect className="pg-map-view" x={left * scale} y="0.75" width={Math.min(W - left * scale, view * scale)} height={H - 1.5} rx="2" />
      </svg>
    </div>
  );
}
