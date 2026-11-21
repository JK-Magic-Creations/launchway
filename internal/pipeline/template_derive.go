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

// DefaultFrom says a parameter's default is worked out from other
// parameters: another parameter's value in a format with %s, or a text with
// {name} where another parameter's value goes, optionally replaced by an
// entry of a list parameter that names the parameter ("prod.application=x").
type DefaultFrom struct {
	Param  string `json:"param,omitempty"`
	Format string `json:"format,omitempty"`
	Text   string `json:"text,omitempty"`
	// NonEmpty: true when the list parameter named has something in it.
	NonEmpty string    `json:"nonempty,omitempty"`
	Override *Override `json:"override,omitempty"`
}

// Override takes a value from a list parameter whose entries are written
// key=value: the entry whose key is Key (with {name} filled in) wins over the
// derived text.
type Override struct {
	Param string `json:"param"`
	Key   string `json:"key"`
}

// placeholders lists the names written as {name} in a text.
func placeholders(text string) []string {
	var out []string
	for {
		i := strings.IndexByte(text, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(text[i:], '}')
		if j < 0 {
			return out
		}
		out = append(out, text[i+1:i+j])
		text = text[i+j+1:]
	}
}

// refs are the parameters a derived default reads.
func (d *DefaultFrom) refs() []string {
	var out []string
	if d.Param != "" {
		out = append(out, d.Param)
	}
	if d.NonEmpty != "" {
		out = append(out, d.NonEmpty)
	}
	out = append(out, placeholders(d.Text)...)
	if d.Override != nil {
		out = append(out, d.Override.Param)
		out = append(out, placeholders(d.Override.Key)...)
	}
	return out
}

// parseDerivedDefault reads default: { param: name, format: "..." } or
// default: { text: "apps/{service}/{environment}.yaml", override: { param: overrides, key: "{environment}.manifest" } }.
func (ps *parser) parseDerivedDefault(n *yaml.Node, at string) *DefaultFrom {
	d := &DefaultFrom{}
	ps.eachKey(n, at, []string{"param", "format", "text", "nonempty", "override"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "param":
			d.Param = ps.str(v, at+".param")
		case "format":
			d.Format = ps.str(v, at+".format")
		case "text":
			d.Text = ps.str(v, at+".text")
		case "nonempty":
			d.NonEmpty = ps.str(v, at+".nonempty")
		case "override":
			o := &Override{}
			ps.eachKey(v, at+".override", []string{"param", "key"}, func(k2, v2 *yaml.Node) {
				if k2.Value == "param" {
					o.Param = ps.str(v2, at+".override.param")
				} else {
					o.Key = ps.str(v2, at+".override.key")
				}
			})
			if o.Param == "" || o.Key == "" {
				ps.add(v, at+".override", "write override: { param: <a list parameter>, key: \"{environment}.name\" }")
			}
			d.Override = o
		}
	})
	switch {
	case d.Param == "" && d.Text == "" && d.NonEmpty == "":
		ps.add(n, at, "a derived default names the parameter it comes from (param) or gives a text with {name} in it (text)")
	case (d.Param != "" && d.Text != "") || (d.NonEmpty != "" && (d.Param != "" || d.Text != "")):
		ps.add(n, at, "use param with format, or text, or nonempty: one of them")
	}
	if d.Format != "" && (strings.Count(d.Format, "%s") < 1 || strings.Count(d.Format, "%") != strings.Count(d.Format, "%s")) {
		ps.add(n, at+".format", "a format has %%s where the other parameter's value goes (once or more), and no other %%")
	}
	return d
}

// checkDerivedDefaults checks, once every parameter is known, that each
// derived default names parameters this template has, and that they do not
// go round.
func (ps *parser) checkDerivedDefaults(params []*Param, nodes map[string]*yaml.Node) {
	by := map[string]*Param{}
	for _, p := range params {
		by[p.Name] = p
	}
	scalar := func(t string) bool {
		return t == paramTypeString || t == paramTypeEnvironment || t == paramTypeApplication || t == paramTypeNumber || t == paramTypeBoolean
	}
	for _, p := range params {
		if p.DefaultFrom == nil {
			continue
		}
		at := "params." + p.Name + ".default"
		bad := false
		for _, name := range p.DefaultFrom.refs() {
			src := by[name]
			switch {
			case src == nil:
				ps.add(nodes[p.Name], at, "the default is taken from the parameter %q, which this template does not have%s", clipText(name, 60), paramHint(params))
				bad = true
			case src == p:
				ps.add(nodes[p.Name], at, "a parameter's default cannot be taken from the parameter itself")
				bad = true
			case p.DefaultFrom.Override != nil && name == p.DefaultFrom.Override.Param:
				if src.Type != paramTypeList {
					ps.add(nodes[p.Name], at+".override.param", "%s is a %s; an override is taken from a list parameter", src.Name, src.Type)
					bad = true
				}
			case name == p.DefaultFrom.NonEmpty:
				if src.Type != paramTypeList {
					ps.add(nodes[p.Name], at+".nonempty", "%s is a %s; nonempty: takes a list parameter", src.Name, src.Type)
					bad = true
				}
			case !scalar(src.Type) || src.Type == paramTypeBoolean:
				ps.add(nodes[p.Name], at, "%s is a %s; a default can be derived from a string, number, environment or application parameter", src.Name, src.Type)
				bad = true
			}
		}
		if !scalar(p.Type) {
			ps.add(nodes[p.Name], at, "%s is a %s; only a string, number, environment or application parameter has a derived default", p.Name, p.Type)
			bad = true
		}
		if bad {
			continue
		}
		// A chain of derivations must not go round.
		var walk func(name string, seen map[string]bool) string
		walk = func(name string, seen map[string]bool) string {
			if seen[name] {
				return name
			}
			seen[name] = true
			if q := by[name]; q != nil && q.DefaultFrom != nil {
				for _, r := range q.DefaultFrom.refs() {
					if c := walk(r, seen); c != "" {
						return c
					}
				}
			}
			delete(seen, name)
			return ""
		}
		if c := walk(p.Name, map[string]bool{}); c != "" {
			ps.add(nodes[p.Name], at, "the defaults of %s and %s are taken from each other: one of them needs a default of its own", p.Name, c)
		}
	}
}

func paramHint(params []*Param) string {
	if n := paramNames(params); n != "" {
		return "; it has " + n
	}
	return ""
}

// fill puts the values of parameters into a text written with {name}.
func fill(text string, values map[string]string) (string, bool) {
	for _, name := range placeholders(text) {
		v, ok := values[name]
		if !ok {
			return "", false
		}
		text = strings.ReplaceAll(text, "{"+name+"}", v)
	}
	return text, true
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
			d := p.DefaultFrom
			var v, why string
			if d.NonEmpty != "" {
				list, ok := values[d.NonEmpty]
				if !ok {
					continue
				}
				items, _ := ParseList(list)
				v, why = fmt.Sprint(len(items) > 0), d.NonEmpty
			} else if d.Text != "" {
				t, ok := fill(d.Text, values)
				if !ok {
					continue
				}
				v, why = t, d.Text
			} else {
				from, ok := values[d.Param]
				if !ok {
					continue
				}
				v, why = from, d.Param
				if d.Format != "" {
					v = strings.ReplaceAll(d.Format, "%s", from)
				}
			}
			if d.Override != nil {
				list, lok := values[d.Override.Param]
				key, kok := fill(d.Override.Key, values)
				if !lok || !kok {
					continue
				}
				entries, _ := ParseList(list)
				for _, e := range entries {
					if k, val, found := strings.Cut(e, "="); found && strings.TrimSpace(k) == key {
						v, why = strings.TrimSpace(val), d.Override.Param+" "+key
					}
				}
			}
			if msg := p.check(v); msg != "" {
				problems = append(problems, fmt.Sprintf("the default of %s, taken from %s (%q), does not fit: %s; give %s a value under with:", p.Name, why, clipText(v, 60), msg, p.Name))
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
