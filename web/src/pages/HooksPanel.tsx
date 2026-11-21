import { useCallback, useEffect, useState } from "react";
import { ago, clock } from "../format";
import { Empty, ErrorBox, Mono } from "../components/ui";
import { Sheet } from "../kit/layout";
import { Chip } from "../kit/Status";
import { forgeApi, type Delivery, type Hook, type HookScope } from "../forgeApi";

const EVENT_HELP: Record<string, string> = {
  push: "a branch or tag moved",
  create: "a branch or tag was created",
  delete: "a branch or tag was deleted",
  repository: "the repository was created, renamed or deleted",
  pull_request: "reserved for pull requests",
  pull_request_review: "reserved for pull-request reviews",
};

function stateTone(d: Delivery): "ok" | "bad" | "warn" {
  return d.state === "delivered" ? "ok" : d.state === "failed" ? "bad" : "warn";
}

/** Webhooks of a repository or of an owner, with the delivery log of each. */
export function HooksPanel({ scope, who }: { scope: HookScope; who: string }) {
  const [hooks, setHooks] = useState<Hook[] | null>(null);
  const [events, setEvents] = useState<string[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [secret, setSecret] = useState("");
  const [chosen, setChosen] = useState<string[]>(["push"]);
  const [made, setMade] = useState<{ secret: string } | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    forgeApi.hooks(scope).then((r) => { setHooks(r.items); setEvents(r.events); setError(null); }).catch(setError);
  }, [scope.owner, scope.name]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(load, [load]);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null); setMade(null);
    try {
      const r = await forgeApi.addHook(scope, { name: name.trim() || undefined, url: url.trim(), events: chosen, secret: secret || undefined });
      if (r.secret) setMade({ secret: r.secret });
      setUrl(""); setName(""); setSecret("");
      load();
    } catch (err) { setError(err); } finally { setBusy(false); }
  };
  const toggle = (ev: string) => setChosen((c) => (c.includes(ev) ? c.filter((x) => x !== ev) : [...c, ev]));
  const act = async (f: () => Promise<unknown>) => { setError(null); try { await f(); load(); } catch (err) { setError(err); } };

  return (
    <>
      <Sheet title="Add a webhook" pad>
        <p className="sub">Launchway sends each event to the address as signed JSON ({who}). Every delivery carries an HMAC-SHA256 signature in <Mono>X-Launchway-Signature-256</Mono>.</p>
        <form onSubmit={add} className="inputs-form" aria-label="Add a webhook">
          <div className="tokenform-grid">
            <label className="field"><span>Address</span><input value={url} required type="url" placeholder="https://ci.example.com/hook" onChange={(e) => setUrl(e.target.value)} /></label>
            <label className="field"><span>Name (optional)</span><input value={name} maxLength={100} onChange={(e) => setName(e.target.value)} /></label>
            <label className="field"><span>Secret (optional)</span><input value={secret} type="password" autoComplete="off" placeholder="left empty, one is made and shown once" onChange={(e) => setSecret(e.target.value)} /></label>
          </div>
          <fieldset className="field"><legend>Events</legend>
            {events.map((ev) => (
              <label key={ev} className="check"><input type="checkbox" checked={chosen.includes(ev)} onChange={() => toggle(ev)} /> <Mono>{ev}</Mono> <span className="faint">{EVENT_HELP[ev]}</span></label>
            ))}
          </fieldset>
          <div><button type="submit" className="btn btn-primary" disabled={busy || !url.trim() || chosen.length === 0}>{busy ? "Adding…" : "Add webhook"}</button></div>
        </form>
      </Sheet>
      {made && (
        <div className="callout callout-ok tokenmade" role="status">
          <strong>The secret for this webhook. Copy it now: it is not shown again.</strong>
          <div className="tokenvalue"><input readOnly value={made.secret} aria-label="The secret" onFocus={(e) => e.currentTarget.select()} className="mono" /></div>
          <button type="button" className="btn" onClick={() => setMade(null)}>Done</button>
        </div>
      )}
      <ErrorBox error={error} />
      <Sheet title="Webhooks">
        {hooks && hooks.length === 0 && <Empty title="No webhooks yet" />}
        {hooks && hooks.map((h) => (
          <div key={h.id} className="hook-row" data-testid="hook">
            <div className="hook-head">
              <div><strong>{h.name}</strong> <Chip tone={h.active ? "ok" : "warn"}>{h.active ? "on" : "off"}</Chip></div>
              <div className="faint small mono">{h.url}</div>
              <div className="faint small">{h.events.join(", ")}{h.has_secret ? " · signed" : " · not signed"}</div>
              {h.disabled_reason && <div className="callout callout-allowed" role="status">{h.disabled_reason}</div>}
            </div>
            <div className="hook-actions">
              <button type="button" className="btn btn-sm" onClick={() => act(async () => { const d = await forgeApi.testHook(scope, h.id); setOpen(h.id); if (d.state !== "delivered") setError(new Error(d.error ?? "The address did not accept the test delivery.")); })}>Send a test</button>
              <button type="button" className="btn btn-sm" onClick={() => setOpen(open === h.id ? null : h.id)}>{open === h.id ? "Hide log" : "Delivery log"}</button>
              <button type="button" className="btn btn-sm" onClick={() => act(() => forgeApi.updateHook(scope, h.id, { active: !h.active }))}>{h.active ? "Turn off" : "Turn on"}</button>
              <button type="button" className="btn btn-danger-outline btn-sm" onClick={() => { if (window.confirm(`Delete the webhook "${h.name}" and its delivery log?`)) act(() => forgeApi.deleteHook(scope, h.id)); }}>Delete</button>
            </div>
            {open === h.id && <DeliveryLog scope={scope} hook={h} onError={setError} />}
          </div>
        ))}
      </Sheet>
    </>
  );
}

function DeliveryLog({ scope, hook, onError }: { scope: HookScope; hook: Hook; onError: (e: unknown) => void }) {
  const [rows, setRows] = useState<Delivery[] | null>(null);
  const [show, setShow] = useState<string | null>(null);
  const load = useCallback(() => { forgeApi.deliveries(scope, hook.id).then(setRows).catch(onError); }, [scope.owner, scope.name, hook.id]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(load, [load]);
  const [now] = useState(() => Date.now());
  if (!rows) return null;
  if (rows.length === 0) return <p className="sub pad">Nothing has been sent yet. Send a test, or push to the repository.</p>;
  return (
    <table className="table" aria-label="Delivery log">
      <thead><tr><th>Event</th><th>Result</th><th>Attempts</th><th>Time</th><th>When</th><th><span className="visually-hidden">Actions</span></th></tr></thead>
      <tbody>
        {rows.map((d) => (
          <tr key={d.id}>
            <td><Mono>{d.event}</Mono>{d.redelivery_of && <span className="faint small"> (sent again)</span>}
              {show === d.id && <pre className="small mono" style={{ whiteSpace: "pre-wrap" }}>{JSON.stringify(d.payload, null, 2)}</pre>}
              {d.error && <div className="small bad-ink">{d.error}</div>}</td>
            <td><Chip tone={stateTone(d)}>{d.state}{d.status ? ` · ${d.status}` : ""}</Chip></td>
            <td>{d.attempts}</td>
            <td>{d.duration_ms} ms</td>
            <td title={clock(Date.parse(d.created_at))}>{ago(Date.parse(d.created_at), now)}</td>
            <td className="td-actions">
              <button type="button" className="btn btn-sm" onClick={() => setShow(show === d.id ? null : d.id)}>{show === d.id ? "Hide request" : "Request"}</button>{" "}
              <button type="button" className="btn btn-sm" onClick={() => { forgeApi.redeliver(scope, hook.id, d.id).then(load, onError); }}>Send again</button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
