import { Link } from "../components/ui";
import { Sheet } from "../kit/layout";
import { itemHref, useLoad } from "./common";
import { workApi, type HomeWork as Data } from "./workApi";

// A small section on Home: your open items, the inbox count, what agents did and what waits for you. Loaded only when Home opens.

export default function HomeWork() {
  const r = useLoad<Data>("home-work", () => workApi.home());
  const h = r.data;
  if (r.error || !h) return null;
  const review = h.agents?.review ?? [], recent = h.agents?.recent ?? [], samples = h.samples ?? [];
  const waiting = review.length, did = recent.length;
  if (!h.assigned_total && !h.unread && waiting === 0 && did === 0 && samples.length === 0) return null;
  return (
    <Sheet title="Your work" actions={<Link href="/work" className="small">Open Work</Link>} pad>
      <ul className="wk-rows tight">
        {(h.assigned_total ?? 0) > 0 && <li><strong className="num">{h.assigned_total}</strong> open {h.assigned_total === 1 ? "item is" : "items are"} assigned to you{h.assigned?.[0] && <>, first <Link href={itemHref(h.assigned[0].ref)}>{h.assigned[0].ref} {h.assigned[0].title}</Link></>}.</li>}
        {(h.unread ?? 0) > 0 && <li><Link href="/work/inbox"><span className="num">{h.unread}</span> unread in your inbox</Link>.</li>}
        {waiting > 0 && <li><strong className="num">{waiting}</strong> {waiting === 1 ? "item" : "items"} done by an agent {waiting === 1 ? "waits" : "wait"} for your review: {review.slice(0, 3).map((it, i) => <span key={it.id}>{i > 0 && ", "}<Link href={itemHref(it.ref)}>{it.ref}</Link></span>)}.</li>}
        {did > 0 && <li>Agents did <strong className="num">{did}</strong> recent {did === 1 ? "thing" : "things"} for you.</li>}
        {samples.length > 0 && <li><Link href="/work"><span className="num">{samples.length}</span> audit {samples.length === 1 ? "sample waits" : "samples wait"} for a person to look</Link>.</li>}
      </ul>
    </Sheet>
  );
}
