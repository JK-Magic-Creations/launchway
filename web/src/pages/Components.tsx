import { useEffect, useState } from "react";
import { api, type ComponentSearch } from "../api";
import { ago, clock } from "../format";
import { navigate } from "../router";
import { Empty, ErrorBox, Link } from "../components/ui";
import { FindingsHowTo } from "../components/Security";

function params() { return new URLSearchParams(location.search); }

function refName(ref: string): string { return ref.replace(/^refs\/(heads|tags)\//, ""); }

// ComponentsPage finds the runs whose SBOMs list a component: which
// projects use a library, and at which version.
export function ComponentsPage() {
  const [asked, setAsked] = useState(() => ({ name: params().get("name") ?? "", version: params().get("version") ?? "" }));
  const [name, setName] = useState(asked.name);
  const [version, setVersion] = useState(asked.version);
  const [data, setData] = useState<ComponentSearch | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const on = () => {
      const a = { name: params().get("name") ?? "", version: params().get("version") ?? "" };
      setAsked(a); setName(a.name); setVersion(a.version);
    };
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  useEffect(() => {
    if (!asked.name) { setData(null); return; }
    let alive = true;
    setBusy(true);
    api.components(asked.name, asked.version || undefined)
      .then((d) => { if (alive) { setData(d); setError(null); } })
      .catch((e) => { if (alive) { setError(e); setData(null); } })
      .finally(() => alive && setBusy(false));
    return () => { alive = false; };
  }, [asked]);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const q = new URLSearchParams();
    if (name.trim()) q.set("name", name.trim());
    if (version.trim()) q.set("version", version.trim());
    navigate(`/images/components${q.toString() ? "?" + q.toString() : ""}`);
  };

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Components</h1>
          <p className="sub">Find the runs whose software bill of materials lists a component: which projects use it, at which version, on which branch.</p>
        </div>
      </header>

      <form className="card compform" role="search" onSubmit={submit}>
        <label className="field">
          Name
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="requests" required maxLength={300} autoFocus={!asked.name} />
        </label>
        <label className="field">
          <span>Version <span className="faint">(optional)</span></span>
          <input value={version} onChange={(e) => setVersion(e.target.value)} placeholder="any version" maxLength={200} />
        </label>
        <button type="submit" className="btn btn-primary" disabled={!name.trim() || busy}>{busy ? "Searching…" : "Search"}</button>
        <p className="sub small compform-note">The name must match exactly, ignoring case; newest runs first.</p>
      </form>

      <ErrorBox error={error} />
      {!asked.name && (
        <Empty title="Search the SBOMs your runs report">
          Type a component's name above, such as a library your projects depend on.
          <FindingsHowTo compact />
        </Empty>
      )}
      {data && data.hits.length === 0 && (
        <Empty title="Not found">
          No SBOM kept lists <strong>{asked.name}</strong>{asked.version ? <> at version <strong>{asked.version}</strong></> : null}. Names match exactly, ignoring case.
        </Empty>
      )}
      {data && data.hits.length > 0 && (
        <>
          <section className="card" aria-label="Projects">
            <h2 className="h-sm">{data.projects.length} project{data.projects.length === 1 ? " uses" : "s use"} {asked.name}{asked.version ? ` ${asked.version}` : ""}</h2>
            <ul className="compprojects">
              {data.projects.map((p) => (
                <li key={p.project}>
                  <strong>{p.project}</strong>
                  <span className="mono">{p.version || "no version"}</span>
                  <span className="faint">latest in <Link href={`/runs/${p.run_id}?tab=security`}>#{p.run_number}</Link> on {refName(p.ref)}, {ago(p.created_at)}</span>
                </li>
              ))}
            </ul>
          </section>
          <section aria-label="Runs">
            <div className="table-scroll card card-flush">
              <table className="inputs-table comptable">
                <thead><tr><th>Project</th><th>Run</th><th>Branch</th><th>Step</th><th>Name</th><th>Version</th><th>Licences</th><th>When</th></tr></thead>
                <tbody>
                  {data.hits.map((h, i) => (
                    <tr key={h.run_id + h.step + i}>
                      <td>{h.project}</td>
                      <td><Link href={`/runs/${h.run_id}?tab=security`}>#{h.run_number}</Link></td>
                      <td>{refName(h.ref)}</td>
                      <td>{h.step}</td>
                      <td className="mono" title={h.purl}>{h.name}</td>
                      <td className="mono">{h.version || "—"}</td>
                      <td>{h.licences || <span className="faint">none given</span>}</td>
                      <td title={clock(h.created_at)}>{ago(h.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {data.more && <p className="sub small">There are more results than shown: only the newest {data.hits.length} are listed. Give a version to narrow the search.</p>}
          </section>
        </>
      )}
    </div>
  );
}
