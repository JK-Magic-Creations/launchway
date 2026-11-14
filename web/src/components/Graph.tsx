import { useMemo } from "react";
import type { StepView } from "../api";
import { elapsed } from "../format";
import { StatusIcon, statusLabel, tone } from "./ui";

const PAD = 12;
const SIZES = {
  normal: { W: 196, H: 64, GX: 64, GY: 16 },
  compact: { W: 150, H: 58, GX: 36, GY: 12 },
};

interface Props {
  levels: string[][];
  steps: StepView[];
  selected: string | null;
  onSelect: (id: string) => void;
  now: number;
  compact?: boolean;      // smaller nodes, for narrow places
  describe?: (s: StepView) => string; // replaces the status line under each name
}

// Graph draws the pipeline as columns: each step sits to the right of
// everything it needs. Edges are drawn underneath in SVG.
export function Graph({ levels, steps, selected, onSelect, now, compact, describe }: Props) {
  const { W, H, GX, GY } = compact ? SIZES.compact : SIZES.normal;
  const byId = useMemo(() => new Map(steps.map((s) => [s.id, s])), [steps]);
  const pos = useMemo(() => {
    const p = new Map<string, { x: number; y: number }>();
    levels.forEach((col, i) => col.forEach((id, j) => p.set(id, { x: PAD + i * (W + GX), y: PAD + j * (H + GY) })));
    return p;
  }, [levels, W, H, GX, GY]);
  const width = PAD * 2 + levels.length * W + Math.max(0, levels.length - 1) * GX;
  const height = PAD * 2 + Math.max(1, ...levels.map((c) => c.length)) * (H + GY) - GY;

  const edges: { from: string; to: string; d: string; active: boolean }[] = [];
  for (const s of steps) {
    const b = pos.get(s.id);
    if (!b) continue;
    for (const n of s.needs ?? []) {
      const a = pos.get(n);
      if (!a) continue;
      const x1 = a.x + W, y1 = a.y + H / 2, x2 = b.x, y2 = b.y + H / 2;
      const mx = (x1 + x2) / 2;
      const dep = byId.get(n);
      edges.push({
        from: n, to: s.id,
        d: `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`,
        active: dep?.status === "succeeded" && (s.status === "running" || s.status === "leased" || s.status === "queued"),
      });
    }
  }

  return (
    <div className="graph-scroll">
      <div className="graph" style={{ width, height }}>
        <svg className="graph-edges" width={width} height={height} aria-hidden="true">
          {edges.map((e) => (
            <path key={e.from + ">" + e.to} d={e.d}
              className={"edge" + (e.active ? " edge-active" : "") +
                (selected && (selected === e.from || selected === e.to) ? " edge-hl" : "")} />
          ))}
        </svg>
        {steps.map((s) => {
          const p = pos.get(s.id);
          if (!p) return null;
          const t = elapsed(s.started_at, s.finished_at, now);
          return (
            <button key={s.id} type="button"
              className={`node t-${tone(s.status)}` + (selected === s.id ? " node-selected" : "")}
              style={{ left: p.x, top: p.y, width: W, height: H }}
              onClick={() => onSelect(s.id)}
              aria-pressed={selected === s.id}
              aria-label={`${s.id}: ${statusLabel(s.status)}`}>
              <span className="node-top">
                <StatusIcon status={s.status} />
                <span className="node-name">{s.id}</span>
                {s.attempt > 1 && <span className="node-attempt" title={`Attempt ${s.attempt}`}>#{s.attempt}</span>}
              </span>
              <span className="node-sub">
                {describe ? describe(s) : s.status === "skipped" ? (s.skip_kind === "condition" ? "condition not met" : "skipped")
                  : s.status === "pending" ? "waiting"
                  : s.status === "queued" ? "waiting for a runner"
                  : s.status === "waiting" ? "waiting for approval"
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
