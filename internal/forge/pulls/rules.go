package pulls

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// Roles of a person on a repository, lowest first.
const (
	RoleNone     = ""
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

func roleRank(r string) int {
	switch r {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// Rule is what a protected branch asks of a pull request that targets it. A
// pull request into a branch that no rule covers needs only to be open, not a
// draft, and mergeable.
type Rule struct {
	// Pattern names the branches the rule covers: main, release/*.
	Pattern string `json:"pattern"`
	// RequiredApprovals is how many people must approve; ApprovalRole is the
	// lowest role an approver may have (operator: anyone who can push;
	// admin: an administrator of the repository).
	RequiredApprovals int    `json:"required_approvals"`
	ApprovalRole      string `json:"approval_role"`
	// RequiredChecks name checks that must have passed on the head: a
	// pipeline (build), or one of its steps (build/test); a bare name also
	// matches a step of that name in any pipeline.
	RequiredChecks []string `json:"required_checks"`
	// RequireUpToDate asks the head to contain the target's tip.
	RequireUpToDate bool `json:"require_up_to_date"`
	// RequireResolved asks every review thread to be resolved.
	RequireResolved bool `json:"require_resolved_threads"`
	// NoSelfApproval keeps the author's own approval from counting.
	NoSelfApproval bool `json:"no_self_approval"`
	// RequireSigned is reserved: signed commits are not checked yet, so a rule
	// that asks for them is refused instead of pretending.
	RequireSigned bool `json:"require_signed_commits"`
	// BypassAdmins lets an administrator of the repository merge anyway, saying
	// why; the reason and what was bypassed are recorded.
	BypassAdmins bool `json:"allow_admin_bypass"`
	// RequireOwnerApproval asks that, for every changed file the repository's
	// owners file (see Owners) names an owner of, one of that file's owners
	// approved. A file nobody owns needs nothing extra.
	RequireOwnerApproval bool `json:"require_owner_approval"`
	// DismissStale dismisses approvals when new commits arrive on a pull
	// request into these branches, whatever the repository setting says.
	DismissStale bool `json:"dismiss_stale_approvals"`
	// RequireFreshApproval asks that the latest push be approved by someone
	// other than the person who pushed it.
	RequireFreshApproval bool `json:"require_latest_push_approved_by_other"`
	// PathChecks are checks required only when a changed file matches.
	PathChecks []PathCheck `json:"required_checks_by_path"`
	// MergeQueue sends merges into these branches through the merge queue:
	// a pull request is queued, tested on the tip plus the entries ahead,
	// and merged in order.
	MergeQueue bool `json:"merge_queue"`
}

// PathCheck requires a check only when the pull request changes a file that
// matches one of Paths (the patterns of the owners file: docs/, *.go,
// /cmd/**). A pipeline kept from running by its own path filter never blocks:
// the check counts as satisfied, as for any required check.
type PathCheck struct {
	Paths []string `json:"paths"`
	Check string   `json:"check"`
}

// ruleExtra is what the extra column holds.
type ruleExtra struct {
	OwnerApproval bool        `json:"owner_approval,omitempty"`
	DismissStale  bool        `json:"dismiss_stale,omitempty"`
	FreshApproval bool        `json:"fresh_approval,omitempty"`
	PathChecks    []PathCheck `json:"path_checks,omitempty"`
	Queue         bool        `json:"queue,omitempty"`
}

func (r Rule) extra() string {
	b, _ := json.Marshal(ruleExtra{r.RequireOwnerApproval, r.DismissStale, r.RequireFreshApproval, r.PathChecks, r.MergeQueue})
	return string(b)
}

func (r *Rule) setExtra(s string) {
	var e ruleExtra
	_ = json.Unmarshal([]byte(s), &e)
	r.RequireOwnerApproval, r.DismissStale, r.RequireFreshApproval, r.PathChecks, r.MergeQueue = e.OwnerApproval, e.DismissStale, e.FreshApproval, e.PathChecks, e.Queue
	if r.PathChecks == nil {
		r.PathChecks = []PathCheck{}
	}
}

// Validate checks a rule a person sent.
func (r *Rule) Validate() error {
	r.Pattern = strings.TrimSpace(r.Pattern)
	if r.Pattern == "" || len(r.Pattern) > 200 || strings.ContainsAny(r.Pattern, " \\[]?\x00") || strings.HasPrefix(r.Pattern, "/") || strings.HasPrefix(r.Pattern, "refs/") {
		return invalid("%q is not a branch name or pattern: use names such as main or release/*", r.Pattern)
	}
	if _, err := path.Match(r.Pattern, "x"); err != nil {
		return invalid("%q is not a valid pattern", r.Pattern)
	}
	if r.RequiredApprovals < 0 || r.RequiredApprovals > 20 {
		return invalid("required approvals is between 0 and 20")
	}
	if r.ApprovalRole == "" {
		r.ApprovalRole = RoleOperator
	}
	if r.ApprovalRole != RoleOperator && r.ApprovalRole != RoleAdmin {
		return invalid("approvals come from the operator or the admin role")
	}
	if r.RequireSigned {
		return invalid("signed commits are not checked yet, so a rule cannot ask for them; leave it off")
	}
	checks := []string{}
	seen := map[string]bool{}
	for _, c := range r.RequiredChecks {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if len(c) > 200 || strings.ContainsAny(c, "\x00\n\r`") {
			return invalid("%q is not a check name: use a pipeline name (build) or a step (build/test)", c)
		}
		seen[c] = true
		checks = append(checks, c)
	}
	if len(checks) > 50 {
		return invalid("at most 50 required checks")
	}
	r.RequiredChecks = checks
	if len(r.PathChecks) > 50 {
		return invalid("at most 50 checks required by path")
	}
	for i := range r.PathChecks {
		pc := &r.PathChecks[i]
		pc.Check = strings.TrimSpace(pc.Check)
		if pc.Check == "" || len(pc.Check) > 200 || strings.ContainsAny(pc.Check, "\x00\n\r`") {
			return invalid("required check %d by path has no valid check name", i+1)
		}
		if len(pc.Paths) == 0 || len(pc.Paths) > 50 {
			return invalid("`%s` is required by path: give 1 to 50 paths (docs/, *.go, /cmd/**)", pc.Check)
		}
		for j, p := range pc.Paths {
			p = strings.TrimSpace(p)
			if err := validOwnerPattern(p); err != nil {
				return invalid("`%s`: %v", pc.Check, err)
			}
			pc.Paths[j] = p
		}
	}
	if r.PathChecks == nil {
		r.PathChecks = []PathCheck{}
	}
	return nil
}

const ruleCols = `pattern, required_approvals, approval_role, required_checks, require_up_to_date, require_resolved, no_self_approval, require_signed, bypass_admins, extra`

// Rules lists a repository's rules, by pattern.
func (s *Store) Rules(ctx context.Context, repoID string) ([]Rule, error) {
	rows, err := s.db.Query(ctx, `SELECT `+ruleCols+` FROM branch_rules WHERE repo_id = ? ORDER BY pattern`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		var checks string
		var up, res, self, sig, byp int
		var extra string
		if err := rows.Scan(&r.Pattern, &r.RequiredApprovals, &r.ApprovalRole, &checks, &up, &res, &self, &sig, &byp, &extra); err != nil {
			return nil, err
		}
		r.setExtra(extra)
		_ = json.Unmarshal([]byte(checks), &r.RequiredChecks)
		if r.RequiredChecks == nil {
			r.RequiredChecks = []string{}
		}
		r.RequireUpToDate, r.RequireResolved, r.NoSelfApproval, r.RequireSigned, r.BypassAdmins = up != 0, res != 0, self != 0, sig != 0, byp != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRule writes the merge rules of one pattern, protecting the pattern if it
// was not protected yet.
func (s *Store) SetRule(ctx context.Context, repoID string, r Rule, by string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	checks, _ := json.Marshal(r.RequiredChecks)
	res, err := s.db.Exec(ctx, `UPDATE branch_rules SET required_approvals = ?, approval_role = ?, required_checks = ?, require_up_to_date = ?, require_resolved = ?,
		no_self_approval = ?, require_signed = 0, bypass_admins = ?, extra = ? WHERE repo_id = ? AND pattern = ?`,
		r.RequiredApprovals, r.ApprovalRole, string(checks), b2i(r.RequireUpToDate), b2i(r.RequireResolved), b2i(r.NoSelfApproval), b2i(r.BypassAdmins), r.extra(), repoID, r.Pattern)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.db.Exec(ctx, `INSERT INTO branch_rules(id, repo_id, pattern, push_role, created_at, created_by, required_approvals, approval_role, required_checks,
		require_up_to_date, require_resolved, no_self_approval, bypass_admins, extra) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ids.New("rul"), repoID, r.Pattern, "admin", ms(s.now()), by, r.RequiredApprovals, r.ApprovalRole, string(checks),
		b2i(r.RequireUpToDate), b2i(r.RequireResolved), b2i(r.NoSelfApproval), b2i(r.BypassAdmins), r.extra())
	return err
}

// RuleFor combines the rules whose patterns cover a branch into the strictest
// one: the most approvals, the highest role, every required check, and each
// yes of any of them. ok is false when no rule covers the branch.
func RuleFor(rules []Rule, branch string) (Rule, bool) {
	var out Rule
	found := false
	for _, r := range rules {
		if m, _ := path.Match(r.Pattern, branch); !m {
			continue
		}
		if !found {
			out, found = r, true
			out.Pattern = branch
			out.RequiredChecks = append([]string{}, r.RequiredChecks...)
			out.BypassAdmins = r.BypassAdmins
			out.PathChecks = append([]PathCheck{}, r.PathChecks...)
			continue
		}
		out.RequiredApprovals = max(out.RequiredApprovals, r.RequiredApprovals)
		if roleRank(r.ApprovalRole) > roleRank(out.ApprovalRole) {
			out.ApprovalRole = r.ApprovalRole
		}
		out.RequiredChecks = append(out.RequiredChecks, r.RequiredChecks...)
		out.RequireUpToDate = out.RequireUpToDate || r.RequireUpToDate
		out.RequireResolved = out.RequireResolved || r.RequireResolved
		out.NoSelfApproval = out.NoSelfApproval || r.NoSelfApproval
		out.RequireOwnerApproval = out.RequireOwnerApproval || r.RequireOwnerApproval
		out.DismissStale = out.DismissStale || r.DismissStale
		out.RequireFreshApproval = out.RequireFreshApproval || r.RequireFreshApproval
		out.MergeQueue = out.MergeQueue || r.MergeQueue
		out.PathChecks = append(out.PathChecks, r.PathChecks...)
		out.BypassAdmins = out.BypassAdmins && r.BypassAdmins // every covering rule must allow it
	}
	if found {
		sort.Strings(out.RequiredChecks)
		out.RequiredChecks = dedupe(out.RequiredChecks)
	}
	return out, found
}

func dedupe(in []string) []string {
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// RunInfo is what the pull request needs to know of a run of its pipelines.
type RunInfo struct {
	ID       string     `json:"id"`
	Project  string     `json:"project"`
	Number   int        `json:"number"`
	Pipeline string     `json:"pipeline"`
	Status   string     `json:"status"`
	SHA      string     `json:"sha"`
	Created  int64      `json:"created_at"`
	Steps    []StepInfo `json:"steps"`
	// PathSkipped marks a head that a trigger's path filter kept from starting
	// a run (no pipeline, no steps): the required checks of that head count
	// as satisfied, since the repository's own rule says nothing needed to run.
	PathSkipped bool `json:"path_skipped,omitempty"`
}

// StepInfo is one step of a run.
type StepInfo struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Allowed bool   `json:"allowed_to_fail,omitempty"`
	// SkipKind says why a skipped step was skipped; "paths" is a path rule.
	SkipKind string `json:"skip_kind,omitempty"`
}

// RunSource finds the runs of a pull request: every run whose trigger is the
// pull request (event pull_request, its number) in a project that watches
// the repository. The API provides it from the engine.
type RunSource interface {
	RunsOf(ctx context.Context, repoID, repoPath string, number int) ([]RunInfo, error)
}

// Check states.
const (
	CheckPending  = "pending"
	CheckRunning  = "running"
	CheckPassed   = "passed"
	CheckFailed   = "failed"
	CheckCanceled = "canceled"
	CheckSkipped  = "skipped"
	// CheckPathSkipped is a step or pipeline that a path rule kept from
	// running: it satisfies a required check.
	CheckPathSkipped = "skipped_by_paths"
	CheckMissing     = "missing"
)

// Check is the result of one pipeline or one step on the head of a pull request.
type Check struct {
	// Name is the pipeline (build) or the step (build/test).
	Name     string `json:"name"`
	Status   string `json:"status"`
	Required bool   `json:"required"`
	RunID    string `json:"run_id,omitempty"`
	Project  string `json:"project,omitempty"`
	Run      int    `json:"run_number,omitempty"`
}

func checkStatus(engineStatus string) string {
	switch engineStatus {
	case "succeeded":
		return CheckPassed
	case "failed":
		return CheckFailed
	case "canceled":
		return CheckCanceled
	case "running":
		return CheckRunning
	case "skipped":
		return CheckSkipped
	}
	return CheckPending
}

// Checks lists the results on the head of a pull request, newest run of each
// pipeline, with the required checks that have no result as missing.
func (s *Service) Checks(ctx context.Context, repoID, repoPath string, p PullRequest, required []string) ([]Check, []RunInfo, error) {
	var runs []RunInfo
	if s.Runs != nil {
		all, err := s.Runs.RunsOf(ctx, repoID, repoPath, p.Number)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range all {
			if r.SHA == p.HeadSHA {
				runs = append(runs, r)
			}
		}
	}
	pathSkipped := false
	for _, r := range runs {
		pathSkipped = pathSkipped || r.PathSkipped
	}
	latest := map[string]RunInfo{}
	for _, r := range runs {
		if r.PathSkipped {
			continue
		}
		key := r.Project + "\x00" + r.Pipeline
		if old, ok := latest[key]; !ok || r.Created > old.Created {
			latest[key] = r
		}
	}
	keys := make([]string, 0, len(latest))
	for k := range latest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var checks []Check
	var used []RunInfo
	for _, k := range keys {
		r := latest[k]
		used = append(used, r)
		checks = append(checks, Check{Name: r.Pipeline, Status: checkStatus(r.Status), RunID: r.ID, Project: r.Project, Run: r.Number})
		for _, st := range r.Steps {
			status := checkStatus(st.Status)
			if st.Status == "failed" && st.Allowed {
				status = CheckPassed
			}
			if st.Status == "skipped" && st.SkipKind == "paths" {
				status = CheckPathSkipped
			}
			checks = append(checks, Check{Name: r.Pipeline + "/" + st.Name, Status: status, RunID: r.ID, Project: r.Project, Run: r.Number})
		}
	}
	for _, r := range used {
		_ = s.Store.LinkRun(ctx, p.ID, r.ID, p.HeadSHA)
	}
	for i := range checks {
		checks[i].Required = isRequired(required, checks[i].Name)
	}
	for _, name := range required {
		if _, ok := findCheck(checks, name); !ok {
			status := CheckMissing
			if pathSkipped {
				status = CheckPathSkipped
			}
			checks = append(checks, Check{Name: name, Status: status, Required: true})
		}
	}
	return checks, used, nil
}

// isRequired says whether a rule's required name asks for a check: the exact
// name, or a bare name that is a pipeline's or a step's.
func isRequired(required []string, check string) bool {
	for _, r := range required {
		if matchesCheck(r, check) {
			return true
		}
	}
	return false
}

func matchesCheck(required, check string) bool {
	if required == check {
		return true
	}
	// A step repeated by for_each is several checks, ci/test[go=1.26] and so
	// on. Naming ci/test (or test) asks for all of them, so the group is one
	// check; naming a member, ci/test[go=1.26], asks for that one alone.
	if !strings.Contains(required, "[") {
		if base, ok := memberBase(check); ok && matchesCheck(required, base) {
			return true
		}
	}
	if !strings.Contains(required, "/") {
		_, step, ok := strings.Cut(check, "/")
		return ok && step == required
	}
	return false
}

// findCheck finds the check a required name asks for. A bare name that two
// pipelines both have as a step is satisfied only when all of them pass, so
// the worst result is the one returned.
func findCheck(checks []Check, required string) (Check, bool) {
	var worst Check
	found := false
	for _, c := range checks {
		if !matchesCheck(required, c.Name) || c.Status == CheckMissing {
			continue
		}
		if !found || checkRank(c.Status) > checkRank(worst.Status) {
			worst, found = c, true
		}
	}
	return worst, found
}

func checkRank(s string) int {
	switch s {
	case CheckPassed:
		return 0
	case CheckSkipped, CheckPathSkipped:
		return 1
	case CheckRunning:
		return 2
	case CheckPending:
		return 3
	case CheckCanceled:
		return 4
	case CheckFailed:
		return 5
	}
	return 6
}

// Blocker is one reason a pull request cannot merge yet.
type Blocker struct {
	// Code is stable: draft, state, conflicts, checking, behind, approvals,
	// changes_requested, check, threads, method.
	Code string `json:"code"`
	Why  string `json:"why"`
}

// Readiness says whether a pull request can merge now, and if not, why, in
// words: the server computes it once and every client shows it.
type Readiness struct {
	CanMerge bool `json:"can_merge"`
	// Why is one plain sentence: "needs 1 more approval; `test` has not
	// passed". Empty when the pull request can merge.
	Why      string    `json:"why,omitempty"`
	Blockers []Blocker `json:"blockers"`
	// CanBypass says the blockers may be bypassed by an administrator who
	// gives a reason; it is only set for the person asking.
	CanBypass bool `json:"can_bypass,omitempty"`
	// Rule is the combined rule that covers the target branch (nil: none).
	Rule *Rule `json:"rule,omitempty"`
	// Approvals counts those that count toward the rule.
	Approvals        int      `json:"approvals"`
	ChangesRequested []string `json:"changes_requested_by"`
	OpenThreads      int      `json:"open_threads"`
	Checks           []Check  `json:"checks"`
	// Methods are the ways the repository allows to merge.
	Methods       []string `json:"methods"`
	DefaultMethod string   `json:"default_method"`
	// NeedsOwners are the changed files whose owners have not approved yet,
	// each with who may (only when the rule asks for owner approval).
	NeedsOwners []FileOwners `json:"needs_owner_approval,omitempty"`
	// Queue says merges into this branch go through the merge queue.
	Queue bool `json:"queue,omitempty"`

	counted []Review // the approvals that count, newest per person
}

// IsWIP says a title marks the work as in progress: it begins with WIP or
// Draft (any case) and a colon, or the same in brackets.
func IsWIP(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	for _, p := range []string{"wip:", "[wip]", "wip -", "draft:", "[draft]", "(wip)"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// plural returns "1 more approval" or "2 more approvals".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Readiness works out whether a pull request may merge. actorRole is the role
// of the person asking (for bypass).
func (s *Service) Readiness(ctx context.Context, repoID, repoPath string, p PullRequest, actorRole string) (Readiness, error) {
	r := Readiness{Blockers: []Blocker{}, ChangesRequested: []string{}, Checks: []Check{}}
	set, err := s.Store.Settings(ctx, repoID)
	if err != nil {
		return r, err
	}
	r.Methods, r.DefaultMethod = set.Allowed(), set.DefaultMethod
	rules, err := s.Store.Rules(ctx, repoID)
	if err != nil {
		return r, err
	}
	rule, covered := RuleFor(rules, p.TargetBranch)
	if covered {
		r.Rule = &rule
	}
	if p.State != Open {
		r.Blockers = append(r.Blockers, Blocker{Code: "state", Why: "it is already " + string(p.State)})
		r.Why = "this pull request is already " + string(p.State)
		return r, nil
	}
	r.Queue = covered && rule.MergeQueue
	r.stateBlockers(p)
	if err := s.reviewBlockers(ctx, p, rule, covered, &r); err != nil {
		return r, err
	}
	var changed []string
	if covered && (rule.RequireOwnerApproval || len(rule.PathChecks) > 0) {
		if changed, err = s.ChangedPaths(ctx, repoID, p); err != nil {
			return r, err
		}
	}
	if covered && rule.RequireOwnerApproval {
		if err := s.ownerBlockers(ctx, repoID, p, changed, &r); err != nil {
			return r, err
		}
	}
	if err := s.checkBlockers(ctx, repoID, repoPath, p, rule, covered, changed, &r); err != nil {
		return r, err
	}
	if len(r.Methods) == 0 {
		r.block("method", "the repository allows no way of merging")
	}
	r.conclude(rule, covered, actorRole, p)
	return r, nil
}

func (r *Readiness) block(code, why string) {
	r.Blockers = append(r.Blockers, Blocker{Code: code, Why: why})
}

// stateBlockers: a draft, conflicts, a judgment that is not made yet.
func (r *Readiness) stateBlockers(p PullRequest) {
	if p.Draft {
		r.block("draft", "it is a draft; mark it ready for review")
	} else if IsWIP(p.Title) {
		r.block("wip", "its title says it is work in progress; take WIP or Draft off the title when it is ready")
	}
	switch p.Mergeability.State {
	case MergeConflicts:
		r.block("conflicts", "it has conflicts in "+strings.Join(p.Mergeability.Files, ", ")+"; update the branch and resolve them")
	case MergeChecking, MergeUnknown:
		why := "it is still being checked for conflicts"
		if p.Mergeability.State == MergeUnknown && p.Mergeability.Reason != "" {
			why = "it could not be checked for conflicts (" + p.Mergeability.Reason + ")"
		}
		r.block("checking", why)
	}
}

// reviewBlockers: approvals, requested changes, up to date, open threads.
func (s *Service) reviewBlockers(ctx context.Context, p PullRequest, rule Rule, covered bool, r *Readiness) error {
	reviews, err := s.Store.Reviews(ctx, p.ID)
	if err != nil {
		return err
	}
	comments, err := s.Store.Comments(ctx, p.ID)
	if err != nil {
		return err
	}
	open := map[string]bool{}
	for _, c := range comments {
		if c.Path != "" && c.ThreadID == c.ID && !c.Resolved && !c.Deleted {
			open[c.ThreadID] = true
		}
	}
	r.OpenThreads = len(open)
	approvers := map[string]bool{}
	latest := map[string]Review{}
	for _, rv := range reviews { // oldest first: the last non-comment review of each person stands
		if rv.State == "submitted" && rv.Verdict != VerdictComment {
			latest[rv.Author] = rv
		}
	}
	for who, rv := range latest {
		switch rv.Verdict {
		case VerdictRequestChanges:
			r.ChangesRequested = append(r.ChangesRequested, who)
		case VerdictApprove:
			if covered && (rule.NoSelfApproval && who == p.Author || roleRank(rv.Role) < roleRank(rule.ApprovalRole)) {
				continue
			}
			approvers[who] = true
			r.counted = append(r.counted, rv)
		}
	}
	sort.Strings(r.ChangesRequested)
	r.Approvals = len(approvers)
	if len(r.ChangesRequested) > 0 {
		r.block("changes_requested", strings.Join(r.ChangesRequested, ", ")+" asked for changes")
	}
	if !covered {
		return nil
	}
	if need := rule.RequiredApprovals - r.Approvals; need > 0 {
		why := "needs " + plural(need, "more approval", "more approvals")
		if rule.ApprovalRole == RoleAdmin {
			why += " from an administrator"
		}
		r.block("approvals", why)
	}
	if rule.RequireUpToDate && p.Mergeability.Behind > 0 {
		r.block("behind", fmt.Sprintf("it is %s behind %s and must be up to date; update the branch", plural(p.Mergeability.Behind, "commit", "commits"), p.TargetBranch))
	}
	if rule.RequireResolved && r.OpenThreads > 0 {
		r.block("threads", plural(r.OpenThreads, "conversation is", "conversations are")+" not resolved")
	}
	if rule.RequireFreshApproval {
		s.freshBlocker(ctx, p, r)
	}
	return nil
}

// freshBlocker: the latest push must be approved, on its own commit, by
// someone other than the person who pushed it.
func (s *Service) freshBlocker(ctx context.Context, p PullRequest, r *Readiness) {
	revs, err := s.Store.Revisions(ctx, p.ID)
	pusher := ""
	if err == nil && len(revs) > 0 {
		pusher = revs[len(revs)-1].Actor
	}
	if pusher == "" {
		pusher = p.Author
	}
	for _, rv := range r.counted {
		if rv.CommitSHA == p.HeadSHA && rv.Author != pusher {
			return
		}
	}
	r.block("latest_push", "the latest push, by "+pusher+", needs an approval of its own commit from someone else")
}

// ownerBlockers: for each changed file that has owners, one of them must have
// approved (by name, or through a group they belonged to when they reviewed).
// The owners file is read from the target branch; one with problems blocks,
// since what it means cannot be trusted.
func (s *Service) ownerBlockers(ctx context.Context, repoID string, p PullRequest, changed []string, r *Readiness) error {
	view, _, err := s.Owners(ctx, repoID, p, changed)
	if err != nil {
		return err
	}
	if len(view.Problems) > 0 {
		r.block("owners_file", OwnersDoc{Problems: view.Problems}.Valid().Error()[len(ErrInvalid.Error())+2:]+"; fix it on "+p.TargetBranch)
		return nil
	}
	for _, f := range view.Files {
		if len(f.Owners) == 0 || ownerApproved(f.Owners, r.counted, p.Author) {
			continue
		}
		r.NeedsOwners = append(r.NeedsOwners, f)
	}
	if n := len(r.NeedsOwners); n > 0 {
		first := r.NeedsOwners[0]
		r.block("owners", fmt.Sprintf("%s still need approval from an owner (for %s: %s)", plural(n, "changed file", "changed files"), first.Path, ownerNames(first.Owners)))
	}
	return nil
}

func ownerNames(o []string) string {
	out := make([]string, len(o))
	for i, w := range o {
		if g, ok := strings.CutPrefix(w, groupPrefix); ok {
			out[i] = "group " + g
		} else {
			out[i] = "@" + w
		}
	}
	return strings.Join(out, ", ")
}

// ownerApproved: some counted approval is by one of the owners (a person's own
// approval of their own pull request never answers for them).
func ownerApproved(owners []string, approvals []Review, author string) bool {
	for _, rv := range approvals {
		if rv.Author == author {
			continue
		}
		for _, o := range owners {
			if g, ok := strings.CutPrefix(o, groupPrefix); ok {
				if slices.Contains(rv.Groups, g) {
					return true
				}
			} else if o == rv.Author {
				return true
			}
		}
	}
	return false
}

// checkBlockers: the required checks that have not passed.
func (s *Service) checkBlockers(ctx context.Context, repoID, repoPath string, p PullRequest, rule Rule, covered bool, changed []string, r *Readiness) error {
	var required []string
	if covered {
		required = append(required, rule.RequiredChecks...)
		required = append(required, pathRequired(rule.PathChecks, changed)...)
		sort.Strings(required)
		required = dedupe(required)
	}
	checks, _, err := s.Checks(ctx, repoID, repoPath, p, required)
	if err != nil {
		return err
	}
	r.Checks = checks
	for _, name := range required {
		c, ok := findCheck(checks, name)
		switch {
		case !ok || c.Status == CheckRunning || c.Status == CheckPending:
			r.block("check", "`"+name+"` has not passed")
		case c.Status == CheckPassed, c.Status == CheckPathSkipped:
		case c.Status == CheckFailed:
			r.block("check", "`"+name+"` failed")
		case c.Status == CheckCanceled:
			r.block("check", "`"+name+"` was canceled")
		default:
			r.block("check", "`"+name+"` was "+c.Status)
		}
	}
	return nil
}

// pathRequired lists the checks required by path whose paths match a changed file.
func pathRequired(pcs []PathCheck, changed []string) []string {
	var out []string
	for _, pc := range pcs {
		hit := false
		for _, f := range changed {
			for _, pat := range pc.Paths {
				if MatchPath(pat, f) {
					hit = true
				}
			}
		}
		if hit {
			out = append(out, pc.Check)
		}
	}
	return out
}

// conclude says whether the pull request can merge, in one sentence, and
// whether an administrator may bypass what blocks it.
func (r *Readiness) conclude(rule Rule, covered bool, actorRole string, p PullRequest) {
	if len(r.Blockers) == 0 {
		r.CanMerge = true
		return
	}
	parts := make([]string, len(r.Blockers))
	for i, b := range r.Blockers {
		parts[i] = b.Why
	}
	r.Why = strings.Join(parts, "; ")
	// The rules that can be bypassed are the review rules, never a conflict or a draft.
	r.CanBypass = covered && rule.BypassAdmins && actorRole == RoleAdmin && p.State == Open
	for _, b := range r.Blockers {
		switch b.Code {
		case "conflicts", "state", "method", "checking", "draft", "wip", "owners_file":
			r.CanBypass = false
		}
	}
}

// memberBase is a check name without the member of a repeated step:
// ci/test[go=1.26] is ci/test.
func memberBase(check string) (string, bool) {
	if !strings.HasSuffix(check, "]") {
		return check, false
	}
	i := strings.LastIndex(check, "[")
	if i <= 0 {
		return check, false
	}
	return check[:i], true
}
