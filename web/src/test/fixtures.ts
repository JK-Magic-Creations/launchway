import type { Approval, EnvironmentView, Me, Project, RunSummary, Runner, StepView } from "../api";

export const me = (over: Partial<Me> = {}): Me => ({
  user: "sam", groups: [], auth_mode: "oidc", via: "session", admin: true, runners: true, roles: {}, mcp: { enabled: true, path: "/mcp" }, ...over,
});

export const run = (over: Partial<RunSummary> = {}): RunSummary => ({
  id: "run_1", project: "shop-api", number: 1, pipeline: "shop-api", status: "succeeded", cancel_requested: false, event: "push", ref: "refs/heads/main",
  branch: "main", tag: "", sha: "a41f9c2aaaa", actor: "sam", message: "Add a gift card", author: "Sam", created_at: 1_000, started_at: 1_000, finished_at: 2_000,
  steps: [], waiting: [], draft: false, needs_newer_server: false, ...over,
});

export const project = (over: Partial<Project> = {}): Project => ({
  name: "shop-api", url: "https://git.example/acme/shop-api.git", default_branch: "main", poll_seconds: 30, last_fetch: 0, decisions: [], last_run: null,
  push: null, editor: { drafts_allow: "admin", may_draft: true, draft_secrets: false, may_save: true, may_force: false }, ...over,
});

export const runner = (over: Partial<Runner> = {}): Runner => ({ id: "r1", name: "build-1", executor: "docker", version: "1", last_seen: 1, online: true, ...over });

export const approval = (over: Partial<Approval> = {}): Approval => ({
  run_id: "run_2", project: "shop-web", run_number: 118, step_id: "approve", message: "Deploy shop-web to production", started_by: "Sam",
  commit_subject: "Release", short_sha: "c0a3b19", branch: "main", waiting_since: 1, deadline: 2, ...over,
});

export const step = (over: Partial<StepView> = {}): StepView => ({
  run_id: "run_1", id: "test", position: 2, status: "failed", attempt: 1, queued_at: 0, started_at: 0, finished_at: 0, exit_code: 1, error: "exit code 1",
  skip_kind: "", runner: "", outputs: {}, excerpt: "FAIL TestPrice", retry: 0, timeout: "10m", deadline: 0, decided_by: "", decided_at: 0, comment: "", inputs: [], ...over,
});

export const environment = (over: Partial<EnvironmentView> = {}): EnvironmentView => ({ name: "staging", target: "home", queue: "q", projects: ["shop-api"], services: [], ...over });
