package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/accept"
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
}

// StepView is a step with its definition and current state.
type StepView struct {
	RunID      string                        `json:"run_id"`
	ID         string                        `json:"id"`
	Position   int                           `json:"position"`
	Status     StepStatus                    `json:"status"`
	Attempt    int                           `json:"attempt"`
	QueuedAt   int64                         `json:"queued_at"`
	StartedAt  int64                         `json:"started_at"`
	FinishedAt int64                         `json:"finished_at"`
	ExitCode   *int                          `json:"exit_code"`
	Error      string                        `json:"error"`
	SkipKind   string                        `json:"skip_kind"`
	Runner     string                        `json:"runner"`
	Outputs    map[string]string             `json:"outputs"`
	Excerpt    string                        `json:"excerpt"` // a failed attempt's last lines of output
	Container  string                        `json:"container,omitempty"`
	Image      string                        `json:"image,omitempty"`
	Run        string                        `json:"run,omitempty"`
	Needs      []string                      `json:"needs,omitempty"`
	When       *pipeline.Condition           `json:"when,omitempty"`
	Secrets    map[string]pipeline.SecretRef `json:"secrets,omitempty"` // references, never values
	Retry      int                           `json:"retry"`
	Timeout    string                        `json:"timeout"`         // as written: 1h, 10m, 90s
	Group      string                        `json:"group,omitempty"` // the uses step it came from, for a template's step
	// Approvals only. Kind and Message come from the definition; the rest
	// is the current wait and decision.
	Kind        string `json:"kind,omitempty"`
	Message     string `json:"message,omitempty"`
	Description string `json:"description,omitempty"`
	Deadline    int64  `json:"deadline"`
	DecidedBy   string `json:"decided_by"`
	DecidedAt   int64  `json:"decided_at"`
	Comment     string `json:"comment"`
	// WaitKind, for a waiting deploy or rollback step, is what it waits
	// for: lock or observe. Empty for an approval.
	WaitKind string `json:"wait_kind,omitempty"`
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
	// Inputs is what each attempt was given, oldest first; empty for
	// attempts leased by a server older than schema 8.
	Inputs []AttemptInputs `json:"inputs"`
	// Reports are the test result files the step declares, as globs.
	Reports []string `json:"reports,omitempty"`
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
	// RunnerHint says why a run that is waiting for a runner may wait a long
	// time: it builds images, and no runner that can has been seen lately.
	RunnerHint string `json:"runner_hint,omitempty"`
	// PoolHint says why no runner may take the run's waiting steps: no
	// pool serves it, or none of the runners seen lately may (schema 21).
	PoolHint string `json:"pool_hint,omitempty"`
	// Policy sums up the run's policy decisions (schema 16); nil if no
	// policy judged it.
	Policy *RunPolicySummary `json:"policy,omitempty"`
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
	ID     string    `json:"id"`
	Number int       `json:"number"`
	From   string    `json:"from"`
	Status RunStatus `json:"status"`
	Actor  string    `json:"actor"`
	At     int64     `json:"created_at"`
}

// RunnerView is a runner as last seen.
type RunnerView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Version  string `json:"version"`
	LastSeen int64  `json:"last_seen"`
	Online   bool   `json:"online"` // seen within RunnerOffline
	// Pool and Targets, for a deploy runner: its runner pool, and the
	// targets it said it serves.
	Pool    string   `json:"pool,omitempty"`
	Targets []string `json:"targets,omitempty"`
	// Images says the runner builds images (it has image builders).
	Images bool `json:"images,omitempty"`

	// Schema 20 and 21. Kind is build or deploy. Auth is how it
	// authenticated: identity (an enrolled runner), shared (the shared
	// runner token, which says nothing about which runner it is) or pool (a
	// deploy pool's token). Pool is "default" for build runners of the
	// default pool.
	Kind   string         `json:"kind,omitempty"`
	Auth   string         `json:"auth,omitempty"`
	Labels []string       `json:"labels,omitempty"`
	OS     string         `json:"os,omitempty"`
	Arch   string         `json:"arch,omitempty"`
	Accept *accept.Config `json:"accept,omitempty"`
	// Identity is the enrolled runner, for one that is.
	Identity *RunnerIdentity `json:"identity,omitempty"`
	// Work is what it holds now.
	Work []RunnerWork `json:"work"`
}

// RunnerWork is a step a runner holds.
type RunnerWork struct {
	Run     string `json:"run"`
	Project string `json:"project"`
	Number  int    `json:"number"`
	Step    string `json:"step"`
}

const runCols = `id, project, number, pipeline, status, cancel_requested, event, ref, branch, tag, sha,
	actor, message, author, created_at, started_at, finished_at, draft, pipeline_hash`

// runCols8 are the columns of runs that schema 8 added.
const runCols8 = `, rerun_of, rerun_of_number, rerun_from, min_schema`

// runCols9 are the columns of runs that schema 9 added.
const runCols9 = `, cancel_reason, superseded_by, superseded_by_number`

// runCols is the columns scanRun reads.
func (e *Engine) runCols() string {
	switch {
	case e.knows(schemaPullRequests):
		return runCols + runCols8 + runCols9
	case e.knows(schemaReruns):
		return runCols + runCols8
	}
	return runCols
}

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
	err := sc.Scan(dst...)
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
	Limit   int
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
	rows, err := e.db.Query(`SELECT run_id, step_id, status, `+waitKind+` FROM steps WHERE run_id IN (`+strings.Join(marks, ",")+`)
		ORDER BY run_id, position`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var runID, kind string
		var b StepBrief
		if err := rows.Scan(&runID, &b.ID, &b.Status, &kind); err != nil {
			return err
		}
		r := byID[runID]
		r.Steps = append(r.Steps, b)
		if b.Status == StepWaitingS && kind == "" {
			r.Waiting = append(r.Waiting, b.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
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
	switch {
	case e.knows(schemaDeploy):
		return stepCols + stepCols8 + `, wait_kind`
	case e.knows(schemaReruns):
		return stepCols + stepCols8
	}
	return stepCols
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
	if err := sc.Scan(dst...); err != nil {
		return nil, err
	}
	if exit.Valid {
		x := int(exit.Int64)
		v.ExitCode = &x
	}
	v.Outputs = map[string]string{}
	_ = json.Unmarshal([]byte(outputs), &v.Outputs)
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
			v.Secrets = def.Secrets
			v.Container = st.Pipeline.ContainerFor(def)
			v.Kind, v.Message, v.Description = def.Kind, def.Message, def.Description
			v.Group = def.Group
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
	if d.PoolHint, err = e.poolHint(st, d.StepList); err != nil {
		return nil, err
	}
	if d.PoolHint == "" {
		if d.PoolHint, err = e.verifyHint(st); err != nil {
			return nil, err
		}
	}
	d.Policy = policySummary(st)
	return d, nil
}

// reruns lists the runs that re-run a run, oldest first.
func (e *Engine) reruns(runID string) ([]RerunBrief, error) {
	rows, err := e.db.Query(`SELECT id, number, rerun_from, status, actor, created_at FROM runs WHERE rerun_of = ?
		ORDER BY created_at, number`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RerunBrief{}
	for rows.Next() {
		var b RerunBrief
		if err := rows.Scan(&b.ID, &b.Number, &b.From, &b.Status, &b.Actor, &b.At); err != nil {
			return nil, err
		}
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

// Runners lists runners, most recently seen first. Runners not seen for a
// day are forgotten and not listed.
func (e *Engine) Runners() ([]RunnerView, error) {
	if e.knows(schemaIdentity) {
		return e.runnersWithIdentity()
	}
	extra := `, '', '', ''`
	switch {
	case e.knows(schemaImages):
		extra = `, pool, targets, features`
	case e.knows(schemaDeploy):
		extra = `, pool, targets, ''`
	}
	rows, err := e.db.Query(`SELECT id, name, executor, version, last_seen` + extra + ` FROM runners ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	online := e.now().Add(-RunnerOffline).UnixMilli()
	out := []RunnerView{}
	for rows.Next() {
		var r RunnerView
		var targets, features string
		if err := rows.Scan(&r.ID, &r.Name, &r.Executor, &r.Version, &r.LastSeen, &r.Pool, &targets, &features); err != nil {
			return nil, err
		}
		r.Images = slices.Contains(strings.Fields(features), FeatureImages)
		if targets != "" {
			r.Targets = strings.Split(targets, ",")
		}
		r.Online = r.LastSeen >= online
		out = append(out, r)
	}
	return out, rows.Err()
}

// runnersWithIdentity lists runners with what schema 20 records: enrolled runners
// that have not asked for work yet (or were forgotten) included.
func (e *Engine) runnersWithIdentity() ([]RunnerView, error) {
	rows, err := e.db.Query(`SELECT id, name, executor, version, last_seen, pool, targets, features, kind, auth, labels, accept, os, arch
		FROM runners ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	online := e.now().Add(-RunnerOffline).UnixMilli()
	out := []RunnerView{}
	at := map[string]int{}
	for rows.Next() {
		var r RunnerView
		var targets, features, labels, acc string
		if err := rows.Scan(&r.ID, &r.Name, &r.Executor, &r.Version, &r.LastSeen, &r.Pool, &targets, &features, &r.Kind, &r.Auth,
			&labels, &acc, &r.OS, &r.Arch); err != nil {
			rows.Close()
			return nil, err
		}
		r.Images = slices.Contains(strings.Fields(features), FeatureImages)
		if targets != "" {
			r.Targets = strings.Split(targets, ",")
		}
		if r.Kind == "" { // written by an older server
			r.Kind = PoolBuild
			if r.Pool != "" {
				r.Kind, r.Auth = PoolDeploy, AuthPoolToken
			}
		}
		if r.Auth == "" {
			r.Auth = AuthShared
		}
		if r.Kind == PoolBuild && r.Pool == "" {
			r.Pool = DefaultPool
		}
		r.Labels, r.Accept = splitLabels(labels), decodeAccept(acc)
		r.Online = r.LastSeen >= online
		r.Work = []RunnerWork{}
		at[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	idents, err := e.RunnerIdentities()
	if err != nil {
		return nil, err
	}
	for _, id := range idents {
		i, ok := at[id.ID]
		if !ok {
			// Enrolled, but not asking for work: it has not started, it
			// was revoked, or it has been gone for a day.
			i = len(out)
			at[id.ID] = i
			out = append(out, RunnerView{ID: id.ID, Name: id.Name, Executor: id.Executor, Version: id.Version, LastSeen: id.LastSeen,
				Pool: id.Pool, Kind: id.Kind, Auth: AuthIdentity, Labels: id.Labels, OS: id.OS, Arch: id.Arch, Work: []RunnerWork{},
				Online: id.RevokedAt == 0 && id.LastSeen >= online})
		}
		out[i].Identity = id
		if out[i].Pool == "" {
			out[i].Pool = id.Pool
		}
	}
	work, err := e.db.Query(`SELECT l.runner, l.run_id, runs.project, runs.number, l.step_id FROM leases l JOIN runs ON runs.id = l.run_id
		ORDER BY l.run_id, l.step_id`)
	if err != nil {
		return nil, err
	}
	defer work.Close()
	for work.Next() {
		var runner string
		var w RunnerWork
		if err := work.Scan(&runner, &w.Run, &w.Project, &w.Number, &w.Step); err != nil {
			return nil, err
		}
		if i, ok := at[runner]; ok {
			out[i].Work = append(out[i].Work, w)
		}
	}
	return out, work.Err()
}
