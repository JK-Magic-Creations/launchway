import { useEffect, useState } from "react";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { importApi, type Connection, type ImportItem, type SourceRequest } from "../mirrors";
import { prose } from "../prose";
const P = await prose("pages.ImportRepositories");

type Step = "source" | "preview" | "copy" | "done";
const STEPS: { id: Step; label: string }[] = [
  { id: "source", label: "Where from" }, { id: "preview", label: "What would be made" }, { id: "copy", label: "Copy and check" }, { id: "done", label: "Result" },
];

// ImportRepositoriesPage walks through getting repositories from another Git
// server into Launchway: choose the server, see what would be made (and what
// is refused and why), copy and check one repository at a time, then verify.
// Nothing on the other server changes, and running it again resumes.
export function ImportRepositoriesPage() {
  const [step, setStep] = useState<Step>("source");
  const [conns, setConns] = useState<Connection[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [conn, setConn] = useState("");
  const [owner, setOwner] = useState("");
  const [mirror, setMirror] = useState(true);
  const [plan, setPlan] = useState<ImportItem[]>([]);
  const [done, setDone] = useState<Record<string, ImportItem>>({});
  const [running, setRunning] = useState(false);

  const load = () => importApi.connections().then((c) => { setConns(c); if (!conn && c[0]) setConn(c[0].name); }).catch(setError);
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const src = (): SourceRequest => ({ connection: conn, owner: owner.trim() || undefined });

  const preview = async () => {
    setError(null);
    try { setPlan((await importApi.plan(src(), mirror)).items); setStep("preview"); } catch (e) { setError(e); }
  };
  const copy = async () => {
    setStep("copy"); setRunning(true); setError(null); setDone({});
    for (const it of plan.filter((p) => p.action !== "skip")) {
      try {
        const r = await importApi.one(src(), it.source, mirror);
        setDone((d) => ({ ...d, [it.source]: r.items[0] }));
      } catch (e) {
        setDone((d) => ({ ...d, [it.source]: { ...it, state: "failed", error: e instanceof Error ? e.message : String(e) } }));
      }
    }
    setRunning(false);
    setStep("done");
  };

  const idx = STEPS.findIndex((s) => s.id === step);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Import repositories</h1>
          <p className="sub">{P.t0}</p>
        </div>
      </header>
      <ol className="steps" aria-label="Steps">
        {STEPS.map((s, i) => <li key={s.id} className={i === idx ? "step-on" : i < idx ? "step-done" : ""} aria-current={i === idx ? "step" : undefined}>{s.label}</li>)}
      </ol>
      <ErrorBox error={error} />

      {step === "source" && (
        <section className="card">
          {conns && conns.length === 0 && <Empty title="No source servers yet">{P.t1}</Empty>}
          {conns && conns.length > 0 && (
            <form className="inputs-form" onSubmit={(e) => { e.preventDefault(); preview(); }} aria-label="Choose what to import">
              <label className="field"><span>Server</span>
                <select value={conn} onChange={(e) => setConn(e.target.value)}>{conns.map((c) => <option key={c.name} value={c.name}>{c.name} ({c.kind} {c.base_url})</option>)}</select></label>
              <label className="field"><span>{P.t2}</span>
                <input value={owner} onChange={(e) => setOwner(e.target.value)} /></label>
              <label className="check"><input type="checkbox" checked={mirror} onChange={(e) => setMirror(e.target.checked)} /> {P.t3}</label>
              <div><button type="submit" className="btn btn-primary" disabled={!conn}>Show what would be made</button></div>
            </form>
          )}
          <AddConnection onAdded={() => load()} />
        </section>
      )}

      {step === "preview" && (
        <section className="card">
          <h2 className="h-sm">{plan.length} repositories</h2>
          {plan.length === 0 && <Empty title="Nothing to import">{P.t4}</Empty>}
          <table className="table"><thead><tr><th>From</th><th>Here</th><th>Visibility</th><th>What happens</th></tr></thead><tbody>
            {plan.map((p) => (
              <tr key={p.source}><td><Mono>{p.source}</Mono></td><td><Mono>{p.repository}</Mono></td><td>{p.visibility}</td>
                <td>{p.action === "skip" ? <span className="muted">Left alone: {p.reason}</span> : p.reason}</td></tr>
            ))}
          </tbody></table>
          <div className="actions">
            <button type="button" className="btn" onClick={() => setStep("source")}>Back</button>
            <button type="button" className="btn btn-primary" disabled={!plan.some((p) => p.action !== "skip")} onClick={copy}>Import {plan.filter((p) => p.action !== "skip").length} repositories</button>
          </div>
          <p className="sub">{P.t5}</p>
        </section>
      )}

      {(step === "copy" || step === "done") && (
        <section className="card" aria-live="polite">
          <h2 className="h-sm">{running ? "Copying and checking…" : "Copied"}</h2>
          <ul className="plain">
            {plan.map((p) => {
              const r = done[p.source];
              return (
                <li key={p.source}><Mono>{p.repository}</Mono>{" "}
                  {p.action === "skip" ? <span className="muted">left alone</span>
                    : !r ? <span className="muted">waiting</span>
                    : r.state === "verified" ? <span>verified, {r.hosted_refs} refs equal</span>
                    : <span role="alert">{r.state}: {r.error}{r.differences?.length ? ` (${r.differences.join("; ")})` : ""}</span>}
                </li>
              );
            })}
          </ul>
        </section>
      )}

      {step === "done" && (
        <section className="card">
          <h2 className="h-sm">{plan.every((p) => p.action === "skip" || done[p.source]?.state === "verified") ? P.t6 : P.t7}</h2>
          <p className="sub">{P.t8}<Link href="/projects">Repositories</Link>{P.t9}</p>
          <div className="actions"><button type="button" className="btn" onClick={() => { setStep("source"); setPlan([]); }}>Import more</button></div>
        </section>
      )}
    </div>
  );
}

function AddConnection({ onAdded }: { onAdded: () => void }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState("gitea");
  const [base, setBase] = useState("");
  const [credKind, setCredKind] = useState<"env" | "file">("env");
  const [cred, setCred] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true); setError(null);
    try {
      await importApi.addConnection({ name: name.trim(), kind, base_url: base.trim() || undefined, credential: cred.trim() ? { [credKind]: cred.trim() } : undefined });
      setName(""); setBase(""); setCred(""); onAdded();
    } catch (err) { setError(err); } finally { setBusy(false); }
  };
  return (
    <details>
      <summary>Add a source server</summary>
      <form onSubmit={submit} className="inputs-form" aria-label="Add a source server">
        <label className="field"><span>Name</span><input value={name} required placeholder="oldgit" onChange={(e) => setName(e.target.value)} /></label>
        <label className="field"><span>Kind</span>
          <select value={kind} onChange={(e) => setKind(e.target.value)}><option value="gitea">Gitea-compatible</option><option value="forgejo">Forgejo</option></select></label>
        <label className="field"><span>Address</span><input value={base} required placeholder="https://git.example.com" onChange={(e) => setBase(e.target.value)} /></label>
        <label className="field"><span>{P.t10}</span>
          <select value={credKind} onChange={(e) => setCredKind(e.target.value as "env" | "file")}><option value="env">{P.t11}</option><option value="file">A file on the server</option></select></label>
        <label className="field"><span>{credKind === "env" ? "Variable name" : "File path"}</span>
          <input value={cred} placeholder={credKind === "env" ? "OLDGIT_TOKEN" : "/run/secrets/oldgit-token"} onChange={(e) => setCred(e.target.value)} />
          <small>{P.t12}</small></label>
        <p className="sub">{P.t13}<Mono>forge.mirror_allow</Mono>.</p>
        <ErrorBox error={error} />
        <div><button type="submit" className="btn" disabled={busy || !name.trim() || !base.trim()}>{busy ? "Adding…" : "Add server"}</button></div>
      </form>
    </details>
  );
}
