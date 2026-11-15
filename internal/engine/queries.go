package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	Timeout    pipeline.Duration             `json:"timeout"`
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
}

// RunDetail is everything about one run.
type RunDetail struct {
	RunSummary
	Source   Source             `json:"source"`
	Graph    *pipeline.Pipeline `json:"graph"`
	Levels   [][]string         `json:"levels"`
	StepList []StepView         `json:"step_list"`
}

// RunnerView is a runner as last seen.
type RunnerView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Version  string `json:"version"`
	LastSeen int64  `json:"last_seen"`
	Online   bool   `json:"online"` // seen within RunnerOffline
}

const runCols = `id, project, number, pipeline, status, cancel_requested, event, ref, branch, tag, sha,
	actor, message, author, created_at, started_at, finished_at, draft, pipeline_hash`

func scanRun(sc interface{ Scan(...any) error }) (*RunSummary, error) {
	r := &RunSummary{}
	var cancel, draft int
	err := sc.Scan(&r.ID, &r.Project, &r.Number, &r.Pipeline, &r.Status, &cancel, &r.Event, &r.Ref, &r.Branch,
		&r.Tag, &r.SHA, &r.Actor, &r.Message, &r.Author, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &draft, &r.PipelineHash)
	r.CancelRequested = cancel != 0
	r.Draft = draft != 0
	return r, err
}

func (e *Engine) runSummary(id string) (*RunSummary, error) {
	r, err := scanRun(e.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id = ?`, id))
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
	q := `SELECT ` + runCols + ` FROM runs`
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
		r, err := scanRun(rows)
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
	rows, err := e.db.Query(`SELECT run_id, step_id, status FROM steps WHERE run_id IN (`+strings.Join(marks, ",")+`)
		ORDER BY run_id, position`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var runID string
		var b StepBrief
		if err := rows.Scan(&runID, &b.ID, &b.Status); err != nil {
			return err
		}
		r := byID[runID]
		r.Steps = append(r.Steps, b)
		if b.Status == StepWaitingS {
			r.Waiting = append(r.Waiting, b.ID)
		}
	}
	return rows.Err()
}

const stepCols = `run_id, step_id, position, status, attempt, queued_at, started_at, finished_at, exit_code,
	error, skip_kind, runner, outputs, deadline, decided_by, decided_at, comment, excerpt`

func scanStep(sc interface{ Scan(...any) error }) (*StepView, error) {
	v := &StepView{}
	var exit sql.NullInt64
	var outputs string
	if err := sc.Scan(&v.RunID, &v.ID, &v.Position, &v.Status, &v.Attempt, &v.QueuedAt, &v.StartedAt, &v.FinishedAt,
		&exit, &v.Error, &v.SkipKind, &v.Runner, &outputs, &v.Deadline, &v.DecidedBy, &v.DecidedAt, &v.Comment, &v.Excerpt); err != nil {
		return nil, err
	}
	if exit.Valid {
		x := int(exit.Int64)
		v.ExitCode = &x
	}
	v.Outputs = map[string]string{}
	_ = json.Unmarshal([]byte(outputs), &v.Outputs)
	return v, nil
}

func (e *Engine) stepView(runID, stepID string) (*StepView, error) {
	return scanStep(e.db.QueryRow(`SELECT `+stepCols+` FROM steps WHERE run_id = ? AND step_id = ?`, runID, stepID))
}

// GetRun returns a run with its graph and every step.
func (e *Engine) GetRun(id string) (*RunDetail, error) {
	sum, err := e.runSummary(id)
	if err != nil {
		return nil, err
	}
	var data string
	if err := e.db.QueryRow(`SELECT data FROM events WHERE run_id = ? AND type = ? ORDER BY seq LIMIT 1`, id, RunCreated).Scan(&data); err != nil {
		return nil, err
	}
	var created Payload
	if err := json.Unmarshal([]byte(data), &created); err != nil {
		return nil, err
	}
	d := &RunDetail{RunSummary: *sum, Graph: created.Pipeline, Levels: created.Pipeline.Levels()}
	if created.Source != nil {
		d.Source = *created.Source
	}
	rows, err := e.db.Query(`SELECT `+stepCols+` FROM steps WHERE run_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		if def := created.Pipeline.Step(v.ID); def != nil {
			v.Image, v.Run, v.Needs, v.When, v.Retry, v.Timeout = def.Image, def.Run, def.Needs, def.When, def.Retry, def.Timeout
			v.Secrets = def.Secrets
			v.Container = created.Pipeline.ContainerFor(def)
			v.Kind, v.Message, v.Description = def.Kind, def.Message, def.Description
			v.Group = def.Group
		}
		d.StepList = append(d.StepList, *v)
	}
	return d, rows.Err()
}

// Logs returns log lines for one step attempt after the given sequence number.
// Attempt 0 means the latest attempt.
func (e *Engine) Logs(runID, stepID string, attempt, after, limit int) ([]LogLine, int, error) {
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
	rows, err := e.db.Query(`SELECT seq, at, stream, text FROM logs WHERE run_id = ? AND step_id = ? AND attempt = ? AND seq > ?
		ORDER BY seq LIMIT ?`, runID, stepID, attempt, after, limit)
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
	rows, err := e.db.Query(`SELECT id, name, executor, version, last_seen FROM runners ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	online := e.now().Add(-RunnerOffline).UnixMilli()
	out := []RunnerView{}
	for rows.Next() {
		var r RunnerView
		if err := rows.Scan(&r.ID, &r.Name, &r.Executor, &r.Version, &r.LastSeen); err != nil {
			return nil, err
		}
		r.Online = r.LastSeen >= online
		out = append(out, r)
	}
	return out, rows.Err()
}
