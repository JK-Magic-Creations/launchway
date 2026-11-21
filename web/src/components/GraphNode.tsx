import type { StepView } from "../api";
import { elapsed } from "../format";
import { groupStatus, nodeName, type RNode } from "../graph/model";
import { terms } from "../terms";
import { ImageIcon } from "./Images";
import { shownStatus, StatusIcon, statusLabel, stepLabel, tone } from "./ui";

const lowerFirst = (t: string) => t.slice(0, 1).toLowerCase() + t.slice(1);

/** What a step says under its name: its state in words, and how long it has taken or took. The picture never
 * carries the state alone: every node has the words too. */
export function stepSub(s: StepView, now: number, phases?: Record<string, string>, describe?: (s: StepView) => string): string {
  if (describe) return describe(s);
  const took = elapsed(s.started_at, s.finished_at, now);
  const t = took ? ` · ${took}` : "";
  if (s.reused_from) return `Reused from #${s.reused_from_number}`;
  switch (s.status) {
    case "skipped": return `Skipped: ${lowerFirst(stepLabel(s)).replace(/^skipped$/, "not needed")}`;
    case "pending": return "Not started";
    case "queued": return terms().say("Waiting for a {runner}");
    case "leased": return `Starting${t}`;
    case "running": return `${statusLabel("running")}${t}`;
    case "succeeded":
      return s.kind === "approval" ? `${terms().say("{clearance}")} from ${s.decided_by}` : `Passed${t}`;
    case "failed":
      if (s.policy_blocked) return "Blocked by policy";
      if (s.kind === "approval") return s.decided_by ? `${terms().lower("no_go")} from ${s.decided_by}` : "Nobody decided";
      return `${s.allowed_failure ? "Failed (allowed)" : "Failed"}${t}`;
    case "waiting": return phases?.[s.id] ?? stepLabel(s);
    default: return statusLabel(s.status);
  }
}

/** The words a screen reader gets for one node. They START with the node's visible text, in the order it is drawn
 * (name, the badges, the attempt, the status line, what it follows), so that the accessible name contains the visible
 * label as WCAG 2.5.3 asks; the extra words (the step kind, the description, what runs beside it) come after. */
export function nodeLabel(n: RNode, sub: string, before: string[], beside: string[], names: (id: string) => string): string {
  const s = n.kind === "step" ? n.steps[0] : undefined;
  const visible = [nodeName(n)];
  if (s) {
    if (s.kind === "image") visible.push("image");
    if (s.policy_blocked) visible.push("policy");
    if (s.kind === "verify") visible.push("verify");
    if (s.kind === "ai") visible.push("ai");
    if (s.attempt > 1) visible.push(`#${s.attempt}`);
  }
  visible.push(sub);
  if (before.length) visible.push(`after ${before.map(names).join(", ")}`);
  const parts = [visible.filter(Boolean).join(" ")];
  if (s?.kind && s.kind !== "deploy") parts.push(`${s.kind} step`);
  if (n.kind === "group") parts.push(`template ${n.group?.uses ?? ""}. Choose to show its steps`);
  if (n.kind === "chain") parts.push("Choose to show them");
  if (s?.description) parts.push(s.description);
  if (s?.attempt && s.attempt > 1) parts.push(`attempt ${s.attempt}`);
  if (beside.length) parts.push(`runs together with ${beside.map(names).join(", ")}`);
  return parts.join(". ");
}

/** One node of the pipeline graph: a step (or a folded group or chain), as a button. */
export function GraphNode({ node, x, y, w, h, selected, tabbable, sub, label, setRef, onChoose, onFold, onKey, needsText }: {
  node: RNode; x: number; y: number; w: number; h: number; selected: boolean; tabbable: boolean;
  sub: string; label: string; needsText: string;
  setRef: (el: HTMLButtonElement | null) => void; onChoose: () => void; onFold: () => void; onKey: (e: React.KeyboardEvent) => void;
}) {
  const style = { "--x": `${x}px`, "--y": `${y}px`, "--nw": `${w}px`, "--nh": `${h}px` } as React.CSSProperties;
  if (node.kind !== "step") {
    const status = groupStatus(node.steps.map((s) => s.status));
    const count = node.steps.length;
    return (
      <li className="pg-item" style={style}>
        <button type="button" ref={setRef} tabIndex={tabbable ? 0 : -1} data-node={node.id} aria-expanded="false" aria-label={label} onClick={onFold} onKeyDown={onKey}
          className={`pg-node pg-folded t-${tone(status)}${selected ? " pg-selected" : ""}`}
          title={node.kind === "group" ? `${node.group?.uses ?? ""}. Choose to show its steps.` : "Steps one after another. Choose to show them."}>
          <span className="pg-top"><StatusIcon status={status} /><span className="pg-name">{nodeName(node)}</span>{" "}<span className="pg-count" aria-hidden="true">▸ {count}</span></span>{" "}
          <span className="pg-sub">{sub}</span>{" "}
          <span className="pg-needs" aria-hidden="true">{needsText}</span>
        </button>
      </li>
    );
  }
  const s = node.steps[0];
  const shown = shownStatus(s);
  const cls = ["pg-node", `t-${tone(shown)}`, s.status === "skipped" && "pg-skipped", s.status === "pending" && "pg-pending", s.group && "pg-templated",
    selected && "pg-selected", s.uses && "pg-template", s.reused_from && "pg-reused"].filter(Boolean).join(" ");
  return (
    <li className="pg-item" style={style}>
      <button type="button" ref={setRef} tabIndex={tabbable ? 0 : -1} data-node={node.id} aria-pressed={selected} aria-label={label} className={cls}
        onClick={onChoose} onKeyDown={onKey} title={s.description || s.id}>
        <span className="pg-top">
          <StatusIcon status={shown} />
          <span className="pg-name">{nodeName(node)}</span>
          {s.kind === "image" && <span className="node-kind" title="Builds a container image"><ImageIcon size={11} />image</span>}
          {s.policy_blocked && <span className="node-policy" title="Blocked by policy">policy</span>}
          {s.kind === "verify" && <span className="node-kind" title={terms().say("Watches an {environment}'s metrics and logs")}>verify</span>}
          {s.kind === "ai" && <span className="node-kind" title="Asks a model">ai</span>}
          {s.attempt > 1 && <span className="node-attempt" title={`Attempt ${s.attempt}`}>#{s.attempt}</span>}
        </span>{" "}
        <span className="pg-sub">{sub}</span>{" "}
        <span className="pg-needs" aria-hidden="true">{needsText}</span>
      </button>
    </li>
  );
}
