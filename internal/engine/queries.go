package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// RunSummary is a run as shown in lists.
type RunSummary struct {
	ID              string      `json:"id"`
	Project         string      `json:"project"`
	Number          int         `json:"number"`
	Pipeline        string      `json:"pipeline"`
	Status          RunStatus   `json:"status"`
	CancelRequested bool        `json:"cancel_requested"`
	Event           string      `json:"event"`
	Ref             string      `json:"ref"`
	Branch          string      `json:"branch"`
	Tag             string      `json:"tag"`
	SHA             string      `json:"sha"`
	Actor           string      `json:"actor"`
	Message         string      `json:"message"`
	Author          string      `json:"author"`
	CreatedAt       int64       `json:"created_at"`
	StartedAt       int64       `json:"started_at"`
	FinishedAt      int64       `json:"finished_at"`
	Steps           []StepBrief `json:"steps"`
	Waiting         []string    `json:"waiting"` // approval steps waiting for a decision
	// Draft marks a run of pipeline text that is not in Git (event draft);
	// PipelineHash names that text.
	Draft        bool   `json:"draft"`
	PipelineHash string `json:"pipeline_hash,omitempty"`
	// A re-run: the run it re-runs (ID and number) and the step it started
	// from. Empty otherwise.
	RerunOf       string `json:"rerun_of,omitempty"`
	RerunOfNumber int    `json:"rerun_of_number,omitempty"`
	RerunFrom     string `json:"rerun_from,omitempty"`
	// RerunFroms are all the steps a re-run started from, when it started
	// from several (schema 24); RerunFrom is the first.
	RerunFroms []string `json:"rerun_froms,omitempty"`
	// MinSchema is the oldest schema whose servers understand the whole
	// run; NeedsNewerServer says this server is older, so it leaves the
	// run alone.
	MinSchema        int  `json:"min_schema"`
	NeedsNewerServer bool `json:"needs_newer_server"`
	// PullRequest is the pull request's number, for a run of one (event
	// pull_request); 0 otherwise.
	PullRequest int `json:"pull_request,omitempty"`
	// CancelReason says why the run was asked to cancel when it was not by
	// hand, such as "superseded by run #12", and SupersededBy (with its
	// number) the run that superseded it. Empty unless it was.
	CancelReason       string `json:"cancel_reason,omitempty"`
	SupersededBy       string `json:"superseded_by,omitempty"`
	SupersededByNumber int    `json:"superseded_by_number,omitempty"`
	// Estimate is the time left of a running run in a list; the run's own
	// page carries the full estimate.
	Estimate *RunEstimate `json:"estimate,omitempty"`
	// DeadlineAt is when the run's timeout will cancel it (Unix ms; schema
	// 24); 0 when it has none, or it is paused (an approval waits) or over.
	DeadlineAt int64 `json:"deadline_at,omitempty"`
	// PullRequestInto is the branch the pull request targets (schema 24),
	// for a pull-request run of a pipeline that filters by it.
	PullRequestInto string `json:"pull_request_into,omitempty"`
	// AllowedFailures counts the steps that failed for good and were allowed
	// to (on_failure: continue), and AllowedFailureSteps names them: a run
	// that succeeded with some is not quite as clean as one without
	// (schema 24).
	AllowedFailures     int      `json:"allowed_failures"`
	AllowedFailureSteps []string `json:"allowed_failure_steps,omitempty"`
	// Lane is the run's place in its lane (schema 24); nil when its pipeline
	// has no lane.
	Lane *LaneInfo `json:"lane,omitempty"`
	// Queue says where the run is in its project's queue (schema 37): its
	// class, and for a held run why it waits.
	Queue *QueueInfo `json:"queue,omitempty"`
	// Tests adds up the run's test results (see RunTests); nil when no step
	// has reported any.
	Tests *TestTotals `json:"tests,omitempty"`
	// Findings adds up the run's security findings (see RunFindings); nil
	// when no step has reported any. Artifacts counts the artifacts it
	// keeps.
	Findings  *FindingCounts `json:"findings,omitempty"`
	Artifacts int            `json:"artifacts,omitempty"`
}

// StepBrief is just enough of a step to draw a status strip.
type StepBrief struct {
	ID     string     `json:"id"`
	Status StepStatus `json:"status"`
	// Allowed says a failed step was allowed to fail (on_failure:
	// continue): the run does not count it as a failure.
	Allowed bool `json:"allowed,omitempty"`
	// Reason is why a failed step failed, in a plain sentence.
	Reason string `json:"reason,omitempty"`
	// Needs are the steps this one waits for (the graph's edges).
	Needs []string `json:"needs,omitempty"`
	// SkipKind, for a skipped step, says why: condition, upstream, paths (a
	// path rule), not_needed or superseded.
	SkipKind string `json:"skip_kind,omitempty"`
}

// StepView is a step with its definition and current state.
type StepView struct {
	RunID      string     `json:"run_id"`
	ID         string     `json:"id"`
	Position   int        `json:"position"`
	Status     StepStatus `json:"status"`
	Attempt    int        `json:"attempt"`
	QueuedAt   int64      `json:"queued_at"`
	StartedAt  int64      `json:"started_at"`
	FinishedAt int64      `json:"finished_at"`
	ExitCode   *int       `json:"exit_code"`
	Error      string     `json:"error"`
	// Reason is why a failed step failed, in a plain sentence, and Cause
	// the structured cause behind it (kind, evidence, what happened next).
	Reason    string                        `json:"reason,omitempty"`
	Cause     *StepCause                    `json:"cause,omitempty"`
	SkipKind  string                        `json:"skip_kind"`
	Runner    string                        `json:"runner"`
	Outputs   map[string]string             `json:"outputs"`
	Excerpt   string                        `json:"excerpt"` // a failed attempt's last lines of output
	Container string                        `json:"container,omitempty"`
	Image     string                        `json:"image,omitempty"`
	Run       string                        `json:"run,omitempty"`
	Needs     []string                      `json:"needs,omitempty"`
	When      *pipeline.Condition           `json:"when,omitempty"`
	Secrets   map[string]pipeline.SecretRef `json:"secrets,omitempty"` // references, never values
	Retry     int                           `json:"retry"`
	Timeout   string                        `json:"timeout"`         // as written: 1h, 10m, 90s
	Group     string                        `json:"group,omitempty"` // the uses step it came from, for a template's step
	// Approvals only. Kind and Message come from the definition; the rest
	// is the current wait and decision.
	Kind        string `json:"kind,omitempty"`
	Message     string `json:"message,omitempty"`
	Description string `json:"description,omitempty"`
	Deadline    int64  `json:"deadline"`
	DecidedBy   string `json:"decided_by"`
	DecidedAt   int64  `json:"decided_at"`
	Comment     string `json:"comment"`
	// Approvals are the people who approved so far (all of them once it
	// succeeded), ApprovalsNeeded how many distinct approvals it asks for
	// and ApprovalsNeededFrom why that many (schema 27); Approvers is the
	// step's rule for who may decide, from its definition.
	Approvals           []ApprovalRecord    `json:"approvals,omitempty"`
	ApprovalsNeeded     int                 `json:"approvals_needed,omitempty"`
	ApprovalsNeededFrom string              `json:"approvals_needed_from,omitempty"`
	Approvers           *pipeline.Approvers `json:"approvers,omitempty"`
	// Workspace and RunnerSelector are set for a step with a runner and a
	// workspace of its own (schema 27); WaitingFor says what a queued one
	// waits for when no runner seen lately may take it.
	Workspace      string                   `json:"workspace,omitempty"`
	RunnerSelector *pipeline.RunnerSelector `json:"runner_selector,omitempty"`
	WaitingFor     string                   `json:"waiting_for,omitempty"`
	// WaitingCode is the stable code beside WaitingFor (quota_full,
	// cluster_full, no_runner, runners_busy; see capacity_wait.go).
	WaitingCode string `json:"waiting_code,omitempty"`
	// WaitKind, for a waiting deploy or rollback step, is what it waits
	// for: lock or observe. Empty for an approval.
	WaitKind string `json:"wait_kind,omitempty"`
	// Estimate is what the step usually takes, from earlier runs.
	Estimate *StepEstimate `json:"estimate,omitempty"`
	// Environment and Deployment come from a deploy, rollback or verify
	// step's definition (a verify step watching a deployment has its
	// environment).
	Environment string `json:"environment,omitempty"`
	Deployment  string `json:"deployment,omitempty"`
	// A step a re-run reused: the run (ID and number) where it ran, and the
	// attempt there. Its outcome and outputs are that attempt's.
	ReusedFrom       string `json:"reused_from,omitempty"`
	ReusedFromNumber int    `json:"reused_from_number,omitempty"`
	ReusedAttempt    int    `json:"reused_attempt,omitempty"`
	// Usage is what the step's latest measured attempt used, against what was
	// reserved (schema 54); nil if no attempt reported any.
	Usage *StepUsage `json:"usage,omitempty"`
	// Inputs is what each attempt was given, oldest first; empty for
	// attempts leased by a server older than schema 8.
	Inputs []AttemptInputs `json:"inputs"`
	// Reports are the test result files the step declares, as globs.
	Reports []string `json:"reports,omitempty"`
	// AllowedFailure says the step failed for good and its pipeline
	// tolerates that (on_failure: continue): shown as failed (allowed). The
	// run does not fail because of it (schema 24).
	AllowedFailure bool `json:"allowed_failure,omitempty"`
	// OnFailure is the step's on_failure, from its definition: continue or
	// empty (stop).
	OnFailure string `json:"on_failure,omitempty"`
	// NotBefore, for a step queued to be retried after a wait, is when it
	// may be handed to a runner (Unix ms); FailureCause is why its last
	// failed attempt failed (exit, timeout or infrastructure). RetryWait,
	// RetryBackoff and RetryOn are its retry's other keys, from the
	// definition (schema 24).
	NotBefore    int64    `json:"not_before,omitempty"`
	FailureCause string   `json:"failure_cause,omitempty"`
	RetryWait    string   `json:"retry_wait,omitempty"`
	RetryBackoff float64  `json:"retry_backoff,omitempty"`
	RetryOn      []string `json:"retry_on,omitempty"`
	// PolicyBlocked says the step failed because a policy in enforce mode
	// refused it (schema 16); Error says which policy and rule, and why.
	PolicyBlocked bool `json:"policy_blocked,omitempty"`
	// Tests are the step's test results, from the attempt that counts (see
	// RunTests); nil when it has none.
	Tests *TestTotals `json:"tests,omitempty"`
	// Caches is what the step's caches did in its latest attempt (schema
	// 25): restored, missed, saved or not saved, with sizes and the reason
	// for a refusal.
	Caches []CacheResult `json:"caches,omitempty"`
}

// RunDetail is everything about one run.
type RunDetail struct {
	RunSummary
	// TraceID is the run's trace id and TraceURL a link to it in the installation's trace viewer; both are set
	// by the server only when trace export is on (TraceURL only with a viewer address template).
	TraceID  string             `json:"trace_id,omitempty"`
	TraceURL string             `json:"trace_url,omitempty"`
	Source   Source             `json:"source"`
	Graph    *pipeline.Pipeline `json:"graph"`
	Levels   [][]string         `json:"levels"`
	StepList []StepView         `json:"step_list"`
	// Reruns are the runs that re-run this one, oldest first.
	Reruns []RerunBrief `json:"reruns"`
	// NotUnderstood lists what in the run this server does not understand
	// (see RunState.NotUnderstood); empty when it understands everything.
	NotUnderstood []string `json:"not_understood"`
	// TriggerDetail is what the run was started with: its input values,
	// and the schedule or webhook that started it. Nil if none.
	TriggerDetail *TriggerDetail `json:"trigger_detail,omitempty"`
	// Deployments are the run's deployments, newest first (schema 11).
	Deployments []DeploymentView `json:"deployments"`
	// Actions are what a person may do with the run now, computed once by
	// the server: cancel, resume, rerun.
	Actions []Action `json:"actions"`
	// Rollback says what a rollback run is (nil for any other run);
	// RolledBackBy lists the rollback runs that undid a deployment of this
	// run, which is the link "rolled back later by run #N" (schema 32).
	Rollback     *RollbackRunView `json:"rollback,omitempty"`
	RolledBackBy []RunRef         `json:"rolled_back_by,omitempty"`
	// Resume says whether the run can be resumed, and if not, what to do
	// instead; nil for a run that is not failed or canceled.
	Resume *ResumeAdvice `json:"resume,omitempty"`
	// RunnerHint says why a run that is waiting for a runner may wait a long
	// time: it builds images, and no runner that can has been seen lately.
	RunnerHint string `json:"runner_hint,omitempty"`
	// PoolHint says why no runner may take the run's waiting steps: no
	// pool serves it, or none of the runners seen lately may (schema 21).
	PoolHint string `json:"pool_hint,omitempty"`
	// Policy sums up the run's policy decisions (schema 16); nil if no
	// policy judged it.
	Policy *RunPolicySummary `json:"policy,omitempty"`
	// Restriction says the run is restricted (a pull request from an
	// automation account or a fork), and whether a maintainer approved it.
	Restriction *Restriction `json:"restriction,omitempty"`
	// Estimate is what the run is expected to take, or took.
	Estimate *RunEstimate `json:"estimate,omitempty"`
}

// RunPolicySummary sums up a run's policy decisions.
type RunPolicySummary struct {
	Decisions int `json:"decisions"`
	// Verdict is the worst verdict of the latest decision at each point
	// (block, warn or pass); Waived counts rules passed by waivers there.
	Verdict string `json:"verdict"`
	Waived  int    `json:"waived"`
}

// RerunBrief is a re-run as listed with the run it re-runs.
type RerunBrief struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	From   string `json:"from"`
	// Froms are all the steps, when it started from several.
	Froms  []string  `json:"froms,omitempty"`
	Status RunStatus `json:"status"`
	Actor  string    `json:"actor"`
	At     int64     `json:"created_at"`
}

const runCols = `id, project, number, pipeline, status, cancel_requested, event, ref, branch, tag, sha,
	actor, message, author, created_at, started_at, finished_at, draft, pipeline_hash`

// runCols8 are the columns of runs that schema 8 added.
const runCols8 = `, rerun_of, rerun_of_number, rerun_from, min_schema`

// runCols9 are the columns of runs that schema 9 added.
const runCols9 = `, cancel_reason, superseded_by, superseded_by_number`

// runCols is the columns scanRun reads.
func (e *Engine) runCols() string {
	cols := runCols
	if e.knows(schemaReruns) {
		cols += runCols8
	}
	if e.knows(schemaPullRequests) {
		cols += runCols9
	}
	if e.knows(schemaFlow) {
		cols += runCols24
	}
	return cols
}

// runCols24 are the columns of runs that schema 24 added.
const runCols24 = `, deadline_at, lane_name, lane_key, lane_label, lane_state, lane_other, lane_other_number, pull_request_into, rerun_froms`

func (e *Engine) scanRun(sc interface{ Scan(...any) error }) (*RunSummary, error) {
	r := &RunSummary{}
	var cancel, draft int
	dst := []any{&r.ID, &r.Project, &r.Number, &r.Pipeline, &r.Status, &cancel, &r.Event, &r.Ref, &r.Branch,
		&r.Tag, &r.SHA, &r.Actor, &r.Message, &r.Author, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &draft, &r.PipelineHash}
	if e.knows(schemaReruns) {
		dst = append(dst, &r.RerunOf, &r.RerunOfNumber, &r.RerunFrom, &r.MinSchema)
	}
	if e.knows(schemaPullRequests) {
		dst = append(dst, &r.CancelReason, &r.SupersededBy, &r.SupersededByNumber)
	}
	var froms string
	var lane struct {
		name, key, label, state, other string
		number                         int
	}
	if e.knows(schemaFlow) {
		dst = append(dst, &r.DeadlineAt, &lane.name, &lane.key, &lane.label, &lane.state, &lane.other, &lane.number, &r.PullRequestInto, &froms)
	}
	err := sc.Scan(dst...)
	r.RerunFroms = rerunFroms(froms)
	r.Lane = laneInfo(lane.name, lane.key, lane.label, lane.state, lane.other, lane.number, 0)
	r.PullRequest = pullRequestNumber(r.Event, r.Ref)
	r.CancelRequested = cancel != 0
	r.Draft = draft != 0
	r.NeedsNewerServer = r.MinSchema > e.schema
	return r, err
}

// pullRequestNumber is the number of the pull request a run's ref is the
// head of, for a pull-request run; 0 otherwise.
func pullRequestNumber(event, ref string) int {
	if event != pipeline.EventPullRequest {
		return 0
	}
	n, ok := ParsePullRequestRef(ref)
	if !ok {
		return 0
	}
	return n
}

// ParsePullRequestRef reads refs/pull/<number>/head.
func ParsePullRequestRef(ref string) (int, bool) {
	rest, ok := strings.CutPrefix(ref, "refs/pull/")
	if !ok {
		return 0, false
	}
	num, ok := strings.CutSuffix(rest, "/head")
	if !ok || num == "" || len(num) > 9 || num[0] == '0' {
		return 0, false
	}
	n := 0
	for _, r := range num {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func (e *Engine) runSummary(id string) (*RunSummary, error) {
	r, err := e.scanRun(e.db.QueryRow(`SELECT `+e.runCols()+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: run %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if err := e.attachBriefs([]*RunSummary{r}); err != nil {
		return nil, err
	}
	return r, nil
}

// Summary returns a run as shown in lists.
func (e *Engine) Summary(id string) (*RunSummary, error) { return e.runSummary(id) }

// RunFilter narrows a list of runs.
type RunFilter struct {
	Project string
	Status  string
	// Lane, if set, lists only runs in the lane of this name (schema 24).
	Lane  string
	Limit int
	// OnlyProjects, if not nil, limits the list to runs of these projects;
	// an empty, non-nil list matches nothing.
	OnlyProjects []string
	// Before, if set, lists only runs after this one in the list's order
	// (older, or as old with a smaller ID): the next page after it.
	Before *RunCursor
}

// RunCursor is a place in the list of runs: a run's creation time and ID.
type RunCursor struct {
	CreatedAt int64
	ID        string
}

// ListRuns returns the newest runs first.
func (e *Engine) ListRuns(f RunFilter) ([]*RunSummary, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	var where []string
	var args []any
	if f.Project != "" {
		where = append(where, "project = ?")
		args = append(args, f.Project)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Lane != "" && e.knows(schemaFlow) {
		where = append(where, "lane_name = ?")
		args = append(args, f.Lane)
	}
	if f.OnlyProjects != nil {
		if len(f.OnlyProjects) == 0 {
			return []*RunSummary{}, nil
		}
		marks := make([]string, len(f.OnlyProjects))
		for i, p := range f.OnlyProjects {
			marks[i] = "?"
			args = append(args, p)
		}
		where = append(where, "project IN ("+strings.Join(marks, ",")+")")
	}
	if b := f.Before; b != nil {
		where = append(where, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, b.CreatedAt, b.CreatedAt, b.ID)
	}
	q := `SELECT ` + e.runCols() + ` FROM runs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var out []*RunSummary
	for rows.Next() {
		r, err := e.scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := e.attachBriefs(out); err != nil {
		return nil, err
	}
	e.listEstimates(out)
	if out == nil {
		out = []*RunSummary{}
	}
	return out, nil
}

func (e *Engine) attachBriefs(runs []*RunSummary) error {
	if len(runs) == 0 {
		return nil
	}
	byID := map[string]*RunSummary{}
	args := make([]any, len(runs))
	marks := make([]string, len(runs))
	for i, r := range runs {
		byID[r.ID] = r
		r.Steps = []StepBrief{}
		r.Waiting = []string{}
		args[i] = r.ID
		marks[i] = "?"
	}
	waitKind := `''`
	if e.knows(schemaDeploy) {
		waitKind = `wait_kind`
	}
	allowed := `0`
	if e.knows(schemaFlow) {
		allowed = `allowed`
	}
	rows, err := e.db.Query(`SELECT run_id, step_id, status, `+waitKind+`, `+allowed+`, error, skip_kind FROM steps WHERE run_id IN (`+strings.Join(marks, ",")+`)
		ORDER BY run_id, position`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var runID, kind string
		var b StepBrief
		var allowed int
		var reason string
		if err := rows.Scan(&runID, &b.ID, &b.Status, &kind, &allowed, &reason, &b.SkipKind); err != nil {
			return err
		}
		if b.Status == StepFailed {
			b.Reason = clipText(reason, 300)
		}
		b.Allowed = b.Status == StepFailed && allowed != 0
		r := byID[runID]
		r.Steps = append(r.Steps, b)
		if b.Allowed {
			r.AllowedFailures++
			r.AllowedFailureSteps = append(r.AllowedFailureSteps, b.ID)
		}
		if b.Status == StepWaitingS && kind == "" {
			r.Waiting = append(r.Waiting, b.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	e.attachNeeds(runs)
	e.attachQueue(runs)
	if err := e.attachLanePositions(runs); err != nil {
		return err
	}
	if err := e.attachTests(runs); err != nil {
		return err
	}
	return e.attachSchema12(runs)
}

const stepCols = `run_id, step_id, position, status, attempt, queued_at, started_at, finished_at, exit_code,
	error, skip_kind, runner, outputs, deadline, decided_by, decided_at, comment, excerpt`

// stepCols8 are the columns of steps that schema 8 added.
const stepCols8 = `, reused_from, reused_from_number, reused_attempt`

// stepCols is the columns scanStep reads.
func (e *Engine) stepCols() string {
	cols := stepCols
	if e.knows(schemaReruns) {
		cols += stepCols8
	}
	if e.knows(schemaDeploy) {
		cols += `, wait_kind`
	}
	if e.knows(schemaFlow) {
		cols += `, allowed, not_before, fail_cause`
	}
	if e.knows(schemaParity2) {
		cols += `, approvals`
	}
	return cols
}

func (e *Engine) scanStep(sc interface{ Scan(...any) error }) (*StepView, error) {
	v := &StepView{Inputs: []AttemptInputs{}}
	var exit sql.NullInt64
	var outputs string
	dst := []any{&v.RunID, &v.ID, &v.Position, &v.Status, &v.Attempt, &v.QueuedAt, &v.StartedAt, &v.FinishedAt,
		&exit, &v.Error, &v.SkipKind, &v.Runner, &outputs, &v.Deadline, &v.DecidedBy, &v.DecidedAt, &v.Comment, &v.Excerpt}
	if e.knows(schemaReruns) {
		dst = append(dst, &v.ReusedFrom, &v.ReusedFromNumber, &v.ReusedAttempt)
	}
	if e.knows(schemaDeploy) {
		dst = append(dst, &v.WaitKind)
	}
	var allowed int
	if e.knows(schemaFlow) {
		dst = append(dst, &allowed, &v.NotBefore, &v.FailureCause)
	}
	var approvals string
	if e.knows(schemaParity2) {
		dst = append(dst, &approvals)
	}
	if err := sc.Scan(dst...); err != nil {
		return nil, err
	}
	v.setApprovals(approvals)
	if exit.Valid {
		x := int(exit.Int64)
		v.ExitCode = &x
	}
	v.Outputs = map[string]string{}
	_ = json.Unmarshal([]byte(outputs), &v.Outputs)
	v.AllowedFailure = v.Status == StepFailed && allowed != 0
	v.PolicyBlocked = v.Status == StepFailed && (strings.HasPrefix(v.Error, policyBlockedPrefix) || strings.HasPrefix(v.Error, runRefusedPrefix))
	return v, nil
}

// stepView is a step's state, with what its attempts were given, as sent
// in live updates (which leave out the definition).
func (e *Engine) stepView(runID, stepID string) (*StepView, error) {
	v, err := e.scanStep(e.db.QueryRow(`SELECT `+e.stepCols()+` FROM steps WHERE run_id = ? AND step_id = ?`, runID, stepID))
	if err != nil {
		return nil, err
	}
	inputs, err := e.stepInputs(runID, stepID)
	if err != nil {
		return nil, err
	}
	if in := inputs[stepID]; in != nil {
		v.Inputs = in
	}
	if us, err := e.stepUsages(runID); err == nil {
		v.Usage = us[stepID]
	}
	steps := []StepView{*v}
	if err := e.stepTests(runID, steps); err != nil {
		return nil, err
	}
	v.Tests = steps[0].Tests
	if v.Status == StepSucceeded || v.Status == StepFailed || v.Status == StepCanceledS {
		if v.Caches, err = e.stepCaches(runID, stepID); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// stepCaches is what a finished step's latest attempt's caches did, from the
// event that ended it.
func (e *Engine) stepCaches(runID, stepID string) ([]CacheResult, error) {
	if !e.knows(schemaCache) {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT data FROM events WHERE run_id = ? AND type IN (?, ?) ORDER BY seq DESC`, runID, StepFinished, StepCanceled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p Payload
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			continue
		}
		if p.Step == stepID {
			return p.Caches, nil
		}
	}
	return nil, rows.Err()
}

// GetRun returns a run with its graph and every step.
func (e *Engine) GetRun(id string) (*RunDetail, error) {
	sum, err := e.runSummary(id)
	if err != nil {
		return nil, err
	}
	// The events say what the run is and whether this server understands
	// all of it; the tables say how it is doing.
	evs, _, err := loadEvents(e.db, id)
	if err != nil {
		return nil, err
	}
	st := e.fold(evs)
	if st == nil || st.Pipeline == nil {
		return nil, fmt.Errorf("%w: run %s", ErrNotFound, id)
	}
	d := &RunDetail{RunSummary: *sum, Graph: st.Pipeline, Levels: st.Pipeline.Levels(), Source: st.Source,
		Reruns: []RerunBrief{}, NotUnderstood: st.NotUnderstood}
	if d.NotUnderstood == nil {
		d.NotUnderstood = []string{}
	}
	d.NeedsNewerServer = d.NeedsNewerServer || len(st.NotUnderstood) > 0
	d.TriggerDetail = triggerDetail(st.Trigger)
	inputs, err := e.stepInputs(id, "")
	if err != nil {
		return nil, err
	}
	usages, err := e.stepUsages(id)
	if err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT `+e.stepCols()+` FROM steps WHERE run_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := e.scanStep(rows)
		if err != nil {
			return nil, err
		}
		if def := st.Pipeline.Step(v.ID); def != nil {
			v.Image, v.Run, v.Needs, v.When, v.Retry, v.Timeout = def.Image, def.Run, def.Needs, def.When, def.Retry, def.Timeout.Short()
			v.OnFailure = def.OnFailure
			if def.RetryWait > 0 {
				v.RetryWait = def.RetryWait.Short()
			}
			v.RetryBackoff, v.RetryOn = def.RetryBackoff, def.RetryOn
			v.Secrets = def.Secrets
			v.Container = st.Pipeline.ContainerFor(def)
			v.Kind, v.Message, v.Description = def.Kind, def.Message, def.Description
			v.Group, v.Approvers = def.Group, def.Approvers
			e.decorateOwnRunner(v, st, def)
			e.decorateWait(v, st, def)
			v.Environment, v.Deployment = def.Environment, def.Deployment
			if dd := st.Pipeline.Step(def.Deployment); def.IsRollback() && dd != nil {
				v.Environment = dd.Environment
			}
			if def.IsVerify() {
				v.Environment, _ = st.Pipeline.VerifyEnvironment(def)
			}
			if def.Reports != nil {
				v.Reports = def.Reports.Tests
			}
		}
		if in := inputs[v.ID]; in != nil {
			v.Inputs = in
		}
		v.Usage = usages[v.ID]
		st.attachCause(v)
		if s := st.Steps[v.ID]; s != nil && s.Evidence != nil && len(s.Evidence.Caches) > 0 {
			v.Caches = s.Evidence.Caches
		}
		d.StepList = append(d.StepList, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if e.knows(schemaReruns) {
		if d.Reruns, err = e.reruns(id); err != nil {
			return nil, err
		}
	}
	if err := e.stepTests(id, d.StepList); err != nil {
		return nil, err
	}
	if d.Deployments, err = e.RunDeployments(id); err != nil {
		return nil, err
	}
	if d.RunnerHint, err = e.imageRunnerHint(st, d.StepList); err != nil {
		return nil, err
	}
	if d.PoolHint, err = e.poolOrVerifyHint(st, d.StepList); err != nil {
		return nil, err
	}
	d.Policy = policySummary(st)
	d.Restriction = st.restrictionView()
	d.Resume = st.resumeAdvice()
	d.Actions = st.runActions()
	d.Rollback = st.rollbackView()
	e.attachEstimates(d)
	if d.RolledBackBy, err = e.rolledBackBy(id); err != nil {
		return nil, err
	}
	return d, nil
}

// reruns lists the runs that re-run a run, oldest first.
func (e *Engine) reruns(runID string) ([]RerunBrief, error) {
	cols := `rerun_from`
	if e.knows(schemaFlow) {
		cols += `, rerun_froms`
	} else {
		cols += `, ''`
	}
	rows, err := e.db.Query(`SELECT id, number, `+cols+`, status, actor, created_at FROM runs WHERE rerun_of = ?
		ORDER BY created_at, number`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RerunBrief{}
	for rows.Next() {
		var b RerunBrief
		var froms string
		if err := rows.Scan(&b.ID, &b.Number, &b.From, &froms, &b.Status, &b.Actor, &b.At); err != nil {
			return nil, err
		}
		b.Froms = rerunFroms(froms)
		out = append(out, b)
	}
	return out, rows.Err()
}

// Logs returns log lines for one step attempt after the given sequence number.
// Attempt 0 means the latest attempt.
func (e *Engine) Logs(runID, stepID string, attempt, after, limit int) ([]LogLine, int, error) {
	return e.QueryLogs(runID, stepID, LogQuery{Attempt: attempt, After: after, Limit: limit})
}

// LogQuery says which of a step attempt's log lines to read.
type LogQuery struct {
	Attempt int // 0: the latest attempt
	After   int // only lines after this sequence number
	Limit   int // at most this many lines: default 5000, at most 10000
	// Tail, if set, reads the last Tail lines (after After) instead of the
	// first ones; the lines are still returned in order.
	Tail bool
	// Search, if set, reads only lines that contain it, ignoring case.
	Search string
	// Stream, if set, reads only that stream: stdout, stderr, system or
	// service (the services beside the steps; schema 27); Service, with
	// stream service, only that service's lines, which start with its name
	// and a colon. Without Stream, service lines are left out.
	Stream, Service string
}

// QueryLogs returns log lines for one step attempt, and the attempt they
// are of.
func (e *Engine) QueryLogs(runID, stepID string, q LogQuery) ([]LogLine, int, error) {
	attempt, after, limit := q.Attempt, q.After, q.Limit
	if limit <= 0 || limit > 10000 {
		limit = 5000
	}
	if attempt == 0 {
		if err := e.db.QueryRow(`SELECT attempt FROM steps WHERE run_id = ? AND step_id = ?`, runID, stepID).Scan(&attempt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, 0, fmt.Errorf("%w: step %s", ErrNotFound, stepID)
			}
			return nil, 0, err
		}
	}
	where := `run_id = ? AND step_id = ? AND attempt = ? AND seq > ?`
	args := []any{runID, stepID, attempt, after}
	if s := q.Search; s != "" {
		where += ` AND LOWER(text) LIKE ? ESCAPE '\'`
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(s))+"%")
	}
	where, args = q.streamFilter(where, args)
	order := "seq"
	if q.Tail {
		order = "seq DESC"
	}
	rows, err := e.db.Query(`SELECT seq, at, stream, text FROM logs WHERE `+where+` ORDER BY `+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []LogLine{}
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.Seq, &l.At, &l.Stream, &l.Text); err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	if q.Tail {
		slices.Reverse(out)
	}
	return out, attempt, rows.Err()
}

// RunInfo is the little a runner needs to know about a run to tidy up after it.
type RunInfo struct {
	ID         string    `json:"id"`
	Status     RunStatus `json:"status"`
	FinishedAt int64     `json:"finished_at"`
}

// RunInfos looks up runs by ID. Unknown IDs are left out.
func (e *Engine) RunInfos(ids []string) ([]RunInfo, error) {
	out := []RunInfo{}
	for len(ids) > 0 {
		n := min(len(ids), 200)
		batch := ids[:n]
		ids = ids[n:]
		args := make([]any, len(batch))
		marks := make([]string, len(batch))
		for i, id := range batch {
			args[i], marks[i] = id, "?"
		}
		rows, err := e.db.Query(`SELECT id, status, finished_at FROM runs WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r RunInfo
			if err := rows.Scan(&r.ID, &r.Status, &r.FinishedAt); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, r)
		}
		rows.Close()
	}
	return out, nil
}

// Events returns a run's full event log, oldest first.
func (e *Engine) Events(runID string) ([]Event, error) {
	evs, _, err := loadEvents(e.db, runID)
	if err == nil && len(evs) == 0 {
		return nil, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	return evs, err
}

// State rebuilds a run's state purely from its events. Tests use it to
// check that the stored tables agree with the log.
func (e *Engine) State(runID string) (*RunState, error) {
	evs, err := e.Events(runID)
	if err != nil {
		return nil, err
	}
	return Fold(evs), nil
}

// poolOrVerifyHint is the hint about build pools for a run's steps, or, when there is none, the one about the
// pools its verification steps need.
func (e *Engine) poolOrVerifyHint(st *RunState, steps []StepView) (string, error) {
	hint, err := e.poolHint(st, steps)
	if err != nil || hint != "" {
		return hint, err
	}
	return e.verifyHint(st)
}
