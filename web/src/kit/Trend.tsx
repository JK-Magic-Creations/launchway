import type { Tick, Trend as TrendData } from "../guided/decide";
import { terms } from "../terms";

const WORD: Record<Tick, string> = { ok: "passed", bad: "failed", run: "running", wait: "waiting for a person", skip: "canceled" };

/** Trend is a strip of ticks, oldest first: one for each of the last runs, newest at the right. A tick is a
 * height and a colour, and the whole strip says in words how many passed and failed, so it is never colour alone. */
export function TrendStrip({ trend, label }: { trend: TrendData; label?: string }) {
  if (trend.ticks.length === 0) return <span className="faint small">{terms().say("no {runs} yet")}</span>;
  const counts = (["ok", "bad", "run", "wait", "skip"] as Tick[]).map((t) => [t, trend.ticks.filter((x) => x === t).length] as const).filter(([, n]) => n > 0);
  const summary = `${label ?? "Last runs"}: ${counts.map(([t, n]) => `${n} ${WORD[t]}`).join(", ")}`;
  return (
    <span className="trend" role="img" aria-label={summary} title={summary}>
      {trend.ticks.map((t, i) => <i key={i} className={t} />)}
    </span>
  );
}

/** The pass rate beside a trend, in words. */
export function RateText({ trend }: { trend: TrendData }) {
  if (trend.rate === null) return <span className="faint small">nothing finished yet</span>;
  return <span className="small"><b>{trend.rate}%</b> <span className="faint">of {trend.finished} finished passed</span></span>;
}
