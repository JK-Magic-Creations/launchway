package pipeline

// Run inputs.
//
// A pipeline may declare typed inputs: values a run is started with. A
// person starting a run gives them in a form; a schedule gives them in its
// entry; a webhook maps them from its request's JSON body; every other
// event uses the defaults. The server checks every value against its
// declaration, whoever gave it: the form in the UI is a convenience.
//
// Values travel as text in one canonical form per type, so they compare
// as text, are recorded in the run's creation event as they were used, and
// reach steps as LAUNCHWAY_INPUT_<NAME>:
//
//   - string, choice: the text itself
//   - boolean: true or false
//   - number: the shortest decimal that is the same number (2, 2.5, -0.25)
//   - list: a JSON array of strings, ["3.11","3.12"]
//
// There is still no expression language. An input is used in exactly
// these places: its variable, a step's when: { input: name, equals: value },
// a template's with: { param: { input: name } }, and a loop's list
// (for_each: { key: { input: name } }). Secret values are not inputs: a
// value given here is recorded with the run and shown to anyone who can see
// it.

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Input types.
const (
	InputString  = "string"
	InputChoice  = "choice"
	InputBoolean = "boolean"
	InputNumber  = "number"
	InputList    = "list"
)

// InputTypes lists the input types, in the order they are documented.
var InputTypes = []string{InputString, InputChoice, InputBoolean, InputNumber, InputList}

// Limits on inputs.
const (
	MaxInputs        = 50    // per pipeline
	MaxInputValueLen = 4096  // bytes in a string value, or in one list item
	MaxListItems     = 100   // items in a list value
	MaxListValueLen  = 16384 // bytes in a list value, as JSON
	MaxPatternLen    = 500
	MaxOptions       = 200
	InputEnvPrefix   = "LAUNCHWAY_INPUT_"
)

// Input is one value a run is started with.
type Input struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // one of InputTypes
	Description string `json:"description,omitempty"`
	// Default is the canonical value used when none is given; nil if there
	// is none.
	Default *string `json:"default,omitempty"`
	// Required says that a value must be given when there is no default. A
	// pipeline whose required input has no default cannot be started by a
	// push or a pull request, nor by a schedule or webhook that does not
	// give it.
	Required bool `json:"required,omitempty"`
	// Options are a choice's values, canonical.
	Options []string `json:"options,omitempty"`
	// Pattern is a regular expression (RE2 syntax) a string must match.
	Pattern string `json:"pattern,omitempty"`
	// Min and Max bound a number.
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Line int      `json:"-"`

	pattern *regexp.Regexp
}

// InputEnv is the variable an input reaches steps as.
func InputEnv(name string) string { return InputEnvPrefix + EnvName(name) }

// Input returns the input with the given name, or nil.
func (p *Pipeline) Input(name string) *Input {
	for _, in := range p.Inputs {
		if in.Name == name {
			return in
		}
	}
	return nil
}

// zero is the value of an input that is not required, has no default and
// was not given.
func (in *Input) zero() string {
	switch in.Type {
	case InputBoolean:
		return "false"
	case InputList:
		return "[]"
	}
	return ""
}

// Canon checks a value given for the input and returns it in canonical
// form, or the reason it does not fit. A list may be given as a JSON array
// of strings or as values separated by commas.
func (in *Input) Canon(raw string) (string, string) {
	switch in.Type {
	case InputBoolean:
		switch strings.TrimSpace(raw) {
		case "true":
			return "true", ""
		case "false":
			return "false", ""
		}
		return "", fmt.Sprintf("write true or false, not %q", clipText(raw, 40))
	case InputNumber:
		s := strings.TrimSpace(raw)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || s == "" {
			return "", fmt.Sprintf("%q is not a number", clipText(raw, 40))
		}
		if in.Min != nil && f < *in.Min {
			return "", fmt.Sprintf("must be at least %s", FormatNumber(*in.Min))
		}
		if in.Max != nil && f > *in.Max {
			return "", fmt.Sprintf("must be at most %s", FormatNumber(*in.Max))
		}
		return FormatNumber(f), ""
	case InputList:
		items, msg := ParseList(raw)
		if msg != "" {
			return "", msg
		}
		b, _ := json.Marshal(items)
		return string(b), ""
	}
	if msg := checkText(raw, MaxInputValueLen); msg != "" {
		return "", msg
	}
	switch in.Type {
	case InputChoice:
		if !slices.Contains(in.Options, raw) {
			return "", fmt.Sprintf("choose one of %s", strings.Join(quoteAll(in.Options), ", "))
		}
	default:
		if in.Required && raw == "" {
			return "", "a value is required"
		}
		if in.pattern != nil && raw != "" && !in.pattern.MatchString(raw) {
			return "", fmt.Sprintf("%q does not match the pattern %s", clipText(raw, 60), in.Pattern)
		}
	}
	return raw, ""
}

// FormatNumber writes a number canonically: the shortest decimal that
// reads back as the same number.
func FormatNumber(f float64) string {
	if f == 0 {
		return "0" // not -0
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ParseList reads a list value: a JSON array of strings, or values
// separated by commas (spaces around each are dropped). Nothing at all is
// the empty list.
func ParseList(raw string) ([]string, string) {
	s := strings.TrimSpace(raw)
	var items []string
	switch {
	case s == "":
		items = []string{}
	case strings.HasPrefix(s, "["):
		if err := json.Unmarshal([]byte(s), &items); err != nil {
			return nil, "a list is a JSON array of strings, such as [\"a\",\"b\"], or values separated by commas"
		}
		if items == nil {
			items = []string{}
		}
	default:
		for _, part := range strings.Split(s, ",") {
			items = append(items, strings.TrimSpace(part))
		}
	}
	if len(items) > MaxListItems {
		return nil, fmt.Sprintf("a list has at most %d items", MaxListItems)
	}
	size := 2
	for _, it := range items {
		if msg := checkText(it, MaxInputValueLen); msg != "" {
			return nil, "an item: " + msg
		}
		size += len(it) + 3
	}
	if size > MaxListValueLen {
		return nil, fmt.Sprintf("the list is longer than %d bytes", MaxListValueLen)
	}
	return items, ""
}

// ListItems reads a canonical list value. It returns nil for a value that
// is not one.
func ListItems(canon string) []string {
	var items []string
	if json.Unmarshal([]byte(canon), &items) != nil {
		return nil
	}
	return items
}

func checkText(v string, limit int) string {
	switch {
	case len(v) > limit:
		return fmt.Sprintf("the value is longer than %d bytes", limit)
	case !utf8.ValidString(v) || strings.ContainsRune(v, 0):
		return "the value is not text"
	}
	return ""
}

// InputErrors says what is wrong with the values given for a run's inputs:
// input name → why. A value given for an input the pipeline does not
// declare is reported under its own name.
type InputErrors map[string]string

func (e InputErrors) Error() string {
	names := make([]string, 0, len(e))
	for n := range e {
		names = append(names, n)
	}
	slices.Sort(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + ": " + e[n]
	}
	return "the run's inputs have problems: " + strings.Join(parts, "; ")
}

// ResolveInputs checks the values given for a run of the pipeline and
// completes them with defaults: every declared input gets a canonical
// value. It refuses a value for an input the pipeline does not declare, a
// value that does not fit, and a missing value for a required input that
// has no default. The error is InputErrors.
func ResolveInputs(p *Pipeline, given map[string]string) (map[string]string, error) {
	errs := InputErrors{}
	out := make(map[string]string, len(p.Inputs))
	for name := range given {
		if p.Input(name) == nil {
			if len(p.Inputs) == 0 {
				errs[clipText(name, 60)] = "the pipeline declares no inputs"
			} else {
				errs[clipText(name, 60)] = "the pipeline has no input called that; it has " + inputNames(p)
			}
		}
	}
	for _, in := range p.Inputs {
		in.compile()
		raw, ok := given[in.Name]
		switch {
		case ok:
			v, msg := in.Canon(raw)
			if msg != "" {
				errs[in.Name] = msg
				continue
			}
			out[in.Name] = v
		case in.Default != nil:
			out[in.Name] = *in.Default
		case in.Required:
			errs[in.Name] = "a value is required"
		default:
			out[in.Name] = in.zero()
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

// CheckValues checks that values are exactly the pipeline's inputs, each in
// canonical form: what ResolveInputs returns. The engine checks this before
// it records a run.
func CheckValues(p *Pipeline, values map[string]string) error {
	if len(values) != len(p.Inputs) {
		return fmt.Errorf("the pipeline declares %d inputs; %d values were given", len(p.Inputs), len(values))
	}
	for _, in := range p.Inputs {
		v, ok := values[in.Name]
		if !ok {
			return fmt.Errorf("no value for the input %s", in.Name)
		}
		if v == in.zero() && !in.Required {
			continue
		}
		in.compile()
		canon, msg := in.Canon(v)
		if msg != "" {
			return fmt.Errorf("the input %s: %s", in.Name, msg)
		}
		if canon != v {
			return fmt.Errorf("the input %s is not in canonical form", in.Name)
		}
	}
	return nil
}

// compile prepares the pattern of an input read from JSON, where it is
// kept as text.
func (in *Input) compile() {
	if in.Pattern != "" && in.pattern == nil {
		in.pattern, _ = regexp.Compile(in.Pattern)
	}
}

func inputNames(p *Pipeline) string {
	names := make([]string, len(p.Inputs))
	for i, in := range p.Inputs {
		names[i] = in.Name
	}
	return strings.Join(names, ", ")
}

// ---- reading inputs ----

var inputKeys = []string{"type", "description", "default", "required", "options", "pattern", "min", "max", "options_from"}

// parseInputs reads inputs: { name: { type, description, default, required,
// options, pattern, min, max } }.
func (ps *parser) parseInputs(n *yaml.Node, p *Pipeline) {
	if n.Kind != yaml.MappingNode {
		ps.add(n, "inputs", "inputs is a mapping of names to inputs, for example: version: { type: string, required: true }")
		return
	}
	if len(n.Content)/2 > MaxInputs {
		ps.add(n, "inputs", "a pipeline may have at most %d inputs", MaxInputs)
		return
	}
	seen, vars := map[string]bool{}, map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		at := "inputs." + k.Value
		if seen[k.Value] {
			ps.add(k, at, "input %q is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		if !paramNameRe.MatchString(k.Value) {
			ps.add(k, at, "%q is not a valid input name: use lower-case letters, digits, - and _, starting with a letter (at most 40)", k.Value)
		} else if other, ok := vars[EnvName(k.Value)]; ok {
			ps.add(k, at, "%s and %s would both be the variable %s; rename one", other, k.Value, InputEnv(k.Value))
		} else {
			vars[EnvName(k.Value)] = k.Value
		}
		in := &Input{Name: k.Value, Type: InputString, Line: k.Line}
		nodes := map[string]*yaml.Node{}
		ps.eachKey(v, at, inputKeys, func(k2, v2 *yaml.Node) {
			nodes[k2.Value] = v2
			where := at + "." + k2.Value
			switch k2.Value {
			case "type":
				in.Type = ps.str(v2, where)
				if !slices.Contains(InputTypes, in.Type) {
					ps.add(v2, where, "the type is %s", strings.Join(InputTypes, ", "))
					in.Type = InputString
				}
			case "description":
				in.Description = strings.TrimSpace(ps.str(v2, where))
				if strings.ContainsAny(in.Description, "\r\n") || utf8.RuneCountInString(in.Description) > MaxDescriptionLen {
					ps.add(v2, where, "a description is one line of at most %d characters", MaxDescriptionLen)
				}
			case "required":
				in.Required = ps.bool(v2, where)
			case "pattern":
				in.Pattern = ps.str(v2, where)
			case "options_from":
				ps.add(k2, where, "options_from is not supported yet: list the options under options")
			}
		})
		if v.Kind != yaml.MappingNode {
			p.Inputs = append(p.Inputs, in)
			continue
		}
		ps.checkInput(in, at, k, nodes)
		p.Inputs = append(p.Inputs, in)
	}
}

// checkInput checks the settings of one input against its type, and its
// default against everything else.
func (ps *parser) checkInput(in *Input, at string, k *yaml.Node, nodes map[string]*yaml.Node) {
	only := func(key string, types ...string) bool {
		n := nodes[key]
		if n != nil && !slices.Contains(types, in.Type) {
			ps.add(n, at+"."+key, "%s is for %s inputs; %s is a %s", key, strings.Join(types, " and "), in.Name, in.Type)
			return false
		}
		return n != nil
	}
	if only("options", InputChoice) {
		n := nodes["options"]
		opts := ps.strList(n, at+".options")
		switch {
		case len(opts) == 0:
			ps.add(n, at+".options", "list at least one option")
		case len(opts) > MaxOptions:
			ps.add(n, at+".options", "at most %d options", MaxOptions)
		}
		for i, o := range opts {
			if msg := checkText(o, MaxInputValueLen); msg != "" {
				ps.add(n, at+".options", "an option: %s", msg)
			}
			if slices.Contains(opts[:i], o) {
				ps.add(n, at+".options", "%q is listed twice", clipText(o, 40))
			}
		}
		in.Options = opts
	} else if in.Type == InputChoice {
		ps.add(k, at+".options", "a choice lists its options, for example: options: [dev, prod]")
	}
	if only("pattern", InputString) {
		n := nodes["pattern"]
		switch re, err := regexp.Compile(in.Pattern); {
		case len(in.Pattern) > MaxPatternLen:
			ps.add(n, at+".pattern", "the pattern is longer than %d characters", MaxPatternLen)
		case err != nil:
			ps.add(n, at+".pattern", "not a regular expression: %s", strings.TrimPrefix(err.Error(), "error parsing regexp: "))
		default:
			in.pattern = re
		}
	}
	for _, key := range []string{"min", "max"} {
		if !only(key, InputNumber) {
			continue
		}
		n := nodes[key]
		f, err := strconv.ParseFloat(ps.str(n, at+"."+key), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			ps.add(n, at+"."+key, "write a number")
			continue
		}
		if key == "min" {
			in.Min = &f
		} else {
			in.Max = &f
		}
	}
	if in.Min != nil && in.Max != nil && *in.Min > *in.Max {
		ps.add(nodes["max"], at+".max", "max is less than min")
	}
	if n := nodes["default"]; n != nil {
		raw, ok := ps.inputLiteral(n, at+".default", in.Type)
		if ok {
			req := in.Required
			in.Required = false // a default may be empty; required only asks for a value
			v, msg := in.Canon(raw)
			in.Required = req
			if msg != "" {
				ps.add(n, at+".default", "the default does not fit: %s", msg)
			} else {
				in.Default = &v
			}
		}
	}
}

// inputLiteral reads a value written in a pipeline for an input of the
// given type: a single value, or for a list a YAML list of values.
func (ps *parser) inputLiteral(n *yaml.Node, at, typ string) (string, bool) {
	if typ == InputList && n.Kind == yaml.SequenceNode {
		items := []string{}
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				ps.add(c, at, "a list holds single values")
				return "", false
			}
			items = append(items, c.Value)
		}
		b, _ := json.Marshal(items)
		return string(b), true
	}
	if n.Kind != yaml.ScalarNode {
		if typ == InputList {
			ps.add(n, at, "write a list, such as [a, b]")
		} else {
			ps.add(n, at, "expected a single value")
		}
		return "", false
	}
	return n.Value, true
}

// ---- when: { input: name, equals: value } ----

// parseInputTest reads the input test of a when condition. The input is
// checked once every input is read (checkInputTests).
func (ps *parser) parseInputTest(c *Condition, k, v *yaml.Node, at string) {
	switch k.Value {
	case "input":
		if ps.tmpl != nil {
			ps.add(k, at+".input", "a template cannot test run inputs; pass the input to a parameter (with: { name: { input: … } }) and use if: { param: … }")
			return
		}
		c.Input = ps.str(v, at+".input")
		c.inputLine = v.Line
		if c.Input == "" {
			ps.add(v, at+".input", "name an input")
		}
	case "output":
		c.Output = strings.TrimSpace(ps.str(v, at+".output"))
		c.inputLine = v.Line
		if c.Output == "" {
			ps.add(v, at+".output", "name a step and its output's key, for example: scan.changed")
		}
	case "equals":
		if v.Kind == yaml.SequenceNode {
			c.Equals = ps.strList(v, at+".equals")
			if len(c.Equals) == 0 {
				ps.add(v, at+".equals", "list at least one value")
			}
		} else {
			c.Equals = []string{ps.str(v, at+".equals")}
		}
		if c.inputLine == 0 {
			c.inputLine = v.Line
		}
	}
}

// checkInputTests checks the when conditions that test inputs, now that
// every input is known, and writes their values in canonical form.
func (ps *parser) checkInputTests(p *Pipeline) {
	for _, s := range p.Steps {
		c := s.When
		if c == nil || (c.Input == "" && c.Equals == nil) || (c.Input == "" && c.Output != "") {
			continue // an output test is checked by checkOutputTests
		}
		at := "steps." + s.ID + ".when"
		line := c.inputLine
		if c.Input == "" {
			ps.problems = append(ps.problems, Problem{Line: line, Path: at + ".equals", Message: "equals goes with input: when: { input: name, equals: value }"})
			continue
		}
		in := p.Input(c.Input)
		if in == nil {
			msg := fmt.Sprintf("there is no input called %q", clipText(c.Input, 60))
			if len(p.Inputs) > 0 {
				msg += "; the pipeline has " + inputNames(p)
			} else {
				msg += ": declare it under inputs:"
			}
			ps.problems = append(ps.problems, Problem{Line: line, Path: at + ".input", Message: msg})
			continue
		}
		if c.Equals == nil {
			if in.Type != InputBoolean {
				ps.problems = append(ps.problems, Problem{Line: line, Path: at, Message: fmt.Sprintf(
					"%s is a %s, not true or false: say which values run the step, with equals:", in.Name, in.Type)})
				continue
			}
			c.Equals = []string{"true"}
			continue
		}
		if in.Type == InputList {
			ps.problems = append(ps.problems, Problem{Line: line, Path: at + ".input", Message: in.Name + " is a list; a when test compares single values"})
			continue
		}
		for i, raw := range c.Equals {
			req := in.Required
			in.Required = false
			v, msg := in.Canon(raw)
			in.Required = req
			if msg != "" {
				ps.problems = append(ps.problems, Problem{Line: line, Path: at + ".equals", Message: in.Name + ": " + msg})
				continue
			}
			c.Equals[i] = v
		}
	}
}

// checkTriggeredInputs checks that the events which start the pipeline
// without a person have a value for every input: a required input with no
// default cannot be used by pushes and pull requests, and a schedule or
// webhook must give it.
func (ps *parser) checkTriggeredInputs(p *Pipeline) {
	var by []string
	if p.On.Push != nil {
		by = append(by, "pushes")
	}
	if p.On.PullRequest != nil {
		by = append(by, "pull requests")
	}
	for _, in := range p.Inputs {
		if !in.Required || in.Default != nil {
			continue
		}
		if len(by) > 0 {
			ps.problems = append(ps.problems, Problem{Line: in.Line, Path: "inputs." + in.Name, Message: fmt.Sprintf(
				"%s is required and has no default, but %s start this pipeline with nobody to give it: give it a default, or do not make it required",
				in.Name, strings.Join(by, " and "))})
		}
		for i, sc := range p.On.Schedule {
			if _, ok := sc.Inputs[in.Name]; !ok {
				ps.problems = append(ps.problems, Problem{Line: sc.Line, Path: fmt.Sprintf("on.schedule[%d].inputs", i), Message: fmt.Sprintf(
					"this schedule gives no value for %s, which is required and has no default: add it under inputs:", in.Name)})
			}
		}
		for _, w := range p.On.Webhook {
			if _, ok := w.Inputs[in.Name]; !ok {
				ps.problems = append(ps.problems, Problem{Line: w.Line, Path: "on.webhook." + w.Name + ".inputs", Message: fmt.Sprintf(
					"this webhook maps nothing to %s, which is required and has no default: add it under inputs:", in.Name)})
			}
		}
	}
}

// UsesSchema13 reports whether the pipeline uses keys that servers older
// than schema 13 do not know: inputs, input tests, loops, schedules and
// webhooks.
func (p *Pipeline) UsesSchema13() bool {
	if len(p.Inputs) > 0 || len(p.Loops) > 0 || len(p.On.Schedule) > 0 || len(p.On.Webhook) > 0 {
		return true
	}
	for _, s := range p.Steps {
		if s.ForEach != nil || len(s.Each) > 0 || (s.When != nil && (s.When.Input != "" || len(s.When.Equals) > 0 || s.When.Upstream != "" || s.When.Output != "")) {
			return true
		}
		if s.When != nil && (slices.Contains(s.When.Event, EventSchedule) || slices.Contains(s.When.Event, EventWebhook)) {
			return true
		}
	}
	return false
}
