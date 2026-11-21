package pipeline

// Changing what a template runs, from the file that uses it, in one line each:
//
//	steps:
//	  ci:
//	    uses: launchway/go-service
//	    skip: [lint, image-scan]                    # leave stages out
//	    extend:                                     # add steps before or after a stage
//	      - after: test
//	        steps:
//	          docs: { image: alpine:3.22, run: ./check-docs.sh }
//	    replace:                                    # take over a stage wholesale
//	      build: { image: golang:1.27, run: make build }
//
// Stages are the template's own steps, by the names its description lists.
// Nothing is hidden: launchway validate --resolved shows the steps that
// result, and a skipped stage does not appear in them.

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Adaptation is what one use of a template changes in it.
type Adaptation struct {
	Skip    []string
	Extend  []*Extension
	Replace []*Step
	Line    int

	skipLines map[string]int
}

// Extension is steps added next to a stage: after it (they need it, and what
// needed the stage needs them too), or before it (they need what it needed,
// and it needs them).
type Extension struct {
	After, Before string
	Steps         []*Step
	Line          int
}

// parseAdapt reads skip, extend or replace of a step that uses a template.
func (ps *parser) parseAdapt(s *Step, key string, n *yaml.Node, at string) {
	if s.adapt == nil {
		s.adapt = &Adaptation{Line: n.Line, skipLines: map[string]int{}}
	}
	a := s.adapt
	switch key {
	case "skip":
		for _, v := range ps.valueList(n, at) {
			if !stepIDRe.MatchString(v) {
				ps.add(n, at, "%q is not a stage name: write the names the flight plan lists, such as skip: [lint, image-scan]", clipText(v, 40))
				continue
			}
			a.Skip = append(a.Skip, v)
			a.skipLines[v] = n.Line
		}
	case "extend":
		items := []*yaml.Node{n}
		if n.Kind == yaml.SequenceNode {
			items = n.Content
		}
		for i, it := range items {
			where := at
			if n.Kind == yaml.SequenceNode {
				where = fmt.Sprintf("%s[%d]", at, i)
			}
			e := &Extension{Line: it.Line}
			var stepsNode *yaml.Node
			ps.eachKey(it, where, []string{"after", "before", "steps"}, func(k, v *yaml.Node) {
				switch k.Value {
				case "after":
					e.After = ps.str(v, where+".after")
				case "before":
					e.Before = ps.str(v, where+".before")
				default:
					stepsNode = v
				}
			})
			switch {
			case it.Kind != yaml.MappingNode:
			case (e.After == "") == (e.Before == ""):
				ps.add(it, where, "write extend: { after: stage, steps: { … } } or { before: stage, steps: { … } }: one of after and before, and the steps to add")
			case stepsNode == nil:
				ps.add(it, where+".steps", "say which steps to add: steps: { name: { image: …, run: … } }")
			default:
				e.Steps = ps.parseAdded(stepsNode, where+".steps")
				a.Extend = append(a.Extend, e)
			}
		}
	case "replace":
		for _, st := range ps.parseAdded(n, at) {
			a.Replace = append(a.Replace, st)
		}
	}
}

// parseAdded reads steps written in a use: as the steps of a pipeline, with
// no parameters to refer to and no loops (a stage added to a flight plan is
// one step).
func (ps *parser) parseAdded(n *yaml.Node, at string) []*Step {
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "write the steps as a mapping of names to steps")
		return nil
	}
	sub := &parser{tmpl: &paramScope{params: map[string]*Param{}, values: map[string]string{}}}
	p := &Pipeline{}
	sub.parseSteps(n, p)
	for _, pr := range sub.problems {
		pr.Path = at + "." + strings.TrimPrefix(pr.Path, "steps.")
		ps.problems = append(ps.problems, pr)
	}
	for _, st := range p.Steps {
		switch {
		case st.ForEach != nil:
			ps.add(n, at+"."+st.ID, "a step added to a flight plan cannot be repeated with for_each: write one step for each")
		case st.IsUse():
			ps.add(n, at+"."+st.ID, "a step added to a flight plan is a step of its own, not a use of another template")
		}
	}
	return p.Steps
}

// adapt applies a use's adaptation to the steps of the template it uses.
// Problems are in the file that has the use.
func (x *expander) adapt(sc *scope, use *Step, t *Template, at string) {
	a := use.adapt
	if a == nil {
		return
	}
	stages := func() string {
		var ids []string
		for _, s := range t.Steps {
			if s.adaptedBy != "extend" {
				ids = append(ids, s.ID)
			}
		}
		return strings.Join(ids, ", ")
	}
	find := func(id string) *Step {
		for _, s := range t.Steps {
			if s.ID == id {
				return s
			}
		}
		return nil
	}
	unknown := func(line int, path, id string) {
		x.problem(sc, line, 0, at+"."+path, "the template %s has no stage %q; its stages are %s", t.Name, clipText(id, 40), stages())
	}
	for _, id := range a.Skip {
		s := find(id)
		if s == nil {
			unknown(a.skipLines[id], "skip", id)
			continue
		}
		s.ifTest = &paramTest{keep: false}
	}
	for _, r := range a.Replace {
		old := find(r.ID)
		if old == nil {
			unknown(r.Line, "replace", r.ID)
			continue
		}
		c := *r
		c.ID = r.ID
		if len(c.Needs) == 0 {
			c.Needs = old.Needs
		}
		c.ifTest = old.ifTest
		c.adaptedBy = "replace"
		for i, s := range t.Steps {
			if s == old {
				t.Steps[i] = &c
			}
		}
	}
	for _, e := range a.Extend {
		ref, path := e.After, "extend.after"
		if ref == "" {
			ref, path = e.Before, "extend.before"
		}
		anchor := find(ref)
		if anchor == nil {
			unknown(e.Line, path, ref)
			continue
		}
		var added []string
		skip := false
		for _, st := range e.Steps {
			if find(st.ID) != nil {
				x.problem(sc, st.Line, 0, at+".extend.steps."+st.ID, "the template %s already has a step called %q: choose another name, or use replace to take it over", t.Name, st.ID)
				skip = true
			}
			added = append(added, st.ID)
		}
		if skip {
			continue
		}
		var news []*Step
		for _, st := range e.Steps {
			c := *st
			c.adaptedBy = "extend"
			if len(c.Needs) == 0 {
				if e.After != "" {
					c.Needs = []string{anchor.ID}
				} else {
					c.Needs = slices.Clone(anchor.Needs)
				}
			}
			news = append(news, &c)
		}
		i := slices.Index(t.Steps, anchor)
		if e.After != "" {
			for _, s := range t.Steps {
				if s.adaptedBy == "extend" && slices.Contains(added, s.ID) {
					continue
				}
				if slices.Contains(s.Needs, anchor.ID) {
					s.Needs = append(slices.Clone(s.Needs), added...)
				}
			}
			t.Steps = slices.Insert(t.Steps, i+1, news...)
		} else {
			anchor.Needs = append(slices.Clone(anchor.Needs), added...)
			t.Steps = slices.Insert(t.Steps, i, news...)
		}
	}
}

// desugar turns a pipeline file that is only a use of a template into the
// file it stands for:
//
//	uses: launchway/go-service
//	with: { go_version: "1.27" }
//
// is
//
//	pipeline: ci
//	on: { push: { branches: [main], paths_ignore: [docs/**, "**/*.md"] }, pull_request: { paths_ignore: [...] } }
//	supersede: true
//	steps:
//	  ci: { uses: launchway/go-service, with: { go_version: "1.27" } }
//
// Whatever the file writes itself (pipeline, on, supersede) is kept.
func (ps *parser) desugar(root *yaml.Node) {
	if root.Kind != yaml.MappingNode || !hasKey(root, "uses") {
		return
	}
	if hasKey(root, "steps") {
		ps.add(root, "uses", "write either uses: (the whole pipeline is one flight plan) or steps:, not both; to add steps next to the flight plan, put it under steps: as a step")
		return
	}
	var rest []*yaml.Node
	step := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: root.Line}
	have := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		have[k.Value] = true
		switch k.Value {
		case "uses", "with", "skip", "extend", "replace":
			step.Content = append(step.Content, k, v)
		default:
			rest = append(rest, k, v)
		}
	}
	scalar := func(v string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Line: root.Line}
	}
	if !have["pipeline"] {
		rest = append(rest, scalar("pipeline"), scalar("ci"))
	}
	if !have["on"] {
		var on yaml.Node
		_ = yaml.Unmarshal([]byte("{ push: { branches: [main], paths_ignore: [\"docs/**\", \"**/*.md\"] }, pull_request: { paths_ignore: [\"docs/**\", \"**/*.md\"] } }"), &on)
		rest = append(rest, scalar("on"), on.Content[0])
	}
	if !have["supersede"] {
		rest = append(rest, scalar("supersede"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true", Line: root.Line})
	}
	steps := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: root.Line, Content: []*yaml.Node{scalar("ci"), step}}
	root.Content = append(rest, scalar("steps"), steps)
}

// checkAdaptKeys refuses skip, extend and replace on a step that uses no template.
func (ps *parser) checkAdaptKeys(at string, keys map[string]*yaml.Node) {
	if keys["with"] != nil && keys["uses"] == nil {
		ps.add(keys["with"], at+".with", "with passes values to a template, so it goes with uses")
	}
	for _, k := range []string{"skip", "extend", "replace"} {
		if keys[k] != nil && keys["uses"] == nil {
			ps.add(keys[k], at+"."+k, "%s changes what a template runs, so it goes with uses", k)
		}
	}
}

// useKeys are the keys a step that uses a template may have; the template
// says everything else.
var useKeys = []string{"uses", "with", "description", "needs", "when", "retry", "timeout", "for_each", "exclude", "max_parallel", "fail_fast", "add", "continue_for", "serial", "skip", "extend", "replace"}
