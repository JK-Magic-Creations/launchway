import type { GraphGroup, StepStatus, StepView } from "../api";
import { serialChains, stages, type GNode } from "./layout";

/** One drawn node: a step, a folded template group, or a folded chain of steps one after another. */
export interface RNode {
  id: string;
  kind: "step" | "group" | "chain";
  /** The steps it stands for (one for a step). */
  steps: StepView[];
  needs: string[];
  group?: GraphGroup;
}

/** The outermost template group of a step: a template that uses templates is one group. */
export const topGroup = (g: string) => g.split("/")[0];

/** The status of several steps as one: what is happening now if anything is, else how it ended. */
export function groupStatus(statuses: StepStatus[]): StepStatus {
  const any = (...s: StepStatus[]) => statuses.some((x) => s.includes(x));
  if (any("running", "leased")) return "running";
  if (any("waiting")) return "waiting";
  if (any("failed")) return "failed";
  if (any("queued")) return "queued";
  if (any("pending")) return statuses.some((x) => x === "succeeded") ? "running" : "pending";
  if (any("canceled")) return "canceled";
  if (statuses.length > 0 && statuses.every((x) => x === "skipped")) return "skipped";
  return "succeeded";
}

export interface Folds {
  /** Template groups folded into one node, by group name. */
  groups: ReadonlySet<string>;
  /** Chains folded into one node, by the ID of their first folded step. */
  chains: ReadonlySet<string>;
}

/** The chains that can be folded: long runs of steps one after another, minus their first and last step (which stay
 * drawn, so what comes in and what goes out is always visible). Keyed by the first folded step. */
export function foldableChains(steps: StepView[], minLen = 5): Map<string, string[]> {
  const out = new Map<string, string[]>();
  const gnodes: GNode[] = steps.map((s) => ({ id: s.id, needs: s.needs ?? [] }));
  for (const run of serialChains(gnodes, minLen)) out.set(run[1], run.slice(1, -1));
  return out;
}

/** Builds the nodes to draw from the steps and what is folded. A node needs what any of its steps needs, outside itself. */
export function buildNodes(steps: StepView[], groups: GraphGroup[] | undefined, folds: Folds, chains: Map<string, string[]>): RNode[] {
  const byGroup = new Map((groups ?? []).map((g) => [g.id, g]));
  const nodeOf = new Map<string, string>();
  const nodes: RNode[] = [];
  const made = new Map<string, RNode>();
  const chainOf = new Map<string, string>();
  for (const [first, members] of chains) if (folds.chains.has(first)) for (const m of members) chainOf.set(m, first);
  for (const s of steps) {
    const g = s.group ? topGroup(s.group) : "";
    let key = s.id, kind: RNode["kind"] = "step";
    if (g && folds.groups.has(g)) { key = "group:" + g; kind = "group"; }
    else if (chainOf.has(s.id)) { key = "chain:" + chainOf.get(s.id); kind = "chain"; }
    nodeOf.set(s.id, key);
    let n = made.get(key);
    if (!n) {
      n = { id: key, kind, steps: [], needs: [], group: kind === "group" ? byGroup.get(g) ?? { id: g, uses: "", library: "", template: "", steps: [] } : undefined };
      made.set(key, n);
      nodes.push(n);
    }
    n.steps.push(s);
  }
  for (const n of nodes) {
    for (const s of n.steps) for (const d of s.needs ?? []) {
      const from = nodeOf.get(d);
      if (from && from !== n.id && !n.needs.includes(from)) n.needs.push(from);
    }
  }
  return nodes;
}

/** The node that holds a step, for selecting. */
export function nodeHolding(nodes: RNode[], stepId: string | null): string | null {
  if (!stepId) return null;
  return nodes.find((n) => n.id === stepId || n.steps.some((s) => s.id === stepId))?.id ?? null;
}

/** The stages in words, for assistive technology: "Stage 2 (runs together): test, security-go, security-web". */
export function stageSentences(nodes: RNode[]): string[] {
  return stages(nodes.map((n) => ({ id: n.id, needs: n.needs }))).map((ids, i) => {
    const names = ids.map((id) => nodes.find((n) => n.id === id)!).map(nodeName);
    return `Stage ${i + 1}${names.length > 1 ? " (runs together)" : ""}: ${names.join(", ")}`;
  });
}

export function nodeName(n: RNode): string {
  if (n.kind === "group") return n.group?.id ?? n.id;
  if (n.kind === "chain") return `${n.steps.length} in a row`;
  const s = n.steps[0];
  return s.group ? s.id.slice(topGroup(s.group).length + 1) : s.id;
}
