package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/verify/source"
)

// AI steps and model calls (schema 19).
//
// Models are the operator's: the server's configuration lists them
// (ai.models), with a provider, an endpoint, the key's secret-store
// reference, limits, and the projects that may use each. With none
// configured, nothing is ever sent anywhere, and ai steps and verify steps
// that name a model are refused when a run is created.
//
// An ai step is queued only if its model allows the project, the run may
// read secrets (a pull-request run only if the project allows secrets for
// pull requests; a draft only if the drafts setting does), the run has not
// used up the model's calls per run, and the project has not used up the
// model's tokens for the day (UTC). A runner that says it can call models
// (FeatureAI) makes the call with a key from its own secret stores: the key
// never enters a container, and the server never sees it. What the call
// used is recorded in the event that ends the step, and counted in
// ai_usage. The answer becomes the step's outputs and its artifact
// "response"; it never runs anything.

// FeatureAI is the runner feature of calling models for ai steps.
const FeatureAI = "ai"

// AIRecord is what an ai step's call was, in the event that ends it.
type AIRecord struct {
	Model    string  `json:"model"`              // the registry's name
	ModelID  string  `json:"model_id,omitempty"` // what the provider said answered
	Provider string  `json:"provider,omitempty"`
	Usage    AIUsage `json:"usage"`
	// StopReason is why the model stopped (end_turn, max_tokens, ...).
	StopReason string `json:"stop_reason,omitempty"`
}

// ModelJob is a model a runner calls: the registry entry (with the key's
// reference, never its value) and how many tokens it may write.
type ModelJob struct {
	Model     ai.Model `json:"model"`
	MaxTokens int      `json:"max_tokens"`
}

// AIJob is what a runner needs for an ai step.
type AIJob struct {
	Model        ModelJob     `json:"model"`
	Instructions string       `json:"instructions,omitempty"`
	Prompt       string       `json:"prompt"`
	Inputs       []AIJobInput `json:"inputs,omitempty"`
	Schema       string       `json:"schema,omitempty"`
	Temperature  *float64     `json:"temperature,omitempty"`
}

// AIJobInput is one input of an ai step: Text for an output (worked out by
// the server), or a file of an artifact the runner fetches (see Job.Fetch).
type AIJobInput struct {
	Label    string `json:"label"`
	Text     string `json:"text,omitempty"`
	Cut      bool   `json:"cut,omitempty"` // Text was cut to MaxBytes
	From     string `json:"from,omitempty"`
	Artifact string `json:"artifact,omitempty"`
	File     string `json:"file,omitempty"`
	MaxBytes int    `json:"max_bytes"`
}

// model returns a model of the registry.
func (e *Engine) model(name string) (ai.Model, bool) {
	if e.live().models == nil {
		return ai.Model{}, false
	}
	return e.live().models.Get(name)
}

// Models returns the registry's models.
func (e *Engine) Models() []ai.Model {
	if e.live().models == nil {
		return nil
	}
	return e.live().models.List()
}

// checkModel checks that a step may use a model.
func (e *Engine) checkModel(project string, s *pipeline.Step, name string) pipeline.Problems {
	m, ok := e.model(name)
	msg := ""
	switch {
	case !ok && len(e.Models()) == 0:
		msg = fmt.Sprintf("this server has no AI models configured (ai.models), so it cannot ask %s; everything else works without one", name)
	case !ok:
		names := []string{}
		for _, m := range e.Models() {
			names = append(names, m.Name)
		}
		msg = fmt.Sprintf("model %q is not configured on this server; it knows %s", name, strings.Join(names, ", "))
	case project != "" && !m.Allows(project):
		msg = fmt.Sprintf("project %s may not use model %s (see its projects in the server's configuration)", project, name)
	default:
		return nil
	}
	return pipeline.Problems{{Line: s.Line, Path: "steps." + s.ID + ".model", Message: s.ID + ": " + msg}}
}

func checkQuery(typ, text string, declared []string) error {
	if typ == "opensearch" {
		typ = "elasticsearch"
	}
	return source.CheckQuery(typ, text, declared)
}

func checkSchema(schema string) error { return ai.CheckSchema(json.RawMessage(schema)) }

// aiRefused says why the run may not call models, or "".
func (st *RunState) aiRefused() string {
	switch {
	case st.Draft != nil && !st.secretsAllowed():
		return "ai steps call a model with a key from the runner's secret stores, so they do not run in draft runs unless the drafts setting allows secrets"
	case !st.secretsAllowed():
		return "ai steps call a model with a key from the runner's secret stores, so they do not run in pull-request runs " +
			"unless the project allows secrets for pull requests (pull_requests.secrets)"
	}
	return ""
}

// aiCalls counts the model calls a run made or is making.
func (st *RunState) aiCalls(model string) int {
	n := 0
	for _, id := range st.Order {
		s := st.Steps[id]
		def := st.Pipeline.Step(id)
		switch {
		case s.AI != nil && s.AI.Model == model:
			n += max(s.AI.Usage.Calls, 1)
		case def.IsAI() && def.AI != nil && def.AI.Model == model && (s.Status == StepQueuedS || s.Status.Active()):
			n++
		case s.Ver != nil && s.Ver.Explanation != nil && s.Ver.Explanation.Model == model && s.Ver.Explanation.Usage != nil:
			n += s.Ver.Explanation.Usage.Calls
		}
	}
	return n
}

// today is the day budgets count in: the UTC date.
func day(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02") }

// tokensUsed returns the tokens a project used with a model today.
func tokensUsed(q interface {
	QueryRow(string, ...any) *sql.Row
}, project, model string, now int64) (int64, error) {
	var in, out int64
	err := q.QueryRow(`SELECT input_tokens, output_tokens FROM ai_usage WHERE project = ? AND model = ? AND day = ?`,
		project, model, day(now)).Scan(&in, &out)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return in + out, err
}

// modelJob checks that the run may call a model now and returns what the
// runner needs, or why not. want is the tokens the step asks for; 0 takes
// the model's limit.
func (c *cmd) modelJob(name string, want int) (*ModelJob, string) {
	st := c.st
	m, ok := c.e.model(name)
	switch {
	case !ok:
		return nil, fmt.Sprintf("model %s is not configured on this server", name)
	case !m.Allows(st.Project):
		return nil, fmt.Sprintf("project %s may not use model %s", st.Project, name)
	}
	if why := st.aiRefused(); why != "" {
		return nil, why
	}
	if m.CallsPerRun > 0 && st.aiCalls(name) > m.CallsPerRun {
		return nil, fmt.Sprintf("the run has made the %d calls model %s allows a run (calls_per_run)", m.CallsPerRun, name)
	}
	tokens := m.MaxTokens
	if want > 0 {
		tokens = min(want, m.MaxTokens)
	}
	if m.TokensPerDay > 0 {
		used, err := tokensUsed(c.tx, st.Project, name, c.now)
		if err != nil {
			c.dbFail(err)
			return nil, "the database could not be read"
		}
		left := m.TokensPerDay - used
		if left < 64 {
			return nil, fmt.Sprintf("project %s has used the %d tokens model %s allows it a day (tokens_per_day); the budget starts again at 00:00 UTC",
				st.Project, m.TokensPerDay, name)
		}
		tokens = int(min(int64(tokens), left))
	}
	return &ModelJob{Model: m, MaxTokens: tokens}, ""
}

// startAI queues an ai step whose needs are met, if the run may call its
// model.
func (c *cmd) startAI(id string, def *pipeline.Step) {
	s := c.st.Steps[id]
	if ps := c.e.checkModel(c.st.Project, def, def.AI.Model); len(ps) > 0 {
		c.failStep(id, ps[0].Message)
		return
	}
	// The step itself is counted once it is queued; count the calls before.
	if _, why := c.modelJob(def.AI.Model, def.AI.MaxTokens); why != "" {
		if c.err != nil {
			return
		}
		c.failStep(id, why)
		return
	}
	c.emit(StepQueued, Payload{Step: id, Attempt: s.Attempt + 1})
}

// aiJob builds an ai step's job, or says why it cannot run now.
func (c *cmd) aiJob(def *pipeline.Step) (*AIJob, string) {
	a := def.AI
	mj, why := c.modelJob(a.Model, a.MaxTokens)
	if why != "" {
		return nil, why
	}
	j := &AIJob{Model: *mj, Instructions: a.Instructions, Prompt: a.Prompt, Schema: a.Schema, Temperature: a.Temperature}
	for _, in := range a.Inputs {
		ji := AIJobInput{Label: in.Label, From: in.From, Artifact: in.Artifact, File: in.File, MaxBytes: in.MaxBytes}
		if in.Output != nil {
			text, err := c.resolve(def, &pipeline.Value{Output: in.Output})
			if err != nil {
				return nil, fmt.Sprintf("input %s: %v", in.Label, err)
			}
			if len(text) > in.MaxBytes {
				text, ji.Cut = clipText(text, in.MaxBytes), true
			}
			ji.Text = text
		}
		j.Inputs = append(j.Inputs, ji)
	}
	return j, ""
}

// aiOutputs keeps, of what a runner says an ai step's outputs are, only the
// ones the step sets, each at most MaxAIOutputBytes.
func aiOutputs(def *pipeline.Step, in map[string]string) map[string]string {
	known := append(append([]string{}, pipeline.AIOutputs...), pipeline.AISchemaOutputs(def.AI.Schema)...)
	out := map[string]string{}
	for k, v := range in {
		if slices.Contains(known, k) {
			out[k] = clipText(v, MaxAIOutputBytes)
		}
	}
	return out
}

// MaxAIOutputBytes bounds each output of an ai step; the whole answer is in
// its artifact.
const MaxAIOutputBytes = 64 << 10

// cleanAIRecord checks what a runner says a call used.
func cleanAIRecord(def *pipeline.Step, r *AIRecord) *AIRecord {
	if r == nil {
		return nil
	}
	c := *r
	c.Model = def.AI.Model
	c.ModelID, c.Provider, c.StopReason = clipText(c.ModelID, 200), clipText(c.Provider, 40), clipText(c.StopReason, 40)
	c.Usage.Calls = min(max(c.Usage.Calls, 1), 10)
	c.Usage.InputTokens = max(c.Usage.InputTokens, 0)
	c.Usage.OutputTokens = max(c.Usage.OutputTokens, 0)
	if math.IsNaN(c.Usage.CostUSD) || c.Usage.CostUSD < 0 || math.IsInf(c.Usage.CostUSD, 0) {
		c.Usage.CostUSD = 0
	}
	return &c
}

// addUsage counts a call's usage against the project's budget for the day.
func (c *cmd) addUsage(project, model string, at int64, u AIUsage) error {
	if model == "" {
		return nil
	}
	_, err := c.tx.Exec(`INSERT INTO ai_usage(project, model, day, calls, input_tokens, output_tokens, cost_micros) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(project, model, day) DO UPDATE SET calls = ai_usage.calls + excluded.calls,
			input_tokens = ai_usage.input_tokens + excluded.input_tokens, output_tokens = ai_usage.output_tokens + excluded.output_tokens,
			cost_micros = ai_usage.cost_micros + excluded.cost_micros`,
		project, model, day(at), max(u.Calls, 1), u.InputTokens, u.OutputTokens, int64(math.Round(u.CostUSD*1e6)))
	return err
}

// ---- reading ----

// ModelView is a model as the API lists it: names and limits, never the
// endpoint's credentials. Usage is the caller's projects' use today.
type ModelView struct {
	Name          string       `json:"name"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	Local         bool         `json:"local"` // no key: a model server of the operator's own
	Projects      []string     `json:"projects"`
	MaxTokens     int          `json:"max_tokens"`
	MaxInputBytes int          `json:"max_input_bytes"`
	CallsPerRun   int          `json:"calls_per_run"`
	TokensPerDay  int64        `json:"tokens_per_day,omitempty"`
	Temperature   bool         `json:"temperature"`
	UsedToday     []ModelUsage `json:"used_today,omitempty"`
}

// ModelUsage is a project's use of a model on a day.
type ModelUsage struct {
	Project      string  `json:"project"`
	Day          string  `json:"day"`
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// ModelViews lists the registry with today's use by the projects visible
// says the caller may see (nil: all).
func (e *Engine) ModelViews(visible func(string) bool) ([]ModelView, error) {
	out := []ModelView{}
	for _, m := range e.Models() {
		v := ModelView{Name: m.Name, Provider: m.Provider, Model: m.Model, Local: m.Key == nil, Projects: m.Projects, MaxTokens: m.MaxTokens,
			MaxInputBytes: m.MaxInputBytes, CallsPerRun: m.CallsPerRun, TokensPerDay: m.TokensPerDay, Temperature: m.SupportsTemperature()}
		if e.knows(schemaVerify) {
			rows, err := e.db.Query(`SELECT project, day, calls, input_tokens, output_tokens, cost_micros FROM ai_usage WHERE model = ? AND day = ? ORDER BY project`,
				m.Name, day(e.now().UnixMilli()))
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var u ModelUsage
				var micros int64
				if err := rows.Scan(&u.Project, &u.Day, &u.Calls, &u.InputTokens, &u.OutputTokens, &micros); err != nil {
					rows.Close()
					return nil, err
				}
				u.CostUSD = float64(micros) / 1e6
				if visible == nil || visible(u.Project) {
					v.UsedToday = append(v.UsedToday, u)
				}
			}
			rows.Close()
		}
		out = append(out, v)
	}
	return out, nil
}

// ---- explaining a failed step ----

// ExplainInput is what is sent to a model to explain a failed step: all of
// it already masked by the runner that ran the step, or written by the
// server.
type ExplainInput struct {
	RunID, Project, Step string
	RunNumber, Attempt   int
	Kind                 string // the step's kind
	Status, Error        string
	LogTail              []string
	FailedTests          []string
	Findings             string
}

// ExplainContext gathers what a model is told about a step that failed:
// the reason, the last lines of its log, failed tests and findings. It
// fails with ErrConflict for a step that did not fail.
func (e *Engine) ExplainContext(runID, stepID string) (*ExplainInput, error) {
	if !e.knows(schemaVerify) {
		return nil, fmt.Errorf("%w: this server does not know explanations", ErrConflict)
	}
	var in *ExplainInput
	err := e.readRun(runID, func(c *cmd) error {
		s := c.st.Steps[stepID]
		def := c.st.Pipeline.Step(stepID)
		if s == nil || def == nil {
			return fmt.Errorf("%w: step %s in run %s", ErrNotFound, stepID, runID)
		}
		if s.Status != StepFailed {
			return fmt.Errorf("%w: %s did not fail (it is %s); only a failed step is explained", ErrConflict, stepID, s.Status)
		}
		in = &ExplainInput{RunID: runID, Project: c.st.Project, Step: stepID, RunNumber: c.st.Number, Attempt: s.Attempt,
			Kind: nonEmpty(def.Kind, "step"), Status: string(s.Status), Error: s.Error}
		if s.Ver != nil && s.Ver.Verdict != nil {
			b, _ := json.Marshal(s.Ver.Verdict)
			in.Findings = "Verification verdict: " + clipText(string(b), 16<<10)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT text FROM logs WHERE run_id = ? AND step_id = ? AND attempt = ? ORDER BY seq DESC LIMIT 200`, runID, stepID, in.Attempt)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		in.LogTail = append(in.LogTail, clipText(t, 2000))
	}
	rows.Close()
	slices.Reverse(in.LogTail)
	if e.knows(schemaTests) {
		trows, err := e.db.Query(`SELECT name, message FROM test_cases WHERE run_id = ? AND step_id = ? AND attempt = ? AND status IN ('failed', 'error') LIMIT 20`,
			runID, stepID, in.Attempt)
		if err == nil {
			for trows.Next() {
				var name, msg string
				if trows.Scan(&name, &msg) == nil {
					in.FailedTests = append(in.FailedTests, clipText(name, 300)+": "+clipText(msg, 1000))
				}
			}
			trows.Close()
		}
	}
	return in, nil
}

// AIAllowed checks that a project may make one more call to a model now:
// the model allows the project and the day's tokens are not used up. It
// returns the tokens the call may write.
func (e *Engine) AIAllowed(project, model string) (ai.Model, int, error) {
	m, ok := e.model(model)
	switch {
	case !ok && len(e.Models()) == 0:
		return m, 0, fmt.Errorf("%w: this server has no AI models configured (ai.models)", ErrConflict)
	case !ok:
		return m, 0, fmt.Errorf("%w: model %s is not configured on this server", ErrNotFound, model)
	case !m.Allows(project):
		return m, 0, fmt.Errorf("%w: project %s may not use model %s", ErrForbidden, project, model)
	}
	tokens := m.MaxTokens
	if m.TokensPerDay > 0 && e.knows(schemaVerify) {
		used, err := tokensUsed(e.db, project, model, e.now().UnixMilli())
		if err != nil {
			return m, 0, err
		}
		if m.TokensPerDay-used < 64 {
			return m, 0, fmt.Errorf("%w: project %s has used the %d tokens model %s allows it a day; the budget starts again at 00:00 UTC",
				ErrConflict, project, m.TokensPerDay, model)
		}
		tokens = int(min(int64(tokens), m.TokensPerDay-used))
	}
	return m, tokens, nil
}

// ExplanationView is a model's explanation of a failed step.
type ExplanationView struct {
	ID        string  `json:"id"`
	RunID     string  `json:"run_id"`
	Step      string  `json:"step"`
	Attempt   int     `json:"attempt"`
	Model     string  `json:"model"`
	Actor     string  `json:"actor"`
	Text      string  `json:"text"`
	Usage     AIUsage `json:"usage"`
	CreatedAt int64   `json:"created_at"`
}

// RecordExplanation keeps an explanation and counts what it used.
func (e *Engine) RecordExplanation(in *ExplainInput, model, actor, text string, u AIUsage) (*ExplanationView, error) {
	if !e.knows(schemaVerify) {
		return nil, fmt.Errorf("%w: this server does not know explanations", ErrConflict)
	}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := e.now().UnixMilli()
	v := &ExplanationView{ID: ids.New("expl"), RunID: in.RunID, Step: in.Step, Attempt: in.Attempt, Model: model, Actor: actor,
		Text: clipText(text, 64<<10), Usage: u, CreatedAt: now}
	if _, err := tx.Exec(`INSERT INTO ai_explanations(id, run_id, step_id, attempt, project, model, actor, text, input_tokens, output_tokens, cost_micros, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.RunID, v.Step, v.Attempt, in.Project, model, actor, v.Text, u.InputTokens, u.OutputTokens,
		int64(math.Round(u.CostUSD*1e6)), now); err != nil {
		return nil, err
	}
	c := &cmd{e: e, tx: tx, now: now}
	if err := c.addUsage(in.Project, model, now, u); err != nil {
		return nil, err
	}
	return v, tx.Commit()
}

// Explanations lists the explanations of a run's step, newest first.
func (e *Engine) Explanations(runID, stepID string) ([]ExplanationView, error) {
	out := []ExplanationView{}
	if !e.knows(schemaVerify) {
		return out, nil
	}
	rows, err := e.db.Query(`SELECT id, run_id, step_id, attempt, model, actor, text, input_tokens, output_tokens, cost_micros, created_at
		FROM ai_explanations WHERE run_id = ? AND step_id = ? ORDER BY created_at DESC LIMIT 20`, runID, stepID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v ExplanationView
		var micros int64
		if err := rows.Scan(&v.ID, &v.RunID, &v.Step, &v.Attempt, &v.Model, &v.Actor, &v.Text, &v.Usage.InputTokens, &v.Usage.OutputTokens,
			&micros, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Usage.Calls, v.Usage.CostUSD = 1, float64(micros)/1e6
		out = append(out, v)
	}
	return out, rows.Err()
}

var _ = strconv.Itoa
