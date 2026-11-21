package standards

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

// FilePath is where a repository keeps its own Standing orders. It is read
// from the default branch only, never from a pull request's head: a pull
// request cannot switch its own checks off.
const FilePath = ".launchway/standards.yaml"

// MaxFileBytes is the largest file that is read.
const MaxFileBytes = 64 << 10

var topKeys = []string{"version", "off", "off_reason", "checks", "gate", "licenses", "ignore"}
var gateKeys = []string{"fail_on", "require_fix", "default_branch", "incomplete"}
var licenseKeys = []string{"allow", "deny"}
var ignoreKeys = []string{"check", "rule", "package", "path", "fingerprint", "status", "reason", "owner", "expires"}

// ParseFile reads a repository's .launchway/standards.yaml. It returns the
// settings that could be read and, for everything that could not, a plain
// sentence naming the line. Nothing in a file that has problems is applied
// by guesswork: an entry with a problem is left out and the problem is
// shown, so the team sees it on the settings page and on the next run.
func ParseFile(data []byte, now time.Time) (Settings, []string) {
	if len(data) > MaxFileBytes {
		return Settings{}, []string{fmt.Sprintf("%s is larger than %d KiB and was not read.", FilePath, MaxFileBytes>>10)}
	}
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		if err.Error() == "EOF" {
			return Settings{}, nil // an empty file says nothing
		}
		return Settings{}, []string{"The file is not valid YAML: " + yamlMessage(err)}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return Settings{}, []string{"The file should be a list of settings, for example `checks:` and `gate:`; it is something else."}
	}
	root := doc.Content[0]
	var problems []string
	add := func(n *yaml.Node, format string, a ...any) {
		line := 0
		if n != nil {
			line = n.Line
		}
		problems = append(problems, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, a...)))
	}
	unknown := func(m *yaml.Node, allowed []string, where string) {
		for i := 0; i+1 < len(m.Content); i += 2 {
			k := m.Content[i].Value
			if !contains(allowed, k) {
				add(m.Content[i], "%q is not a setting%s.%s", k, where, didYouMean(k, allowed))
			}
		}
	}
	unknown(root, topKeys, "")
	var s Settings
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		switch k.Value {
		case "off":
			var b bool
			if err := v.Decode(&b); err != nil {
				add(v, "off should be true or false.")
			} else {
				s.Off = &b
			}
		case "off_reason":
			s.OffReason = v.Value
		case "checks":
			s.Checks = fileChecks(v, add)
		case "gate":
			fileGate(&s, v, add, unknown)
		case "licenses":
			fileLicenses(&s, v, add, unknown)
		case "ignore":
			fileIgnores(&s, v, now, add, &problems)
		}
	}
	// The same checks as for settings from anywhere else, for what the
	// walk above did not cover (thresholds, license names, the off reason).
	rest := s
	rest.Ignore = nil
	rest.Checks = nil
	problems = append(problems, rest.Problems(now, false)...)
	if len(s.Ignore) > maxIgnores {
		problems = append(problems, fmt.Sprintf("ignore: at most %d entries.", maxIgnores))
		s.Ignore = s.Ignore[:maxIgnores]
	}
	sort.Strings(problems)
	return s, problems
}

type addFn func(n *yaml.Node, format string, a ...any)
type unknownFn func(m *yaml.Node, allowed []string, where string)

func fileChecks(v *yaml.Node, add addFn) map[string]State {
	if v.Kind != yaml.MappingNode {
		add(v, "checks should be a list of check: state pairs, for example `secrets: block`.")
		return nil
	}
	out := map[string]State{}
	for j := 0; j+1 < len(v.Content); j += 2 {
		id, val := v.Content[j], v.Content[j+1]
		state := val.Value
		if val.Kind == yaml.MappingNode {
			state = ""
			for x := 0; x+1 < len(val.Content); x += 2 {
				if val.Content[x].Value == "state" {
					state = val.Content[x+1].Value
				} else {
					add(val.Content[x], "%q is not a setting of a check; only state is.", val.Content[x].Value)
				}
			}
		}
		if _, ok := CheckByID(id.Value); !ok {
			add(id, "%q is not a check.%s The checks are: %s.", id.Value, didYouMean(id.Value, CheckIDs()), strings.Join(CheckIDs(), ", "))
			continue
		}
		if !State(state).Valid() {
			add(val, "%q is not a state; use off, advise or block.", state)
			continue
		}
		out[id.Value] = State(state)
	}
	return out
}

func fileGate(s *Settings, v *yaml.Node, add addFn, unknown unknownFn) {
	if v.Kind != yaml.MappingNode {
		add(v, "gate should be a group of settings: fail_on, require_fix, default_branch, incomplete.")
		return
	}
	unknown(v, gateKeys, " of gate")
	for j := 0; j+1 < len(v.Content); j += 2 {
		gk, gv := v.Content[j], v.Content[j+1]
		switch gk.Value {
		case "fail_on":
			s.Gate.FailOn = findings.Severity(strings.ToLower(gv.Value))
		case "require_fix":
			var b bool
			if gv.Decode(&b) != nil {
				add(gv, "gate.require_fix should be true or false.")
			} else {
				s.Gate.RequireFix = &b
			}
		case "default_branch":
			s.Gate.DefaultBranch = State(strings.ToLower(gv.Value))
		case "incomplete":
			s.Gate.Incomplete = strings.ToLower(gv.Value)
		}
	}
}

func fileLicenses(s *Settings, v *yaml.Node, add addFn, unknown unknownFn) {
	if v.Kind != yaml.MappingNode {
		add(v, "licenses should be a group of settings: allow and deny, each a list of license names.")
		return
	}
	unknown(v, licenseKeys, " of licenses")
	for j := 0; j+1 < len(v.Content); j += 2 {
		lk, lv := v.Content[j], v.Content[j+1]
		var list []string
		if lv.Decode(&list) != nil {
			add(lv, "licenses.%s should be a list of license names, for example [MIT, Apache-2.0].", lk.Value)
			continue
		}
		if list == nil {
			list = []string{}
		}
		switch lk.Value {
		case "allow":
			s.Licenses.Allow = &list
		case "deny":
			s.Licenses.Deny = &list
		}
	}
}

func fileIgnores(s *Settings, v *yaml.Node, now time.Time, add addFn, problems *[]string) {
	if v.Kind != yaml.SequenceNode {
		add(v, "ignore should be a list of entries, each with check, rule, package or path, a reason and an end date.")
		return
	}
	for _, e := range v.Content {
		if e.Kind != yaml.MappingNode {
			add(e, "an ignore entry should be a group of settings (rule, reason, expires...).")
			continue
		}
		for i := 0; i+1 < len(e.Content); i += 2 {
			if k := e.Content[i].Value; !contains(ignoreKeys, k) {
				add(e.Content[i], "%q is not a setting of an ignore entry.%s", k, didYouMean(k, ignoreKeys))
			}
		}
		var ig Ignore
		if err := e.Decode(&ig); err != nil {
			add(e, "this ignore entry could not be read: %s", yamlMessage(err))
			continue
		}
		if ps := ig.Problems(now, fmt.Sprintf("line %d: ignore entry", e.Line)); len(ps) > 0 {
			*problems = append(*problems, ps...)
			continue
		}
		if ig.Status == "" {
			ig.Status = StatusAccepted
		}
		s.Ignore = append(s.Ignore, ig)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// didYouMean suggests the closest name when one is near enough.
func didYouMean(got string, names []string) string {
	best, bestD := "", 1<<30
	for _, n := range names {
		if d := editDistance(strings.ToLower(got), n); d < bestD {
			best, bestD = n, d
		}
	}
	if best != "" && (bestD <= max(2, len(best)/3) || strings.HasPrefix(strings.ToLower(got), best) || strings.HasPrefix(best, strings.ToLower(got))) {
		return fmt.Sprintf(" Did you mean %q?", best)
	}
	return ""
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// yamlMessage trims the library's wording to something readable.
func yamlMessage(err error) string {
	m := strings.TrimPrefix(err.Error(), "yaml: ")
	if i := strings.Index(m, "\n"); i >= 0 {
		m = m[:i]
	}
	return m
}

// ExportFile writes settings as the repository file would be written, so a
// project can move its settings into Git. Locks are not written: they are
// never set from a file.
func ExportFile(s Settings) []byte {
	var b strings.Builder
	b.WriteString("# Standing orders for this repository. Read from the default branch.\n# What the levels above lock cannot be weakened here; the settings page shows what applies.\nversion: 1\n")
	if s.Off != nil {
		fmt.Fprintf(&b, "off: %v\n", *s.Off)
		if s.OffReason != "" {
			fmt.Fprintf(&b, "off_reason: %q\n", s.OffReason)
		}
	}
	if len(s.Checks) > 0 {
		b.WriteString("checks:\n")
		ids := make([]string, 0, len(s.Checks))
		for id := range s.Checks {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(&b, "  %s: %s\n", id, s.Checks[id])
		}
	}
	g := s.Gate
	if g.FailOn != "" || g.RequireFix != nil || g.DefaultBranch != "" || g.Incomplete != "" {
		b.WriteString("gate:\n")
		if g.FailOn != "" {
			fmt.Fprintf(&b, "  fail_on: %s\n", g.FailOn)
		}
		if g.RequireFix != nil {
			fmt.Fprintf(&b, "  require_fix: %v\n", *g.RequireFix)
		}
		if g.DefaultBranch != "" {
			fmt.Fprintf(&b, "  default_branch: %s\n", g.DefaultBranch)
		}
		if g.Incomplete != "" {
			fmt.Fprintf(&b, "  incomplete: %s\n", g.Incomplete)
		}
	}
	if s.Licenses.Allow != nil || s.Licenses.Deny != nil {
		b.WriteString("licenses:\n")
		if s.Licenses.Allow != nil {
			fmt.Fprintf(&b, "  allow: [%s]\n", strings.Join(*s.Licenses.Allow, ", "))
		}
		if s.Licenses.Deny != nil {
			fmt.Fprintf(&b, "  deny: [%s]\n", strings.Join(*s.Licenses.Deny, ", "))
		}
	}
	if len(s.Ignore) > 0 {
		b.WriteString("ignore:\n")
		for _, ig := range s.Ignore {
			first := true
			w := func(k, v string) {
				if v == "" {
					return
				}
				p := "    "
				if first {
					p, first = "  - ", false
				}
				fmt.Fprintf(&b, "%s%s: %q\n", p, k, v)
			}
			w("check", ig.Check)
			w("rule", ig.Rule)
			w("package", ig.Package)
			w("path", ig.Path)
			w("fingerprint", ig.Fingerprint)
			w("status", ig.Status)
			w("reason", ig.Reason)
			w("owner", ig.Owner)
			w("expires", ig.Expires)
		}
	}
	return []byte(b.String())
}
