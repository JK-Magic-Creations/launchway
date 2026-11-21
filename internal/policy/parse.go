package policy

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Problem is one mistake in a policy file, with where it is.
type Problem struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	where := p.File
	if p.Line > 0 {
		where += ":" + strconv.Itoa(p.Line)
	}
	if p.Path != "" {
		if where != "" {
			where += ": "
		}
		where += p.Path
	}
	if where == "" {
		return p.Message
	}
	return where + ": " + p.Message
}

// Problems are a policy file's mistakes. Any one refuses the whole file.
type Problems []Problem

func (ps Problems) Error() string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return strings.Join(out, "\n")
}

// parser walks YAML nodes, recording problems with their lines.
type parser struct {
	file     string
	problems Problems
}

func (ps *parser) add(n *yaml.Node, at, format string, a ...any) {
	line := 0
	if n != nil {
		line = n.Line
	}
	ps.problems = append(ps.problems, Problem{File: ps.file, Line: line, Path: at, Message: fmt.Sprintf(format, a...)})
}

// mapping calls fn for each key of a mapping, refusing keys not in known.
func (ps *parser) mapping(n *yaml.Node, at string, known []string, fn func(key string, k, v *yaml.Node)) {
	if n == nil {
		return
	}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "must be a mapping of %s", strings.Join(known, ", "))
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		key := k.Value
		switch {
		case !slices.Contains(known, key):
			ps.add(k, join(at, key), "unknown key %q; the keys here are %s", key, strings.Join(known, ", "))
		case seen[key]:
			ps.add(k, join(at, key), "written twice")
		default:
			seen[key] = true
			fn(key, k, v)
		}
	}
}

func join(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

func (ps *parser) str(n *yaml.Node, at string, max int) string {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		ps.add(n, at, "must be text")
		return ""
	}
	if len(n.Value) > max {
		ps.add(n, at, "is longer than %d characters", max)
		return ""
	}
	return n.Value
}

func (ps *parser) boolean(n *yaml.Node, at string) bool {
	var b bool
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" || n.Decode(&b) != nil {
		ps.add(n, at, "must be true or false")
		return false
	}
	return b
}

func (ps *parser) integer(n *yaml.Node, at string, min, max int) int {
	var i int
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || n.Decode(&i) != nil {
		ps.add(n, at, "must be a whole number")
		return 0
	}
	if i < min || i > max {
		ps.add(n, at, "must be between %d and %d", min, max)
		return 0
	}
	return i
}

// list reads a list of text values (one value alone is a list of one).
func (ps *parser) list(n *yaml.Node, at string, check func(v *yaml.Node, s string) bool) []string {
	items := []*yaml.Node{n}
	switch n.Kind {
	case yaml.SequenceNode:
		items = n.Content
	case yaml.ScalarNode:
	default:
		ps.add(n, at, "must be a list of text values")
		return nil
	}
	if len(items) > MaxPatterns {
		ps.add(n, at, "at most %d values", MaxPatterns)
		return nil
	}
	var out []string
	for i, v := range items {
		where := fmt.Sprintf("%s[%d]", at, i)
		s := ps.str(v, where, 200)
		if s == "" {
			if v.Kind == yaml.ScalarNode && v.Value == "" {
				ps.add(v, where, "must not be empty")
			}
			continue
		}
		if check != nil && !check(v, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// patterns reads a list of path.Match patterns.
func (ps *parser) patterns(n *yaml.Node, at string) []string {
	return ps.list(n, at, func(v *yaml.Node, s string) bool {
		if _, err := path.Match(s, ""); err != nil {
			ps.add(v, at, "%q is not a valid pattern", s)
			return false
		}
		return true
	})
}

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	toolNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+-]{0,99}$`)
)

// ValidName reports whether s can name a policy or a source.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Events a scope may name, as pipeline.Events lists them (the policy
// package does not import pipeline, so it keeps its own copy, checked by a
// test).
var Events = []string{"push", "manual", "draft", "pull_request", "schedule", "webhook"}

// ParseFile reads a policy file from a Git source: a mapping with one key,
// policies, a list of policies. source names the source.
func ParseFile(data []byte, file, source string) ([]Policy, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		var te *yaml.TypeError
		msg := err.Error()
		if errors.As(err, &te) {
			msg = strings.Join(te.Errors, "; ")
		}
		line := 0
		if m := regexp.MustCompile(`line (\d+)`).FindStringSubmatch(msg); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		return nil, Problems{{File: file, Line: line, Message: "not valid YAML: " + strings.TrimPrefix(msg, "yaml: ")}}
	}
	if len(doc.Content) == 0 {
		return nil, Problems{{File: file, Line: 1, Message: "the file is empty; write policies: followed by a list of policies"}}
	}
	ps := &parser{file: file}
	var out []Policy
	ps.mapping(doc.Content[0], "", []string{"policies"}, func(_ string, _, v *yaml.Node) {
		out = ps.policies(v, "policies", source)
	})
	if len(ps.problems) > 0 {
		return nil, ps.problems
	}
	return out, nil
}

// ParseList reads a list of policies from a YAML node, as the server's
// configuration holds them under policy.policies. Problems carry file and
// line.
func ParseList(n *yaml.Node, file, at, source string) ([]Policy, error) {
	ps := &parser{file: file}
	out := ps.policies(n, at, source)
	if len(ps.problems) > 0 {
		return nil, ps.problems
	}
	return out, nil
}

func (ps *parser) policies(n *yaml.Node, at, source string) []Policy {
	if n.Kind != yaml.SequenceNode {
		ps.add(n, at, "must be a list of policies")
		return nil
	}
	if len(n.Content) > MaxPolicies {
		ps.add(n, at, "at most %d policies", MaxPolicies)
		return nil
	}
	var out []Policy
	names := map[string]int{}
	for i, pn := range n.Content {
		where := fmt.Sprintf("%s[%d]", at, i)
		p := ps.policy(pn, where, source)
		if p.Name != "" {
			if line, dup := names[p.Name]; dup {
				ps.add(pn, where+".name", "policy %s is already defined on line %d", p.Name, line)
			}
			names[p.Name] = pn.Line
		}
		out = append(out, p)
	}
	return out
}

func (ps *parser) policy(n *yaml.Node, at, source string) Policy {
	p := Policy{Source: source, File: ps.file, Line: n.Line}
	var rulesNode *yaml.Node
	ps.mapping(n, at, []string{"name", "description", "mode", "scope", "rules"}, func(key string, k, v *yaml.Node) {
		where := join(at, key)
		switch key {
		case "name":
			if p.Name = ps.str(v, where, 63); p.Name != "" && !ValidName(p.Name) {
				ps.add(v, where, "%q: use lower-case letters, digits and hyphens (at most 63)", p.Name)
				p.Name = ""
			}
		case "description":
			p.Description = strings.TrimSpace(ps.str(v, where, MaxDescription))
		case "mode":
			switch p.Mode = ps.str(v, where, 20); p.Mode {
			case ModeReport, ModeEnforce, "":
			default:
				ps.add(v, where, "%q: write report or enforce", p.Mode)
			}
		case "scope":
			p.Scope = ps.scope(v, where)
		case "rules":
			rulesNode = v
			p.Rules = ps.rules(v, where)
		}
	})
	if n.Kind != yaml.MappingNode {
		return p
	}
	switch {
	case p.Name == "" && !ps.has(n, "name"):
		ps.add(n, at, "a policy needs a name")
	}
	if p.Mode == "" && !ps.has(n, "mode") {
		ps.add(n, at, "a policy needs a mode: report (judge and show) or enforce (block)")
	}
	if rulesNode == nil {
		ps.add(n, at, "a policy needs rules; the rules are %s", RuleNames())
	} else if len(p.Rules.used()) == 0 && rulesNode.Kind == yaml.MappingNode && len(rulesNode.Content) == 0 {
		ps.add(rulesNode, join(at, "rules"), "a policy needs at least one rule; the rules are %s", RuleNames())
	}
	if len(p.Scope.Environments) > 0 {
		for _, r := range p.Rules.used() {
			if PointOf(r) != PointDeploy {
				ps.add(rulesNode, join(at, "rules."+r), "a policy scoped to environments may only have deploy rules (%s is judged when the %s); write it in a policy of its own",
					r, map[string]string{PointRun: "run starts", PointImage: "image is about to be pushed"}[PointOf(r)])
			}
		}
	}
	return p
}

// has reports whether a mapping node has a key.
func (ps *parser) has(n *yaml.Node, key string) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

func (ps *parser) scope(n *yaml.Node, at string) Scope {
	var s Scope
	ps.mapping(n, at, []string{"projects", "environments", "branches", "events"}, func(key string, _, v *yaml.Node) {
		where := join(at, key)
		switch key {
		case "projects":
			s.Projects = ps.patterns(v, where)
		case "environments":
			s.Environments = ps.patterns(v, where)
		case "branches":
			s.Branches = ps.patterns(v, where)
		case "events":
			s.Events = ps.events(v, where)
		}
	})
	return s
}

func (ps *parser) events(n *yaml.Node, at string) []string {
	return ps.list(n, at, func(v *yaml.Node, s string) bool {
		if !slices.Contains(Events, s) {
			ps.add(v, at, "%q is not an event; the events are %s", s, strings.Join(Events, ", "))
			return false
		}
		return true
	})
}

func (ps *parser) severity(n *yaml.Node, at string, allowNone bool) string {
	s := ps.str(n, at, 20)
	if s == "" {
		return ""
	}
	if !slices.Contains(severities, s) || (s == "none" && !allowNone) {
		want := "low, medium, high or critical"
		if allowNone {
			want = "none, " + want
		}
		ps.add(n, at, "%q: write %s", s, want)
		return ""
	}
	return s
}

func (ps *parser) rules(n *yaml.Node, at string) Rules {
	var r Rules
	known := make([]string, len(Catalogue))
	for i, c := range Catalogue {
		known[i] = c.Name
	}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "must be a mapping of rules; the rules are %s", RuleNames())
		return r
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch {
		case !slices.Contains(known, k.Value):
			ps.add(k, join(at, k.Value), "unknown rule %q; the rules are %s", k.Value, RuleNames())
		case seen[k.Value]:
			ps.add(k, join(at, k.Value), "written twice")
		default:
			seen[k.Value] = true
			ps.rule(&r, k.Value, v, join(at, k.Value))
		}
	}
	return r
}

// rule reads one rule into r.
func (ps *parser) rule(r *Rules, key string, v *yaml.Node, where string) {
	switch key {
	case RuleLibraryVersions:
		lv := &LibraryVersions{}
		ps.mapping(v, where, []string{"pinned"}, func(_ string, _, x *yaml.Node) {
			switch lv.Pinned = ps.str(x, where+".pinned", 10); lv.Pinned {
			case "tag", "commit":
			default:
				ps.add(x, where+".pinned", "%q: write tag (a tag or a commit) or commit", lv.Pinned)
			}
		})
		if lv.Pinned == "" {
			lv.Pinned = "tag"
		}
		r.LibraryVersions = lv
	case RuleRequiredTemplates:
		if v.Kind != yaml.SequenceNode || len(v.Content) == 0 {
			ps.add(v, where, "must be a list of { library, template, versions }")
			return
		}
		for i, tn := range v.Content {
			w := fmt.Sprintf("%s[%d]", where, i)
			var t RequiredTemplate
			ps.mapping(tn, w, []string{"library", "template", "versions"}, func(key string, _, x *yaml.Node) {
				switch key {
				case "library":
					t.Library = ps.str(x, w+".library", 40)
				case "template":
					t.Template = ps.str(x, w+".template", 100)
				case "versions":
					t.Versions = ps.patterns(x, w+".versions")
				}
			})
			if tn.Kind == yaml.MappingNode && (t.Library == "" || t.Template == "") {
				ps.add(tn, w, "name the library (as the server's configuration lists it) and the template")
			}
			r.RequiredTemplates = append(r.RequiredTemplates, t)
		}
	case RuleStepImages:
		si := &StepImages{}
		ps.mapping(v, where, []string{"digest", "registries"}, func(key string, _, x *yaml.Node) {
			switch key {
			case "digest":
				si.Digest = ps.boolean(x, where+".digest")
			case "registries":
				si.Registries = ps.list(x, where+".registries", func(y *yaml.Node, s string) bool {
					if _, err := path.Match(strings.TrimSuffix(s, "/**"), ""); err != nil || strings.Contains(s, "://") {
						ps.add(y, where+".registries", "%q is not a repository pattern (host/path, * within a part, a final /** for any depth)", s)
						return false
					}
					return true
				})
			}
		})
		if v.Kind == yaml.MappingNode && !si.Digest && len(si.Registries) == 0 {
			ps.add(v, where, "say digest: true, or list registries, or both")
		}
		r.StepImages = si
	case RuleMaxTimeout:
		s := ps.str(v, where, 20)
		if s == "" {
			return
		}
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 || d > MaxTimeoutLimit {
			ps.add(v, where, "%q: write a duration such as 2h or 45m, at most 720h", s)
			return
		}
		r.MaxTimeout = s
	case RuleForbidSecrets:
		fs := &ForbidSecrets{}
		ps.mapping(v, where, []string{"events"}, func(_ string, _, x *yaml.Node) {
			fs.Events = ps.events(x, where+".events")
		})
		if v.Kind == yaml.MappingNode && len(fs.Events) == 0 {
			ps.add(v, where, "list the events whose runs may not read secrets, such as [pull_request, draft]")
		}
		r.ForbidSecrets = fs
	case RuleTests:
		t := &Tests{Min: 1}
		ps.mapping(v, where, []string{"min"}, func(_ string, _, x *yaml.Node) {
			t.Min = ps.integer(x, where+".min", 1, 10000000)
		})
		r.Tests = t
	case RuleFindings:
		f := &Findings{}
		ps.mapping(v, where, []string{"max_severity", "per_tool", "scanners"}, func(key string, _, x *yaml.Node) {
			w := where + "." + key
			switch key {
			case "max_severity":
				f.MaxSeverity = ps.severity(x, w, true)
			case "per_tool":
				f.PerTool = map[string]string{}
				if x.Kind != yaml.MappingNode {
					ps.add(x, w, "must be a mapping of tool name to severity, such as { Trivy: medium }")
					return
				}
				for i := 0; i+1 < len(x.Content); i += 2 {
					tk, tv := x.Content[i], x.Content[i+1]
					if !toolNameRe.MatchString(tk.Value) {
						ps.add(tk, w, "%q is not a tool name", tk.Value)
						continue
					}
					if sev := ps.severity(tv, w+"."+tk.Value, true); sev != "" {
						f.PerTool[tk.Value] = sev
					}
				}
			case "scanners":
				f.Scanners = ps.list(x, w, func(y *yaml.Node, s string) bool {
					if !toolNameRe.MatchString(s) {
						ps.add(y, w, "%q is not a tool name", s)
						return false
					}
					return true
				})
			}
		})
		if v.Kind == yaml.MappingNode && f.MaxSeverity == "" && len(f.PerTool) == 0 && len(f.Scanners) == 0 {
			ps.add(v, where, "say max_severity, per_tool or scanners (or several)")
		}
		r.Findings = f
	case RuleSBOM:
		s := &SBOM{MinComponents: 1}
		ps.mapping(v, where, []string{"min_components"}, func(_ string, _, x *yaml.Node) {
			s.MinComponents = ps.integer(x, where+".min_components", 1, 10000000)
		})
		r.SBOM = s
	case RuleDeployedImages:
		d := &DeployedImages{}
		ps.mapping(v, where, []string{"digest", "built_by_run", "signed", "scanned"}, func(key string, _, x *yaml.Node) {
			b := ps.boolean(x, where+"."+key)
			switch key {
			case "digest":
				d.Digest = b
			case "built_by_run":
				d.BuiltByRun = b
			case "signed":
				d.Signed = b
			case "scanned":
				d.Scanned = b
			}
		})
		if v.Kind == yaml.MappingNode && !d.Digest && !d.BuiltByRun && !d.Signed && !d.Scanned {
			ps.add(v, where, "say at least one of digest, built_by_run, signed or scanned")
		}
		r.DeployedImages = d
	case RuleApprovals, RuleAllowedFailures:
		ps.countRule(r, key, v, where)
	case RuleRefs:
		rf := &Refs{}
		ps.mapping(v, where, []string{"branches", "tags"}, func(key string, _, x *yaml.Node) {
			if key == "branches" {
				rf.Branches = ps.patterns(x, where+".branches")
			} else {
				rf.Tags = ps.patterns(x, where+".tags")
			}
		})
		if v.Kind == yaml.MappingNode && len(rf.Branches) == 0 && len(rf.Tags) == 0 {
			ps.add(v, where, "list the branches or tags (patterns) runs may deploy from")
		}
		r.Refs = rf
	case RuleImagePush:
		ip := &ImagePush{}
		ps.mapping(v, where, []string{"scan", "sign", "sbom", "provenance", "registries"}, func(key string, _, x *yaml.Node) {
			w := where + "." + key
			switch key {
			case "scan":
				ip.Scan = ps.severity(x, w, false)
			case "sign":
				switch ip.Sign = ps.str(x, w, 10); ip.Sign {
				case "runner", "any":
				default:
					ps.add(x, w, "%q: write runner (the runner's own key) or any", ip.Sign)
				}
			case "sbom":
				ip.SBOM = ps.boolean(x, w)
			case "provenance":
				ip.Provenance = ps.boolean(x, w)
			case "registries":
				ip.Registries = ps.patterns(x, w)
			}
		})
		if v.Kind == yaml.MappingNode && ip.Scan == "" && ip.Sign == "" && !ip.SBOM && !ip.Provenance && len(ip.Registries) == 0 {
			ps.add(v, where, "say at least one of scan, sign, sbom, provenance or registries")
		}
		r.ImagePush = ip
	}
}

// Check checks policies from several places together: names must be
// unique across all of them.
func Check(sets ...[]Policy) error {
	var problems Problems
	seen := map[string]Policy{}
	for _, ps := range sets {
		for _, p := range ps {
			if o, dup := seen[p.Name]; dup {
				problems = append(problems, Problem{File: p.File, Line: p.Line, Path: p.Name,
					Message: fmt.Sprintf("policy %s is also defined in %s (%s:%d); names must be unique", p.Name, o.Source, o.File, o.Line)})
				continue
			}
			seen[p.Name] = p
		}
	}
	if len(problems) > 0 {
		return problems
	}
	return nil
}
