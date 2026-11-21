package pipeline

// A step on a runner and a workspace of its own (schema 27):
//
//	test:
//	  for_each: { arch: [amd64, arm64] }
//	  runner: { labels: [ { each: arch } ] }       # or { pool: gpu }
//	  image: golang:1.25
//	  run: go test ./...
//
// A step that sets runner: gets a fresh checkout of the run's commit on a
// runner that matches, shared with no other step; files travel between such
// steps only through artifacts and fetch. Steps without runner: keep the
// run's shared workspace and the top-level selector.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// HasOwnRunner reports whether the step chose its runner and so has a
// workspace of its own.
func (s *Step) HasOwnRunner() bool { return s != nil && s.Runner != nil }

// parseStepRunner reads a step's runner: { pool, labels }. In a template the
// pool and the labels may be { param: … }; in a looped step a label may be
// { each: key } (with format:).
func (ps *parser) parseStepRunner(s *Step, n *yaml.Node, at string) {
	sel := &RunnerSelector{}
	s.Runner = sel
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "runner is a mapping, for example: runner: { labels: [arm64] } or { pool: gpu }")
		return
	}
	ps.eachKey(n, at, []string{"pool", "labels"}, func(k, v *yaml.Node) {
		w := at + "." + k.Value
		switch k.Value {
		case "pool":
			if v.Kind == yaml.MappingNode && hasKey(v, "each") {
				ps.add(v, w, "a pool is not chosen by a loop value; write it, or in a template take it from a parameter")
				return
			}
			sel.Pool = ps.scalar(v, w)
			if sel.Pool != "" && !ValidLabel(sel.Pool) {
				ps.add(v, w, "%q is not a pool name: use lower-case letters, digits, '.', '_' and '-'", sel.Pool)
			}
		case "labels":
			sel.Labels = ps.stepRunnerLabels(v, w)
		}
	})
	if len(sel.Labels) > MaxRunnerLabels {
		ps.add(n, at+".labels", "at most %d labels", MaxRunnerLabels)
	}
	if sel.Pool == "" && len(sel.Labels) == 0 {
		ps.add(n, at, "name a pool or labels: a step with a runner of its own must say which runners it needs")
	}
}

func (ps *parser) stepRunnerLabels(v *yaml.Node, w string) []string {
	if v.Kind != yaml.SequenceNode {
		labels := ps.valueList(v, w) // { param: list }
		for _, l := range labels {
			if !ValidLabel(l) {
				ps.add(v, w, "%q is not a label: use lower-case letters, digits, '.', '_' and '-'", l)
			}
		}
		return labels
	}
	var out []string
	for i, item := range v.Content {
		if item.Kind == yaml.MappingNode && hasKey(item, "each") {
			ps.curField, ps.curName = "runner", strconv.Itoa(i)
			out = append(out, ps.eachValue(item, fmt.Sprintf("%s[%d]", w, i)))
			ps.curField, ps.curName = "", ""
			continue
		}
		l := ps.scalar(item, fmt.Sprintf("%s[%d]", w, i))
		if !ValidLabel(l) {
			ps.add(item, w, "%q is not a label: use lower-case letters, digits, '.', '_' and '-'", l)
		}
		out = append(out, l)
	}
	for i, l := range out {
		if slices.Contains(out[:i], l) && !strings.Contains(l, "{") {
			ps.add(v, w, "%q is listed twice", l)
		}
	}
	return out
}

// eachRunner gives a loop member's step the labels its { each } values name,
// and says what is not a label.
func (s *Step) eachRunner(idx int, value string) {
	sel := *s.Runner
	sel.Labels = slices.Clone(sel.Labels)
	if idx >= 0 && idx < len(sel.Labels) {
		sel.Labels[idx] = value
	}
	s.Runner = &sel
}

// stepRunnerProblems checks the labels a loop member got.
func (s *Step) stepRunnerProblems() string {
	if s.Runner == nil {
		return ""
	}
	for _, l := range s.Runner.Labels {
		if !ValidLabel(l) {
			return fmt.Sprintf("%q is not a runner label: a runner label uses lower-case letters, digits, '.', '_' and '-'", clipText(l, 40))
		}
	}
	return ""
}

// parseRunStepFlow reads on_failure and runner, the keys of a step that runs
// something that say how its failure counts and where it runs.
func (ps *parser) parseRunStepFlow(s *Step, key string, v *yaml.Node, at string) {
	if key == "runner" {
		ps.parseStepRunner(s, v, at)
		return
	}
	ps.parseOnFailure(s, v, at)
}
