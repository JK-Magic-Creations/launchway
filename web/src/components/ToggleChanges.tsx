import { useCallback, useState } from "react";
import type { DeploymentView } from "../api";
import { useMe, canOperate } from "../auth";
import { ago, clock } from "../format";
import { useLoad } from "../guided/useLoad";
import { Dialog } from "../kit/Dialog";
import { Button, Sheet } from "../kit/layout";
import { useNow } from "../live";
import { onLink } from "../router";
import { togglesApi, type Change } from "../togglesApi";
import { ErrorBox } from "./ui";
import "../toggles.css";
import { terms } from "../terms";

type Item = { at: number; key: string; node: React.ReactNode };

/** What changed in an environment, deployments and toggle changes together and newest first, so that "what changed
 * before it broke?" has one answer. Each toggle change links to its toggle, and the ones still in effect since the
 * last deployment can be reverted together from here. */
export function EnvironmentChanges({ environment, projects, deployments }: { environment: string; projects: string[]; deployments: DeploymentView[] }) {
  const now = useNow(30000);
  const me = useMe();
  const shownKey = projects.slice(0, 6).join("\n");
  const fn = useCallback(async (bg: boolean) => {
    const out: (Change & { project_name: string })[] = [];
    for (const p of shownKey.split("\n").filter(Boolean)) {
      try { const r = await togglesApi.timeline(p, environment, bg); out.push(...r.changes.map((c) => ({ ...c, project_name: p }))); } catch { /* no toggles, or not allowed: nothing to show */ }
    }
    return out;
  }, [shownKey, environment]);
  const tl = useLoad(fn, 20000);
  const [error, setError] = useState<unknown>(null);
  const [asking, setAsking] = useState<string | null>(null);
  const [reason, setReason] = useState("");
  const changes = tl.data ?? [];
  if (!tl.data || changes.length === 0) return null;
  const items: Item[] = [
    ...deployments.map((d) => ({ at: d.opened_at, key: "d" + d.id, node: <><b>Deployed</b> <span className="mono">{d.version}</span> of {d.service}{d.mode === "rollback" ? " (a rollback)" : ""}</> })),
    ...changes.map((c) => ({ at: c.at, key: "t" + c.id, node: <><b>{terms().say("{Toggle}")}</b> <a href={`/projects/${encodeURIComponent(c.project_name)}/toggles/${encodeURIComponent(c.toggle)}`} onClick={onLink}>{c.toggle}</a> {c.action === "state" ? "changed" : c.action}{c.status === "pending" ? " (waiting for approval)" : ""} by {c.actor}: {c.reason}</> })),
  ].sort((a, b) => b.at - a.at).slice(0, 25);
  const lastDeploy = Math.max(0, ...deployments.filter((d) => d.deployed_at).map((d) => d.deployed_at!));
  const since = changes.filter((c) => c.action === "state" && c.status === "applied" && !c.reverted_by && !c.reverts && c.at > lastDeploy);
  const byProject = new Map<string, number>();
  for (const c of since) byProject.set(c.project_name, (byProject.get(c.project_name) ?? 0) + 1);
  const revert = async (project: string) => {
    setError(null);
    try { await togglesApi.revertSince(project, environment, lastDeploy, reason.trim()); setAsking(null); setReason(""); tl.reload(); } catch (e) { setError(e); }
  };
  return (
    <Sheet title="Deployments and toggle changes together" actions={<span className="faint small">newest first</span>}>
      <ErrorBox error={error} />
      <ul className="tg-timeline">{items.map((i) => <li key={i.key}><span className="tg-when" title={clock(i.at)}>{ago(i.at, now)}</span><span>{i.node}</span></li>)}</ul>
      {since.length > 0 && [...byProject].map(([p, n]) => (me?.admin || canOperate(me, p)) && (
        <p className="tg-callout" key={p}>{n} toggle change{n === 1 ? " was" : "s were"} made in {p} since the last deployment here.{" "}
          <Button sm onClick={() => setAsking(p)}>Revert {n === 1 ? "it" : "them"}</Button></p>
      ))}
      {asking && (
        <Dialog label={`Revert the toggle changes in ${environment}`} onClose={() => setAsking(null)}>
          <form className="tg-form" onSubmit={(e) => { e.preventDefault(); void revert(asking); }}>
            <h2>Revert the toggle changes since the last deployment</h2>
            <p>Every toggle of {asking} changed in {environment} after the last deployment goes back to what it was. A revert is never held for approval and is recorded like any change.</p>
            <label>Why <input value={reason} onChange={(e) => setReason(e.target.value)} required data-autofocus /></label>
            <ErrorBox error={error} />
            <div className="dialog-actions"><button type="button" className="btn" onClick={() => setAsking(null)}>Cancel</button><button type="submit" className="btn btn-primary" disabled={reason.trim() === ""}>Revert them</button></div>
          </form>
        </Dialog>
      )}
    </Sheet>
  );
}
