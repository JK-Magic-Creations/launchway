import { useCallback, useState } from "react";
import { api, type Project } from "../api";
import { ErrorBox } from "../components/ui";
import { useLoad } from "../guided/useLoad";
import { Button, Sheet } from "../kit/layout";
import { sourcesApi, type EventSource, type SourceTest } from "../eventsApi";
import { terms } from "../terms";

/** Mapping lines are written one to a line as input=expression. */
function parseMapping(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const at = line.indexOf("=");
    if (at > 0 && line.slice(at + 1).trim()) out[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }
  return out;
}

/** The incoming webhook sources of one project: make one (its secret is shown once), try a body against it,
 * turn it off, replace its secret or remove it. */
export default function EventSources() {
  const projects = useLoad<Project[]>(useCallback(() => api.projects(), []));
  const [project, setProject] = useState("");
  const chosen = project || projects.data?.[0]?.name || "";
  const list = useLoad<EventSource[]>(useCallback(() => (chosen ? sourcesApi.list(chosen) : Promise.resolve([])), [chosen]));
  const [err, setErr] = useState<unknown>(null);
  const [secret, setSecret] = useState<{ name: string; value: string } | null>(null);
  const [form, setForm] = useState({ name: "", mapping: "", filter: "", dedupe: "", debounce: "" });
  const [body, setBody] = useState<Record<string, string>>({});
  const [tested, setTested] = useState<Record<string, SourceTest>>({});
  const run = (p: Promise<unknown>) => p.then(() => { setErr(null); list.reload(); }).catch(setErr);

  const create = (e: React.FormEvent) => {
    e.preventDefault();
    sourcesApi.create(chosen, { name: form.name, mapping: parseMapping(form.mapping), filter: form.filter, dedupe_key: form.dedupe, debounce_seconds: Number(form.debounce) || 0 })
      .then((r) => { setSecret({ name: r.source.name, value: r.secret }); setForm({ name: "", mapping: "", filter: "", dedupe: "", debounce: "" }); setErr(null); list.reload(); }).catch(setErr);
  };
  const test = (s: EventSource) => {
    let parsed: unknown;
    try { parsed = JSON.parse(body[s.name] ?? ""); } catch { setErr(new Error("The body to try must be JSON.")); return; }
    sourcesApi.test(chosen, s.name, parsed).then((r) => { setErr(null); setTested({ ...tested, [s.name]: r }); }).catch(setErr);
  };

  return (
    <Sheet title="Event sources" actions={<span className="faint small">{terms().say("An address other systems call, signed, to start a {run}.")}</span>}>
      <label className="field"><span>Project</span>
        <select value={chosen} onChange={(e) => setProject(e.target.value)}>{(projects.data ?? []).map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}</select>
      </label>
      <ErrorBox error={err ?? list.error} />
      {secret && (
        <div role="status" className="card">
          <p><b>Signing secret for {secret.name}</b>, shown only now. Keep it in your secret store.</p>
          <p className="mono">{secret.value}</p>
          <p className="faint small">Sign each call with HMAC-SHA256 over &quot;timestamp.body&quot;; send X-Launchway-Timestamp and X-Launchway-Signature: sha256=hex.</p>
          <Button sm onClick={() => setSecret(null)}>I have saved it</Button>
        </div>
      )}
      <ul className="sh-b">
        {(list.data ?? []).map((s) => (
          <li key={s.id}>
            <b>{s.name}</b>{!s.enabled && " (off)"} <span className="mono small">{s.url}</span>
            <div className="small">{Object.entries(s.mapping).map(([k, v]) => <div key={k} className="mono">{k} = {v}</div>)}{s.filter && <div className="mono">filter: {s.filter}</div>}</div>
            <label className="field"><span>Try a body for {s.name}</span>
              <textarea rows={3} value={body[s.name] ?? ""} onChange={(e) => setBody({ ...body, [s.name]: e.target.value })} />
            </label>
            <Button sm onClick={() => test(s)} aria-label={`Try the body on ${s.name}`}>Try it</Button>{" "}
            <Button sm onClick={() => run(sourcesApi.setEnabled(chosen, s.name, !s.enabled))} aria-label={`${s.enabled ? "Turn off" : "Turn on"} ${s.name}`}>{s.enabled ? "Turn off" : "Turn on"}</Button>{" "}
            <Button sm onClick={() => sourcesApi.rotate(chosen, s.name).then((r) => { setSecret({ name: s.name, value: r.secret }); setErr(null); }).catch(setErr)} aria-label={`New secret for ${s.name}`}>New secret</Button>{" "}
            <Button sm onClick={() => run(sourcesApi.remove(chosen, s.name))} aria-label={`Delete ${s.name}`}>Delete</Button>
            {tested[s.name] && (
              <div role="status" className="small">
                <b>{tested[s.name].status}</b>: {tested[s.name].reason}
                {(tested[s.name].problems ?? []).map((p) => <div key={p}>{p}</div>)}
                {Object.entries(tested[s.name].inputs ?? {}).map(([k, v]) => <div key={k} className="mono">{k} = {v}</div>)}
              </div>
            )}
          </li>
        ))}
        {list.data && list.data.length === 0 && <li className="faint">This project has no event sources.</li>}
      </ul>
      <form onSubmit={create}>
        <h3>New source</h3>
        <label className="field"><span>Name</span><input required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></label>
        <label className="field"><span>Inputs, one per line as input=expression</span><textarea rows={3} value={form.mapping} onChange={(e) => setForm({ ...form, mapping: e.target.value })} /></label>
        <label className="field"><span>Filter (must be true)</span><input value={form.filter} onChange={(e) => setForm({ ...form, filter: e.target.value })} /></label>
        <label className="field"><span>Dedupe key</span><input value={form.dedupe} onChange={(e) => setForm({ ...form, dedupe: e.target.value })} /></label>
        <label className="field"><span>Debounce seconds</span><input inputMode="numeric" value={form.debounce} onChange={(e) => setForm({ ...form, debounce: e.target.value })} /></label>
        <button type="submit" className="btn btn-primary">Make the source</button>
      </form>
    </Sheet>
  );
}
