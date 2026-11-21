package pipeline

// Loops: a step, or a step that uses a template, repeated over lists.
//
//	test:
//	  for_each:
//	    python: ["3.11", "3.12", "3.13"]
//	    os: { input: systems }          # a run input of type list
//	  exclude: [ { python: "3.11", os: alpine } ]
//	  max_parallel: 3
//	  fail_fast: false
//	  image: { each: python, format: "python:%s" }
//	  run: pytest
//
// A loop is expanded when a run is created (Bind, and Expand for loops in
// templates), into one concrete step per combination of values, in the
// order the lists are written: test[python=3.11,os=debian], and so on. The
// IDs are stable: the same lists give the same IDs, so a re-run or a resume
// finds the same steps. Each member receives its values as
// LAUNCHWAY_EACH_<KEY>, and { each: key } (with format: for text around it)
// puts a value in the member's image, an env value, or a with value. A step
// that needs the loop needs every member; another step reaches one
// member's outputs as LAUNCHWAY_NEEDS_<STEP>_<KEY>_<VALUE>..._<OUTPUT>.
//
// Lists are written in place, come from a run input of type list, or, in a
// template, from a parameter of type list. Lists produced by an earlier
// step while the run goes (dynamic fan-out) are not supported; LoopList
// leaves room for them (see the README).

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Limits on loops.
const (
	// DefaultMaxLoopMembers is how many combinations one loop may have when
	// the server's configuration does not say; MaxLoopMembersLimit is the
	// most a configuration may allow.
	DefaultMaxLoopMembers = 64
	MaxLoopMembersLimit   = MaxExpandedSteps
	MaxLoopKeys           = 5
	MaxLoopValueLen       = 40
)

// loopValueRe is what a loop value may be. Values become part of step IDs,
// which appear in URLs, variable names and the page, so they are kept
// plain: letters, digits, . _ + and -.
var loopValueRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,39}$`)

// ForEach is how a step is repeated, as written.
type ForEach struct {
	Keys  []string   `json:"keys"`
	Lists []LoopList `json:"lists"` // one per key
	// Exclude leaves out the combinations that match one of its entries:
	// every key an entry names has that value.
	Exclude []map[string]string `json:"exclude,omitempty"`
	// Add appends whole combinations after the cross product; ContinueFor
	// selects members that behave as on_failure: continue (loops_extra.go).
	Add         []loopEntry `json:"-"`
	ContinueFor []loopEntry `json:"-"`
	// MaxParallel bounds how many members are queued or running at once; 0
	// is no bound.
	MaxParallel int `json:"max_parallel,omitempty"`
	// FailFast stops the other members once one fails: those not started
	// are not run, and those running are stopped. On by default.
	FailFast bool `json:"fail_fast"`
	// Serial makes each member wait for the member before it to finish
	// well: the members go one after the other in the order of the lists,
	// and a member that fails ends the line (a delivery through dev, then
	// staging, then prod).
	Serial bool `json:"serial,omitempty"`
	Line   int  `json:"-"`
}

// LoopList is where one key's values come from: written in place, or a run
// input of type list. (In a template, a parameter's values are filled in as
// the template is read.) A list made by an earlier step at run time would
// be a third source here; it is not built.
type LoopList struct {
	Values []string `json:"values,omitempty"`
	Input  string   `json:"input,omitempty"`
}

// Loop is a repeated step after expansion.
type Loop struct {
	ID          string       `json:"id"` // the step as written, with its template prefix
	Description string       `json:"description,omitempty"`
	Keys        []string     `json:"keys"`
	Members     []LoopMember `json:"members"`
	MaxParallel int          `json:"max_parallel,omitempty"`
	FailFast    bool         `json:"fail_fast"`
	Serial      bool         `json:"serial,omitempty"`
}

// LoopMember is one combination: a step, or, when the loop repeats a step
// that uses a template, the group of steps the template became.
type LoopMember struct {
	ID   string            `json:"id"`
	Each map[string]string `json:"each"`
	// Extra says the combination was added by add, outside the lists;
	// AllowedFailure, that continue_for lets it fail without failing the run.
	Extra          bool `json:"extra,omitempty"`
	AllowedFailure bool `json:"allowed_failure,omitempty"`
}

// Loop returns the loop with the given ID, or nil.
func (p *Pipeline) Loop(id string) *Loop {
	for _, l := range p.Loops {
		if l.ID == id {
			return l
		}
	}
	return nil
}

// LoopsOf returns the loops a step belongs to, with the member it is in:
// the member is the step itself or the group it is part of.
func (p *Pipeline) LoopsOf(stepID string) []LoopRef {
	var out []LoopRef
	for _, l := range p.Loops {
		for i, m := range l.Members {
			if m.ID == stepID || strings.HasPrefix(stepID, m.ID+"/") {
				out = append(out, LoopRef{Loop: l, Member: i})
			}
		}
	}
	return out
}

// LoopRef is a loop and one of its members.
type LoopRef struct {
	Loop   *Loop
	Member int
}

// Has reports whether a step is the member, or part of it.
func (m LoopMember) Has(stepID string) bool {
	return m.ID == stepID || strings.HasPrefix(stepID, m.ID+"/")
}

// StepEnvName is EnvName for a step ID that may name a loop member:
// test[python=3.12,os=debian] is TEST_PYTHON_3_12_OS_DEBIAN.
func StepEnvName(id string) string { return EnvName(strings.ReplaceAll(id, "]", "")) }

// eachRef is a value a looped step takes from its combination: the image,
// an env value or a with value.
type eachRef struct {
	field  string // image, env or with
	name   string // the env or with key
	key    string // the loop key
	format string // text with one %s, or ""
	line   int
}

var loopKeys = []string{"for_each", "exclude", "max_parallel", "fail_fast", "add", "continue_for", "serial"}

// parseForEach reads for_each: { key: list, ... }.
func (ps *parser) parseForEach(n *yaml.Node, at string) *ForEach {
	f := &ForEach{FailFast: true, Line: n.Line}
	if n.Kind != yaml.MappingNode || len(n.Content) == 0 {
		ps.add(n, at, "for_each maps names to lists, for example: for_each: { python: [\"3.12\", \"3.13\"] }")
		return f
	}
	if len(n.Content)/2 > MaxLoopKeys {
		ps.add(n, at, "a loop has at most %d lists", MaxLoopKeys)
		return f
	}
	vars := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		where := at + "." + k.Value
		if slices.Contains(f.Keys, k.Value) {
			ps.add(k, where, "%q is written twice", k.Value)
			continue
		}
		if !paramNameRe.MatchString(k.Value) {
			ps.add(k, where, "%q is not a valid name: use lower-case letters, digits, - and _, starting with a letter (at most 40)", k.Value)
		} else if other, ok := vars[EnvName(k.Value)]; ok {
			ps.add(k, where, "%s and %s would both be the variable LAUNCHWAY_EACH_%s; rename one", other, k.Value, EnvName(k.Value))
		} else {
			vars[EnvName(k.Value)] = k.Value
		}
		var list LoopList
		switch {
		case v.Kind == yaml.SequenceNode:
			list.Values = []string{}
			for _, c := range v.Content {
				if c.Kind != yaml.ScalarNode {
					ps.add(c, where, "a list holds single values")
					continue
				}
				list.Values = append(list.Values, c.Value)
			}
			if len(list.Values) == 0 {
				ps.add(v, where, "the list is empty")
			}
			ps.checkLoopValues(v, where, list.Values)
		case v.Kind == yaml.MappingNode && hasKey(v, "input"):
			if ps.tmpl != nil {
				ps.add(v, where, "a template cannot read run inputs; take the list as a parameter of type list: { param: name }")
				break
			}
			ps.eachKey(v, where, []string{"input"}, func(_, v2 *yaml.Node) { list.Input = ps.str(v2, where+".input") })
			ps.loopInputs = append(ps.loopInputs, loopInputRef{input: list.Input, line: v.Line, at: where})
		case v.Kind == yaml.MappingNode && hasKey(v, "param"):
			if ps.tmpl == nil {
				ps.add(v, where, "{ param: … } is for templates; here, write the list, or { input: name } for a run input of type list")
				break
			}
			var name string
			ps.eachKey(v, where, []string{"param"}, func(_, v2 *yaml.Node) { name = ps.str(v2, where+".param") })
			p := ps.tmpl.params[name]
			switch {
			case p == nil:
				ps.add(v, where+".param", "%s", ps.unknownParam(name))
			case p.Type != paramTypeList:
				ps.add(v, where+".param", "%s is a %s; a loop needs a parameter of type list", name, p.Type)
			default:
				list.Values = ListItems(ps.tmpl.values[name])
				if list.Values == nil {
					list.Values = []string{}
				}
				ps.checkLoopValues(v, where, list.Values)
			}
		case v.Kind == yaml.MappingNode && hasKey(v, "from"):
			ps.add(v, where, "lists made by an earlier step while the run goes are not supported yet; write the list, or use a run input of type list")
		default:
			ps.add(v, where, "write a list, such as [a, b], or { input: name } for a run input of type list")
		}
		f.Keys = append(f.Keys, k.Value)
		f.Lists = append(f.Lists, list)
	}
	return f
}

// checkLoopValues checks values that will become part of step IDs.
func (ps *parser) checkLoopValues(n *yaml.Node, at string, values []string) {
	for i, v := range values {
		if msg := loopValueProblem(v); msg != "" {
			ps.add(n, at, "%s", msg)
		}
		if slices.Contains(values[:i], v) {
			ps.add(n, at, "%q is listed twice", clipText(v, 40))
		}
	}
}

func loopValueProblem(v string) string {
	if !loopValueRe.MatchString(v) {
		return fmt.Sprintf("%q cannot be a loop value: values name the steps the loop makes, so use 1 to %d letters, digits, . _ + and -, "+
			"starting with a letter or digit; put other text around a value with { each: key, format: \"…%%s…\" }", clipText(v, 40), MaxLoopValueLen)
	}
	return ""
}

// loopInputRef is a loop list taken from a run input, checked once every
// input is known.
type loopInputRef struct {
	input, at string
	line      int
}

// parseLoopSettings reads exclude, max_parallel and fail_fast.
func (ps *parser) parseLoopSettings(s *Step, at string, keys map[string]*yaml.Node, nodes map[string]*yaml.Node) {
	for _, key := range loopKeys[1:] {
		if keys[key] != nil && s.ForEach == nil {
			ps.add(keys[key], at+"."+key, "%s goes with for_each", key)
		}
	}
	f := s.ForEach
	if f == nil {
		for _, r := range s.eachRefs {
			ps.problems = append(ps.problems, Problem{Line: r.line, Path: at, Message: "{ each: " + r.key + " } is for steps repeated with for_each"})
		}
		return
	}
	if n := nodes["max_parallel"]; n != nil {
		f.MaxParallel = ps.int(n, at+".max_parallel")
		if f.MaxParallel < 1 || f.MaxParallel > MaxLoopMembersLimit {
			ps.add(n, at+".max_parallel", "max_parallel is between 1 and %d", MaxLoopMembersLimit)
		}
	}
	if n := nodes["fail_fast"]; n != nil {
		f.FailFast = ps.bool(n, at+".fail_fast")
	}
	if n := nodes["serial"]; n != nil {
		f.Serial = ps.bool(n, at+".serial")
	}
	if n := nodes["exclude"]; n != nil {
		if n.Kind != yaml.SequenceNode {
			ps.add(n, at+".exclude", "exclude is a list of combinations, for example: exclude: [ { python: \"3.11\", os: alpine } ]")
			return
		}
		for _, e := range n.Content {
			entry := map[string]string{}
			ps.eachKey(e, at+".exclude", f.Keys, func(k, v *yaml.Node) {
				entry[k.Value] = ps.str(v, at+".exclude."+k.Value)
			})
			if e.Kind == yaml.MappingNode && len(entry) == 0 {
				ps.add(e, at+".exclude", "an entry names at least one of %s", strings.Join(f.Keys, ", "))
			}
			f.Exclude = append(f.Exclude, entry)
		}
		// With every list written in place, an entry that matches nothing
		// is a mistake.
		literal := true
		for _, l := range f.Lists {
			literal = literal && l.Input == ""
		}
		if literal {
			for i, entry := range f.Exclude {
				if len(entry) > 0 && !excludeMatchesAny(f, entry) {
					ps.add(n.Content[i], at+".exclude", "this entry matches no combination of the lists")
				}
			}
		}
	}
	if s.IsApproval() {
		ps.add(keys["for_each"], at+".for_each", "an approval cannot be repeated")
	}
	ps.parseLoopExtras(s, at, nodes)
	for _, r := range s.eachRefs {
		if !slices.Contains(f.Keys, r.key) {
			ps.problems = append(ps.problems, Problem{Line: r.line, Path: at, Message: fmt.Sprintf("there is no list called %q under for_each; it has %s", clipText(r.key, 40), strings.Join(f.Keys, ", "))})
		}
	}
}

func excludeMatchesAny(f *ForEach, entry map[string]string) bool {
	for k, v := range entry {
		i := slices.Index(f.Keys, k)
		if i < 0 || !slices.Contains(f.Lists[i].Values, v) {
			return false
		}
	}
	return true
}

// eachValue reads { each: key } or { each: key, format: "…%s…" } in a
// looped step's image, env value or with value. It returns text that
// stands for the value until the loop is expanded.
func (ps *parser) eachValue(n *yaml.Node, at string) string {
	if ps.cur == nil || ps.curField == "" {
		ps.add(n, at, "{ each: … } is for the image, env values and with values of a step with for_each")
		return ""
	}
	r := eachRef{field: ps.curField, name: ps.curName, line: n.Line}
	var formatNode *yaml.Node
	ps.eachKey(n, at, []string{"each", "format"}, func(k, v *yaml.Node) {
		if k.Value == "each" {
			r.key = ps.str(v, at+".each")
		} else {
			r.format, formatNode = ps.str(v, at+".format"), v
		}
	})
	if formatNode != nil && (strings.Count(r.format, "%s") != 1 || strings.Count(r.format, "%") != 1) {
		ps.add(formatNode, at+".format", "a format has exactly one %%s, where the value goes, and no other %%")
		r.format = ""
	}
	ps.cur.eachRefs = append(ps.cur.eachRefs, r)
	placeholder := "{" + r.key + "}"
	if r.format != "" {
		return strings.Replace(r.format, "%s", placeholder, 1)
	}
	return placeholder
}

// ---- expansion ----

// loopLookup gives the values of a loop list. ok is false when the values
// are not known (a preview without the input).
type loopLookup func(l LoopList) (values []string, ok bool, msg string)

// expandLoops replaces every step with for_each in a list of steps (one
// file's: the pipeline's or a template's) by its members, and makes steps
// that needed it need them all. prefix is the template's place in the run
// ("" for the pipeline), for the length of IDs and the loops' IDs. With
// preview, a loop whose values are not known stands aside, as a template
// step whose if is false does.
func expandLoops(steps []*Step, prefix string, limit int, lookup loopLookup, preview bool) ([]*Step, []*Loop, Problems) {
	var problems Problems
	var loops []*Loop
	replace := map[string][]string{}
	var out []*Step
	for _, s := range steps {
		if s.ForEach == nil || dropped(s) {
			out = append(out, s)
			continue
		}
		f := s.ForEach
		at := "steps." + s.ID + ".for_each"
		lists := make([][]string, len(f.Keys))
		known := true
		for i, l := range f.Lists {
			vals, ok, msg := lookup(l)
			if msg != "" {
				problems = append(problems, Problem{Line: f.Line, Path: at + "." + f.Keys[i], Message: msg})
				known = false
				continue
			}
			if !ok {
				known = false
				continue
			}
			for j, v := range vals {
				if m := loopValueProblem(v); m != "" {
					problems = append(problems, Problem{Line: f.Line, Path: at + "." + f.Keys[i], Message: m})
					known = false
				} else if slices.Contains(vals[:j], v) {
					problems = append(problems, Problem{Line: f.Line, Path: at + "." + f.Keys[i], Message: fmt.Sprintf("%q is in the list twice", v)})
					known = false
				}
			}
			lists[i] = vals
		}
		if !known {
			replace[s.ID] = s.Needs
			continue
		}
		if !f.literal() {
			for _, p := range f.extraProblems(lists) {
				problems = append(problems, Problem{Line: p.line, Path: p.path(at[:strings.LastIndex(at, ".")]), Message: p.msg})
			}
		}
		combos := f.combinations(lists)
		switch {
		case len(combos) == 0 && preview:
			replace[s.ID] = s.Needs
			continue
		case len(combos) == 0:
			problems = append(problems, Problem{Line: f.Line, Path: at, Message: "the loop has nothing to run: " + emptyWhy(f, lists)})
			continue
		case len(combos) > limit:
			problems = append(problems, Problem{Line: f.Line, Path: at, Message: fmt.Sprintf(
				"the loop makes %d combinations; this server allows at most %d per loop", len(combos), limit)})
			continue
		}
		loop := &Loop{ID: prefix + s.ID, Description: s.Description, Keys: slices.Clone(f.Keys), MaxParallel: f.MaxParallel, FailFast: f.FailFast, Serial: f.Serial}
		var ids []string
		envNames := map[string]string{}
		for _, combo := range combos {
			parts := make([]string, len(f.Keys))
			for i, k := range f.Keys {
				parts[i] = k + "=" + combo[k]
			}
			id := s.ID + "[" + strings.Join(parts, ",") + "]"
			if len(prefix+id) > MaxStepIDLen {
				problems = append(problems, Problem{Line: s.Line, Path: "steps." + s.ID, Message: fmt.Sprintf(
					"expanded, the step id %s is longer than %d characters; use shorter names or values", clipText(prefix+id, 60), MaxStepIDLen)})
				break
			}
			env := StepEnvName(id)
			if other, dup := envNames[env]; dup {
				problems = append(problems, Problem{Line: f.Line, Path: at, Message: fmt.Sprintf(
					"%s and %s would both have outputs named LAUNCHWAY_NEEDS_%s_…; make the values differ in letters or digits", other, id, env)})
				break
			}
			envNames[env] = id
			allowed := f.toleratesFailure(combo)
			m := member(s, id, combo, allowed)
			for _, ca := range m.Cache {
				for _, msg := range CacheProblems(ca) {
					problems = append(problems, Problem{Line: ca.Line, Path: "steps." + s.ID + ".cache", Message: "for " + id + ": " + msg})
				}
			}
			if msg := m.stepRunnerProblems(); msg != "" {
				problems = append(problems, Problem{Line: s.Line, Path: "steps." + s.ID + ".runner", Message: id + ": " + msg})
				break
			}
			if f.Serial && len(ids) > 0 {
				m.Needs = append(m.Needs, ids[len(ids)-1])
			}
			out = append(out, m)
			ids = append(ids, id)
			loop.Members = append(loop.Members, LoopMember{ID: prefix + id, Each: combo,
				Extra: len(f.Add) > 0 && f.isExtra(combo, lists), AllowedFailure: allowed})
		}
		replace[s.ID] = ids
		loops = append(loops, loop)
	}
	if len(replace) > 0 {
		for _, s := range out {
			s.Needs = rewriteNeeds(s.Needs, replace)
		}
	}
	return out, loops, problems
}

// rewriteNeeds replaces loops by their members (or, for a loop that stands
// aside, by what it needed) in a list of needs.
func rewriteNeeds(needs []string, replace map[string][]string) []string {
	if len(needs) == 0 {
		return needs
	}
	var out []string
	var add func(id string, depth int)
	add = func(id string, depth int) {
		r, ok := replace[id]
		if !ok || depth > len(replace) {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
			return
		}
		for _, x := range r {
			if x == id {
				if !slices.Contains(out, id) {
					out = append(out, id)
				}
				continue
			}
			add(x, depth+1)
		}
	}
	for _, n := range needs {
		add(n, 0)
	}
	return out
}

// member makes one member of a looped step.
func member(s *Step, id string, each map[string]string, allowed bool) *Step {
	c := *s
	c.ID, c.ForEach, c.Each = id, nil, each
	c.Env = cloneMap(s.Env)
	c.With = cloneMap(s.With)
	c.Needs = slices.Clone(s.Needs)
	c.Cache = cloneCaches(s.Cache)
	for _, r := range s.eachRefs {
		v := each[r.key]
		if r.format != "" {
			v = strings.Replace(r.format, "%s", v, 1)
		}
		switch r.field {
		case "image":
			c.Image = v
		case "env":
			c.Env[r.name] = v
		case "with":
			c.With[r.name] = v
		case cacheKeyDeferred:
			setCacheValue(c.Cache, r.name, v)
		case "runner":
			idx, _ := strconv.Atoi(r.name)
			c.eachRunner(idx, v)
		}
	}
	c.eachRefs = nil
	if allowed {
		c.tolerate()
	}
	return &c
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// product lists the combinations of the lists' values, the first key
// varying slowest, without those an exclude entry matches.
func product(keys []string, lists [][]string, exclude []map[string]string) []map[string]string {
	combos := []map[string]string{{}}
	for i, k := range keys {
		var next []map[string]string
		for _, c := range combos {
			for _, v := range lists[i] {
				n := make(map[string]string, len(c)+1)
				for a, b := range c {
					n[a] = b
				}
				n[k] = v
				next = append(next, n)
			}
		}
		combos = next
		if len(combos) > MaxLoopMembersLimit*4 {
			// Far over any limit; stop multiplying.
			break
		}
	}
	out := combos[:0]
	for _, c := range combos {
		if !excluded(c, exclude) {
			out = append(out, c)
		}
	}
	return out
}

func excluded(c map[string]string, exclude []map[string]string) bool {
	for _, e := range exclude {
		if len(e) == 0 {
			continue
		}
		match := true
		for k, v := range e {
			if c[k] != v {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func emptyWhy(f *ForEach, lists [][]string) string {
	for i, l := range lists {
		if len(l) == 0 {
			if f.Lists[i].Input != "" {
				return fmt.Sprintf("the input %s, which gives the list %s, is empty", f.Lists[i].Input, f.Keys[i])
			}
			return fmt.Sprintf("the list %s is empty", f.Keys[i])
		}
	}
	return "exclude leaves out every combination"
}
