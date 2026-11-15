package pipeline

// Binding a pipeline to one run's input values.
//
// Parse reads a file the same way whoever starts it. Bind then makes the
// graph of one run: it puts the run's input values where with: refers to
// them, and repeats looped steps over their lists. Expand comes after, to
// replace template steps. The run keeps the bound, expanded graph, which
// holds plain values only, and its input values (in its trigger), so a
// resume or a re-run sees exactly what the run was created with.

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// inputRef is a with value that comes from a run input: with: { name: {
// input: version } }, optionally with format: for text around it.
type inputRef struct {
	input, format string
	line          int
}

// BindOptions are the server's limits, and whether the graph is only being
// shown.
type BindOptions struct {
	// MaxLoopMembers bounds the combinations of one loop; 0 is
	// DefaultMaxLoopMembers.
	MaxLoopMembers int
	// Preview binds for showing the graph, not for running it: a loop whose
	// values are not known stands aside instead of failing.
	Preview bool
}

func (o BindOptions) limit() int {
	if o.MaxLoopMembers <= 0 {
		return DefaultMaxLoopMembers
	}
	return min(o.MaxLoopMembers, MaxLoopMembersLimit)
}

// PreviewValues are the values a graph is shown with when nobody has given
// any: each input's default, or nothing.
func PreviewValues(p *Pipeline) map[string]string {
	out := make(map[string]string, len(p.Inputs))
	for _, in := range p.Inputs {
		if in.Default != nil {
			out[in.Name] = *in.Default
		} else {
			out[in.Name] = in.zero()
		}
	}
	return out
}

// Bound reports whether a pipeline is ready to be expanded and run: no step
// is still to be repeated by a loop.
func (p *Pipeline) Bound() bool {
	for _, s := range p.Steps {
		if s.ForEach != nil || len(s.withInputs) > 0 || len(s.eachRefs) > 0 || s.environmentInput != "" {
			return false
		}
		for _, v := range deployValues(s) {
			if v.Input != "" {
				return false
			}
		}
	}
	return true
}

// Bind returns the graph of a run of p with the given input values, as
// ResolveInputs returns them. p is not changed. On failure the error is
// Problems.
func Bind(p *Pipeline, values map[string]string, o BindOptions) (*Pipeline, error) {
	out := *p
	out.Steps = make([]*Step, len(p.Steps))
	out.maxLoop = o.limit()
	var problems Problems
	for i, s := range p.Steps {
		c := *s
		if len(s.withInputs) > 0 {
			c.With = cloneMap(s.With)
			for k, r := range s.withInputs {
				v := values[r.input]
				if r.format != "" {
					v = strings.Replace(r.format, "%s", v, 1)
				}
				c.With[k] = v
			}
			c.withInputs = nil
		}
		if s.IsDeployKind() {
			b, msg := bindDeploy(&c, values)
			if msg != "" {
				problems = append(problems, Problem{Line: s.Line, Path: "steps." + s.ID, Message: msg})
				out.Steps[i] = &c
				continue
			}
			c = *b
		}
		out.Steps[i] = &c
	}
	lookup := func(l LoopList) ([]string, bool, string) {
		if l.Input == "" {
			return l.Values, true, ""
		}
		v, ok := values[l.Input]
		if !ok {
			if o.Preview {
				return nil, false, ""
			}
			return nil, false, fmt.Sprintf("there is no value for the input %s", l.Input)
		}
		items := ListItems(v)
		if items == nil {
			return nil, false, fmt.Sprintf("the input %s is not a list", l.Input)
		}
		if len(items) == 0 && o.Preview {
			return nil, false, ""
		}
		return items, true, ""
	}
	steps, loops, ps := expandLoops(out.Steps, "", out.maxLoop, lookup, o.Preview)
	problems = append(problems, ps...)
	out.Steps, out.Loops = steps, loops
	if len(problems) > 0 {
		sort.SliceStable(problems, func(i, j int) bool { return problems[i].Line < problems[j].Line })
		return nil, problems
	}
	if len(out.Steps) == 0 {
		return nil, Problems{{Message: "bound, the pipeline has no steps left"}}
	}
	if len(out.Steps) > MaxExpandedSteps {
		return nil, Problems{{Message: fmt.Sprintf("with its loops repeated, the pipeline has %d steps; at most %d are allowed", len(out.Steps), MaxExpandedSteps)}}
	}
	return &out, nil
}

// ---- reading references to inputs ----

// withValue reads one with value: a value, a list (for a parameter of type
// list), or in the pipeline { input: name } with an optional format.
func (ps *parser) withValue(s *Step, key string, v *yaml.Node, where string) string {
	switch {
	case v.Kind == yaml.SequenceNode:
		items := []string{}
		for _, c := range v.Content {
			if c.Kind != yaml.ScalarNode {
				ps.add(c, where, "a list holds single values")
				continue
			}
			items = append(items, c.Value)
		}
		raw, _ := jsonList(items)
		return raw
	case v.Kind == yaml.MappingNode && hasKey(v, "input"):
		if ps.tmpl != nil {
			ps.add(v, where, "a template cannot read run inputs; take the value as a parameter of this template")
			return ""
		}
		r := &inputRef{line: v.Line}
		var formatNode *yaml.Node
		ps.eachKey(v, where, []string{"input", "format"}, func(k, v2 *yaml.Node) {
			if k.Value == "input" {
				r.input = ps.str(v2, where+".input")
			} else {
				r.format, formatNode = ps.str(v2, where+".format"), v2
			}
		})
		if formatNode != nil && (strings.Count(r.format, "%s") != 1 || strings.Count(r.format, "%") != 1) {
			ps.add(formatNode, where+".format", "a format has exactly one %%s, where the value goes, and no other %%")
			r.format = ""
		}
		if s.withInputs == nil {
			s.withInputs = map[string]*inputRef{}
		}
		s.withInputs[key] = r
		ps.withInputs = append(ps.withInputs, withInputRef{step: s, key: key, ref: r, formatSet: formatNode != nil})
		return "{" + r.input + "}"
	}
	return ps.scalar(v, where)
}

type withInputRef struct {
	step      *Step
	key       string
	ref       *inputRef
	formatSet bool
}

// checkInputRefs checks the inputs that with values and loop lists name,
// now that every input is known.
func (ps *parser) checkInputRefs(p *Pipeline) {
	for _, w := range ps.withInputs {
		at := "steps." + w.step.ID + ".with." + w.key
		in := p.Input(w.ref.input)
		switch {
		case in == nil:
			ps.problems = append(ps.problems, Problem{Line: w.ref.line, Path: at, Message: fmt.Sprintf("there is no input called %q", clipText(w.ref.input, 60))})
		case w.formatSet && in.Type != InputString && in.Type != InputChoice:
			ps.problems = append(ps.problems, Problem{Line: w.ref.line, Path: at + ".format", Message: fmt.Sprintf("format is for string and choice inputs; %s is a %s", in.Name, in.Type)})
		}
	}
	for _, l := range ps.loopInputs {
		in := p.Input(l.input)
		switch {
		case in == nil:
			ps.problems = append(ps.problems, Problem{Line: l.line, Path: l.at, Message: fmt.Sprintf("there is no input called %q", clipText(l.input, 60))})
		case in.Type != InputList:
			ps.problems = append(ps.problems, Problem{Line: l.line, Path: l.at, Message: fmt.Sprintf("%s is a %s; a loop needs an input of type list", in.Name, in.Type)})
		case in.Default != nil:
			for _, v := range ListItems(*in.Default) {
				if msg := loopValueProblem(v); msg != "" {
					ps.problems = append(ps.problems, Problem{Line: in.Line, Path: "inputs." + in.Name + ".default", Message: msg})
				}
			}
		}
	}
	slices.SortStableFunc(ps.problems, func(a, b Problem) int { return a.Line - b.Line })
}

func jsonList(items []string) (string, error) {
	b, err := json.Marshal(items)
	return string(b), err
}

// canonList writes a list value canonically, as a JSON array.
func canonList(v string) string {
	items, msg := ParseList(v)
	if msg != "" {
		return v
	}
	s, _ := jsonList(items)
	return s
}

// checkInputs checks everything about inputs that needs the whole file.
func (ps *parser) checkInputs(p *Pipeline) {
	ps.checkInputTests(p)
	ps.checkScheduleInputs(p)
	ps.checkWebhookInputs(p)
	ps.checkInputRefs(p)
	ps.checkTriggeredInputs(p)
	ps.checkDeployInputs(p)
}
