import { useEffect, useState } from "react";
import { navigate } from "../router";

/** The inbox in the top bar, with how many entries are unread and how many wait on the person. Nothing is shown where the server has no
 * notifications. */
export function InboxLink({ current }: { current: boolean }) {
  const [n, setN] = useState<{ unread: number; waiting: number } | null>(null);
  useEffect(() => {
    let live = true;
    const tick = () => void import("../noticesApi").then((m) => m.noticesApi.count()).then((c) => { if (live) setN(c); }).catch(() => { if (live) setN(null); });
    tick();
    const t = window.setInterval(tick, 60000);
    return () => { live = false; window.clearInterval(t); };
  }, []);
  if (!n) return null;
  const total = n.unread + n.waiting;
  return (
    <a className={"btn btn-sm" + (current ? " on" : "")} href="/inbox" onClick={(e) => { e.preventDefault(); navigate("/inbox"); }} aria-current={current ? "page" : undefined}
      aria-label={total > 0 ? `Inbox: ${n.unread} unread, ${n.waiting} waiting on you` : "Inbox: nothing new"} data-act="inbox">
      <span className="hide-s">Inbox</span>{total > 0 && <span className="chip" aria-hidden="true"> {total}</span>}
    </a>
  );
}
