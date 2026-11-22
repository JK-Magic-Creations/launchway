import { useId, useState } from "react";
import { forgeApi, type HostedRepo } from "../forgeApi";
import { Link } from "../components/ui";
import { Chip } from "../kit/Status";
import { Button, Sheet } from "../kit/layout";
import { announce } from "../kit/announce";
import { Blank, ErrorPanel, itemHref, Loading, money, scopeLabel, useLoad, when, WorkPage } from "./common";
import { workApi, type Agent, type AgentLogEntry, type SpendRow } from "./workApi";
import { terms } from "../terms";
import { prose } from "../prose";
import { DivFaintSmall, PSub, SpanFaint, SpanFaintSmall, TdNum, ThCol, ThRow } from "../kit/bits";
const P = await prose("work.Agents");

// The agent registry: who may work on items without a person, how much each may spend, a switch that stops one at once, and
// where the money went. A token is shown once, when it is made, and never again.

const scopeText = (a: Agent) => (a.scope.id ? `${a.scope.kind}:${a.scope.id}` : a.scope.kind);
const cents = (v: string) => (v.trim() === "" ? undefined : Math.round(Number(v) * 100));

export default function Agents() {
  const agents = useLoad<Agent[]>("agents", () => workApi.agents());
  const spend = useLoad<{ rows: SpendRow[]; note: string }>("spend", () => workApi.spend());
  const repos = useLoad<HostedRepo[]>("repos", () => forgeApi.repositories());
  const [adding, setAdding] = useState(false);
  const [token, setToken] = useState<{ agent: string; token: string; rotated: boolean } | null>(null);
  const [picked, setPicked] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const list = agents.data ?? [];
  const current = list.find((a) => a.name === picked);
  const refresh = () => { agents.reload(); spend.reload(); };

  return (
    <WorkPage title="Agents" eyebrow={<Link href="/work">Work</Link>} sentence={P.t0}
      actions={<Button kind="pri" icon="plus" aria-expanded={adding} onClick={() => setAdding(!adding)}>Register an agent</Button>}>
      {token && <TokenOnce t={token} onDone={() => setToken(null)} />}
      {adding && <Register repos={repos.data ?? []} onCancel={() => setAdding(false)} onMade={(agent, tok) => { setAdding(false); setToken({ agent, token: tok, rotated: false }); refresh(); }} />}
      <ErrorPanel error={error ?? agents.error} onRetry={agents.reload} />
      {agents.loading && <Loading what="agents" />}
      {agents.data && list.length === 0 && !adding && (
        <Blank title="No agents yet" action={<Button kind="pri" icon="plus" onClick={() => setAdding(true)}>Register an agent</Button>}>
          {P.t1}</Blank>
      )}
      {list.length > 0 && (
        <div className="sheet tablewrap">
          <table className="table wk-table">
            <caption className="visually-hidden">Registered agents</caption>
            <thead><tr><ThCol>Agent</ThCol><ThCol>State</ThCol><ThCol>Queue</ThCol><ThCol>Active</ThCol><ThCol>Today</ThCol><ThCol>In review</ThCol><ThCol>Spent today</ThCol></tr></thead>
            <tbody>
              {list.map((a) => (
                <tr key={a.id} className={picked === a.name ? "on" : ""}>
                  <ThRow><button type="button" className="wk-textbtn strong" aria-pressed={picked === a.name} onClick={() => setPicked(picked === a.name ? null : a.name)}>{a.name}</button>
                    <DivFaintSmall>{a.model || "model not set"}{a.class ? `, ${a.class} jobs` : ""}</DivFaintSmall></ThRow>
                  <td>{a.disabled ? <Chip tone="bad">Switched off</Chip> : <Chip tone="ok">On</Chip>}</td>
                  <TdNum>{a.queued}</TdNum><TdNum>{a.active}</TdNum><TdNum>{a.today}</TdNum><TdNum>{a.in_review}</TdNum>
                  <TdNum>{money(a.used_cents_day)}{a.budget_cents_day ? ` of ${money(a.budget_cents_day)}` : " (no limit)"}</TdNum>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {current && <AgentPanel key={current.name} a={current} onChanged={refresh} onToken={(t) => setToken({ agent: current.name, token: t, rotated: true })} onError={setError} />}
      <Sheet title="Spend" pad>
        <PSub>{spend.data?.note ? spend.data.note[0].toUpperCase() + spend.data.note.slice(1) + "." : ""}</PSub>
        <ErrorPanel error={spend.error} onRetry={spend.reload} />
        {spend.data && spend.data.rows.length === 0 && <p className="faint">Nothing has been spent yet.</p>}
        {spend.data && spend.data.rows.length > 0 && (
          <div className="tablewrap"><table className="table wk-table">
            <caption className="visually-hidden">{terms().say(P.t2)}</caption>
            <thead><tr><ThCol>Name</ThCol><ThCol>Kind</ThCol><ThCol>Budget</ThCol><ThCol>Spent</ThCol><ThCol>Remaining</ThCol><ThCol>Tokens</ThCol></tr></thead>
            <tbody>{spend.data.rows.map((r, i) => (
              <tr key={r.kind + r.name + i}>
                <ThRow>{r.kind === "item" ? <Link href={itemHref(r.name)}>{r.name}</Link> : r.name}</ThRow>
                <td>{r.kind}</td>
                <TdNum>{r.budget_cents ? money(r.budget_cents) : <SpanFaint>No limit</SpanFaint>}</TdNum>
                <TdNum>{money(r.spent_cents)}</TdNum>
                <TdNum>{r.budget_cents ? money(r.remaining_cents ?? 0) : <SpanFaint>Not limited</SpanFaint>}</TdNum>
                <TdNum>{r.spent_tokens.toLocaleString("en-US")}</TdNum>
              </tr>
            ))}</tbody>
          </table></div>
        )}
      </Sheet>
    </WorkPage>
  );
}

/** The token, shown once. It is not kept on the page after this is dismissed. */
function TokenOnce({ t, onDone }: { t: { agent: string; token: string; rotated: boolean }; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try { await navigator.clipboard.writeText(t.token); setCopied(true); announce("Token copied."); } catch { announce(P.t3, "assertive"); }
  };
  return (
    <section className="wk-token" role="alert" aria-label="New agent token">
      <h2>{t.rotated ? `New token for ${t.agent}` : `${t.agent} is registered`}</h2>
      <p><strong>{P.t4}</strong> {P.t5}{t.rotated && P.t6}</p>
      <div className="wk-token-box"><input id="wk-token-value" className="mono" readOnly value={t.token} aria-label="Token" onFocus={(e) => e.currentTarget.select()} /><Button onClick={copy} icon="copy">{copied ? "Copied" : "Copy token"}</Button></div>
      <Button kind="pri" onClick={onDone}>I have stored it</Button>
    </section>
  );
}

function Register({ repos, onMade, onCancel }: { repos: HostedRepo[]; onMade: (agent: string, token: string) => void; onCancel: () => void }) {
  const [f, setF] = useState({ name: "", description: "", scope: "installation", model: "", class: "medium", concurrency: "1", per_day: "", budget: "", tokens: "", merge: false });
  const [error, setError] = useState<unknown>(null);
  const ids = { n: useId(), d: useId(), s: useId(), m: useId(), c: useId(), cc: useId(), p: useId(), b: useId(), t: useId() };
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setF({ ...f, [k]: e.target.value });
  const go = async (e: React.FormEvent) => {
    e.preventDefault();
    const i = f.scope.indexOf(":");
    try {
      const r = await workApi.createAgent({
        name: f.name.trim(), description: f.description, scope: i < 0 ? { kind: f.scope } : { kind: f.scope.slice(0, i), id: f.scope.slice(i + 1) },
        model: f.model.trim(), class: f.class, concurrency: Number(f.concurrency) || 1, per_day: Number(f.per_day) || 0, may_merge: f.merge,
        budget_cents_day: cents(f.budget), budget_tokens_day: f.tokens ? Number(f.tokens) : undefined,
      });
      onMade(r.agent.name, r.token);
    } catch (x) { setError(x); }
  };
  return (
    <form className="sheet pad wk-new" onSubmit={go} aria-label="Register an agent">
      <h2 className="h-sm">Register an agent</h2>
      <div className="wk-grid">
        <div className="wk-f"><label htmlFor={ids.n}>Name</label><input id={ids.n} value={f.name} onChange={set("name")} required maxLength={60} placeholder="docs-agent" /></div>
        <div className="wk-f span"><label htmlFor={ids.d}>What it does</label><input id={ids.d} value={f.description} onChange={set("description")} maxLength={300} /></div>
        <div className="wk-f"><label htmlFor={ids.s}>Works in</label>
          <select id={ids.s} value={f.scope} onChange={set("scope")}><option value="installation">{terms().say("Everything (all {projects})")}</option>{repos.map((r) => <option key={r.id} value={"project:" + r.path}>{r.path}</option>)}</select></div>
        <div className="wk-f"><label htmlFor={ids.m}>Model it runs on</label><input id={ids.m} value={f.model} onChange={set("model")} placeholder="the model family" /></div>
        <div className="wk-f"><label htmlFor={ids.c}>Size of job it takes</label><select id={ids.c} value={f.class} onChange={set("class")}><option>small</option><option>medium</option><option>large</option></select></div>
        <div className="wk-f"><label htmlFor={ids.cc}>Items at once</label><input id={ids.cc} type="number" min="1" value={f.concurrency} onChange={set("concurrency")} /></div>
        <div className="wk-f"><label htmlFor={ids.p}>Items a day</label><input id={ids.p} type="number" min="0" value={f.per_day} onChange={set("per_day")} placeholder="no limit" /></div>
        <div className="wk-f"><label htmlFor={ids.b}>Budget a day (dollars)</label><input id={ids.b} type="number" min="0" step="0.01" value={f.budget} onChange={set("budget")} placeholder="no limit" /></div>
        <div className="wk-f"><label htmlFor={ids.t}>Tokens a day</label><input id={ids.t} type="number" min="0" value={f.tokens} onChange={set("tokens")} placeholder="no limit" /></div>
      </div>
      <label className="wk-check"><input type="checkbox" checked={f.merge} onChange={(e) => setF({ ...f, merge: e.target.checked })} />{terms().say(P.t7)}</label>
      <ErrorPanel error={error} />
      <div className="acts"><Button type="submit" kind="pri" disabled={!f.name.trim()}>Register and show token</Button><Button onClick={onCancel}>Cancel</Button></div>
    </form>
  );
}

function AgentPanel({ a, onChanged, onToken, onError }: { a: Agent; onChanged: () => void; onToken: (t: string) => void; onError: (e: unknown) => void }) {
  const acts = useLoad<AgentLogEntry[]>("actions|" + a.name, () => workApi.agentActions(a.name));
  const [confirm, setConfirm] = useState<"" | "off" | "rotate">("");
  const [f, setF] = useState({ model: a.model ?? "", class: a.class ?? "", budget: a.budget_cents_day ? String(a.budget_cents_day / 100) : "", tokens: a.budget_tokens_day ? String(a.budget_tokens_day) : "" });
  const ids = { m: useId(), c: useId(), b: useId(), t: useId(), i: useId() };
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setF({ ...f, [k]: e.target.value });
  const run = async (fn: () => Promise<void>) => { try { await fn(); onError(null); } catch (e) { onError(e); } };
  const toggle = () => run(async () => { await workApi.updateAgent(a.name, { disabled: !a.disabled }); setConfirm(""); announce(`${a.name} is switched ${a.disabled ? "on" : "off"}.`); onChanged(); });
  const rotate = () => run(async () => { const r = await workApi.rotateAgent(a.name); setConfirm(""); onToken(r.token); onChanged(); });
  const save = (e: React.FormEvent) => {
    e.preventDefault();
    void run(async () => {
      await workApi.updateAgent(a.name, { model: f.model, class: f.class || undefined, budget_cents_day: cents(f.budget) ?? 0, budget_tokens_day: f.tokens ? Number(f.tokens) : 0 });
      announce("Agent saved."); onChanged();
    });
  };
  return (
    <Sheet title={a.name} pad actions={a.disabled ? <Chip tone="bad">Switched off</Chip> : <Chip tone="ok">On</Chip>}>
      <PSub>{a.description || "No description."} Registered at {scopeLabel(scopeText(a))} by {a.owner}. {a.last_seen ? `Last seen ${when(a.last_seen)}.` : "Never seen yet."} Token {a.token_hint ? `ends ${a.token_hint}` : "hidden"}.</PSub>
      <div className="acts wk-agent-acts">
        <Button kind={a.disabled ? "ok" : "bad"} onClick={() => setConfirm("off")}>{a.disabled ? "Switch on" : "Switch off"}</Button>
        <Button onClick={() => setConfirm("rotate")}>Rotate token</Button>
      </div>
      {confirm === "off" && (
        <div className="callout callout-wait" role="alertdialog" aria-label="Confirm">
          <span>{a.disabled ? `Switch ${a.name} back on? It may claim work again.` : `Switch ${a.name} off? It stops at once and the work it holds is released.`}</span>
          <Button kind={a.disabled ? "ok" : "bad"} onClick={toggle}>{a.disabled ? "Yes, switch on" : "Yes, switch off"}</Button><Button onClick={() => setConfirm("")}>Cancel</Button>
        </div>
      )}
      {confirm === "rotate" && (
        <div className="callout callout-wait" role="alertdialog" aria-label="Confirm">
          <span>Make a new token for {a.name}{P.t8}</span>
          <Button kind="bad" onClick={rotate}>Yes, rotate</Button><Button onClick={() => setConfirm("")}>Cancel</Button>
        </div>
      )}
      <form className="wk-form" onSubmit={save}>
        <h3 className="h-sm">Class, model and budget</h3>
        <div className="wk-grid">
          <div className="wk-f"><label htmlFor={ids.m}>Model</label><input id={ids.m} value={f.model} onChange={set("model")} /></div>
          <div className="wk-f"><label htmlFor={ids.c}>Size of job it takes</label><select id={ids.c} value={f.class} onChange={set("class")}><option value="">Not set</option><option>small</option><option>medium</option><option>large</option></select></div>
          <div className="wk-f"><label htmlFor={ids.b}>Budget a day (dollars)</label><input id={ids.b} type="number" min="0" step="0.01" value={f.budget} onChange={set("budget")} placeholder="no limit" /></div>
          <div className="wk-f"><label htmlFor={ids.t}>Tokens a day</label><input id={ids.t} type="number" min="0" value={f.tokens} onChange={set("tokens")} placeholder="no limit" /></div>
        </div>
        <div><Button type="submit">Save agent</Button></div>
      </form>
      <h3 className="h-sm">Recent actions</h3>
      <ErrorPanel error={acts.error} onRetry={acts.reload} />
      {acts.data && acts.data.length === 0 && <p className="faint">{P.t9}</p>}
      {acts.data && acts.data.length > 0 && (
        <ol className="wk-timeline">{acts.data.slice(0, 20).map((x, i) => <li key={i}><strong>{x.action.replace(/_/g, " ")}</strong>{x.item && <> <Link href={itemHref(x.item)}>{x.item}</Link></>}{x.detail && <> {x.detail}</>} <SpanFaintSmall>{when(x.at)}</SpanFaintSmall></li>)}</ol>
      )}
    </Sheet>
  );
}
