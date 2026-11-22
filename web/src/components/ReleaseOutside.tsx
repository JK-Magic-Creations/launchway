import { useState } from "react";
import { ErrorBox } from "./ui";
import { ago, clock } from "../format";
import { Chip, type Tone } from "../kit/Status";
import { Button, Sheet } from "../kit/layout";
import { useNow } from "../live";
import { releasesApi, type OutsidePublish, type ReleaseView } from "../releasesApi";
import { SpanHidden, ThCol } from "../kit/bits";

const TONE: Record<OutsidePublish["state"], Tone | undefined> = { pending: "warn", published: "ok", failed: "bad" };
const WORD: Record<OutsidePublish["state"], string> = { pending: "Waiting", published: "Published", failed: "Failed" };

/**
 * How a published release stands on the outside Git host of each push mirror that publishes releases: waiting (for the mirror to copy the
 * tag), published (with the address of the release there) or failed (with the reason, credentials removed, and a button to try again).
 * The Launchway release is the same whatever is shown here. Nothing is drawn when no mirror publishes releases.
 */
export function OutsideSheet({ v, onChanged }: { v: ReleaseView; onChanged: () => void }) {
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState("");
  const now = useNow(30000);
  const list = v.outside ?? [];
  if (list.length === 0) return null;
  const r = v.release;
  const retry = async (o: OutsidePublish) => {
    setBusy(o.mirror_id); setError(null);
    try { await releasesApi.retryOutside(r.project, r.version, o.mirror_id); onChanged(); } catch (e) { setError(e); }
    setBusy("");
  };
  return (
    <Sheet title="On an outside Git host">
      <p className="rel-hint" style={{ padding: "12px 16px 0" }}>A push mirror that publishes releases makes this release on the host it copies to: the same tag, title, notes and files. If that fails, this release is not affected.</p>
      <div className="rel-table-wrap"><table className="rel-table">
        <caption className="visually-hidden">This release on the outside Git host of each push mirror</caption>
        <thead><tr><ThCol>Mirror</ThCol><ThCol>State</ThCol><ThCol>Where</ThCol><ThCol>Tries</ThCol><ThCol>Updated</ThCol><ThCol><SpanHidden>Actions</SpanHidden></ThCol></tr></thead>
        <tbody>{list.map((o) => (
          <tr key={o.id}>
            <td>{o.mirror_url ?? o.mirror_id}</td>
            <td><Chip tone={TONE[o.state]}>{WORD[o.state] ?? o.state}</Chip>{o.reason ? <div className="faint">{o.reason}</div> : null}</td>
            <td>{o.url ? <a href={o.url} rel="noreferrer noopener">{o.url}</a> : "not yet"}</td>
            <td className="n">{o.attempts}</td>
            <td title={clock(o.updated_at)}>{ago(o.updated_at, now)}</td>
            <td>{o.state === "failed" && v.may.outside_retry && <Button sm disabled={busy === o.mirror_id} onClick={() => retry(o)}>Retry<SpanHidden> on {o.mirror_url ?? o.mirror_id}</SpanHidden></Button>}</td>
          </tr>))}</tbody></table></div>
      <ErrorBox error={error} />
    </Sheet>
  );
}
