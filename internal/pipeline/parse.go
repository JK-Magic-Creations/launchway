package pipeline

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults and limits.
const (
	DefaultTimeout = time.Hour
	MaxTimeout     = 24 * time.Hour
	MaxRetry       = 10
)

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	stepIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Problem is one thing wrong with a pipeline file, with where it is.
type Problem struct {
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	var b strings.Builder
	if p.Line > 0 {
		fmt.Fprintf(&b, "line %d: ", p.Line)
	}
	if p.Path != "" {
		b.WriteString(p.Path + ": ")
	}
	b.WriteString(p.Message)
	return b.String()
}

// Problems is every problem found. It is returned as an error.
type Problems []Problem

func (ps Problems) Error() string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}

type parser struct {
	problems Problems
}

func (ps *parser) add(n *yaml.Node, at, format string, args ...any) {
	p := Problem{Path: at, Message: fmt.Sprintf(format, args...)}
	if n != nil {
		p.Line, p.Column = n.Line, n.Column
	}
	ps.problems = append(ps.problems, p)
}

// Parse reads and validates a pipeline file. On failure the error is Problems.
func Parse(data []byte) (*Pipeline, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Problems{{Message: strings.TrimPrefix(err.Error(), "yaml: ")}}
	}
	ps := &parser{}
	if len(doc.Content) == 0 {
		ps.add(nil, "", "the file is empty")
		return nil, ps.problems
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		ps.add(root, "", "expected a mapping with pipeline, on and steps")
		return nil, ps.problems
	}

	p := &Pipeline{}
	var stepsNode *yaml.Node
	ps.eachKey(root, "", []string{"pipeline", "on", "containers", "steps"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "pipeline":
			p.Name = ps.str(v, "pipeline")
		case "on":
			ps.parseOn(v, p)
		case "containers":
			ps.parseContainers(v, p)
		case "steps":
			stepsNode = v
		}
	})
	if p.Name == "" {
		ps.add(root, "pipeline", "a name is required, for example: pipeline: my-service")
	} else if !nameRe.MatchString(p.Name) {
		ps.add(root, "pipeline", "%q is not a valid name: use lower-case letters, digits and hyphens", p.Name)
	}
	if stepsNode == nil {
		ps.add(root, "steps", "at least one step is required")
	} else {
		ps.parseSteps(stepsNode, p)
	}
	ps.check(p)
	if len(ps.problems) > 0 {
		sort.SliceStable(ps.problems, func(i, j int) bool { return ps.problems[i].Line < ps.problems[j].Line })
		return nil, ps.problems
	}
	return p, nil
}

// eachKey walks a mapping, reporting unknown and repeated keys.
func (ps *parser) eachKey(m *yaml.Node, at string, known []string, fn func(k, v *yaml.Node)) {
	if m.Kind != yaml.MappingNode {
		ps.add(m, at, "expected a mapping")
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		where := join(at, k.Value)
		if seen[k.Value] {
			ps.add(k, where, "%q is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		if !contains(known, k.Value) {
			if hint, ok := notYet[k.Value]; ok {
				ps.add(k, where, "%q is not supported yet (%s)", k.Value, hint)
			} else {
				ps.add(k, where, "unknown key %q; expected one of: %s", k.Value, strings.Join(known, ", "))
			}
			continue
		}
		fn(k, v)
	}
}

// Keys from the design that are recognised but not implemented yet.
var notYet = map[string]string{
	"uses":     "templates and libraries arrive in a later milestone",
	"with":     "templates and libraries arrive in a later milestone",
	"kind":     "release, promote, approval and input nodes arrive in a later milestone",
	"outputs":  "write outputs to the file named by $LAUNCHWAY_OUTPUTS instead",
	"runs_on":  "runner pools arrive in a later milestone",
	"matrix":   "matrix expansion arrives in a later milestone",
	"inputs":   "run inputs arrive in a later milestone",
	"schedule": "scheduled triggers arrive in a later milestone",
}

func (ps *parser) parseOn(n *yaml.Node, p *Pipeline) {
	ps.eachKey(n, "on", []string{"push"}, func(k, v *yaml.Node) {
		t := &PushTrigger{}
		ps.eachKey(v, "on.push", []string{"branches", "tags"}, func(k2, v2 *yaml.Node) {
			list := ps.strList(v2, "on.push."+k2.Value)
			for _, pat := range list {
				ps.checkPattern(v2, "on.push."+k2.Value, pat)
			}
			if k2.Value == "branches" {
				t.Branches = list
			} else {
				t.Tags = list
			}
		})
		if len(t.Branches) == 0 && len(t.Tags) == 0 {
			ps.add(v, "on.push", "list branches or tags, for example: push: { branches: [main] }")
		}
		p.On.Push = t
	})
}

var (
	stepKeys      = []string{"container", "image", "run", "needs", "when", "env", "secrets", "retry", "timeout"}
	secretKeys    = []string{"store", "key", "field"}
	storeNameRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	containerKeys = []string{"image", "env", "cpu", "memory", "service", "command"}
	containerRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	quantityRe    = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(m|k|M|G|T|Ki|Mi|Gi|Ti)?$`)
	reservedNames = map[string]bool{"workspace": true}
)

func (ps *parser) parseContainers(n *yaml.Node, p *Pipeline) {
	if n.Kind != yaml.MappingNode {
		ps.add(n, "containers", "containers is a mapping of names to containers")
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		at := "containers." + k.Value
		switch {
		case seen[k.Value]:
			ps.add(k, at, "container %q is written twice", k.Value)
			continue
		case !containerRe.MatchString(k.Value):
			ps.add(k, at, "%q is not a valid container name: use lower-case letters, digits and hyphens", k.Value)
		case reservedNames[k.Value] || strings.HasPrefix(k.Value, "image-"):
			ps.add(k, at, "%q is reserved; choose another name", k.Value)
		}
		seen[k.Value] = true
		c := &Container{Name: k.Value}
		ps.eachKey(v, at, containerKeys, func(k2, v2 *yaml.Node) {
			where := at + "." + k2.Value
			switch k2.Value {
			case "image":
				c.Image = ps.str(v2, where)
			case "env":
				c.Env = ps.parseEnv(v2, where)
			case "cpu", "memory":
				q := ps.str(v2, where)
				if !quantityRe.MatchString(q) {
					ps.add(v2, where, "%q is not a size; write for example 500m or 2 for cpu, 512Mi or 2Gi for memory", q)
				}
				if k2.Value == "cpu" {
					c.CPU = q
				} else {
					c.Memory = q
				}
			case "service":
				switch ps.str(v2, where) {
				case "true", "yes":
					c.Service = true
				case "false", "no":
				default:
					ps.add(v2, where, "write true or false")
				}
			case "command":
				c.Command = ps.strList(v2, where)
			}
		})
		if strings.TrimSpace(c.Image) == "" {
			ps.add(k, at+".image", "an image is required")
		}
		if len(c.Command) > 0 && !c.Service {
			ps.add(k, at+".command", "only a service has its own command; steps say what to run with run")
		}
		p.Containers = append(p.Containers, c)
	}
}

func (ps *parser) parseSteps(n *yaml.Node, p *Pipeline) {
	if n.Kind != yaml.MappingNode {
		ps.add(n, "steps", "steps is a mapping of step names to steps")
		return
	}
	if len(n.Content) == 0 {
		ps.add(n, "steps", "at least one step is required")
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		at := "steps." + k.Value
		if seen[k.Value] {
			ps.add(k, at, "step %q is written twice", k.Value)
			continue
		}
		seen[k.Value] = true
		if !stepIDRe.MatchString(k.Value) {
			ps.add(k, at, "%q is not a valid step name: use lower-case letters, digits, hyphens and underscores", k.Value)
		}
		s := &Step{ID: k.Value, Line: k.Line, Timeout: Duration(DefaultTimeout)}
		var secretsNode *yaml.Node
		ps.eachKey(v, at, stepKeys, func(k2, v2 *yaml.Node) {
			where := at + "." + k2.Value
			switch k2.Value {
			case "container":
				s.Container = ps.str(v2, where)
			case "image":
				s.Image = ps.str(v2, where)
			case "run":
				s.Run = ps.str(v2, where)
			case "needs":
				s.Needs = ps.strList(v2, where)
			case "when":
				s.When = ps.parseWhen(v2, where)
			case "env":
				s.Env = ps.parseEnv(v2, where)
			case "secrets":
				secretsNode = v2
				s.Secrets = ps.parseSecrets(v2, where)
			case "retry":
				s.Retry = ps.int(v2, where)
				if s.Retry < 0 || s.Retry > MaxRetry {
					ps.add(v2, where, "retry must be between 0 and %d", MaxRetry)
				}
			case "timeout":
				d, err := time.ParseDuration(ps.str(v2, where))
				switch {
				case err != nil:
					ps.add(v2, where, "write a duration such as 30s, 10m or 1h")
				case d < time.Second || d > MaxTimeout:
					ps.add(v2, where, "timeout must be between 1s and %s", MaxTimeout)
				default:
					s.Timeout = Duration(d)
				}
			}
		})
		switch {
		case s.Container != "" && s.Image != "":
			ps.add(k, at, "give either container or image, not both")
		case s.Container != "":
			switch c := p.Container(s.Container); {
			case c == nil:
				ps.add(k, at+".container", "there is no container called %q under containers", s.Container)
			case c.Service:
				ps.add(k, at+".container", "%q is a service; steps cannot run in it", s.Container)
			default:
				s.Image = c.Image
			}
		case strings.TrimSpace(s.Image) == "":
			ps.add(k, at, "say what to run in, for example: image: alpine:3.22, or container: one of the containers above")
		}
		if strings.TrimSpace(s.Run) == "" {
			ps.add(k, at+".run", "a script to run is required")
		}
		if secretsNode != nil && secretsNode.Kind == yaml.MappingNode {
			for i := 0; i < len(secretsNode.Content); i += 2 {
				name := secretsNode.Content[i]
				if _, ok := s.Env[name.Value]; ok {
					ps.add(name, at+".secrets."+name.Value, "%q is also set under env; a variable comes from one or the other", name.Value)
				}
			}
		}
		p.Steps = append(p.Steps, s)
	}
}

func (ps *parser) parseWhen(n *yaml.Node, at string) *Condition {
	c := &Condition{}
	ps.eachKey(n, at, []string{"branch", "tag", "event"}, func(k, v *yaml.Node) {
		list := ps.strList(v, at+"."+k.Value)
		switch k.Value {
		case "branch":
			c.Branch = list
		case "tag":
			c.Tag = list
		case "event":
			for _, e := range list {
				if e != "push" && e != "manual" {
					ps.add(v, at+".event", "event %q is not known; use push or manual", e)
				}
			}
			c.Event = list
		}
		for _, pat := range list {
			ps.checkPattern(v, at+"."+k.Value, pat)
		}
	})
	return c
}

func (ps *parser) parseEnv(n *yaml.Node, at string) map[string]string {
	env := map[string]string{}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "env is a mapping of names to values")
		return env
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch {
		case !envKeyRe.MatchString(k.Value):
			ps.add(k, at+"."+k.Value, "%q is not a valid variable name", k.Value)
		case strings.HasPrefix(strings.ToUpper(k.Value), "LAUNCHWAY_"):
			ps.add(k, at+"."+k.Value, "names starting with LAUNCHWAY_ are reserved")
		}
		env[k.Value] = ps.str(v, at+"."+k.Value)
	}
	return env
}

// ValidStoreName reports whether name can name a secret store.
func ValidStoreName(name string) bool { return storeNameRe.MatchString(name) }

// parseSecrets reads a mapping of variable names to secret references.
func (ps *parser) parseSecrets(n *yaml.Node, at string) map[string]SecretRef {
	out := map[string]SecretRef{}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "secrets is a mapping of variable names to references, for example: TOKEN: { store: vault, key: apps/x, field: token }")
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		where := at + "." + k.Value
		switch _, dup := out[k.Value]; {
		case dup:
			ps.add(k, where, "%q is written twice", k.Value)
			continue
		case !envKeyRe.MatchString(k.Value):
			ps.add(k, where, "%q is not a valid variable name", k.Value)
		case strings.HasPrefix(strings.ToUpper(k.Value), "LAUNCHWAY_"):
			ps.add(k, where, "names starting with LAUNCHWAY_ are reserved")
		}
		var ref SecretRef
		ps.eachKey(v, where, secretKeys, func(k2, v2 *yaml.Node) {
			switch k2.Value {
			case "store":
				ref.Store = ps.str(v2, where+".store")
			case "key":
				ref.Key = ps.str(v2, where+".key")
			case "field":
				ref.Field = ps.str(v2, where+".field")
			}
		})
		if v.Kind == yaml.MappingNode {
			if !ValidStoreName(ref.Store) {
				ps.add(v, where+".store", "name the store, in lower case, for example: store: vault")
			}
			if strings.TrimSpace(ref.Key) == "" {
				ps.add(v, where+".key", "a key is required")
			}
		}
		out[k.Value] = ref
	}
	return out
}

func (ps *parser) checkPattern(n *yaml.Node, at, pattern string) {
	if _, err := path.Match(pattern, ""); err != nil {
		ps.add(n, at, "%q is not a valid pattern", pattern)
	}
}

func (ps *parser) str(n *yaml.Node, at string) string {
	if n.Kind != yaml.ScalarNode {
		ps.add(n, at, "expected a single value")
		return ""
	}
	return n.Value
}

func (ps *parser) int(n *yaml.Node, at string) int {
	v, err := strconv.Atoi(ps.str(n, at))
	if err != nil {
		ps.add(n, at, "expected a whole number")
	}
	return v
}

// strList accepts either one value or a list of values.
func (ps *parser) strList(n *yaml.Node, at string) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" {
			return nil
		}
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				ps.add(c, at, "expected a list of values")
				continue
			}
			out = append(out, c.Value)
		}
		return out
	default:
		ps.add(n, at, "expected a value or a list of values")
		return nil
	}
}

// check validates the graph as a whole.
func (ps *parser) check(p *Pipeline) {
	ids := map[string]*Step{}
	for _, s := range p.Steps {
		ids[s.ID] = s
	}
	for _, s := range p.Steps {
		seen := map[string]bool{}
		for _, n := range s.Needs {
			at := "steps." + s.ID + ".needs"
			switch {
			case n == s.ID:
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: at, Message: "a step cannot need itself"})
			case ids[n] == nil:
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: at, Message: fmt.Sprintf("needs %q, but there is no step with that name", n)})
			case seen[n]:
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: at, Message: fmt.Sprintf("%q is listed twice", n)})
			}
			seen[n] = true
		}
	}
	// Report each cycle once, naming the steps in it.
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var visit func(s *Step) bool
	visit = func(s *Step) bool {
		color[s.ID] = grey
		stack = append(stack, s.ID)
		for _, n := range s.Needs {
			dep := ids[n]
			if dep == nil || n == s.ID {
				continue
			}
			if color[n] == grey {
				start := 0
				for i, id := range stack {
					if id == n {
						start = i
					}
				}
				cycle := append(append([]string{}, stack[start:]...), n)
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: "steps." + s.ID + ".needs",
					Message: "steps need each other in a loop: " + strings.Join(cycle, " → ")})
				return true
			}
			if color[n] == white && visit(dep) {
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[s.ID] = black
		return false
	}
	for _, s := range p.Steps {
		if color[s.ID] == white {
			stack = stack[:0]
			if visit(s) {
				return
			}
		}
	}
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
