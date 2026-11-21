package standards

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// GateStepID is the ID of the step that gives the Inspection's verdict. A
// pull request can require it by name, like any step: <pipeline>/inspection.
const GateStepID = "inspection"

// InjectInput is what the server needs to add the Inspection to a run.
type InjectInput struct {
	Effective Effective
	Policy    Policy
	// Since is the commit the secret scan reads from: the commits after it.
	// Empty reads the whole history.
	Since string
	// Has says whether the pipeline already has a step of that ID. A team
	// that wrote its own step of the same name keeps it, and the check is
	// not added: the pipeline provides it.
	Has func(id string) bool
}

// Injection is what was added.
type Injection struct {
	Steps    []*pipeline.Step
	Provided []string // checks the pipeline already has a step for
}

// BuildSteps makes the steps of the Inspection: one per active check, run in
// parallel and needing nothing, and the gate step that waits for all of
// them. None is given a secret, a deploy or a registry credential, so the
// Inspection runs unchanged in a restricted run. It returns no steps when
// no check is active.
func BuildSteps(in InjectInput) (*Injection, error) {
	var out Injection
	var ids []string
	for _, ec := range in.Effective.Checks {
		if !ec.Active {
			continue
		}
		c, _ := CheckByID(ec.ID)
		if in.Has != nil && in.Has(c.StepID()) {
			out.Provided = append(out.Provided, c.ID)
			ids = append(ids, c.StepID())
			continue
		}
		tool, ok := ToolNamed(c.Tool)
		if !ok {
			return nil, fmt.Errorf("standards: the tool %q of the check %q is not in the tools manifest", c.Tool, c.ID)
		}
		s := &pipeline.Step{
			ID: c.StepID(), Description: c.Title, Image: tool.Image, Run: c.Script, Timeout: pipeline.Duration(c.Budget),
			OnFailure: "continue", Env: map[string]string{},
			Added: &pipeline.Added{By: pipeline.AddedByStandingOrders, Check: c.ID, Why: ec.Because},
		}
		switch c.Report {
		case ReportSBOM:
			s.Reports = &pipeline.Reports{SBOM: c.Outputs}
		default:
			s.Reports = &pipeline.Reports{Findings: c.Outputs}
		}
		if c.ID == "secrets" && in.Since != "" {
			s.Env["LAUNCHWAY_STANDING_SINCE"] = in.Since
		}
		if c.ID == "license" {
			s.Env["STANDING_LICENSE_ALLOW"] = strings.Join(in.Effective.Allow.V, ",")
			s.Env["STANDING_LICENSE_DENY"] = strings.Join(in.Effective.Deny.V, ",")
		}
		if c.Cache != nil {
			key := pipeline.CacheKey{Text: "standing-" + tool.Version, Files: wildcards(c.Cache.Files)}
			s.Cache = []*pipeline.Cache{{Name: c.Cache.Name, Paths: c.Cache.Paths, Key: key, Fallback: "latest"}}
		}
		out.Steps = append(out.Steps, s)
		ids = append(ids, s.ID)
	}
	if len(ids) == 0 {
		return &out, nil
	}
	policy, err := json.Marshal(in.Policy)
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	gate := &pipeline.Step{
		ID: GateStepID, Kind: pipeline.KindGate, Description: "The Inspection's verdict, from what the checks found", Needs: ids,
		Gate:  &pipeline.Gate{Policy: policy},
		Added: &pipeline.Added{By: pipeline.AddedByStandingOrders, Check: "", Why: "the verdict of the Standing orders in force for this repository"},
	}
	out.Steps = append(out.Steps, gate)
	return &out, nil
}

// wildcards turns file names into globs that find them at any depth.
func wildcards(files []string) []string {
	var out []string
	for _, f := range files {
		out = append(out, "**/"+f)
	}
	return out
}

// Apply adds the Inspection to a copy of the pipeline and returns it. The
// pipeline is returned unchanged (the same value) when nothing is added.
func Apply(p *pipeline.Pipeline, in InjectInput) (*pipeline.Pipeline, *Injection, error) {
	if in.Has == nil {
		in.Has = func(id string) bool { return p.Step(id) != nil }
	}
	inj, err := BuildSteps(in)
	if err != nil {
		return p, nil, err
	}
	if len(inj.Steps) == 0 || p.Step(GateStepID) != nil {
		return p, inj, nil
	}
	q := p.Clone()
	if err := q.AppendSteps(inj.Steps, pipeline.SchemaStandings); err != nil {
		return p, inj, err
	}
	return q, inj, nil
}
