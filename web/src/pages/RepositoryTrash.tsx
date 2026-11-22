import { useCallback, useEffect, useState } from "react";
import { useMe } from "../auth";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { Button, PageHead, Sheet } from "../kit/layout";
import { Chip } from "../kit/Status";
import { trashApi, type TrashedRepo } from "../trashApi";
import { useHosting } from "./SshKeys";

const day = (iso: string) => new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });

/** Removing for good asks for the address, typed in, and says what is lost. */
function PurgeAsk({ row, busy, onGo, onCancel }: { row: TrashedRepo; busy: boolean; onGo: () => void; onCancel: () => void }) {
  const [typed, setTyped] = useState("");
  return (
    <div role="group" aria-label={`Remove ${row.path} for good`} className="callout callout-warn">
      <p><strong>Remove {row.path} for good.</strong> Its Git storage (every branch, tag and commit), pull requests, keys, webhooks and rules are deleted. This cannot be undone.</p>
      <label className="field"><span>Type <Mono>{row.path}</Mono> to confirm</span>
        <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" data-autofocus /></label>
      <div className="actions">
        <Button kind="bad" disabled={busy || typed !== row.path} onClick={onGo}>{busy ? "Removing…" : "Remove for good"}</Button>
        <Button onClick={onCancel}>Cancel</Button>
      </div>
    </div>
  );
}

function Row({ row, admin, onDone }: { row: TrashedRepo; admin: boolean; onDone: (what: string) => void }) {
  const [mode, setMode] = useState<"" | "restore" | "purge">("");
  const [name, setName] = useState(row.name_taken ? `${row.name}-restored` : row.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async (fn: () => Promise<string>) => {
    setBusy(true); setError(null);
    try { onDone(await fn()); } catch (e) { setError(e); } finally { setBusy(false); }
  };
  return (
    <li>
      <div className="main">
        <div className="nm"><Mono>{row.path}</Mono> {row.purging && <Chip tone="warn">being removed</Chip>}{row.name_taken && <Chip tone="warn">address taken</Chip>}</div>
        <div className="sb faint">Deleted {day(row.deleted_at)}{row.deleted_by ? ` by ${row.deleted_by}` : ""}; removed for good on {day(row.purge_at)}.</div>
        {mode === "restore" && (
          <form className="inputs-form" aria-label={`Restore ${row.path}`} onSubmit={(e) => { e.preventDefault(); run(async () => (await trashApi.restore(row.id, { name: name.trim() })).path); }}>
            <label className="field"><span>Name to restore it under{row.name_taken ? ` (${row.path} belongs to another repository now)` : ""}</span>
              <input value={name} onChange={(e) => setName(e.target.value)} data-autofocus /></label>
            <div className="actions"><Button kind="pri" type="submit" disabled={busy || !name.trim()}>{busy ? "Restoring…" : "Restore"}</Button><Button onClick={() => setMode("")}>Cancel</Button></div>
          </form>
        )}
        {mode === "purge" && <PurgeAsk row={row} busy={busy} onCancel={() => setMode("")} onGo={() => run(async () => { await trashApi.purge(row.id, row.path); return `${row.path} was removed for good.`; })} />}
        <ErrorBox error={error} />
      </div>
      {mode === "" && !row.purging && (
        <div className="actions">
          <Button sm onClick={() => setMode("restore")}>Restore</Button>
          {admin && <Button sm kind="bad" onClick={() => setMode("purge")}>Remove for good</Button>}
        </div>
      )}
    </li>
  );
}

/** The deleted repositories a person administers (an administrator of everything sees all of them): who deleted each,
 * when it goes for good, restoring one (under another name when its address was taken) and removing one for good. */
export function RepositoryTrashPage() {
  const hosting = useHosting();
  const me = useMe();
  const [rows, setRows] = useState<TrashedRepo[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [said, setSaid] = useState("");
  const load = useCallback(() => trashApi.list().then(setRows).catch(setError), []);
  useEffect(() => { if (hosting === "on") load(); }, [hosting, load]);
  if (hosting === null) return null;
  return (
    <div className="page">
      <PageHead eyebrow="Code" title="Deleted repositories"
        sentence={hosting === "off" ? "Git hosting is off on this server." : "A deleted repository waits here, with everything it had, until it is restored or its retention ends."}
        actions={<Link href="/repositories" className="btn btn-sm">Repositories</Link>} />
      <ErrorBox error={error} />
      <p role="status" className="faint">{said}</p>
      {hosting === "on" && rows && rows.length === 0 && <Empty title="No deleted repositories">Nothing you administer is in the trash.</Empty>}
      {rows && rows.length > 0 && (
        <Sheet title="In the trash">
          <ul className="rowlist">
            {rows.map((r) => <Row key={r.id} row={r} admin={!!me?.admin} onDone={(what) => { setSaid(what.includes("for good") ? what : `Restored ${what}.`); load(); }} />)}
          </ul>
        </Sheet>
      )}
    </div>
  );
}
