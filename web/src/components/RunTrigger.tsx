import type { RunDetail } from "../api";
import { clock } from "../format";
import { Mono } from "./ui";
import type { RunWithTriggers } from "../triggers";
import "../triggers.css";
import { terms } from "../terms";

// RunTrigger shows what a run was started with: the schedule or webhook
// that started it, and its input values with what each input is.
export function RunTrigger({ run }: { run: RunDetail }) {
  const r = run as RunWithTriggers;
  const d = r.trigger_detail;
  if (!d) return null;
  const decl = r.graph.inputs ?? [];
  const names = decl.length ? decl.map((i) => i.name) : Object.keys(d.inputs ?? {}).sort();
  return (
    <section className="card trigger-card" aria-labelledby="trigger-title">
      <h2 id="trigger-title">Started with</h2>
      {d.schedule && (
        <div>Started by the schedule <Mono>{d.schedule}</Mono>{d.scheduled_for ? <>, for {clock(d.scheduled_for)}</> : null}.</div>
      )}
      {d.webhook && <div>Started by a call to the webhook <Mono>{d.webhook}</Mono>.</div>}
      {names.length > 0 && d.inputs && (
        <dl className="run-inputs">
          {names.map((n) => {
            const i = decl.find((x) => x.name === n);
            const v = d.inputs![n] ?? "";
            return (
              <div key={n} style={{ display: "contents" }}>
                <dt title={i?.description}>{n}</dt>
                <dd>{show(v, i?.type)}{i?.default !== undefined && i.default === v ? <span className="faint"> (default)</span> : null}</dd>
              </div>
            );
          })}
        </dl>
      )}
    </section>
  );
}

function show(v: string, type?: string) {
  if (type === "list") {
    try { const items = JSON.parse(v) as string[]; return items.length ? items.join(", ") : <span className="faint">(empty list)</span>; } catch { /* as it is */ }
  }
  return v === "" ? <span className="faint">(empty)</span> : v;
}

/** Says, in a re-run dialog, that the inputs are carried over. */
export function RerunInputsNote({ run }: { run: RunDetail }) {
  const d = (run as RunWithTriggers).trigger_detail;
  if (!d?.inputs || Object.keys(d.inputs).length === 0) return null;
  return (
    <p className="sub">
      {terms().say("The re-run keeps this {run}'s inputs (")}{Object.entries(d.inputs).map(([k, v]) => `${k}=${v}`).join(", ")}): the steps it reuses ran with them.
      {" "}{terms().say("To run with other values, start a new {run}.")}
    </p>
  );
}
