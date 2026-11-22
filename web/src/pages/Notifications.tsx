import { useCallback, useEffect, useState } from "react";
import { ago, clock } from "../format";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { PageHead, Sheet } from "../kit/layout";
import { announce } from "../kit/announce";
import { noticesApi, type Inbox, type MailStatus, type Preferences, type Watch } from "../noticesApi";
import { prose } from "../prose";
import { BtnPrimary, BtnSm, DivFaintSmall, DivPage, LabelField, PSub, SpanFaintSmall, SpanHidden, ThCol, ThRow } from "../kit/bits";
const P = await prose("pages.Notifications");

const STATES: [string, string][] = [["open", "Open"], ["unread", "Unread"], ["done", "Done"], ["all", "All"]];
const SOURCES: [string, string][] = [["", "Everything"], ["notice", "Notices"], ["work", "Work"], ["pull", "Waiting on you"]];

/** One inbox: the notices, the work entries and the pull requests that wait on the person, with read and done marks and filters. */
export function InboxPage() {
  const [state, setState] = useState("open");
  const [source, setSource] = useState("");
  const [data, setData] = useState<Inbox | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [now] = useState(() => Date.now());
  const load = useCallback(() => noticesApi.inbox(state, source, "").then((d) => { setData(d); setError(null); }).catch(setError), [state, source]);
  useEffect(() => { void load(); }, [load]);
  const mark = async (what: "read" | "done", ids: string[], value: boolean) => {
    try {
      await (what === "read" ? noticesApi.read(ids, value) : noticesApi.done(ids, value));
      announce(what === "read" ? (value ? "Marked read" : "Marked unread") : value ? "Marked done" : "Opened again");
      await load();
    } catch (e) { setError(e); }
  };
  const marks = (data?.items ?? []).filter((i) => i.source !== "pull");
  const unreadIds = marks.filter((i) => !i.read).map((i) => i.id);
  return (
    <DivPage>
      <PageHead eyebrow="Inbox" title="Inbox" sentence={<>{P.t0}<Link href="/settings/notifications">{P.t1}</Link>.</>} />
      <ErrorBox error={error} />
      <div className="code-bar">
        <LabelField><span>Show</span>
          <select value={state} onChange={(e) => setState(e.target.value)}>{STATES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select></LabelField>
        <LabelField><span>From</span>
          <select value={source} onChange={(e) => setSource(e.target.value)}>{SOURCES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select></LabelField>
        {unreadIds.length > 0 && <BtnSm onClick={() => mark("read", unreadIds.slice(0, 200), true)}>Mark all read</BtnSm>}
      </div>
      {data && <PSub role="status">{data.unread} unread, {data.waiting} waiting on you.</PSub>}
      {data && data.items.length === 0 && <Empty title="Nothing here">{state === "open" ? "You are caught up." : "Nothing matches."}</Empty>}
      {data && data.items.length > 0 && (
        <Sheet title="Entries">
          <ul className="inbox-list">
            {data.items.map((i) => (
              <li key={i.id} className={"inbox-item" + (i.read || i.waiting_on_you ? "" : " unread")}>
                <div>
                  {i.link ? <Link href={i.link}>{i.title}</Link> : i.title}
                  {i.waiting_on_you && <span className="chip"> waiting on you</span>}
                  {!i.read && !i.waiting_on_you && <SpanHidden> (unread)</SpanHidden>}
                  <DivFaintSmall>{i.repository && <><Mono>{i.repository}</Mono>{" "}</>}{i.actor && <>by {i.actor}{" "}</>}<span title={clock(Date.parse(i.at))}>{ago(Date.parse(i.at), now)}</span>{i.detail ? ` · ${i.detail}` : ""}</DivFaintSmall>
                </div>
                {i.source !== "pull" && (
                  <div className="row-actions">
                    <BtnSm onClick={() => mark("read", [i.id], !i.read)}>{i.read ? "Mark unread" : "Mark read"}<SpanHidden>: {i.title}</SpanHidden></BtnSm>
                    {i.source === "notice" && <BtnSm onClick={() => mark("done", [i.id], !i.done)}>{i.done ? "Open again" : "Done"}<SpanHidden>: {i.title}</SpanHidden></BtnSm>}
                  </div>
                )}
              </li>
            ))}
          </ul>
        </Sheet>
      )}
    </DivPage>
  );
}

const CHANNELS: [string, string][] = [["in_app", "In the inbox"], ["email", "Inbox and e-mail"], ["off", "Off"]];
const LEVELS: [string, string][] = [["all", "Everything"], ["participating", "What I take part in"], ["none", "Nothing"]];
const toMinutes = (v: string) => { const [h, m] = v.split(":").map(Number); return h * 60 + m; };
const fromMinutes = (n: number) => `${String(Math.floor(n / 60)).padStart(2, "0")}:${String(n % 60).padStart(2, "0")}`;

/** What a person is told of and how: per kind, per repository or Flight, quiet hours and the daily digest. */
export function NotificationSettingsPage() {
  const [p, setP] = useState<Preferences | null>(null);
  const [mail, setMail] = useState<MailStatus | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [scope, setScope] = useState("");
  const [level, setLevel] = useState<Watch["level"]>("all");
  const [testing, setTesting] = useState(false);
  const [tested, setTested] = useState("");
  useEffect(() => {
    noticesApi.preferences().then(setP).catch(setError);
    noticesApi.mail().then(setMail).catch(() => {}); // only administrators may read it
  }, []);
  const save = async (b: Parameters<typeof noticesApi.save>[0], said: string) => {
    setError(null);
    try { setP(await noticesApi.save(b)); announce(said); } catch (e) { setError(e); }
  };
  if (!p) return <DivPage><PageHead eyebrow="Settings" title="Notifications" /><ErrorBox error={error} /></DivPage>;
  const quiet = p.settings.quiet_start >= 0;
  return (
    <DivPage>
      <PageHead eyebrow="Settings" title="Notifications" sentence={<>{P.t2}<Link href="/inbox">inbox</Link>.</>} />
      <ErrorBox error={error} />
      {!p.mail_on && <div className="callout callout-allowed" role="status"><strong>{P.t3}</strong> {P.t4}<Mono>mail</Mono> installation setting.</div>}
      {p.mail_on && !p.address_known && <div className="callout callout-allowed" role="status"><strong>No address is known for you.</strong> {P.t5}</div>}

      <Sheet title="What you are told" pad>
        <table className="table">
          <caption className="sr-only">{P.t6}</caption>
          <thead><tr><ThCol>When</ThCol><ThCol>How</ThCol></tr></thead>
          <tbody>
            {p.kinds.map((k) => (
              <tr key={k.kind}>
                <ThRow className="plain-th">{k.title}{k.waiting_on_you && <SpanFaintSmall> (waits on you)</SpanFaintSmall>}</ThRow>
                <td><select aria-label={`How you are told: ${k.title}`} value={k.channel} onChange={(e) => save({ kinds: { [k.kind]: e.target.value } }, `Saved: ${k.title}`)}>
                  {CHANNELS.map(([v, l]) => <option key={v} value={v} disabled={v === "email" && !p.mail_on}>{l}</option>)}</select></td>
              </tr>
            ))}
          </tbody>
        </table>
        <PSub>{P.t7}</PSub>
      </Sheet>

      <Sheet title={P.t8} pad>
        <PSub>{P.t9}<strong>Everything</strong> adds all activity there; <strong>Nothing</strong> mutes it.</PSub>
        {p.watches.length === 0 && <p className="faint">No choices yet.</p>}
        <ul className="plain-list">
          {p.watches.map((w) => (
            <li key={w.scope}>
              <Mono>{w.scope}</Mono>{" "}
              <select aria-label={`Level for ${w.scope}`} value={w.level} onChange={(e) => save({ watches: [{ scope: w.scope, level: e.target.value as Watch["level"] }] }, `Saved: ${w.scope}`)}>
                {LEVELS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select>{" "}
              <BtnSm onClick={() => save({ watches: [{ scope: w.scope, level: "participating" }] }, `Removed: ${w.scope}`)}>Remove<SpanHidden> {w.scope}</SpanHidden></BtnSm>
            </li>
          ))}
        </ul>
        <form className="inputs-form" aria-label="Watch a repository or Flight" onSubmit={(e) => { e.preventDefault(); void save({ watches: [{ scope: scope.trim(), level }] }, `Saved: ${scope}`).then(() => setScope("")); }}>
          <LabelField><span>Repository or Flight</span><input value={scope} placeholder={P.t10} onChange={(e) => setScope(e.target.value)} /></LabelField>
          <LabelField><span>Level</span><select value={level} onChange={(e) => setLevel(e.target.value as Watch["level"])}>{LEVELS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select></LabelField>
          <div><button type="submit" className="btn" disabled={!scope.trim()}>Save choice</button></div>
        </form>
      </Sheet>

      <Sheet title="Quiet hours and digest" pad>
        <form className="inputs-form" aria-label="Quiet hours and digest" onSubmit={(e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget);
          const on = f.get("quiet") === "on";
          void save({ settings: { quiet_start: on ? toMinutes(String(f.get("from"))) : -1, quiet_end: on ? toMinutes(String(f.get("to"))) : -1, zone: String(f.get("zone")), digest: f.get("digest") === "on", digest_hour: Number(f.get("hour")) } }, "Saved");
        }}>
          <label className="check"><input type="checkbox" name="quiet" defaultChecked={quiet} /> {P.t11}</label>
          <div className="tokenform-grid">
            <LabelField><span>From</span><input type="time" name="from" defaultValue={fromMinutes(quiet ? p.settings.quiet_start : 22 * 60)} /></LabelField>
            <LabelField><span>To</span><input type="time" name="to" defaultValue={fromMinutes(quiet ? p.settings.quiet_end : 7 * 60)} /></LabelField>
            <LabelField><span>Time zone</span><input name="zone" defaultValue={p.settings.zone} placeholder="Europe/Berlin" /></LabelField>
          </div>
          <label className="check"><input type="checkbox" name="digest" defaultChecked={p.settings.digest} /> {P.t12}</label>
          <LabelField><span>Digest hour (0 to 23)</span><input type="number" name="hour" min={0} max={23} defaultValue={p.settings.digest_hour} /></LabelField>
          <div><BtnPrimary>Save</BtnPrimary></div>
        </form>
      </Sheet>

      {mail && (
        <Sheet title="Mail server (administrators)" pad>
          {!mail.enabled ? <p>{P.t13}<Mono>mail</Mono> {P.t14}</p> : (
            <>
              <p><Mono>{mail.host}:{mail.port}</Mono> over {mail.security}, from <Mono>{mail.from}</Mono>. {mail.pending} waiting, {mail.sent} sent ({mail.sent_last_hour} in the last hour), {mail.failed} failed. Limit {mail.rate_per_minute} a minute, {mail.rate_per_hour} an hour.</p>
              {!mail.public_url && <p role="note">No <Mono>mail.public_url</Mono> {P.t15}</p>}
              <button type="button" className="btn" disabled={testing} onClick={async () => {
                setTesting(true); setTested("");
                try { const r = await noticesApi.testMail(); setTested(`Sent a test message to ${r.to}.`); } catch (e) { setError(e); } finally { setTesting(false); }
              }}>{testing ? "Sending…" : "Send a test message to me"}</button>
              {tested && <p role="status">{tested}</p>}
            </>
          )}
        </Sheet>
      )}
    </DivPage>
  );
}
