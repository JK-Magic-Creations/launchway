package pipeline

// Flow control keys of schema 24: conditions on step outputs, tolerated
// failures, retry waits and causes, a run's timeout, lanes and the target
// branch of a pull request. A pipeline that uses any of them carries
// "requires": 24, so servers that do not know them leave its runs alone.

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaFlow is the server schema that brought flow control: when: { output },
// on_failure, retry with a wait and causes, a run timeout, lanes and
// on.pull_request.into.
const SchemaFlow = 24

// OnFailure values.
const (
	OnFailureStop     = "stop"
	OnFailureContinue = "continue"
)

// UsesSchemaFlow reports whether the pipeline uses a key servers older
// than schema 24 do not know.
func (p *Pipeline) UsesSchemaFlow() bool {
	for _, s := range p.Steps {
		if s.OnFailure != "" || (s.When != nil && s.When.Output != "") {
			return true
		}
	}
	return false
}

// parseOnFailure reads on_failure of a step that runs something: stop (the
// default) or continue.
func (ps *parser) parseOnFailure(s *Step, v *yaml.Node, at string) {
	switch val := ps.str(v, at); val {
	case OnFailureStop:
		s.OnFailure = ""
	case OnFailureContinue:
		s.OnFailure = OnFailureContinue
	default:
		ps.add(v, at, "on_failure is stop (the default) or continue")
	}
}

// checkOutputTests checks the when conditions that test step outputs, once
// the graph is known: the step must be one this step needs, directly or
// through others, and not one that repeats.
func (ps *parser) checkOutputTests(p *Pipeline) {
	ids := map[string]*Step{}
	for _, s := range p.Steps {
		ids[s.ID] = s
	}
	for _, s := range p.Steps {
		c := s.When
		if c == nil || c.Output == "" {
			continue
		}
		at := "steps." + s.ID + ".when.output"
		line := c.inputLine
		if line == 0 {
			line = s.Line
		}
		add := func(format string, args ...any) {
			ps.problems = append(ps.problems, Problem{Line: line, Path: at, Message: fmt.Sprintf(format, args...)})
		}
		step, key, ok := strings.Cut(c.Output, ".")
		switch {
		case !ok || step == "" || strings.TrimSpace(key) == "" || strings.ContainsAny(key, " \t"):
			add("write the step and the output's key, for example: when: { output: scan.changed, equals: true }")
			continue
		case c.Input != "":
			add("a condition tests an input or an output, not both: put them in two steps, or test one of them")
			continue
		case len(c.Equals) == 0:
			add("say which values run the step, with equals: when: { output: %s, equals: true }", c.Output)
			continue
		}
		t := ids[step]
		switch {
		case step == s.ID:
			add("a step cannot test its own output")
		case t == nil:
			add("there is no step %q to test an output of", clipText(step, 60))
		case t.IsUse():
			add("%s uses a template: test an output of one of the steps the template made, which the template does not expose by name", step)
		case t.ForEach != nil:
			add("%s repeats over a list (for_each); an output of one of its members cannot be tested, because which member is not known when the pipeline is written. "+
				"Add a step that needs %s and writes the output you want to test", step, step)
		case !p.Upstream(s.ID)[step]:
			add("%s is not a step this step needs, directly or through other steps, so its output is not known when this step is decided: add it to needs", step)
		}
	}
}
