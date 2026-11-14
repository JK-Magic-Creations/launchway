import { useEffect, useState } from "react";
import { api, type Runner } from "../api";
import { ago } from "../format";
import { useNow } from "../live";
import { Empty, ErrorBox, Mono } from "../components/ui";

export function RunnersPage() {
  const [runners, setRunners] = useState<Runner[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const now = useNow(2000);
  useEffect(() => {
    const load = (background: boolean) => api.runners(background).then(setRunners).catch(setError);
    load(false);
    const t = setInterval(() => load(true), 3000);
    return () => clearInterval(t);
  }, []);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Runners</h1>
          <p className="sub">Machines that run steps. A runner only ever connects out to the server, so it can sit in any network.</p>
        </div>
      </header>
      <ErrorBox error={error} />
      {runners && runners.length === 0 && <Empty title="No runners have connected yet" />}
      {runners && runners.length > 0 && (
        <div className="card card-flush">
          <table className="table">
            <thead><tr><th>Runner</th><th>Executor</th><th>Version</th><th>Last seen</th></tr></thead>
            <tbody>
              {runners.map((r) => {
                const online = now - r.last_seen < 60_000;
                return (
                  <tr key={r.id}>
                    <td><span className={"online " + (online ? "online-on" : "")} aria-label={online ? "online" : "offline"} />{r.name}<div className="faint small mono">{r.id}</div></td>
                    <td>{r.executor}</td>
                    <td><Mono>{r.version}</Mono></td>
                    <td>{online ? "connected" : ago(r.last_seen, now)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <section className="card">
        <h2 className="h-sm">Add a runner on another machine</h2>
        <p className="sub">Copy the <Mono>launchway</Mono> binary to the machine, then run it with this server's address and the token in <Mono>launchway-data/runner-token</Mono>:</p>
        <pre className="code">{`launchway runner --server ${location.origin} --token <token> --name build-2`}</pre>
      </section>
    </div>
  );
}
