package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// What needs a person, as one rule shared by the Home page, the Sorties list,
// the command line and the MCP tools. The rule is a pure function of the
// recent runs (AttentionRun), the deployments and the decisions that wait, so
// that it can be tested as a table; AttentionFacts reads those from the
// database, bounded so that it stays fast with hundreds of thousands of runs.
//
// The rule, in short:
//   - Sorties of one pipeline are grouped by where they came from: the
//     pull request, or else the branch (a tag or other ref groups by its ref).
//   - In each group the newest Sortie that was not canceled decides.
//   - If it is queued or running, an older failure in the group is SUPERSEDED:
//     it is "being retried" and does not need anyone.
//   - If it succeeded, the group is fine.
//   - If it failed, the failure needs someone when the group matters: the
//     default (or a release) branch, the caller's own pull request or Sortie,
//     or a pipeline the caller follows.
//   - A deployment needs someone when the latest one that ended for its
//     service in its environment failed (a return, or a later success, clears it).
//   - A decision waits for someone: it is listed.
//   - A Sortie queued too long, or running far longer than that pipeline's
//     Sorties usually take, is stuck.

// AttentionRun is the part of a run the rule needs.
type AttentionRun struct {
	ID         string    `json:"id"`
	Project    string    `json:"project"`
	Number     int       `json:"number"`
	Status     RunStatus `json:"status"`
	Event      string    `json:"event"`
	Ref        string    `json:"ref"`
	Branch     string    `json:"branch"`
	Actor      string    `json:"actor"`
	Message    string    `json:"message"`
	CreatedAt  int64     `json:"created_at"`
	StartedAt  int64     `json:"started_at"`
	FinishedAt int64     `json:"finished_at"`
	RerunOf    string    `json:"rerun_of,omitempty"`
}

// PullRequest is the pull request number of a pull-request run; 0 otherwise.
func (r AttentionRun) PullRequest() int { return pullRequestNumber(r.Event, r.Ref) }

// group is where a run came from: the unit the rule compares runs within.
func (r AttentionRun) group() string {
	if pr := r.PullRequest(); pr > 0 {
		return fmt.Sprintf("%s\x00pr\x00%d", r.Project, pr)
	}
	if r.Branch != "" {
		return r.Project + "\x00branch\x00" + r.Branch
	}
	return r.Project + "\x00ref\x00" + r.Ref
}

// AttentionDeployment is the part of a deployment the rule needs.
type AttentionDeployment struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	RunNumber   int    `json:"run_number"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	Version     string `json:"version"`
	Verdict     string `json:"verdict"`
	ClosedAt    int64  `json:"closed_at"`
}

// AttentionInput is everything the rule looks at.
type AttentionInput struct {
	Runs        []AttentionRun
	Deployments []AttentionDeployment
	Approvals   []Approval
	// Now is the time the rule is applied at (Unix ms).
	Now int64
	// User is who asks; their own pull requests and Sorties matter.
	User string
	// DefaultBranch gives a project's default branch; nil or "" knows none.
	DefaultBranch func(project string) string
	// Follows are the projects the caller follows (favorites); their
	// failures matter on any branch.
	Follows map[string]bool
	// Limits; zero values give the defaults.
	QueuedStuck time.Duration // queued this long: stuck (default 15 minutes)
	RunningMin  time.Duration // never stuck before this long (default 30 minutes)
	RunningMult float64       // stuck beyond this multiple of the usual (default 3)
}

// Attention kinds.
const (
	AttnFailed   = "failed"
	AttnDeploy   = "deployment"
	AttnDecision = "decision"
	AttnStuck    = "stuck"
)

// AttentionItem is one thing that needs a person.
type AttentionItem struct {
	// Key is stable for the thing and changes when a newer one replaces it,
	// so a dismissal applies to exactly what was dismissed.
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Tone        string `json:"tone"` // bad or warn
	Project     string `json:"project"`
	Branch      string `json:"branch,omitempty"`
	PullRequest int    `json:"pull_request,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	RunNumber   int    `json:"run_number,omitempty"`
	Step        string `json:"step,omitempty"`
	// Title says what happened; Why says why it is listed; Do says what to do.
	Title string `json:"title"`
	Why   string `json:"why"`
	Do    string `json:"do"`
	Since int64  `json:"since"`
	Href  string `json:"href"`
	rank  int
}

// Retry is a failure that does not need anyone because a newer Sortie of the
// same pipeline and branch (or pull request) is on its way.
type Retry struct {
	Project     string `json:"project"`
	Branch      string `json:"branch,omitempty"`
	PullRequest int    `json:"pull_request,omitempty"`
	FailedID    string `json:"failed_id"`
	FailedNo    int    `json:"failed_number"`
	NowID       string `json:"now_id"`
	NowNo       int    `json:"now_number"`
	NowStatus   string `json:"now_status"`
}

// AttentionResult is the answer.
type AttentionResult struct {
	Needs []AttentionItem `json:"needs"`
	// Retrying: failures superseded by a newer Sortie in progress, by the
	// run that is in progress.
	Retrying []Retry `json:"retrying"`
	// Superseded counts every failure that was set aside for any reason (a
	// newer success, a Sortie in progress, a branch that does not matter).
	Superseded int `json:"superseded"`
}

func isProgress(s RunStatus) bool { return s == RunQueued || s == RunRunning }

// ComputeAttention applies the rule. It does not touch the database.
func ComputeAttention(in AttentionInput) AttentionResult {
	queuedStuck := in.QueuedStuck
	if queuedStuck <= 0 {
		queuedStuck = 15 * time.Minute
	}
	runningMin := in.RunningMin
	if runningMin <= 0 {
		runningMin = 30 * time.Minute
	}
	mult := in.RunningMult
	if mult <= 0 {
		mult = 3
	}
	waiting := map[string]bool{}
	for _, a := range in.Approvals {
		waiting[a.RunID] = true
	}
	groups, order, typical := groupRuns(in.Runs)
	var res AttentionResult
	res.Needs = []AttentionItem{}
	res.Retrying = []Retry{}
	for _, g := range order {
		list := groups[g]
		var head *AttentionRun
		hi := 0
		for i := range list {
			if list[i].Status != RunCanceled {
				head, hi = &list[i], i
				break
			}
		}
		if head == nil {
			continue
		}
		switch {
		case head.Status == RunFailed:
			if it, ok := failedItem(in, *head, list[hi+1:]); ok {
				res.Needs = append(res.Needs, it)
			} else {
				res.Superseded++
			}
		case isProgress(head.Status):
			for _, o := range list[hi+1:] {
				if o.Status == RunCanceled {
					continue
				}
				if o.Status == RunFailed {
					res.Retrying = append(res.Retrying, Retry{Project: head.Project, Branch: head.Branch, PullRequest: head.PullRequest(),
						FailedID: o.ID, FailedNo: o.Number, NowID: head.ID, NowNo: head.Number, NowStatus: string(head.Status)})
					res.Superseded++
				}
				break
			}
			if !waiting[head.ID] {
				if it, ok := stuckItem(in, *head, queuedStuck, runningMin, mult, typical[head.Project]); ok {
					res.Needs = append(res.Needs, it)
				}
			}
		default: // succeeded: a failure behind it is cleared
			for _, o := range list[hi+1:] {
				if o.Status == RunCanceled {
					continue
				}
				if o.Status == RunFailed {
					res.Superseded++
				}
				break
			}
		}
	}
	res.Needs = append(res.Needs, deploymentNeeds(in.Deployments)...)
	res.Needs = append(res.Needs, decisionNeeds(in.Approvals)...)
	sort.SliceStable(res.Needs, func(i, j int) bool {
		if res.Needs[i].rank != res.Needs[j].rank {
			return res.Needs[i].rank < res.Needs[j].rank
		}
		return res.Needs[i].Since > res.Needs[j].Since
	})
	sort.SliceStable(res.Retrying, func(i, j int) bool { return res.Retrying[i].NowNo > res.Retrying[j].NowNo })
	return res
}

func nameOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// matters says why a failure in a group is worth a person's time, or "" if it
// is not.
func matters(in AttentionInput, r AttentionRun) string {
	pr := r.PullRequest()
	switch {
	case pr > 0 && in.User != "" && r.Actor == in.User:
		return fmt.Sprintf("it is your pull request #%d", pr)
	case pr == 0 && in.User != "" && r.Actor == in.User && r.Event != "push" && r.Event != "":
		return "you started it"
	case in.Follows[r.Project]:
		return "you follow " + r.Project
	}
	if pr == 0 && r.Branch != "" {
		def := ""
		if in.DefaultBranch != nil {
			def = in.DefaultBranch(r.Project)
		}
		if r.Branch == def {
			return "it is the latest result on " + r.Branch + ", the default branch"
		}
		if strings.HasPrefix(r.Branch, "release/") || strings.HasPrefix(r.Branch, "hotfix/") {
			return "it is the latest result on " + r.Branch + ", a protected branch"
		}
	}
	return ""
}

func failedItem(in AttentionInput, r AttentionRun, older []AttentionRun) (AttentionItem, bool) {
	why := matters(in, r)
	if why == "" {
		return AttentionItem{}, false
	}
	row := 1
	for _, o := range older {
		if o.Status == RunCanceled {
			continue
		}
		if o.Status != RunFailed {
			break
		}
		row++
	}
	where := r.Branch
	if pr := r.PullRequest(); pr > 0 {
		where = fmt.Sprintf("pull request #%d", pr)
	}
	if where == "" {
		where = r.Ref
	}
	it := AttentionItem{Key: "failed:" + r.ID, Kind: AttnFailed, Tone: "bad", Project: r.Project, Branch: r.Branch, PullRequest: r.PullRequest(),
		RunID: r.ID, RunNumber: r.Number, Since: nz(r.FinishedAt, r.CreatedAt), Href: "/runs/" + r.ID, rank: 2,
		Title: fmt.Sprintf("Sortie #%d of %s failed on %s", r.Number, r.Project, where), Do: "See what went wrong, then fix it or run it again."}
	it.Why = strings.ToUpper(why[:1]) + why[1:] + "; nothing newer has run."
	if row > 1 {
		it.Title = fmt.Sprintf("%s has failed %d times in a row on %s", r.Project, row, where)
		it.Why = fmt.Sprintf("The last %d Sorties failed and nothing newer is running; %s.", row, why)
	}
	if strings.Contains(why, "default branch") || strings.Contains(why, "protected") {
		it.rank = 2
	} else {
		it.rank = 4
	}
	return it, true
}

func nz(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func stuckItem(in AttentionInput, r AttentionRun, queued, floor time.Duration, mult float64, durations []int64) (AttentionItem, bool) {
	if r.Status == RunQueued {
		age := time.Duration(in.Now-r.CreatedAt) * time.Millisecond
		if age < queued {
			return AttentionItem{}, false
		}
		return AttentionItem{Key: "stuck:" + r.ID, Kind: AttnStuck, Tone: "warn", Project: r.Project, Branch: r.Branch, PullRequest: r.PullRequest(),
			RunID: r.ID, RunNumber: r.Number, Since: r.CreatedAt, Href: "/runs/" + r.ID, rank: 5,
			Title: fmt.Sprintf("Sortie #%d of %s has been queued for %s", r.Number, r.Project, human(age)),
			Why:   "A Sortie normally starts within minutes. No runner has taken it.", Do: "Check that a runner with the right labels is online."}, true
	}
	if r.StartedAt == 0 {
		return AttentionItem{}, false
	}
	age := time.Duration(in.Now-r.StartedAt) * time.Millisecond
	limit := floor
	usual := time.Duration(0)
	if len(durations) >= 5 {
		d := append([]int64(nil), durations...)
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		usual = time.Duration(d[len(d)/2]) * time.Millisecond
		if m := time.Duration(float64(usual) * mult); m > limit {
			limit = m
		}
	}
	if age < limit {
		return AttentionItem{}, false
	}
	why := fmt.Sprintf("It has run for %s; the limit before it is called stuck is %s.", human(age), human(limit))
	if usual > 0 {
		why = fmt.Sprintf("It has run for %s; Sorties of %s usually take %s.", human(age), r.Project, human(usual))
	}
	return AttentionItem{Key: "stuck:" + r.ID, Kind: AttnStuck, Tone: "warn", Project: r.Project, Branch: r.Branch, PullRequest: r.PullRequest(),
		RunID: r.ID, RunNumber: r.Number, Since: r.StartedAt, Href: "/runs/" + r.ID, rank: 5,
		Title: fmt.Sprintf("Sortie #%d of %s is taking far longer than usual", r.Number, r.Project), Why: why, Do: "Open it to see the step that is slow; cancel it if it is hung."}, true
}

func human(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%d hours", h)
		}
		return fmt.Sprintf("%d hours %d minutes", h, m)
	}
}

// deploymentNeeds: the latest ended deployment of each service in each environment, when it failed.
func deploymentNeeds(deployments []AttentionDeployment) []AttentionItem {
	var out []AttentionItem
	latest := map[string]AttentionDeployment{}
	for _, d := range deployments {
		k := d.Project + "\x00" + d.Environment + "\x00" + d.Service
		if o, ok := latest[k]; !ok || d.ClosedAt > o.ClosedAt {
			latest[k] = d
		}
	}
	for _, d := range latest {
		if d.Verdict != VerdictFailed && d.Verdict != VerdictRollbackFailed {
			continue
		}
		it := AttentionItem{Key: "deployment:" + d.ID, Kind: AttnDeploy, Tone: "bad", Project: d.Project, RunID: d.RunID, RunNumber: d.RunNumber,
			Since: d.ClosedAt, Href: "/runs/" + d.RunID, rank: 1}
		if d.Verdict == VerdictRollbackFailed {
			it.rank = 0
			it.Title = fmt.Sprintf("The rollback of %s in %s failed", nameOr(d.Service, d.Project), d.Environment)
			it.Why = "The deployment failed and the rollback that should have put the last good version back failed too. Nothing since has deployed."
			it.Do = "Look at the environment: it may be running a broken version."
		} else {
			it.Title = fmt.Sprintf("The deployment of %s to %s failed", nameOr(d.Service, d.Project), d.Environment)
			it.Why = "It is the latest deployment of this service there and no later one has succeeded or returned it to the last good version."
			it.Do = "See what failed, then run it again or return to the last good version."
		}
		out = append(out, it)
	}
	return out
}

// decisionNeeds: a decision that waits is listed.
func decisionNeeds(approvals []Approval) []AttentionItem {
	var out []AttentionItem
	for _, a := range approvals {
		what := a.Message
		if what == "" {
			what = a.StepID
		}
		out = append(out, AttentionItem{Key: "decision:" + a.RunID + "/" + a.StepID, Kind: AttnDecision, Tone: "warn", Project: a.Project,
			Branch: a.Branch, PullRequest: a.PullRequest, RunID: a.RunID, RunNumber: a.RunNumber, Step: a.StepID,
			Title: "Waiting for a decision: " + what, Why: fmt.Sprintf("Sortie #%d of %s is paused until someone says Go or No-go.", a.RunNumber, a.Project),
			Do: "Review it and decide.", Since: a.WaitingSince, Href: "/runs/" + a.RunID + "?step=" + a.StepID, rank: 3})
	}
	return out
}

// groupRuns puts the runs into groups (a pipeline's pull request or branch), each newest first, and collects how
// long each pipeline's successful runs took.
func groupRuns(runsIn []AttentionRun) (map[string][]AttentionRun, []string, map[string][]int64) {
	// Newest first, so a group's list reads from the latest back.
	runs := append([]AttentionRun(nil), runsIn...)
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].CreatedAt != runs[j].CreatedAt {
			return runs[i].CreatedAt > runs[j].CreatedAt
		}
		if runs[i].Project == runs[j].Project {
			return runs[i].Number > runs[j].Number
		}
		return runs[i].ID > runs[j].ID
	})
	groups := map[string][]AttentionRun{}
	var order []string
	typical := map[string][]int64{} // project → durations of recent successes
	for _, r := range runs {
		if r.Event == "draft" {
			continue // pipeline text that is not in Git is nobody's to fix
		}
		g := r.group()
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], r)
		if r.Status == RunSucceeded && r.FinishedAt > r.StartedAt && r.StartedAt > 0 {
			typical[r.Project] = append(typical[r.Project], r.FinishedAt-r.StartedAt)
		}
	}
	return groups, order, typical
}
