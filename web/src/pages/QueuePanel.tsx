import { useCallback, useState } from "react";
import { call } from "../api";
import { ErrorBox } from "../components/ui";
import { useLoad } from "../guided/useLoad";
import { Sheet } from "../kit/layout";

interface Action { name: string; available: boolean; reason?: string }
interface Entry { run: string; number: number; project: string; position: number; code: string; reason: string; actions: Action[] }

const WORDS: Record<string, string> = { run_next: "Run next", hold: "Hold", release: "Release" };

/** The runs held back by a limit on concurrent runs, in the order they will go, with why each waits and
 * what it waits behind. Only what the server's `actions` list says is available is offered. */
export default function QueuePanel({ project }: { project: string }) {
  const q = useLoad(useCallback(() => call<Entry[]>("GET", `/api/v1/queue${project ? `?project=${encodeURIComponent(project)}` : ""}`), [project]), 5000);
  const [err, setErr] = useState<unknown>(null);
  const act = (run: string, action: string) => call("POST", `/api/v1/runs/${run}/queue`, { action }).then(() => { setErr(null); q.reload(); }).catch(setErr);
  if (!Array.isArray(q.data) || q.data.length === 0) return null;
  return (
    <Sheet title="Waiting for room" actions={<span className="faint small">In the order they will go. A limit holds a run back; running steps are never stopped for it.</span>}>
      <ErrorBox error={err} />
      <ol className="sh-b">
        {q.data.map((e) => (
          <li key={e.run} data-code={e.code}>
            <a href={`/runs/${e.run}`}>{e.project} run #{e.number}</a>: {e.reason}{" "}
            {e.actions.filter((a) => a.available).map((a) => <button key={a.name} className="btn small" onClick={() => act(e.run, a.name)}>{WORDS[a.name] ?? a.name}</button>)}
          </li>
        ))}
      </ol>
    </Sheet>
  );
}
