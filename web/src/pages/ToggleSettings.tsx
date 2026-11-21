import { useCallback, useState } from "react";
import { useMe, canOperate } from "../auth";
import { ErrorBox } from "../components/ui";
import { ago, clock } from "../format";
import { useLoad } from "../guided/useLoad";
import { Dialog } from "../kit/Dialog";
import { Button, EmptyState, LinkButton, Sheet } from "../kit/layout";
import { useNow } from "../live";
import { togglesApi, type Guards, type KeyInfo } from "../togglesApi";
import { ProjectHead } from "./PipelineChrome";
import "../toggles.css";
import { terms } from "../terms";

/** Safeguards, SDK keys and scheduled changes of a project's toggles. */
export function ToggleSettingsPage({ name }: { name: string }) {
  const me = useMe();
  const now = useNow(30000);
  const g = useLoad(useCallback(() => togglesApi.safeguards(name), [name]));
  const k = useLoad(useCallback(() => togglesApi.keys(name), [name]));
  const s = useLoad(useCallback(() => togglesApi.schedules(name), [name]));
  const [made, setMade] = useState<{ key: string; note: string } | null>(null);
  const [making, setMaking] = useState(false);
  const [guard, setGuard] = useState<{ env: string; g: Guards } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const admin = !!me?.admin || me?.roles?.[name] === "admin";
  const can = !!me?.admin || canOperate(me, name);
  const act = async (fn: () => Promise<unknown>, then: () => void) => { setError(null); try { await fn(); then(); } catch (e) { setError(e); } };
  const envs = Object.entries(g.data?.environments ?? {});
  return (
    <div className="page">
      <ProjectHead name={name} on="toggles" title={`Keys and safeguards of ${name}'s toggles`}
        sentence="Who may change toggles and how, the keys applications read them with, and the changes scheduled for later."
        actions={<LinkButton href={`/projects/${encodeURIComponent(name)}/toggles`} icon="arrow">The toggles</LinkButton>} />
      <ErrorBox error={error} />
      <Sheet title="Safeguards">
        <p className="sub">{terms().say("On by default, and the team in charge. A reason is asked for every change; an approval by someone else in a protected {environment}. Turning one down needs a reason and is recorded; a level above can lock one.")}</p>
        {envs.length === 0 ? <p className="sub">{terms().say("No {environments} yet.")}</p> : (
          <table className="tg-table">
            <caption className="sr-only">{terms().say("Safeguards in each {environment}")}</caption>
            <thead><tr><th scope="col">{terms().say("{Environment}")}</th><th scope="col">Reason required</th><th scope="col">Approval required</th><th scope="col">People</th><th scope="col">Watch after a change</th><th scope="col"><span className="sr-only">Change</span></th></tr></thead>
            <tbody>{envs.map(([env, x]) => (
              <tr key={env}>
                <th scope="row">{env}</th>
                <td>{x.require_reason ? "Yes" : "No"}<span className="tg-meta">{x.from.require_reason}</span></td>
                <td>{x.require_approval ? "Yes" : "No"}<span className="tg-meta">{x.from.require_approval}{x.locked_by?.require_approval ? ` · locked by ${x.locked_by.require_approval}` : ""}</span></td>
                <td>{x.approvals}</td>
                <td>{x.watch_minutes > 0 ? `${x.watch_minutes} min` : "None"}</td>
                <td>{admin && <Button sm onClick={() => setGuard({ env, g: x })} aria-label={`Change the safeguards of ${env}`}>Change</Button>}</td>
              </tr>
            ))}</tbody>
          </table>
        )}
        {!admin && <p className="tg-callout">Changing safeguards needs the admin role on {name}.</p>}
      </Sheet>
      <Sheet title="SDK keys" actions={admin ? <Button sm kind="pri" onClick={() => setMaking(true)}>Make a key</Button> : undefined}>
        <p className="sub">{terms().say("A server key is a secret: it reads every rule, for one {environment}. A client key is public and can only ask for values of the toggles you made visible. Neither can change anything.")}</p>
        {!can ? <p className="sub">Listing keys needs the operator role.</p> : (k.data?.keys ?? []).length === 0 ? <EmptyState title="No keys yet">Make a server key for a service that reads toggles, or a client key for a web page.</EmptyState> : (
          <ul className="tg-keys">
            {k.data!.keys.map((x: KeyInfo) => (
              <li key={x.id}>
                <span><b>{x.name || x.id}</b> <span className="tg-kind">{x.kind}</span> in {x.environment}</span>
                <span className="mono">{x.prefix}</span>
                <span className="tg-meta">{x.revoked_at ? `revoked ${ago(x.revoked_at, now)}` : x.expires_at ? `valid until ${clock(x.expires_at)} (rotated)` : x.last_used_at ? `last used ${ago(x.last_used_at, now)}` : "never used"}</span>
                {admin && !x.revoked_at && <>
                  <Button sm onClick={() => act(async () => { const r = await togglesApi.rotateKey(name, x.id); setMade({ key: r.key, note: r.note }); }, k.reload)}>Rotate</Button>
                  <Button sm kind="bad" onClick={() => act(() => togglesApi.revokeKey(name, x.id), k.reload)}>Revoke</Button>
                </>}
              </li>
            ))}
          </ul>
        )}
      </Sheet>
      <Sheet title="Scheduled changes">
        {(s.data?.schedules ?? []).length === 0 ? <p className="sub">Nothing is scheduled. Set a time when changing a toggle to make it then.</p> : (
          <ul className="tg-timeline">
            {s.data!.schedules.map((x) => (
              <li key={x.id}><span><b>{x.toggle.key}</b> in {x.environment} at {clock(x.run_at)}: {x.reason}</span><span className="faint small">by {x.created_by}</span>
                {can && <Button sm onClick={() => act(() => togglesApi.cancelSchedule(name, x.id), s.reload)}>Cancel</Button>}</li>
            ))}
          </ul>
        )}
      </Sheet>
      {making && <MakeKey project={name} onClose={() => setMaking(false)} onMade={(m) => { setMaking(false); setMade(m); k.reload(); }} />}
      {guard && <Guard project={name} env={guard.env} g={guard.g} onClose={() => setGuard(null)} onDone={() => { setGuard(null); g.reload(); }} />}
      {made && (
        <Dialog label="Your new key" onClose={() => setMade(null)}>
          <div className="tg-form"><h2>Your new key</h2>
            <p className="tg-secret" data-testid="the-key">{made.key}</p>
            <p>{made.note}</p>
            <div className="dialog-actions"><button className="btn btn-primary" onClick={() => setMade(null)}>I have kept it</button></div></div>
        </Dialog>
      )}
    </div>
  );
}

function MakeKey({ project, onClose, onMade }: { project: string; onClose: () => void; onMade: (m: { key: string; note: string }) => void }) {
  const envs = useLoad(useCallback(() => togglesApi.safeguards(project), [project]));
  const [env, setEnv] = useState("");
  const [kind, setKind] = useState("server");
  const [name, setName] = useState("");
  const [origins, setOrigins] = useState("");
  const [error, setError] = useState<unknown>(null);
  const names = Object.keys(envs.data?.environments ?? {});
  const go = async () => {
    setError(null);
    try { const r = await togglesApi.makeKey(project, { environment: env || names[0]!, kind, name, origins: origins.split(/[\s,]+/).filter(Boolean) }); onMade({ key: r.key, note: r.note }); } catch (e) { setError(e); }
  };
  return (
    <Dialog label="Make a key" onClose={onClose}>
      <form className="tg-form" onSubmit={(e) => { e.preventDefault(); void go(); }}>
        <h2>Make a key</h2>
        <label>{terms().say("{Environment}")} <select value={env || names[0] || ""} onChange={(e) => setEnv(e.target.value)}>{names.map((n) => <option key={n}>{n}</option>)}</select></label>
        <label>Kind <select value={kind} onChange={(e) => setKind(e.target.value)}><option value="server">server (a secret; reads rules)</option><option value="client">client (public; reads values)</option></select></label>
        <label>Name <input value={name} onChange={(e) => setName(e.target.value)} placeholder="what it is for" /></label>
        {kind === "client" && <label>Websites allowed to use it (addresses, separated by spaces) <input value={origins} onChange={(e) => setOrigins(e.target.value)} placeholder="https://shop.example.com" /></label>}
        <ErrorBox error={error} />
        <div className="dialog-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn-primary">Make the key</button></div>
      </form>
    </Dialog>
  );
}

function Guard({ project, env, g, onClose, onDone }: { project: string; env: string; g: Guards; onClose: () => void; onDone: () => void }) {
  const [reasonOn, setReasonOn] = useState(g.require_reason);
  const [approvalOn, setApprovalOn] = useState(g.require_approval);
  const [people, setPeople] = useState(g.approvals);
  const [watch, setWatch] = useState(g.watch_minutes);
  const [lock, setLock] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const relaxing = (g.require_reason && !reasonOn) || (g.require_approval && !approvalOn) || people < g.approvals || watch < g.watch_minutes;
  const go = async () => {
    setError(null);
    try { await togglesApi.setSafeguards(project, { environment: env, require_reason: reasonOn, require_approval: approvalOn, approvals: people, watch_minutes: watch, locked: lock ? ["require_reason", "require_approval", "approvals", "watch_minutes"] : undefined, reason }); onDone(); } catch (e) { setError(e); }
  };
  return (
    <Dialog label={`Safeguards of ${env}`} onClose={onClose}>
      <form className="tg-form" onSubmit={(e) => { e.preventDefault(); void go(); }}>
        <h2>Safeguards in {env}</h2>
        <label className="tg-check"><input type="checkbox" checked={reasonOn} onChange={(e) => setReasonOn(e.target.checked)} /> A reason is required for every change</label>
        <label className="tg-check"><input type="checkbox" checked={approvalOn} onChange={(e) => setApprovalOn(e.target.checked)} /> A change waits for another person's approval</label>
        <label>People who must approve <input type="number" min={1} max={10} value={people} onChange={(e) => setPeople(Number(e.target.value))} /></label>
        <label>{terms().say("Mark the {environment} as watched for this many minutes after a change")} <input type="number" min={0} max={1440} value={watch} onChange={(e) => setWatch(Number(e.target.value))} /></label>
        <label className="tg-check"><input type="checkbox" checked={lock} onChange={(e) => setLock(e.target.checked)} /> Lock these settings for everything below this level</label>
        <label>Why {relaxing ? "(required: you are turning a safeguard down)" : "(optional)"} <input value={reason} onChange={(e) => setReason(e.target.value)} required={relaxing} /></label>
        <ErrorBox error={error} />
        <div className="dialog-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn-primary">Save</button></div>
      </form>
    </Dialog>
  );
}
