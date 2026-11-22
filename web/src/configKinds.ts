// The form of each kind of configuration object: its fields, each with the
// explanation the Go structs give (internal/config, internal/ai,
// internal/settings). Keys without a field here are kept as they are and can
// be edited on the raw tab.

import type { FieldDef, Option } from "./components/ConfigForm";
import type { Levels, Spec } from "./configApi";
import { terms } from "./terms";
import { prose } from "./prose";
const P = await prose("configKinds");

const roleOpts = (unset: string): Option[] => [
  { value: "", label: unset }, { value: "admin", label: "admin" }, { value: "operator", label: "operator" }, { value: "none", label: "none (nobody)" },
];

export const HEALTH_TYPES: Option[] = [
  { value: "prometheus", label: "Prometheus", help: P.t0 },
  { value: "loki", label: "Loki", help: P.t1 },
  { value: "clickhouse", label: "ClickHouse", help: P.t2 },
  { value: "elasticsearch", label: "Elasticsearch", help: P.t3 },
  { value: "opensearch", label: "OpenSearch", help: P.t4 },
  { value: "postgres", label: "PostgreSQL", help: P.t5 },
  { value: "http", label: "HTTP JSON API", help: P.t6 },
];

export const SECRET_STORE_TYPES: Option[] = [
  { value: "vault", label: "HashiCorp Vault" }, { value: "aws-secrets-manager", label: "AWS Secrets Manager" },
  { value: "gcp-secret-manager", label: "Google Secret Manager" }, { value: "azure-key-vault", label: "Azure Key Vault" },
  { value: "kubernetes", label: "Kubernetes Secrets" }, { value: "env", label: P.t7 }, { value: "file", label: "Files on the runner" },
];

export const EVENTS = ["push", "manual", "draft", "pull_request", "schedule", "webhook"];

function levelFields(): FieldDef[] {
  const out: FieldDef[] = [];
  for (const [k, w, what] of [["installation", "{Installation}", "this whole {installation}"], ["organisation", "{Organization}", P.t8],
    ["unit", "{Unit}", P.t9], ["project", "{Project}", P.t10]] as const) {
    out.push(
      { path: `levels.${k}.singular`, label: `${w}: one`, help: `What to call ${what}. Empty keeps the word of the chosen set. The Words page does this for every word at once.`, type: "text", section: "What things are called", match: [`levels.${k}`] },
      { path: `levels.${k}.plural`, label: `${w}: several`, help: P.t11, type: "text", section: "What things are called", match: [`levels.${k}`] },
    );
  }
  out.push(
    { path: "terms.set", label: "Set of words", type: "select", section: "What things are called", options: [{ value: "", label: "Aviation (the default)" }, { value: "aviation", label: "Aviation" }, { value: "business", label: "Plain business" }],
      help: P.t12 },
    { path: "terms.overrides", label: "Renamed words", type: "map", section: "What things are called", help: P.t13 },
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
    { path: "approvals.allow_self", label: P.t14, type: "tri", section: "Approvals and drafts",
      unsetMeans: P.t15,
      help: P.t16 },
    { path: "drafts.allow", label: "Who may start draft runs", type: "select", section: "Approvals and drafts", options: roleOpts("Not set: operator"),
      help: P.t17 },
    { path: "drafts.secrets", label: "Draft runs may read secrets", type: "tri", section: "Approvals and drafts", unsetMeans: "Not set: no",
      help: P.t18 },
    { path: "pull_requests.secrets", label: P.t19, type: "tri", section: "Approvals and drafts", unsetMeans: "Not set: no",
      help: P.t20 },
    { path: "schedules.enabled", label: "Scheduled runs", type: "tri", section: "Approvals and drafts", unsetMeans: "Not set: on",
      help: P.t21 },
    { path: "runner_admin", label: "Who may manage runners", type: "select", section: "Approvals and drafts", options: roleOpts("Not set: admin"),
      help: P.t22 },
    { path: "policy_waivers.allow", label: "Who may waive a policy rule", type: "select", section: "Policies and tokens", options: roleOpts("Not set: admin"),
      help: P.t23 },
    { path: "policy_waivers.max_days", label: "Longest waiver, in days", type: "number", section: "Policies and tokens", placeholder: "30",
      help: P.t24 },
    { path: "tokens.max_days", label: "Longest API token, in days", type: "number", section: "Policies and tokens", placeholder: "365",
      help: P.t25 },
    { path: "loops.max_members", label: "Combinations in one loop", type: "number", section: "Limits", placeholder: "64",
      help: P.t26 },
    num("tests.max_upload_mib", P.t27, P.t28, "Limits"),
    num("tests.max_cases_per_run", "Test results kept per run", P.t29, "Limits"),
    num("tests.max_cases_per_project", P.t30, P.t31, "Limits"),
    num("findings.max_upload_mib", P.t32, P.t33, "Limits"),
    num("findings.max_per_run", "Findings kept per run", "Default 20000.", "Limits"),
    num("findings.max_per_project", "Findings kept per {project}", "Default 500000.", "Limits"),
    num(P.t34, "SBOM components kept per run", "Default 20000.", "Limits"),
    num(P.t35, P.t36, "Default 1000000.", "Limits"),
    num("findings.max_sbom_mib", "SBOM read per attempt (MiB)", P.t37, "Limits"),
    num("reports.max_attachment_files", "Attachment files per step", P.t38, "Limits"),
    num("reports.max_attachment_mib", "Attachments per step (MiB)", "Default 512.", "Limits"),
    num("reports.max_html_files", "Files in one HTML report", "Default 5000.", "Limits"),
    num("reports.max_html_mib", "Size of one HTML report (MiB)", "Default 256.", "Limits"),
    num(P.t39, P.t40, "Default 24.", "Limits"),
    num(P.t41, "Coverage files kept per run", "Default 20000.", "Limits"),
    num(P.t42, P.t43, "Default 2000000.", "Limits"),
    num("artifacts.max_file_mib", "Largest artifact file (MiB)", P.t44, "Artifacts"),
    num("artifacts.max_run_mib", "Artifacts per run (MiB)", P.t45, "Artifacts"),
    num("artifacts.max_project_gib", "Artifacts per {project} (GiB)", P.t46, "Artifacts"),
    { path: "artifacts.retain", label: "Keep artifacts for", type: "duration", section: "Artifacts", placeholder: "30d",
      help: P.t47 },
    { path: "artifacts.max_retain", label: "Keep artifacts at most", type: "duration", section: "Artifacts", placeholder: "90d",
      help: P.t48 },
    { path: "forge.hosting", label: "Git hosting", type: "bool", section: "Git hosting",
      help: P.t49 },
    { path: "forge.public_url", label: "Address clients clone from", type: "text", section: "Git hosting", placeholder: "https://launchway.example.com",
      help: P.t50 },
    { path: "forge.default_owner", label: "Default owner", type: "text", section: "Git hosting", placeholder: "launchway",
      help: P.t51 },
    { path: "forge.allow_insecure_http", label: P.t52, type: "bool", section: "Git hosting",
      help: P.t53 },
    num("forge.max_repo_mib", "Largest repository (MiB)", P.t54, "Git hosting"),
    num("forge.max_push_mib", "Largest push (MiB)", P.t55, "Git hosting"),
    num("forge.max_per_owner", "Repositories per owner", P.t56, "Git hosting"),
    num("forge.creates_per_minute", "Repositories made per minute", P.t57, "Git hosting"),
    num("forge.max_concurrent", "Git requests at once", P.t58, "Git hosting"),
    num("forge.cache_gib", P.t59, P.t60, "Git hosting"),
    { path: "forge.maintain_every", label: "Maintain packs every", type: "duration", section: "Git hosting", placeholder: "15m",
      help: P.t61 },
    { path: "forge.ssh.listen", label: "Git over SSH: listen on", type: "text", section: "Git hosting", placeholder: ":2222",
      help: P.t62 },
    { path: "forge.ssh.public_host", label: "SSH clone host", type: "text", section: "Git hosting", placeholder: "git.example.com",
      help: P.t63 },
    num("forge.ssh.public_port", "SSH clone port", P.t64, "Git hosting"),
    { path: "forge.ssh.user_ca", label: "SSH certificate authorities", type: "textarea", section: "Git hosting",
      help: P.t65 },
    num("forge.ssh.max_connections", "SSH connections", P.t66, "Git hosting"),
    num("forge.ssh.max_per_address", "SSH connections per address", P.t67, "Git hosting"),
    { path: "forge.ssh.idle_timeout", label: "SSH idle timeout", type: "duration", section: "Git hosting", placeholder: "10m",
      help: P.t68 },
    { path: "forge.ssh.max_duration", label: "SSH longest connection", type: "duration", section: "Git hosting", placeholder: "6h",
      help: P.t69 },
  ],
  organisation: [
    { path: "display_name", label: "Display name", type: "text", placeholder: "Acme", help: P.t70 },
    { path: "description", label: "Description", type: "textarea", help: P.t71 },
  ],
  unit: [
    { path: "display_name", label: "Display name", type: "text", placeholder: "Payments team", help: P.t72 },
    { path: "description", label: "Description", type: "textarea", help: P.t73 },
  ],
  library: [
    { path: "repo", label: "Repository", type: "text", wide: true, placeholder: P.t74,
      help: P.t75 },
  ],
  project: [
    { path: "repo", label: "Repository", type: "text", wide: true, placeholder: P.t76, section: "Repository",
      help: P.t77 },
    { path: "branch", label: "Default branch", type: "text", placeholder: "main", section: "Repository",
      help: P.t78 },
    { path: "poll", label: "Look for pushes every", type: "duration", placeholder: "30s", section: "Repository",
      help: P.t79 },
    { path: "pipeline.library", label: "Library", type: "text", section: P.t80, match: ["pipeline.library", "library"],
      help: P.t81 },
    { path: "pipeline.template", label: "Template", type: "text", section: P.t80, match: ["pipeline.template", "template"],
      help: P.t82 },
    { path: "pipeline.version", label: "Library version", type: "text", placeholder: "v1.4.0", section: P.t80, match: ["pipeline.version"],
      help: P.t83 },
    { path: "pipeline.with", label: "Template parameters", type: "map", section: P.t80, match: ["pipeline.with", "with"],
      help: P.t84 },
    { path: "pipeline.on.push", label: "Run on pushes", type: "presence", section: P.t80, match: ["on.push", "push"],
      help: P.t85 },
    { path: "pipeline.on.push.branches", label: "Branches", type: "list", placeholder: "main, release/*", section: P.t80,
      showIf: (s) => !!(s.pipeline as Spec | undefined)?.on && ((s.pipeline as Spec).on as Spec).push !== undefined, match: ["branches"],
      help: P.t86 },
    { path: "pipeline.on.push.tags", label: "Tags", type: "list", placeholder: "v*", section: P.t80,
      showIf: (s) => !!(s.pipeline as Spec | undefined)?.on && ((s.pipeline as Spec).on as Spec).push !== undefined, match: ["tags"],
      help: P.t87 },
    { path: "pipeline.on.pull_request", label: "Run on pull requests", type: "presence", section: P.t80, match: ["pull_request"],
      help: P.t88 },
    { path: "pipeline.supersede", label: P.t89, type: "bool", section: P.t80, match: ["supersede"],
      help: P.t90 },
    { path: "pull_requests.secrets", label: P.t19, type: "tri", section: "What runs may do", unsetMeans: P.t91,
      match: ["pull_requests"], help: P.t92 },
    { path: "drafts.allow", label: "Who may start draft runs", type: "select", section: "What runs may do", options: roleOpts(P.t91),
      match: ["drafts"], help: P.t93 },
    { path: "drafts.secrets", label: "Draft runs may read secrets", type: "tri", section: "What runs may do", unsetMeans: P.t91,
      help: P.t93 },
    { path: "schedules.enabled", label: "Scheduled runs", type: "tri", section: "What runs may do", unsetMeans: P.t91,
      match: ["schedules"], help: P.t93 },
    { path: "push.branch_prefix", label: P.t94, type: "text", placeholder: "launchway/", section: "Saving from the editor",
      match: ["push", "branch_prefix"], help: P.t95 },
    { path: "push.allow_direct", label: "Direct branches", type: "list", placeholder: "release/*", section: "Saving from the editor", match: ["allow_direct"],
      help: P.t96 },
    { path: "git_webhook.kind", label: "Git host webhook", type: "select", section: "Git webhook", match: ["git_webhook"],
      options: [{ value: "", label: "None" }, { value: "github", label: "GitHub" }, { value: "gitea", label: "Gitea" }, { value: "forgejo", label: "Forgejo" }, { value: "gitlab", label: "GitLab" }],
      help: P.t97 },
    { path: "git_webhook.secret_env", label: P.t98, type: "text", section: "Git webhook", placeholder: "LAUNCHWAY_SHOP_HOOK_SECRET",
      showIf: (s) => !!(s.git_webhook as Spec | undefined)?.kind, help: P.t99 },
    { path: "git_webhook.secret_file", label: "Webhook secret: file", type: "text", section: "Git webhook", placeholder: "/run/secrets/shop-hook",
      showIf: (s) => !!(s.git_webhook as Spec | undefined)?.kind, help: P.t100 },
  ],
  environment: [
    { path: "target", label: "Target", type: "text", placeholder: "prod-cluster",
      help: P.t101 },
    { path: "projects", label: P.t102, type: "list", placeholder: "shop, web-*",
      help: P.t103 },
    { path: "queue", label: "Queue", type: "select", options: [{ value: "", label: "newest (the default)" }, { value: "newest", label: "newest" }, { value: "all", label: "all" }],
      help: P.t104 },
    { path: "protected", label: "Protected", type: "bool",
      help: P.t105 },
  ],
  "runner-pool": [
    { path: "kind", label: "Kind", type: "select", options: [{ value: "", label: "deploy (the default)" }, { value: "deploy", label: "deploy" }, { value: "build", label: "build" }],
      help: P.t106 },
    { path: "projects", label: "{Projects} it serves", type: "list", placeholder: "*, or shop, web-*", showIf: (s) => s.kind === "build",
      help: P.t107 },
    { path: "events", label: "Only runs started by", type: "list", placeholder: "push, manual", showIf: (s) => s.kind === "build",
      help: `Build pools: limit it to runs of these events (${EVENTS.join(", ")}). Empty: all.` },
    { path: "targets", label: "Targets it serves", type: "list", placeholder: "prod-cluster", showIf: (s) => s.kind !== "build",
      help: P.t108 },
    { path: "token_env", label: P.t109, type: "text", placeholder: P.t110, showIf: (s) => s.kind !== "build",
      help: P.t111 },
    { path: "token_file", label: "Pool token: file", type: "text", showIf: (s) => s.kind !== "build",
      help: P.t100 },
    { path: "require_identity", label: "Only enrolled runners", type: "bool",
      help: P.t112 },
  ],
  "role-binding": [
    { path: "group", label: "Group", type: "text", placeholder: "platform-team", help: P.t113 },
    { path: "user", label: "User", type: "text", placeholder: "alice", help: "Or one person's user name." },
    { path: "role", label: "Role", type: "select",
      options: [{ value: "", label: "Choose a role" }, { value: "viewer", label: "viewer: sees runs and logs" }, { value: "operator", label: P.t114 }, { value: "admin", label: P.t115 }],
      help: P.t116 },
    { path: "projects", label: "On {projects}", type: "list", placeholder: "shop, web-*", help: P.t117 },
    { path: "environments", label: "Only for environments", type: "list", placeholder: "staging",
      help: P.t118 },
  ],
  policy: [
    { path: "description", label: "Description", type: "textarea", help: P.t119 },
    { path: "mode", label: "Mode", type: "select", options: [{ value: "", label: "Choose" }, { value: "report", label: "report: warn only" }, { value: "enforce", label: "enforce: block" }],
      help: P.t120 },
    { path: "scope.projects", label: "{Projects}", type: "list", section: "Applies to", placeholder: "*", help: P.t121 },
    { path: "scope.environments", label: "Environments", type: "list", section: "Applies to", placeholder: "prod", help: P.t122 },
    { path: "scope.branches", label: "Branches", type: "list", section: "Applies to", placeholder: "main", help: P.t123 },
    { path: "scope.events", label: "Events", type: "list", section: "Applies to", placeholder: "push, manual", help: `Any of ${EVENTS.join(", ")}.` },
  ],
  "policy-source": [
    { path: "library", label: "Library", type: "text", help: P.t124 },
    { path: "version", label: "Version", type: "text", placeholder: "v3", help: P.t125 },
    { path: "path", label: "Directory", type: "text", placeholder: "policies", help: P.t126 },
    { path: "projects", label: "{Projects} it may govern", type: "list", placeholder: "*", help: P.t127 },
    { path: "refresh", label: "Look again every", type: "duration", placeholder: "1m", help: P.t128 },
    { path: "stale_after", label: "Keep using an old version for", type: "duration", placeholder: "1h",
      help: P.t129 },
  ],
  "health-source": [
    { path: "type", label: "Type", type: "select", options: [{ value: "", label: "Choose a type" }, ...HEALTH_TYPES.map((t) => ({ value: t.value, label: t.label }))],
      help: P.t130 },
    { path: "projects", label: "{Projects} that may read it", type: "list", placeholder: "*", help: P.t131 },
  ],
  model: [
    { path: "provider", label: "Provider", type: "select", options: [{ value: "", label: "Choose" }, { value: "anthropic", label: "anthropic" }, { value: "openai", label: P.t132 }],
      help: P.t133 },
    { path: "model", label: "Model id", type: "text", placeholder: "claude-sonnet-5-5", help: P.t134 },
    { path: "endpoint", label: "Endpoint", type: "text", wide: true, placeholder: "https://api.anthropic.com",
      help: P.t135 },
    { path: "key", label: "API key (a reference)", type: "secretref", match: ["key"],
      help: P.t136 },
    { path: "projects", label: "{Projects} that may use it", type: "list", placeholder: "shop, web-*", help: "Patterns. Empty: none may." },
    { path: "max_tokens", label: "Output tokens per call", type: "number", section: "Limits", placeholder: "4096", help: "Default 4096, at most 128000." },
    { path: "calls_per_run", label: "Calls per run", type: "number", section: "Limits", placeholder: "20", help: "Default 20." },
    { path: "tokens_per_day", label: "Tokens per {project} per day", type: "number", section: "Limits", placeholder: "no limit", help: P.t137 },
    { path: "max_input_bytes", label: "Input bytes per call", type: "number", section: "Limits", placeholder: "262144", help: P.t138 },
    { path: "timeout", label: "Timeout per call", type: "duration", section: "Limits", placeholder: "2m", help: P.t139 },
    { path: "effort", label: "Effort", type: "select", section: "Limits", showIf: (s) => s.provider === "anthropic",
      options: [{ value: "", label: "the model's default" }, ...["low", "medium", "high", "xhigh", "max"].map((v) => ({ value: v, label: v }))], help: "anthropic only." },
    { path: "headers", label: "Extra headers", type: "map", section: "Limits", help: P.t140 },
  ],
  "step-resources": [
    { path: "limits.max_cpu", label: "Most CPU a step may ask for", type: "text", section: "Limits", placeholder: "8", help: P.t141 },
    { path: "limits.max_memory", label: P.t142, type: "text", section: "Limits", placeholder: "16Gi", help: P.t143 },
    { path: "limits.memory_per_thread", label: "Memory per thread", type: "text", section: "Limits", placeholder: "1Gi", help: "Default 1Gi, at least 64Mi." },
    { path: "defaults.step.cpu", label: "CPU a step gets", type: "text", section: P.t144, placeholder: "1", match: ["defaults.step", "cpu"], help: "What it is guaranteed." },
    { path: "defaults.step.cpu_limit", label: "CPU it may use at most", type: "text", section: P.t144, placeholder: "4", help: P.t145 },
    { path: "defaults.step.memory", label: "Memory a step gets", type: "text", section: P.t144, placeholder: "2Gi", match: ["defaults.step", "memory"], help: "What it is guaranteed." },
    { path: "defaults.step.memory_limit", label: "Memory it may use at most", type: "text", section: P.t144, placeholder: "6Gi",
      help: P.t146 },
  ],
  "sign-in": [
    { path: "issuer", label: P.t147, type: "text", help: P.t148 },
    { path: "client_id", label: "Client id", type: "text", help: P.t149 },
    { path: "redirect_url", label: "Redirect address", type: "text", help: P.t150 },
    { path: "client_secret_env", label: P.t151, type: "text",
      help: P.t152 },
    { path: "groups_claim", label: "Groups claim", type: "text", help: P.t153 },
    { path: "username_claim", label: "User name claim", type: "text", help: P.t154 },
    { path: "admins.groups", label: P.t155, type: "list", help: P.t156 },
    { path: "admins.users", label: "Or users", type: "list", help: P.t157 },
  ],
  "secret-store": [
    { path: "type", label: "Type", type: "select", options: [{ value: "", label: "Choose a type" }, ...SECRET_STORE_TYPES],
      help: P.t158 },
    { path: "description", label: "Description", type: "textarea", help: "What is kept there." },
    { path: "projects", label: "{Projects} that may use it", type: "list", placeholder: "*", help: P.t127 },
  ],
};

/** A field for the object's name, with what names may be. */
export function nameHelp(kind: string, lv?: Levels): string {
  const what = kind === "project" ? (lv?.project.singular.toLowerCase() ?? "project") : "object";
  if (kind === "policy") return P.t159;
  if (kind === "model") return P.t160;
  return `How the ${what} is named in URLs, pipelines and the command line: letters, digits, dots, hyphens and underscores. It cannot be changed later.`;
}

// ---- policy rules ----

export interface RuleField { key: string; label: string; type: "bool" | "number" | "text" | "list" | "select"; options?: string[]; help?: string }

/** The options of each rule, from docs/reference/policy-rules.md. A rule
 * not listed (a newer server's) is edited as JSON. */
export const RULES: Record<string, { scalar?: "duration"; templates?: true; fields?: RuleField[]; example: unknown }> = {
  library_versions: { example: { pinned: "tag" }, fields: [{ key: "pinned", label: "Pinned to", type: "select", options: ["tag", "commit"], help: P.t161 }] },
  required_templates: { templates: true, example: [{ library: "ci-templates", template: "security-scan" }] },
  step_images: { example: { digest: true }, fields: [
    { key: "digest", label: "Pinned by digest", type: "bool", help: P.t162 },
    { key: "registries", label: "Allowed repositories", type: "list", help: P.t163 }] },
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
  approvals: { example: { min: 1 }, fields: [{ key: "min", label: P.t164, type: "number", help: P.t165 }] },
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
  get deploy() { return terms().say(P.t166); },
  image: "before an image push",
};

/** withLevelNames rewrites "project(s)" in the fields' words with what the
 * installation calls a project. */
export function withLevelNames(fields: FieldDef[], lv?: Levels): FieldDef[] {
  if (!lv) return fields;
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
