import type { ReactNode } from "react";

// Small SVG charts for work tracking. Each one is an image with a name and a description, drawn from design tokens (so both
// themes work), and each is meant to sit above a table of the same numbers: the table is the equivalent, not a footnote.

const W = 640, H = 260, L = 44, R = 12, T = 12, B = 44;
const iw = W - L - R, ih = H - T - B;

function nice(max: number): number {
  if (max <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(max)));
  const f = max / p;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p;
}
const short = (s: string, n = 12) => (s.length > n ? s.slice(0, n - 1) + "…" : s);
const fmt = (n: number) => (Number.isInteger(n) ? String(n) : n.toFixed(1));

function Axes({ max, ticks = 4, unit }: { max: number; ticks?: number; unit?: string }) {
  return (
    <g className="ax">
      {Array.from({ length: ticks + 1 }, (_, i) => {
        const v = (max / ticks) * i, y = T + ih - (v / max) * ih;
        return <g key={i}><line x1={L} x2={W - R} y1={y} y2={y} className={i === 0 ? "base" : "grid"} /><text x={L - 6} y={y + 4} textAnchor="end">{fmt(v)}</text></g>;
      })}
      {unit && <text x={W - R} y={10} textAnchor="end" className="unit">{unit}</text>}
    </g>
  );
}

function Frame({ name, desc, children }: { name: string; desc: string; children: ReactNode }) {
  return <svg className="wk-chart" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={name}><title>{name}</title><desc>{desc}</desc>{children}</svg>;
}

export function Legend({ items }: { items: { label: string; cls: string }[] }) {
  return <ul className="wk-legend" aria-label="Legend">{items.map((i) => <li key={i.label}><svg width="14" height="14" aria-hidden="true"><rect width="14" height="14" rx="3" className={i.cls} /></svg>{i.label}</li>)}</ul>;
}

/** Two bars for each sprint: what was committed, what was completed. */
export function PairBars({ rows, unit, name }: { rows: { label: string; a: number; b: number }[]; unit: string; name: string }) {
  const max = nice(Math.max(1, ...rows.flatMap((r) => [r.a, r.b])));
  const slot = iw / Math.max(rows.length, 1), bw = Math.min(26, slot / 2.6);
  return (
    <Frame name={name} desc={`${rows.length} sprints, committed and completed ${unit}.`}>
      <Axes max={max} unit={unit} />
      {rows.map((r, i) => {
        const x = L + slot * i + slot / 2;
        const ha = (r.a / max) * ih, hb = (r.b / max) * ih;
        return (
          <g key={r.label}>
            <rect x={x - bw - 2} y={T + ih - ha} width={bw} height={ha} className="s-a" rx="2" />
            <rect x={x + 2} y={T + ih - hb} width={bw} height={hb} className="s-b" rx="2" />
            <text x={x} y={H - 24} textAnchor="middle" className="cat">{short(r.label)}</text>
          </g>
        );
      })}
    </Frame>
  );
}

/** Lines over days: the work left and the ideal line. */
export function Lines({ days, series, unit, name }: { days: string[]; series: { label: string; cls: string; values: number[] }[]; unit: string; name: string }) {
  const max = nice(Math.max(1, ...series.flatMap((s) => s.values)));
  const n = Math.max(days.length - 1, 1);
  const px = (i: number) => L + (i / n) * iw, py = (v: number) => T + ih - (v / max) * ih;
  const every = Math.max(1, Math.ceil(days.length / 8));
  return (
    <Frame name={name} desc={`${days.length} days, ${series.map((s) => s.label).join(" and ")}, in ${unit}.`}>
      <Axes max={max} unit={unit} />
      {series.map((s) => <polyline key={s.label} fill="none" className={s.cls} points={s.values.map((v, i) => `${px(i)},${py(v)}`).join(" ")} />)}
      {days.map((d, i) => (i % every === 0 ? <text key={d} x={px(i)} y={H - 24} textAnchor="middle" className="cat">{d.slice(5)}</text> : null))}
    </Frame>
  );
}

/** Stacked areas over days: how many items were in each state. */
export function StackedAreas({ days, layers, name }: { days: string[]; layers: { label: string; cls: string; values: number[] }[]; name: string }) {
  const totals = days.map((_, i) => layers.reduce((a, l) => a + (l.values[i] ?? 0), 0));
  const max = nice(Math.max(1, ...totals));
  const n = Math.max(days.length - 1, 1);
  const px = (i: number) => L + (i / n) * iw, py = (v: number) => T + ih - (v / max) * ih;
  const below = days.map(() => 0);
  const shapes = layers.map((l) => {
    const top = days.map((_, i) => below[i]! + (l.values[i] ?? 0));
    const d = top.map((v, i) => `${i ? "L" : "M"}${px(i)},${py(v)}`).join("") + [...days.keys()].reverse().map((i) => `L${px(i)},${py(below[i]!)}`).join("") + "Z";
    top.forEach((v, i) => { below[i] = v; });
    return <path key={l.label} d={d} className={l.cls} />;
  });
  const every = Math.max(1, Math.ceil(days.length / 8));
  return (
    <Frame name={name} desc={`${days.length} days, items in each of ${layers.length} states, stacked.`}>
      <Axes max={max} unit="items" />
      {shapes}
      {days.map((d, i) => (i % every === 0 ? <text key={d} x={px(i)} y={H - 24} textAnchor="middle" className="cat">{d.slice(5)}</text> : null))}
    </Frame>
  );
}

/** Bars of counts, for the time items took. */
export function Bars({ rows, name, unit }: { rows: { label: string; value: number }[]; name: string; unit: string }) {
  const max = nice(Math.max(1, ...rows.map((r) => r.value)));
  const slot = iw / Math.max(rows.length, 1), bw = Math.min(40, slot * 0.7);
  return (
    <Frame name={name} desc={`${rows.length} groups of ${unit}.`}>
      <Axes max={max} unit={unit} />
      {rows.map((r, i) => {
        const x = L + slot * i + slot / 2, h = (r.value / max) * ih;
        return <g key={r.label}><rect x={x - bw / 2} y={T + ih - h} width={bw} height={h} className="s-a" rx="2" /><text x={x} y={H - 24} textAnchor="middle" className="cat">{short(r.label, 10)}</text></g>;
      })}
    </Frame>
  );
}

const DAY = 86_400_000;

/** Horizontal bars between a start and a due date, with how far each is. */
export function Gantt({ rows, name }: { rows: { label: string; start: number; end: number; percent: number }[]; name: string }) {
  const rowH = 30, height = Math.max(80, rows.length * rowH + 36);
  const min = Math.min(...rows.map((r) => r.start)), max = Math.max(...rows.map((r) => r.end), min + DAY);
  const left = 150, width = 640 - left - 12;
  const x = (t: number) => left + ((t - min) / (max - min)) * width;
  const dates = [min, min + (max - min) / 2, max];
  return (
    <svg className="wk-chart" viewBox={`0 0 640 ${height}`} role="img" aria-label={name}><title>{name}</title><desc>{`${rows.length} bars from start to due date.`}</desc>
      <g className="ax">{dates.map((t, i) => <g key={i}><line x1={x(t)} x2={x(t)} y1={20} y2={height - 6} className="grid" /><text x={x(t)} y={12} textAnchor={i === 0 ? "start" : i === 2 ? "end" : "middle"}>{new Date(t).toISOString().slice(0, 10)}</text></g>)}</g>
      {rows.map((r, i) => {
        const y = 28 + i * rowH, x1 = x(r.start), x2 = Math.max(x(r.end), x1 + 4);
        return (
          <g key={i}>
            <text x={left - 8} y={y + 14} textAnchor="end" className="cat">{short(r.label, 22)}</text>
            <rect x={x1} y={y} width={x2 - x1} height={18} rx="4" className="s-track" />
            <rect x={x1} y={y} width={((x2 - x1) * Math.min(100, r.percent)) / 100} height={18} rx="4" className="s-a" />
          </g>
        );
      })}
    </svg>
  );
}

export const LAYER_CLASSES = ["l0", "l1", "l2", "l3", "l4", "l5", "l6", "l7"];
