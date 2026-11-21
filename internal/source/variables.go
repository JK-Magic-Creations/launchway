package source

import (
	"context"
	"sort"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// VariableSource is where variables set at levels come from: the settings
// package resolves them with the one precedence rule. The manager asks for
// the variables in effect for a project when it makes a run's graph, and
// records which ones the run used.
type VariableSource interface {
	// Bind returns the variables in effect for a project, for an
	// environment if one is known.
	Bind(ctx context.Context, project, environment string) (pipeline.Variables, error)
	// RecordRun keeps the variables a run used, as it used them.
	RecordRun(ctx context.Context, runID, environment string, used []pipeline.BoundVariable) error
}

type varHolder struct{ v VariableSource }

// SetVariables says where variables come from. Without it, an input or a
// parameter that names a variable finds none.
func (m *Manager) SetVariables(v VariableSource) { m.vars.Store(&varHolder{v}) }

func (m *Manager) variables() VariableSource {
	if h := m.vars.Load(); h != nil {
		return h.v
	}
	return nil
}

// varRecord is what a run used, kept once the run exists.
type varRecord struct {
	env  string
	used []pipeline.BoundVariable
}

// environmentOf is the environment a run is for when the pipeline has an
// input called environment: the value given, else its default. A variable
// is resolved for the environment of the steps that use it, which must be
// known when the run is created.
func environmentOf(pl *pipeline.Pipeline, given map[string]string) string {
	in := pl.Input("environment")
	if in == nil {
		return ""
	}
	if v, ok := given["environment"]; ok {
		return v
	}
	if in.Default != nil {
		return *in.Default
	}
	return ""
}

// wantsVariables reports whether a pipeline can take a variable at all: an
// input that names one, or templates, whose parameters may.
func wantsVariables(pl *pipeline.Pipeline) bool {
	if len(pl.Libraries) > 0 {
		return true
	}
	for _, in := range pl.Inputs {
		if in.From != "" {
			return true
		}
	}
	return false
}

// bindVariables asks for the variables in effect for the run being made.
func (m *Manager) bindVariables(ctx context.Context, project string, pl *pipeline.Pipeline, given map[string]string) (pipeline.Variables, string, error) {
	src := m.variables()
	if src == nil || project == "" || !wantsVariables(pl) {
		return nil, "", nil
	}
	env := environmentOf(pl, given)
	vars, err := src.Bind(ctx, project, env)
	return vars, env, err
}

// record keeps what a run used; a failure is logged, never the run's.
func (m *Manager) record(ctx context.Context, runID string, rec *varRecord) {
	src := m.variables()
	if src == nil || rec == nil || len(rec.used) == 0 || runID == "" {
		return
	}
	if err := src.RecordRun(ctx, runID, rec.env, rec.used); err != nil {
		m.log.Error("could not record the variables a run used", "run", runID, "err", err)
	}
}

func mergeUsed(a, b []pipeline.BoundVariable) []pipeline.BoundVariable {
	seen := map[string]bool{}
	var out []pipeline.BoundVariable
	for _, l := range [][]pipeline.BoundVariable{a, b} {
		for _, v := range l {
			if !seen[v.Name] {
				seen[v.Name] = true
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// createRun creates a run and records the variables it used.
func (m *Manager) createRun(ctx context.Context, nr engine.NewRun, rec *varRecord) (*engine.RunSummary, error) {
	run, err := m.eng.CreateRun(nr)
	if err == nil && run != nil {
		m.record(ctx, run.ID, rec)
	}
	return run, err
}

// CheckFor is Check for a project: the variables in effect for it are bound,
// so that a locked variable the file tries to override, or a variable a
// template needs that nothing sets, is reported as a run would report it.
func (m *Manager) CheckFor(ctx context.Context, project, text string, overrides map[string]string) (*pipeline.Pipeline, error) {
	pl, err := pipeline.Parse([]byte(text))
	if err != nil {
		return nil, err
	}
	vars, _, err := m.bindVariables(ctx, project, pl, nil)
	if err != nil {
		return nil, err
	}
	if pl, err = m.preview(pl); err != nil {
		return nil, err
	}
	pl, _, _, err = m.expandVars(ctx, pl, overrides, checkFresh, vars)
	return pl, err
}
