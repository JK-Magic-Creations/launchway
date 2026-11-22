import { useRef, useState } from "react";

// A small line chart for the Insights pages. Every chart comes with the same numbers as a table
// (a native <details>, so it is reachable by keyboard and read by screen readers), is itself
// focusable with a one-sentence summary, and never relies on colour alone: each series has its own
// line style and marker shape, named in the legend. Colours are fixed (not theme variables) so
// "copy as image" can draw the chart exactly as shown.

export interface ChartSeries { name: string; values: (number | null)[]; dash?: string; marker: "circle" | "square" | "triangle"; color: string }

export interface ChartProps {
  id: string;
  title: string;
  xs: number[]; // Unix milliseconds
  series: ChartSeries[];
  format: (n: number) => string;
  /** The top of the axis when it is fixed (a rate is 0 to 1). */
  max?: number;
  empty: string;
}

const W = 640, H = 170, L = 52, R = 12, T = 10, B = 26;
const day = (ms: number) => new Date(ms).toISOString().slice(0, 10);

function Marker({ kind, x, y, color }: { kind: ChartSeries["marker"]; x: number; y: number; color: string }) {
  if (kind === "square") return <rect x={x - 3.5} y={y - 3.5} width={7} height={7} fill={color} />;
  if (kind === "triangle") return <polygon points={`${x},${y - 4.5} ${x - 4},${y + 3} ${x + 4},${y + 3}`} fill={color} />;
  return <circle cx={x} cy={y} r={3.5} fill={color} />;
}

export function InsightChart({ id, title, xs, series, format, max, empty }: ChartProps) {
  const svg = useRef<SVGSVGElement>(null);
  const [note, setNote] = useState("");
  const all = series.flatMap((s) => s.values).filter((v): v is number => v !== null);
  const has = all.some((v) => v > 0);
  const top = max ?? Math.max(1e-9, ...all) * 1.1;
  const px = (i: number) => L + (xs.length <= 1 ? (W - L - R) / 2 : (i * (W - L - R)) / (xs.length - 1));
  const py = (v: number) => T + (H - T - B) * (1 - Math.min(v, top) / top);
  const summary = has
    ? `${title}. ${series.map((s) => { const v = s.values.filter((x): x is number => x !== null); return `${s.name}: from ${format(Math.min(...v))} to ${format(Math.max(...v))}`; }).join("; ")}. The same numbers are in the table below the chart.`
    : `${title}. ${empty}`;

  async function copyImage() {
    const el = svg.current;
    if (!el) return;
    try {
      const xml = new XMLSerializer().serializeToString(el);
      const url = URL.createObjectURL(new Blob([xml], { type: "image/svg+xml" }));
      const img = new Image();
      await new Promise<void>((ok, bad) => { img.onload = () => ok(); img.onerror = () => bad(new Error("The chart could not be drawn")); img.src = url; });
      const canvas = document.createElement("canvas");
      canvas.width = W * 2; canvas.height = (H + 4) * 2;
      const c = canvas.getContext("2d");
      if (!c) throw new Error("No drawing surface");
      c.fillStyle = "#ffffff"; c.fillRect(0, 0, canvas.width, canvas.height);
      c.drawImage(img, 0, 0, canvas.width, canvas.height);
      URL.revokeObjectURL(url);
      const blob: Blob | null = await new Promise((ok) => canvas.toBlob(ok, "image/png"));
      if (!blob) throw new Error("No image was made");
      if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
        await navigator.clipboard.write([new ClipboardItem({ "image/png": blob })]);
        setNote("Copied the chart as an image.");
      } else {
        const a = document.createElement("a");
        a.href = URL.createObjectURL(blob); a.download = `${id}.png`; a.click();
        setNote("This browser cannot copy images, so the chart was saved as a file.");
      }
    } catch (e) {
      setNote(e instanceof Error ? e.message : "The chart could not be copied.");
    }
  }

  return (
    <figure className="ichart" id={id}>
      <figcaption><h3>{title}</h3></figcaption>
      <svg ref={svg} viewBox={`0 0 ${W} ${H}`} xmlns="http://www.w3.org/2000/svg" role="img" tabIndex={0} aria-label={summary} className="ichart-svg">
        <rect x={0} y={0} width={W} height={H} fill="#ffffff" fillOpacity={0} />
        {[0, 0.5, 1].map((f) => (
          <g key={f}>
            <line x1={L} x2={W - R} y1={py(top * f)} y2={py(top * f)} stroke="#8a8f98" strokeOpacity={0.45} strokeWidth={1} />
            <text x={L - 6} y={py(top * f) + 4} textAnchor="end" fontSize={11} fill="#4b5563">{format(top * f)}</text>
          </g>
        ))}
        {xs.length > 0 && <text x={L} y={H - 6} fontSize={11} fill="#4b5563">{day(xs[0])}</text>}
        {xs.length > 1 && <text x={W - R} y={H - 6} textAnchor="end" fontSize={11} fill="#4b5563">{day(xs[xs.length - 1])}</text>}
        {series.map((s) => {
          const pts = s.values.map((v, i) => (v === null ? null : [px(i), py(v)] as const));
          const path = pts.map((p, i) => (p ? `${i > 0 && pts[i - 1] ? "L" : "M"}${p[0].toFixed(1)} ${p[1].toFixed(1)}` : "")).join("");
          return (
            <g key={s.name}>
              <path d={path} fill="none" stroke={s.color} strokeWidth={2} strokeDasharray={s.dash} />
              {pts.map((p, i) => p && xs.length <= 60 ? <Marker key={i} kind={s.marker} x={p[0]} y={p[1]} color={s.color} /> : null)}
            </g>
          );
        })}
      </svg>
      <ul className="ichart-legend" aria-label="Legend">
        {series.map((s) => (
          <li key={s.name}>
            <svg width="34" height="12" aria-hidden="true"><line x1="0" x2="34" y1="6" y2="6" stroke={s.color} strokeWidth="2" strokeDasharray={s.dash} /><Marker kind={s.marker} x={17} y={6} color={s.color} /></svg>
            {s.name}
          </li>
        ))}
      </ul>
      <div className="ichart-tools">
        <button type="button" className="btn" onClick={copyImage}>Copy as image</button>
        <span role="status" className="small">{note}</span>
      </div>
      <details className="ichart-table">
        <summary>Show the numbers as a table</summary>
        <div className="rel-table-wrap">
          <table className="rel-table">
            <caption className="visually-hidden">{title}, as a table</caption>
            <thead><tr><th scope="col">From</th>{series.map((s) => <th key={s.name} scope="col" className="n">{s.name}</th>)}</tr></thead>
            <tbody>{xs.map((x, i) => <tr key={x}><th scope="row">{day(x)}</th>{series.map((s) => <td key={s.name} className="n">{s.values[i] === null || s.values[i] === undefined ? "none" : format(s.values[i] as number)}</td>)}</tr>)}</tbody>
          </table>
        </div>
      </details>
    </figure>
  );
}
