import { useEffect, useRef, useState } from "react";
import { ApiError } from "../../api";
import { actionOf, guideApi, type GuideStep } from "../../guideApi";
import { importApi, type Connection, type ImportItem, type SourceRequest } from "../../mirrors";
import { Mono } from "../../components/ui";
import { Unavailable, Wingman } from "./Frame";

type Phase = "choose" | "listing" | "pick" | "running" | "done";

const msg = (e: unknown) => (e instanceof ApiError ? e.message : e instanceof Error ? e.message : "Launchway could not be reached. Try again shortly.");

/** Step 7: bring repositories in from another Git server. A connection (address and a token, pasted once where this server can keep it,
 * or named as a variable), the list found, a choice of all or some, then each one copied and checked ref by ref with a retry for failures. */
export function ImportStep({ step, onChange }: { step: GuideStep; onChange: () => void }) {
  const add = actionOf(step, "add_connection");
  const paste = actionOf(step, "paste_token");
  const [conns, setConns] = useState<Connection[] | null>(null);
  const [conn, setConn] = useState("");
  const [owner, setOwner] = useState("");
  const [mirror, setMirror] = useState(false);
  const [phase, setPhase] = useState<Phase>("choose");
  const [error, setError] = useState("");
  const [plan, setPlan] = useState<ImportItem[]>([]);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [done, setDone] = useState<Record<string, ImportItem>>({});
  const stop = useRef(false);

  const load = () => importApi.connections().then((all) => { const c = all.filter((x) => x.kind === "gitea" || x.kind === "forgejo"); setConns(c); setConn((cur) => cur || c[0]?.name || ""); }).catch((e) => { setError(msg(e)); setConns([]); });
  useEffect(() => { if (add?.available) void load(); else setConns([]); }, [add?.available]);

  const src = (): SourceRequest => ({ connection: conn, owner: owner.trim() || undefined });
  const list = async () => {
    setError(""); setPhase("listing");
    try {
      const r = await importApi.plan(src(), mirror);
      setPlan(r.items);
      setPicked(new Set(r.items.filter((i) => i.action !== "skip").map((i) => i.source)));
      setPhase("pick");
    } catch (e) { setError(msg(e)); setPhase("choose"); }
  };
  const run = async (sources: string[]) => {
    stop.current = false; setPhase("running"); setError("");
    for (const s of sources) {
      if (stop.current) break;
      const item = plan.find((p) => p.source === s)!;
      setDone((d) => ({ ...d, [s]: { ...item, state: "queued" } }));
      try {
        const r = await importApi.one(src(), s, mirror);
        setDone((d) => ({ ...d, [s]: r.items[0] }));
      } catch (e) { setDone((d) => ({ ...d, [s]: { ...item, state: "failed", error: msg(e) } })); }
    }
    setPhase("done"); onChange();
  };
  const todo = plan.filter((p) => p.action !== "skip" && picked.has(p.source));
  const failed = plan.filter((p) => done[p.source] && done[p.source].state !== "verified");
  const verified = plan.filter((p) => done[p.source]?.state === "verified").length;
  const toggle = (s: string) => setPicked((cur) => { const n = new Set(cur); if (n.has(s)) n.delete(s); else n.add(s); return n; });
  const selectable = plan.filter((p) => p.action !== "skip");

  return (
    <>
      <p>{step.summary}</p>
      <Wingman learn={{ label: "Learn more about importing and mirroring", slug: "reference/cli", anchor: "launchway-repos-import-verify-and-connection" }}>
        I copy every branch, tag and note from the other server and compare them, ref by ref, with the source. Nothing on the other server changes. Keep each repository a mirror while people move over, or take it
        over at once; the same thing for a whole server is <code>launchway repos import-all --connection NAME</code>.
      </Wingman>
      <Unavailable action={add} />
      {add?.available && phase === "choose" && (
        <>
          {conns && conns.length > 0 && (
            <form className="gd-form" onSubmit={(e) => { e.preventDefault(); void list(); }} aria-label="Choose what to import">
              <label className="field"><span>Server to import from</span>
                <select value={conn} onChange={(e) => setConn(e.target.value)}>{conns.map((c) => <option key={c.name} value={c.name}>{c.name} ({c.kind} {c.base_url})</option>)}</select></label>
              <label className="field"><span>Only this user or organization (empty: everything the token can see)</span><input value={owner} onChange={(e) => setOwner(e.target.value)} /></label>
              <label className="check"><input type="checkbox" checked={mirror} onChange={(e) => setMirror(e.target.checked)} /> Keep each one a pull mirror of its source (the staged move), instead of taking it over now</label>
              <div><button type="submit" className="btn btn-primary" disabled={!conn}>Find the repositories</button></div>
            </form>
          )}
          <AddConnection pasteAction={paste} first={conns?.length === 0} onAdded={(n) => { setConn(n); void load(); onChange(); }} />
        </>
      )}
      {phase === "listing" && <p className="loading" role="status">Looking at the other server…</p>}
      {(phase === "pick" || phase === "running" || phase === "done") && (
        <section aria-label="Repositories found">
          <h3 className="h-sm">{plan.length} {plan.length === 1 ? "repository" : "repositories"} found</h3>
          {phase === "pick" && (
            <div className="gd-choices">
              <button type="button" className="btn" onClick={() => setPicked(new Set(selectable.map((p) => p.source)))}>Select all</button>
              <button type="button" className="btn" onClick={() => setPicked(new Set())}>Select none</button>
            </div>
          )}
          <ul className="plain gd-list">
            {plan.map((p) => {
              const r = done[p.source];
              return (
                <li key={p.source}>
                  {phase === "pick" && p.action !== "skip" && <input type="checkbox" id={"pick-" + p.source} checked={picked.has(p.source)} onChange={() => toggle(p.source)} aria-label={`Import ${p.source}`} />}
                  {phase === "pick" && p.action !== "skip" ? <label htmlFor={"pick-" + p.source}><Mono>{p.source}</Mono></label> : <Mono>{p.source}</Mono>}
                  <span className="faint"> {p.action === "skip" ? `left alone: ${p.reason}` : p.visibility}</span>
                  {r && (
                    <span role={r.state === "failed" ? "alert" : undefined}> {r.state === "verified" ? <strong>verified: {r.hosted_refs} of {r.source_refs} refs equal</strong>
                      : r.state === "queued" ? "copying and checking…" : <>{r.state}: {r.error}{r.differences?.length ? ` (${r.differences.join("; ")})` : ""}</>}</span>
                  )}
                </li>
              );
            })}
          </ul>
          {phase === "pick" && (
            <div className="gd-choices">
              <button type="button" className="btn btn-primary" disabled={todo.length === 0} onClick={() => void run(todo.map((t) => t.source))}>Import {todo.length} {todo.length === 1 ? "repository" : "repositories"}</button>
              <button type="button" className="btn" onClick={() => setPhase("choose")}>Back</button>
            </div>
          )}
          {phase === "running" && <div className="gd-choices"><span role="status">Copying and checking, one at a time…</span><button type="button" className="btn" onClick={() => { stop.current = true; }}>Stop after this one</button></div>}
          {phase === "done" && (
            <div aria-live="polite">
              <div className={"callout " + (failed.length ? "callout-bad" : "callout-ok")}>
                {failed.length === 0 ? `All ${verified} imported repositories were compared with their source, ref by ref, and are equal.` : `${verified} verified. ${failed.length} did not finish.`}
              </div>
              <div className="gd-choices">
                {failed.length > 0 && <button type="button" className="btn btn-primary" onClick={() => void run(failed.map((f) => f.source))}>Try the {failed.length} that failed again</button>}
                <button type="button" className="btn" onClick={() => { setPhase("choose"); setPlan([]); setDone({}); }}>Import more</button>
              </div>
            </div>
          )}
        </section>
      )}
      {error && <div className="callout callout-bad" role="alert">{error}</div>}
    </>
  );
}

function AddConnection({ pasteAction, first, onAdded }: { pasteAction: ReturnType<typeof actionOf>; first: boolean; onAdded: (name: string) => void }) {
  const canPaste = !!pasteAction?.available;
  const [name, setName] = useState("");
  const [kind, setKind] = useState("gitea");
  const [base, setBase] = useState("");
  const [how, setHow] = useState<"paste" | "env">(canPaste ? "paste" : "env");
  const [token, setToken] = useState("");
  const [env, setEnv] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true); setErr("");
    try {
      if (how === "paste") await guideApi.addConnectionWithToken({ name: name.trim(), kind, base_url: base.trim(), token });
      else await importApi.addConnection({ name: name.trim(), kind, base_url: base.trim(), credential: env.trim() ? { env: env.trim() } : undefined });
      onAdded(name.trim()); setToken(""); setName(""); setBase(""); setEnv("");
    } catch (x) { setErr(msg(x)); } finally { setBusy(false); }
  };
  return (
    <details open={first}>
      <summary>Add a server to import from</summary>
      <form className="gd-form" onSubmit={submit} aria-label="Add a server to import from">
        <label className="field"><span>A name for it</span><input value={name} onChange={(e) => setName(e.target.value)} placeholder="oldgit" required /></label>
        <label className="field"><span>What kind of server</span><select value={kind} onChange={(e) => setKind(e.target.value)}><option value="gitea">Gitea</option><option value="forgejo">Forgejo</option></select></label>
        <label className="field"><span>Its address</span><input value={base} onChange={(e) => setBase(e.target.value)} placeholder="https://git.example.com" required /></label>
        <fieldset className="field"><legend>The access token</legend>
          <label className="check"><input type="radio" name="gd-how" checked={how === "paste"} disabled={!canPaste} onChange={() => setHow("paste")} /> Paste it now, once (kept in a file only this server can read; never shown again)</label>
          {!canPaste && <p className="faint">Not available here: {pasteAction?.reason}</p>}
          <label className="check"><input type="radio" name="gd-how" checked={how === "env"} onChange={() => setHow("env")} /> Name the environment variable of the server that holds it</label>
        </fieldset>
        {how === "paste" && canPaste && <label className="field"><span>The token</span><input type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" required /></label>}
        {how === "env" && <label className="field"><span>Variable name</span><input value={env} onChange={(e) => setEnv(e.target.value)} placeholder="OLDGIT_TOKEN" /></label>}
        <p className="faint">An address on a private network is refused until an administrator lists it in <code>forge.mirror_allow</code>; the refusal says so.</p>
        {err && <div className="callout callout-bad" role="alert">{err}</div>}
        <div><button type="submit" className="btn" disabled={busy || !name.trim() || !base.trim()}>{busy ? "Adding…" : "Add server"}</button></div>
      </form>
    </details>
  );
}
