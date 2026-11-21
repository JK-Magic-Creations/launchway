import { useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { api, type DeploymentView, type RunDetail } from "../api";
import { MeContext } from "../auth";
import { Empty, ErrorBox, Link, Mono } from "../components/ui";
import { Tabs } from "../kit/Tabs";
import { announce } from "../kit/announce";
import { useNow, useStream } from "../live";
import { launchApi, type LNode, type TreeView } from "../launchApi";
import { Details } from "../launch/Details";
import { HistoryTab } from "../launch/History";
import { LogsTab, sourceId } from "../launch/Logs";
import { Overview } from "../launch/Overview";
import { TrafficTab } from "../launch/Traffic";
import { ObjectTree } from "../launch/Tree";
import { useUrlState } from "../launch/useUrlState";
import "../launch/launch.css";

const KEYS = ["tab", "obj", "src", "prev", "follow", "wrap", "q", "range", "side", "left"] as const;
const TABS = [
  { id: "overview", label: "Overview" }, { id: "objects", label: "Objects" }, { id: "logs", label: "Logs" },
  { id: "traffic", label: "Traffic and tests" }, { id: "history", label: "History" },
];

/** One delivery, watched and steered: what it made, live, what it is doing and what happens next. Everything a
 * person selects is in the address, so a view can be shared. */
export function LaunchPage({ run, dep }: { run: string; dep: string }) {
  const me = useContext(MeContext);
  const [st, set] = useUrlState(KEYS);
  const [detail, setDetail] = useState<RunDetail | null>(null);
  const [view, setView] = useState<TreeView | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [treeError, setTreeError] = useState<unknown>(null);
  const last = useRef(0);
  const timer = useRef<number | undefined>(undefined);
  const now = useNow(1000, true);

  const load = useCallback(() => {
    last.current = Date.now();
    api.run(run, true).then(setDetail).catch(setError);
    launchApi.tree(run, dep).then((v) => { setView(v); setTreeError(null); }).catch(setTreeError);
  }, [run, dep]);
  // At most one fetch every 2 seconds, whatever the live stream or the timer asks.
  const soon = useCallback(() => {
    const wait = last.current + 2000 - Date.now();
    if (wait <= 0) load(); else if (timer.current === undefined) timer.current = window.setTimeout(() => { timer.current = undefined; load(); }, wait);
  }, [load]);
  useEffect(() => { load(); const t = window.setInterval(() => { if (!document.hidden) load(); }, 6000); return () => { window.clearInterval(t); window.clearTimeout(timer.current); }; }, [load]);
  useStream(`/api/v1/stream?run=${encodeURIComponent(run)}`, { run: soon, step: soon, deployment: soon });

  const d: DeploymentView | undefined = detail?.deployments?.find((x) => x.id === dep);
  const tab = TABS.some((t) => t.id === st.tab) ? st.tab : "overview";
  const nodes = useMemo(() => view?.tree.nodes ?? [], [view]);
  const sel = nodes.find((n) => n.id === st.obj);

  // A pod, a Job or an object chosen elsewhere opens in the right tab.
  const open = (id: string) => {
    if (id.startsWith("logs:job:")) { set({ tab: "logs", src: "job:" + id.slice(9), side: "" }); return; }
    if (id.startsWith("logs:")) { set({ tab: "logs", src: "pod:" + id.slice(5), side: "" }); return; }
    set({ tab: "objects", obj: id });
  };
  const toLogs = (q: Record<string, string>) => set({ tab: "logs", src: sourceId(q), prev: q.previous ? "1" : "", side: "" });

  useEffect(() => { document.title = d ? `Launch of ${d.service} to ${d.environment} · Launchway` : "Launch · Launchway"; }, [d]);
  const phase = view?.tree.rollout?.phase;
  const lastPhase = useRef("");
  useEffect(() => { if (phase && lastPhase.current && phase !== lastPhase.current) announce(`The rollout is now ${phase}.`); lastPhase.current = phase ?? ""; }, [phase]);

  const health = useMemo(() => {
    const c = { bad: 0, run: 0 };
    nodes.forEach((n) => { if (n.change === "leftover") return; if (n.health === "Degraded" || n.health === "Missing") c.bad++; if (n.health === "Progressing") c.run++; });
    return c;
  }, [nodes]);

  if (error && !detail) return <div className="page"><ErrorBox error={error} /></div>;
  if (!detail || !d) return <div className="page"><div className="loading">Loading…</div></div>;
  const ro = view?.tree.rollout;
  return (
    <div className="page lw-page">
      <p className="lw-crumb"><Link href={`/runs/${run}`}>Run #{detail.number} of {detail.project}</Link> › step <Mono>{d.step}</Mono></p>
      <header className="lw-head">
        <h1 className="lw-title">Launch of {d.service} to {d.environment}</h1>
        <p className="lw-sub">
          Version <Mono>{d.version}</Mono> · Airfield <Mono>{d.target}</Mono> · application <Mono>{d.application}</Mono>
          {ro && <> · <strong>{ro.strategy === "blueGreen" ? "blue-green" : ro.strategy}</strong> {ro.kind} <Mono>{ro.name}</Mono>: {ro.phase}{ro.paused ? ", paused" : ""}{ro.aborted ? ", aborted" : ""}</>}
          {view?.closed && <> · <strong>Launch over</strong></>}
        </p>
        {view && <p className="lw-live" role="status">
          {health.bad > 0 ? `${health.bad} object${health.bad === 1 ? "" : "s"} not healthy` : "All objects healthy"}{health.run > 0 ? `, ${health.run} still starting` : ""}.
          <span className="faint"> Read by a deploy runner {Math.max(0, Math.round((now - view.tree.at) / 1000))}s ago; refreshes by itself.</span>
        </p>}
      </header>
      {treeError != null && !view && <TreeProblem error={treeError} />}
      {treeError != null && view && <p className="callout callout-bad" role="alert">The deploy runner has not answered the last time: what is shown may be out of date. <ErrorBox error={treeError} /></p>}
      {view && (
        <>
          <Tabs tabs={TABS} on={tab} onChange={(id) => set({ tab: id === "overview" ? "" : id })} label="Views of this Launch" />
          <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`} className="lw-panel">
            {tab === "overview" && <Overview v={view} run={detail} d={d} now={now} onOpen={open} reload={load} />}
            {tab === "objects" && <ObjectsTab v={view} now={now} sel={sel} selId={st.obj} showLeft={st.left !== "0"} onLeft={(on) => set({ left: on ? "" : "0" })} onSelect={(id) => set({ obj: id })} onLogs={toLogs} />}
            {tab === "logs" && <LogsTab v={view} state={{ src: st.src, prev: st.prev, follow: st.follow, wrap: st.wrap, q: st.q, range: st.range, side: st.side }} setState={(p) => set(p)} />}
            {tab === "traffic" && <TrafficTab v={view} run={detail} onOpen={open} />}
            {tab === "history" && <HistoryTab d={d} now={now} admin={!!me?.admin} />}
          </div>
        </>
      )}
    </div>
  );
}

function ObjectsTab({ v, now, sel, selId, showLeft, onLeft, onSelect, onLogs }: {
  v: TreeView; now: number; sel?: LNode; selId: string; showLeft: boolean; onLeft: (on: boolean) => void; onSelect: (id: string) => void; onLogs: (q: Record<string, string>) => void;
}) {
  const leftovers = v.tree.nodes.filter((n) => n.change === "leftover").length;
  const changed = v.tree.nodes.filter((n) => n.change === "new" || n.change === "changed").length;
  return (
    <div className="lw-objects">
      <div className="lw-obj-bar">
        <p className="faint lw-small">{v.tree.nodes.length} objects of this release in {v.tree.namespaces.join(", ")}; {changed} new or changed in this Launch{v.tree.truncated ? "; the tree was cut at its limit" : ""}.</p>
        {leftovers > 0 && <label className="lw-chk"><input type="checkbox" checked={showLeft} onChange={(e) => onLeft(e.target.checked)} /> Show {leftovers} left over from an earlier render</label>}
      </div>
      <div className="lw-split">
        <div className="lw-tree-wrap"><ObjectTree nodes={v.tree.nodes} selected={selId} onSelect={onSelect} showLeftovers={showLeft} /></div>
        <div className="lw-det-wrap">
          {sel ? <Details key={sel.id} v={v} node={sel} now={now} onLogs={onLogs} onSelect={onSelect} /> : <Empty title="Choose an object">Pick a Pod, a ReplicaSet, a ConfigMap or anything else in the tree to see its status, events and manifest.</Empty>}
        </div>
      </div>
    </div>
  );
}

function TreeProblem({ error }: { error: unknown }) {
  return (
    <div className="callout callout-bad" role="alert">
      <strong>The deploy runner could not show this delivery's objects.</strong>
      <ErrorBox error={error} />
      <p className="faint">The run page still shows what the deploy step observed. Looking into a delivery needs a deploy runner for the Airfield with the inspect settings turned on.</p>
    </div>
  );
}
