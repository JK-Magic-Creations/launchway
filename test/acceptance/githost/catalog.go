package githostaccept

// How a test is covered by this suite.
const (
	Full    = "automated" // the whole acceptance test runs here
	Partial = "partial"   // the repository-level part runs here; the rest needs the homelab
	Manual  = "homelab"   // cannot run against a throwaway repository; see Why
)

// Entry is one acceptance test of doc 32 section 2.1.
type Entry struct {
	ID, Name, Coverage string
	// Why says what is not automated here, and what runs it instead.
	Why string
}

// Catalog lists the 31 acceptance tests and AT-GAP-9, in order.
var Catalog = []Entry{
	{"AT-HG-01", "Import: refs and objects byte-identical, fsck clean, second import changes nothing", Partial, "The 44 real repositories are compared by the S40 import tooling (it needs both hosts); here the same comparison runs on a seeded repository pushed with push --mirror."},
	{"AT-HG-02", "Smart HTTP: clone, fetch, push, non-fast-forward refused, shallow, partial, protocol v2, large push, both URL shapes", Partial, "A pod in argocd and the 100 MiB per-object message are homelab checks; the gateway name is covered by AT-HG-19."},
	{"AT-HG-03", "SSH Git access", Manual, "Out of Milestone 1 (doc 34 section 12: no SSH; the homelab holds no SSH keys). Port 22 closed is a doctor check on Launchway."},
	{"AT-HG-04", "Pull mirror of an external repository, read-only, refreshed", Full, ""},
	{"AT-HG-05", "Push mirror to a remote, refs equal after a push", Partial, "41 repositories to GitHub with deploy keys and the broken-key alert need GitHub and the alert rules; here a push mirror to a local Git server."},
	{"AT-HG-06", "Archived repository refuses pushes, reads work, unarchive restores", Full, ""},
	{"AT-HG-07", "Private by default, anonymous and wrong tokens refused, unknown name looks like a private one", Partial, "A read-only token for one repository needs a second identity; supply LW_ACCEPT_RO_TOKEN and LW_ACCEPT_RO_REPO. the existing host's tokens are not repository-scoped, and creating a user on the live host is forbidden."},
	{"AT-HG-08", "Protected tags: a v* tag cannot be moved or deleted; the audit log shows the attempt", Partial, "The audit-log half needs the host's audit API (Launchway); here the refusal is tested."},
	{"AT-HG-09", "Pull requests: open, list, merge, state and timestamps", Partial, "The 16 real pull requests are compared against the existing host's by the importer; here the lifecycle on a test repository."},
	{"AT-HG-10", "Issues: labels, milestone, comment, close, timestamps", Partial, "The 681 real issues (doc 27 section 10.2) are compared by the importer and its queue tests."},
	{"AT-HG-11", "No wiki, releases, LFS or packages", Full, "The package count needs a token with read:package; without one the check reports what it could not read."},
	{"AT-HG-12", "Every workflow migrated, green run per repository, 14 days without an old-host runner", Manual, "Needs Launchway pipelines and a 14-day window on the homelab."},
	{"AT-HG-13", "Argo Events chain removed, no webhook errors for 7 days", Manual, "Needs the cluster (kubectl get eventsource,sensor) and a 7-day window. Webhook delivery itself is in AT-HG-14."},
	{"AT-HG-14", "Argo CD style polling sees a push quickly; webhook delivery with a signature", Partial, "The 60 applications Synced with equal revisions is a homelab check (argocd app list)."},
	{"AT-HG-15", "Automated write path: bump commit, then revert commit", Partial, "Least privilege (the identity cannot write elsewhere) needs a second token and the Launchway promote step."},
	{"AT-HG-16", "Commit statuses: pending then success or failure, combined state", Partial, "Beacon's Deliveries view and blocking a proposal on a failing run need those systems."},
	{"AT-HG-17", "Sign-in starts an OIDC redirect", Partial, "The full Authentik login, MFA path and both-replica callback are interactive."},
	{"AT-HG-18", "Machine identities: bad and anonymous credentials refused at once; no token in output", Partial, "The Vault path inventory and secret-scan of logs and Hubble exports need the cluster."},
	{"AT-HG-19", "Network: health route, second address, large push", Partial, "Twelve namespaces admitted and a 13th refused, and the same after a node reboot, are homelab checks."},
	{"AT-HG-20", "Read API surface consumers use (version, user, search, commits, contents)", Partial, "Beacon, mcp-accounts, jkma-sandbox, Homepage and the Blackbox probe against Launchway, and the existing host scaled to zero for an hour, are homelab checks."},
	{"AT-HG-21", "Laptop remotes repointed by script", Manual, "Needs the repointing script (S40-10) and the 128 working directories."},
	{"AT-HG-22", "Backlog tool replaced by launchway work import/sync", Manual, "Needs the importer (S35-32) and its ported tests."},
	{"AT-HG-23", "Documentation and skills updated", Manual, "A grep across homelab-ops, skills and memory on the laptop."},
	{"AT-HG-24", "Backup and restore byte-identical", Partial, "A host-side backup and restore (LW_ACCEPT_BACKUP_CMD), the 1,000 kill-injections, the off-node copy and the alerts need Launchway and the homelab; here a bundle backup restored into a second repository."},
	{"AT-HG-25", "Health probe fast; metrics exposed", Partial, "ServiceProbeDown firing in under 5 minutes and the backup alert names need Prometheus. Metrics are required of Launchway only (the existing host's are off)."},
	{"AT-HG-26", "p95 clone and push latency", Full, "The 30-day steady memory and no OOM kill are read from the soak, not here."},
	{"AT-HG-27", "Canary upgrade with Git endpoints in the analysis", Manual, "Needs Launchway's canary and a bad image."},
	{"AT-HG-28", "Availability: replica kill, node loss, break-glass", Manual, "Needs two Launchway replicas and 30 days of soak."},
	{"AT-HG-29", "Vault rule: every credential in Vault, delivered by External Secrets", Manual, "Needs Vault and kubectl."},
	{"AT-HG-30", "Code browsing: tree, blob, commits, compare, raw, archive, search", Full, ""},
	{"AT-HG-31", "Retirement leaves Launchway healthy", Manual, "Runs once, after the owner's final approval."},
	{"AT-GAP-9", "Traffic census", Manual, "census/census.py summarises a window of access logs; it reads Loki or a log file and is not a test of the host."},
}
