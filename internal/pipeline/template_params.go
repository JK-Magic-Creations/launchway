package pipeline

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/starters"
)

// Template parameters: what a template declares, and checking a value against it.

// paramScope is what a template being read knows about its parameters.
type paramScope struct {
	params map[string]*Param
	values map[string]string // every parameter's value for this use, defaults included
}

type yamlPos struct{ Line, Column int }

// paramTest is a template step's if: whether the step stays.
type paramTest struct {
	keep bool
}

// ---- reading pipelines that use templates ----

// parseLibraries reads libraries: { name: { repo: r, version: v } }.
func (ps *parser) parseLibraries(n *yaml.Node, p *Pipeline) {
	if n.Kind != yaml.MappingNode {
		ps.add(n, "libraries", "libraries is a mapping of names to libraries, for example: platform: { repo: ci-templates, version: v3 }")
		return
	}
	if len(n.Content)/2 > MaxLibraries {
		ps.add(n, "libraries", "a pipeline may use at most %d libraries", MaxLibraries)
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		at := "libraries." + k.Value
		if seen[k.Value] {
			ps.add(k, at, "library %q is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		if !libNameRe.MatchString(k.Value) {
			ps.add(k, at, "%q is not a valid library name: use lower-case letters, digits and hyphens, starting with a letter (at most 40)", k.Value)
		}
		l := &Library{Name: k.Value, Line: k.Line}
		ps.eachKey(v, at, []string{"repo", "version"}, func(k2, v2 *yaml.Node) {
			switch k2.Value {
			case "repo":
				l.Repo = ps.str(v2, at+".repo")
				if !libNameRe.MatchString(l.Repo) {
					ps.add(v2, at+".repo", "repo names one of the libraries the server allows, such as ci-templates; %q is not such a name", clipText(l.Repo, 60))
				}
			case "version":
				l.Version = ps.str(v2, at+".version")
				if !ValidVersion(l.Version) {
					ps.add(v2, at+".version", "the version is a tag, a branch or a full commit id, of at most %d letters, digits and . _ / + -", MaxVersionLen)
				}
			}
		})
		if v.Kind == yaml.MappingNode {
			if l.Repo == "" {
				ps.add(k, at+".repo", "say which library: repo: one of the libraries the server allows")
			}
			if l.Version == "" {
				ps.add(k, at+".version", "say which version: a tag such as v3, a branch, or a full commit id")
			}
		}
		p.Libraries = append(p.Libraries, l)
	}
}

// parseWith reads the values a step passes to its template, as written. In a
// template, a value may be { param: name }: the using template's parameter.
func (ps *parser) parseWith(n *yaml.Node, at string) (map[string]string, map[string]*yamlPos) {
	out, lines := map[string]string{}, map[string]*yamlPos{}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "with is a mapping of parameter names to values")
		return out, lines
	}
	if len(n.Content)/2 > MaxParams {
		ps.add(n, at, "at most %d values", MaxParams)
		return out, lines
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		where := at + "." + k.Value
		if _, dup := out[k.Value]; dup {
			ps.add(k, where, "%q is written twice", k.Value)
			continue
		}
		ps.curField, ps.curName = "with", k.Value
		val := ps.withValue(ps.cur, k.Value, v, where)
		ps.curField, ps.curName = "", ""
		if len(val) > MaxParamValueLen {
			ps.add(v, where, "the value is longer than %d bytes", MaxParamValueLen)
		}
		out[k.Value] = val
		lines[k.Value] = &yamlPos{Line: k.Line, Column: k.Column}
	}
	return out, lines
}

// parseUseStep checks a step that uses a template.
func (ps *parser) parseUseStep(at string, s *Step, keys map[string]*yaml.Node) {
	for _, key := range append(append([]string{}, stepKeys...), "if") {
		n := keys[key]
		if n != nil && !contains(useKeys, key) && (key != "if" || ps.tmpl == nil) {
			ps.add(n, at+"."+key, "a step that uses a template takes no %s: the template says what to run (it may take %s)", key, strings.Join(useKeys[1:], ", "))
		}
	}
	lib, name, cross := strings.Cut(s.Uses, "/")
	switch {
	case ps.tmpl != nil && cross:
		ps.add(keys["uses"], at+".uses", "a template can use templates of its own library only, written by name alone (uses: %s); "+
			"using a template of another library from a template is not supported yet", clipText(name, 60))
	case ps.tmpl != nil:
		if !templateNameRe.MatchString(s.Uses) {
			ps.add(keys["uses"], at+".uses", "%q is not a template name", clipText(s.Uses, 60))
		}
	case !cross || !templateNameRe.MatchString(name) || lib == "":
		ps.add(keys["uses"], at+".uses", "write uses: library/template, such as platform/python-service, with the library named under libraries:")
	}
}

// parseIf reads a template step's if: { param: name } or
// { param: name, equals: value }.
func (ps *parser) parseIf(n *yaml.Node, at string) *paramTest {
	t := &paramTest{keep: true}
	if ps.tmpl == nil {
		ps.add(n, at, "if is only for steps in templates; use when")
		return t
	}
	var name, equals, in, empty string
	var nameNode, equalsNode, inNode, emptyNode, allNode *yaml.Node
	ps.eachKey(n, at, []string{"param", "equals", "in", "empty", "all"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "all":
			allNode = v
		case "param":
			name, nameNode = ps.str(v, at+".param"), v
		case "in":
			in, inNode = ps.str(v, at+".in"), v
		case "empty":
			empty, emptyNode = ps.str(v, at+".empty"), v
		default:
			equals, equalsNode = ps.str(v, at+".equals"), v
		}
	})
	if n.Kind != yaml.MappingNode {
		return t
	}
	if allNode != nil {
		if nameNode != nil || allNode.Kind != yaml.SequenceNode || len(allNode.Content) == 0 {
			ps.add(n, at, "write all: [ { param: a }, { param: b, equals: x } ] alone: the step stays when every test holds")
			return t
		}
		for i, it := range allNode.Content {
			if sub := ps.parseIf(it, fmt.Sprintf("%s.all[%d]", at, i)); sub != nil && !sub.keep {
				t.keep = false
			}
		}
		return t
	}
	p := ps.tmpl.params[name]
	switch {
	case nameNode == nil:
		ps.add(n, at, "write if: { param: name } for a boolean parameter, or if: { param: name, equals: value }")
	case p == nil:
		ps.add(nameNode, at+".param", "%s", ps.unknownParam(name))
	case inNode != nil:
		l := ps.tmpl.params[in]
		if l == nil {
			ps.add(inNode, at+".in", "%s", ps.unknownParam(in))
		} else if l.Type != paramTypeList {
			ps.add(inNode, at+".in", "%s is a %s; in: takes a list parameter", in, l.Type)
		} else {
			items, _ := ParseList(ps.tmpl.values[in])
			t.keep = slices.Contains(items, ps.tmpl.values[name])
		}
	case emptyNode != nil:
		switch {
		case p.Type != paramTypeList && p.Type != paramTypeObjects && p.Type != paramTypeString:
			ps.add(emptyNode, at+".empty", "%s is a %s; empty: is for a list, a list of objects or a text parameter", name, p.Type)
		case empty != "true" && empty != "false":
			ps.add(emptyNode, at+".empty", "write empty: true (keep the step when the list is empty) or empty: false (keep it when the list has something)")
		default:
			isEmpty := strings.TrimSpace(ps.tmpl.values[name]) == ""
			if p.Type == paramTypeList {
				items, _ := ParseList(ps.tmpl.values[name])
				isEmpty = len(items) == 0
			}
			if p.Type == paramTypeObjects {
				isEmpty = len(ObjectItems(ps.tmpl.values[name])) == 0
			}
			t.keep = isEmpty == (empty == "true")
		}
	case equalsNode == nil && p.Type != paramTypeBoolean:
		ps.add(n, at, "%s is a %s, not true or false: say which value keeps the step, with equals:", name, p.Type)
	case equalsNode != nil:
		if msg := p.check(equals); msg != "" {
			ps.add(equalsNode, at+".equals", "%s", msg)
		}
		t.keep = sameValue(p.Type, ps.tmpl.values[name], equals)
	default:
		t.keep = ps.tmpl.values[name] == "true"
	}
	return t
}

// scalar reads a value written in place. In a template it may instead be a
// reference to a parameter: { param: name }, or { param: name, format:
// "text with one %s" } for a string parameter.
func (ps *parser) scalar(n *yaml.Node, at string) string {
	if n.Kind == yaml.MappingNode && hasKey(n, "each") {
		return ps.eachValue(n, at)
	}
	if n.Kind != yaml.MappingNode || !hasKey(n, "param") {
		return ps.str(n, at)
	}
	if ps.tmpl == nil {
		ps.add(n, at, "{ param: … } is for templates; here, write the value itself")
		return ""
	}
	var name, format string
	var formatNode *yaml.Node
	ps.eachKey(n, at, []string{"param", "format"}, func(k, v *yaml.Node) {
		if k.Value == "param" {
			name = ps.str(v, at+".param")
		} else {
			format, formatNode = ps.str(v, at+".format"), v
		}
	})
	p := ps.tmpl.params[name]
	if p == nil {
		ps.add(n, at+".param", "%s", ps.unknownParam(name))
		return ""
	}
	v := ps.tmpl.values[name]
	if formatNode == nil {
		return v
	}
	switch {
	case p.Type != paramTypeString && p.Type != paramTypeEnvironment && p.Type != paramTypeApplication:
		ps.add(formatNode, at+".format", "format is for string parameters; %s is a %s", name, p.Type)
	case strings.Count(format, "%s") != 1 || strings.Count(format, "%") != 1:
		ps.add(formatNode, at+".format", "a format has exactly one %%s, where the value goes, and no other %%")
	default:
		return strings.Replace(format, "%s", v, 1)
	}
	return ""
}

// valueList is strList whose items, or the whole of which, may be
// parameter references in a template.
func (ps *parser) valueList(n *yaml.Node, at string) []string {
	switch n.Kind {
	case yaml.MappingNode:
		if v := ps.scalar(n, at); v != "" {
			return []string{v}
		}
		return nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			switch {
			case c.Kind == yaml.MappingNode:
				out = append(out, ps.scalar(c, at))
			case c.Kind != yaml.ScalarNode:
				ps.add(c, at, "expected a list of values")
			default:
				out = append(out, c.Value)
			}
		}
		return out
	}
	return ps.strList(n, at)
}

func (ps *parser) unknownParam(name string) string {
	names := make([]string, 0, len(ps.tmpl.params))
	for n := range ps.tmpl.params {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Sprintf("there is no parameter %q: the template declares none under params:", clipText(name, 60))
	}
	return fmt.Sprintf("there is no parameter %q; the template declares %s", clipText(name, 60), strings.Join(names, ", "))
}

func hasKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return true
		}
	}
	return false
}

// checkUses checks that every uses step names a library the pipeline lists.
func (ps *parser) checkUses(p *Pipeline) {
	for _, s := range p.Steps {
		if !s.IsUse() || ps.tmpl != nil {
			continue
		}
		lib, _, ok := strings.Cut(s.Uses, "/")
		if ok && lib == starters.Name && p.Library(lib) == nil {
			// The library that ships with Launchway needs no entry under
			// libraries:; naming it there (with another repo) replaces it.
			p.Libraries = append(p.Libraries, &Library{Name: starters.Name, Repo: starters.Name, Version: starters.Version, Line: s.usesLine})
			continue
		}
		if ok && lib != "" && p.Library(lib) == nil {
			ps.problems = append(ps.problems, Problem{Line: s.usesLine, Path: "steps." + s.ID + ".uses",
				Message: fmt.Sprintf("no library called %q is listed under libraries:", clipText(lib, 60))})
		}
	}
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
