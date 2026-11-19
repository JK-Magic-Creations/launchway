package pipeline

// How run inputs, loops and steps that run after failures meet deploy and
// rollback steps.
//
//   - A deploy step's version, an image tag or a value it writes may be a
//     run input ({ input: name }) of type string, choice or number; its
//     environment may be a choice input whose options are all environment
//     names. Bind puts the run's value in place, so the engine and the
//     deploy runner see plain values, checked against the server's
//     environments when the run is created, as written ones are.
//   - Deploy and rollback steps cannot be repeated by for_each (each
//     deployment holds its environment's lock and is recorded on its own),
//     and are not in templates, so no loop contains them.
//   - A deploy step runs only after the steps it needs succeeded: it takes
//     no when: { upstream: … }. And a rollback step decides on the steps it
//     needs, so none of them, directly or not, may be a step that runs
//     after failures (upstream: failed or always): its success would hide
//     the failure the rollback is for. Such a cleanup step should need the
//     rollback instead.

import (
	"fmt"
	"strings"
)

// deployValues lists a deploy step's values that may be run inputs.
func deployValues(s *Step) []*Value {
	var out []*Value
	if s.Version != nil {
		out = append(out, s.Version)
	}
	if s.ArgoCD != nil && s.ArgoCD.WriteBack != nil {
		for _, c := range s.ArgoCD.WriteBack.Changes {
			if c.Tag != nil {
				out = append(out, c.Tag)
			}
			if c.Digest != nil {
				out = append(out, c.Digest)
			}
			if c.Value != nil {
				out = append(out, c.Value)
			}
		}
	}
	return out
}

// checkDeployInputs checks the run inputs deploy steps use, and the rules
// about steps that run after failures.
func (ps *parser) checkDeployInputs(p *Pipeline) {
	for _, s := range p.Steps {
		if !s.IsDeployKind() {
			continue
		}
		at := "steps." + s.ID
		for _, v := range deployValues(s) {
			if v.Input == "" {
				continue
			}
			in := p.Input(v.Input)
			switch {
			case in == nil:
				ps.problems = append(ps.problems, Problem{Line: v.inputLine, Path: at, Message: fmt.Sprintf("%s: there is no input called %q", s.ID, clipText(v.Input, 60))})
			case in.Type == InputList || in.Type == InputBoolean:
				ps.problems = append(ps.problems, Problem{Line: v.inputLine, Path: at, Message: fmt.Sprintf(
					"%s: %s is a %s; a deploy step's values take a string, choice or number input", s.ID, in.Name, in.Type)})
			}
		}
		if s.environmentInput != "" {
			in := p.Input(s.environmentInput)
			switch {
			case in == nil:
				ps.problems = append(ps.problems, Problem{Line: s.EnvironmentLine(), Path: at + ".environment",
					Message: fmt.Sprintf("%s: there is no input called %q", s.ID, clipText(s.environmentInput, 60))})
			case in.Type != InputChoice:
				ps.problems = append(ps.problems, Problem{Line: s.EnvironmentLine(), Path: at + ".environment",
					Message: fmt.Sprintf("%s: an environment may come from a choice input, whose options say where it may deploy; %s is a %s", s.ID, in.Name, in.Type)})
			default:
				for _, o := range in.Options {
					if !nameRe.MatchString(o) {
						ps.problems = append(ps.problems, Problem{Line: in.Line, Path: "inputs." + in.Name + ".options",
							Message: fmt.Sprintf("%q cannot be an environment, which %s deploys to: use lower-case letters, digits and hyphens", clipText(o, 40), s.ID)})
					}
				}
			}
		}
		if s.IsDeploy() && s.When.UpstreamRule() != UpstreamSucceeded {
			ps.problems = append(ps.problems, Problem{Line: s.Line, Path: at + ".when.upstream", Message: fmt.Sprintf(
				"%s: a deploy step runs only after the steps it needs succeeded; it takes no upstream", s.ID)})
		}
		if s.IsRollback() {
			for id := range p.Upstream(s.ID) {
				if g := p.Step(id); g != nil && g.When.UpstreamRule() != UpstreamSucceeded {
					ps.problems = append(ps.problems, Problem{Line: g.Line, Path: "steps." + g.ID + ".when.upstream", Message: fmt.Sprintf(
						"%s runs after failures (upstream: %s), so the rollback step %s, which needs it, would not see them; "+
							"make %s need %s instead", g.ID, g.When.Upstream, s.ID, g.ID, s.ID)})
				}
			}
		}
	}
}

// bindDeploy puts a run's input values in a deploy step's values and
// environment. It returns a copy; s is not changed.
func bindDeploy(s *Step, values map[string]string) (*Step, string) {
	need := s.environmentInput != ""
	for _, v := range deployValues(s) {
		need = need || v.Input != ""
	}
	if !need {
		return s, ""
	}
	c := *s
	lit := func(v *Value) *Value {
		if v == nil || v.Input == "" {
			return v
		}
		return &Value{Literal: values[v.Input]}
	}
	c.Version = lit(s.Version)
	if s.ArgoCD != nil {
		a := *s.ArgoCD
		if s.ArgoCD.WriteBack != nil {
			w := *s.ArgoCD.WriteBack
			w.Changes = make([]Change, len(s.ArgoCD.WriteBack.Changes))
			for i, ch := range s.ArgoCD.WriteBack.Changes {
				ch.Tag, ch.Digest, ch.Value = lit(ch.Tag), lit(ch.Digest), lit(ch.Value)
				w.Changes[i] = ch
			}
			a.WriteBack = &w
		}
		c.ArgoCD = &a
	}
	for _, v := range []*Value{c.Version} {
		if v != nil && strings.TrimSpace(v.Literal) == "" && v.Output == nil && v.Run == "" {
			return nil, fmt.Sprintf("%s: the version comes from an input that is empty", s.ID)
		}
	}
	if s.environmentInput != "" {
		c.Environment = values[s.environmentInput]
		c.environmentInput = ""
		if !nameRe.MatchString(c.Environment) {
			return nil, fmt.Sprintf("%s: the environment comes from the input %s, which is %q, not an environment name", s.ID, s.environmentInput, c.Environment)
		}
	}
	return &c, ""
}
