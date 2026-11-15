// Types mirror the Go API in internal/engine and internal/source.

export type RunStatus = "queued" | "running" | "succeeded" | "failed" | "canceled";
export type StepStatus =
  | "pending" | "queued" | "leased" | "running" | "waiting"
  | "succeeded" | "failed" | "skipped" | "canceled";

export interface StepBrief { id: string; status: StepStatus }

export interface RunSummary {
  id: string;
  project: string;
  number: number;
  pipeline: string;
  status: RunStatus;
  cancel_requested: boolean;
  event: string;
  ref: string;
  branch: string;
  tag: string;
  sha: string;
  actor: string;
  message: string;
  author: string;
  created_at: number;
  started_at: number;
  finished_at: number;
  steps: StepBrief[];
  /** Approval steps waiting for someone to decide. */
  waiting: string[];
  /** A run of pipeline text from the editor that is not in Git (event "draft"). */
  draft: boolean;
  /** Names the draft's text: sha256:… */
  pipeline_hash?: string;
  /** A re-run: the run it re-runs, its number, and the step it started from. */
  rerun_of?: string;
  rerun_of_number?: number;
  rerun_from?: string;
  /** The run has events or a pipeline written by a newer Launchway than this server, which leaves it alone. */
  needs_newer_server: boolean;
  /** A run of a pull request (event "pull_request"): its number. */
  pull_request?: number;
  /** Why the run was asked to cancel, when not by hand, such as "superseded by run #12". */
  cancel_reason?: string;
  /** The newer run that superseded this one, and its number. */
  superseded_by?: string;
  superseded_by_number?: number;
  /** The run's test results added up; absent when no step has reported any. */
  tests?: TestTotals;
}

/** Test results counted. Flaky: passed only after the test tool ran them again. */
export interface TestTotals {
  total: number; passed: number; failed: number; errored: number; skipped: number; flaky: number;
  duration_ms: number;
  /** Not every result was kept: a file was cut short, or there were more than a run keeps. */
  truncated?: boolean;
}

export interface TestFile {
  path: string; format?: "junit" | "go-test-json"; bytes: number; cases: number; error?: string; warning?: string;
}

export interface StepTests extends TestTotals {
  step: string;
  attempt: number;
  reused_from?: string;
  reused_from_number?: number;
  stored: number;
  pruned?: boolean;
  files: TestFile[];
  notes?: string[];
  attempts: ({ attempt: number } & TestTotals)[];
}

export interface RunTests {
  totals: TestTotals | null;
  steps: StepTests[];
  /** Steps whose definition asks for test reports. */
  declared: string[];
  flaky: number;
  new_failures: number;
  previous?: { id: string; number: number };
}

export type TestStatus = "passed" | "failed" | "errored" | "skipped";

export interface TestCase {
  id: number; key: string; run_id: string; step: string; attempt: number;
  suite: string; name: string; file?: string; status: TestStatus; duration_ms: number;
  message?: string; has_details: boolean; report_file?: string;
  flaky?: boolean; new_failure?: boolean;
  details?: string; output?: string;
}

export interface CasePage {
  cases: TestCase[]; matching: number; counts: Record<string, number>; offset: number; limit: number;
}

export interface CaseQuery { status?: string; q?: string; step?: string; attempt?: number; limit?: number; offset?: number }

export interface HistoryResult {
  run_id: string; number: number; sha: string; ref: string; created_at: number; step: string; attempt: number;
  status: TestStatus; duration_ms: number; message?: string; flaky?: boolean;
}

export interface TestHistory { key: string; suite: string; name: string; file?: string; ref: string; results: HistoryResult[] }

export interface TestStat {
  key: string; suite: string; name: string; file?: string; runs: number;
  avg_ms?: number; max_ms?: number; failures?: number; last_failed?: number;
  commits?: number; tool_retry?: boolean; last_seen?: number;
}

export interface OverviewRun { id: string; number: number; sha: string; status: RunStatus; created_at: number; tests: TestTotals | null }

export interface TestOverview {
  ref: string; runs: OverviewRun[]; slowest: TestStat[]; failing: TestStat[]; flaky: TestStat[]; refs: string[];
}

/** What kind of step it is: a step that runs a script has none. */
export type StepKind = "approval" | "deploy" | "rollback" | "rollout";
/** What a waiting deploy step waits for: its environment's lock, or what deploy runners observe. */
export type WaitKind = "lock" | "observe";
/** Why a step was skipped: condition, upstream, not_needed (a rollback with nothing to roll back) or superseded. */
export type SkipKind = "" | "condition" | "upstream" | "not_needed" | "superseded";

// ---- deployments ----

/** How a deployment ended. */
export type Verdict = "deployed" | "failed" | "rolled_back" | "rollback_failed" | "abandoned";

/** Where a deployment stands, as worked out from what a deploy runner observed. */
export type DeployPhase =
  | "unobserved" | "app_missing" | "source_error" | "syncing" | "sync_blocked" | "sync_failed"
  | "progressing" | "degraded" | "rollout_progressing" | "rollout_paused" | "rollout_aborted" | "healthy"
  | "command_pending";

/** Names a deployment, and the run and step that made it. */
export interface DeployRef { deployment: string; run: string; number: number; step?: string; version?: string }

/** An Argo CD application, as a deploy runner saw it. */
export interface DeployApp {
  found: boolean;
  source_error?: string;
  sync: string; // Synced, OutOfSync or Unknown
  health: string; // Healthy, Progressing, Degraded, Suspended, Missing or Unknown
  health_message?: string;
  revision?: string;
  /** Whether revision includes the deployment's commit; absent when it could not be checked. */
  contains?: boolean;
  source: number;
  automated: boolean;
  operation?: {
    phase: string; message?: string; revision?: string; contains?: boolean; started_at?: number; finished_at?: number;
  };
  reconciled_at?: number;
  resources?: { group?: string; kind: string; namespace?: string; name: string; found: boolean; sync?: string; health?: string; message?: string }[];
  conditions?: string[];
  /** What the deploy runner could not find out, such as the sync windows; never decides anything. */
  notes?: string[];
  /** For an application that syncs by itself: why its sync windows hold it now. */
  window_blocked?: string;
}

/** An Argo Rollouts rollout, as a deploy runner saw it. */
export interface DeployRollout {
  found: boolean;
  phase?: string; // Healthy, Progressing, Paused, Degraded
  message?: string;
  generation: number;
  observed_generation?: string;
  observed: boolean;
  workload_ref?: boolean;
  current_pod_hash?: string;
  stable_rs?: string;
  strategy?: string; // canary or blueGreen
  /** The current canary step, of steps; absent when there are none. */
  step?: number;
  steps: number;
  step_names?: string[];
  canary_weight?: number;
  paused: boolean;
  pause_reasons?: string[];
  pause_until?: number;
  aborted: boolean;
  /** Waits at a pause no timer ends: someone (or a command) must promote it. */
  pause_indefinite?: boolean;
  /** A full promotion was asked for and is not done yet. */
  promote_full?: boolean;
  replicas: number;
  ready: number;
  updated: number;
  available: number;
}

export interface DeployMetric {
  name: string; phase: string; message?: string;
  count: number; successful: number; failed: number; inconclusive: number; error: number;
}

/** One of a rollout's own analysis runs. */
export interface DeployAnalysis {
  name: string;
  phase: string; // Pending, Running, Successful, Failed, Error, Inconclusive
  message?: string;
  kind?: string; // Step or Background
  step_index?: number;
  pod_hash?: string;
  metrics?: DeployMetric[];
}

/** What a deploy runner saw of a deployment at one moment. error: it could not see the application at all. */
export interface DeploySnapshot {
  error?: string;
  app?: DeployApp;
  rollout?: DeployRollout;
  analysis?: DeployAnalysis[];
}

export interface TimelineEntry { at: number; step: string; phase: string; detail: string }

export interface DeploymentView {
  id: string;
  run_id: string;
  run_number: number;
  project: string;
  step: string;
  rollback_step?: string;
  environment: string;
  service: string;
  target: string;
  application: string;
  mode: string;
  version: string;
  previous_id?: string;
  previous_version?: string;
  repo?: string;
  branch?: string;
  commit?: string;
  changed: boolean;
  revert?: string;
  phase: DeployPhase | "";
  detail: string;
  verdict?: Verdict;
  opened_at: number;
  deployed_at?: number;
  closed_at?: number;
  snapshot?: DeploySnapshot;
  observed_at?: number;
  observed_by?: string;
  /** It holds its environment's lock. */
  holding: boolean;
  /** Only on a run's deployments, not in the history. */
  timeline?: TimelineEntry[];
  /** The deployment's pod template hash, once the rollout has shown it. */
  pod_hash?: string;
  /** Commands given to the deployment's rollout, oldest first. */
  commands?: CommandView[];
  /** The runner observing the deployment's target, and until when (Unix ms); absent when none does. */
  observer?: string;
  observer_expires?: number;
}

/** What an environment runs, as far as Launchway knows. */
export interface CurrentView { deployment: string; version: string; run: string; run_number: number; since: number }

/** One service of a project in an environment. */
export interface UnitView {
  project: string;
  service: string;
  current?: CurrentView;
  /** The deployment holding the lock. */
  lock?: DeployRef;
  /** Runs waiting for the lock. */
  waiting: DeployRef[];
  /** The newest deployment, with what was last observed. */
  last?: DeploymentView;
  /** What a deploy runner last saw of the service while nothing was deploying it. */
  live?: LiveView;
}

export interface EnvironmentView {
  name: string;
  target: string;
  queue: string;
  projects: string[];
  services: UnitView[];
  /** The runner that observes the target, and until when; absent when none does. */
  observer?: string;
  observer_expires?: number;
  /** The person who started a run may not promote or retry its deployments here. */
  protected?: boolean;
}

export interface Condition { branch?: string[]; tag?: string[]; event?: string[] }

/** Where a secret is kept. The value itself never leaves the runner. */
export interface SecretRef { store: string; key: string; field?: string }

export interface StepView {
  run_id: string;
  id: string;
  position: number;
  status: StepStatus;
  attempt: number;
  queued_at: number;
  started_at: number;
  finished_at: number;
  exit_code: number | null;
  error: string;
  skip_kind: string;
  runner: string;
  outputs: Record<string, string>;
  /** A failed attempt's last lines of output, masked like the log. */
  excerpt: string;
  container?: string;
  image?: string;
  run?: string;
  needs?: string[];
  when?: Condition;
  secrets?: Record<string, SecretRef>;
  retry: number;
  timeout: string;
  description?: string;
  /** For a template's step: the uses step it came from. */
  group?: string;
  /** For a step not expanded (the editor without libraries): the template it uses. */
  uses?: string;
  // Approvals only. A waiting approval started waiting at started_at.
  // kind is also "deploy" or "rollback" for the steps that deploy.
  kind?: StepKind;
  message?: string;
  deadline: number;
  decided_by: string;
  decided_at: number;
  comment: string;
  /** A waiting deploy or rollback step: what it waits for. Empty for an approval. */
  wait_kind?: WaitKind;
  /** A deploy step: the environment it deploys to. */
  environment?: string;
  /** A rollback step: the deploy step whose deployment it reverts. A rollout
   * step: the deploy step whose rollout it acts on. An approval: the deploy
   * step whose deployment it guards (a linked approval). */
  deployment?: string;
  /** A step a re-run reused: the run where it actually ran, its number, and the attempt there. */
  reused_from?: string;
  reused_from_number?: number;
  reused_attempt?: number;
  /** What each attempt was given, oldest first. */
  inputs: AttemptInputs[];
  /** The test result files the step declares, as globs. */
  reports?: string[];
  /** The step's test results, from the attempt that counts. */
  tests?: TestTotals;
}

/** Where a variable came from. */
export type InputOrigin = "step" | "container" | "needs" | "launchway" | "template";

export interface InputVar {
  name: string;
  value: string;
  from: InputOrigin;
  /** needs: the step the output came from; template: the step that used the template. */
  step?: string;
  /** needs: the output's key. */
  output?: string;
  /** Shortened to stay within the server's limits (the step received it in full). */
  truncated?: boolean;
}

/** What one attempt of a step was given. Secrets by reference only. */
export interface StepInputs {
  image?: string;
  container?: string;
  script: string;
  script_truncated?: boolean;
  library?: { name: string; commit: string };
  env: InputVar[];
  params?: InputVar[];
  secrets?: { name: string; ref: string }[];
  truncated?: boolean;
}

export interface AttemptInputs { attempt: number; inputs: StepInputs }

/** What re-running a run from a step would run again and reuse. */
export interface RerunPlan { run: string[]; reuse: string[] }

export interface RerunBrief { id: string; number: number; from: string; status: RunStatus; actor: string; created_at: number }

export interface GraphContainer {
  name: string; image: string; env?: Record<string, string>; cpu?: string; memory?: string; service?: boolean; command?: string[];
}

export interface GraphStep {
  id: string;
  description?: string;
  kind?: StepKind;
  message?: string;
  container?: string;
  image: string;
  run: string;
  needs?: string[];
  when?: Condition;
  env?: Record<string, string>;
  secrets?: Record<string, SecretRef>;
  retry: number;
  timeout: string;
  /** Not expanded: the template the step is replaced by, and the values it passes. */
  uses?: string;
  with?: Record<string, string>;
  /** Expanded: the uses step it came from, and the library whose files it works with. */
  group?: string;
  library?: string;
  needs_groups?: string[];
  reports?: { tests: string[] };
  /** A rollout, rollback or linked approval step: the deploy step it is about. */
  deployment?: string;
  /** A rollout step: what it does to the rollout. */
  action?: RolloutAction;
}

/** A library a pipeline uses, and the commit its version resolved to when the run was created. */
export interface GraphLibrary {
  name: string;
  repo: string;
  version: string;
  override?: string;
  commit?: string;
  ref_kind?: "tag" | "branch" | "commit";
}

/** A step that used a template, once expanded. */
export interface GraphGroup {
  id: string;
  parent?: string;
  description?: string;
  uses: string;
  library: string;
  template: string;
  params?: Record<string, string>;
  steps: string[];
  outputs?: Record<string, { step: string; key: string }>;
}

export interface Graph {
  name: string;
  on: { push?: { branches?: string[]; tags?: string[] }; pull_request?: Record<string, never> };
  /** A new run of a push or pull request cancels the older ones of the same ref still going. */
  supersede?: boolean;
  libraries?: GraphLibrary[];
  containers?: GraphContainer[];
  steps: GraphStep[];
  groups?: GraphGroup[];
  /** Set when the pipeline is the one configured on the server, not a file in the repository. */
  origin?: { kind: "server"; text: string };
}

export interface RunDetail extends RunSummary {
  source: { kind: string; url?: string; path?: string; libraries?: { name: string; url: string; commit: string }[] };
  graph: Graph;
  levels: string[][];
  step_list: StepView[];
  /** The runs that re-run this one, oldest first. */
  reruns: RerunBrief[];
  /** What in the run this server does not understand; empty when it understands all of it. */
  not_understood: string[];
  /** The run's deployments, newest first. */
  deployments?: DeploymentView[];
}

export interface LogLine { seq: number; at: number; stream: "stdout" | "stderr" | "system"; text: string }

export interface RunEvent {
  seq: number;
  run_id: string;
  type: string;
  at: number;
  data: Record<string, unknown>;
}

export interface Decision { at: number; ref: string; sha: string; outcome: "started" | "ignored" | "error"; detail: string; run_id?: string }

export interface PushSettings { branch_prefix: string; allow_direct: string[] | null }

/** The pipeline the server runs for a project whose repository has no pipeline file. */
export interface ProjectPipeline {
  library: string;
  version: string;
  template: string;
  with?: Record<string, string> | null;
  on: { push?: { branches?: string[]; tags?: string[] }; pull_request?: Record<string, never> };
  supersede?: boolean;
}

/** What the caller may do with the pipeline editor on a project. */
export interface EditorRights {
  drafts_allow: "admin" | "operator" | "none";
  may_draft: boolean;
  draft_secrets: boolean;
  may_save: boolean;
  may_force: boolean;
}

export interface Project {
  name: string;
  url: string;
  default_branch: string;
  poll_seconds: number;
  last_fetch: number;
  error?: string;
  decisions: Decision[];
  last_run: RunSummary | null;
  /** How the editor may save to the repository; null if it may not. */
  push: PushSettings | null;
  /** The pipeline configured on the server, used when a commit has no pipeline file. */
  pipeline?: ProjectPipeline;
  editor: EditorRights;
}

/** The pipeline file at the tip of a branch, as the editor opens it. */
export interface PipelineFile {
  ref: string;
  kind: "branch" | "tag";
  sha: string;
  subject: string;
  author: string;
  committed_at: number;
  path: string;
  /** False: the branch has no pipeline yet, and text is a starting point. */
  exists: boolean;
  /** No file, and the project runs the pipeline configured on the server: text is that pipeline. */
  configured: boolean;
  text: string;
}

export interface SaveRequest {
  base_ref: string;
  base_sha: string;
  pipeline: string;
  message: string;
  /** Empty for a new branch, or base_ref to commit to it directly. */
  branch: string;
  force?: boolean;
}

export interface SaveResult {
  branch: string;
  sha: string;
  base_ref: string;
  base_sha: string;
  path: string;
  direct: boolean;
  pull_request_url?: string;
}

/** The pipeline text a run ran: from Git, or kept with a draft run. */
export interface RunPipelineText { text: string; draft: boolean; hash?: string; origin?: "server" }

export interface Ref { name: string; kind: "branch" | "tag"; sha: string; subject: string; author: string; committed_at: number }

export interface Approval {
  run_id: string;
  project: string;
  run_number: number;
  step_id: string;
  message: string;
  started_by: string;
  commit_subject: string;
  short_sha: string;
  branch: string;
  tag?: string;
  pull_request?: number;
  waiting_since: number;
  deadline: number;
}

// online: seen within the last two minutes. Runners not seen for a day are no longer listed.
export interface Runner {
  id: string; name: string; executor: string; version: string; last_seen: number; online: boolean;
  /** A deploy runner (executor "deploy"): its pool, and the targets it serves. */
  pool?: string; targets?: string[];
}

/** A problem in pipeline text; with file set, in that template of the library instead. */
export interface Problem { file?: string; library?: string; line?: number; column?: number; path?: string; message: string }

export interface ValidateResult {
  ok: boolean; problems?: Problem[]; graph?: Graph; levels?: string[][];
  /** Templates were expanded from the libraries' current versions. */
  expanded?: boolean;
}

/** Library name → tag, branch or commit, used instead of the version the pipeline names. */
export type LibraryOverrides = Record<string, string>;

export class ApiError extends Error {
  constructor(public status: number, message: string, public problems?: Problem[]) { super(message); }
}

/** Who is signed in, and what they may do. The server checks every request
 * regardless; the UI uses this only to hide what would be refused. */
export type Role = "viewer" | "operator" | "admin";
export interface Me {
  user: string;
  groups: string[];
  auth_mode: "none" | "oidc";
  via: "none" | "session" | "token";
  admin: boolean;
  runners: boolean;
  roles: Record<string, Role>;
}

export interface ApiToken { id: string; name: string; created_at: number; expires_at: number; last_used: number }

export interface AuditEntry {
  id: number; at: number; user: string; source: string; action: string;
  project: string; target: string; outcome: "ok" | "denied" | "failed"; detail: string;
}

export interface AuditQuery { user?: string; project?: string; action?: string; from?: number; to?: number; before?: number; limit?: number }

/** Sent when any call finds the caller is not signed in. */
export const SIGNED_OUT = "launchway:signed-out";

interface CallOptions {
  raw?: boolean;
  /** Made by the page on its own (polling, refreshing after a live update),
   * so it does not keep an idle session alive. */
  background?: boolean;
}

async function call<T>(method: string, path: string, body?: unknown, opts: CallOptions = {}): Promise<T> {
  const headers: Record<string, string> = {};
  const init: RequestInit = { method, headers, credentials: "same-origin" };
  // Every request that changes something says it comes from these pages;
  // the server refuses such requests without it.
  if (method !== "GET") headers["X-Launchway-CSRF"] = "1";
  if (opts.background) headers["X-Launchway-Background"] = "1";
  if (body !== undefined) {
    if (opts.raw) {
      init.body = body as string;
      headers["Content-Type"] = "text/yaml";
    } else {
      init.body = JSON.stringify(body);
      headers["Content-Type"] = "application/json";
    }
  }
  const resp = await fetch(path, init);
  const text = await resp.text();
  let data: unknown = undefined;
  try { data = text ? JSON.parse(text) : undefined; } catch { /* not JSON */ }
  if (!resp.ok) {
    if (resp.status === 401) window.dispatchEvent(new Event(SIGNED_OUT));
    const body = data as { error?: string; problems?: Problem[] } | undefined;
    throw new ApiError(resp.status, body?.error ?? (text || resp.statusText), body?.problems);
  }
  return data as T;
}

export const api = {
  info: () => call<{ version: string; auth_mode: "none" | "oidc"; actor?: string }>("GET", "/api/v1/info"),
  me: () => call<Me>("GET", "/api/v1/me"),
  logout: () => call<void>("POST", "/auth/logout"),
  runs: (q: { project?: string; status?: string; limit?: number } = {}, background = false) => {
    const p = new URLSearchParams();
    if (q.project) p.set("project", q.project);
    if (q.status) p.set("status", q.status);
    if (q.limit) p.set("limit", String(q.limit));
    return call<RunSummary[]>("GET", "/api/v1/runs?" + p.toString(), undefined, { background });
  },
  run: (id: string, background = false) => call<RunDetail>("GET", `/api/v1/runs/${id}`, undefined, { background }),
  events: (id: string) => call<RunEvent[]>("GET", `/api/v1/runs/${id}/events`),
  pipelineFile: (id: string) => call<RunPipelineText>("GET", `/api/v1/runs/${id}/pipeline-file`),
  logs: (id: string, step: string, attempt: number, after = 0) =>
    call<{ attempt: number; lines: LogLine[] }>("GET",
      `/api/v1/runs/${id}/steps/${encodeURIComponent(step)}/logs?attempt=${attempt}&after=${after}`),
  cancel: (id: string) => call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/cancel`),
  resume: (id: string) => call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/resume`),
  rerunPlan: (id: string, from: string) => call<RerunPlan>("GET", `/api/v1/runs/${id}/rerun?from=${encodeURIComponent(from)}`),
  rerun: (id: string, from: string) => call<RunSummary>("POST", `/api/v1/runs/${id}/rerun`, { from }),
  approve: (id: string, step: string, comment: string) =>
    call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/steps/${encodeURIComponent(step)}/approve`, { comment }),
  reject: (id: string, step: string, comment: string) =>
    call<{ ok: boolean }>("POST", `/api/v1/runs/${id}/steps/${encodeURIComponent(step)}/reject`, { comment }),
  approvals: (background = false) => call<Approval[]>("GET", "/api/v1/approvals", undefined, { background }),
  projects: (background = false) => call<Project[]>("GET", "/api/v1/projects", undefined, { background }),
  project: (name: string) => call<Project>("GET", `/api/v1/projects/${encodeURIComponent(name)}`),
  refs: (name: string) => call<Ref[]>("GET", `/api/v1/projects/${name}/refs`),
  pipeline: (name: string, ref: string) =>
    call<PipelineFile>("GET", `/api/v1/projects/${encodeURIComponent(name)}/pipeline?ref=${encodeURIComponent(ref)}`),
  draftRun: (name: string, ref: string, pipeline: string, libraries?: LibraryOverrides) =>
    call<RunSummary>("POST", `/api/v1/projects/${encodeURIComponent(name)}/draft-runs`, { ref, pipeline, libraries }),
  savePipeline: (name: string, req: SaveRequest) =>
    call<SaveResult>("POST", `/api/v1/projects/${encodeURIComponent(name)}/pipeline`, req),
  startRun: (name: string, ref: string, libraries?: LibraryOverrides) =>
    call<RunSummary>("POST", `/api/v1/projects/${encodeURIComponent(name)}/runs`, { ref, libraries }),
  runners: (background = false) => call<Runner[]>("GET", "/api/v1/runners", undefined, { background }),
  /** With a project (or expand), templates are expanded from the server's libraries. */
  validate: (yaml: string, opts: { project?: string; expand?: boolean } = {}) => {
    const p = new URLSearchParams();
    if (opts.project) p.set("project", opts.project);
    else if (opts.expand) p.set("expand", "1");
    const q = p.toString();
    return call<ValidateResult>("POST", "/api/v1/validate" + (q ? "?" + q : ""), yaml, { raw: true });
  },
  tokens: () => call<ApiToken[]>("GET", "/api/v1/tokens"),
  createToken: (name: string, expiresInDays: number) =>
    call<ApiToken & { token: string }>("POST", "/api/v1/tokens", { name, expires_in_days: expiresInDays }),
  revokeToken: (id: string) => call<void>("DELETE", `/api/v1/tokens/${encodeURIComponent(id)}`),
  runTests: (id: string, background = false) => call<RunTests>("GET", `/api/v1/runs/${id}/tests`, undefined, { background }),
  testCases: (id: string, q: CaseQuery, background = false) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
    return call<CasePage>("GET", `/api/v1/runs/${id}/tests/cases?` + p.toString(), undefined, { background });
  },
  testCase: (id: string, caseId: number) => call<TestCase>("GET", `/api/v1/runs/${id}/tests/cases/${caseId}`),
  projectTests: (name: string, ref?: string, runs?: number) => {
    const p = new URLSearchParams();
    if (ref) p.set("ref", ref);
    if (runs) p.set("runs", String(runs));
    return call<TestOverview>("GET", `/api/v1/projects/${encodeURIComponent(name)}/tests?` + p.toString());
  },
  testHistory: (name: string, key: string, ref?: string) => {
    const p = new URLSearchParams({ key });
    if (ref) p.set("ref", ref);
    return call<TestHistory>("GET", `/api/v1/projects/${encodeURIComponent(name)}/tests/history?` + p.toString());
  },
  environments: (background = false) => call<EnvironmentView[]>("GET", "/api/v1/environments", undefined, { background }),
  deployments: (q: { environment?: string; project?: string; service?: string; limit?: number }, background = false) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
    return call<DeploymentView[]>("GET", "/api/v1/deployments?" + p.toString(), undefined, { background });
  },
  deploymentControls: (run: string, dep: string, background = true) =>
    call<DeploymentControls>("GET", `/api/v1/runs/${run}/deployments/${encodeURIComponent(dep)}/controls`, undefined, { background }),
  command: (run: string, dep: string, action: RolloutAction, comment: string) =>
    call<CommandView>("POST", `/api/v1/runs/${run}/deployments/${encodeURIComponent(dep)}/${action}`, { comment }),
  withdrawCommand: (run: string, dep: string, cmd: string) =>
    call<{ ok: boolean }>("POST", `/api/v1/runs/${run}/deployments/${encodeURIComponent(dep)}/commands/${encodeURIComponent(cmd)}/withdraw`),
  audit: (q: AuditQuery) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
    return call<{ entries: AuditEntry[]; next_before: number }>("GET", "/api/v1/audit?" + p.toString());
  },
};

// ---- controlling deployments ----

/** What can be done to a deployment's rollout. */
export type RolloutAction = "promote" | "promote-full" | "abort" | "retry";

/** A command given to a deployment's rollout, and what became of it. A
 * pending command with detail was handed back by a runner that could not
 * reach the cluster (detail says why). */
export interface CommandView {
  id: string;
  deployment: string;
  run_id: string;
  step: string;
  action: RolloutAction;
  actor: string;
  comment?: string;
  origin: "person" | "step" | "pauses" | "on_cancel";
  /** Where the rollout was when the command was given: it is applied only if it is still there. step is 0-based. */
  expect: { pod_hash: string; step?: number; paused?: boolean };
  state: "pending" | "taken" | "done" | "withdrawn";
  result?: "applied" | "already_applied" | "not_applied" | "failed";
  detail?: string;
  taken_by?: string;
  taken_until?: number;
  tries: number;
  withdrawn_by?: string;
  at: number;
  done_at?: number;
}

/** Whether a command can be given now; forbidden: the reason is who is asking, not where the rollout is. */
export interface Control { action: RolloutAction; available: boolean; reason?: string; forbidden?: boolean }

export interface DeploymentControls {
  environment: string;
  protected: boolean;
  /** In the order promote, promote-full, abort, retry. */
  controls: Control[];
  /** The caller may withdraw a command nobody has taken; else withdraw_reason says why not. */
  withdraw: boolean;
  withdraw_reason: string;
}

/** What a deploy runner last saw of an idle service. */
export interface LiveView {
  deployment: string;
  snapshot?: DeploySnapshot;
  observed_at: number;
  observed_by: string;
  drift?: "" | "out_of_sync" | "differs" | "missing";
  drift_detail?: string;
}
