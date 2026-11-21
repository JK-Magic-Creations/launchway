package pulls

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
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
	return nil
}

const ruleCols = `pattern, required_approvals, approval_role, required_checks, require_up_to_date, require_resolved, no_self_approval, require_signed, bypass_admins`

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
		if err := rows.Scan(&r.Pattern, &r.RequiredApprovals, &r.ApprovalRole, &checks, &up, &res, &self, &sig, &byp); err != nil {
			return nil, err
		}
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
		no_self_approval = ?, require_signed = 0, bypass_admins = ? WHERE repo_id = ? AND pattern = ?`,
		r.RequiredApprovals, r.ApprovalRole, string(checks), b2i(r.RequireUpToDate), b2i(r.RequireResolved), b2i(r.NoSelfApproval), b2i(r.BypassAdmins), repoID, r.Pattern)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.db.Exec(ctx, `INSERT INTO branch_rules(id, repo_id, pattern, push_role, created_at, created_by, required_approvals, approval_role, required_checks,
		require_up_to_date, require_resolved, no_self_approval, bypass_admins) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ids.New("rul"), repoID, r.Pattern, "admin", ms(s.now()), by, r.RequiredApprovals, r.ApprovalRole, string(checks),
		b2i(r.RequireUpToDate), b2i(r.RequireResolved), b2i(r.NoSelfApproval), b2i(r.BypassAdmins))
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
}

// StepInfo is one step of a run.
type StepInfo struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Allowed bool   `json:"allowed_to_fail,omitempty"`
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
	CheckMissing  = "missing"
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
	latest := map[string]RunInfo{}
	for _, r := range runs {
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
			checks = append(checks, Check{Name: name, Status: CheckMissing, Required: true})
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
	case CheckSkipped:
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
	block := func(code, why string) { r.Blockers = append(r.Blockers, Blocker{Code: code, Why: why}) }
	if p.State != Open {
		block("state", "it is already "+string(p.State))
		r.Why = "this pull request is already " + string(p.State)
		return r, nil
	}
	if p.Draft {
		block("draft", "it is a draft; mark it ready for review")
	}
	switch p.Mergeability.State {
	case MergeConflicts:
		block("conflicts", "it has conflicts in "+strings.Join(p.Mergeability.Files, ", ")+"; update the branch and resolve them")
	case MergeChecking, MergeUnknown:
		why := "it is still being checked for conflicts"
		if p.Mergeability.State == MergeUnknown && p.Mergeability.Reason != "" {
			why = "it could not be checked for conflicts (" + p.Mergeability.Reason + ")"
		}
		block("checking", why)
	}
	reviews, err := s.Store.Reviews(ctx, p.ID)
	if err != nil {
		return r, err
	}
	comments, err := s.Store.Comments(ctx, p.ID)
	if err != nil {
		return r, err
	}
	seen := map[string]bool{}
	for _, c := range comments {
		if c.Path != "" && c.ThreadID == c.ID && !c.Resolved && !c.Deleted {
			seen[c.ThreadID] = true
		}
	}
	r.OpenThreads = len(seen)
	approvers := map[string]bool{}
	latest := map[string]Review{}
	for _, rv := range reviews { // oldest first: the last non-comment review of each person stands
		if rv.State != "submitted" || rv.Verdict == VerdictComment {
			continue
		}
		latest[rv.Author] = rv
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
		}
	}
	sort.Strings(r.ChangesRequested)
	r.Approvals = len(approvers)
	if len(r.ChangesRequested) > 0 {
		block("changes_requested", strings.Join(r.ChangesRequested, ", ")+" asked for changes")
	}
	if covered {
		if need := rule.RequiredApprovals - r.Approvals; need > 0 {
			why := "needs " + plural(need, "more approval", "more approvals")
			if rule.ApprovalRole == RoleAdmin {
				why += " from an administrator"
			}
			block("approvals", why)
		}
		if rule.RequireUpToDate && p.Mergeability.Behind > 0 {
			block("behind", fmt.Sprintf("it is %s behind %s and must be up to date; update the branch", plural(p.Mergeability.Behind, "commit", "commits"), p.TargetBranch))
		}
		if rule.RequireResolved && r.OpenThreads > 0 {
			block("threads", plural(r.OpenThreads, "conversation is", "conversations are")+" not resolved")
		}
	}
	var required []string
	if covered {
		required = rule.RequiredChecks
	}
	checks, _, err := s.Checks(ctx, repoID, repoPath, p, required)
	if err != nil {
		return r, err
	}
	r.Checks = checks
	for _, name := range required {
		c, ok := findCheck(checks, name)
		switch {
		case !ok:
			block("check", "`"+name+"` has not passed")
		case c.Status == CheckPassed:
		case c.Status == CheckFailed:
			block("check", "`"+name+"` failed")
		case c.Status == CheckRunning || c.Status == CheckPending:
			block("check", "`"+name+"` has not passed")
		case c.Status == CheckCanceled:
			block("check", "`"+name+"` was canceled")
		default:
			block("check", "`"+name+"` was "+c.Status)
		}
	}
	if len(r.Methods) == 0 {
		block("method", "the repository allows no way of merging")
	}
	if len(r.Blockers) == 0 {
		r.CanMerge = true
		return r, nil
	}
	parts := make([]string, len(r.Blockers))
	for i, b := range r.Blockers {
		parts[i] = b.Why
	}
	r.Why = strings.Join(parts, "; ")
	// The rules that can be bypassed are the review rules, never a conflict or a draft.
	r.CanBypass = covered && rule.BypassAdmins && actorRole == RoleAdmin && p.State == Open
	for _, b := range r.Blockers {
		if b.Code == "conflicts" || b.Code == "state" || b.Code == "method" || b.Code == "checking" || b.Code == "draft" {
			r.CanBypass = false
		}
	}
	return r, nil
}
