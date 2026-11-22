package pipeline

// Shared libraries and templates.
//
// A library is a Git repository with templates/<name>.yaml files and any
// other files (scripts) its templates use. A pipeline names the libraries it
// uses and the version of each (a tag, branch or commit), and a step that
// says uses: library/template is replaced by the template's steps. That
// replacement, Expand, happens once, when a run is created, against the
// commit each version pointed at then; the run keeps the expanded graph and
// the commits, so it never depends on the library again except for its
// files, which runners fetch at the recorded commit.
//
// Templates have typed parameters, and there is still no expression
// language. A parameter reaches a template in exactly three ways: every step
// of the template receives it as the variable PARAM_<NAME>; a value that
// would be written in place may be { param: name } (with format: for
// strings) in an image, an env value, a timeout, a retry or a when list; and
// if: { param: name } leaves a step out when it is false. All three are
// resolved during Expand, so the graph a run keeps holds plain values only.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Limits on libraries, templates and parameters.
const (
	MaxLibraries         = 10   // per pipeline
	MaxParams            = 50   // per template, and values per with
	MaxParamValueLen     = 4096 // bytes in one parameter value
	MaxNesting           = 4    // templates using templates, below the pipeline
	MaxExpandedSteps     = 500  // steps in a pipeline once expanded
	MaxStepIDLen         = 200  // bytes in an expanded step's ID
	MaxVersionLen        = 128
	ParamEnvPrefix       = "PARAM_"
	paramTypeString      = "string"
	paramTypeNumber      = "number"
	paramTypeBoolean     = "boolean"
	paramTypeList        = "list"
	paramTypeEnvironment = "environment"
	paramTypeOutput      = "output"
	paramTypeApplication = "application"
	templatesDir         = "templates/"
	templateExtension    = ".yaml"
)

var (
	// libNameRe is a library's name, in a pipeline and in the server's list.
	// It names a directory and a variable (LAUNCHWAY_LIB_<NAME>), so it has
	// no underscores: a-b and a_b would be the same variable.
	libNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	// templateNameRe names a template, and its file under templates/.
	templateNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	paramNameRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	outputKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`)
	// versionRe is a tag, branch or commit as a pipeline may write it. It is
	// only ever compared with the names Git lists, never handed to Git as an
	// argument, but it starts with a letter or digit all the same.
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]{0,127}$`)
	commitRe  = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// ValidLibraryName reports whether name can name a library.
func ValidLibraryName(name string) bool { return libNameRe.MatchString(name) }

// ValidTemplateName reports whether name can name a template.
func ValidTemplateName(name string) bool { return templateNameRe.MatchString(name) }

// ValidVersion reports whether v can be a library's version or override: a
// tag, a branch or a full commit ID.
func ValidVersion(v string) bool {
	return versionRe.MatchString(v) && !strings.Contains(v, "..") && !strings.Contains(v, "//") && !strings.HasSuffix(v, "/")
}

// IsCommitID reports whether v is a full commit ID.
func IsCommitID(v string) bool { return commitRe.MatchString(v) }

// TemplatePath is where a template's file is in its library.
func TemplatePath(name string) string { return templatesDir + name + templateExtension }

// ParamEnv is the variable a parameter reaches a template's steps as.
func ParamEnv(name string) string { return ParamEnvPrefix + EnvName(name) }

// EnvName turns a step ID, output key, library or parameter name into the
// part of a variable name that stands for it: upper case, with anything but
// letters and digits (such as - and the / in a template step's ID) as _.
func EnvName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, s)
}

// ---- reading templates ----

// Template is a template file, read with the values of one use.
type Template struct {
	Name        string
	Description string
	Params      []*Param
	Values      map[string]string
	Containers  []*Container
	Steps       []*Step // uses steps included; steps whose if is false are marked
	Outputs     []templateOutput
	Loops       []*Loop // the template's loops, expanded; IDs without the use's prefix
}

type templateOutput struct {
	Key, Step, StepKey string
	Line               int
}

var templateKeys = []string{"template", "description", "params", "containers", "steps", "outputs"}

// readTemplate reads a template for one use. Problems with the values the
// use passes are reported through withProblem, in the file that has the
// use; problems in the template itself are returned.
func readTemplate(data []byte, name string, with map[string]string, withLines map[string]*yamlPos, usesLine int,
	withProblem func(line, col int, path, msg string), loopLimit int, prefix string, vb *varBinding) (*Template, Problems) {
	if len(data) > MaxFileBytes {
		return nil, Problems{{Message: fmt.Sprintf("the template is larger than %d KiB", MaxFileBytes>>10)}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Problems{syntaxProblem(err)}
	}
	ps := &parser{}
	if len(doc.Content) == 0 {
		ps.add(nil, "", "the template is empty")
		return nil, ps.problems
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		ps.add(root, "", "expected a mapping with template, params and steps")
		return nil, ps.problems
	}
	t := &Template{}
	nodes := map[string]*yaml.Node{}
	ps.eachKey(root, "", templateKeys, func(k, v *yaml.Node) { nodes[k.Value] = v })
	if v := nodes["template"]; v == nil {
		ps.add(root, "template", "say which template this is: template: %s", name)
	} else if t.Name = ps.str(v, "template"); t.Name != name {
		ps.add(v, "template", "the file %s is the template %s; it says template: %s", TemplatePath(name), name, clipText(t.Name, 60))
	}
	if v := nodes["description"]; v != nil {
		t.Description = strings.TrimSpace(ps.str(v, "description"))
		if strings.ContainsAny(t.Description, "\r\n") || utf8.RuneCountInString(t.Description) > MaxDescriptionLen {
			ps.add(v, "description", "a description is one line of at most %d characters", MaxDescriptionLen)
		}
	}
	scope := &paramScope{params: map[string]*Param{}, values: map[string]string{}}
	if v := nodes["params"]; v != nil {
		t.Params = ps.parseParams(v)
		for _, p := range t.Params {
			scope.params[p.Name] = p
		}
	}
	// The values of this use. Their problems are in the using file.
	withOK := true
	keys := make([]string, 0, len(with))
	for k := range with {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pos := withLines[k]
		if pos == nil {
			pos = &yamlPos{Line: usesLine}
		}
		p := scope.params[k]
		if p == nil {
			withOK = false
			msg := unknownParamMessage(name, k, t.Params)
			withProblem(pos.Line, pos.Column, "with."+k, msg)
			continue
		}
		if msg := vb.checkWith(p, with[k]); msg != "" {
			withOK = false
			withProblem(pos.Line, pos.Column, "with."+k, msg)
			continue
		}
		scope.values[k] = with[k]
		if p.Type == paramTypeList {
			scope.values[k] = canonList(with[k])
		}
	}
	if !fillParams(t.Params, scope.values, with, name, usesLine, withProblem, vb) {
		withOK = false
	}
	t.Values = scope.values
	if !withOK || len(ps.problems) > 0 {
		// Reading the steps with missing values would only report the same
		// problem again in other words.
		return t, ps.problems
	}

	ps.tmpl = scope
	p := &Pipeline{}
	if v := nodes["containers"]; v != nil {
		ps.parseContainers(v, p)
	}
	if v := nodes["steps"]; v == nil {
		ps.add(root, "steps", "a template needs at least one step")
	} else {
		ps.parseSteps(v, p)
	}
	if ps.check(p) {
		ps.checkOutputTests(p)
		ps.checkImages(p)
	}
	ps.checkArtifacts(p)
	t.Containers, t.Steps = p.Containers, p.Steps
	if v := nodes["outputs"]; v != nil {
		t.Outputs = ps.parseOutputs(v, p)
	}
	for _, o := range t.Outputs {
		if s := p.Step(o.Step); s != nil && s.ForEach != nil {
			ps.problems = append(ps.problems, Problem{Line: o.Line, Path: "outputs." + o.Key,
				Message: fmt.Sprintf("%s is repeated by for_each, so it is several steps; an output names one step", o.Step)})
		}
	}
	if len(ps.problems) == 0 {
		// Every list is known here: written in place, or a parameter's.
		steps, loops, lps := expandLoops(t.Steps, prefix, loopLimit, func(l LoopList) ([]string, bool, string) { return l.Values, true, "" }, false)
		ps.problems = append(ps.problems, lps...)
		t.Steps, t.Loops = steps, loops
	}
	sort.SliceStable(ps.problems, func(i, j int) bool { return ps.problems[i].Line < ps.problems[j].Line })
	return t, ps.problems
}

// unknownParamMessage says a use passes a parameter the template does not
// have, suggests the nearest one it does have, and lists them.
func unknownParamMessage(template, key string, params []*Param) string {
	msg := fmt.Sprintf("the template %s has no parameter %q", template, clipText(key, 60))
	var all []string
	for _, p := range params {
		all = append(all, p.Name)
	}
	sep := "; it takes "
	if near := nearest(key, all); near != "" {
		msg += ": did you mean " + near + "?"
		sep = " It takes "
	}
	if names := paramNames(params); names != "" {
		msg += sep + names
	}
	return msg
}

func paramNames(params []*Param) string {
	names := make([]string, len(params))
	for i, p := range params {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}

// parseParams reads params: { name: { type, default, description, options } }.
func (ps *parser) parseParams(n *yaml.Node) []*Param {
	if n.Kind != yaml.MappingNode {
		ps.add(n, "params", "params is a mapping of names to parameters, for example: scan: { type: boolean, default: true }")
		return nil
	}
	if len(n.Content)/2 > MaxParams {
		ps.add(n, "params", "a template may have at most %d parameters", MaxParams)
		return nil
	}
	var out []*Param
	nodes := map[string]*yaml.Node{}
	defer func() { ps.checkDerivedDefaults(out, nodes) }()
	seen, vars := map[string]bool{}, map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		at := "params." + k.Value
		if seen[k.Value] {
			ps.add(k, at, "parameter %q is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		if !paramNameRe.MatchString(k.Value) {
			ps.add(k, at, "%q is not a valid parameter name: use lower-case letters, digits, - and _, starting with a letter (at most 40)", k.Value)
		} else if other, ok := vars[EnvName(k.Value)]; ok {
			ps.add(k, at, "%s and %s would both be the variable %s; rename one", other, k.Value, ParamEnv(k.Value))
		} else {
			vars[EnvName(k.Value)] = k.Value
		}
		p := &Param{Name: k.Value, Type: paramTypeString, Line: k.Line}
		nodes[k.Value] = k
		var defNode, optNode *yaml.Node
		ps.eachKey(v, at, []string{"type", "default", "description", "options", "from"}, func(k2, v2 *yaml.Node) {
			where := at + "." + k2.Value
			switch k2.Value {
			case "type":
				p.Type = ps.str(v2, where)
				if !slices.Contains(ParamTypes, p.Type) {
					ps.add(v2, where, "the type is %s or %s", strings.Join(ParamTypes[:len(ParamTypes)-1], ", "), ParamTypes[len(ParamTypes)-1])
					p.Type = paramTypeString
				}
			case "default":
				if v2.Kind == yaml.MappingNode {
					p.DefaultFrom = ps.parseDerivedDefault(v2, where)
					return
				}
				var d string
				if v2.Kind == yaml.SequenceNode {
					d, _ = ps.inputLiteral(v2, where, InputList)
				} else {
					d = ps.str(v2, where)
				}
				p.Default, defNode = &d, v2
			case "description":
				p.Description = strings.TrimSpace(ps.str(v2, where))
				if strings.ContainsAny(p.Description, "\r\n") || utf8.RuneCountInString(p.Description) > MaxDescriptionLen {
					ps.add(v2, where, "a description is one line of at most %d characters", MaxDescriptionLen)
				}
			case "from":
				p.From = ps.variableName(v2, where)
			case "options":
				p.Options, optNode = ps.strList(v2, where), v2
			}
		})
		if optNode != nil {
			if p.Type == paramTypeBoolean || p.Type == paramTypeList {
				ps.add(optNode, at+".options", "options are for strings and numbers")
			}
			opts := p.Options
			p.Options = nil // check each against the type alone
			for _, o := range opts {
				if msg := p.check(o); msg != "" {
					ps.add(optNode, at+".options", "%s", msg)
				}
			}
			p.Options = opts
		}
		if defNode != nil {
			if msg := p.check(*p.Default); msg != "" {
				ps.add(defNode, at+".default", "the default does not fit: %s", msg)
			} else if p.Type == paramTypeList {
				d := canonList(*p.Default)
				p.Default = &d
			}
		}
		out = append(out, p)
	}
	return out
}

// parseOutputs reads outputs: { key: { from: step.key } }.
func (ps *parser) parseOutputs(n *yaml.Node, p *Pipeline) []templateOutput {
	if n.Kind != yaml.MappingNode {
		ps.add(n, "outputs", "outputs is a mapping of names to step outputs, for example: coverage: { from: test.coverage }")
		return nil
	}
	var out []templateOutput
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		at := "outputs." + k.Value
		if seen[k.Value] {
			ps.add(k, at, "output %q is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		if !outputKeyRe.MatchString(k.Value) {
			ps.add(k, at, "%q is not a valid output name: use letters, digits, - and _", k.Value)
		}
		var from string
		ps.eachKey(v, at, []string{"from"}, func(_, v2 *yaml.Node) { from = ps.str(v2, at+".from") })
		step, key, ok := strings.Cut(from, ".")
		switch {
		case v.Kind != yaml.MappingNode:
			continue // reported by eachKey
		case !ok || step == "" || key == "":
			ps.add(v, at+".from", "write from: step.output, such as from: test.coverage")
			continue
		case p.Step(step) == nil:
			ps.add(v, at+".from", "there is no step called %q in this template", clipText(step, 60))
			continue
		}
		out = append(out, templateOutput{Key: k.Value, Step: step, StepKey: key, Line: k.Line})
	}
	return out
}

// ---- expansion ----

// ErrNoTemplate is what a TemplateLoader returns when the library has no
// such template at the commit.
var ErrNoTemplate = errors.New("no such template")

// TemplateLoader reads templates/<name>.yaml from a library at the commit
// the Library records.
type TemplateLoader func(lib *Library, name string) ([]byte, error)

// Expand replaces every step that uses a template by the template's steps.
// Every library the pipeline uses must have its Commit set. The result is a
// new pipeline; p is not changed. On failure the error is Problems, each in
// the pipeline file or, with File set, in a template.
//
// A template's steps get IDs <uses-step>/<step>, Group set to the uses step
// and Library to the library. Those that need nothing in the template need
// what the uses step needs; a step that needs the uses step needs all of
// its steps. The uses step's when, retry and timeout apply to every step of
// the template that does not set its own. The template's containers are
// added under new names, <uses-step>-<name> where that is free and valid.
func Expand(p *Pipeline, load TemplateLoader) (*Pipeline, error) {
	out, _, err := ExpandWith(p, load, nil)
	return out, err
}

func expandBound(p *Pipeline, load TemplateLoader, vb *varBinding) (*Pipeline, error) {
	if p.Expanded() && len(p.Libraries) == 0 {
		return p, nil
	}
	x := &expander{load: load, vb: vb, used: map[string]bool{}, containerNames: map[string]bool{}, loopLimit: p.maxLoop}
	if x.loopLimit <= 0 {
		x.loopLimit = DefaultMaxLoopMembers
	}
	out := &Pipeline{Name: p.Name, On: p.On, Origin: p.Origin, Supersede: p.Supersede, SupersedeOff: p.SupersedeOff, Inputs: p.Inputs, Loops: p.Loops, maxLoop: p.maxLoop,
		Runner: p.Runner, Requires: p.Requires, Timeout: p.Timeout, TimeoutLine: p.TimeoutLine, Lane: p.Lane}
	if !p.Bound() {
		return nil, Problems{{Message: "the pipeline has loops or input references that have not been bound to a run's values"}}
	}
	for _, l := range p.Libraries {
		c := *l
		out.Libraries = append(out.Libraries, &c)
	}
	for _, c := range p.Containers {
		cc := *c
		out.Containers = append(out.Containers, &cc)
		x.containerNames[c.Name] = true
	}
	x.out = out
	root := &scope{byID: map[string]*Step{}, children: map[string]*scope{}, steps: p.Steps, containers: map[string]string{}}
	for _, s := range p.Steps {
		root.byID[s.ID] = s
	}
	for _, c := range p.Containers {
		root.containers[c.Name] = c.Name
	}
	x.open(root, p, 0)
	if len(x.problems) == 0 {
		x.emit(root)
	}
	if len(x.problems) == 0 && len(out.Steps) > MaxExpandedSteps {
		x.problems = append(x.problems, Problem{Message: fmt.Sprintf("expanded, the pipeline has %d steps; at most %d are allowed", len(out.Steps), MaxExpandedSteps)})
	}
	x.checkDeployments(out)
	if len(x.problems) == 0 && len(out.Steps) == 0 {
		x.problems = append(x.problems, Problem{Message: "expanded, the pipeline has no steps left: every template step's if was false"})
	}
	if len(x.problems) > 0 {
		sort.SliceStable(x.problems, func(i, j int) bool {
			a, b := x.problems[i], x.problems[j]
			if (a.File == "") != (b.File == "") {
				return a.File == ""
			}
			if a.Library+a.File != b.Library+b.File {
				return a.Library+a.File < b.Library+b.File
			}
			return a.Line < b.Line
		})
		return nil, x.problems
	}
	// Only the libraries some step used are kept.
	kept := out.Libraries[:0]
	for _, l := range out.Libraries {
		if x.used[l.Name] {
			kept = append(kept, l)
		}
	}
	out.Libraries = kept
	// A template's step may ask for resources, which the pipeline file itself
	// did not show.
	out.requireDeploy()
	out.Requires = out.requiresNewer(out.Requires) // step resources, build caches
	out.requireFlow()                              // likewise for the flow control keys a template's step uses
	if ps := checkFetchLoops(out, true); len(ps) > 0 {
		return nil, ps
	}
	return out, nil
}

type expander struct {
	vb             *varBinding
	load           TemplateLoader
	out            *Pipeline
	problems       Problems
	used           map[string]bool // libraries some step used
	containerNames map[string]bool
	loopLimit      int
}

// scope is one file's steps: the pipeline's, or a template's for one use.
type scope struct {
	prefix     string // "" for the pipeline; "<group>/" for a template
	group      *Group
	lib        *Library
	file       string // the template's path; "" for the pipeline
	tmpl       *Template
	steps      []*Step
	byID       map[string]*Step
	children   map[string]*scope // the template of each uses step
	containers map[string]string // a container's name here → its name in the run
	stack      []string          // this template and those it is used from, outermost first, for loops

	// What the uses step gives the template's steps.
	inherit       []string // the steps it needs
	inheritGroups []string
	when          *Condition
	retry         *retryPolicy
	timeout       *Duration
	resources     *Resources        // the uses step's resources: what a script stage of the template takes where it sets none of its own
	each          map[string]string // the loop values of the uses step, and of those around it
	continueFail  bool              // continue_for selected the loop member of the uses step
}

func (x *expander) problem(sc *scope, line, col int, path, format string, args ...any) {
	p := Problem{Line: line, Column: col, Path: path, Message: fmt.Sprintf(format, args...)}
	if sc.lib != nil && sc.file != "" {
		p.File, p.Library = sc.file, sc.lib.Name
	}
	x.problems = append(x.problems, p)
}

// open reads the template of every uses step in the scope, and of theirs.
func (x *expander) open(sc *scope, root *Pipeline, depth int) {
	for _, s := range sc.steps {
		if !s.IsUse() || (s.ifTest != nil && !s.ifTest.keep) {
			continue
		}
		lib, name := sc.lib, s.Uses
		if sc.lib == nil {
			var alias string
			alias, name, _ = strings.Cut(s.Uses, "/")
			lib = root.Library(alias)
		}
		at := "steps." + s.ID
		switch {
		case lib == nil:
			x.problem(sc, s.usesLine, 0, at+".uses", "no library called %q is listed under libraries:", s.Uses)
			continue
		case !IsCommitID(lib.Commit):
			x.problem(sc, s.usesLine, 0, at+".uses", "the library %s has not been resolved to a commit", lib.Name)
			continue
		case depth >= MaxNesting:
			x.problem(sc, s.usesLine, 0, at+".uses", "templates are used %d deep here; at most %d levels are allowed", depth+1, MaxNesting)
			continue
		}
		for i, t := range sc.stack {
			if t == name {
				loop := append(append([]string{}, sc.stack[i:]...), name)
				x.problem(sc, s.usesLine, 0, at+".uses", "templates use each other in a loop: %s", strings.Join(loop, " → "))
				name = ""
				break
			}
		}
		if name == "" {
			continue
		}
		where := fmt.Sprintf("%s in library %s (%s at %s)", TemplatePath(name), lib.Name, lib.Wanted(), short(lib.Commit))
		data, err := x.load(lib, name)
		if errors.Is(err, ErrNoTemplate) {
			x.problem(sc, s.usesLine, 0, at+".uses", "the library %s has no template %s: there is no %s", lib.Name, name, where)
			continue
		}
		if err != nil {
			x.problem(sc, s.usesLine, 0, at+".uses", "reading %s: %v", where, err)
			continue
		}
		before := len(x.problems)
		with := s.With
		if hasOutputRef(with) {
			with = x.absoluteOutputs(sc, s, at)
		}
		t, tps := readTemplate(data, name, with, s.withLines, s.usesLine, func(line, col int, path, msg string) {
			x.problem(sc, line, col, at+"."+path, "%s", msg)
		}, x.loopLimit, sc.prefix+s.ID+"/", x.vb)
		if len(tps) > 0 {
			for _, tp := range tps {
				tp.File, tp.Library = TemplatePath(name), lib.Name
				x.problems = append(x.problems, tp)
			}
			x.problem(sc, s.usesLine, 0, at+".uses", "the template %s/%s has %d problem%s, listed with its file", lib.Name, name, len(tps), plural(len(tps)))
		}
		if len(x.problems) == before {
			x.adapt(sc, s, t, at)
		}
		if len(x.problems) > before {
			continue
		}
		x.used[lib.Name] = true
		child := &scope{prefix: sc.prefix + s.ID + "/", lib: lib, file: TemplatePath(name), tmpl: t, steps: t.Steps,
			byID: map[string]*Step{}, children: map[string]*scope{}, containers: map[string]string{}}
		child.stack = append(append([]string{}, sc.stack...), name)
		for _, ts := range t.Steps {
			child.byID[ts.ID] = ts
		}
		sc.children[s.ID] = child
		x.open(child, root, depth+1)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// dropped reports whether a template step's if left it out.
func dropped(s *Step) bool { return s.ifTest != nil && !s.ifTest.keep }

// members are the run's steps a uses step became, in order.
func (x *expander) members(sc *scope) []string {
	var out []string
	for _, s := range sc.steps {
		switch {
		case dropped(s):
		case s.IsUse():
			if c := sc.children[s.ID]; c != nil {
				out = append(out, x.members(c)...)
			}
		default:
			out = append(out, sc.prefix+s.ID)
		}
	}
	return out
}

// waitedFor are the steps a step that needs a use waits for: all of the use's
// steps but its rollbacks. A rollback is skipped when everything went well,
// and a skipped step would hold back what needs it; what a rollback is for
// is found by the verification that failed before it ran.
func (x *expander) waitedFor(sc *scope) []string {
	var out []string
	for _, id := range x.members(sc) {
		if st := x.stepByRunID(sc, id); st != nil && st.Kind == KindRollback {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (x *expander) stepByRunID(sc *scope, id string) *Step {
	for _, s := range sc.steps {
		if sc.prefix+s.ID == id {
			return s
		}
		if s.IsUse() {
			if c := sc.children[s.ID]; c != nil {
				if got := x.stepByRunID(c, id); got != nil {
					return got
				}
			}
		}
	}
	return nil
}

// needsOf works out what a step in a scope needs, as run step IDs, and
// which groups it needs as a whole.
func (x *expander) needsOf(sc *scope, s *Step) (ids, groups []string) {
	if len(s.Needs) == 0 {
		return nonNil(sc.inherit), nonNil(sc.inheritGroups)
	}
	seen := map[string]bool{}
	add := func(list []string, into *[]string) {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				*into = append(*into, id)
			}
		}
	}
	for _, n := range s.Needs {
		t := sc.byID[n]
		switch {
		case t == nil:
		case dropped(t):
			// A step left out stands aside: what needed it needs what it needed.
			i, g := x.needsOf(sc, t)
			add(i, &ids)
			add(g, &groups)
		case t.IsUse():
			c := sc.children[n]
			if c == nil {
				continue
			}
			m := x.waitedFor(c)
			if len(m) == 0 {
				// Nothing was left of it; it stands aside like a step left out.
				i, g := x.needsOf(sc, t)
				add(i, &ids)
				add(g, &groups)
			}
			add(m, &ids)
			add([]string{sc.prefix + n}, &groups)
		default:
			add([]string{sc.prefix + n}, &ids)
		}
	}
	return ids, groups
}

// nonNil copies a list, giving nil for an empty one.
func nonNil(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	return append([]string{}, list...)
}

// emit adds a scope's steps, groups and containers to the run's pipeline.
func (x *expander) emit(sc *scope) {
	if sc.tmpl != nil {
		x.out.Loops = append(x.out.Loops, sc.tmpl.Loops...)
		for _, c := range sc.tmpl.Containers {
			cc := *c
			cc.Name = x.containerName(sc.prefix, c.Name)
			sc.containers[c.Name] = cc.Name
			x.out.Containers = append(x.out.Containers, &cc)
		}
	}
	for _, s := range sc.steps {
		if dropped(s) {
			continue
		}
		id := sc.prefix + s.ID
		if len(id) > MaxStepIDLen {
			x.problem(sc, s.Line, 0, "steps."+s.ID, "expanded, this step's id %s is longer than %d characters", clipText(id, 60), MaxStepIDLen)
			continue
		}
		needs, groups := x.needsOf(sc, s)
		if s.IsUse() {
			child := sc.children[s.ID]
			if child == nil {
				continue
			}
			g := &Group{ID: id, Uses: s.Uses, Library: child.lib.Name, Template: child.tmpl.Name, Params: child.tmpl.Values,
				Description: s.Description}
			if g.Description == "" {
				g.Description = child.tmpl.Description
			}
			if sc.group != nil {
				g.Parent = sc.group.ID
			}
			if len(g.Params) == 0 {
				g.Params = nil
			}
			child.group = g
			child.inherit, child.inheritGroups = needs, groups
			child.when, child.retry, child.timeout = sc.when, sc.retry, sc.timeout
			child.resources = overlayResources(s.Resources, sc.resources)
			child.each = mergeEach(sc.each, s.Each)
			child.continueFail = sc.continueFail || s.continueFail
			if s.When != nil {
				child.when = whenIn(s.When, sc.prefix)
			}
			if s.retrySet {
				r := s.retryPolicy()
				child.retry = &r
			}
			if s.timeoutSet {
				t := s.Timeout
				child.timeout = &t
			}
			x.out.Groups = append(x.out.Groups, g)
			x.emit(child)
			g.Steps = x.members(child)
			if g.Steps == nil {
				g.Steps = []string{}
			}
			x.outputs(child, g)
			continue
		}
		c := *s
		c.ID = id
		c.Needs, c.NeedsGroups = needs, groups
		c.Fetch = x.fetchesOf(sc, s)
		c.ImageBuild = x.imageOf(sc, s)
		x.deployOf(sc, &c)
		c.Each = mergeEach(sc.each, s.Each)
		c.withLines, c.ifTest = nil, nil
		if sc.group != nil {
			c.Group = sc.group.ID
			c.Library = sc.lib.Name
		}
		if c.Container != "" {
			c.Container = sc.containers[s.Container]
		}
		c.When, c.AI = sc.whenFor(c.When), c.AI.inScope(sc.prefix)
		if sc.continueFail {
			c.tolerate()
		}
		if !s.retrySet && sc.retry != nil && !s.IsApproval() {
			c.setRetryPolicy(*sc.retry)
		}
		if !s.timeoutSet && sc.timeout != nil {
			c.Timeout = *sc.timeout
		}
		if sc.resources != nil && c.ResourceKind() != "" {
			c.Resources = overlayResources(c.Resources, sc.resources)
			if c.resourcesLine == 0 {
				c.resourcesLine = s.Line
			}
			if _, err := c.Resources.Parse(); err != nil {
				x.problem(sc, c.resourcesLine, 0, "steps."+s.ID+".resources", "the resources of this stage, with those its use gives the template: %v", err)
			}
		}
		if sc.tmpl != nil && !s.IsApproval() {
			env := make(map[string]string, len(s.Env)+len(sc.tmpl.Values))
			for k, v := range s.Env {
				env[k] = v
			}
			for k, v := range sc.tmpl.Values {
				env[ParamEnv(k)] = v
			}
			c.Env = env
		}
		x.out.Steps = append(x.out.Steps, &c)
	}
}

// outputs maps the outputs a template exposes onto steps of the run.
func (x *expander) outputs(sc *scope, g *Group) {
	for _, o := range sc.tmpl.Outputs {
		s := sc.byID[o.Step]
		switch {
		case s == nil || dropped(s):
			// Left out by its if: the output is simply not there.
		case s.IsUse():
			inner := x.out.Group(sc.prefix + o.Step)
			if inner == nil {
				continue
			}
			ref, ok := inner.Outputs[o.StepKey]
			if !ok {
				x.problem(sc, o.Line, 0, "outputs."+o.Key, "%s uses the template %s, which exposes no output %q", o.Step, inner.Template, o.StepKey)
				continue
			}
			setOutput(g, o.Key, ref)
		default:
			setOutput(g, o.Key, OutputRef{Step: sc.prefix + o.Step, Key: o.StepKey})
		}
	}
}

func setOutput(g *Group, key string, ref OutputRef) {
	if g.Outputs == nil {
		g.Outputs = map[string]OutputRef{}
	}
	g.Outputs[key] = ref
}

// containerName gives a template's container a name in the run:
// <uses-step>-<name> if that is a valid name nobody has, else a name made
// unique with a hash of where it came from.
func (x *expander) containerName(prefix, name string) string {
	base := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSuffix(prefix, "/"), "/", "-"), "_", "-") + "-" + name
	ok := func(n string) bool {
		return containerRe.MatchString(n) && !reservedNames[n] && !strings.HasPrefix(n, "image-") && !x.containerNames[n]
	}
	if !ok(base) {
		sum := sha256.Sum256([]byte(prefix + name))
		tail := name
		if len(tail) > 28 {
			tail = strings.TrimRight(tail[:28], "-")
		}
		base = "t" + hex.EncodeToString(sum[:])[:8] + "-" + tail
		for i := 2; !ok(base); i++ {
			base = fmt.Sprintf("t%s%d-%s", hex.EncodeToString(sum[:])[:6], i, tail)
		}
	}
	x.containerNames[base] = true
	return base
}

// mergeEach combines the loop values around a step with its own; its own
// win. It is nil when there are none.
func mergeEach(outer, own map[string]string) map[string]string {
	if len(outer) == 0 {
		return own
	}
	out := make(map[string]string, len(outer)+len(own))
	for k, v := range outer {
		out[k] = v
	}
	for k, v := range own {
		out[k] = v
	}
	return out
}

func hasOutputRef(with map[string]string) bool {
	for _, v := range with {
		if _, _, ok := OutputParam(v); ok {
			return true
		}
	}
	return false
}

// absoluteOutputs returns the values a use passes, with each output named
// as the run will name its step, so that the template can take it as a
// parameter of type output. A step that is not in the file is a problem.
func (x *expander) absoluteOutputs(sc *scope, use *Step, at string) map[string]string {
	out := make(map[string]string, len(use.With))
	for k, v := range use.With {
		if step, key, ok := OutputParam(v); ok {
			if t := sc.byID[step]; t != nil && dropped(t) {
				pos := use.withLines[k]
				line := use.usesLine
				if pos != nil {
					line = pos.Line
				}
				x.problem(sc, line, 0, at+".with."+k, "%s is switched off here, so it makes no output for %s: turn it on, or leave out what needs it", step, k)
			} else if t == nil {
				pos := use.withLines[k]
				line := use.usesLine
				if pos != nil {
					line = pos.Line
				}
				x.problem(sc, line, 0, at+".with."+k, "{ output: %s.%s } names a step that is not here; the steps are %s", clipText(step, 40), clipText(key, 40), stepNames(sc))
			}
			v = "output(" + sc.prefix + step + "." + key + ")"
		}
		out[k] = v
	}
	return out
}

func stepNames(sc *scope) string {
	var ids []string
	for _, s := range sc.steps {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ", ")
}
