import { useCallback, useMemo, useState } from "react";
import { call } from "../api";
import { useMe, canOperate } from "../auth";
import { ToggleRules } from "../components/ToggleRules";
import { ErrorBox } from "../components/ui";
import { ago, clock } from "../format";
import { useLoad } from "../guided/useLoad";
import { Dialog } from "../kit/Dialog";
import { Chip } from "../kit/Status";
import { Button, EmptyState, LinkButton, Sheet } from "../kit/layout";
import { useNow } from "../live";
import { onLink } from "../router";
import { TOGGLE_KINDS, TOGGLE_TYPES, showValue, togglesApi, valueOf, type Change, type EnvView, type Toggle, type ToggleState, type ToggleType } from "../togglesApi";
import { ProjectHead } from "./PipelineChrome";
import "../toggles.css";

const enc = encodeURIComponent;

/** A change in a sentence. */
function sentence(c: Change): string {
  const where = c.environment ? ` in ${c.environment}` : "";
  switch (c.action) {
    case "state": return `${c.toggle} changed${where}`;
    case "create": return `${c.toggle} made`;
    case "update": return `${c.toggle}'s definition changed`;
    case "delete": return `${c.toggle} removed`;
    case "safeguards": return `safeguards changed${where}`;
    case "segment": return `segment ${c.toggle} changed`;
    case "key": return `an SDK key changed${where}`;
  }
  return `${c.toggle} ${c.action}${where}`;
}

function Mode({ e }: { e: EnvView }) {
  if (!e.configured) return <span className="tg-cell"><span className="tg-mode off">Off</span><span className="tg-meta">never set here</span></span>;
  return (
    <span className="tg-cell">
      <span className={`tg-mode ${e.mode}`}>{e.mode === "off" ? "Off" : e.mode === "on" ? "On" : e.mode === "rollout" ? "Rollout" : "Targeted"}</span>
      <span className="tg-meta">{e.serves}</span>
      {e.waiting > 0 && <span className="tg-note">{e.waiting} change{e.waiting === 1 ? "" : "s"} waiting for approval</span>}
    </span>
  );
}

/** A project's toggles: one row each, with what each does in every environment at a glance. */
export function ProjectTogglesPage({ name }: { name: string }) {
  const me = useMe();
  const fn = useCallback((bg: boolean) => togglesApi.list(name, bg), [name]);
  const list = useLoad(fn, 20000);
  const [making, setMaking] = useState(false);
  const can = !!me?.admin || canOperate(me, name);
  const data = list.data;
  const envs = data?.environments ?? [];
  const stale = (data?.toggles ?? []).filter((t) => t.stale.length > 0);
  return (
    <div className="page">
      <ProjectHead name={name} on="toggles" title={`Feature toggles of ${name}`}
        sentence="A value your application reads at run time, so that a feature can be turned on, off or rolled out to a share of users without a deployment."
        actions={<>{can && <Button kind="pri" icon="plus" onClick={() => setMaking(true)}>New toggle</Button>}<LinkButton href={`/projects/${enc(name)}/toggle-settings`} icon="settings">Keys and safeguards</LinkButton></>} />
      <ErrorBox error={list.error} />
      {(data?.waiting?.length ?? 0) > 0 && <Waiting project={name} changes={data!.waiting} onChanged={list.reload} />}
      <Sheet title="Toggles">
        {!data ? <div className="loading">Loading…</div> : data.toggles.length === 0 ? (
          <EmptyState title="No toggles yet" action={can ? <Button kind="pri" onClick={() => setMaking(true)}>Make the first toggle</Button> : undefined}>
            Make one here, then read it in your application with the Go or TypeScript SDK. Safeguards are on from the start: a reason for every change, and an approval in a protected environment.
          </EmptyState>
        ) : (
          <table className="tg-table">
            <caption className="sr-only">Toggles of {name} and what each does in each environment</caption>
            <thead><tr><th scope="col">Toggle</th>{envs.map((e) => <th scope="col" key={e}>{e}</th>)}</tr></thead>
            <tbody>
              {data.toggles.map((t) => (
                <tr key={t.key}>
                  <th scope="row">
                    <a className="tg-key" href={`/projects/${enc(name)}/toggles/${enc(t.key)}`} onClick={onLink}>{t.key}</a>
                    <span className="tg-meta">{t.type} · {t.kind}{t.owner ? ` · ${t.owner}` : ""}{t.inherited ? ` · shared from the ${t.level} level` : ""}</span>
                    {t.stale.length > 0 && <span className="tg-note">May be finished with: see its page</span>}
                  </th>
                  {envs.map((e) => { const v = t.environments.find((x) => x.environment === e); return <td key={e}>{v ? <Mode e={v} /> : <span className="tg-meta">off</span>}</td>; })}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Sheet>
      {stale.length > 0 && (
        <p className="tg-callout">{stale.length} toggle{stale.length === 1 ? "" : "s"} may have done {stale.length === 1 ? "its" : "their"} job: {stale.map((t) => t.key).join(", ")}. This is only a note: Launchway never removes or changes a toggle by itself.</p>
      )}
      {can && <FilePanel project={name} onApplied={list.reload} />}
      {making && <NewToggle project={name} onClose={() => setMaking(false)} onDone={() => { setMaking(false); list.reload(); }} />}
    </div>
  );
}

function NewToggle({ project, onClose, onDone }: { project: string; onClose: () => void; onDone: () => void }) {
  const [key, setKey] = useState("");
  const [type, setType] = useState<ToggleType>("boolean");
  const [kind, setKind] = useState("release");
  const [description, setDescription] = useState("");
  const [owner, setOwner] = useState("");
  const [expires, setExpires] = useState("");
  const [def, setDef] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const go = async () => {
    setBusy(true); setError(null);
    try {
      let d: unknown = undefined;
      if (def !== "") d = valueOf(type, def);
      await togglesApi.create(project, { key, type, kind, description, owner, expires: expires || undefined, default: d, reason });
      onDone();
    } catch (e) { setError(e); setBusy(false); }
  };
  return (
    <Dialog label="New toggle" onClose={onClose}>
      <form className="tg-form" onSubmit={(e) => { e.preventDefault(); void go(); }}>
        <h2>New toggle</h2>
        <label>Key <input value={key} onChange={(e) => setKey(e.target.value)} required data-autofocus placeholder="checkout-v2" pattern="[a-z][a-z0-9]*([\-_.][a-z0-9]+)*" title="lower-case letters and digits, with - _ or . between them" /></label>
        <label>Type <select value={type} onChange={(e) => setType(e.target.value as ToggleType)}>{TOGGLE_TYPES.map((t) => <option key={t}>{t}</option>)}</select></label>
        <label>Kind <select value={kind} onChange={(e) => setKind(e.target.value)}>{TOGGLE_KINDS.map((t) => <option key={t}>{t}</option>)}</select></label>
        <label>Value while off {type === "boolean" ? "(false when left empty)" : ""} <input value={def} onChange={(e) => setDef(e.target.value)} /></label>
        <label>What it is for <input value={description} onChange={(e) => setDescription(e.target.value)} /></label>
        <label>Owner <input value={owner} onChange={(e) => setOwner(e.target.value)} placeholder="a person or team who will remove it" /></label>
        <label>Expected removal date <input type="date" value={expires} onChange={(e) => setExpires(e.target.value)} /></label>
        <label>Why <input value={reason} onChange={(e) => setReason(e.target.value)} required /></label>
        <ErrorBox error={error} />
        <div className="dialog-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn-primary" disabled={busy}>{busy ? "Making…" : "Make the toggle"}</button></div>
      </form>
    </Dialog>
  );
}

/** Changes that wait for an approval: someone other than the person who asked decides. */
function Waiting({ project, changes, onChanged }: { project: string; changes: Change[]; onChanged: () => void }) {
  const me = useMe();
  const now = useNow(30000);
  const [error, setError] = useState<unknown>(null);
  const decide = async (id: number, what: "approve" | "reject" | "withdraw") => {
    setError(null);
    try { await togglesApi.decide(project, id, what, ""); onChanged(); } catch (e) { setError(e); }
  };
  return (
    <Sheet title="Waiting for approval">
      <ErrorBox error={error} />
      <ul className="tg-timeline">
        {changes.map((c) => (
          <li key={c.id}>
            <span><b>{sentence(c)}</b> by {c.actor} {ago(c.at, now)}: {c.reason}</span>
            <span className="faint small">{c.approvals.by.length} of {c.approvals.need} approval{c.approvals.need === 1 ? "" : "s"}</span>
            {c.actor === me?.user
              ? <Button sm onClick={() => decide(c.id, "withdraw")}>Withdraw</Button>
              : <><Button sm kind="ok" onClick={() => decide(c.id, "approve")}>Approve</Button><Button sm onClick={() => decide(c.id, "reject")}>Reject</Button></>}
            <a href={`/projects/${enc(project)}/toggles/${enc(c.toggle)}`} onClick={onLink}>See the toggle</a>
          </li>
        ))}
      </ul>
    </Sheet>
  );
}

function Why({ title, label, onClose, onGo, busy, error, children, ok }: { title: string; label: string; onClose: () => void; onGo: (reason: string) => void; busy: boolean; error: unknown; children?: React.ReactNode; ok: string }) {
  const [reason, setReason] = useState("");
  return (
    <Dialog label={title} onClose={onClose}>
      <form className="tg-form" onSubmit={(e) => { e.preventDefault(); onGo(reason.trim()); }}>
        <h2>{title}</h2>
        {children}
        <label>{label} <input value={reason} onChange={(e) => setReason(e.target.value)} required data-autofocus /></label>
        <ErrorBox error={error} />
        <div className="dialog-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn btn-primary" disabled={busy || reason.trim() === ""}>{busy ? "Working…" : ok}</button></div>
      </form>
    </Dialog>
  );
}

/** One toggle: what it does in each environment (and the editor), where it is evaluated, and its history. */
export function ToggleDetailPage({ project, toggle: key }: { project: string; toggle: string }) {
  const me = useMe();
  const now = useNow(30000);
  const fn = useCallback((bg: boolean) => togglesApi.get(project, key, bg), [project, key]);
  const t = useLoad(fn, 20000);
  const hfn = useCallback((bg: boolean) => togglesApi.history(project, key, bg), [project, key]);
  const hist = useLoad(hfn, 20000);
  const lfn = useCallback((bg: boolean) => togglesApi.list(project, bg), [project]);
  const all = useLoad(lfn);
  const ufn = useCallback(() => togglesApi.usage(project, key), [project, key]);
  const usage = useLoad(ufn);
  const [editing, setEditing] = useState<string | null>(null);
  const [reverting, setReverting] = useState<Change | null>(null);
  const [removing, setRemoving] = useState(false);
  const [killing, setKilling] = useState<string | null>(null);
  const can = !!me?.admin || canOperate(me, project);
  const tg = t.data;
  const others = useMemo(() => (all.data?.toggles ?? []).filter((x) => x.key !== key).map((x) => x.key), [all.data, key]);
  const reload = () => { t.reload(); hist.reload(); };
  if (!tg) return <div className="page"><ProjectHead name={project} on="toggles" title={key} /><ErrorBox error={t.error} />{t.loading && <div className="loading">Loading…</div>}</div>;
  return (
    <div className="page">
      <ProjectHead name={project} on="toggles" title={tg.key}
        sentence={tg.description || "No description yet."}
        actions={<LinkButton href={`/projects/${enc(project)}/toggles`} icon="arrow">All toggles</LinkButton>} />
      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
        <Chip>{tg.type}</Chip><Chip>{tg.kind}</Chip>
        {tg.owner && <Chip>owner {tg.owner}</Chip>}
        {tg.expires_at ? <Chip tone="warn">remove by {new Date(tg.expires_at).toISOString().slice(0, 10)}</Chip> : null}
        {tg.client_visible ? <Chip>visible to browsers and apps</Chip> : <Chip>server-side only</Chip>}
        {tg.inherited && <Chip>shared from the {tg.level} level</Chip>}
        {tg.lock && <Chip tone="warn">locked by {tg.lock.by}: {tg.lock.reason}</Chip>}
      </div>
      {tg.stale.map((n) => <p className="tg-callout" key={n.reason}><b>A note, not an action.</b> {n.detail}</p>)}
      <ErrorBox error={t.error} />
      {tg.environments.length === 0 && <Sheet title="Environments"><p className="sub">No environment is known to this project yet.</p></Sheet>}
      {tg.environments.map((e) => (
        <Sheet key={e.environment} title={e.environment} actions={can && editing !== e.environment && !(tg.inherited && !me?.admin) ? <>{tg.kind === "kill" && tg.safe_value !== null && <Button sm kind="bad" onClick={() => setKilling(e.environment)}>Switch to the safe value now</Button>}<Button sm onClick={() => setEditing(e.environment)}>Edit</Button></> : undefined}>
          <div className="tg-env">
            <Mode e={e} />
            {e.watch_until && e.watch_until > now ? <span className="tg-note">Watched until {clock(e.watch_until)}: a change was made recently.</span> : null}
            {e.updated_at ? <span className="tg-meta">Last changed {ago(e.updated_at, now)} by {e.updated_by}.</span> : null}
            {editing === e.environment
              ? <EnvEditor project={project} toggle={tg} env={e} others={others} onClose={() => setEditing(null)} onDone={() => { setEditing(null); reload(); }} />
              : (e.state.rules ?? []).length > 0 && <ol className="tg-quiet">{(e.state.rules ?? []).map((r) => <li key={r.id}>Rule {r.id}: {(r.clauses ?? []).map((c) => `${c.attribute ?? "segment"} ${c.negate ? "not " : ""}${c.op} ${(c.values ?? []).join(", ")}`).join(" and ") || "everyone"}</li>)}</ol>}
          </div>
        </Sheet>
      ))}
      <Sheet title="Where it is evaluated">
        {usage.data?.usage?.length ? (
          <ul className="tg-timeline">{usage.data.usage.map((u) => <li key={u.environment}><b>{u.environment}</b> <span>{u.evaluations.toLocaleString()} evaluations in the last 30 days, last on {u.last_evaluated}</span><span className="faint small">{Object.entries(u.variations).map(([v, n]) => `${v}: ${n}`).join(" · ")}</span></li>)}</ul>
        ) : <p className="sub">No application has reported evaluating it yet. Server SDKs report counts every minute.</p>}
        <p className="tg-callout">{usage.data?.privacy ?? "Counts only: nothing about who was evaluated is stored."}</p>
      </Sheet>
      <Sheet title="History">
        <ErrorBox error={hist.error} />
        {(hist.data?.changes ?? []).length === 0 ? <p className="sub">Nothing yet.</p> : (
          <ol className="tg-history">
            {hist.data!.changes.map((c) => (
              <li key={c.id}>
                <span><b>{sentence(c)}</b>: {c.reason || "no reason given"}</span>
                <span className="tg-when">{clock(c.at)} · {c.actor} · via {c.via}{c.status !== "applied" ? ` · ${c.status}` : ""}{c.reverts ? ` · reverts #${c.reverts}` : ""}{c.reverted_by ? ` · reverted by #${c.reverted_by}` : ""}</span>
                {can && c.status === "applied" && !c.reverted_by && !c.reverts && ["state", "update", "delete"].includes(c.action) && <Button sm onClick={() => setReverting(c)}>Revert</Button>}
              </li>
            ))}
          </ol>
        )}
      </Sheet>
      {can && !tg.inherited && <Sheet title="Remove this toggle"><p className="sub">Remove the check from your code first. The removal is kept in the history and can be reverted.</p><Button kind="bad" onClick={() => setRemoving(true)}>Remove {tg.key}…</Button></Sheet>}
      {killing && <Killer project={project} toggle={tg} env={killing} onClose={() => setKilling(null)} onDone={() => { setKilling(null); reload(); }} />}
      {reverting && <Reverter project={project} change={reverting} onClose={() => setReverting(null)} onDone={() => { setReverting(null); reload(); }} />}
      {removing && <Remover project={project} toggle={tg.key} onClose={() => setRemoving(false)} />}
    </div>
  );
}

/** A kill switch's prominent control: serve the safe value to everyone, now. It is never held for an approval; it still says why. */
function Killer({ project, toggle, env, onClose, onDone }: { project: string; toggle: Toggle; env: string; onClose: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const go = async (reason: string) => {
    setBusy(true); setError(null);
    try {
      await togglesApi.setState(project, toggle.key, env, { state: { on: true, off_value: toggle.safe_value, fallthrough: { value: toggle.safe_value } }, reason });
      onDone();
    } catch (e) { setError(e); setBusy(false); }
  };
  return (
    <Why title={`Switch ${toggle.key} to its safe value in ${env}`} label="Why" onClose={onClose} onGo={go} busy={busy} error={error} ok="Switch it now">
      <p>Everyone in {env} gets <b>{showValue(toggle.safe_value)}</b> at once, whatever the rules said. This is never held for an approval, and is recorded. Revert it from the history when the trouble is over.</p>
    </Why>
  );
}

function Reverter({ project, change, onClose, onDone }: { project: string; change: Change; onClose: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [also, setAlso] = useState(0);
  const go = async (reason: string) => {
    setBusy(true); setError(null);
    try { const o = await togglesApi.revert(project, change.id, reason); setAlso(o.also_undone ?? 0); onDone(); } catch (e) { setError(e); setBusy(false); }
  };
  return (
    <Why title={`Revert change #${change.id}`} label="Why" onClose={onClose} onGo={go} busy={busy} error={error} ok="Revert">
      <p>{sentence(change)} by {change.actor}. The environment goes back to what it was before. A revert is never held for approval, and is recorded like any other change.{also > 0 ? ` It also undid ${also} later change(s).` : ""}</p>
    </Why>
  );
}

function Remover({ project, toggle, onClose }: { project: string; toggle: string; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const go = async (reason: string) => {
    setBusy(true); setError(null);
    try { await togglesApi.remove(project, toggle, reason); window.location.assign(`/projects/${enc(project)}/toggles`); } catch (e) { setError(e); setBusy(false); }
  };
  return <Why title={`Remove ${toggle}`} label="Why it is removed" onClose={onClose} onGo={go} busy={busy} error={error} ok="Remove it" />;
}

/** The editor for one environment: the rules, a reason, and when. What the server answers is said as it is: made, waiting or scheduled. */
function EnvEditor({ project, toggle, env, others, onClose, onDone }: { project: string; toggle: Toggle; env: EnvView; others: string[]; onClose: () => void; onDone: () => void }) {
  const [state, setState] = useState<ToggleState>(() => JSON.parse(JSON.stringify(env.state)) as ToggleState);
  const [reason, setReason] = useState("");
  const [at, setAt] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [said, setSaid] = useState("");
  const segs = useLoad(useCallback(() => segmentNames(project), [project]));
  const save = async () => {
    setBusy(true); setError(null);
    try {
      const out = await togglesApi.setState(project, toggle.key, env.environment, { state, reason: reason.trim(), at: at ? new Date(at).toISOString() : undefined });
      if ("scheduled" in out) { setSaid(`Scheduled for ${clock(out.scheduled.run_at)}.`); }
      else if (out.waiting) { setSaid(`Not made yet: it needs ${out.needs}. Change #${out.change.id} waits for someone else to approve it.`); }
      else { onDone(); return; }
      setBusy(false);
    } catch (e) { setError(e); setBusy(false); }
  };
  return (
    <form className="tg-form" onSubmit={(e) => { e.preventDefault(); void save(); }}>
      <ToggleRules type={toggle.type} state={state} onChange={setState} segments={segs.data ?? []} otherToggles={others} />
      <label>Why (recorded with the change) <input value={reason} onChange={(e) => setReason(e.target.value)} required={true} /></label>
      <label>Make it at a later time (optional) <input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} /></label>
      <ErrorBox error={error} />
      {said && <p className="tg-callout" role="status">{said}</p>}
      <div className="row" style={{ gap: 8 }}>
        <button type="submit" className="btn btn-primary" disabled={busy}>{busy ? "Saving…" : at ? "Schedule the change" : "Save the change"}</button>
        <button type="button" className="btn" onClick={said ? onDone : onClose}>{said ? "Close" : "Cancel"}</button>
      </div>
    </form>
  );
}

async function segmentNames(project: string): Promise<string[]> {
  const r = await call<{ segments: { name: string }[] }>("GET", `/api/v1/projects/${enc(project)}/toggle-segments`);
  return (r.segments ?? []).map((s) => s.name);
}

interface Drift { kind: "add" | "change" | "only_live"; what: string; toggle?: string; environment?: string; detail: string }

/** The toggles file: the project's toggles as text for a pull request, and how a file in the repository differs from
 * what is live. Comparing changes nothing; applying makes the live toggles match the file, through the same
 * safeguards as any change, and never removes what the file leaves out. */
function FilePanel({ project, onApplied }: { project: string; onApplied: () => void }) {
  const [text, setText] = useState("");
  const [drift, setDrift] = useState<Drift[] | null>(null);
  const [reason, setReason] = useState("");
  const [said, setSaid] = useState("");
  const [error, setError] = useState<unknown>(null);
  const run = async (fn: () => Promise<void>) => { setError(null); setSaid(""); try { await fn(); } catch (e) { setError(e); } };
  const base = `/api/v1/projects/${enc(project)}`;
  return (
    <Sheet title="The toggles file">
      <p className="sub">Keep the toggles in a file in your repository (<code>.launchway/toggles.yaml</code>) and change them in a pull request. Launchway shows how the file and the live toggles differ; it applies nothing by itself, and never removes a toggle the file leaves out.</p>
      <ErrorBox error={error} />
      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
        <Button sm onClick={() => run(async () => { const r = await call<{ file: string }>("GET", `${base}/toggle-file`); setText(r.file); setDrift(null); })}>Write the live toggles as a file</Button>
      </div>
      <label className="tg-field">The file<textarea rows={10} value={text} onChange={(e) => { setText(e.target.value); setDrift(null); }} spellCheck={false} className="mono" /></label>
      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
        <Button sm disabled={text.trim() === ""} onClick={() => run(async () => { const r = await call<{ drift: Drift[] }>("POST", `${base}/toggle-plan`, { file: text }); setDrift(r.drift ?? []); })}>Show the differences</Button>
      </div>
      {drift && (drift.length === 0 ? <p className="tg-callout" role="status">The file and the live toggles agree.</p> : (
        <>
          <ul className="tg-timeline" aria-label="Differences between the file and the live toggles">
            {drift.map((d, i) => <li key={i}><span className="tg-kind">{d.kind === "only_live" ? "only live" : d.kind}</span><span>{d.toggle ? <b>{d.toggle}</b> : null}{d.environment ? ` in ${d.environment}` : ""} {d.detail}</span></li>)}
          </ul>
          <form className="row" style={{ gap: 8, flexWrap: "wrap" }} onSubmit={(e) => { e.preventDefault(); void run(async () => {
            const r = await call<{ made: string[]; waiting: string[]; failed: string[] }>("POST", `${base}/toggle-apply`, { file: text, reason: reason.trim() });
            setSaid(`Made ${r.made.length}; ${r.waiting.length} waiting for approval; ${r.failed.length} could not be made${r.failed.length ? ": " + r.failed.join("; ") : ""}.`); setDrift(null); onApplied();
          }); }}>
            <label className="tg-field">Why the file is applied <input value={reason} onChange={(e) => setReason(e.target.value)} required /></label>
            <button type="submit" className="btn" disabled={reason.trim() === ""}>Apply the file</button>
          </form>
        </>
      ))}
      {said && <p className="tg-callout" role="status">{said}</p>}
    </Sheet>
  );
}
