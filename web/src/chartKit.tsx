import { useEffect, useRef, useState } from "react";

// The parts the bar charts of the tests and security pages share: how the bars take the width there is (from 6 to 40
// pixels each; with many runs the chart scrolls), the width of the box, a rounded scale maximum and a bar segment.

export interface Layout { w: number; bar: number; step: number; pad: number }

export function layout(n: number, width: number, pad: number): Layout {
  const step = Math.max(Math.min((width - pad - 8) / Math.max(n, 1), 110), 10);
  const bar = Math.max(Math.min(step * 0.62, 40), 6);
  return { w: pad + n * step + 8, bar, step, pad };
}

export function barX(l: Layout, i: number) { return l.pad + i * l.step + (l.step - l.bar) / 2; }

export function useWidth(): [React.RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(800);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    setW(el.clientWidth);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

export function niceMax(v: number): number {
  if (v <= 4) return Math.max(Math.ceil(v / 2) * 2, 2);
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.2, 1.6, 2, 3, 4, 5, 6, 8, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

/** A bar segment with a rounded end on top. */
export function seg(x: number, y: number, h: number, bar: number, cls: string, top: boolean) {
  if (h <= 0) return null;
  const r = top ? Math.min(4, h, bar / 2) : 0;
  const d = `M${x},${y + h}V${y + r}${r ? `Q${x},${y} ${x + r},${y}` : ""}H${x + bar - r}${r ? `Q${x + bar},${y} ${x + bar},${y + r}` : ""}V${y + h}Z`;
  return <path d={d} className={cls} />;
}
