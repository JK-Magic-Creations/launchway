package pipeline

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Defaults and limits.
const (
	DefaultTimeout = time.Hour
	MaxTimeout     = 24 * time.Hour
	MaxRetry       = 10

	DefaultApprovalTimeout = 24 * time.Hour
	MaxApprovalTimeout     = 30 * 24 * time.Hour
	MaxMessageLen          = 500 // characters in an approval's message
	MaxDescriptionLen      = 200 // characters in a step's description

	// MaxFileBytes bounds a pipeline file. The server refuses larger text
	// from the editor; a file in Git that is larger is refused too.
	MaxFileBytes = 256 << 10
)

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	stepIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Problem is one thing wrong with a pipeline file, with where it is.
type Problem struct {
	// File is set when the problem is in a template rather than in the
	// pipeline file: the template's file, in Library (the name the pipeline
	// gives the library). Line and Column are then in that file.
	File    string `json:"file,omitempty"`
	Library string `json:"library,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	var b strings.Builder
	if p.File != "" {
		fmt.Fprintf(&b, "%s (library %s): ", p.File, p.Library)
	}
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
	// tmpl is set while reading a template: the parameters it declares and
	// the values they have for the use being expanded.
	tmpl *paramScope

	// cur is the step being read, and curField and curName the value in it
	// being read, for { each: } (see loops.go).
	cur               *Step
	curField, curName string
	// References to inputs, checked once every input is known (bind.go).
	loopInputs []loopInputRef
	withInputs []withInputRef
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
	if len(data) > MaxFileBytes {
		return nil, Problems{{Message: fmt.Sprintf("the file is larger than %d KiB", MaxFileBytes>>10)}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Problems{syntaxProblem(err)}
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
	var stepsNode, laneNode *yaml.Node
	ps.eachKey(root, "", []string{"pipeline", "on", "supersede", "libraries", "containers", "steps", "inputs", "runner", "timeout", "lane"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "lane":
			laneNode = v
		case "timeout":
			ps.parseRunTimeout(p, v)
		case "runner":
			ps.parseRunner(v, p)
		case "pipeline":
			p.Name = ps.str(v, "pipeline")
		case "supersede":
			p.Supersede = ps.bool(v, "supersede")
		case "on":
			ps.parseOn(v, p)
		case "libraries":
			ps.parseLibraries(v, p)
		case "containers":
			ps.parseContainers(v, p)
		case "steps":
			stepsNode = v
		case "inputs":
			ps.parseInputs(v, p)
		}
	})
	if laneNode != nil {
		ps.parseLane(p, laneNode) // after supersede, which it may not be written with
	}
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
	if ps.check(p) {
		ps.checkOutputTests(p)
		ps.checkDeploys(p)
		ps.checkImages(p)
		ps.checkVerify(p)
		ps.checkAI(p)
	}
	ps.checkUses(p)
	ps.checkInputs(p)
	ps.checkLane(p)
	ps.checkArtifacts(p)
	if len(ps.problems) > 0 {
		sort.SliceStable(ps.problems, func(i, j int) bool { return ps.problems[i].Line < ps.problems[j].Line })
		return nil, ps.problems
	}
	if p.UsesSchemaControl() {
		p.Requires = SchemaControl
	}
	if p.UsesSchema15() {
		p.Requires = SchemaImages
	}
	if p.UsesSchemaVerify() {
		p.Requires = SchemaVerify
	}
	if p.UsesSchemaPools() {
		p.Requires = SchemaPools
	}
	p.Requires = p.requiresNewer(p.Requires) // step resources, build caches
	p.requireFlow()
	return p, nil
}

// yamlLine finds the line number in a YAML error, such as
// "yaml: line 4: did not find expected key".
var yamlLine = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

// syntaxProblem turns a YAML syntax error into a problem, with its line
// when the error names one.
func syntaxProblem(err error) Problem {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	if m := yamlLine.FindStringSubmatch(msg); m != nil {
		if n, convErr := strconv.Atoi(m[1]); convErr == nil {
			return Problem{Line: n, Message: m[2]}
		}
	}
	return Problem{Message: msg}
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
			} else if hint, ok := notHere[k.Value]; ok {
				ps.add(k, where, "%q is not allowed here: %s", k.Value, hint)
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
	"outputs": "write outputs to the file named by $LAUNCHWAY_OUTPUTS instead",
	"runs_on": "choose runners for the whole pipeline with runner: { pool: …, labels: [ … ] } at its top",
	"matrix":  "to repeat a step over lists, use for_each",
}

func (ps *parser) parseOn(n *yaml.Node, p *Pipeline) {
	ps.eachKey(n, "on", []string{"push", "pull_request", "schedule", "webhook"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "schedule":
			ps.parseSchedules(v, p)
			return
		case "webhook":
			ps.parseWebhooks(v, p)
			return
		}
		if k.Value == "pull_request" {
			p.On.PullRequest = ps.parsePullRequest(v)
			return
		}
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

// parsePullRequest reads on.pull_request: written pull_request: {} (or with
// no value at all) for every pull request, or with into: the branches a pull
// request must target. A pull request has no branch of its own that could be
// filtered, only the one it targets.
func (ps *parser) parsePullRequest(v *yaml.Node) *PullRequestTrigger {
	t := &PullRequestTrigger{}
	switch {
	case v.Kind == yaml.ScalarNode && v.Tag == "!!null":
	case v.Kind == yaml.MappingNode:
		// branches and tags are push's keys: say where a pull request's
		// filter is, instead of listing what is expected.
		rest := &yaml.Node{Kind: yaml.MappingNode, Line: v.Line, Column: v.Column}
		for i := 0; i+1 < len(v.Content); i += 2 {
			k := v.Content[i]
			if k.Value == "branches" || k.Value == "tags" {
				ps.add(k, "on.pull_request."+k.Value, "a pull request has no branch or tag of its own to filter: the server sees the branch it "+
					"targets. To run only for pull requests into some branches, write pull_request: { into: [main, \"release/*\"] }")
				continue
			}
			rest.Content = append(rest.Content, v.Content[i], v.Content[i+1])
		}
		ps.eachKey(rest, "on.pull_request", []string{"into"}, func(k, x *yaml.Node) {
			t.Into = ps.strList(x, "on.pull_request.into")
			for _, pat := range t.Into {
				ps.checkPattern(x, "on.pull_request.into", pat)
			}
			if len(t.Into) == 0 {
				ps.add(x, "on.pull_request.into", "list the branches pull requests may target, for example: into: [main]; or write pull_request: {} for any")
			}
		})
	default:
		ps.add(v, "on.pull_request", "write pull_request: {}, or pull_request: { into: [main] }")
	}
	return t
}

var (
	stepKeys = []string{"kind", "description", "container", "image", "run", "needs", "when", "env", "secrets", "retry", "timeout", "reports", "message", "uses", "with",
		"for_each", "exclude", "max_parallel", "fail_fast", "artifacts", "fetch", "deployment", "resources", "on_failure", "cache"}
	// useKeys are the keys a step that uses a template may have; the
	// template says everything else.
	useKeys = []string{"uses", "with", "description", "needs", "when", "retry", "timeout", "for_each", "exclude", "max_parallel", "fail_fast"}
	// approvalOnly and runOnly are the keys that make sense for one kind of
	// step and not the other.
	approvalOnly = []string{"message"}
	runOnly      = []string{"container", "image", "run", "env", "secrets", "retry", "reports", "artifacts", "fetch", "resources", "on_failure", "cache"}
	// Kinds from the design that are recognised but not implemented yet.
	kindsNotYet   = map[string]bool{"release": true, "promote": true, "input": true}
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
				c.Image = ps.scalar(v2, where)
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
		if kind := mappingValue(v, "kind"); kind == KindDeploy || kind == KindRollback || kind == KindRolloutStep {
			p.Steps = append(p.Steps, ps.parseDeployStep(k, v, at, kind))
			continue
		}
		if mappingValue(v, "kind") == KindImage {
			p.Steps = append(p.Steps, ps.parseImageStep(k, v, at))
			continue
		}
		if mappingValue(v, "kind") == KindVerify {
			p.Steps = append(p.Steps, ps.parseVerifyStep(k, v, at))
			continue
		}
		if mappingValue(v, "kind") == KindAI {
			p.Steps = append(p.Steps, ps.parseAIStep(k, v, at))
			continue
		}
		s := &Step{ID: k.Value, Line: k.Line}
		var secretsNode, kindNode, timeoutNode *yaml.Node
		var timeout time.Duration
		keys := map[string]*yaml.Node{} // the keys written, for checks that depend on the kind
		vals := map[string]*yaml.Node{} // and their values
		ps.cur = s
		known := stepKeys
		if ps.tmpl != nil {
			known = append(append([]string{}, stepKeys...), "if")
		}
		ps.eachKey(v, at, known, func(k2, v2 *yaml.Node) {
			keys[k2.Value] = k2
			vals[k2.Value] = v2
			where := at + "." + k2.Value
			switch k2.Value {
			case "for_each":
				s.ForEach = ps.parseForEach(v2, where)
			case "kind":
				kindNode = v2
				s.Kind = ps.str(v2, where)
			case "container":
				s.Container = ps.str(v2, where)
			case "image":
				ps.curField = "image"
				s.Image = ps.scalar(v2, where)
				ps.curField = ""
			case "run":
				s.Run = ps.str(v2, where)
			case "needs":
				s.Needs = ps.strList(v2, where)
				s.NeedsLine = v2.Line
			case "when":
				s.When = ps.parseWhen(v2, where)
			case "env":
				s.Env = ps.parseEnv(v2, where)
			case "secrets":
				secretsNode = v2
				s.Secrets = ps.parseSecrets(v2, where)
			case "retry":
				ps.parseRetry(s, v2, where)
			case "timeout":
				d, err := time.ParseDuration(ps.scalar(v2, where))
				if err != nil {
					ps.add(v2, where, "write a duration such as 30s, 10m or 1h")
				} else {
					timeoutNode, timeout = v2, d
					s.timeoutSet = true
				}
			case "on_failure":
				ps.parseOnFailure(s, v2, where)
			case "reports":
				s.Reports = ps.parseReports(v2, where)
			case "resources":
				s.Resources = ps.parseResources(v2, where)
				s.resourcesLine = v2.Line
			case "artifacts", "cache":
				ps.parseKept(s, k2.Value, v2, where) // files for the run's later steps, or caches for later runs
			case "fetch":
				s.Fetch = ps.parseFetch(v2, where)
			case "uses":
				s.Uses = ps.str(v2, where)
				s.usesLine = v2.Line
			case "with":
				s.With, s.withLines = ps.parseWith(v2, where)
			case "if":
				s.ifTest = ps.parseIf(v2, where)
			case "message":
				s.Message = strings.TrimSpace(ps.str(v2, where))
			case "deployment":
				s.Deployment = ps.str(v2, where)
				s.deploymentLine = v2.Line
			case "description":
				s.Description = strings.TrimSpace(ps.str(v2, where))
				switch {
				case strings.ContainsAny(s.Description, "\r\n"):
					ps.add(v2, where, "a description is one line; put longer notes in a comment")
				case utf8.RuneCountInString(s.Description) > MaxDescriptionLen:
					ps.add(v2, where, "the description is longer than %d characters", MaxDescriptionLen)
				}
			}
		})
		ps.parseLoopSettings(s, at, keys, vals)
		ps.cur = nil
		if keys["with"] != nil && keys["uses"] == nil {
			ps.add(keys["with"], at+".with", "with passes values to a template, so it goes with uses")
		}
		switch {
		case keys["uses"] != nil:
			ps.parseUseStep(at, s, keys)
		case s.Kind == "" || s.Kind == KindStep:
			s.Kind = "" // the default is not written into the graph
			ps.parseRunStep(k, at, s, p, keys, secretsNode)
		case s.Kind == KindApproval:
			ps.parseApproval(k, at, s, keys)
		case s.Kind == "analysis":
			ps.add(kindNode, at+".kind", "write kind: verify: Launchway judges the metrics and logs itself (see verify steps)")
		case kindsNotYet[s.Kind]:
			ps.add(kindNode, at+".kind", "kind %q is not supported yet (release, promote, analysis and input steps arrive in a later milestone)", s.Kind)
		default:
			ps.add(kindNode, at+".kind", "unknown kind %q; use step, approval, image, deploy, rollout, rollback, verify or ai", s.Kind)
		}
		def, most := DefaultTimeout, MaxTimeout
		if s.IsApproval() {
			def, most = DefaultApprovalTimeout, MaxApprovalTimeout
		}
		s.Timeout = Duration(def)
		if timeoutNode != nil {
			if timeout < time.Second || timeout > most {
				ps.add(timeoutNode, at+".timeout", "timeout must be between 1s and %s", Duration(most).Short())
			} else {
				s.Timeout = Duration(timeout)
			}
		}
		p.Steps = append(p.Steps, s)
	}
}

// parseRunStep checks a step that runs a script.
func (ps *parser) parseRunStep(k *yaml.Node, at string, s *Step, p *Pipeline, keys map[string]*yaml.Node, secretsNode *yaml.Node) {
	if n := keys["deployment"]; n != nil {
		ps.add(n, at+".deployment", "deployment links an approval, rollout or rollback step to a deploy step; a step that runs a script takes no deployment")
	}
	for _, key := range approvalOnly {
		if n := keys[key]; n != nil {
			ps.add(n, at+"."+key, "%q is only for approval steps (kind: approval)", key)
		}
	}
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
}

// parseApproval checks a step that waits for a person to decide.
func (ps *parser) parseApproval(k *yaml.Node, at string, s *Step, keys map[string]*yaml.Node) {
	if n := keys["deployment"]; n != nil && ps.tmpl != nil {
		ps.add(n, at+".deployment", "an approval in a template cannot name a deployment; write it in the pipeline that uses the template")
	}
	for _, key := range runOnly {
		if n := keys[key]; n != nil {
			ps.add(n, at+"."+key, "an approval runs nothing, so it takes no %s", key)
		}
	}
	switch {
	case s.Message == "":
		ps.add(k, at+".message", "an approval needs a message saying what to decide, for example: message: Deploy to production?")
	case strings.ContainsAny(s.Message, "\r\n"):
		ps.add(keys["message"], at+".message", "the message must be one line")
	case utf8.RuneCountInString(s.Message) > MaxMessageLen:
		ps.add(keys["message"], at+".message", "the message is longer than %d characters", MaxMessageLen)
	}
}

func (ps *parser) parseWhen(n *yaml.Node, at string) *Condition {
	c := &Condition{}
	ps.eachKey(n, at, []string{"branch", "tag", "event", "input", "output", "equals", "upstream"}, func(k, v *yaml.Node) {
		if k.Value == "upstream" {
			c.Upstream = ps.str(v, at+".upstream")
			if !slices.Contains(Upstreams, c.Upstream) {
				ps.add(v, at+".upstream", "upstream is succeeded (the default), failed or always")
			}
			return
		}
		if k.Value == "input" || k.Value == "output" || k.Value == "equals" {
			ps.parseInputTest(c, k, v, at)
			return
		}
		list := ps.valueList(v, at+"."+k.Value)
		switch k.Value {
		case "branch":
			c.Branch = list
		case "tag":
			c.Tag = list
		case "event":
			for _, e := range list {
				if !contains(Events, e) {
					ps.add(v, at+".event", "event %q is not known; use %s", e, strings.Join(Events, ", "))
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
		case ps.tmpl != nil && strings.HasPrefix(strings.ToUpper(k.Value), ParamEnvPrefix):
			ps.add(k, at+"."+k.Value, "in a template, names starting with %s are the template's parameters", ParamEnvPrefix)
		}
		if ps.cur != nil {
			ps.curField, ps.curName = "env", k.Value
		}
		env[k.Value] = ps.scalar(v, at+"."+k.Value)
		ps.curField, ps.curName = "", ""
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
		case ps.tmpl != nil && strings.HasPrefix(strings.ToUpper(k.Value), ParamEnvPrefix):
			ps.add(k, where, "in a template, names starting with %s are the template's parameters", ParamEnvPrefix)
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

func (ps *parser) bool(n *yaml.Node, at string) bool {
	switch ps.str(n, at) {
	case "true", "yes":
		return true
	case "false", "no":
		return false
	}
	ps.add(n, at, "write true or false")
	return false
}

func (ps *parser) int(n *yaml.Node, at string) int {
	v, err := strconv.Atoi(ps.scalar(n, at))
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

// check validates the graph as a whole. It reports whether the needs are
// sound (every one names a step, and there is no loop), so that checks that
// walk the graph can follow.
func (ps *parser) check(p *Pipeline) bool {
	before := len(ps.problems)
	ids := map[string]*Step{}
	for _, s := range p.Steps {
		ids[s.ID] = s
	}
	for _, s := range p.Steps {
		seen := map[string]bool{}
		line := s.Line
		if s.NeedsLine > 0 {
			line = s.NeedsLine
		}
		for _, n := range s.Needs {
			at := "steps." + s.ID + ".needs"
			switch {
			case n == s.ID:
				ps.problems = append(ps.problems, Problem{Line: line, Path: at, Message: "a step cannot need itself"})
			case ids[n] == nil:
				ps.problems = append(ps.problems, Problem{Line: line, Path: at, Message: fmt.Sprintf("needs %q, but there is no step with that name", n)})
			case seen[n]:
				ps.problems = append(ps.problems, Problem{Line: line, Path: at, Message: fmt.Sprintf("%q is listed twice", n)})
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
				return false
			}
		}
	}
	return len(ps.problems) == before
}

// mappingValue returns the scalar value of a key in a mapping, or "".
func mappingValue(m *yaml.Node, key string) string {
	if m.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key && m.Content[i+1].Kind == yaml.ScalarNode {
			return m.Content[i+1].Value
		}
	}
	return ""
}

// parseDeployStep reads a deploy or rollback step.
func (ps *parser) parseDeployStep(k, v *yaml.Node, at, kind string) *Step {
	s := &Step{ID: k.Value, Line: k.Line, Kind: kind}
	var timeoutNode *yaml.Node
	var timeout time.Duration
	seen := map[string]bool{}
	for i := 0; i+1 < len(v.Content); i += 2 {
		key, n := v.Content[i], v.Content[i+1]
		where := at + "." + key.Value
		if seen[key.Value] {
			ps.add(key, where, "%q is written twice", key.Value)
			continue
		}
		seen[key.Value] = true
		switch key.Value {
		case "needs":
			s.Needs = ps.strList(n, where)
			s.NeedsLine = n.Line
		case "when":
			if kind == KindDeploy {
				s.When = ps.parseWhen(n, where)
			}
		case "timeout":
			d, err := time.ParseDuration(ps.scalar(n, where))
			if err != nil {
				ps.add(n, where, "write a duration such as 30s, 10m or 1h")
			} else {
				timeoutNode, timeout = n, d
			}
		case "description":
			s.Description = strings.TrimSpace(ps.str(n, where))
			switch {
			case strings.ContainsAny(s.Description, "\r\n"):
				ps.add(n, where, "a description is one line; put longer notes in a comment")
			case utf8.RuneCountInString(s.Description) > MaxDescriptionLen:
				ps.add(n, where, "the description is longer than %d characters", MaxDescriptionLen)
			}
		}
	}
	ps.parseDeployKeys(v, at, s)
	ps.checkDeployStep(k, v, at, s)
	def := DefaultDeployTimeout
	if kind == KindRollback {
		def = DefaultRollbackTimeout
	}
	s.Timeout = Duration(def)
	if timeoutNode != nil {
		if timeout < time.Minute || timeout > MaxDeployTimeout {
			ps.add(timeoutNode, at+".timeout", "timeout must be between 1m and %s", Duration(MaxDeployTimeout).Short())
		} else {
			s.Timeout = Duration(timeout)
		}
	}
	return s
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
