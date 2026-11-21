package pipeline

// Variables at levels, as a pipeline meets them. A pipeline never reads the
// levels; it is handed the variables in effect for a project (the settings
// package resolved them with the one precedence rule), and an input or a
// template parameter takes one by name:
//
//	inputs:
//	  region: { type: string, from: region }        # a variable is its default
//	params:
//	  registry: { type: string, from: registry }    # in a template
//
// The binding is explicit: nothing reaches a step just because a variable
// with its name exists. The run records the variables it used (see the run
// page), and its inputs, which hold their values, so a re-run repeats it.

import (
	"encoding/json"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// variableNameRe is a variable's name, as the settings package keeps it.
var variableNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// ValidVariableName reports whether s can name a variable.
func ValidVariableName(s string) bool { return variableNameRe.MatchString(s) }

// BoundVariable is one variable in effect, as a pipeline takes it.
type BoundVariable struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Text is the value as an input or a parameter takes it: text as it is,
	// a list as a JSON array, a number in its shortest form. Empty for a
	// secret, which is a reference a runner follows and never an input.
	Text string `json:"text"`
	// Display is the value in the words people read; a secret shows its
	// reference.
	Display string `json:"display"`
	// Source is where it comes from, in a sentence: "project cart-api".
	Source string `json:"source"`
	// Locked says that a lock above stops any nearer value, a run's or a
	// file's included; LockedBy and Who say by what level and for whom to
	// ask.
	Locked   bool   `json:"locked,omitempty"`
	LockedBy string `json:"locked_by,omitempty"`
	Who      string `json:"who,omitempty"`
	// Detail is everything the resolution knew (the chain behind the value),
	// kept by the run as recorded, and opaque here.
	Detail json.RawMessage `json:"detail,omitempty"`
}

// Variables are the variables in effect for a project, by name.
type Variables map[string]BoundVariable

// LockedMessage is the plain sentence for a value a lock refuses. what says
// who gave it: "this run asked for" or "the pipeline file sets".
func LockedMessage(name string, v BoundVariable, what, got string) string {
	return fmt.Sprintf("%s is locked to %s by %s; %s %s. Only %s can change it.", name, v.Display, v.LockedBy, what, got, v.Who)
}

// Words for who gave a value, in LockedMessage.
const (
	AskedByRun = "this run asked for"
	SetByFile  = "the pipeline file sets"
)

// varBinding is what a template's expansion takes variables through, and
// what it used.
type varBinding struct {
	vars Variables
	used map[string]BoundVariable
}

func (b *varBinding) lookup(name string) (BoundVariable, bool) {
	if b == nil {
		return BoundVariable{}, false
	}
	v, ok := b.vars[name]
	if ok {
		if b.used == nil {
			b.used = map[string]BoundVariable{}
		}
		b.used[name] = v
	}
	return v, ok
}

// Used returns the variables that were taken, by name.
func (b *varBinding) Used() []BoundVariable {
	if b == nil {
		return nil
	}
	out := make([]BoundVariable, 0, len(b.used))
	for _, v := range b.used {
		out = append(out, v)
	}
	sortBound(out)
	return out
}

func sortBound(v []BoundVariable) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j].Name < v[j-1].Name; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// bindableTypes are the variable types an input or a parameter may take.
// A map is a structure and a secret is a reference; neither is a value a
// step reads as an input.
func bindable(typ string) bool {
	switch typ {
	case "string", "number", "boolean", "list", "connection":
		return true
	}
	return false
}

// takeVariable returns a variable's text for a parameter of the given type,
// or why it does not fit.
func takeVariable(v BoundVariable, what, paramType string) (string, string) {
	if !bindable(v.Type) {
		return "", fmt.Sprintf("the variable %s is a %s, which %s cannot take (a %s is used where a step names it)", v.Name, v.Type, what, v.Type)
	}
	want := paramType
	if v.Type == "connection" {
		v.Type = "string"
	}
	if want == InputChoice {
		want = InputString
	}
	if want != v.Type {
		return "", fmt.Sprintf("the variable %s is a %s; %s is a %s", v.Name, v.Type, what, paramType)
	}
	return v.Text, ""
}

// noValueHint adds to "needs a value" where a variable could give it.
func noValueHint(from string) string {
	if from == "" {
		return ""
	}
	return fmt.Sprintf("; or set the variable %s for this project, its unit or its organization (Settings, Variables)", from)
}

// ExpandWith is Expand with the variables in effect for the project: a
// template parameter that names one (from:) takes it when a use gives no
// value. It also returns the variables it took.
func ExpandWith(p *Pipeline, load TemplateLoader, vars Variables) (*Pipeline, []BoundVariable, error) {
	vb := &varBinding{vars: vars}
	out, err := expandBound(p, load, vb)
	return out, vb.Used(), err
}

// variableName reads the name of a variable after from:.
func (ps *parser) variableName(n *yaml.Node, where string) string {
	name := ps.str(n, where)
	if !variableNameRe.MatchString(name) {
		ps.add(n, where, "%q is not a variable name: lower-case letters, digits and _, starting with a letter", clipText(name, 60))
		return ""
	}
	return name
}

// lockedWith returns the problem with a value a file writes for a parameter
// whose variable is locked, or "".
func (b *varBinding) lockedWith(p *Param, value, written string) string {
	if p.From == "" {
		return ""
	}
	if v, ok := b.lookup(p.From); ok && v.Locked && value != v.Text {
		return LockedMessage(p.From, v, SetByFile, written)
	}
	return ""
}

// param takes a parameter's variable: its text in the parameter's canonical
// form, whether there was one, and why it does not fit.
func (b *varBinding) param(p *Param) (text string, found bool, msg string) {
	if p.From == "" {
		return "", false, ""
	}
	v, ok := b.lookup(p.From)
	if !ok {
		return "", false, ""
	}
	text, msg = takeVariable(v, "the parameter "+p.Name, p.Type)
	if msg == "" {
		msg = p.check(text)
	}
	if msg != "" {
		return "", true, fmt.Sprintf("the variable %s (%s) does not fit the parameter %s: %s", p.From, v.Source, p.Name, msg)
	}
	if p.Type == paramTypeList {
		text = canonList(text)
	}
	return text, true, ""
}
