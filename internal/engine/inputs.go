package engine

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// What each step attempt was given. When a step is leased, the server
// records with the step.leased event everything it hands the runner that is
// not a secret: the environment with where each value came from, the image
// and container, the script, the library commit, and which secrets the step
// asked for (their references, never their values, which the server never
// sees). So a run can be read later, or re-run from a step, knowing what
// each step received. The step_inputs table holds the same, one row per
// value, so that it can be searched.

// Limits on what is recorded per attempt. Values beyond them are shortened,
// and marked so; the runner still receives them in full.
const (
	MaxInputValueBytes  = 4 << 10  // one variable or parameter
	MaxInputScriptBytes = 16 << 10 // the script
	MaxInputsBytes      = 64 << 10 // all of an attempt's values together
)

// Where a variable came from (InputVar.From).
const (
	FromStep      = "step"      // the step's own env
	FromContainer = "container" // the env of the container it runs in
	FromNeeds     = "needs"     // an output of a step it needs (Step, Output)
	FromLaunchway = "launchway" // the standard LAUNCHWAY_* values
	FromTemplate  = "template"  // a parameter of the template the step came from (Step is the uses step)
)

// StepInputs is what one attempt of a step was given, apart from secret
// values.
type StepInputs struct {
	Image           string        `json:"image,omitempty"`
	Container       string        `json:"container,omitempty"`
	Script          string        `json:"script"`
	ScriptTruncated bool          `json:"script_truncated,omitempty"`
	Library         *InputLibrary `json:"library,omitempty"`
	Env             []InputVar    `json:"env"`              // by name
	Params          []InputVar    `json:"params,omitempty"` // the template's parameters, by name
	Secrets         []InputSecret `json:"secrets,omitempty"`
	// Truncated says that some value was shortened to stay within the limits.
	Truncated bool `json:"truncated,omitempty"`
}

// InputVar is one variable or parameter and where its value came from.
type InputVar struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	From      string `json:"from"`             // FromStep, FromContainer, ...
	Step      string `json:"step,omitempty"`   // needs: the step the output came from; template: the uses step
	Output    string `json:"output,omitempty"` // needs: the output's key
	Truncated bool   `json:"truncated,omitempty"`
}

// InputLibrary is the library whose files a template's step worked with.
type InputLibrary struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// InputSecret is a secret a step asked for: its variable and where the
// runner looked it up. Never a value.
type InputSecret struct {
	Name string `json:"name"`
	Ref  string `json:"ref"` // store:key#field
}

// AttemptInputs is what one attempt was given.
type AttemptInputs struct {
	Attempt int        `json:"attempt"`
	Inputs  StepInputs `json:"inputs"`
}

// stepEnv is the environment a step is given, apart from secrets, with
// where each value came from, by name. Later sources replace earlier ones,
// as in the job: container, step, needs, then the standard values.
func (c *cmd) stepEnv(stepID string) []InputVar {
	st := c.st
	def := st.Pipeline.Step(stepID)
	s := st.Steps[stepID]
	t := st.Trigger
	vars := map[string]InputVar{}
	if ct := st.Pipeline.Container(st.Pipeline.ContainerFor(def)); ct != nil {
		for k, v := range ct.Env {
			vars[k] = InputVar{Name: k, Value: v, From: FromContainer}
		}
	}
	for k, v := range def.Env {
		vars[k] = InputVar{Name: k, Value: v, From: FromStep}
	}
	for _, n := range def.Needs {
		if inGroups(n, def.NeedsGroups) {
			continue // a template's steps are reached through the outputs it exposes
		}
		for k, v := range st.Steps[n].Outputs {
			name := "LAUNCHWAY_NEEDS_" + pipeline.StepEnvName(localID(def, n)) + "_" + pipeline.EnvName(k)
			vars[name] = InputVar{Name: name, Value: v, From: FromNeeds, Step: n, Output: k}
		}
	}
	for _, id := range def.NeedsGroups {
		g := st.Pipeline.Group(id)
		if g == nil {
			continue
		}
		for k, ref := range g.Outputs {
			if dep := st.Steps[ref.Step]; dep != nil {
				if v, ok := dep.Outputs[ref.Key]; ok {
					name := "LAUNCHWAY_NEEDS_" + pipeline.EnvName(localID(def, id)) + "_" + pipeline.EnvName(k)
					vars[name] = InputVar{Name: name, Value: v, From: FromNeeds, Step: ref.Step, Output: ref.Key}
				}
			}
		}
	}
	for k, v := range map[string]string{
		"CI": "true", "LAUNCHWAY": "true",
		"LAUNCHWAY_RUN_ID": st.ID, "LAUNCHWAY_RUN_NUMBER": strconv.Itoa(st.Number),
		"LAUNCHWAY_PROJECT": st.Project, "LAUNCHWAY_PIPELINE": st.Pipeline.Name,
		"LAUNCHWAY_STEP": stepID, "LAUNCHWAY_ATTEMPT": strconv.Itoa(s.Attempt),
		"LAUNCHWAY_EVENT": t.Event, "LAUNCHWAY_REF": t.Ref, "LAUNCHWAY_BRANCH": t.Branch,
		"LAUNCHWAY_TAG": t.Tag, "LAUNCHWAY_COMMIT": t.SHA,
	} {
		vars[k] = InputVar{Name: k, Value: v, From: FromLaunchway}
	}
	if t.Event == pipeline.EventPullRequest {
		vars["LAUNCHWAY_PULL_REQUEST"] = InputVar{Name: "LAUNCHWAY_PULL_REQUEST", Value: strconv.Itoa(t.PullRequest), From: FromLaunchway}
		if t.PullRequestInto != "" {
			vars["LAUNCHWAY_PULL_REQUEST_INTO"] = InputVar{Name: "LAUNCHWAY_PULL_REQUEST_INTO", Value: t.PullRequestInto, From: FromLaunchway}
		}
	}
	inputEnv(t, vars)
	eachEnv(def, vars)
	out := slices.Collect(maps.Values(vars))
	slices.SortFunc(out, func(a, b InputVar) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// stepSecrets returns the secrets a step may ask the runner for: none in a
// draft or pull-request run whose steps may not read secrets.
func (c *cmd) stepSecrets(def *pipeline.Step) map[string]pipeline.SecretRef {
	if !c.st.secretsAllowed() {
		return nil
	}
	return def.Secrets
}

// inputs is what a step attempt is about to be given, within the limits.
func (c *cmd) inputs(stepID string) *StepInputs {
	st := c.st
	def := st.Pipeline.Step(stepID)
	in := &StepInputs{Image: def.Image, Container: st.Pipeline.ContainerFor(def), Script: def.Run, Env: c.stepEnv(stepID)}
	if def.Library != "" {
		lib := InputLibrary{Name: def.Library}
		if l := st.Pipeline.Library(def.Library); l != nil {
			lib.Commit = l.Commit
		}
		in.Library = &lib
	}
	if g := st.Pipeline.Group(def.Group); g != nil {
		for _, k := range slices.Sorted(maps.Keys(g.Params)) {
			in.Params = append(in.Params, InputVar{Name: k, Value: g.Params[k], From: FromTemplate, Step: g.ID})
		}
	}
	secrets := c.stepSecrets(def)
	for _, k := range slices.Sorted(maps.Keys(secrets)) {
		in.Secrets = append(in.Secrets, InputSecret{Name: k, Ref: secrets[k].String()})
	}
	capInputs(in)
	return in
}

// capInputs shortens values so that an attempt's record stays within the
// limits, marking what it shortened.
func capInputs(in *StepInputs) {
	cut := func(v *string, limit int) bool {
		if len(*v) <= limit {
			return false
		}
		n := max(limit, 0)
		for n > 0 && !utf8.RuneStart((*v)[n]) {
			n--
		}
		*v = (*v)[:n]
		in.Truncated = true
		return true
	}
	in.ScriptTruncated = cut(&in.Script, MaxInputScriptBytes)
	budget := MaxInputsBytes - len(in.Script) - len(in.Image) - len(in.Container)
	for _, list := range [][]InputVar{in.Env, in.Params} {
		for i := range list {
			v := &list[i]
			if cut(&v.Value, min(MaxInputValueBytes, budget)) {
				v.Truncated = true
			}
			budget -= len(v.Value)
		}
	}
}

// storeInputs writes an attempt's inputs to step_inputs, one row per value,
// replacing what an earlier lease of the same attempt recorded.
func storeInputs(tx *txx, runID, stepID string, attempt int, in *StepInputs) error {
	if _, err := tx.Exec(`DELETE FROM step_inputs WHERE run_id = ? AND step_id = ? AND attempt = ?`, runID, stepID, attempt); err != nil {
		return err
	}
	type row struct {
		kind, name, value, origin, step, key string
		truncated                            bool
	}
	rows := []row{
		{kind: "image", value: in.Image},
		{kind: "container", value: in.Container},
		{kind: "script", value: in.Script, truncated: in.ScriptTruncated},
	}
	if in.Library != nil {
		rows = append(rows, row{kind: "library", name: in.Library.Name, value: in.Library.Commit})
	}
	for _, v := range in.Env {
		rows = append(rows, row{"env", v.Name, v.Value, v.From, v.Step, v.Output, v.Truncated})
	}
	for _, v := range in.Params {
		rows = append(rows, row{"param", v.Name, v.Value, v.From, v.Step, v.Output, v.Truncated})
	}
	for _, sc := range in.Secrets {
		rows = append(rows, row{kind: "secret", name: sc.Name, value: sc.Ref})
	}
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT INTO step_inputs(run_id, step_id, attempt, kind, name, value, origin, origin_step, origin_key, truncated)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, runID, stepID, attempt, r.kind, r.name, r.value, r.origin, r.step, r.key, boolInt(r.truncated)); err != nil {
			return err
		}
	}
	return nil
}

// stepInputs reads what the attempts of a run's steps were given, from
// step_inputs: step → attempts, oldest first. With stepID, only that step.
func (e *Engine) stepInputs(runID, stepID string) (map[string][]AttemptInputs, error) {
	out := map[string][]AttemptInputs{}
	if !e.knows(schemaReruns) {
		return out, nil
	}
	q := `SELECT step_id, attempt, kind, name, value, origin, origin_step, origin_key, truncated FROM step_inputs WHERE run_id = ?`
	args := []any{runID}
	if stepID != "" {
		q += ` AND step_id = ?`
		args = append(args, stepID)
	}
	rows, err := e.db.Query(q+` ORDER BY step_id, attempt, kind, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var step, kind, name, value, origin, ostep, okey string
		var attempt, truncated int
		if err := rows.Scan(&step, &attempt, &kind, &name, &value, &origin, &ostep, &okey, &truncated); err != nil {
			return nil, err
		}
		list := out[step]
		if len(list) == 0 || list[len(list)-1].Attempt != attempt {
			list = append(list, AttemptInputs{Attempt: attempt, Inputs: StepInputs{Env: []InputVar{}}})
		}
		in := &list[len(list)-1].Inputs
		in.Truncated = in.Truncated || truncated != 0
		v := InputVar{Name: name, Value: value, From: origin, Step: ostep, Output: okey, Truncated: truncated != 0}
		switch kind {
		case "image":
			in.Image = value
		case "container":
			in.Container = value
		case "script":
			in.Script, in.ScriptTruncated = value, truncated != 0
		case "library":
			in.Library = &InputLibrary{Name: name, Commit: value}
		case "env":
			in.Env = append(in.Env, v)
		case "param":
			in.Params = append(in.Params, v)
		case "secret":
			in.Secrets = append(in.Secrets, InputSecret{Name: name, Ref: value})
		default:
			// A kind a newer server records; this one does not show it.
		}
		out[step] = list
	}
	return out, rows.Err()
}
