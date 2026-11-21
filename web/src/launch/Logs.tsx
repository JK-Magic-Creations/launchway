import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ErrorBox } from "../components/ui";
import { announce } from "../kit/announce";
import { useNow } from "../live";
import { launchApi, type LNode, type LogLine, type LogQuery, type TreeView } from "../launchApi";

const MAX_LINES = 4000;
const RANGES: [string, string, number][] = [["all", "All that was read", 0], ["5m", "Last 5 minutes", 300], ["15m", "Last 15 minutes", 900], ["1h", "Last hour", 3600]];

interface Res { key: string; lines: LogLine[]; notes: string[]; state: "idle" | "loading" | "live" | "ended" | "error"; error: unknown }

/** Reads logs of one source, once or followed. Follow uses the push stream; the runner hides secrets first. */
function useLog(v: TreeView, q: LogQuery, follow: boolean, on: boolean): Res {
  const key = JSON.stringify([q, follow, on, v.run, v.deployment]);
  const fresh = useCallback((): Res => ({ key, lines: [], notes: [], state: on ? "loading" : "idle", error: null }), [key, on]);
  const [res, setRes] = useState<Res>(fresh);
  const upd = useCallback((f: (r: Res) => Partial<Res>) => setRes((r) => { const base = r.key === key ? r : fresh(); return { ...base, ...f(base) }; }), [key, fresh]);
  const { run, deployment } = v;
  useEffect(() => {
    if (!on) return;
    const query = { ...JSON.parse(key)[0] as LogQuery, tail: 300 };
    if (!follow) {
      let dead = false;
      launchApi.logs(run, deployment, query).then((r) => { if (!dead) upd(() => ({ lines: r.lines, notes: r.notes ?? [], state: "ended" })); })
        .catch((e) => { if (!dead) upd(() => ({ error: e, state: "error" })); });
      return () => { dead = true; };
    }
    const es = new EventSource(launchApi.streamUrl(run, deployment, { ...query, tail: 200 }));
    let ended = false;
    es.onopen = () => upd(() => ({ state: "live" }));
    es.addEventListener("lines", (e) => {
      try { const batch = JSON.parse((e as MessageEvent).data) as LogLine[]; upd((r) => ({ lines: [...r.lines, ...batch].slice(-MAX_LINES) })); } catch { /* ignore */ }
    });
    es.addEventListener("end", (e) => {
      ended = true; es.close();
      try {
        const d = JSON.parse((e as MessageEvent).data) as { error?: string; notes?: string[] };
        if (d.error) { upd(() => ({ notes: d.notes ?? [], error: new Error(d.error), state: "error" })); return; }
        upd(() => ({ notes: d.notes ?? [], state: "ended" }));
      } catch { upd(() => ({ state: "ended" })); }
    });
    es.onerror = () => { if (!ended) { es.close(); upd(() => ({ state: "ended" })); } };
    return () => { ended = true; es.close(); };
  }, [key, follow, on, run, deployment, upd]);
  return res.key === key ? res : fresh();
}

function stamp(t: number): string {
  return t ? new Date(t).toISOString().slice(11, 23) : "";
}

function Pane({ title, lines, state, notes, error, search, wrap, interleaved, follow }: {
  title: string; lines: LogLine[]; state: string; notes: string[]; error: unknown; search: string; wrap: boolean; interleaved: boolean; follow: boolean;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);
  useEffect(() => { if (stick && box.current) box.current.scrollTop = box.current.scrollHeight; }, [lines, stick]);
  const needle = search.trim().toLowerCase();
  const shown = needle ? lines.filter((l) => l.line.toLowerCase().includes(needle) || l.src.toLowerCase().includes(needle)) : lines;
  const word = state === "loading" ? "Reading…" : state === "live" ? "Live" : state === "error" ? "Could not read" : state === "ended" ? (follow ? "Stream ended" : "Read") : "";
  return (
    <div className="lw-pane">
      <div className="lw-pane-head">
        <h2 className="h-xs">{title}</h2>
        <span className={`lw-state lw-state-${state}`}>{word}{needle ? ` · ${shown.length} of ${lines.length} lines match` : ` · ${lines.length} lines`}</span>
      </div>
      <ErrorBox error={error} />
      {notes.map((n) => <p key={n} className="lw-note">{n}</p>)}
      <div ref={box} className={"lw-log" + (wrap ? " wrap" : "")} role="log" aria-label={`Log of ${title}`} aria-live="off"
        onScroll={(e) => { const el = e.currentTarget; setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 24); }}>
        {shown.length === 0 && state !== "loading" && <div className="lw-empty">{lines.length ? "No line matches the search." : "No log lines."}</div>}
        {shown.map((l, i) => (
          <div key={i} className="lw-line">
            <span className="lw-t">{stamp(l.t)}</span>
            {interleaved && <span className={"lw-src " + (l.src.startsWith("canary") || l.src.startsWith("preview") ? "lw-src-new" : "lw-src-old")}>[{l.src}]</span>}
            <span className="lw-text">{l.line}</span>
          </div>
        ))}
      </div>
      {!stick && <button type="button" className="btn btn-sm" onClick={() => { setStick(true); if (box.current) box.current.scrollTop = box.current.scrollHeight; }}>Jump to the end</button>}
    </div>
  );
}

interface Source { id: string; label: string; q: LogQuery }

/** The sources a person can read: roles, ReplicaSets, pods with their containers, and push-test Jobs. */
export function sources(nodes: LNode[]): Source[] {
  const out: Source[] = [];
  const roles = new Set(nodes.filter((n) => n.kind === "ReplicaSet" && n.role && n.role !== "old").map((n) => n.role as string));
  for (const r of ["stable", "canary", "active", "preview"]) if (roles.has(r)) out.push({ id: "role:" + r, label: `All ${r} pods`, q: { role: r } });
  for (const n of nodes.filter((n) => n.kind === "ReplicaSet" && n.replicas && n.replicas.desired > 0)) out.push({ id: "rs:" + n.id, label: `ReplicaSet ${n.name}${n.role ? ` (${n.role})` : ""}`, q: { replica_set: n.id } });
  for (const n of nodes.filter((n) => n.kind === "Job" && n.change !== "leftover")) out.push({ id: "job:" + n.id, label: `Test job ${n.name}`, q: { job: n.id } });
  for (const n of nodes.filter((n) => n.kind === "Pod" && n.change !== "leftover")) {
    const cs = (n.containers ?? []).filter((c) => !c.init);
    if (cs.length <= 1) out.push({ id: "pod:" + n.name, label: `Pod ${n.name}`, q: { pod: n.name } });
    else cs.forEach((c) => out.push({ id: `pod:${n.name}:${c.name}`, label: `Pod ${n.name}, container ${c.name}`, q: { pod: n.name, container: c.name } }));
  }
  return out;
}

export function sourceId(q: Record<string, string>): string {
  if (q.role) return "role:" + q.role;
  if (q.replica_set) return "rs:" + q.replica_set;
  if (q.job) return "job:" + q.job;
  if (q.pod && q.container) return `pod:${q.pod}:${q.container}`;
  if (q.pod) return "pod:" + q.pod;
  return "";
}

export function LogsTab({ v, state, setState }: { v: TreeView; state: Record<"src" | "prev" | "follow" | "wrap" | "q" | "range" | "side", string>; setState: (p: Partial<Record<"src" | "prev" | "follow" | "wrap" | "q" | "range" | "side", string>>) => void }) {
  const srcs = useMemo(() => sources(v.tree.nodes), [v.tree.nodes]);
  const hasBoth = srcs.some((s) => s.id === "role:stable") && srcs.some((s) => s.id === "role:canary" || s.id === "role:preview");
  const sel = srcs.find((s) => s.id === state.src);
  const follow = state.follow === "1";
  const previous = state.prev === "1";
  const side = state.side === "1" && hasBoth;
  const newer = srcs.find((s) => s.id === "role:canary") ?? srcs.find((s) => s.id === "role:preview");
  const range = RANGES.find((r) => r[0] === state.range) ?? RANGES[0];
  const mk = (q: LogQuery | undefined): LogQuery => ({ ...q, previous: previous && !side ? true : undefined, tail: 300 });
  const main = useLog(v, mk(side ? srcs.find((s) => s.id === "role:stable")?.q : sel?.q), follow, !!(side ? true : sel) && v.can_logs);
  const second = useLog(v, mk(newer?.q), follow, side && v.can_logs);
  const now = useNow(5000, !!range[2]);
  const t0 = range[2] ? now - range[2] * 1000 : 0;
  const filt = useCallback((ls: LogLine[]) => (t0 ? ls.filter((l) => !l.t || l.t >= t0) : ls), [t0]);
  useEffect(() => { if (main.state === "live") announce("Following the log. New lines are not read out."); }, [main.state]);

  if (!v.can_logs) return <p className="callout callout-info">Logs need the operator role on {v.project}{v.protected ? ` in ${v.environment} (a protected environment)` : ""}. {v.why ?? ""}</p>;
  if (srcs.length === 0) return <p className="faint">No pods yet, so no logs. They appear here as soon as the delivery starts them.</p>;

  const download = () => {
    const lines = filt(main.lines).concat(side ? filt(second.lines) : []);
    const text = lines.map((l) => `${stamp(l.t)} [${l.src}] ${l.line}`).join("\n") + "\n";
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    a.download = `${v.deployment}-${(sel?.id ?? "stable-canary").replace(/[^a-z0-9.-]+/gi, "_")}.log`;
    a.click();
    URL.revokeObjectURL(a.href);
  };
  const interleaved = !side && !!sel && (sel.q.role !== undefined || sel.q.replica_set !== undefined || sel.q.job !== undefined);
  return (
    <div className="lw-logs">
      <div className="lw-bar" role="group" aria-label="What to read">
        <label className="lw-fld"><span>Source</span>
          <select value={side ? "" : state.src} disabled={side} onChange={(e) => setState({ src: e.target.value })}>
            <option value="">Choose what to read…</option>
            {srcs.map((s) => <option key={s.id} value={s.id}>{s.label}</option>)}
          </select>
        </label>
        {hasBoth && <label className="lw-chk"><input type="checkbox" checked={side} onChange={(e) => setState({ side: e.target.checked ? "1" : "", src: "" })} /> Stable and canary side by side</label>}
        <label className="lw-chk"><input type="checkbox" checked={follow} onChange={(e) => setState({ follow: e.target.checked ? "1" : "" })} /> Follow (live)</label>
        <label className="lw-chk"><input type="checkbox" checked={previous} disabled={side} onChange={(e) => setState({ prev: e.target.checked ? "1" : "" })} /> Earlier container of a restarted pod</label>
        <label className="lw-chk"><input type="checkbox" checked={state.wrap === "1"} onChange={(e) => setState({ wrap: e.target.checked ? "1" : "" })} /> Wrap lines</label>
        <label className="lw-fld"><span>Time</span>
          <select value={range[0]} onChange={(e) => setState({ range: e.target.value === "all" ? "" : e.target.value })}>{RANGES.map((r) => <option key={r[0]} value={r[0]}>{r[1]}</option>)}</select>
        </label>
        <label className="lw-fld"><span>Search</span><input type="search" value={state.q} placeholder="text in a line or its source" onChange={(e) => setState({ q: e.target.value })} /></label>
        <button type="button" className="btn" onClick={download} disabled={!main.lines.length && !second.lines.length}>Download</button>
      </div>
      <p className="faint lw-small">Secrets the cluster holds and anything shaped like a credential are masked before a line leaves the deploy runner. At most 20 pods and 8 MiB are read at once.</p>
      {!sel && !side && <p className="faint">Choose what to read: all the canary pods, the stable ones, one pod, or a test job.</p>}
      {(sel || side) && (
        <div className={side ? "lw-panes two" : "lw-panes"}>
          <Pane title={side ? "Stable" : sel?.label ?? ""} lines={filt(main.lines)} state={main.state} notes={main.notes} error={main.error} search={state.q} wrap={state.wrap === "1"} interleaved={interleaved} follow={follow} />
          {side && <Pane title={newer?.id === "role:preview" ? "Preview" : "Canary"} lines={filt(second.lines)} state={second.state} notes={second.notes} error={second.error} search={state.q} wrap={state.wrap === "1"} interleaved={false} follow={follow} />}
        </div>
      )}
    </div>
  );
}
