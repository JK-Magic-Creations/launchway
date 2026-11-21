import { useEffect, useState } from "react";
import { launchApi, type LaunchLineView } from "../api/launch";
import { Link } from "../components/ui";
import { LaunchLine, toStations } from "../kit/LaunchLine";
import { Chip } from "../kit/Status";
import { terms } from "../terms";

/** The launch line of the latest run of a pull request, the same line the Home and Activity pages draw (the kit's
 * LaunchLine, through its public props), with the run's number, its state in words and a link to it. It sits under
 * the pull request's own four stations rather than replacing them: those say where the change stands in review and
 * merge, which a run knows nothing about; this one says what the pipeline did with this exact commit. The line is
 * fetched again while the run is not finished, so it moves as the run does. */
export function PullRunLine({ runId, head }: { runId: string | undefined; head: string }) {
  const [line, setLine] = useState<LaunchLineView | null>(null);
  const [failed, setFailed] = useState(false);
  const finished = line ? ["succeeded", "failed", "canceled", "cancelled"].includes(line.status) : false;
  useEffect(() => {
    if (!runId) return;
    let alive = true;
    const load = () => launchApi.line(runId).then((l) => { if (alive) { setLine(l); setFailed(false); } }).catch(() => { if (alive) setFailed(true); });
    load();
    if (finished) return () => { alive = false; };
    const t = window.setInterval(load, 5000);
    return () => { alive = false; window.clearInterval(t); };
  }, [runId, head, finished]);
  if (!runId) return <p className="faint small pr-runline">No run has started for this pull request yet.</p>;
  if (!line) return failed ? <p className="faint small pr-runline">{terms().say("The latest {run},")} <Link href={`/runs/${runId}`}>{runId}</Link>, cannot be shown here.</p> : null;
  return (
    <section className="pr-runline" aria-label={terms().say("Latest {run}")}>
      <div className="pr-runline-head"><span>{terms().say("Latest {run}:")} <Link href={`/runs/${line.run}`}>#{line.number} of {line.project}</Link> <span className="faint">{line.now}</span></span></div>
      <LaunchLine stations={toStations(line.stations)} label={`The pipeline's launch line for run #${line.number}`} />
    </section>
  );
}

/** One editable list of the pull request's side panel: reviewers, assignees or labels. Each item has a button that
 * removes it, and a box adds more, but only when the server's `actions` say this person may (`editable`), so a viewer
 * sees the list and nothing that would be refused. */
export function PullList({ title, items, editable, noun, none, chip, onAdd, onRemove }: {
  title: string; items: string[]; editable: boolean; noun: string; none: string; chip?: boolean;
  onAdd: (name: string) => Promise<void>; onRemove: (name: string) => Promise<void>;
}) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const run = async (f: () => Promise<void>) => { setBusy(true); try { await f(); } finally { setBusy(false); } };
  return (
    <div role="group" aria-label={title}>
      <h3>{title}</h3>
      {items.length === 0 && <span className="faint">{none}</span>}
      <ul className="pr-people">
        {items.map((n) => (
          <li key={n}>{chip ? <Chip>{n}</Chip> : n}
            {editable && <button type="button" className="btn btn-sm" disabled={busy} aria-label={`Remove ${n} from ${title.toLowerCase()}`} onClick={() => run(() => onRemove(n))}>Remove</button>}
          </li>
        ))}
      </ul>
      {editable && (
        <form className="pr-people-add" onSubmit={(e) => { e.preventDefault(); const v = text.trim(); if (v) run(async () => { await onAdd(v); setText(""); }); }} aria-label={`Add to ${title.toLowerCase()}`}>
          <input value={text} aria-label={`Add a ${noun}`} placeholder={noun === "label" ? "bug" : "user name"} onChange={(e) => setText(e.target.value)} />
          <button type="submit" className="btn btn-sm" disabled={busy || !text.trim()}>Add</button>
        </form>
      )}
    </div>
  );
}
