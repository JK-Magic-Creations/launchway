package pipeline

// Extra combinations and tolerated members of a loop.
//
//	test:
//	  for_each: { python: ["3.12", "3.13"], os: [debian, alpine] }
//	  add: [ { python: "3.14", os: debian } ]       # outside the cross product
//	  continue_for: [ { python: "3.14" } ]          # may fail without failing the run
//
// add appends whole combinations after the cross product, in the order
// written; continue_for selects members, matched as exclude entries are,
// that behave as on_failure: continue. Members keep the id the loop keys
// give them, so a loop that gains an add entry keeps the ids it had.

import (
	"fmt"
	"maps"
	"strings"

	"gopkg.in/yaml.v3"
)

// loopExtraKeys are the loop keys of schema 27.
var loopExtraKeys = []string{"add", "continue_for"}

// loopEntry is one entry of add or continue_for, with where it was written.
type loopEntry struct {
	values map[string]string
	line   int
}

// parseLoopExtras reads add and continue_for.
func (ps *parser) parseLoopExtras(s *Step, at string, nodes map[string]*yaml.Node) {
	f := s.ForEach
	if f == nil {
		return
	}
	if n := nodes["add"]; n != nil {
		f.Add = ps.loopEntries(n, at+".add", f, true)
	}
	if n := nodes["continue_for"]; n != nil {
		f.ContinueFor = ps.loopEntries(n, at+".continue_for", f, false)
	}
	// With every list written in place the lists-dependent mistakes are
	// known now; otherwise expansion reports them.
	if f.literal() {
		for _, p := range f.extraProblems(f.literalLists()) {
			ps.problems = append(ps.problems, Problem{Line: p.line, Path: p.path(at), Message: p.msg})
		}
	}
}

// loopEntries reads a list of combinations. With whole set, every loop key
// must be named (add); otherwise at least one (continue_for).
func (ps *parser) loopEntries(n *yaml.Node, at string, f *ForEach, whole bool) []loopEntry {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		key := at[strings.LastIndex(at, ".")+1:]
		ps.add(n, at, "%s is a list of combinations, for example: %s: [ { %s } ]", key, key, strings.Join(exampleEntry(f), ", "))
		return nil
	}
	var out []loopEntry
	for _, e := range n.Content {
		entry := loopEntry{values: map[string]string{}, line: e.Line}
		ps.eachKey(e, at, f.Keys, func(k, v *yaml.Node) {
			val := ps.str(v, at+"."+k.Value)
			entry.values[k.Value] = val
			if whole {
				if msg := loopValueProblem(val); msg != "" {
					ps.add(v, at+"."+k.Value, "%s", msg)
				}
			}
		})
		if e.Kind != yaml.MappingNode {
			continue
		}
		switch {
		case whole && len(entry.values) != len(f.Keys):
			var missing []string
			for _, k := range f.Keys {
				if _, ok := entry.values[k]; !ok {
					missing = append(missing, k)
				}
			}
			ps.add(e, at, "an added combination names every key of the loop (%s); this one lacks %s", strings.Join(f.Keys, ", "), strings.Join(missing, ", "))
		case len(entry.values) == 0:
			ps.add(e, at, "an entry names at least one of %s", strings.Join(f.Keys, ", "))
		default:
			out = append(out, entry)
		}
	}
	return out
}

func exampleEntry(f *ForEach) []string {
	var out []string
	for _, k := range f.Keys {
		out = append(out, k+": value")
	}
	return out
}

// literal reports whether every list is written in place.
func (f *ForEach) literal() bool {
	for _, l := range f.Lists {
		if l.Input != "" {
			return false
		}
	}
	return true
}

func (f *ForEach) literalLists() [][]string {
	out := make([][]string, len(f.Lists))
	for i, l := range f.Lists {
		out[i] = l.Values
	}
	return out
}

// extraProblem is a mistake in add or continue_for found once the lists are
// known.
type extraProblem struct {
	line int
	key  string // add or continue_for
	msg  string
}

func (p extraProblem) path(at string) string { return at + "." + p.key }

func entryText(f *ForEach, e map[string]string) string {
	var parts []string
	for _, k := range f.Keys {
		if v, ok := e[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

// extraProblems checks add and continue_for against the lists: an added
// combination that is already in the cross product, written twice or left
// out by exclude, and a continue_for entry that matches no member.
func (f *ForEach) extraProblems(lists [][]string) []extraProblem {
	var out []extraProblem
	cross := product(f.Keys, lists, nil)
	for i, a := range f.Add {
		switch {
		case excluded(a.values, f.Exclude):
			out = append(out, extraProblem{a.line, "add", fmt.Sprintf("%s is left out by exclude; remove one of the two", entryText(f, a.values))})
		case matchesCombo(cross, a.values):
			out = append(out, extraProblem{a.line, "add", fmt.Sprintf("%s is already a combination of the lists; add is for combinations outside them", entryText(f, a.values))})
		case i > 0 && addedBefore(f.Add[:i], a.values):
			out = append(out, extraProblem{a.line, "add", fmt.Sprintf("%s is added twice", entryText(f, a.values))})
		}
	}
	members := f.combinations(lists)
	for _, c := range f.ContinueFor {
		if !matchesCombo(members, c.values) {
			out = append(out, extraProblem{c.line, "continue_for", fmt.Sprintf("%s matches no member of the loop", entryText(f, c.values))})
		}
	}
	return out
}

func matchesCombo(combos []map[string]string, entry map[string]string) bool {
	for _, c := range combos {
		if excluded(c, []map[string]string{entry}) {
			return true
		}
	}
	return false
}

func addedBefore(before []loopEntry, v map[string]string) bool {
	for _, b := range before {
		if maps.Equal(b.values, v) {
			return true
		}
	}
	return false
}

// combinations are the members of the loop in the order they run: the cross
// product without what exclude leaves out, then what add appends.
func (f *ForEach) combinations(lists [][]string) []map[string]string {
	combos := product(f.Keys, lists, f.Exclude)
	for _, a := range f.Add {
		if !excluded(a.values, f.Exclude) && !matchesCombo(combos, a.values) {
			combos = append(combos, a.values)
		}
	}
	return combos
}

// toleratesFailure reports whether continue_for selects the combination.
func (f *ForEach) toleratesFailure(c map[string]string) bool {
	for _, e := range f.ContinueFor {
		if excluded(c, []map[string]string{e.values}) {
			return true
		}
	}
	return false
}

// isExtra reports whether the combination is one add wrote.
func (f *ForEach) isExtra(c map[string]string, lists [][]string) bool {
	return !matchesCombo(product(f.Keys, lists, nil), c)
}

// tolerate makes a step that runs something behave as on_failure: continue,
// because continue_for selected its loop member. A step that uses a template
// passes it on to the template's steps (see emit). Other kinds of step
// cannot tolerate a failure (see on_failure) and are left as they are.
func (s *Step) tolerate() {
	switch {
	case s.IsUse():
		s.continueFail = true
	case s.Kind == "" || s.Kind == KindImage:
		s.OnFailure = OnFailureContinue
	}
}
