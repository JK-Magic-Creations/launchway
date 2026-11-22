import { useCallback } from "react";
import { ErrorBox } from "../components/ui";
import { Loading, useLoad } from "../work/common";
import { outsideApi } from "../outsideApi";

// What each host kind can do, as the server declares it (GET /api/v1/outside-host-matrix): nothing here names a kind.
// Loaded only when a person opens it on the connections page.
export default function HostMatrix() {
  const load = useCallback(() => outsideApi.matrix(), []);
  const { data: m, error, loading } = useLoad("host-matrix", load);
  return (
    <section aria-label="What each host can do">
      <ErrorBox error={error} />
      {loading && !m && <Loading what="the matrix" />}
      {m && (
        <>
          <table className="table" aria-label="Features by kind of host">
            <thead><tr><th scope="col">Feature</th>{m.kinds.map((k) => <th key={k.kind} scope="col">{k.label}</th>)}</tr></thead>
            <tbody>
              {m.features.map((f) => (
                <tr key={f.key}><th scope="row">{f.title}</th>{m.kinds.map((k) => <td key={k.kind}>{k.capabilities[f.key].support}</td>)}</tr>
              ))}
            </tbody>
          </table>
          {m.kinds.map((k) => (
            <details key={k.kind}>
              <summary>{k.label}: why, and what to do instead</summary>
              <ul>
                {m.features.map((f) => ({ f, d: k.capabilities[f.key] })).filter(({ d }) => d.support !== "native").map(({ f, d }) => (
                  <li key={f.key}><strong>{f.title}</strong> ({d.support}): {d.reason} Instead: {d.workaround}</li>
                ))}
              </ul>
            </details>
          ))}
        </>
      )}
    </section>
  );
}
