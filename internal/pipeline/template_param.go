package pipeline

// A template parameter and the check of a value against it.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Param is one parameter a template declares.
type Param struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"` // string, number, boolean, list, environment or application
	Default *string `json:"default,omitempty"`
	// DefaultFrom, instead of Default, takes the default from another
	// parameter (see template_derive.go).
	DefaultFrom *DefaultFrom `json:"default_from,omitempty"`
	Description string       `json:"description,omitempty"`
	Options     []string     `json:"options,omitempty"`
	From        string       `json:"from,omitempty"` // the variable it takes (variables.go)
	// SecretReference says a step takes its secret reference's store, key or
	// field from the parameter; set when templates are described.
	SecretReference bool `json:"secret_reference,omitempty"`
	Line            int  `json:"-"`
}

// check returns why v is not a value of the parameter, or "".
func (p *Param) check(v string) string {
	switch p.Type {
	case paramTypeNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Sprintf("%s is a number; %q is not one", p.Name, v)
		}
	case paramTypeBoolean:
		if v != "true" && v != "false" {
			return fmt.Sprintf("%s is true or false; %q is neither", p.Name, v)
		}
	case paramTypeList:
		if _, msg := ParseList(v); msg != "" {
			return fmt.Sprintf("%s is a list: %s", p.Name, msg)
		}
	case paramTypeEnvironment, paramTypeApplication:
		if msg := p.checkName(v); msg != "" {
			return msg
		}
	default:
		switch {
		case len(v) > MaxParamValueLen:
			return fmt.Sprintf("the value of %s is longer than %d bytes", p.Name, MaxParamValueLen)
		case !utf8.ValidString(v) || strings.ContainsRune(v, 0):
			return fmt.Sprintf("the value of %s is not text", p.Name)
		}
	}
	if len(p.Options) > 0 {
		for _, o := range p.Options {
			if sameValue(p.Type, o, v) {
				return ""
			}
		}
		return fmt.Sprintf("%s must be one of %s; %q is not", p.Name, strings.Join(quoteAll(p.Options), ", "), v)
	}
	return ""
}

// sameValue compares two values of a parameter type: numbers by value,
// everything else as written.
func sameValue(typ, a, b string) bool {
	if typ == paramTypeNumber {
		x, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
		y, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
		return err1 == nil && err2 == nil && x == y
	}
	return a == b
}

func quoteAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = strconv.Quote(s)
	}
	return out
}

// fillParams gives each parameter a use wrote no value for the value of its
// variable, else its default, and reports a parameter that has neither. It
// returns false if any problem was reported.
func fillParams(params []*Param, values, with map[string]string, template string, usesLine int,
	withProblem func(line, col int, path, msg string), vb *varBinding) bool {
	ok := true
	for _, p := range params {
		if _, set := values[p.Name]; set {
			continue
		}
		if text, found, msg := vb.param(p); found {
			if msg != "" {
				ok = false
				withProblem(usesLine, 0, "with."+p.Name, msg)
			} else {
				values[p.Name] = text
			}
			continue
		}
		if p.DefaultFrom != nil {
			continue // filled in below, once what it derives from is known
		}
		if p.Default == nil {
			if _, given := with[p.Name]; !given {
				ok = false
				withProblem(usesLine, 0, "with."+p.Name, fmt.Sprintf("the template %s needs a value for %s, which has no default: add it under with:%s", template, p.Name, noValueHint(p.From)))
			}
			continue
		}
		values[p.Name] = *p.Default
	}
	for _, msg := range derive(params, values) {
		ok = false
		withProblem(usesLine, 0, "with", msg)
	}
	for _, p := range params {
		if _, set := values[p.Name]; !set && p.DefaultFrom != nil {
			ok = false
			withProblem(usesLine, 0, "with."+p.Name, fmt.Sprintf("the default of %s is taken from %s, which has no value here: add one of them under with:", p.Name, strings.Join(p.DefaultFrom.refs(), ", ")))
		}
	}
	return ok
}
