import { useState } from "react";
import { Link } from "../components/ui";
import { Button } from "../kit/layout";
import { announce } from "../kit/announce";
import { Blank, ErrorPanel, itemHref, Loading, useLoad, when, WorkPage } from "./common";
import { workApi } from "./workApi";

// What happened to work you follow: assigned to you, mentioned, moved by an agent. Email is not available, and the page says so.

export default function Inbox() {
  const inbox = useLoad("inbox", () => workApi.inbox());
  const [error, setError] = useState<unknown>(null);
  const list = inbox.data?.entries ?? [];
  const unread = list.filter((e) => !e.read);
  const readAll = async () => {
    try { await workApi.markRead(unread.map((e) => e.id)); announce("All marked as read."); inbox.reload(); } catch (e) { setError(e); }
  };
  const readOne = async (id: number) => {
    try { await workApi.markRead([id]); inbox.reload(); } catch (e) { setError(e); }
  };
  return (
    <WorkPage title="Inbox" eyebrow={<Link href="/work">Work</Link>} sentence="What happened to work you follow."
      actions={<Button onClick={readAll} disabled={unread.length === 0}>Mark all read</Button>}>
      <p className="callout callout-wait" role="note">Email is not available. Entries appear here and on Home, and notification channels carry them elsewhere.</p>
      <ErrorPanel error={error ?? inbox.error} onRetry={inbox.reload} />
      {inbox.loading && <Loading what="inbox" />}
      {inbox.data && list.length === 0 && <Blank title="Nothing in your inbox" action={<Link href="/work">Go to your work</Link>}>When an item is assigned to you, an agent hands work over, or someone mentions you, it shows up here.</Blank>}
      {list.length > 0 && (
        <ul className="sheet wk-inbox" aria-label={`Inbox, ${unread.length} unread`}>
          {list.map((e) => (
            <li key={e.id} className={e.read ? "read" : "unread"}>
              <span className={"dot" + (e.read ? "" : " on")} aria-hidden="true" />
              <div className="what">
                <div>{!e.read && <span className="visually-hidden">Unread: </span>}{e.text}</div>
                <div className="faint small">{e.kind.replace(/[._]/g, " ")}, {when(e.at)}{e.ref && <> · <Link href={itemHref(e.ref)}>{e.ref}{e.title ? " " + e.title : ""}</Link></>}</div>
              </div>
              {!e.read && <Button sm kind="ghost" aria-label={`Mark as read: ${e.text}`} onClick={() => readOne(e.id)}>Mark read</Button>}
            </li>
          ))}
        </ul>
      )}
    </WorkPage>
  );
}
