import { vi } from "vitest";
import type { Agent, Board, Burndown, Cfd, Item, ItemDetail, Milestone, SettingsAnswer, Sprint, TimeStats, Velocity } from "../work/workApi";

/** Work tracking as the server answers it: items, a board, a sprint, charts, agents and settings, for the page and a11y tests. */
export const item = (over: Partial<Item> = {}): Item => ({
  id: "it_1", ref: "PAY-1", scope: { kind: "project", id: "acme/shop" }, number: 1, type: "story", title: "Round prices to the cent", body: "Prices show **two** decimals.\n\n- cart\n- receipt",
  state: "in_progress", state_name: "In progress", category: "doing", priority: "high", estimate: 3, rank: 1000, labels: ["pricing"], assignees: [{ name: "ada", kind: "person" }],
  version: 4, created_at: "2026-11-10T09:00:00Z", updated_at: "2026-11-20T09:00:00Z", reporter: "sam", comment_count: 1, ...over,
});

export const detail = (over: Partial<ItemDetail> = {}): ItemDetail => ({
  ...item(), criteria: [{ id: "c1", text: "Receipt shows two decimals", done: true }, { id: "c2", text: "Cart shows two decimals", done: false }],
  links: [{ id: "l1", kind: "blocks", from: "PAY-1", to: "PAY-2", to_title: "Ship the receipt", to_state: "backlog", created_at: "2026-11-11T09:00:00Z" }],
  code: [{ id: "k1", kind: "pull", repo: "acme/shop", ref: "7", title: "Round prices", state: "merged", closes: true, created_at: "2026-11-12T09:00:00Z" }],
  attachments: [{ id: "a1", name: "receipt.png", content_type: "image/png", size: 4096, sha256: "ab", kind: "image", by: "ada", at: "2026-11-12T09:00:00Z" }],
  delivery: { merged: true, sentence: "Merged, on dev since 10:15, not yet on prod.", environments: [{ environment: "dev", running: true, since: "2026-11-20T10:15:00Z", version: "1.4.2" }, { environment: "prod", running: false }] },
  usage: null, actions: ["edit", "comment", "move", "link", "rank"], ...over,
});

export const agentDetail = (): ItemDetail => detail({
  ref: "PAY-9",
  claim: { agent: "docs-agent", claimed_at: "2026-11-21T08:00:00Z", heartbeat_at: "2026-11-21T08:30:00Z", lease_until: "2026-11-21T09:30:00Z" },
  assignees: [{ name: "docs-agent", kind: "agent" }], usage: { tokens: 12000, cents: 245, seconds: 1800, exceeded: false }, actions: ["edit", "comment", "move", "link", "take_over"],
});

export const board = (): Board => ({
  scope: { kind: "project", id: "acme/shop" },
  columns: [
    { state: "ready", name: "Ready", category: "todo", count: 1, points: 2, shown: 1, items: [item({ id: "it_2", ref: "PAY-2", title: "Ship the receipt", state: "ready", state_name: "Ready", category: "todo", priority: "normal" })] },
    { state: "in_progress", name: "In progress", category: "doing", wip: 1, count: 2, over_wip: true, points: 5, shown: 2, items: [item(), item({ id: "it_3", ref: "PAY-3", title: "Tax rounding", priority: "urgent" })] },
    { state: "done", name: "Done", category: "done", count: 0, points: 0, shown: 0, items: [] },
  ],
});

export const sprint = (over: Partial<Sprint> = {}): Sprint => ({
  id: "sp_1", scope: { kind: "project", id: "acme/shop" }, name: "Sprint 4", goal: "Prices are right", start_at: "2026-11-09T00:00:00Z", end_at: "2026-11-20T00:00:00Z", state: "active",
  capacity: { ada: 8, sam: 5 }, unit: "points", committed: 21, completed: 13, added: 3, carried_over: 2, items: 9, items_done: 5, total_capacity: 13,
  warning: "21 points are committed against 13 points of capacity. Consider moving 8 points to the next sprint.", ...over,
});

export const burndown = (): Burndown => ({ sprint: "Sprint 4", unit: "points", committed: 21, points: [
  { date: "2026-11-09", remaining: 21, ideal: 21, done: 0 }, { date: "2026-11-10", remaining: 18, ideal: 17, done: 3 }, { date: "2026-11-11", remaining: 15, ideal: 13, done: 6 } ] });

export const velocity = (): Velocity => ({ unit: "points", average: 14.5, sprints: [{ sprint: "Sprint 2", committed: 16, completed: 14, unit: "points" }, { sprint: "Sprint 3", committed: 18, completed: 15, unit: "points" }] });
export const flow = (): Cfd => ({
  states: [{ key: "ready", name: "Ready", category: "todo" }, { key: "in_progress", name: "In progress", category: "doing" }, { key: "done", name: "Done", category: "done" }],
  days: [{ date: "2026-11-09", counts: { ready: 5, in_progress: 2, done: 1 } }, { date: "2026-11-10", counts: { ready: 4, in_progress: 3, done: 2 } }, { date: "2026-11-11", counts: { ready: 3, in_progress: 3, done: 4 } }],
});
export const times = (): TimeStats => ({
  count: 4, lead_median_days: 5, lead_p85_days: 9, lead_mean_days: 6, cycle_median_days: 3, cycle_p85_days: 6, cycle_mean_days: 4, items: [],
  histogram: [{ label: "0-2", count: 1 }, { label: "3-5", count: 2 }, { label: "6-9", count: 1 }],
});
export const milestone = (): Milestone => ({ id: "m1", scope: { kind: "project", id: "acme/shop" }, title: "Release 1.5", due_at: "2026-12-01T00:00:00Z", state: "open", total: 10, done: 4, points: 30, points_done: 12, percent: 40 });

export const agent = (over: Partial<Agent> = {}): Agent => ({
  id: "ag_1", name: "docs-agent", description: "Keeps the docs current", owner: "sam", scope: { kind: "installation" }, projects: [], concurrency: 1, per_day: 5, may_merge: false, model: "model-a", class: "small",
  budget_cents_day: 1000, used_cents_day: 245, used_tokens_day: 12000, disabled: false, token_hint: "x9f2", created_at: "2026-11-01T00:00:00Z", active: 1, today: 2, queued: 3, in_review: 1, ...over,
});

export const settings = (over: Partial<SettingsAnswer> = {}): SettingsAnswer => ({
  scope: { kind: "project", id: "acme/shop" }, own: {}, can_change: true,
  effective: {
    chain: [{ kind: "project", id: "acme/shop" }, { kind: "installation" }], autonomy_source: { kind: "" }, estimate_unit: "points",
    states: [{ key: "ready", name: "Ready", category: "todo" }, { key: "in_progress", name: "In progress", category: "doing", wip: 3 }, { key: "done", name: "Done", category: "done" }],
    transitions: [{ to: "done", who: "human" }], types: [{ key: "issue", name: "Issue" }, { key: "bug", name: "Bug" }], fields: [], close: { on: "merge", state: "done", verbs: ["fixes", "closes"] }, agents: { enabled: true },
  },
  autonomy: { level: 1, enforced_level: 1, available: true, source: "default", paused: false, failures: 0, pause_after: 3, agents_enabled: true }, ...over,
});

/** Answers for installServer (src/test/mockApi.ts): every work address with fixtures. */
export function workAnswers(): Record<string, unknown> {
  const B = "/api/v1/work";
  return {
    [`${B}/items`]: { items: [item(), item({ id: "it_2", ref: "PAY-2", title: "Ship the receipt", state: "ready", state_name: "Ready", category: "todo", priority: "urgent", assignees: [{ name: "docs-agent", kind: "agent" }] })], total: 2, limit: 25, offset: 0 },
    [`${B}/items/PAY-1`]: detail(), [`${B}/items/PAY-9`]: agentDetail(),
    [`${B}/items/PAY-1/comments`]: { comments: [{ id: "cm1", author: "ada", author_kind: "person", body: "Looks right to me.", created_at: "2026-11-20T09:00:00Z", edits: 1 }] },
    [`${B}/items/PAY-1/history`]: { item: "PAY-1", events: [{ seq: 1, at: "2026-11-10T09:00:00Z", actor: "sam", actor_kind: "person", kind: "created" }, { seq: 2, at: "2026-11-11T09:00:00Z", actor: "docs-agent", actor_kind: "agent", via: "api", kind: "state", data: { from: "ready", to: "in_progress" } }] },
    [`${B}/views`]: { views: [{ id: "v1", name: "My bugs", query: "type:bug assignee:me", shared: false }] },
    [`${B}/board`]: board(), [`${B}/sprints`]: { sprints: [sprint(), sprint({ id: "sp_2", name: "Sprint 5", state: "planned" })] },
    [`${B}/sprints/sp_1`]: sprint(), [`${B}/sprints/sp_1/burndown`]: burndown(),
    [`${B}/charts/velocity`]: velocity(), [`${B}/charts/flow`]: flow(), [`${B}/charts/times`]: times(),
    [`${B}/roadmap`]: { rows: [{ kind: "epic", ref: "PAY-5", title: "Checkout rewrite", start_at: "2026-11-01T00:00:00Z", due_at: "2026-12-15T00:00:00Z", percent: 35, total: 8, done: 3 }, { kind: "milestone", title: "Release 1.5", due_at: "2026-12-01T00:00:00Z", percent: 40, total: 10, done: 4 }] },
    [`${B}/proposals`]: { groups: [
      { source: "health", group: "repository-hygiene", title: "Set up the repository basics ({n} left)", state: "pending", severity: 2, count: 3, unresolved: 2, report_url: "https://ci.example/report",
        occurrences: [{ dedupe_key: "no-readme", title: "Add a README", body: "A new reader has nowhere to start.", severity: 1, evidence: ["https://ci.example/e1"] }, { dedupe_key: "no-license", title: "Add a LICENSE", severity: 1 }] },
      { source: "health", group: "ci", title: "Add a pipeline", state: "pending", severity: 3, count: 1, unresolved: 1, occurrences: [] }] },
    [`${B}/milestones`]: { milestones: [milestone()] }, [`${B}/settings`]: settings(),
    [`${B}/agents`]: { agents: [agent(), agent({ id: "ag_2", name: "review-agent", disabled: true })] }, [`${B}/spend`]: { rows: [{ name: "docs-agent", kind: "agent", budget_cents: 1000, spent_cents: 245, remaining_cents: 755, spent_tokens: 12000, seconds: 1800 }], note: "what agents reported" },
    [`${B}/agents/docs-agent/actions`]: { agent: "docs-agent", actions: [{ at: "2026-11-21T08:00:00Z", item: "PAY-1", action: "claim", detail: "" }] },
    [`${B}/inbox`]: { entries: [{ id: 1, kind: "assigned", ref: "PAY-1", title: "Round prices", text: "PAY-1 was assigned to you", at: "2026-11-20T09:00:00Z", read: false }, { id: 2, kind: "mention", text: "Ada mentioned you", at: "2026-11-19T09:00:00Z", read: true }], unread: 1, email: "not available" },
    [`${B}/home`]: { assigned: [item()], assigned_total: 1, unread: 1, agents: { review: [item({ id: "it_4", ref: "PAY-4", title: "Docs for refunds" })], recent: [{ at: "2026-11-21T08:00:00Z", item: "PAY-4", action: "hand_back" }] }, samples: [] },
    [`${B}/pull-reviews`]: { reviews: [{ id: "r1", repo: "acme/shop", pull: "1", agent: "review-agent", at: "2026-11-21T08:00:00Z", counts_as_approval: false, advice: "the project lets only a person's review count",
      verdict: { decision: "approve", summary: "Looks correct.", confidence: 0.82, scope_ok: true, risks: ["Rounding of negative prices"], criteria: [{ criterion: "Receipt shows two decimals", met: true, evidence: "price_test.go line 12" }] } }] },
  };
}

export interface Call { method: string; url: string; body?: unknown }

/** A fetch that answers from a table of "METHOD /path" to a value or a function, and records every call. */
export function serve(table: Record<string, unknown | ((body: unknown, url: URL) => unknown)>): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://x");
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url: url.pathname + url.search, body });
    const hit = table[`${method} ${url.pathname}`];
    if (hit === undefined) return new Response(JSON.stringify({ error: `no answer for ${method} ${url.pathname}` }), { status: 404 });
    const out = typeof hit === "function" ? (hit as (b: unknown, u: URL) => unknown)(body, url) : hit;
    return new Response(JSON.stringify(out), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  return calls;
}
