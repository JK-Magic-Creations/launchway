package store

import "sort"

// Class says what a table is for the wall.
type Class int

const (
	// Global tables belong to the installation, not to a Base: the schema
	// marker, the keyring's root-wrapped keys, the list of Bases, accounts
	// (opaque ids only), the host's own identity.
	Global Class = iota + 1
	// Scoped tables (Base tables) carry base_id, begin every key with it, and (on PostgreSQL,
	// with several Bases on) a row-level security policy.
	Scoped
	// Pending tables will be Scoped tables in the stage named; until then they
	// are unprotected. The list is an allow-list that only shrinks: moving a
	// table to Scoped removes its line, and nothing may be added to it
	// (docs/development/the-wall.md).
	Pending
)

func (c Class) String() string {
	switch c {
	case Global:
		return "global"
	case Scoped:
		return "base"
	case Pending:
		return "pending"
	}
	return "?"
}

// Table is one row of the registry.
type Table struct {
	Name  string
	Class Class
	// Stage names the stage of docs/development/the-wall.md that moves a
	// Pending table (S2 ... S6); empty otherwise.
	Stage string
	// Why says in a few words what a Global table is, or what a Pending
	// table is keyed by today.
	Why string
}

// Tables lists every table of the schema once. A test migrates a fresh
// database and fails if a table is missing here, or listed and absent. A branch
// that adds a table adds its line in the same change (see the-wall.md).
var Tables = []Table{
	// Global.
	{Name: "meta", Class: Global, Why: "schema version and liveness"},
	{Name: "bases", Class: Global, Why: "the list of Bases (the operator's view)"},
	{Name: "accounts", Class: Global, Why: "opaque account ids only: no name, no address"},
	{Name: "setup_state", Class: Global, Why: "the installation's one-time first-administrator code"},
	{Name: "encryption_state", Class: Global, Why: "the installation id and the keyring's progress"},
	{Name: "encryption_keks", Class: Global, Why: "key-encryption keys wrapped by the root key"},
	{Name: "ssh_host_keys", Class: Global, Why: "the Git-over-SSH server's own identity"},
	{Name: "installation_limits", Class: Global, Why: "the operator's ceilings"},

	// Base, Stage 1: identity.
	{Name: "users", Class: Scoped},
	{Name: "sessions", Class: Scoped},
	{Name: "api_tokens", Class: Scoped},
	{Name: "local_accounts", Class: Scoped},
	{Name: "local_links", Class: Scoped},
	{Name: "local_recovery_codes", Class: Scoped},
	{Name: "memberships", Class: Scoped},

	// Pending S2: the run engine.
	{Name: "runs", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "run_counters", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "run_queue", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "run_variables", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "refs_seen", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "push_decisions", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "events", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "steps", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "logs", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "leases", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "step_inputs", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "step_waits", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "lane_holders", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "lane_waiting", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "schedule_fires", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "schedule_seen", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "ai_usage", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "ai_explanations", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "artifacts", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "artifact_files", Class: Pending, Stage: "S2", Why: "artifact"},
	{Name: "artifact_usage", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "coverage_reports", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "coverage_files", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "test_reports", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "test_cases", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "findings", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "finding_reports", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "finding_rules", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "sbom_reports", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "sbom_components", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "images", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "image_refs", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "verifications", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "verify_baselines", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "deployments", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "deployment_timeline", Class: Pending, Stage: "S2", Why: "deployment"},
	{Name: "deploy_queue", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "deploy_locks", Class: Pending, Stage: "S2", Why: "environment and project name"},
	{Name: "deploy_commands", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "environment_state", Class: Pending, Stage: "S2", Why: "environment and project name"},
	{Name: "rollbacks", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "rollback_reverts", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "policy_evaluations", Class: Pending, Stage: "S2", Why: "run"},
	{Name: "policy_waivers", Class: Pending, Stage: "S2", Why: "project name"},
	{Name: "policy_waiver_events", Class: Pending, Stage: "S2", Why: "waiver"},

	// Pending S3: Git hosting, pull requests, Git connections.
	{Name: "forge_connections", Class: Pending, Stage: "S3", Why: "connection name (unique installation-wide)"},
	{Name: "forge_repositories", Class: Pending, Stage: "S3", Why: "connection and path"},
	{Name: "forge_deliveries", Class: Pending, Stage: "S3", Why: "connection"},
	{Name: "forge_events", Class: Pending, Stage: "S3", Why: "installation-wide sequence"},
	{Name: "repo_refs", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "repo_packs", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "repo_generation", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "repo_maintenance", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "repo_mirrors", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "repo_pull_settings", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "repo_webhooks", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "repo_hook_deliveries", Class: Pending, Stage: "S3", Why: "webhook"},
	{Name: "branch_rules", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "branch_writers", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "imported_issues", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "imported_comments", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "pull_requests", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "pull_counters", Class: Pending, Stage: "S3", Why: "repo_id"},
	{Name: "pull_comments", Class: Pending, Stage: "S3", Why: "pull request"},
	{Name: "pull_people", Class: Pending, Stage: "S3", Why: "pull request"},
	{Name: "pull_reviews", Class: Pending, Stage: "S3", Why: "pull request"},
	{Name: "pull_revisions", Class: Pending, Stage: "S3", Why: "pull request"},
	{Name: "pull_runs", Class: Pending, Stage: "S3", Why: "pull request and run"},
	{Name: "ssh_keys", Class: Pending, Stage: "S3", Why: "owner name; fingerprint unique installation-wide"},

	// Pending S4: settings, variables, keys.
	{Name: "config_objects", Class: Pending, Stage: "S4", Why: "org (Wing)"},
	{Name: "config_changes", Class: Pending, Stage: "S4", Why: "org (Wing)"},
	{Name: "config_imports", Class: Pending, Stage: "S4", Why: "org (Wing)"},
	{Name: "config_revisions", Class: Pending, Stage: "S4", Why: "org (Wing)"},
	{Name: "variables", Class: Pending, Stage: "S4", Why: "org (Wing)"},
	{Name: "variable_changes", Class: Pending, Stage: "S4", Why: "org (Wing)"},
	{Name: "encryption_deks", Class: Pending, Stage: "S4", Why: "purpose and org: org becomes the Base"},
	{Name: "policy_sources", Class: Pending, Stage: "S4", Why: "name"},
	{Name: "step_resource_defaults", Class: Pending, Stage: "S4", Why: "kind"},
	{Name: "step_resource_limits", Class: Pending, Stage: "S4", Why: "name"},
	{Name: "target_observers", Class: Pending, Stage: "S4", Why: "target"},

	// Pending S5: Stations, caches, blobs.
	{Name: "runners", Class: Pending, Stage: "S5", Why: "none: every runner serves the installation"},
	{Name: "runner_join_tokens", Class: Pending, Stage: "S5", Why: "pool; hash unique installation-wide"},
	{Name: "runner_identities", Class: Pending, Stage: "S5", Why: "pool"},
	{Name: "runner_access_tokens", Class: Pending, Stage: "S5", Why: "hash"},
	{Name: "runner_assertions", Class: Pending, Stage: "S5", Why: "assertion id"},
	{Name: "caches", Class: Pending, Stage: "S5", Why: "project name and scope"},
	{Name: "cache_policy", Class: Pending, Stage: "S5", Why: "project name"},
	{Name: "cache_usage", Class: Pending, Stage: "S5", Why: "project name"},
	{Name: "artifact_blobs", Class: Pending, Stage: "S5", Why: "content hash shared across projects"},
	{Name: "blob_uploads", Class: Pending, Stage: "S5", Why: "key"},
	{Name: "blob_parts", Class: Pending, Stage: "S5", Why: "upload"},
	{Name: "blob_chunks", Class: Pending, Stage: "S5", Why: "upload"},
	{Name: "blob_objects", Class: Pending, Stage: "S5", Why: "key"},

	// Pending S6: audit, notifications, webhooks.
	{Name: "audit", Class: Pending, Stage: "S6", Why: "username; installation-wide sequence"},
	{Name: "notifications", Class: Pending, Stage: "S6", Why: "project name"},
	{Name: "notify_state", Class: Pending, Stage: "S6", Why: "name"},
	{Name: "webhook_deliveries", Class: Pending, Stage: "S6", Why: "project name"},
}

// Lookup returns the registry line of a table.
func Lookup(name string) (Table, bool) {
	for _, t := range Tables {
		if t.Name == name {
			return t, true
		}
	}
	return Table{}, false
}

// Names returns the names of the tables of a class, sorted.
func Names(c Class) []string {
	var out []string
	for _, t := range Tables {
		if t.Class == c {
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

// PendingByStage counts Pending tables by stage.
func PendingByStage() map[string]int {
	out := map[string]int{}
	for _, t := range Tables {
		if t.Class == Pending {
			out[t.Stage]++
		}
	}
	return out
}
