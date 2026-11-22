// Calls for pull requests. They mirror internal/api/forge_pulls*.go.

import { call } from "./api";
import { qs } from "./browseApi";

export interface Action { name: string; available: boolean; reason?: string; risk?: string; suggestion?: string }
export interface Check { name: string; status: string; required: boolean; run_id?: string; project?: string; run_number?: number }
export interface Readiness {
  can_merge: boolean; why?: string; can_bypass?: boolean; approvals: number; changes_requested_by: string[]; open_threads: number;
  checks: Check[]; methods: string[]; default_method: string; blockers: { code: string; why: string }[];
  needs_owner_approval?: { path: string; owners: string[] }[]; queue?: boolean;
}
export interface QueueEntry { pull_number: number; title?: string; position: number; state: "waiting" | "testing" | "removed" | "merged"; method: string; queued_by?: string; reason?: string; runs?: string[]; head_sha?: string; tested_sha?: string }
export interface OwnersView { present: boolean; problems: { line: number; message: string }[]; files: { path: string; owners: string[] }[]; owners: string[] }
export interface Pull {
  id: string; number: number; repository?: string; title: string; body: string; author: string; source_branch: string; target_branch: string;
  state: "open" | "merged" | "closed"; draft: boolean; labels: string[]; assignees: string[]; requested_reviewers: string[]; requested_reviewer_groups?: string[];
  head_sha: string; base_sha: string; merge_base?: string; merge_commit?: string; merge_method?: string; merged_by?: string; reverts?: number;
  auto_merge?: string; linked_runs: string[]; created_at: string; updated_at: string; merged_at?: string; closed_at?: string; revisions: number;
  mergeability: { state: "clean" | "conflicts" | "checking" | "unknown"; conflicting_files?: string[]; behind: number; ahead: number; reason?: string };
  // Present in the full view only.
  readiness?: Readiness; actions?: Action[]; fetch_ref?: string; your_role?: string; queue?: QueueEntry;
}
export interface FileChange { path: string; old_path?: string; status: string; additions: number; deletions: number; binary: boolean; similarity?: number; too_large?: boolean }
export interface Patch { path?: string; diff: string; truncated?: boolean; held?: boolean; binary?: boolean; bytes: number }
export interface Commit { sha: string; author: string; authored_at: string; subject: string; body?: string }
export interface Comment {
  id: string; review_id?: string; thread_id?: string; author: string; body: string; path?: string; commit_sha?: string; side?: string; line?: number; start_line?: number;
  has_suggestion?: boolean; applied_commit?: string; outdated?: boolean; resolved?: boolean; resolved_by?: string; deleted?: boolean; created_at: string;
}
export interface Thread { id: string; path: string; line: number; start_line?: number; side: string; outdated: boolean; resolved: boolean; resolved_by?: string; comments: Comment[] }
export interface Review { id: string; author: string; verdict: "comment" | "approve" | "request_changes"; body?: string; commit_sha: string; state: string; dismissed_reason?: string; created_at: string; stale?: boolean }
export interface Revision { seq: number; head_sha: string; base_sha: string; forced: boolean; actor?: string; created_at: string }
export interface Conversation { comments: Comment[]; threads: Thread[]; reviews: Review[]; revisions: Revision[] }
export interface BranchComparison {
  base: string; head: string; base_sha?: string; head_sha?: string; merge_base?: string; ahead: number; behind: number; commits: Commit[]; existing_pull?: number;
  changes: { files: FileChange[]; total: number; additions: number; deletions: number };
  mergeability: Pull["mergeability"];
}

export interface MergeRule {
  pattern: string; required_approvals: number; approval_role: string; required_checks: string[]; require_up_to_date: boolean;
  require_resolved_threads: boolean; no_self_approval: boolean; require_signed_commits: boolean; allow_admin_bypass: boolean;
  require_owner_approval?: boolean; dismiss_stale_approvals?: boolean; require_latest_push_approved_by_other?: boolean; merge_queue?: boolean;
  required_checks_by_path?: { paths: string[]; check: string }[];
}
export interface PullSettings {
  allow_merge: boolean; allow_squash: boolean; allow_rebase: boolean; default_method: string; delete_branch_after_merge: boolean;
  dismiss_stale_approvals: boolean; merge_message_template: string; squash_message_template: string; assign_reviewers?: string;
}

const enc = encodeURIComponent;
const base = (path: string) => `/api/v1/repositories/${path.split("/").map(enc).join("/")}`;
export const pullsApi = {
  list: (path: string, q: { state?: string; author?: string; reviewer?: string; label?: string }) =>
    call<{ items: Pull[]; total: number }>("GET", `${base(path)}/pulls${qs(q)}`),
  get: (path: string, n: number) => call<Pull>("GET", `${base(path)}/pulls/${n}`),
  files: (path: string, n: number, since?: number) => call<{ files: FileChange[]; total: number; additions: number; deletions: number }>("GET", `${base(path)}/pulls/${n}/files${qs({ limit: 500, since })}`),
  patch: (path: string, n: number, file: string, full: boolean, since?: number) => call<Patch>("GET", `${base(path)}/pulls/${n}/patch${qs({ path: file, full, since })}`),
  commits: (path: string, n: number) => call<{ items: Commit[] }>("GET", `${base(path)}/pulls/${n}/commits`).then((r) => r.items),
  conversation: (path: string, n: number) => call<Conversation>("GET", `${base(path)}/pulls/${n}/conversation`),
  compare: (path: string, head: string, target?: string) => call<BranchComparison>("GET", `${base(path)}/compare${qs({ head, base: target })}`),
  create: (path: string, b: { title: string; body: string; source_branch: string; target_branch?: string; draft?: boolean }) => call<Pull>("POST", `${base(path)}/pulls`, b),
  update: (path: string, n: number, b: Record<string, unknown>) => call<Pull>("PATCH", `${base(path)}/pulls/${n}`, b),
  merge: (path: string, n: number, b: { method?: string; delete_branch?: boolean; expect_head?: string; bypass_reason?: string }) => call<{ pull: Pull; commit: string }>("POST", `${base(path)}/pulls/${n}/merge`, b),
  revert: (path: string, n: number) => call<Pull>("POST", `${base(path)}/pulls/${n}/revert`, {}),
  comment: (path: string, n: number, b: { body: string; path?: string; line?: number; start_line?: number; reply_to?: string }) => call<Comment>("POST", `${base(path)}/pulls/${n}/comments`, b),
  updateComment: (path: string, n: number, id: string, b: { body?: string; resolved?: boolean }) => call<Comment>("PATCH", `${base(path)}/pulls/${n}/comments/${enc(id)}`, b),
  applySuggestion: (path: string, n: number, id: string) => call<{ commit: string }>("POST", `${base(path)}/pulls/${n}/comments/${enc(id)}/apply`, {}),
  review: (path: string, n: number, b: { verdict: string; body: string }) => call<Review>("POST", `${base(path)}/pulls/${n}/reviews`, b),
  requestReview: (path: string, n: number, reviewers: string[]) => call<Pull>("POST", `${base(path)}/pulls/${n}/review-requests`, { reviewers }),
  unrequestReview: (path: string, n: number, reviewer: string) => call<Pull>("DELETE", `${base(path)}/pulls/${n}/review-requests/${enc(reviewer)}`),
  rules: (path: string) => call<{ rules: MergeRule[]; actions: Action[] }>("GET", `${base(path)}/merge-rules`),
  setRules: (path: string, rules: MergeRule[]) => call<{ rules: MergeRule[]; actions: Action[] }>("PUT", `${base(path)}/merge-rules`, { rules }),
  settings: (path: string) => call<PullSettings>("GET", `${base(path)}/pull-settings`),
  setSettings: (path: string, b: Partial<PullSettings>) => call<PullSettings>("PUT", `${base(path)}/pull-settings`, b),
  owners: (path: string, n: number) => call<{ file: string; branch: string; owners: OwnersView }>("GET", `${base(path)}/pulls/${n}/owners`),
  viewed: (path: string, n: number) => call<{ viewed: string[] }>("GET", `${base(path)}/pulls/${n}/viewed`).then((r) => r.viewed),
  setViewed: (path: string, n: number, paths: string[], viewed: boolean) => call<{ viewed: string[] }>("PUT", `${base(path)}/pulls/${n}/viewed`, { paths, viewed }).then((r) => r.viewed),
  enqueue: (path: string, n: number, method?: string) => call<QueueEntry>("POST", `${base(path)}/pulls/${n}/queue`, { method }),
  dequeue: (path: string, n: number) => call<void>("DELETE", `${base(path)}/pulls/${n}/queue`),
  updateBranch: (path: string, n: number) => call<{ head: string }>("POST", `${base(path)}/pulls/${n}/update-branch`, {}),
  requestReviewGroup: (path: string, n: number, group: string) => call<Pull>("POST", `${base(path)}/pulls/${n}/review-requests`, { groups: [group] }),
  unrequestReviewGroup: (path: string, n: number, group: string) => call<Pull>("DELETE", `${base(path)}/pulls/${n}/review-requests/${enc("group:" + group)}`),
  inbox: () => call<{ items: { repository: string; number: number; title: string; kind: string; why: string }[] }>("GET", "/api/v1/pulls/inbox").then((r) => r.items),
  dismiss: (path: string, n: number, id: string, reason: string) => call<void>("POST", `${base(path)}/pulls/${n}/reviews/${enc(id)}/dismiss`, { reason }),
};
