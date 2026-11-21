// Types and calls for the software graph. They mirror internal/graph and internal/api/graph.go.

import { call } from "../api";

export type Kind = "repository" | "package" | "component" | "requirement" | "image" | "built" | "pipeline" | "airfield" | "deployment" | "machine" | "platform" | "owner" | "commit" | "advisory";

export const KINDS: Kind[] = ["repository", "package", "component", "requirement", "image", "built", "pipeline", "airfield", "deployment", "machine", "platform", "owner", "commit", "advisory"];

/** What each kind is called to people. */
export const KIND_WORDS: Record<Kind, string> = {
  repository: "Repository", package: "Package", component: "Component", requirement: "Requirement", image: "Image", built: "Built image", pipeline: "Pipeline",
  airfield: "Airfield", deployment: "Deployment", machine: "Machine", platform: "Platform", owner: "Owner", commit: "Commit", advisory: "Advisory",
};

export interface GNode { id?: number; kind: Kind; key: string; name?: string; owner?: string; attrs?: Record<string, unknown>; origin?: string; ref?: string; confidence?: string }
export interface Ref { kind: Kind; key: string }
export interface GEdge {
  id: number; kind: string; from: Ref; to: Ref; qualifier?: string; constraint?: string; resolved?: string; attrs?: Record<string, unknown>;
  origin: string; ref?: string; confidence: string; valid_from: number; valid_to?: number;
}
export interface Step { edge: GEdge; node: Ref }
export interface Hit { node: GNode; depth: number; path?: Step[] }
export interface Truncation { truncated: boolean; truncated_reason?: string }
export interface Reach extends Truncation { Start?: GNode; start?: GNode; hits: Hit[] }
export interface Running { deployment: GNode; airfield?: string; digest?: string; version?: string; since?: number; exact: boolean; why: Step[] }
export interface Impact extends Truncation {
  start: GNode; total: number; by_kind: Record<string, number>; repositories: string[];
  by_owner: { owner: string; count: number; items: (Hit & { owner: string })[] }[]; deployments: Running[];
}
export interface Where extends Truncation { start: GNode; deployments: Running[]; airfields: string[] }
export interface RepoEdge { consumer: string; provider: string; via: string[]; kinds: string[]; versions?: string[]; override?: string; reason?: string }
export interface Override { id: number; kind: "before" | "ignore" | "pin"; from: string; to: string; value?: string; reason: string; author?: string; created_at: number }
export interface Cycle { path: string[]; members: string[]; suggestion?: string }
export interface Order extends Truncation { order: string[]; levels: string[][]; edges: RepoEdge[]; cycles?: Cycle[]; overrides?: Override[]; unknown?: string[] }
export interface Rebuild extends Truncation {
  repository: string; items: { repository: string; level: number; why: RepoEdge[] }[]; skipped?: RepoEdge[]; cycles?: Cycle[];
}
export interface Fact { facet: string; value: string; source: "declared" | "inferred"; evidence?: string }
export interface Advice { facet: string; inferred: string; declared: string; evidence?: string; message: string }
export interface Needs { component: GNode; repository?: string; facts: Fact[]; advice?: Advice[]; file: string }
export interface Candidate { host: GNode; fits: boolean; satisfied: Fact[]; missing?: { fact: Fact; reason: string }[] }
export interface Placement { component: GNode; facts: Fact[]; fits: Candidate[]; near_misses?: Candidate[]; gaps: { facts: Fact[]; message: string }[]; hosts_considered: number }
export interface Summary { nodes: Record<string, number>; edges: number; sources: { source: string; at: number; scopes: number }[] }
export interface Path extends Truncation { from: GNode; to: GNode; found: boolean; way?: string; steps?: Step[] }

const q = (o: Record<string, string | number | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== "") p.set(k, String(v));
  const s = p.toString();
  return s ? "?" + s : "";
};
const base = "/api/v1/graph";

export const refText = (r: Ref) => `${r.kind}:${r.key}`;
export const sameRef = (a: Ref, b: Ref) => a.kind === b.kind && a.key === b.key;

export const graphApi = {
  summary: () => call<Summary>("GET", base),
  search: (text: string, kind?: string) => call<{ items: GNode[]; more: boolean }>("GET", `${base}/search${q({ q: text, kind })}`),
  node: (ref: string) => call<{ node: GNode; edges: GEdge[]; more_edges: boolean }>("GET", `${base}/node${q({ ref })}`),
  neighbours: (ref: string, direction: "downstream" | "upstream", depth = 1, nodes = 300) => call<Reach>("GET", `${base}/neighbours${q({ ref, direction, depth, nodes })}`),
  path: (from: string, to: string) => call<Path>("GET", `${base}/path${q({ from, to, depth: 16 })}`),
  impact: (ref: string) => call<Impact>("GET", `${base}/impact${q({ ref })}`),
  where: (ref: string) => call<Where>("GET", `${base}/where${q({ ref })}`),
  order: (repos: string[], upstream: boolean) => call<Order>("GET", `${base}/order${repos.length || upstream ? "?" : ""}${[...repos.map((r) => "repository=" + encodeURIComponent(r)), upstream ? "upstream=true" : ""].filter(Boolean).join("&")}`),
  rebuildSet: (repository: string) => call<Rebuild>("GET", `${base}/rebuild-set${q({ repository })}`),
  overrides: () => call<{ items: Override[] }>("GET", `${base}/overrides`),
  addOverride: (o: { kind: string; from: string; to: string; value?: string; reason: string }) => call<Override>("POST", `${base}/overrides`, o),
  removeOverride: (id: number) => call<void>("DELETE", `${base}/overrides/${id}`),
  needs: (ref: string) => call<{ items: Needs[]; file: string }>("GET", `${base}/needs${q({ ref })}`),
  placement: (ref: string) => call<{ items: Placement[] }>("GET", `${base}/placement${q({ ref })}`),
  specification: (refs: string[], headroom?: number) => call<Record<string, unknown>>("GET", `${base}/specification?${[...refs.map((r) => "ref=" + encodeURIComponent(r)), headroom ? "headroom=" + headroom : ""].filter(Boolean).join("&")}`),
  proposeRequirements: (repo: string, content: string, title?: string) => call<{ number: number; branch: string; title: string }>("POST", `/api/v1/repositories/${repo}/requirements/proposal`, { content, title }),
};
