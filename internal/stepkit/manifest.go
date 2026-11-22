package stepkit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Types of an input or an output.
const (
	TypeString  = "string"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
	TypeList    = "list"   // of strings
	TypeRecord  = "record" // a JSON object
	TypeSecret  = "secret" // by reference as an input; masked as an output
)

// Types lists them in the order they are documented.
var Types = []string{TypeString, TypeNumber, TypeBoolean, TypeList, TypeRecord, TypeSecret}

// Limits on a manifest.
const (
	MaxInputs      = 40
	MaxOutputs     = 40
	MaxHosts       = 20
	MaxWritable    = 20
	MaxOptions     = 100
	MaxDescription = 300
)

var (
	// StepNameRe names a step and its directory.
	StepNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	fieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	hostRe      = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*(:[0-9]{1,5})?$`)
	imageRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,254}$`)
	quantityRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(m|Ki|Mi|Gi|Ti)?$`)
	durationRe  = regexp.MustCompile(`^[0-9]+(s|m|h)$`)
)

// Input is one value a step takes.
type Input struct {
	Name        string
	Type        string
	Description string
	Required    bool     // no default and not optional
	Default     *string  // canonical text of the default
	Options     []string // one of these (strings and numbers)
	Pattern     string   // a string must match it (RE2, anchored by the writer)
	Min, Max    *float64 // numbers; for strings and lists the least and most length
	Line        int
	pattern     *regexp.Regexp
}

// Output is one value a step returns.
type Output struct {
	Name        string
	Type        string
	Description string
	Line        int
}

// Permissions are what a step asks to be allowed. It is nothing unless it asks.
type Permissions struct {
	// Network lists the hosts (name, *.suffix, optionally :port) the step may
	// reach. Empty: no network at all.
	Network []string
	// Write lists the workspace paths the step may write; the rest of the
	// workspace is read-only to it. "." is the whole workspace.
	Write []string
}

// Manifest is a step.yaml.
type Manifest struct {
	Name        string
	Description string
	Contract    int
	Image       string
	Toolchain   string
	Command     []string // as written: one shell line, or an argument list
	Shell       bool     // Command is one line for sh
	Inputs      []*Input
	Outputs     []*Output
	CPU, Memory string
	Timeout     string
	Permissions Permissions
	Digest      string // sha256 of the manifest text
	Source      string // the text, as read
}

// Problem is something wrong in a manifest, at a line.
type Problem struct {
	Line    int
	Path    string
	Message string
}

func (p Problem) Error() string {
	if p.Line > 0 {
		return fmt.Sprintf("line %d: %s", p.Line, p.Message)
	}
	return p.Message
}

// Problems is a list of them; it is an error when not empty.
type Problems []Problem

func (ps Problems) Error() string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Error())
	}
	return b.String()
}

// Toolchains maps a toolchain name to an image.
var Toolchains = map[string]string{
	"go":     "docker.io/library/golang:%s",
	"python": "docker.io/library/python:%s-slim",
	"node":   "docker.io/library/node:%s-slim",
	"sh":     "docker.io/library/alpine:%s",
}

var toolchainDefault = map[string]string{"go": "1.25", "python": "3.13", "node": "22", "sh": "3.22"}

type reader struct {
	ps Problems
}

func (r *reader) add(n *yaml.Node, path, format string, a ...any) {
	line := 0
	if n != nil {
		line = n.Line
	}
	r.ps = append(r.ps, Problem{Line: line, Path: path, Message: fmt.Sprintf(format, a...)})
}

// keys walks a mapping, refusing unknown and repeated keys.
func (r *reader) keys(n *yaml.Node, path string, allowed []string, f func(k, v *yaml.Node)) {
	if n.Kind != yaml.MappingNode {
		r.add(n, path, "expected a mapping here")
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			r.add(k, path+"."+k.Value, "%s is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		ok := false
		for _, a := range allowed {
			if a == k.Value {
				ok = true
			}
		}
		if !ok {
			r.add(k, path+"."+k.Value, "unknown key %q here; the keys are %s%s", clip(k.Value, 40), strings.Join(allowed, ", "), nearest(k.Value, allowed))
			continue
		}
		f(k, v)
	}
}

func nearest(got string, among []string) string {
	best, bestD := "", 3
	for _, a := range among {
		if d := editDistance(got, a); d < bestD {
			best, bestD = a, d
		}
	}
	if best == "" {
		return ""
	}
	return " (did you mean " + best + "?)"
}

func editDistance(a, b string) int {
	if len(a) > 64 || len(b) > 64 {
		return 99
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

func (r *reader) str(n *yaml.Node, path string) string {
	if n.Kind != yaml.ScalarNode {
		r.add(n, path, "expected text here")
		return ""
	}
	if !utf8.ValidString(n.Value) || strings.ContainsRune(n.Value, 0) {
		r.add(n, path, "this is not text")
		return ""
	}
	return n.Value
}

func (r *reader) strList(n *yaml.Node, path string, max int) []string {
	if n.Kind != yaml.SequenceNode {
		r.add(n, path, "expected a list here")
		return nil
	}
	if len(n.Content) > max {
		r.add(n, path, "at most %d entries are allowed here", max)
		return nil
	}
	var out []string
	for i, c := range n.Content {
		out = append(out, r.str(c, fmt.Sprintf("%s[%d]", path, i)))
	}
	return out
}

// ParseManifest reads a step.yaml. The text is untrusted: its size, the
// number of its parts and every value are bounded, and nothing in it is run.
func ParseManifest(data []byte) (*Manifest, Problems) {
	r := &reader{}
	if len(data) > MaxManifestBytes {
		return nil, Problems{{Message: fmt.Sprintf("step.yaml is larger than %d KiB", MaxManifestBytes>>10)}}
	}
	if !utf8.Valid(data) {
		return nil, Problems{{Message: "step.yaml is not text (UTF-8)"}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Problems{{Line: yamlErrLine(err), Message: "step.yaml is not valid YAML: " + clip(firstLine(err.Error()), 200)}}
	}
	if len(doc.Content) == 0 {
		return nil, Problems{{Message: "step.yaml is empty"}}
	}
	root := doc.Content[0]
	if hasAlias(root, 0) {
		return nil, Problems{{Line: root.Line, Message: "step.yaml may not use YAML anchors or aliases"}}
	}
	sum := sha256.Sum256(data)
	m := &Manifest{Digest: hex.EncodeToString(sum[:]), Source: string(data)}
	var inputsN, outputsN *yaml.Node
	r.keys(root, "", []string{"step", "contract", "description", "image", "toolchain", "command", "inputs", "outputs", "resources", "timeout", "permissions"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "step":
			m.Name = r.str(v, "step")
			if !StepNameRe.MatchString(m.Name) {
				r.add(v, "step", "%q is not a step name: use lower-case letters, digits and - (at most 63), starting with a letter or digit", clip(m.Name, 40))
			}
		case "contract":
			n, err := strconv.Atoi(r.str(v, "contract"))
			if err != nil || n < 1 {
				r.add(v, "contract", "contract is a whole number, the version of the step contract the step was written for (now %d)", ContractVersion)
			}
			m.Contract = n
		case "description":
			m.Description = strings.TrimSpace(r.str(v, "description"))
			if strings.ContainsAny(m.Description, "\r\n") || utf8.RuneCountInString(m.Description) > MaxDescription {
				r.add(v, "description", "a description is one line of at most %d characters", MaxDescription)
			}
		case "image":
			m.Image = r.str(v, "image")
			if !imageRe.MatchString(m.Image) {
				r.add(v, "image", "%q is not an image reference", clip(m.Image, 60))
			}
		case "toolchain":
			m.Toolchain = r.str(v, "toolchain")
		case "command":
			switch v.Kind {
			case yaml.ScalarNode:
				m.Command, m.Shell = []string{r.str(v, "command")}, true
			case yaml.SequenceNode:
				m.Command = r.strList(v, "command", 50)
			default:
				r.add(v, "command", "command is one line for the shell, or a list of arguments")
			}
		case "inputs":
			inputsN = v
		case "outputs":
			outputsN = v
		case "resources":
			r.keys(v, "resources", []string{"cpu", "memory"}, func(k2, v2 *yaml.Node) {
				val := r.str(v2, "resources."+k2.Value)
				if !quantityRe.MatchString(val) {
					r.add(v2, "resources."+k2.Value, "%q is not an amount such as 500m, 2, 512Mi or 2Gi", clip(val, 20))
				}
				if k2.Value == "cpu" {
					m.CPU = val
				} else {
					m.Memory = val
				}
			})
		case "timeout":
			m.Timeout = r.str(v, "timeout")
			if !durationRe.MatchString(m.Timeout) {
				r.add(v, "timeout", "%q is not a duration such as 90s, 10m or 1h", clip(m.Timeout, 20))
			}
		case "permissions":
			r.permissions(v, m)
		}
	})
	if root.Kind != yaml.MappingNode {
		return nil, r.ps
	}
	if m.Name == "" && !hasKeyNode(root, "step") {
		r.add(root, "step", "name the step: step: my-step")
	}
	switch {
	case m.Contract == 0 && !hasKeyNode(root, "contract"):
		r.add(root, "contract", "say which version of the step contract this step is written for: contract: %d", ContractVersion)
	case m.Contract > ContractVersion:
		r.add(root, "contract", "this step is written for contract %d; this Launchway speaks up to %d: update Launchway or lower the step's contract", m.Contract, ContractVersion)
	case m.Contract != 0 && m.Contract < MinContract:
		r.add(root, "contract", "contract %d is no longer supported; the oldest is %d", m.Contract, MinContract)
	}
	r.image(root, m)
	if len(m.Command) == 0 && !hasKeyNode(root, "command") {
		r.add(root, "command", "say what to run: command: python3 main.py")
	}
	if inputsN != nil {
		m.Inputs = r.inputs(inputsN)
	}
	if outputsN != nil {
		m.Outputs = r.outputs(outputsN)
	}
	if len(r.ps) > 0 {
		sort.SliceStable(r.ps, func(i, j int) bool { return r.ps[i].Line < r.ps[j].Line })
		return nil, r.ps
	}
	return m, nil
}

func (r *reader) image(root *yaml.Node, m *Manifest) {
	switch {
	case m.Image != "" && m.Toolchain != "":
		r.add(root, "image", "give image or toolchain, not both")
	case m.Image == "" && m.Toolchain == "":
		r.add(root, "image", "say what the step runs in: image: docker.io/library/python:3.13-slim, or toolchain: python@3.13")
	case m.Toolchain != "":
		name, ver, _ := strings.Cut(m.Toolchain, "@")
		tmpl, ok := Toolchains[name]
		if !ok {
			names := make([]string, 0, len(Toolchains))
			for n := range Toolchains {
				names = append(names, n)
			}
			sort.Strings(names)
			r.add(root, "toolchain", "toolchain %q is not known; the toolchains are %s", clip(name, 30), strings.Join(names, ", "))
			return
		}
		if ver == "" {
			ver = toolchainDefault[name]
		}
		if !regexp.MustCompile(`^[0-9][0-9A-Za-z.]{0,15}$`).MatchString(ver) {
			r.add(root, "toolchain", "%q is not a version of %s", clip(ver, 20), name)
			return
		}
		m.Image = fmt.Sprintf(tmpl, ver)
	}
}

func hasKeyNode(n *yaml.Node, key string) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

func hasAlias(n *yaml.Node, depth int) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return true
	}
	if depth > 20 {
		return true // too deep is as good as refused
	}
	for _, c := range n.Content {
		if hasAlias(c, depth+1) {
			return true
		}
	}
	return false
}

func yamlErrLine(err error) int {
	s := err.Error()
	if i := strings.Index(s, "line "); i >= 0 {
		n := 0
		for _, c := range s[i+5:] {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
			if n > 1e6 {
				return 0
			}
		}
		return n
	}
	return 0
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func (r *reader) permissions(n *yaml.Node, m *Manifest) {
	r.keys(n, "permissions", []string{"network", "write"}, func(k, v *yaml.Node) {
		path := "permissions." + k.Value
		switch k.Value {
		case "network":
			hosts := r.strList(v, path, MaxHosts)
			for i, h := range hosts {
				if !hostRe.MatchString(h) || len(h) > 253 {
					r.add(v.Content[i], fmt.Sprintf("%s[%d]", path, i), "%q is not a host: write a name such as registry.npmjs.org, or *.example.com, optionally with :port", clip(h, 60))
				}
			}
			m.Permissions.Network = hosts
		case "write":
			paths := r.strList(v, path, MaxWritable)
			for i, p := range paths {
				if msg := CheckWorkspacePath(p); msg != "" {
					r.add(v.Content[i], fmt.Sprintf("%s[%d]", path, i), "%s", msg)
				}
			}
			m.Permissions.Write = paths
		}
	})
}

// CheckWorkspacePath says why p cannot be a path inside the workspace that a
// step may write, or "". It is a check of the text; the Station also refuses
// a path that is a link (see SafeJoin).
func CheckWorkspacePath(p string) string {
	switch {
	case p == "":
		return "a path is empty"
	case p == ".":
		return ""
	case strings.HasPrefix(p, "/") || strings.Contains(p, `\`):
		return fmt.Sprintf("%q must be relative to the workspace, with / between folders", clip(p, 60))
	case strings.ContainsAny(p, "*?[\x00") || !utf8.ValidString(p):
		return fmt.Sprintf("%q is a path, not a pattern", clip(p, 60))
	}
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".", "..":
			return fmt.Sprintf("%q has a %q part; write a plain relative path such as dist or build/output", clip(p, 60), part)
		}
	}
	if p == ".git" || strings.HasPrefix(p, ".git/") || p == ".launchway" || strings.HasPrefix(p, ".launchway/") {
		return fmt.Sprintf("%q is kept by Launchway and cannot be listed as writable", p)
	}
	return ""
}

func (r *reader) inputs(n *yaml.Node) []*Input {
	if n.Kind != yaml.MappingNode {
		r.add(n, "inputs", "inputs is a mapping of names to inputs, for example: name: { type: string, required: true }")
		return nil
	}
	if len(n.Content)/2 > MaxInputs {
		r.add(n, "inputs", "a step may have at most %d inputs", MaxInputs)
		return nil
	}
	var out []*Input
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		at := "inputs." + k.Value
		if seen[k.Value] {
			r.add(k, at, "input %q is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		if !fieldNameRe.MatchString(k.Value) {
			r.add(k, at, "%q is not an input name: use lower-case letters, digits and _, starting with a letter (at most 40)", clip(k.Value, 40))
		}
		in := &Input{Name: k.Value, Type: TypeString, Line: k.Line}
		var defN, optN *yaml.Node
		requiredSet, optionalRequired := false, false
		r.keys(v, at, []string{"type", "description", "required", "default", "options", "pattern", "min", "max"}, func(k2, v2 *yaml.Node) {
			w := at + "." + k2.Value
			switch k2.Value {
			case "type":
				in.Type = r.str(v2, w)
				if !contains(Types, in.Type) {
					r.add(v2, w, "the type is one of %s", strings.Join(Types, ", "))
					in.Type = TypeString
				}
			case "description":
				in.Description = strings.TrimSpace(r.str(v2, w))
				if strings.ContainsAny(in.Description, "\r\n") || utf8.RuneCountInString(in.Description) > MaxDescription {
					r.add(v2, w, "a description is one line of at most %d characters", MaxDescription)
				}
			case "required":
				requiredSet = true
				switch r.str(v2, w) {
				case "true":
					optionalRequired = true
				case "false":
				default:
					r.add(v2, w, "required is true or false")
				}
			case "default":
				defN = v2
			case "options":
				optN = v2
				in.Options = r.strList(v2, w, MaxOptions)
			case "pattern":
				in.Pattern = r.str(v2, w)
				if len(in.Pattern) > 200 {
					r.add(v2, w, "a pattern is at most 200 characters")
				} else if re, err := regexp.Compile(in.Pattern); err != nil {
					r.add(v2, w, "the pattern is not a valid regular expression: %s", clip(err.Error(), 120))
				} else {
					in.pattern = re
				}
			case "min", "max":
				f, err := strconv.ParseFloat(r.str(v2, w), 64)
				if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
					r.add(v2, w, "%s is a number", k2.Value)
					return
				}
				if k2.Value == "min" {
					in.Min = &f
				} else {
					in.Max = &f
				}
			}
		})
		if in.Type == TypeSecret && (defN != nil || optN != nil || in.Pattern != "" || in.Min != nil || in.Max != nil) {
			r.add(v, at, "a secret is given by reference: it has no default, options, pattern, min or max")
			defN, optN = nil, nil
		}
		if optN != nil && (in.Type == TypeBoolean || in.Type == TypeList || in.Type == TypeRecord) {
			r.add(optN, at+".options", "options are for strings and numbers")
		}
		if defN != nil {
			text, msg := defaultText(in.Type, defN)
			if msg != "" {
				r.add(defN, at+".default", "the default does not fit: %s", msg)
			} else {
				in.Default = &text
			}
		}
		in.Required = in.Default == nil
		if requiredSet && !optionalRequired && in.Type != TypeSecret {
			in.Required = false // optional: absent when not given
		}
		if requiredSet && !optionalRequired && in.Type == TypeSecret {
			r.add(v, at+".required", "a secret input is always required: a step that can work without it takes no secret")
		}
		if optionalRequired && in.Default != nil {
			r.add(v, at+".required", "an input with a default is never required: remove the default or required")
		}
		if in.Default != nil {
			if msg := in.Check(*in.Default); msg != "" {
				r.add(defN, at+".default", "the default does not fit: %s", msg)
			}
		}
		for _, o := range in.Options {
			saved := in.Options
			in.Options = nil
			if msg := in.Check(o); msg != "" {
				r.add(optN, at+".options", "%s", msg)
			}
			in.Options = saved
		}
		out = append(out, in)
	}
	return out
}

func (r *reader) outputs(n *yaml.Node) []*Output {
	if n.Kind != yaml.MappingNode {
		r.add(n, "outputs", "outputs is a mapping of names to outputs, for example: url: { type: string }")
		return nil
	}
	if len(n.Content)/2 > MaxOutputs {
		r.add(n, "outputs", "a step may have at most %d outputs", MaxOutputs)
		return nil
	}
	var out []*Output
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		at := "outputs." + k.Value
		if seen[k.Value] {
			r.add(k, at, "output %q is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		if !fieldNameRe.MatchString(k.Value) {
			r.add(k, at, "%q is not an output name: use lower-case letters, digits and _, starting with a letter (at most 40)", clip(k.Value, 40))
		}
		o := &Output{Name: k.Value, Type: TypeString, Line: k.Line}
		r.keys(v, at, []string{"type", "description"}, func(k2, v2 *yaml.Node) {
			w := at + "." + k2.Value
			if k2.Value == "type" {
				o.Type = r.str(v2, w)
				if !contains(Types, o.Type) {
					r.add(v2, w, "the type is one of %s", strings.Join(Types, ", "))
					o.Type = TypeString
				}
				return
			}
			o.Description = strings.TrimSpace(r.str(v2, w))
			if strings.ContainsAny(o.Description, "\r\n") || utf8.RuneCountInString(o.Description) > MaxDescription {
				r.add(v2, w, "a description is one line of at most %d characters", MaxDescription)
			}
		})
		out = append(out, o)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// defaultText turns a YAML default into the text a `with` value would have.
func defaultText(typ string, n *yaml.Node) (string, string) {
	switch typ {
	case TypeList:
		if n.Kind != yaml.SequenceNode {
			return "", "a list default is written as [a, b]"
		}
		items := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return "", "a list holds text"
			}
			items = append(items, c.Value)
		}
		return JSONList(items), ""
	case TypeRecord:
		var v any
		if err := n.Decode(&v); err != nil {
			return "", "a record default is written as { key: value }"
		}
		return canonJSON(v)
	default:
		if n.Kind != yaml.ScalarNode {
			return "", "a " + typ + " default is a single value"
		}
		return n.Value, ""
	}
}

// Check says why v (as a `with` value is written: text) is not a value of
// the input, or "".
func (in *Input) Check(v string) string {
	switch in.Type {
	case TypeNumber:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Sprintf("%s is a number; %q is not one", in.Name, clip(v, 40))
		}
		if in.Min != nil && f < *in.Min {
			return fmt.Sprintf("%s is at least %s; %s is less", in.Name, fmtNum(*in.Min), fmtNum(f))
		}
		if in.Max != nil && f > *in.Max {
			return fmt.Sprintf("%s is at most %s; %s is more", in.Name, fmtNum(*in.Max), fmtNum(f))
		}
	case TypeBoolean:
		if v != "true" && v != "false" {
			return fmt.Sprintf("%s is true or false; %q is neither", in.Name, clip(v, 40))
		}
	case TypeList:
		items, err := ParseJSONList(v)
		if err != nil {
			return fmt.Sprintf("%s is a list of text: %s", in.Name, err)
		}
		if in.Min != nil && float64(len(items)) < *in.Min {
			return fmt.Sprintf("%s has at least %s entries; it has %d", in.Name, fmtNum(*in.Min), len(items))
		}
		if in.Max != nil && float64(len(items)) > *in.Max {
			return fmt.Sprintf("%s has at most %s entries; it has %d", in.Name, fmtNum(*in.Max), len(items))
		}
	case TypeRecord:
		if len(v) > 4096 {
			return fmt.Sprintf("%s is longer than 4096 bytes", in.Name)
		}
		if _, err := ParseJSONRecord(v); err != nil {
			return fmt.Sprintf("%s is a record, written as JSON such as {\"a\": 1}: %s", in.Name, err)
		}
	case TypeSecret:
		return ""
	default:
		switch {
		case len(v) > 4096:
			return fmt.Sprintf("the value of %s is longer than 4096 bytes", in.Name)
		case !utf8.ValidString(v) || strings.ContainsRune(v, 0):
			return fmt.Sprintf("the value of %s is not text", in.Name)
		}
		n := float64(utf8.RuneCountInString(v))
		if in.Min != nil && n < *in.Min {
			return fmt.Sprintf("%s has at least %s characters; it has %d", in.Name, fmtNum(*in.Min), int(n))
		}
		if in.Max != nil && n > *in.Max {
			return fmt.Sprintf("%s has at most %s characters; it has %d", in.Name, fmtNum(*in.Max), int(n))
		}
		if in.pattern != nil && !in.pattern.MatchString(v) {
			return fmt.Sprintf("%s must match %s; %q does not", in.Name, in.Pattern, clip(v, 40))
		}
	}
	if len(in.Options) > 0 {
		for _, o := range in.Options {
			if in.Type == TypeNumber {
				a, e1 := strconv.ParseFloat(strings.TrimSpace(o), 64)
				b, e2 := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if e1 == nil && e2 == nil && a == b {
					return ""
				}
			} else if o == v {
				return ""
			}
		}
		quoted := make([]string, len(in.Options))
		for i, o := range in.Options {
			quoted[i] = strconv.Quote(o)
		}
		return fmt.Sprintf("%s must be one of %s; %q is not", in.Name, strings.Join(quoted, ", "), clip(v, 40))
	}
	return ""
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// Input returns the named input, or nil.
func (m *Manifest) Input(name string) *Input {
	for _, in := range m.Inputs {
		if in.Name == name {
			return in
		}
	}
	return nil
}

// Output returns the named output, or nil.
func (m *Manifest) Output(name string) *Output {
	for _, o := range m.Outputs {
		if o.Name == name {
			return o
		}
	}
	return nil
}

// SecretInputs lists the inputs that are secrets, by name.
func (m *Manifest) SecretInputs() []string {
	var out []string
	for _, in := range m.Inputs {
		if in.Type == TypeSecret {
			out = append(out, in.Name)
		}
	}
	return out
}

// CheckWith checks the values a use passes (name to text) against the inputs:
// unknown names, wrong types and missing required values, each as one line of
// plain words keyed by the input's name ("" for a problem of the whole). Secrets are
// given as name_store, name_key and name_field, and are checked by name.
func (m *Manifest) CheckWith(with map[string]string) map[string]string {
	out := map[string]string{}
	known := map[string]bool{}
	for _, in := range m.Inputs {
		if in.Type == TypeSecret {
			for _, suffix := range []string{"_store", "_key", "_field"} {
				known[in.Name+suffix] = true
			}
			if _, ok := with[in.Name+"_store"]; !ok && in.Required {
				out[in.Name+"_store"] = fmt.Sprintf("%s is a secret and is given by reference: add %s_store and %s_key under with:", in.Name, in.Name, in.Name)
			}
			continue
		}
		known[in.Name] = true
		v, ok := with[in.Name]
		switch {
		case !ok && in.Required:
			out[in.Name] = fmt.Sprintf("the step %s needs a value for %s, which has no default: add it under with:", m.Name, in.Name)
		case ok:
			if msg := in.Check(v); msg != "" {
				out[in.Name] = msg
			}
		}
	}
	names := make([]string, 0, len(with))
	for k := range with {
		names = append(names, k)
	}
	sort.Strings(names)
	var inputNames []string
	for _, in := range m.Inputs {
		inputNames = append(inputNames, in.Name)
	}
	for _, k := range names {
		if !known[k] {
			out[k] = fmt.Sprintf("the step %s has no input %q%s; its inputs are %s", m.Name, clip(k, 40), nearest(k, inputNames), listOrNone(inputNames))
		}
	}
	return out
}

func listOrNone(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}
