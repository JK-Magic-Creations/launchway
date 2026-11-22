import { lazy, Suspense, useCallback, useState } from "react";
import { PageHead } from "../kit/layout";
import { Empty, ErrorBox } from "../components/ui";
import { CheckInput, SelectInput, TextInput } from "../components/ConfigForm";
import { Loading, useLoad } from "../work/common";
import { HOST_KINDS, hostKind } from "../hostKinds";
import { outsideApi, type Connection, type ConnectionTest } from "../outsideApi";

// Connections to outside Git hosts: a narrow service-account credential, kept as a reference to a secret, through
// which a Flight keeps its code on a host Launchway does not own. Add one, test what its token may do, remove it.

const HostMatrix = lazy(() => import("./HostMatrix"));

const KINDS = HOST_KINDS.map((k) => ({ value: k.value, label: k.name }));

export function ConnectionsPage() {
  const load = useCallback(() => outsideApi.connections(), []);
  const { data, error, loading, reload } = useLoad("connections", load);
  return (
    <div className="page page-wide">
      <PageHead eyebrow="Settings" title="Connections to Git hosts"
        sentence="Keep a Flight's code on a Git host you already use. A connection holds a narrow service-account token, only as the name of its secret; every call is audited." />
      <ErrorBox error={error} />
      {loading && !data && <Loading what="connections" />}
      {data && data.length === 0 && <Empty title="No connection yet">Add one below, then choose a repository on it when you connect a repository.</Empty>}
      {data && data.length > 0 && (
        <ul className="plain" aria-label="Connections">
          {data.map((c) => <ConnectionRow key={c.name} c={c} onRemoved={reload} />)}
        </ul>
      )}
      <AddConnection onAdded={reload} />
      <Capabilities />
    </div>
  );
}

function Capabilities() {
  const [open, setOpen] = useState(false);
  return (
    <section>
      <button type="button" className="btn" aria-expanded={open} onClick={() => setOpen(!open)}>What each host can do</button>
      {open && <Suspense fallback={null}><HostMatrix /></Suspense>}
    </section>
  );
}

function ConnectionRow({ c, onRemoved }: { c: Connection; onRemoved: () => void }) {
  const [test, setTest] = useState<ConnectionTest | null>(null);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [confirm, setConfirm] = useState(false);
  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what); setError(null);
    try { await fn(); } catch (e) { setError(e); } finally { setBusy(""); }
  };
  return (
    <li className="card" aria-label={`Connection ${c.name}`}>
      <h2 className="h-sm">{c.name} <span className="faint">{c.kind}{c.base_url ? ` at ${c.base_url}` : ""}</span></h2>
      <p className="small">Token read from <code>{c.credential}</code>{c.webhook_secret ? <>; webhook secret from <code>{c.webhook_secret}</code></> : "; no webhook secret yet, so the host cannot call Launchway and Flights are polled"}.
        {c.owners && c.owners.length > 0 ? <> Reaches only the owners {c.owners.join(", ")}.</> : null}</p>
      <div className="cf-row">
        <button type="button" className="btn" disabled={!!busy} onClick={() => run("test", async () => setTest(await outsideApi.test(c.name)))}>
          {busy === "test" ? "Testing…" : "Test the connection"}
        </button>
        {!confirm && <button type="button" className="btn" disabled={!!busy} onClick={() => setConfirm(true)}>Remove</button>}
        {confirm && (
          <>
            <span role="alert">Remove {c.name}? Flights on it must be detached first.</span>
            <button type="button" className="btn btn-danger" disabled={!!busy} onClick={() => run("remove", async () => { await outsideApi.remove(c.name); onRemoved(); })}>
              {busy === "remove" ? "Removing…" : `Yes, remove ${c.name}`}
            </button>
            <button type="button" className="btn" onClick={() => setConfirm(false)}>Keep it</button>
          </>
        )}
      </div>
      <ErrorBox error={error} />
      {test && <TestResult t={test} />}
    </li>
  );
}

/** TestResult says who the token is and, for each feature, whether it has the permission or lacks it, and what to grant. */
export function TestResult({ t }: { t: ConnectionTest }) {
  const r = t.report;
  return (
    <section aria-label={`Test of ${t.connection}`} className="outside-test">
      <p role="status">The token is <strong>{r.account}</strong>{r.token_expires ? <>, expiring {new Date(r.token_expires).toLocaleDateString()}</> : null}; it reaches {r.repositories} {r.repositories === 1 ? "repository" : "repositories"} on the first page.</p>
      <table className="table">
        <caption className="visually-hidden">What the token may do</caption>
        <thead><tr><th scope="col">Feature</th><th scope="col">Result</th><th scope="col">Permission it needs</th></tr></thead>
        <tbody>
          {r.checks.map((c) => (
            <tr key={c.feature}>
              <th scope="row">{c.feature}</th>
              <td>{c.ok ? "Has it" : "Lacks it"}{c.detail ? <span className="faint">: {c.detail}</span> : null}</td>
              <td>{c.needs}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

function AddConnection({ onAdded }: { onAdded: () => void }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState("github");
  const [base, setBase] = useState("");
  const [cred, setCred] = useState("");
  const [hook, setHook] = useState("");
  const [owners, setOwners] = useState("");
  const [self, setSelf] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState("");
  const hk = hostKind(kind);
  const needAddress = !!hk?.addressRequired;
  const showAddress = needAddress || self;
  const valid = name.trim() !== "" && cred.trim() !== "" && (!needAddress || base.trim() !== "");
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null); setDone("");
    try {
      await outsideApi.add({
        name: name.trim(), kind, base_url: showAddress && base.trim() ? base.trim() : undefined,
        credential: { env: cred.trim() }, webhook_secret: hook.trim() ? { env: hook.trim() } : undefined,
        owners: owners.split(",").map((o) => o.trim()).filter(Boolean),
      });
      setDone(`Added ${name.trim()}. Test it to see what its token may do.`);
      setName(""); setCred(""); setHook(""); setOwners(""); setBase(""); setSelf(false);
      onAdded();
    } catch (err) { setError(err); } finally { setBusy(false); }
  };
  return (
    <section aria-labelledby="add-conn">
      <h2 id="add-conn" className="h-sm">Add a connection</h2>
      <form onSubmit={submit} className="cf-grid">
        <TextInput label="Name" required value={name} onChange={setName} mono help="Letters, digits and hyphens." />
        <SelectInput label="Kind of host" value={kind} onChange={setKind} options={KINDS} />
        <TextInput label="Environment variable that holds the token" required value={cred} onChange={setCred} mono placeholder="ACME_GH_TOKEN"
          help="The name of a variable of the server, never the token itself. Use a service account." />
        <TextInput label="Environment variable that holds the webhook secret" value={hook} onChange={setHook} mono placeholder="ACME_GH_HOOK"
          help="Optional. Without it Flights are polled." />
        <TextInput label="Owners it may reach" value={owners} onChange={setOwners} mono placeholder="acme, acme-labs" help="Comma separated. Empty: every owner the token reaches." wide />
        {!needAddress && <CheckInput label="A self-managed install" checked={self} onChange={setSelf} help="Give its address below instead of the public service." />}
        {showAddress && <TextInput label="Address of the install" required={needAddress} value={base} onChange={setBase} mono placeholder="https://git.example.com" wide
          help="An internal address needs forge.mirror_allow." />}
        <div className="cf-row">
          <button type="submit" className="btn btn-primary" disabled={!valid || busy}>{busy ? "Adding…" : "Add the connection"}</button>
          {done && <span role="status" className="cf-ok">{done}</span>}
        </div>
      </form>
      <ErrorBox error={error} />
    </section>
  );
}
