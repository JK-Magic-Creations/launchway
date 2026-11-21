package pipeline

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Two things a template may say that a plain value cannot.
//
//   - A parameter's default may derive from another parameter:
//     default: { param: name, format: "apps/%s/base.yaml" }. Its value is the
//     other parameter's, put into the format wherever %s stands (or as it is,
//     without a format),
//     unless a use gives the parameter a value of its own.
//   - A list entry (an image's tag, a write-back change, a verify metric or
//     log query) may carry if, as a step may: { ..., if: { param: x } } is
//     there only when the parameter is true (or, with equals, has that
//     value).

// DefaultFrom says a parameter's default is another parameter's value, in a
// format with one %s.
type DefaultFrom struct {
	Param  string `json:"param"`
	Format string `json:"format,omitempty"`
}

// parseDerivedDefault reads default: { param: name, format: "..." }.
func (ps *parser) parseDerivedDefault(n *yaml.Node, at string) *DefaultFrom {
	d := &DefaultFrom{}
	ps.eachKey(n, at, []string{"param", "format"}, func(k, v *yaml.Node) {
		if k.Value == "param" {
			d.Param = ps.str(v, at+".param")
		} else {
			d.Format = ps.str(v, at+".format")
		}
	})
	if d.Format != "" && (strings.Count(d.Format, "%s") < 1 || strings.Count(d.Format, "%") != strings.Count(d.Format, "%s")) {
		ps.add(n, at+".format", "a format has %%s where the other parameter's value goes (once or more), and no other %%")
	}
	return d
}

// checkDerivedDefaults checks, once every parameter is known, that each
// derived default names another parameter, and that they do not go round.
func (ps *parser) checkDerivedDefaults(params []*Param, nodes map[string]*yaml.Node) {
	by := map[string]*Param{}
	for _, p := range params {
		by[p.Name] = p
	}
	for _, p := range params {
		if p.DefaultFrom == nil {
			continue
		}
		at := "params." + p.Name + ".default"
		src := by[p.DefaultFrom.Param]
		switch {
		case src == nil:
			ps.add(nodes[p.Name], at+".param", "the default is taken from the parameter %q, which this template does not have%s", clipText(p.DefaultFrom.Param, 60), paramHint(params))
		case src == p:
			ps.add(nodes[p.Name], at+".param", "a parameter's default cannot be taken from the parameter itself")
		case src.Type != paramTypeString && src.Type != paramTypeEnvironment && src.Type != paramTypeApplication:
			ps.add(nodes[p.Name], at+".param", "%s is a %s; a default can be derived from a string parameter", src.Name, src.Type)
		case p.Type != paramTypeString:
			ps.add(nodes[p.Name], at+".param", "%s is a %s; only a string parameter has a derived default", p.Name, p.Type)
		default:
			seen := map[string]bool{p.Name: true}
			for cur := src; cur != nil && cur.DefaultFrom != nil; cur = by[cur.DefaultFrom.Param] {
				if seen[cur.Name] {
					ps.add(nodes[p.Name], at+".param", "the defaults of %s and %s are taken from each other: one of them needs a default of its own", p.Name, cur.Name)
					break
				}
				seen[cur.Name] = true
			}
		}
	}
}

func paramHint(params []*Param) string {
	if n := paramNames(params); n != "" {
		return "; it has " + n
	}
	return ""
}

// derive fills in the derived defaults that no value was given for, once the
// values they derive from are known. It returns what is wrong, in words.
func derive(params []*Param, values map[string]string) []string {
	var problems []string
	for range params { // a chain is at most as long as the list
		for _, p := range params {
			if _, set := values[p.Name]; set || p.DefaultFrom == nil {
				continue
			}
			from, ok := values[p.DefaultFrom.Param]
			if !ok {
				continue
			}
			v := from
			if p.DefaultFrom.Format != "" {
				v = strings.ReplaceAll(p.DefaultFrom.Format, "%s", from)
			}
			if msg := p.check(v); msg != "" {
				problems = append(problems, fmt.Sprintf("the default of %s, taken from %s (%q), does not fit: %s; give %s a value under with:", p.Name, p.DefaultFrom.Param, clipText(from, 60), msg, p.Name))
				values[p.Name] = v // reported once
				continue
			}
			values[p.Name] = v
		}
	}
	return problems
}

// entry reads a list entry of a template that may carry if. It returns the
// entry without the if (which the entry's own reader would not know), and
// whether the entry stays. Outside a template an if is left for the entry's
// reader to refuse.
func (ps *parser) entry(n *yaml.Node, at string) (*yaml.Node, bool) {
	if ps.tmpl == nil || n.Kind != yaml.MappingNode || !hasKey(n, "if") {
		return n, true
	}
	cp := *n
	cp.Content = nil
	keep := true
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "if" {
			if t := ps.parseIf(n.Content[i+1], at+".if"); t != nil {
				keep = t.keep
			}
			continue
		}
		cp.Content = append(cp.Content, n.Content[i], n.Content[i+1])
	}
	return &cp, keep
}
