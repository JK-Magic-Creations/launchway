package pipeline

// Fetching the artifacts of every member of a loop:
//
//	package:
//	  needs: build
//	  fetch:
//	    - { from: build, name: binary, into: dist }             # dist/<member>/…
//	    - { from: build, name: binary, member: { os: linux, arch: amd64 }, into: out }
//
// <member> is the member's values joined by _ in key order (linux_amd64).
// With member:, the one member's files go directly in into (or the
// workspace).

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// MemberDir is the directory name of a member of a loop in the all-members
// form of fetch: its values joined by _ in the loop's key order.
func (l *Loop) MemberDir(m LoopMember) string {
	parts := make([]string, len(l.Keys))
	for i, k := range l.Keys {
		parts[i] = m.Each[k]
	}
	return strings.Join(parts, "_")
}

// parseFetchMember reads member: { key: value, ... }.
func (ps *parser) parseFetchMember(n *yaml.Node, at string) map[string]string {
	if n.Kind != yaml.MappingNode || len(n.Content) == 0 {
		ps.add(n, at, "member names a value for every key of the loop, for example: member: { os: linux, arch: amd64 }")
		return nil
	}
	m := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if _, dup := m[k.Value]; dup {
			ps.add(k, at+"."+k.Value, "%q is written twice", k.Value)
			continue
		}
		m[k.Value] = ps.str(n.Content[i+1], at+"."+k.Value)
	}
	return m
}

// loopFetchProblem checks a fetch from a loop written in the pipeline file:
// the all-members form needs into; member names every key of the loop, and,
// when the lists are written in place, values that exist.
func loopFetchProblem(f *Fetch, loop *Step) string {
	fe := loop.ForEach
	if f.Member == nil {
		if f.Into == "" {
			return fmt.Sprintf("%s is a loop: say where the members' files go with into: <directory>; each member's files are put in <directory>/<member>, for example %s_…", f.From, memberExample(fe))
		}
		return ""
	}
	var missing, unknown []string
	for _, k := range fe.Keys {
		if _, ok := f.Member[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range f.Member {
		if !slices.Contains(fe.Keys, k) {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	switch {
	case len(unknown) > 0:
		return fmt.Sprintf("member has %s, which %s does not have; its keys are %s", strings.Join(unknown, ", "), f.From, strings.Join(fe.Keys, ", "))
	case len(missing) > 0:
		return fmt.Sprintf("member names every key of the loop (%s); this one lacks %s", strings.Join(fe.Keys, ", "), strings.Join(missing, ", "))
	}
	if fe.literal() {
		combos := fe.combinations(fe.literalLists())
		if !matchesCombo(combos, f.Member) {
			return fmt.Sprintf("%s has no member %s", f.From, entryText(fe, f.Member))
		}
	}
	return ""
}

func memberExample(fe *ForEach) string {
	if len(fe.Lists) > 0 && len(fe.Lists[0].Values) > 0 {
		return fe.Lists[0].Values[0]
	}
	return "value"
}

// checkFetchLoops checks, in a pipeline whose loops are expanded, the fetches
// that name a loop: the member exists, and the all-members form has an into.
// With final set (templates expanded too) a fetch that names nothing is an
// error; before that, a name such as use/loop may belong to a template not
// read yet.
func checkFetchLoops(p *Pipeline, final bool) Problems {
	var out Problems
	for _, s := range p.Steps {
		for _, f := range s.Fetch {
			at := "steps." + s.ID + ".fetch"
			l := p.Loop(f.From)
			if l == nil {
				if final && p.Step(f.From) == nil {
					out = append(out, Problem{Line: f.Line, Path: at, Message: "fetches from " + quoteText(f.From) + ", but there is no step or loop with that name"})
				}
				continue
			}
			if f.Member == nil {
				if f.Into == "" {
					out = append(out, Problem{Line: f.Line, Path: at, Message: f.From + " is a loop: say where the members' files go with into: <directory>"})
				}
				continue
			}
			if !slices.ContainsFunc(l.Members, func(m LoopMember) bool { return maps.Equal(m.Each, f.Member) }) {
				out = append(out, Problem{Line: f.Line, Path: at, Message: fmt.Sprintf("%s has no member %s", f.From, memberText(l, f.Member))})
			}
		}
	}
	return out
}

func memberText(l *Loop, each map[string]string) string {
	var parts []string
	for _, k := range l.Keys {
		if v, ok := each[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

// fromTemplateLoop returns the step that uses a template when a fetch names
// a loop inside it (use/loop), or nil.
func fromTemplateLoop(from string, ids map[string]*Step) *Step {
	use, _, ok := strings.Cut(from, "/")
	if s := ids[use]; ok && s != nil && s.IsUse() {
		return s
	}
	return nil
}
