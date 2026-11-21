import { useState } from "react";
import { Chip } from "../kit/Status";

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
