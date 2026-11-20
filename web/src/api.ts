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
  /** When the run's timeout will cancel it (Unix ms). Absent when it has none, or while an approval waits (that time does not count). */
  deadline_at?: number;
  /** The run's place in its pipeline's lane (runs of the project take turns); absent when it has none. */
  lane?: LaneInfo;
  /** The run's test results added up; absent when no step has reported any. */
  tests?: TestTotals;
}

/** Where a run stands in its lane. */
export interface LaneInfo {
  name: string;
  key: string;
  /** The lane in words: release/ref main. */
  label: string;
  /** holding: it goes; waiting: queued behind another run; replaced: a newer run took its place; skipped: the lane was busy. */
  state: "holding" | "waiting" | "replaced" | "skipped";
  /** 1 for the run next in line, for a waiting run. */
  position?: number;
  /** The run that holds the lane (waiting or skipped), or that took this one's place (replaced). */
  behind?: { id: string; number: number };
  reason?: string;
}

/** Test results counted. Flaky: passed only after the test tool ran them again. */
export interface TestTotals {
  total: number; passed: number; failed: number; errored: number; skipped: number; flaky: number;
  duration_ms: number;
  /** Not every result was kept: a file was cut short, or there were more than a run keeps. */
  truncated?: boolean;
}

/** The format a test report file was read as. A newer server may name others. */
export type TestFormat =
  | "junit" | "go-test-json" | "cucumber-json" | "karate-json" | "playwright-json" | "mochawesome" | "ctrf"
  | "testng" | "nunit3" | "xunit2" | "trx" | "tap" | "robot";

export interface TestFile {
  path: string; format?: TestFormat | string; bytes: number; cases: number; error?: string; warning?: string;
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
  /** Tags or labels the report gives the test (@smoke, a JUnit property, a Playwright tag). */
  tags?: string[];
  /** What the test ran against: a Playwright project or browser, a matrix entry. */
  variant?: string;
  /** In a list: the result has steps or other structure to show when opened. */
  has_steps?: boolean;
  // Only in one result fetched on its own:
  /** How many times the test tool ran the test again. */
  retries?: number;
  /** Earlier tries inside the test tool, oldest first; the result is the last. */
  attempts?: TestAttempt[];
  steps?: TestStep[];
  /** Not every step was kept. */
  steps_cut?: boolean;
  attachments?: TestAttachment[];
  properties?: { name: string; value: string }[];
}

export interface TestAttempt { status: TestStatus; duration_ms: number; message?: string }

export interface TestHeader { name: string; value: string }

/** An HTTP request a step made and its answer (Karate and other API test tools). */
export interface HTTPExchange {
  method: string; url: string; status?: number; duration_ms?: number;
  request_headers?: TestHeader[]; request_body?: string;
  response_headers?: TestHeader[]; response_body?: string;
}

/** One step of a test: a Gherkin step, a Playwright step, a Robot keyword. */
export interface TestStep {
  /** 0 for a top-level step. */
  depth?: number;
  /** Given, When, Then, And, *; a Robot keyword's library. */
  keyword?: string;
  name: string; status: TestStatus; duration_ms?: number;
  error?: string;
  /** A doc string, data table or log the step wrote. */
  text?: string;
  http?: HTTPExchange;
  /** Indexes into the result's attachments. */
  attachments?: number[];
}

export type AttachmentKind = "image" | "video" | "trace" | "text" | "html" | "file";

export interface TestAttachment {
  name: string; kind: AttachmentKind | string; content_type?: string; size?: number;
  artifact?: string; path?: string;
  /** Why it cannot be shown or fetched. */
  problem?: string;
}

export interface Facet { value: string; count: number }

export interface CasePage {
  cases: TestCase[]; matching: number; counts: Record<string, number>; offset: number; limit: number;
  tags?: Facet[]; variants?: Facet[]; suites?: Facet[];
}

export type CaseSort = "" | "failures" | "slowest" | "name";

export interface CaseQuery {
  status?: string; q?: string; step?: string; attempt?: number; limit?: number; offset?: number;
  tag?: string; suite?: string; variant?: string; sort?: CaseSort;
}

/** A test whose result differs from, or matches a failure of, the previous run. */
export interface CompareCase {
  key: string; id: number; suite: string; name: string; variant?: string;
  status: TestStatus; before: TestStatus | ""; message?: string; duration_ms: number;
}

export interface TestCompare {
  previous?: { id: string; number: number };
  new_failures: CompareCase[]; fixed: CompareCase[]; still_failing: CompareCase[];
  counts: { new_failures: number; fixed: number; still_failing: number };
}

// ---- coverage ----

/** Covered items of one kind; a total of 0 means it was not measured. */
export interface CoverageCounter { covered: number; total: number }

export interface CoverageTotals {
  lines: CoverageCounter; branches: CoverageCounter; functions: CoverageCounter;
  /** What lines counts: "lines", or "statements" (Go). */
  unit?: string;
}

export interface CoverageReportFile { path: string; format?: string; bytes: number; files: number; error?: string; warning?: string }

export interface CoverageThreshold { lines?: number; branches?: number; functions?: number }

export interface CoverageStep extends CoverageTotals {
  step: string; attempt: number; reused_from?: string; reused_from_number?: number;
  source_files: number; stored: number; pruned?: boolean;
  reports: CoverageReportFile[]; notes?: string[];
  /** Why the step failed its fail_under. */
  failed_under?: string;
  threshold?: CoverageThreshold;
}

export interface CoverageCompare { run: { id: string; number: number }; ref: string; totals: CoverageTotals }

export interface RunCoverage {
  totals: CoverageTotals | null;
  steps: CoverageStep[];
  declared: string[];
  previous?: CoverageCompare;
  base?: CoverageCompare;
}

export interface CoverageFileRow {
  step: string; path: string; package?: string;
  lines: CoverageCounter; branches: CoverageCounter; functions: CoverageCounter;
  /** Points of line coverage against the previous run; absent when not known. */
  change?: number | null;
}

export interface CoveragePackage {
  package: string; files: number; lines: CoverageCounter; branches: CoverageCounter; functions: CoverageCounter;
}

export interface CoverageFiles {
  files: CoverageFileRow[]; packages: CoveragePackage[]; matching: number; offset: number; limit: number;
}

export type CoverageSort = "path" | "lines" | "missed";

export interface CoverageTrend {
  ref: string;
  runs: { id: string; number: number; sha: string; created_at: number; totals: CoverageTotals }[];
}

// ---- hosted HTML reports ----

export interface HostedReport {
  step: string; attempt: number; name: string; artifact: string; index: string;
  files: number; bytes: number; has_index: boolean; expires_at?: number;
  /** Opens the report: a signed link that works for a few hours. */
  url?: string;
  base: string;
}

export interface RunReports {
  reports: HostedReport[];
  /** Reports the pipeline declares, as step/name. */
  declared: string[];
  origin: "sandboxed" | "separate";
}

export interface HistoryResult {
  run_id: string; number: number; sha: string; ref: string; created_at: number; step: string; attempt: number;
  status: TestStatus; duration_ms: number; message?: string; flaky?: boolean;
}

export interface TestHistory { key: string; suite: string; name: string; file?: string; ref: string; results: HistoryResult[] }

export interface TestStat {
  key: string; suite: string; name: string; file?: string; variant?: string; runs: number;
  avg_ms?: number; max_ms?: number; failures?: number; last_failed?: number;
  commits?: number; tool_retry?: boolean; last_seen?: number;
}

export interface OverviewRun { id: string; number: number; sha: string; status: RunStatus; created_at: number; tests: TestTotals | null }

export interface TestOverview {
  ref: string; runs: OverviewRun[]; slowest: TestStat[]; failing: TestStat[]; flaky: TestStat[]; refs: string[];
}

/** What kind of step it is: a step that runs a script has none. */
export type StepKind = "approval" | "deploy" | "rollback" | "rollout" | "image" | "verify" | "ai";
/** What a waiting deploy step waits for: its environment's lock, or what deploy runners observe; a verify step: a person (verify). */
export type WaitKind = "lock" | "observe" | "verify";
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

export interface Condition {
  branch?: string[]; tag?: string[]; event?: string[];
  /** A run input and the values that run the step (see equals). */
  input?: string;
  /** step.key: an output of a step this one needs; the step runs only when it is one of equals. */
  output?: string;
  equals?: string[];
  upstream?: string;
}

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
  /** A step queued for a retry that waits: when a runner may be given it (Unix ms). */
  not_before?: number;
  /** Why the last failed attempt failed: exit, timeout or infrastructure. */
  failure_cause?: string;
  retry_wait?: string;
  retry_backoff?: number;
  retry_on?: string[];
  /** The step failed for good and its pipeline allows that (on_failure: continue): shown as failed (allowed). */
  allowed_failure?: boolean;
  /** From the definition: continue, or empty for stop. */
  on_failure?: string;
  /** What each attempt was given, oldest first. */
  inputs: AttemptInputs[];
  /** The step failed because a policy in enforce mode refused it; error says which and why. */
  policy_blocked?: boolean;
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
  reports?: GraphReports;
  /** A rollout, rollback or linked approval step: the deploy step it is about. */
  deployment?: string;
  /** A rollout step: what it does to the rollout. */
  action?: RolloutAction;
  /** An image step: the container image it builds. */
  image_build?: ImageBuild;
  /** A verify step: what it watches and how it decides. */
  verify?: GraphVerify;
  /** An ai step: the model it asks and what. */
  ai?: GraphAI;
  /** A verify step watching an environment, not a deployment. */
  environment?: string;
  service?: string;
}

/** What a step reports, as its pipeline declares it. */
export interface GraphReports {
  tests?: string[] | null;
  test_attachments?: string;
  coverage?: string[] | null;
  coverage_fail_under?: CoverageThreshold;
  html?: { name: string; path: string; index?: string }[] | null;
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
  /** Why a run with image steps may wait long: no runner that builds images has been seen. */
  runner_hint?: string;
  /** Why no runner may take the run's waiting steps: pools, labels or what runners accept. */
  pool_hint?: string;
  /** The run's policy decisions summed up; absent if no policy judged it. */
  policy?: { decisions: number; verdict: PolicyVerdict; waived: number };
}

// ---- policies (schema 16) ----

export type PolicyVerdict = "pass" | "warn" | "block";
export type RuleVerdict = "pass" | "fail" | "waived";
export type PolicyPoint = "run" | "deploy" | "image";

export interface PolicyScope { projects?: string[]; environments?: string[]; branches?: string[]; events?: string[] }

export interface PolicyDef {
  name: string; description?: string; mode: "report" | "enforce"; scope: PolicyScope;
  rules: Record<string, unknown>; source: string; version?: string; file?: string; line?: number;
}

export interface EvidenceRef { kind: string; step?: string; run?: string; run_number?: number; text: string }

export interface RuleResult {
  rule: string; verdict: RuleVerdict; found: string; want: string;
  evidence?: EvidenceRef[]; waiver?: Waiver; waivers?: string[]; notes?: string[];
}

export interface PolicyResult { policy: PolicyDef; verdict: "pass" | "fail" | "waived"; rules: RuleResult[] }

export interface PolicyDecision {
  seq: number; point: PolicyPoint; step?: string; action?: string; environment?: string; at: number;
  verdict: PolicyVerdict; policies: PolicyResult[] | null;
  input: { project: string; run: string; starter?: string; protected?: boolean; evidence?: { findings?: FindingFact[] } };
}

export interface FindingFact { fingerprint: string; tool: string; rule: string; severity: string; file?: string; line?: number; step: string }

export interface Waiver {
  id: string; project: string; policy: string; rule: string; run?: string; run_number?: number; environment?: string;
  finding?: string; reason: string; granted_by: string; granted_at: number; expires_at: number;
  revoked_by?: string; revoked_at?: number; revoke_reason?: string; state?: "active" | "expired" | "revoked";
}

export interface RuleInfo { name: string; point: PolicyPoint; summary: string }

export interface RunPolicy {
  decisions: PolicyDecision[]; waivers: Waiver[]; can_waive: boolean; waive_refusal: string;
  rules: Record<string, string[]>; catalogue: RuleInfo[]; max_waiver_days: number;
}

export interface PolicySourceView {
  name: string; kind: "config" | "git"; library?: string; ref?: string; path?: string; projects: string[];
  version: string; loaded_at?: number; checked_at?: number; fetch_error?: string; fetch_error_at?: number;
  error?: string; policies: PolicyDef[];
}

export interface PolicyVerdictRow {
  policy: string; run_id: string; project: string; run_number: number; point: PolicyPoint; step?: string;
  environment?: string; version: string; mode: string; verdict: "pass" | "fail" | "waived"; at: number;
}

export interface PolicyOverview { catalogue: RuleInfo[]; sources: PolicySourceView[]; recent: PolicyVerdictRow[]; max_waiver_days: number }

export interface WaiverRequest {
  policy: string; rule: string; reason: string; expires_in_hours: number; run?: string; environment?: string; finding?: string;
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
  /** Its pool ("default" for build runners of the default pool), and for a deploy runner the targets it serves. */
  pool?: string; targets?: string[];
  /** The runner builds images (it has image builders). */
  images?: boolean;
  /** build or deploy. */
  kind?: string;
  /** identity: enrolled; shared: the shared runner token; pool: a deploy pool's token. */
  auth?: "identity" | "shared" | "pool";
  labels?: string[];
  os?: string; arch?: string;
  /** What its accept: section allows, as the runner reported it. */
  accept?: RunnerAccept;
  /** The enrolled runner's details; only for those who manage runners. */
  identity?: RunnerIdentity;
  /** The steps it holds now, of the projects the viewer may read. */
  work?: { run: string; project: string; number: number; step: string }[];
}

export interface RunnerAccept {
  projects?: string[]; repositories?: string[]; images?: string[]; images_by_digest?: boolean; executors?: string[];
  environments?: string[]; pull_requests?: boolean; drafts?: boolean; max_timeout?: string; env_deny?: string[];
}

export interface RunnerIdentity {
  id: string; name: string; pool: string; kind: string; key_fingerprint: string; key_created_at: number;
  join_token: string; enrolled_by: string; enrolled_at: number; labels: string[]; hostname: string;
  os: string; arch: string; version: string; executor: string; ephemeral: boolean; last_seen: number;
  revoked_at?: number; revoked_by?: string; revoke_reason?: string; rotate_asked?: number;
}

export interface RunnerPool {
  name: string; kind: "build" | "deploy"; projects: string[]; events: string[]; targets: string[];
  require_identity: boolean;
  /** Runners may also use a shared token: the shared runner token, or the deploy pool's own. */
  shared_token: boolean;
  /** False for the default pool when the configuration does not name it. */
  configured: boolean;
}

export interface RunnerPools { pools: RunnerPool[]; manage: boolean; identities: boolean }

export interface JoinToken {
  id: string; pool: string; labels: string[]; description: string; max_uses: number; uses: number; ephemeral: boolean;
  created_by: string; created_at: number; expires_at: number; last_used_at?: number; revoked_by?: string; revoked_at?: number;
  state: "active" | "used" | "expired" | "revoked";
}

export interface JoinTokenRequest { pool: string; labels?: string[]; description?: string; max_uses?: number; ttl?: string; ephemeral?: boolean }

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
  constructor(public status: number, message: string, public problems?: Problem[], public fields?: Record<string, string>) { super(message); }
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
  /** The MCP endpoint for agents: whether it is on, its path, and the OAuth resource if the provider's tokens work there. */
  mcp?: { enabled: boolean; path: string; oauth_resource?: string };
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

export async function call<T>(method: string, path: string, body?: unknown, opts: CallOptions = {}): Promise<T> {
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
    const body = data as { error?: string; problems?: Problem[]; fields?: Record<string, string> } | undefined;
    throw new ApiError(resp.status, body?.error ?? (text || resp.statusText), body?.problems, body?.fields);
  }
  return data as T;
}

export const api = {
  info: () => call<{ version: string; auth_mode: "none" | "oidc"; actor?: string }>("GET", "/api/v1/info"),
  me: () => call<Me>("GET", "/api/v1/me"),
  logout: () => call<void>("POST", "/auth/logout"),
  runs: (q: { project?: string; status?: string; lane?: string; limit?: number } = {}, background = false) => {
    const p = new URLSearchParams();
    if (q.project) p.set("project", q.project);
    if (q.status) p.set("status", q.status);
    if (q.lane) p.set("lane", q.lane);
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
  runnerPools: (background = false) => call<RunnerPools>("GET", "/api/v1/runner-pools", undefined, { background }),
  joinTokens: (background = false) => call<JoinToken[]>("GET", "/api/v1/runner-join-tokens", undefined, { background }),
  createJoinToken: (req: JoinTokenRequest) => call<{ token: string; join_token: JoinToken }>("POST", "/api/v1/runner-join-tokens", req),
  revokeJoinToken: (id: string) => call<JoinToken>("POST", `/api/v1/runner-join-tokens/${encodeURIComponent(id)}/revoke`),
  revokeRunner: (id: string, reason: string) => call<{ released: number }>("POST", `/api/v1/runners/${encodeURIComponent(id)}/revoke`, { reason }),
  rotateRunner: (id: string) => call<RunnerIdentity>("POST", `/api/v1/runners/${encodeURIComponent(id)}/rotate`),
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
  testCompare: (id: string, background = false) => call<TestCompare>("GET", `/api/v1/runs/${id}/tests/compare`, undefined, { background }),
  runCoverage: (id: string, background = false) => call<RunCoverage>("GET", `/api/v1/runs/${id}/coverage`, undefined, { background }),
  coverageFiles: (id: string, q: { step?: string; q?: string; sort?: CoverageSort; limit?: number; offset?: number }, background = false) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
    return call<CoverageFiles>("GET", `/api/v1/runs/${id}/coverage/files?` + p.toString(), undefined, { background });
  },
  projectCoverage: (name: string, ref?: string, runs?: number) => {
    const p = new URLSearchParams();
    if (ref) p.set("ref", ref);
    if (runs) p.set("runs", String(runs));
    return call<CoverageTrend>("GET", `/api/v1/projects/${encodeURIComponent(name)}/coverage?` + p.toString());
  },
  runReports: (id: string, background = false) => call<RunReports>("GET", `/api/v1/runs/${id}/reports`, undefined, { background }),
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
  artifactStore: (background = false) => call<ArtifactStore>("GET", "/api/v1/artifact-store", undefined, { background }),
  runArtifacts: (id: string, background = false) => call<RunArtifacts>("GET", `/api/v1/runs/${id}/artifacts`, undefined, { background }),
  deleteArtifact: (id: string, artifactId: string) =>
    call<ArtifactView>("DELETE", `/api/v1/runs/${id}/artifacts/${encodeURIComponent(artifactId)}`),
  runFindings: (id: string, q: FindingQuery = {}, background = false) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
    return call<RunFindings>("GET", `/api/v1/runs/${id}/findings?` + p.toString(), undefined, { background });
  },
  projectSecurity: (name: string, ref?: string, runs?: number) => {
    const p = new URLSearchParams();
    if (ref) p.set("ref", ref);
    if (runs) p.set("runs", String(runs));
    return call<ProjectSecurity>("GET", `/api/v1/projects/${encodeURIComponent(name)}/security?` + p.toString());
  },
  policies: (background = false) => call<PolicyOverview>("GET", "/api/v1/policies?recent=10", undefined, { background }),
  runPolicy: (id: string, background = false) => call<RunPolicy>("GET", `/api/v1/runs/${id}/policy`, undefined, { background }),
  waivers: (q: { project?: string; active?: boolean } = {}, background = false) => {
    const p = new URLSearchParams();
    if (q.project) p.set("project", q.project);
    if (q.active) p.set("active", "1");
    return call<{ waivers: Waiver[]; waive_projects: string[]; rules: Record<string, string[]> }>("GET", `/api/v1/waivers?${p}`, undefined, { background });
  },
  grantWaiver: (project: string, w: WaiverRequest) => call<Waiver>("POST", `/api/v1/projects/${encodeURIComponent(project)}/waivers`, w),
  revokeWaiver: (project: string, id: string, reason: string) =>
    call<Waiver>("POST", `/api/v1/projects/${encodeURIComponent(project)}/waivers/${encodeURIComponent(id)}/revoke`, { reason }),
  runImages: (id: string, background = false) => call<{ images: ImageView[] }>("GET", `/api/v1/runs/${id}/images`, undefined, { background }),
  images: (q: ImageQuery) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
    return call<{ hits: ImageHit[] }>("GET", "/api/v1/images?" + p.toString());
  },
  components: (name: string, version?: string, limit?: number) => {
    const p = new URLSearchParams({ name });
    if (version) p.set("version", version);
    if (limit) p.set("limit", String(limit));
    return call<ComponentSearch>("GET", "/api/v1/components?" + p.toString());
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

// ---- Artifacts, security findings and SBOMs ----

/** Added to the run as listed: its findings added up, and how many artifacts it keeps. */
export interface RunSummary {
  /** Absent when no step has reported any findings. */
  findings?: FindingCounts;
  /** Artifacts kept; absent when none. */
  artifacts?: number;
}

export interface ArtifactFileInfo { path: string; size: number; sha256: string; mode: number }

export type ArtifactStatus = "uploading" | "kept" | "failed" | "expired" | "deleted";

export interface ArtifactView {
  id: string; run_id: string; run_number: number; step: string; attempt: number; name: string;
  /** "sbom" for the SBOM a step reports. */
  kind?: string;
  status: ArtifactStatus;
  file_count: number; bytes: number; created_at: number;
  kept_at?: number; expires_at?: number; ended_at?: number; ended_by?: string; note?: string;
  /** Belongs to a step this run reused from the run run_id, an earlier one. */
  reused?: boolean;
  files: ArtifactFileInfo[];
  files_truncated?: boolean;
}

export interface RunArtifacts {
  artifacts: ArtifactView[];
  /** Steps that declare artifacts. */
  declared: string[];
  available: boolean;
  problem?: string;
  max_retain_ms: number;
}

/** What the database records as kept in one store; store is "" for files kept before schema 17. */
export interface StoreCount { store: string; files: number; bytes: number }

/** Where this server keeps artifacts (GET /api/v1/artifact-store). */
export interface ArtifactStore {
  store: string;
  available: boolean;
  problem?: string;
  why?: string;
  usage?: {
    used_bytes: number; object_bytes: number; objects: number; open_uploads: number; collect_bytes: number;
    limit_bytes: number; max_object_bytes: number;
  };
  kept: StoreCount;
  elsewhere: StoreCount[];
  advice: string[];
}

export type Severity = "critical" | "high" | "medium" | "low" | "unknown";

export interface FindingCounts {
  total: number; critical: number; high: number; medium: number; low: number; unknown: number;
  suppressed?: number;
  truncated?: boolean;
}

export interface FindingFile {
  path: string; format?: string; bytes: number; findings: number; tools?: string[]; error?: string; warning?: string;
}

export interface SBOMFile {
  path: string; format?: string; spec_version?: string; bytes: number; components: number; error?: string; warning?: string;
}

export interface RunLink { id: string; number: number }

export interface RuleView { name?: string; short?: string; full?: string; help?: string; help_uri?: string; tags?: string[] }

export type FindingStatus = "new" | "existing" | "fixed";

export interface FindingView {
  id: number; step: string; attempt: number; run_id: string; run_number: number; fingerprint: string;
  tool: string; rule_id: string; severity: Severity; level?: string; message: string;
  file?: string; start_line?: number; end_line?: number; start_column?: number;
  /** About a secret: its value is never kept. */
  secret?: boolean;
  status?: FindingStatus;
  first_seen?: RunLink;
  rule?: RuleView;
}

export interface StatusCounts { new: number; existing: number; fixed: number }

export interface StepFindings {
  step: string; attempt: number; counts: FindingCounts; tools: string[]; files: FindingFile[];
  notes?: string[]; stored: number; pruned?: boolean; reused_from?: RunLink;
}

export interface StepSBOM {
  step: string; attempt: number; components: number; stored: number; truncated?: boolean; pruned?: boolean;
  licences: Record<string, number>; files: SBOMFile[]; notes?: string[];
  /** The artifact that keeps the SBOM file, in the run run_id. */
  artifact?: string;
  run_id: string;
}

export interface FindingQuery {
  /** Comma separated. */
  severity?: string; tool?: string; status?: "" | FindingStatus; q?: string; step?: string; limit?: number; offset?: number;
}

export interface RunFindings {
  counts: FindingCounts | null;
  statuses: StatusCounts;
  previous?: RunLink;
  steps: StepFindings[];
  sboms: StepSBOM[];
  declared: string[];
  tools: string[];
  findings: FindingView[];
  matched: number;
}

export interface SecurityRun { id: string; number: number; sha: string; status: RunStatus; created_at: number; findings?: FindingCounts }

export interface ProjectSecurity {
  ref: string; refs: string[]; runs: SecurityRun[];
  latest?: RunLink; counts?: FindingCounts; statuses: StatusCounts; open: FindingView[]; open_matched: number;
}

export interface ComponentHit {
  project: string; run_id: string; run_number: number; ref: string; step: string; name: string;
  version?: string; type?: string; purl?: string; licences?: string; created_at: number;
}

export interface ComponentSearch { hits: ComponentHit[]; projects: ComponentHit[]; more?: boolean }

// ---- Container images ----

/** A value in an image step's settings, as written: text, a step's output, a fact about the run, or an input. */
export interface ImageValue {
  literal?: string;
  output?: { step: string; key: string };
  run?: "sha" | "short_sha" | "branch" | "tag" | "number";
  input?: string;
}

/** What an image step builds and where it pushes it, as the pipeline says. */
export interface ImageBuild {
  context: string;
  file: string;
  target?: string;
  platforms?: string[];
  build_args?: Record<string, ImageValue>;
  tags: { registry: string; repository: string; tag: ImageValue }[];
  /** Absent means true. */
  push?: boolean;
  cache?: { registry: string; repository: string };
  sbom?: boolean;
  provenance?: boolean;
  sign?: { runner?: boolean; key?: SecretRef; password?: SecretRef };
  scan?: { fail_on?: ScanLevel; mode: "report" | "enforce" };
}

export type ScanLevel = "critical" | "high" | "medium" | "low" | "";

export interface ImageScan {
  critical: number; high: number; medium: number; low: number; unknown: number;
  fail_on?: ScanLevel; mode?: "report" | "enforce"; failed?: boolean;
}

/** The image an image step built, from its latest attempt. */
export interface ImageView {
  step: string;
  attempt: number;
  outcome: "succeeded" | "failed" | "canceled";
  created_at: number;
  /** The index's digest, or the single manifest's. */
  digest: string;
  media_type?: string;
  /** Bytes, compressed, every platform together. */
  size: number;
  platforms?: { platform: string; digest: string; size: number }[];
  /** host/repo@sha256:…; empty when not pushed. */
  image?: string;
  /** host/repo:tag, pushed or not. */
  tags?: string[];
  pushed: boolean;
  /** Why it was not pushed. */
  not_pushed?: string;
  signed: boolean;
  key_id?: string;
  /** "runner", or a secret reference. */
  key_source?: string;
  signatures?: string[];
  /** "stored": kept as an artifact only; "attached": also signed and attached to the image. */
  provenance?: "" | "stored" | "attached";
  sbom?: boolean;
  scan?: ImageScan | null;
  builders?: string[];
  steps?: number;
  cached?: number;
  cache?: string;
  base_images?: string[];
  /** Names of the run's artifacts holding the SBOM and the provenance. */
  sbom_artifact?: string;
  provenance_artifact?: string;
}

/** An image found by digest, repository or project; kind says what matched. */
export interface ImageHit {
  project: string; run_id: string; run_number: number; ref: string; step: string; attempt: number;
  kind: "index" | "tag" | "platform";
  repository: string; tag?: string; digest: string; platform?: string;
  pushed: boolean; signed: boolean; outcome: "succeeded" | "failed" | "canceled"; created_at: number;
}

export interface ImageQuery { digest?: string; repository?: string; tag?: string; project?: string; limit?: number }

/** Where one attachment of a test result is served; download asks for a file. */
export function attachmentURL(run: string, caseId: number, n: number, download = false): string {
  return `/api/v1/runs/${run}/tests/cases/${caseId}/attachments/${n}${download ? "?download=1" : ""}`;
}

// ---- Verifications ----

/** How a verification, or one of its checks, stands. */
export type VerifyOutcome = "running" | "passed" | "failed" | "inconclusive";

export interface VerifyPoint { t: number; v: number }

export interface VerifyCluster { template: string; count: number; samples?: string[] }

/** What a deploy runner read in one interval. */
export interface VerifyInterval {
  index: number;
  start: number;
  end: number;
  metrics?: { name: string; canary?: VerifyPoint[]; stable?: VerifyPoint[]; error?: string }[];
  logs?: {
    name: string; lines: number; truncated?: boolean; clusters?: VerifyCluster[];
    stable?: VerifyCluster[]; stable_lines?: number; error?: string;
  }[];
}

/** A set of readings summed up; n is 0 when there were none. */
export interface VerifyStats { n: number; mean: number; sd: number; min: number; max: number }

/** One metric in one interval. */
export interface IntervalCheck {
  index: number;
  outcome: "passed" | "failed" | "no_data";
  /** The new version's mean in the interval. */
  value: number;
  stable_z?: number;
  baseline_z?: number;
  reason?: string;
}

export interface MetricVerdict {
  name: string;
  outcome: VerifyOutcome;
  reason: string;
  intervals?: IntervalCheck[];
  canary: VerifyStats;
  stable: VerifyStats;
  baseline: VerifyStats;
}

export type ClusterClass = "known" | "unknown" | "unexpected_frequency" | "no_reference";

export interface ClusterVerdict {
  template: string;
  count: number;
  class: ClusterClass;
  /** Counts as an error. */
  error: boolean;
  reference?: number;
  rate_ratio?: number;
  samples?: string[];
  /** This cluster is why the log query failed. */
  failing?: boolean;
}

export interface LogVerdict {
  name: string; outcome: VerifyOutcome; reason: string; lines: number; clusters?: ClusterVerdict[];
  /** Each interval's read: value is the lines read in it. */
  intervals?: IntervalCheck[];
}

export interface VerifyVerdict {
  outcome: VerifyOutcome;
  /** No more intervals are needed. */
  final: boolean;
  summary: string;
  metrics?: MetricVerdict[];
  logs?: LogVerdict[];
  /** How many intervals were judged. */
  intervals: number;
}

export interface AIUsage { calls: number; input_tokens: number; output_tokens: number; cost_usd?: number; duration_ms?: number }

/** What a model said about a verdict: untrusted text. */
export interface VerifyExplanation {
  model: string;
  summary?: string;
  cause?: string;
  suggestion?: string;
  outcome?: string;
  /** The model turned the statistics' pass into something else (decide: model-assisted). */
  changed?: boolean;
  error?: string;
  usage?: AIUsage;
}

export interface BaselineRun { run: string; number: number; step: string; version?: string; at: number }

export interface VerifyQuery { name: string; source: string; query?: string; stable_query?: string; statement?: string; index?: string }

export interface VerificationView {
  run_id: string;
  run_number: number;
  project: string;
  step: string;
  /** The step's status. */
  status: StepStatus;
  environment: string;
  service: string;
  target: string;
  deployment?: string;
  version?: string;
  /** When the first interval starts (Unix ms). */
  start: number;
  interval_ms: number;
  total: number;
  deadline?: number;
  sensitivity: "low" | "medium" | "high" | string;
  on_fail: string;
  on_inconclusive: string;
  decide?: string;
  model?: string;
  params?: Record<string, string>;
  /** The earlier passed verifications it is compared with, newest first. */
  baseline?: BaselineRun[];
  baseline_metrics?: Record<string, VerifyStats>;
  intervals: VerifyInterval[];
  /** The recorded verdict, or what the intervals so far say. */
  verdict: VerifyVerdict;
  decided: boolean;
  explanation?: VerifyExplanation;
  override?: "passed" | "failed" | "";
  override_by?: string;
  override_comment?: string;
  queries: VerifyQuery[];
  started_at: number;
}

export interface VerificationSummary {
  run_id: string; run_number: number; project: string; step: string; environment: string; service: string;
  deployment?: string; version?: string; outcome: VerifyOutcome; summary: string; intervals: number; total: number;
  overridden_by?: string; started_at: number; decided_at?: number;
  /** The verify step's status ("waiting" while it waits for a person). */
  status?: StepStatus;
}

export type VerifyAction = "mark-passed" | "mark-failed";

export interface VerifyControls {
  environment: string;
  controls: { action: VerifyAction; available: boolean; reason?: string; forbidden?: boolean }[];
}

/** The pipeline's definition of a verify step, as the run's graph has it. */
export interface GraphVerify {
  delay?: string; duration: string; interval: string; sensitivity: string; baseline: number;
  on_fail: string; on_inconclusive: string; model?: string; decide?: string;
  metrics?: { name: string; source: string; direction?: string; max?: number; min?: number; fail_fast_max?: number; fail_fast_min?: number }[];
  logs?: { name: string; source: string }[];
}

/** The pipeline's definition of an ai step. */
export interface GraphAI { model: string; prompt: string; instructions?: string; schema?: string; max_tokens: number }

// ---- AI models ----

export interface ModelUsage { project: string; day: string; calls: number; input_tokens: number; output_tokens: number; cost_usd: number }

export interface ModelView {
  name: string;
  provider: string;
  model: string;
  /** No key: a model server of the operator's own. */
  local: boolean;
  projects: string[];
  max_tokens: number;
  max_input_bytes: number;
  calls_per_run: number;
  tokens_per_day?: number;
  temperature: boolean;
  used_today?: ModelUsage[];
}

export interface Models { models: ModelView[]; server_can_explain: boolean }

/** A model's explanation of a failed step: untrusted text. */
export interface ExplanationView {
  id: string; run_id: string; step: string; attempt: number; model: string; actor: string; text: string;
  usage: AIUsage; created_at: number;
}

export interface HealthSourceView { name: string; type: string; projects: string[] }

export const verifyApi = {
  runVerifications: (id: string, background = true) =>
    call<VerificationView[]>("GET", `/api/v1/runs/${id}/verifications`, undefined, { background }),
  verifications: (q: { environment?: string; project?: string; service?: string; limit?: number }, background = false) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
    return call<VerificationSummary[]>("GET", "/api/v1/verifications?" + p.toString(), undefined, { background });
  },
  controls: (run: string, step: string, background = true) =>
    call<VerifyControls>("GET", `/api/v1/runs/${run}/steps/${encodeURIComponent(step)}/verify/controls`, undefined, { background }),
  override: (run: string, step: string, outcome: "passed" | "failed", reason: string) =>
    call<{ ok: boolean }>("POST", `/api/v1/runs/${run}/steps/${encodeURIComponent(step)}/verify/override`, { outcome, reason }),
  models: (background = true) => call<Models>("GET", "/api/v1/ai/models", undefined, { background }),
  explain: (run: string, step: string, model: string) =>
    call<ExplanationView>("POST", `/api/v1/runs/${run}/steps/${encodeURIComponent(step)}/explain`, { model }),
  explanations: (run: string, step: string, background = true) =>
    call<ExplanationView[]>("GET", `/api/v1/runs/${run}/steps/${encodeURIComponent(step)}/explanations`, undefined, { background }),
  healthSources: (background = true) => call<HealthSourceView[]>("GET", "/api/v1/health-sources", undefined, { background }),
};
