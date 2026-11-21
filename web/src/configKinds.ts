// The form of each kind of configuration object: its fields, each with the
// explanation the Go structs give (internal/config, internal/ai,
// internal/settings). Keys without a field here are kept as they are and can
// be edited on the raw tab.

import type { FieldDef, Option } from "./components/ConfigForm";
import type { Levels, Spec } from "./configApi";
import { terms } from "./terms";

const roleOpts = (unset: string): Option[] => [
  { value: "", label: unset }, { value: "admin", label: "admin" }, { value: "operator", label: "operator" }, { value: "none", label: "none (nobody)" },
];

export const HEALTH_TYPES: Option[] = [
  { value: "prometheus", label: "Prometheus", help: "Metrics queried with PromQL (also Thanos, Mimir, VictoriaMetrics). The usual choice for error rates and latency." },
  { value: "loki", label: "Loki", help: "Logs queried with LogQL: count errors or look for a message after a deployment." },
  { value: "clickhouse", label: "ClickHouse", help: "SQL over logs or events kept in ClickHouse." },
  { value: "elasticsearch", label: "Elasticsearch", help: "Searches over logs; the runner lists the only indices pipelines may search." },
  { value: "opensearch", label: "OpenSearch", help: "The same as Elasticsearch, for OpenSearch clusters." },
  { value: "postgres", label: "PostgreSQL", help: "Named statements the runner runs in a read-only transaction, such as counting failed orders." },
  { value: "http", label: "HTTP JSON API", help: "Named requests to any JSON API, with a path into the answer: a status page or your own health endpoint." },
];

export const SECRET_STORE_TYPES: Option[] = [
  { value: "vault", label: "HashiCorp Vault" }, { value: "aws-secrets-manager", label: "AWS Secrets Manager" },
  { value: "gcp-secret-manager", label: "Google Secret Manager" }, { value: "azure-key-vault", label: "Azure Key Vault" },
  { value: "kubernetes", label: "Kubernetes Secrets" }, { value: "env", label: "Environment variables of the runner" }, { value: "file", label: "Files on the runner" },
];

export const EVENTS = ["push", "manual", "draft", "pull_request", "schedule", "webhook"];

function levelFields(): FieldDef[] {
  const out: FieldDef[] = [];
  for (const [k, w, what] of [["installation", "{Installation}", "this whole {installation}"], ["organisation", "{Organization}", "the {organization} that owns the {projects}"],
    ["unit", "{Unit}", "a group of {projects} with shared settings (a team, a department)"], ["project", "{Project}", "one repository Launchway watches"]] as const) {
    out.push(
      { path: `levels.${k}.singular`, label: `${w}: one`, help: `What to call ${what}. Empty keeps the word of the chosen set. The Words page does this for every word at once.`, type: "text", section: "What things are called", match: [`levels.${k}`] },
      { path: `levels.${k}.plural`, label: `${w}: several`, help: `The plural, as lists and menus show it.`, type: "text", section: "What things are called", match: [`levels.${k}`] },
    );
  }
  out.push(
    { path: "terms.set", label: "Set of words", type: "select", section: "What things are called", options: [{ value: "", label: "Aviation (the default)" }, { value: "aviation", label: "Aviation" }, { value: "business", label: "Plain business" }],
      help: "Labels only: no address, command or file changes with it." },
    { path: "terms.overrides", label: "Renamed words", type: "map", section: "What things are called", help: "A word (such as guide or flight_plan; add _plural for a plural) and what to call it. The Words page is easier." },
  );
  return out;
}

/** sayField puts the words in effect into a field's text: {clearance} is what this installation calls it. */
export function sayField(f: FieldDef): FieldDef {
  const t = terms();
  return { ...f, label: t.say(f.label), help: typeof f.help === "string" ? t.say(f.help) : f.help, section: f.section && t.say(f.section),
    unsetMeans: f.unsetMeans && t.say(f.unsetMeans), options: f.options?.map((o) => ({ ...o, label: t.say(o.label) })) };
}

const num = (path: string, label: string, help: string, section: string): FieldDef => ({ path, label, help, type: "number", section, placeholder: "default" });

export const FIELDS: Record<string, FieldDef[]> = {
  installation: [
    ...levelFields(),
    { path: "approvals.allow_self", label: "People may approve their own runs", type: "tri", section: "Approvals and drafts",
      unsetMeans: "Not set: yes without sign-in, no with sign-in",
      help: "Lets the person who started a run approve it. Off is the safer choice once people sign in: a second person must look." },
    { path: "drafts.allow", label: "Who may start draft runs", type: "select", section: "Approvals and drafts", options: roleOpts("Not set: operator"),
      help: "The role needed on a project to run pipeline text from the editor that is not in Git yet. none turns draft runs off." },
    { path: "drafts.secrets", label: "Draft runs may read secrets", type: "tri", section: "Approvals and drafts", unsetMeans: "Not set: no",
      help: "Draft pipelines are not reviewed; letting them read secrets lets anyone who may start one read them too." },
    { path: "pull_requests.secrets", label: "Pull-request runs may read secrets", type: "tri", section: "Approvals and drafts", unsetMeans: "Not set: no",
      help: "A pull request can change the pipeline; with secrets on, whoever opens one can make a step print them. Projects may override this." },
    { path: "schedules.enabled", label: "Scheduled runs", type: "tri", section: "Approvals and drafts", unsetMeans: "Not set: on",
      help: "Turns every pipeline's schedule triggers on or off. Projects may override this." },
    { path: "runner_admin", label: "Who may manage runners", type: "select", section: "Approvals and drafts", options: roleOpts("Not set: admin"),
      help: "The role needed on every project to make join tokens and to revoke or rotate runners." },
    { path: "policy_waivers.allow", label: "Who may waive a policy rule", type: "select", section: "Policies and tokens", options: roleOpts("Not set: admin"),
      help: "The role needed on a project to waive one of its policies' rules (on the environment too, for a deploy rule). none turns waivers off." },
    { path: "policy_waivers.max_days", label: "Longest waiver, in days", type: "number", section: "Policies and tokens", placeholder: "30",
      help: "How long a waiver may last; at most 365." },
    { path: "tokens.max_days", label: "Longest API token, in days", type: "number", section: "Policies and tokens", placeholder: "365",
      help: "The longest an API token may last; at most 3650." },
    { path: "loops.max_members", label: "Combinations in one loop", type: "number", section: "Limits", placeholder: "64",
      help: "How many combinations one loop (a matrix) may have; at most 500." },
    num("tests.max_upload_mib", "Test reports per step attempt (MiB)", "How many MiB of report files the server accepts for one step attempt; runners are told and send no more. Default 24.", "Limits"),
    num("tests.max_cases_per_run", "Test results kept per run", "Beyond it, only the totals. Default 50000.", "Limits"),
    num("tests.max_cases_per_project", "Test results kept per project", "Over all its runs: the oldest runs' results are removed first, keeping their totals. Default 1000000.", "Limits"),
    num("findings.max_upload_mib", "Security reports per step attempt (MiB)", "SARIF accepted per step attempt. Default 32.", "Limits"),
    num("findings.max_per_run", "Findings kept per run", "Default 20000.", "Limits"),
    num("findings.max_per_project", "Findings kept per project", "Default 500000.", "Limits"),
    num("findings.max_components_per_run", "SBOM components kept per run", "Default 20000.", "Limits"),
    num("findings.max_components_per_project", "SBOM components kept per project", "Default 1000000.", "Limits"),
    num("findings.max_sbom_mib", "SBOM read per attempt (MiB)", "How much of an SBOM is read to summarise it. Default 64.", "Limits"),
    num("reports.max_attachment_files", "Attachment files per step", "Files in a step's attachments directory. Default 2000.", "Limits"),
    num("reports.max_attachment_mib", "Attachments per step (MiB)", "Default 512.", "Limits"),
    num("reports.max_html_files", "Files in one HTML report", "Default 5000.", "Limits"),
    num("reports.max_html_mib", "Size of one HTML report (MiB)", "Default 256.", "Limits"),
    num("reports.max_coverage_upload_mib", "Coverage reports per step attempt (MiB)", "Default 24.", "Limits"),
    num("reports.max_coverage_files_per_run", "Coverage files kept per run", "Default 20000.", "Limits"),
    num("reports.max_coverage_files_per_project", "Coverage files kept per project", "Default 2000000.", "Limits"),
    num("artifacts.max_file_mib", "Largest artifact file (MiB)", "The largest file an artifact may hold. Default 1024.", "Artifacts"),
    num("artifacts.max_run_mib", "Artifacts per run (MiB)", "How much one run may keep. Default 4096.", "Artifacts"),
    num("artifacts.max_project_gib", "Artifacts per project (GiB)", "How much a project may keep over all its runs. Default 50.", "Artifacts"),
    { path: "artifacts.retain", label: "Keep artifacts for", type: "duration", section: "Artifacts", placeholder: "30d",
      help: "How long artifacts are kept when the pipeline does not say (30d, 2w, 12h)." },
    { path: "artifacts.max_retain", label: "Keep artifacts at most", type: "duration", section: "Artifacts", placeholder: "90d",
      help: "The longest any artifact is kept, whatever the pipeline says." },
  ],
  organisation: [
    { path: "display_name", label: "Display name", type: "text", placeholder: "Acme", help: "The {organization}'s name as people read it, such as your company's name." },
    { path: "description", label: "Description", type: "textarea", help: "A sentence about it, shown where it helps." },
  ],
  unit: [
    { path: "display_name", label: "Display name", type: "text", placeholder: "Payments team", help: "How the {unit} is shown. The name above stays the same in URLs and the command line." },
    { path: "description", label: "Description", type: "textarea", help: "Who it is and what its projects are." },
  ],
  library: [
    { path: "repo", label: "Repository", type: "text", wide: true, placeholder: "https://git.example.com/platform/ci-templates.git",
      help: "The Git repository of templates and scripts. It is fetched with the same credentials as projects: the server's own Git settings, never a password typed here." },
  ],
  project: [
    { path: "repo", label: "Repository", type: "text", wide: true, placeholder: "https://git.example.com/team/shop.git", section: "Repository",
      help: "The Git URL (https:// or SSH) Launchway reads. It uses the server's own Git credentials." },
    { path: "branch", label: "Default branch", type: "text", placeholder: "main", section: "Repository",
      help: "The branch manual runs start from when nobody picks another." },
    { path: "poll", label: "Look for pushes every", type: "duration", placeholder: "30s", section: "Repository",
      help: "How often Launchway asks the repository for new commits, such as 30s or 2m. A Git webhook can make it look at once." },
    { path: "pipeline.library", label: "Library", type: "text", section: "Pipeline from a library template", match: ["pipeline.library", "library"],
      help: "The library whose template is this project's pipeline, at commits without a .launchway/pipeline.yaml. A file in the repository takes its place." },
    { path: "pipeline.template", label: "Template", type: "text", section: "Pipeline from a library template", match: ["pipeline.template", "template"],
      help: "The template's name in that library." },
    { path: "pipeline.version", label: "Library version", type: "text", placeholder: "v1.4.0", section: "Pipeline from a library template", match: ["pipeline.version"],
      help: "A tag, branch or full commit id of the library. A tag keeps runs repeatable; a branch follows the library as it changes." },
    { path: "pipeline.with", label: "Template parameters", type: "map", section: "Pipeline from a library template", match: ["pipeline.with", "with"],
      help: "The values the template is given, name=value per line." },
    { path: "pipeline.on.push", label: "Run on pushes", type: "presence", section: "Pipeline from a library template", match: ["on.push", "push"],
      help: "Start a run when someone pushes to the repository (to the branches or tags below; none listed: any)." },
    { path: "pipeline.on.push.branches", label: "Branches", type: "list", placeholder: "main, release/*", section: "Pipeline from a library template",
      showIf: (s) => !!(s.pipeline as Spec | undefined)?.on && ((s.pipeline as Spec).on as Spec).push !== undefined, match: ["branches"],
      help: "Branch names or patterns that start runs when pushed to." },
    { path: "pipeline.on.push.tags", label: "Tags", type: "list", placeholder: "v*", section: "Pipeline from a library template",
      showIf: (s) => !!(s.pipeline as Spec | undefined)?.on && ((s.pipeline as Spec).on as Spec).push !== undefined, match: ["tags"],
      help: "Tag names or patterns that start runs when pushed." },
    { path: "pipeline.on.pull_request", label: "Run on pull requests", type: "presence", section: "Pipeline from a library template", match: ["pull_request"],
      help: "Start a run for each pull request and each push to it." },
    { path: "pipeline.supersede", label: "A newer run cancels older ones", type: "bool", section: "Pipeline from a library template", match: ["supersede"],
      help: "A new run of a push or pull request cancels the older runs of the same branch that are still going, so only the latest commit uses runners." },
    { path: "pull_requests.secrets", label: "Pull-request runs may read secrets", type: "tri", section: "What runs may do", unsetMeans: "Not set: the {installation}'s setting",
      match: ["pull_requests"], help: "Overrides the {installation}'s setting for this project. Whoever opens a pull request can change what its steps do." },
    { path: "drafts.allow", label: "Who may start draft runs", type: "select", section: "What runs may do", options: roleOpts("Not set: the {installation}'s setting"),
      match: ["drafts"], help: "Overrides the {installation}'s setting for this project." },
    { path: "drafts.secrets", label: "Draft runs may read secrets", type: "tri", section: "What runs may do", unsetMeans: "Not set: the {installation}'s setting",
      help: "Overrides the {installation}'s setting for this project." },
    { path: "schedules.enabled", label: "Scheduled runs", type: "tri", section: "What runs may do", unsetMeans: "Not set: the {installation}'s setting",
      match: ["schedules"], help: "Overrides the {installation}'s setting for this project." },
    { path: "push.branch_prefix", label: "Editor saves to branches starting with", type: "text", placeholder: "launchway/", section: "Saving from the editor",
      match: ["push", "branch_prefix"], help: "With this set (or Direct branches), the pipeline editor commits to the repository with the server's credentials. Every save makes a new branch starting with this." },
    { path: "push.allow_direct", label: "Direct branches", type: "list", placeholder: "release/*", section: "Saving from the editor", match: ["allow_direct"],
      help: "Branch patterns the editor may commit to directly, without a new branch. Default: none." },
    { path: "git_webhook.kind", label: "Git host webhook", type: "select", section: "Git webhook", match: ["git_webhook"],
      options: [{ value: "", label: "None" }, { value: "github", label: "GitHub" }, { value: "gitea", label: "Gitea" }, { value: "forgejo", label: "Forgejo" }, { value: "gitlab", label: "GitLab" }],
      help: "Lets the project's Git host call a webhook that makes the server look for pushes at once." },
    { path: "git_webhook.secret_env", label: "Webhook secret: environment variable", type: "text", section: "Git webhook", placeholder: "LAUNCHWAY_SHOP_HOOK_SECRET",
      showIf: (s) => !!(s.git_webhook as Spec | undefined)?.kind, help: "The name of the server's environment variable that holds the webhook's secret. The secret itself is never stored here." },
    { path: "git_webhook.secret_file", label: "Webhook secret: file", type: "text", section: "Git webhook", placeholder: "/run/secrets/shop-hook",
      showIf: (s) => !!(s.git_webhook as Spec | undefined)?.kind, help: "Or a file on the server holding it." },
  ],
  environment: [
    { path: "target", label: "Target", type: "text", placeholder: "prod-cluster",
      help: "What deploy runners serve to reach the environment: in practice, its cluster. A deploy pool lists the targets its runners serve." },
    { path: "projects", label: "Projects that may deploy here", type: "list", placeholder: "shop, web-*",
      help: "Names or patterns such as web-*. A project not listed cannot deploy here." },
    { path: "queue", label: "Queue", type: "select", options: [{ value: "", label: "newest (the default)" }, { value: "newest", label: "newest" }, { value: "all", label: "all" }],
      help: "newest: a run waiting for the environment's lock is skipped when a newer run of the same service starts waiting. all: every run deploys in turn." },
    { path: "protected", label: "Protected", type: "bool",
      help: "The person who started a run may not promote its deployment here (unless people may approve their own runs); someone else with the operator role must. Anyone with the operator role here may abort." },
  ],
  "runner-pool": [
    { path: "kind", label: "Kind", type: "select", options: [{ value: "", label: "deploy (the default)" }, { value: "deploy", label: "deploy" }, { value: "build", label: "build" }],
      help: "A build pool runs the steps of projects. A deploy pool does deploy and rollback steps for targets, and nothing else." },
    { path: "projects", label: "Projects it serves", type: "list", placeholder: "*, or shop, web-*", showIf: (s) => s.kind === "build",
      help: "Build pools: names or patterns (\"*\" for all) of the projects whose steps its runners take." },
    { path: "events", label: "Only runs started by", type: "list", placeholder: "push, manual", showIf: (s) => s.kind === "build",
      help: `Build pools: limit it to runs of these events (${EVENTS.join(", ")}). Empty: all.` },
    { path: "targets", label: "Targets it serves", type: "list", placeholder: "prod-cluster", showIf: (s) => s.kind !== "build",
      help: "Deploy pools: the environments' targets its runners deploy to." },
    { path: "token_env", label: "Pool token: environment variable", type: "text", placeholder: "LAUNCHWAY_POOL_DEPLOY_PROD_TOKEN", showIf: (s) => s.kind !== "build",
      help: "Deploy pools without require_identity: the server's environment variable holding the pool's own token (at least 32 characters). Never the token itself." },
    { path: "token_file", label: "Pool token: file", type: "text", showIf: (s) => s.kind !== "build",
      help: "Or a file on the server holding it." },
    { path: "require_identity", label: "Only enrolled runners", type: "bool",
      help: "Refuses shared tokens: only runners enrolled with a join token for this pool serve it. Recommended: each runner then has its own identity that can be revoked." },
  ],
  "role-binding": [
    { path: "group", label: "Group", type: "text", placeholder: "platform-team", help: "A group from your identity provider. Give a group or a user, not both." },
    { path: "user", label: "User", type: "text", placeholder: "alice", help: "Or one person's user name." },
    { path: "role", label: "Role", type: "select",
      options: [{ value: "", label: "Choose a role" }, { value: "viewer", label: "viewer: sees runs and logs" }, { value: "operator", label: "operator: also starts, approves, promotes" }, { value: "admin", label: "admin: also changes configuration" }],
      help: "viewer reads; operator also starts runs, approves and acts on deployments; admin may also change the configuration." },
    { path: "projects", label: "On projects", type: "list", placeholder: "shop, web-*", help: "Names or patterns. Empty: every project." },
    { path: "environments", label: "Only for environments", type: "list", placeholder: "staging",
      help: "Limits what the binding grants on deployments (promote, abort and approvals that name a deployment) to these environments. Empty: all." },
  ],
  policy: [
    { path: "description", label: "Description", type: "textarea", help: "What the policy is for, at most 500 characters. Shown wherever its verdict is." },
    { path: "mode", label: "Mode", type: "select", options: [{ value: "", label: "Choose" }, { value: "report", label: "report: warn only" }, { value: "enforce", label: "enforce: block" }],
      help: "enforce stops the step (or the run at its start) when a rule fails; report records and shows a warning and never changes an outcome." },
    { path: "scope.projects", label: "Projects", type: "list", section: "Applies to", placeholder: "*", help: "Patterns of the projects it governs. Empty: every run." },
    { path: "scope.environments", label: "Environments", type: "list", section: "Applies to", placeholder: "prod", help: "Only deploy rules may be scoped to environments." },
    { path: "scope.branches", label: "Branches", type: "list", section: "Applies to", placeholder: "main", help: "A run without a branch (a tag or a pull request) does not match." },
    { path: "scope.events", label: "Events", type: "list", section: "Applies to", placeholder: "push, manual", help: `Any of ${EVENTS.join(", ")}.` },
  ],
  "policy-source": [
    { path: "library", label: "Library", type: "text", help: "The library whose files hold the policies." },
    { path: "version", label: "Version", type: "text", placeholder: "v3", help: "A tag or a full commit id; a branch is refused, so policies change only when you move the version." },
    { path: "path", label: "Directory", type: "text", placeholder: "policies", help: "Every *.yaml and *.yml file below it is read. Default policies." },
    { path: "projects", label: "Projects it may govern", type: "list", placeholder: "*", help: "Patterns. Empty: every project." },
    { path: "refresh", label: "Look again every", type: "duration", placeholder: "1m", help: "How often each server looks at the version again, 10s to 168h." },
    { path: "stale_after", label: "Keep using an old version for", type: "duration", placeholder: "1h",
      help: "How long a version loaded earlier is still used while the repository cannot be reached; after that, governed runs fail closed." },
  ],
  "health-source": [
    { path: "type", label: "Type", type: "select", options: [{ value: "", label: "Choose a type" }, ...HEALTH_TYPES.map((t) => ({ value: t.value, label: t.label }))],
      help: "What kind of system it is. Where it is and its credentials are set on the deploy runners, which are next to it." },
    { path: "projects", label: "Projects that may read it", type: "list", placeholder: "*", help: "Names or patterns. Empty: none." },
  ],
  model: [
    { path: "provider", label: "Provider", type: "select", options: [{ value: "", label: "Choose" }, { value: "anthropic", label: "anthropic" }, { value: "openai", label: "openai (any OpenAI-compatible chat API)" }],
      help: "How to talk to the model: the Anthropic Messages API, or OpenAI-compatible chat completions (local model servers among them)." },
    { path: "model", label: "Model id", type: "text", placeholder: "claude-sonnet-5-5", help: "The provider's id of the model, such as claude-sonnet-5-5 or qwen3:8b." },
    { path: "endpoint", label: "Endpoint", type: "text", wide: true, placeholder: "https://api.anthropic.com",
      help: "The base URL. Default https://api.anthropic.com for anthropic; required for openai." },
    { path: "key", label: "API key (a reference)", type: "secretref", match: ["key"],
      help: "Where the key is kept, in a secret store the runners (and the server, to explain failures) have: never the key itself. A local server may need none." },
    { path: "projects", label: "Projects that may use it", type: "list", placeholder: "shop, web-*", help: "Patterns. Empty: none may." },
    { path: "max_tokens", label: "Output tokens per call", type: "number", section: "Limits", placeholder: "4096", help: "Default 4096, at most 128000." },
    { path: "calls_per_run", label: "Calls per run", type: "number", section: "Limits", placeholder: "20", help: "Default 20." },
    { path: "tokens_per_day", label: "Tokens per project per day", type: "number", section: "Limits", placeholder: "no limit", help: "Input plus output. Empty: no limit." },
    { path: "max_input_bytes", label: "Input bytes per call", type: "number", section: "Limits", placeholder: "262144", help: "System plus prompt. Default 256 KiB." },
    { path: "timeout", label: "Timeout per call", type: "duration", section: "Limits", placeholder: "2m", help: "Retries included; default 2m, at most 15m." },
    { path: "effort", label: "Effort", type: "select", section: "Limits", showIf: (s) => s.provider === "anthropic",
      options: [{ value: "", label: "the model's default" }, ...["low", "medium", "high", "xhigh", "max"].map((v) => ({ value: v, label: v }))], help: "anthropic only." },
    { path: "headers", label: "Extra headers", type: "map", section: "Limits", help: "Headers that are not secret, name=value per line." },
  ],
  "step-resources": [
    { path: "limits.max_cpu", label: "Most CPU a step may ask for", type: "text", section: "Limits", placeholder: "8", help: "In cores, such as 8 or 500m. Empty: no limit." },
    { path: "limits.max_memory", label: "Most memory a step may ask for", type: "text", section: "Limits", placeholder: "16Gi", help: "Such as 16Gi. Empty: no limit." },
    { path: "limits.memory_per_thread", label: "Memory per thread", type: "text", section: "Limits", placeholder: "1Gi", help: "Default 1Gi, at least 64Mi." },
    { path: "defaults.step.cpu", label: "CPU a step gets", type: "text", section: "Defaults for steps that ask for nothing", placeholder: "1", match: ["defaults.step", "cpu"], help: "What it is guaranteed." },
    { path: "defaults.step.cpu_limit", label: "CPU it may use at most", type: "text", section: "Defaults for steps that ask for nothing", placeholder: "4", help: "Above it the step is slowed, not stopped." },
    { path: "defaults.step.memory", label: "Memory a step gets", type: "text", section: "Defaults for steps that ask for nothing", placeholder: "2Gi", match: ["defaults.step", "memory"], help: "What it is guaranteed." },
    { path: "defaults.step.memory_limit", label: "Memory it may use at most", type: "text", section: "Defaults for steps that ask for nothing", placeholder: "6Gi",
      help: "Other kinds of step (image builds and the like) have defaults of their own: set them on the Raw tab under defaults." },
  ],
  "sign-in": [
    { path: "issuer", label: "The provider's address (issuer)", type: "text", help: "The OpenID Connect issuer URL the provider shows for the application." },
    { path: "client_id", label: "Client id", type: "text", help: "The application's client id in the provider." },
    { path: "redirect_url", label: "Redirect address", type: "text", help: "This server's address plus /auth/callback; register exactly it in the provider." },
    { path: "client_secret_env", label: "Environment variable that holds the client secret", type: "text",
      help: "Only the name is kept here, never the secret: it is set on every server from your secret manager." },
    { path: "groups_claim", label: "Groups claim", type: "text", help: "The claim the provider puts groups in; groups by default." },
    { path: "username_claim", label: "User name claim", type: "text", help: "Which claim names the person; the default is usually right." },
    { path: "admins.groups", label: "Groups whose members are the first administrators", type: "list", help: "Group names as the provider sends them." },
    { path: "admins.users", label: "Or users", type: "list", help: "User names of the first administrators." },
  ],
  "secret-store": [
    { path: "type", label: "Type", type: "select", options: [{ value: "", label: "Choose a type" }, ...SECRET_STORE_TYPES],
      help: "What kind of store pipelines may name. Its address and credentials stay with the runners." },
    { path: "description", label: "Description", type: "textarea", help: "What is kept there." },
    { path: "projects", label: "Projects that may use it", type: "list", placeholder: "*", help: "Patterns. Empty: every project." },
  ],
};

/** A field for the object's name, with what names may be. */
export function nameHelp(kind: string, lv?: Levels): string {
  const what = kind === "project" ? (lv?.project.singular.toLowerCase() ?? "project") : "object";
  if (kind === "policy") return "Lower-case letters, digits and hyphens, at most 63 characters. It cannot be changed later.";
  if (kind === "model") return "What pipelines call it: lower-case letters, digits and hyphens. It cannot be changed later.";
  return `How the ${what} is named in URLs, pipelines and the command line: letters, digits, dots, hyphens and underscores. It cannot be changed later.`;
}

// ---- policy rules ----

export interface RuleField { key: string; label: string; type: "bool" | "number" | "text" | "list" | "select"; options?: string[]; help?: string }

/** The options of each rule, from docs/reference/policy-rules.md. A rule
 * not listed (a newer server's) is edited as JSON. */
export const RULES: Record<string, { scalar?: "duration"; templates?: true; fields?: RuleField[]; example: unknown }> = {
  library_versions: { example: { pinned: "tag" }, fields: [{ key: "pinned", label: "Pinned to", type: "select", options: ["tag", "commit"], help: "tag: a tag or a full commit id; commit: only a full commit id." }] },
  required_templates: { templates: true, example: [{ library: "ci-templates", template: "security-scan" }] },
  step_images: { example: { digest: true }, fields: [
    { key: "digest", label: "Pinned by digest", type: "bool", help: "Every image must be name@sha256:…" },
    { key: "registries", label: "Allowed repositories", type: "list", help: "host/path patterns: * within one part, a final /** any depth." }] },
  max_timeout: { scalar: "duration", example: "2h" },
  forbid_secrets: { example: { events: ["pull_request", "draft"] }, fields: [{ key: "events", label: "Events", type: "list", help: `Runs of these events may not ask for secrets: ${EVENTS.join(", ")}.` }] },
  tests: { example: { min: 1 }, fields: [{ key: "min", label: "At least this many results", type: "number" }] },
  findings: { example: { max_severity: "high" }, fields: [
    { key: "max_severity", label: "Worst severity allowed", type: "select", options: ["", "none", "low", "medium", "high", "critical"] },
    { key: "scanners", label: "Scanners that must report", type: "list" }] },
  sbom: { example: {}, fields: [{ key: "min_components", label: "At least this many components", type: "number" }] },
  deployed_images: { example: { digest: true }, fields: [
    { key: "digest", label: "Pinned by digest", type: "bool" }, { key: "built_by_run", label: "Built by this run", type: "bool" },
    { key: "signed", label: "Signed", type: "bool" }, { key: "scanned", label: "Scanned", type: "bool" }] },
  approvals: { example: { min: 1 }, fields: [{ key: "min", label: "{Clearances} from other people", type: "number", help: "1 to 20; the person who started the run does not count." }] },
  refs: { example: { branches: ["main"] }, fields: [{ key: "branches", label: "Branches", type: "list" }, { key: "tags", label: "Tags", type: "list" }] },
  image_push: { example: { scan: "high", sbom: true }, fields: [
    { key: "scan", label: "Scan, failing on", type: "select", options: ["", "low", "medium", "high", "critical"] },
    { key: "sign", label: "Sign", type: "select", options: ["", "runner", "any"] },
    { key: "sbom", label: "Make an SBOM", type: "bool" }, { key: "provenance", label: "Record provenance", type: "bool" },
    { key: "registries", label: "Allowed registries", type: "list" }] },
};

/** When a rule is judged, in words. */
export const POINT: Record<string, string> = {
  run: "at run start",
  get deploy() { return terms().say("before deploying ({preflight})"); },
  image: "before an image push",
};

/** withLevelNames rewrites "project(s)" in the fields' words with what the
 * installation calls a project. */
export function withLevelNames(fields: FieldDef[], lv?: Levels): FieldDef[] {
  if (!lv || (lv.project.singular === "Project" && lv.project.plural === "Projects")) return fields;
  const sub = (t: string) => t
    .replace(/\bProjects\b/g, lv.project.plural).replace(/\bprojects\b/g, lv.project.plural.toLowerCase())
    .replace(/\bProject\b/g, lv.project.singular).replace(/\bproject\b/g, lv.project.singular.toLowerCase());
  return fields.map((f) => ({ ...f, label: sub(f.label), help: typeof f.help === "string" ? sub(f.help) : f.help, section: f.section && sub(f.section) }));
}

/** levelWords rewrites "project(s)" and "unit(s)" in a sentence with what
 * the installation calls them. */
export function levelWords(t: string, lv?: Levels): string {
  if (!lv) return t;
  return t
    .replace(/\bProjects\b/g, lv.project.plural).replace(/\bprojects\b/g, lv.project.plural.toLowerCase())
    .replace(/\bProject\b/g, lv.project.singular).replace(/\bproject\b/g, lv.project.singular.toLowerCase())
    .replace(/\bUnits\b/g, lv.unit.plural).replace(/\bunits\b/g, lv.unit.plural.toLowerCase())
    .replace(/\bUnit\b/g, lv.unit.singular).replace(/\bunit\b/g, lv.unit.singular.toLowerCase());
}
