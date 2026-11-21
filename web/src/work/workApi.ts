// Types and calls for work tracking (issues, boards, backlog, sprints, charts, agents). They mirror
// internal/work and internal/api/work_*.go. Only the lazy work pages import this file.

import { call } from "../api";

export interface Scope { kind: string; id?: string }
export interface Assignee { name: string; kind: string }
export interface Criterion { id: string; text: string; done: boolean }
export interface Claim { agent: string; claimed_at: string; heartbeat_at: string; lease_until: string; note?: string; expired?: boolean }
export interface Progress { total: number; done: number; points: number; points_done: number; in_progress: number; blocked?: number }

export interface Item {
  id: string; ref: string; scope: Scope; number: number; type: string; title: string; body?: string;
  state: string; state_name?: string; category?: string; resolution?: string; priority: string; estimate?: number;
  parent_id?: string; parent_ref?: string; milestone_id?: string; milestone?: string; sprint_id?: string; sprint?: string; rank: number;
  start_at?: string; due_at?: string; reporter?: string; reporter_kind?: string; confidential?: boolean; difficulty?: string;
  custom?: Record<string, unknown>; labels: string[]; assignees: Assignee[]; watchers?: string[]; criteria?: Criterion[];
  blocked?: boolean; blocked_by?: string[]; claim?: Claim; children?: Progress; version: number;
  created_at: string; updated_at: string; closed_at?: string; comment_count?: number;
}

export interface Link {
  id: string; kind: string; from: string; to: string; from_title?: string; to_title?: string; from_state?: string; to_state?: string;
  created_by?: string; created_at: string;
}
export interface CodeLink { id: string; kind: string; repo?: string; ref: string; title?: string; state?: string; closes?: boolean; source?: string; created_at: string }
export interface Attachment { id: string; name: string; content_type: string; size: number; sha256: string; kind: string; by: string; at: string }
export interface EnvLine { environment: string; running: boolean; since?: string; deployment?: string; run?: string; version?: string }
export interface Delivered { merged: boolean; environments: EnvLine[]; sentence: string }
export interface UsageTotal { tokens: number; cents: number; seconds: number; exceeded: boolean; message?: string }
export interface ItemDetail extends Item { links: Link[]; code: CodeLink[]; attachments: Attachment[]; delivery?: Delivered | null; usage?: UsageTotal | null; actions: string[] }

export interface Page { items: Item[]; total: number; limit: number; offset: number }
export interface Comment { id: string; item?: string; author: string; author_kind: string; body: string; created_at: string; edited_at?: string; edits?: number }
export interface WorkEvent { seq: number; item_id?: string; at: string; actor: string; actor_kind: string; via?: string; kind: string; data?: Record<string, unknown> }

export interface NewItem {
  scope: Scope; type: string; title: string; body?: string; priority?: string; estimate?: number; parent?: string; milestone?: string; sprint?: string;
  labels?: string[]; assignees?: string[];
}
export interface Patch {
  expected_version?: number; title?: string; body?: string; type?: string; state?: string; priority?: string; estimate?: number; parent?: string;
  milestone?: string; sprint?: string; labels?: string[]; assignees?: string[]; criteria?: { id: string; text: string; done: boolean }[]; due_at?: string; resolution?: string;
}

export interface View { id: string; name: string; query: string; shared?: boolean; scope?: Scope; owner?: string }

export interface Column { state: string; name: string; category: string; wip?: number; count: number; over_wip?: boolean; points: number; items: Item[]; shown: number }
export interface Lane { key: string; name: string }
export interface Board { scope: Scope; columns: Column[]; lanes?: Lane[]; lane_by?: string; lane_of?: Record<string, string>; query?: string }

export interface Milestone { id: string; scope: Scope; title: string; description?: string; due_at?: string; state: string; total: number; done: number; points: number; points_done: number; percent: number }
export interface Sprint {
  id: string; scope: Scope; name: string; goal?: string; start_at: string; end_at: string; state: string; capacity?: Record<string, number>; unit: string;
  committed: number; completed: number; added: number; carried_over: number; items: number; items_done: number; total_capacity: number; warning?: string;
}
export interface SprintIn { scope?: string; name?: string; goal?: string; start_at?: string; end_at?: string; capacity?: Record<string, number> }
export interface BurnPoint { date: string; remaining: number; ideal: number; done: number; added?: number }
export interface Burndown { sprint: string; unit: string; committed: number; points: BurnPoint[] }
export interface VelocityEntry { sprint: string; committed: number; completed: number; unit: string }
export interface Velocity { sprints: VelocityEntry[]; average: number; unit: string }
export interface StateDef { key: string; name: string; category: string; wip?: number }
export interface CfdDay { date: string; counts: Record<string, number> }
export interface Cfd { states: StateDef[]; days: CfdDay[] }
export interface Bucket { label: string; count: number }
export interface ItemTime { ref: string; title: string; lead_days: number; cycle_days: number }
export interface TimeStats {
  count: number; lead_median_days: number; lead_p85_days: number; lead_mean_days: number; cycle_median_days: number; cycle_p85_days: number; cycle_mean_days: number;
  items: ItemTime[]; histogram: Bucket[];
}
export interface RoadmapRow { kind: string; ref?: string; title: string; start_at?: string; due_at?: string; percent: number; state?: string; total: number; done: number }

export interface Transition { from?: string[]; to: string; who: string }
export interface FieldDef { key: string; name: string; type: string; options?: string[]; required?: boolean; types?: string[] }
export interface TypeDef { key: string; name: string; template?: string; disabled?: boolean }
export interface CloseRule { on: string; state: string; verbs?: string[] }
export interface Autonomy { level: number; paths?: string[]; types?: string[]; max_files?: number; max_lines?: number; pause_after?: number; pause_days?: number; reason?: string }
export interface ReviewPolicy {
  mode: string; paths?: string[]; types?: string[]; max_files?: number; max_lines?: number; require_different_model?: boolean; sample_percent?: number; reason?: string;
}
export interface AgentRules { enabled?: boolean; human_approval?: boolean; allow_merge?: boolean; reason?: string; review?: ReviewPolicy; autonomy?: Autonomy }
export interface DifficultyRule { label?: string; min_estimate?: number; difficulty: string }
export interface Settings {
  prefix?: string; estimate_unit?: string; states?: StateDef[]; transitions?: Transition[]; types?: TypeDef[]; fields?: FieldDef[]; close?: CloseRule;
  agents?: AgentRules; classes?: Record<string, string[]>; difficulty_rules?: DifficultyRule[]; environments?: string[];
}
export interface Effective extends Settings { chain: Scope[]; autonomy_source: Scope }
export interface AutonomyStatus { level: number; enforced_level: number; available: boolean; source: string; policy?: Autonomy | null; paused: boolean; failures: number; pause_after: number; agents_enabled: boolean }
export interface SettingsAnswer { scope: Scope; own: Settings; effective: Effective; autonomy: AutonomyStatus; can_change: boolean }

export interface Agent {
  id: string; name: string; description?: string; owner: string; scope: Scope; projects: string[]; types?: string[]; labels?: string[]; concurrency: number; per_day: number;
  may_merge: boolean; model?: string; class?: string; budget_tokens_day?: number; budget_cents_day?: number; item_tokens?: number; item_cents?: number; item_seconds?: number;
  used_tokens_day: number; used_cents_day: number; disabled: boolean; token_hint?: string; token_at?: string; last_seen?: string; created_at: string;
  active: number; today: number; queued: number; in_review: number;
}
export interface NewAgent {
  name: string; description?: string; scope: Scope; projects?: string[]; concurrency?: number; per_day?: number; may_merge?: boolean; model?: string; class?: string;
  budget_tokens_day?: number; budget_cents_day?: number;
}
export interface AgentPatch {
  model?: string; class?: string; description?: string; concurrency?: number; per_day?: number; disabled?: boolean; may_merge?: boolean;
  budget_tokens_day?: number; budget_cents_day?: number;
}
export interface AgentLogEntry { at: string; item?: string; action: string; detail?: string }
export interface SpendRow {
  name: string; kind: string; budget_cents?: number; spent_cents: number; remaining_cents?: number; budget_tokens?: number; spent_tokens: number; remaining_tokens?: number; seconds: number; period?: string;
}
export interface Sample { id: string; scope: Scope; repo: string; pull: string; agent?: string; flagged_at: string; reviewed_by?: string; reviewed_at?: string; note?: string }
export interface Verdict { decision: string; criteria: { criterion: string; met: boolean; evidence: string }[]; risks: string[]; scope_ok: boolean; confidence: number; summary: string }
export interface AgentReview { id: string; repo: string; pull: string; agent: string; verdict: Verdict; counts_as_approval: boolean; advice?: string; at: string }
export interface InboxEntry { id: number; kind: string; item_id?: string; ref?: string; title?: string; text: string; at: string; read: boolean }
export interface AgentWork { review: Item[]; recent: AgentLogEntry[] }
export interface HomeWork { assigned: Item[]; assigned_total: number; unread: number; agents: AgentWork; samples: Sample[] }

export interface Occurrence { dedupe_key: string; title: string; body?: string; labels?: string[]; priority?: string; severity: number; evidence?: string[] }
export interface ProposalGroup { source: string; group: string; title: string; state: string; severity: number; count: number; unresolved: number; item?: string; occurrences: Occurrence[]; report_url?: string }
export interface ProposeResult { received: number; new: number; updated: number; dismissed: number; fixed: number; waiting: number; opened: number; folded: number }
export interface GroupEdit { title?: string; body?: string; priority?: string }

const enc = encodeURIComponent;
const sc = (s: string) => "scope=" + enc(s);
const qs = (o: Record<string, string | number | boolean | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== "" && v !== false) p.set(k, String(v));
  const s = p.toString();
  return s ? "?" + s : "";
};
const B = "/api/v1/work";

/** A scope written project:owner/name, unit:name, organization or installation, as the object the server reads. */
export function scopeObject(s: string): Scope {
  const i = s.indexOf(":");
  if (i < 0) return s.includes("/") ? { kind: "project", id: s } : { kind: s };
  return { kind: s.slice(0, i), id: s.slice(i + 1) };
}

export const workApi = {
  list: (scope: string | null, o: { q?: string; sort?: string; desc?: boolean; limit?: number; offset?: number; parent?: string } = {}) =>
    call<Page>("GET", `${B}/items${qs({ scope: scope ?? undefined, ...o })}`),
  create: (b: NewItem) => call<Item>("POST", `${B}/items`, b),
  get: (ref: string) => call<ItemDetail>("GET", `${B}/items/${enc(ref)}`),
  update: (ref: string, p: Patch) => call<Item>("PATCH", `${B}/items/${enc(ref)}`, p),
  rank: (ref: string, where: "top" | "bottom" | "before" | "after", other?: string) => call<Item>("POST", `${B}/items/${enc(ref)}/rank`, { where, other: other ?? "" }),
  move: (ref: string, state: string, before?: string) => call<Item>("POST", `${B}/items/${enc(ref)}/move`, { state, before: before ?? "" }),
  takeOver: (ref: string) => call<Item>("POST", `${B}/items/${enc(ref)}/take-over`, {}),
  history: (ref: string) => call<{ item: string; events: WorkEvent[] }>("GET", `${B}/items/${enc(ref)}/history`).then((r) => r.events),
  comments: (ref: string) => call<{ comments: Comment[] }>("GET", `${B}/items/${enc(ref)}/comments`).then((r) => r.comments),
  comment: (ref: string, body: string) => call<Comment>("POST", `${B}/items/${enc(ref)}/comments`, { body }),
  editComment: (id: string, body: string) => call<Comment>("PATCH", `${B}/comments/${enc(id)}`, { body }),
  commentEdits: (id: string) => call<{ edits: Comment[] }>("GET", `${B}/comments/${enc(id)}/edits`).then((r) => r.edits),
  addLink: (ref: string, kind: string, to: string) => call<Link>("POST", `${B}/items/${enc(ref)}/links`, { kind, to }),
  removeLink: (id: string) => call<{ removed: boolean }>("DELETE", `${B}/links/${enc(id)}`),
  attachmentUrl: (id: string) => `${B}/attachments/${enc(id)}`,
  views: (scope: string) => call<{ views: View[] }>("GET", `${B}/views?${sc(scope)}`).then((r) => r.views),
  saveView: (scope: string, name: string, query: string, shared: boolean) => call<View>("POST", `${B}/views`, { scope, name, query, shared }),
  deleteView: (id: string) => call<{ removed: boolean }>("DELETE", `${B}/views/${enc(id)}`),

  board: (scope: string, o: { q?: string; lanes?: string; sprint?: string } = {}) => call<Board>("GET", `${B}/board${qs({ scope, ...o })}`),
  milestones: (scope: string) => call<{ milestones: Milestone[] }>("GET", `${B}/milestones?${sc(scope)}`).then((r) => r.milestones),
  createMilestone: (scope: string, b: { title: string; description?: string; due_at?: string }) => call<Milestone>("POST", `${B}/milestones`, { scope, ...b }),
  updateMilestone: (id: string, b: { title?: string; state?: string; due_at?: string; description?: string }) => call<Milestone>("PATCH", `${B}/milestones/${enc(id)}`, b),
  sprints: (scope: string) => call<{ sprints: Sprint[] }>("GET", `${B}/sprints?${sc(scope)}`).then((r) => r.sprints),
  sprint: (id: string) => call<Sprint>("GET", `${B}/sprints/${enc(id)}`),
  createSprint: (scope: string, b: SprintIn) => call<Sprint>("POST", `${B}/sprints`, { ...b, scope }),
  updateSprint: (id: string, b: SprintIn) => call<Sprint>("PATCH", `${B}/sprints/${enc(id)}`, b),
  startSprint: (id: string) => call<Sprint>("POST", `${B}/sprints/${enc(id)}/start`, {}),
  completeSprint: (id: string, carryTo: string) => call<Sprint>("POST", `${B}/sprints/${enc(id)}/complete`, { carry_to: carryTo }),
  burndown: (id: string) => call<Burndown>("GET", `${B}/sprints/${enc(id)}/burndown`),
  velocity: (scope: string) => call<Velocity>("GET", `${B}/charts/velocity?${sc(scope)}`),
  flow: (scope: string) => call<Cfd>("GET", `${B}/charts/flow?${sc(scope)}`),
  times: (scope: string) => call<TimeStats>("GET", `${B}/charts/times?${sc(scope)}`),
  roadmap: (scope: string) => call<{ rows: RoadmapRow[] }>("GET", `${B}/roadmap?${sc(scope)}`).then((r) => r.rows),
  settings: (scope: string) => call<SettingsAnswer>("GET", `${B}/settings?${sc(scope)}`),
  putSettings: (scope: string, settings: Settings) => call<SettingsAnswer>("PUT", `${B}/settings`, { scope, settings }),
  clearPause: (scope: string, reason: string) => call<{ cleared: boolean }>("POST", `${B}/autonomy/clear-pause`, { Scope: scope, Reason: reason }),
  inbox: () => call<{ entries: InboxEntry[]; unread: number; email: string }>("GET", `${B}/inbox`),
  markRead: (ids: number[]) => call<{ ok: boolean }>("POST", `${B}/inbox/read`, { ids }),
  home: () => call<HomeWork>("GET", `${B}/home`),

  proposals: (scope: string, state: string) => call<{ groups: ProposalGroup[] }>("GET", `${B}/proposals${qs({ scope, state })}`).then((r) => r.groups),
  acceptProposals: (scope: string, source: string, groups: string[], edits?: Record<string, GroupEdit>) => call<ProposeResult>("POST", `${B}/proposals/accept`, { scope, source, groups, edits }),
  dismissProposals: (scope: string, source: string, groups: string[], reason: string) => call<{ dismissed: number }>("POST", `${B}/proposals/dismiss`, { scope, source, groups, reason }),
  mergeProposals: (scope: string, source: string, groups: string[], into: string) => call<{ merged: boolean }>("POST", `${B}/proposals/merge`, { scope, source, groups, into }),
  health: (repository: string) => call<ProposeResult>("POST", `${B}/health`, { repository }),
  agents: () => call<{ agents: Agent[] }>("GET", `${B}/agents`).then((r) => r.agents),
  createAgent: (b: NewAgent) => call<{ agent: Agent; token: string; note: string }>("POST", `${B}/agents`, b),
  updateAgent: (name: string, p: AgentPatch) => call<Agent>("PATCH", `${B}/agents/${enc(name)}`, p),
  rotateAgent: (name: string) => call<{ agent: Agent; token: string; note: string }>("POST", `${B}/agents/${enc(name)}/rotate-token`, {}),
  agentActions: (name: string) => call<{ agent: string; actions: AgentLogEntry[] }>("GET", `${B}/agents/${enc(name)}/actions`).then((r) => r.actions),
  spend: () => call<{ rows: SpendRow[]; note: string }>("GET", `${B}/spend`),
  samples: () => call<{ samples: Sample[] }>("GET", `${B}/samples`).then((r) => r.samples),
  completeSample: (id: string, note: string) => call<{ reviewed: boolean }>("POST", `${B}/samples/${enc(id)}/complete`, { Note: note }),
  pullReviews: (repo: string, pull: number | string) => call<{ reviews: AgentReview[] }>("GET", `${B}/pull-reviews?repo=${enc(repo)}&pull=${enc(String(pull))}`).then((r) => r.reviews),
};
