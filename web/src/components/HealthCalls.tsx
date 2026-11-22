import type { VerificationView } from "../api";
import { clock } from "../format";
import { terms } from "../terms";

// The calls a deploy runner made itself for a health check (a metric with a
// check, not a query): how many answered as expected, how fast, and, for the
// ones that did not, what the service answered or why there was no answer.
// Shown inside the metric's block; nothing renders for a metric read from a source.
export function HealthCalls({ v, name }: { v: VerificationView; name: string }) {
  const calls = v.intervals.flatMap((iv) => (iv.metrics ?? []).filter((m) => m.name === name).flatMap((m) => m.attempts ?? []));
  const notes = v.intervals.flatMap((iv) => (iv.metrics ?? []).filter((m) => m.name === name && m.note).map((m) => ({ i: iv.index, note: m.note! })));
  if (calls.length === 0) return null;
  const bad = calls.filter((c) => !c.ok);
  const lat = calls.map((c) => c.latency_ms).sort((a, b) => a - b);
  const median = lat[Math.floor(lat.length / 2)];
  const shown = bad.slice(0, 50);
  return (
    <details className="vreadings" open={bad.length > 0}>
      <summary>
        {calls.length} call{calls.length === 1 ? "" : "s"} {terms().say("made by the {deploy:do} {runner}:")} {calls.length - bad.length} answered as expected, {bad.length} did not (median {median} ms)
      </summary>
      {notes.length > 0 && (
        <ul className="vlog-errors">{notes.map((n) => <li key={n.i}>Interval {n.i + 1}: {n.note}</li>)}</ul>
      )}
      {shown.length > 0 && (
        <div className="table-scroll">
          <table className="inputs-table vreadings-table">
            <caption className="sr-only">Calls that did not answer as expected</caption>
            <thead><tr><th>Time</th><th>Status</th><th>Latency</th><th>What happened</th><th>What came back</th></tr></thead>
            <tbody>
              {shown.map((c) => (
                <tr key={c.t} className="vrow-bad">
                  <td data-label="Time">{clock(c.t)}</td>
                  <td data-label="Status" className="mono">{c.status ?? "no answer"}</td>
                  <td data-label="Latency" className="mono">{c.latency_ms} ms</td>
                  <td data-label="What happened">{c.error ?? "it did not answer as expected"}</td>
                  <td data-label="What came back" className="mono">{c.excerpt || "-"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {bad.length > shown.length && <p className="faint small">{bad.length - shown.length} more are in the verification's record.</p>}
        </div>
      )}
    </details>
  );
}
