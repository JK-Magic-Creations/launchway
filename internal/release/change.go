package release

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Change is one thing that changed since the previous release: a merged pull
// request, or a commit that came with none. Where the work tracker exists,
// a closed work item is a Change too (see Source).
type Change struct {
	// Kind says where it came from: "pull", "commit" or "item".
	Kind string `json:"kind"`
	// Ref is the pull request or item number (0 for a commit).
	Ref    int      `json:"ref,omitempty"`
	SHA    string   `json:"sha,omitempty"`
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Author string   `json:"author,omitempty"`
	Labels []string `json:"labels,omitempty"`
	URL    string   `json:"url,omitempty"`
	Breaks bool     `json:"breaking,omitempty"`
	// Parts are the subjects of the commits a merged pull request brought. They are not listed on their own; they
	// count when the pull request itself carries no marker (a feature built from `feat:` commits is a feature).
	Parts   []string `json:"parts,omitempty"`
	Section string   `json:"section,omitempty"` // filled by Classify
}

// The sections of release notes, in the order they are written.
const (
	SecBreaking = "breaking"
	SecSecurity = "security"
	SecFeature  = "feature"
	SecFix      = "fix"
	SecDeps     = "dependencies"
	SecOther    = "other"
)

// Sections lists them in order with their headings.
var Sections = []struct{ ID, Heading string }{
	{SecBreaking, "Breaking changes"}, {SecSecurity, "Security"}, {SecFeature, "Features"},
	{SecFix, "Fixes"}, {SecDeps, "Dependencies"}, {SecOther, "Other changes"},
}

var conventional = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^)]*)\))?(!)?:\s*(.+)$`)

// Parse reads a conventional commit prefix: type, scope, whether it is marked
// breaking, and the subject without the prefix.
func ParseSubject(s string) (typ, scope string, breaking bool, rest string) {
	m := conventional.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", "", false, strings.TrimSpace(s)
	}
	return strings.ToLower(m[1]), m[2], m[3] == "!", m[4]
}

func hasLabel(labels []string, words ...string) bool {
	for _, l := range labels {
		l = strings.ToLower(l)
		for _, w := range words {
			if l == w || strings.HasSuffix(l, "/"+w) || strings.HasSuffix(l, ":"+w) {
				return true
			}
		}
	}
	return false
}

// Classify says which section a change belongs to, and why in a few words.
// Labels outrank the commit prefix, because a person set them.
func Classify(c Change) (section, why string) {
	typ, scope, bang, _ := ParseSubject(c.Title)
	breaking := c.Breaks || bang || strings.Contains(c.Body, "BREAKING CHANGE:") || strings.Contains(c.Body, "BREAKING-CHANGE:") ||
		hasLabel(c.Labels, "breaking", "breaking-change", "major")
	switch {
	case breaking:
		return SecBreaking, "marked as breaking"
	case hasLabel(c.Labels, "security") || typ == "security" || scope == "security":
		return SecSecurity, "security"
	case hasLabel(c.Labels, "dependencies", "deps", "dependency") || typ == "deps" || scope == "deps" || scope == "dependencies" || typ == "build" && strings.Contains(strings.ToLower(scope), "deps"):
		return SecDeps, "a dependency change"
	case hasLabel(c.Labels, "feature", "enhancement", "minor") || typ == "feat" || typ == "feature":
		return SecFeature, "a feature"
	case hasLabel(c.Labels, "bug", "fix", "bugfix", "patch") || typ == "fix" || typ == "perf" || typ == "revert":
		return SecFix, "a fix"
	}
	if len(c.Parts) > 0 {
		best, why := SecOther, ""
		rank := map[string]int{SecOther: 0, SecDeps: 1, SecFix: 2, SecFeature: 3, SecSecurity: 4, SecBreaking: 5}
		for _, part := range c.Parts {
			if sec, _ := Classify(Change{Title: part}); rank[sec] > rank[best] {
				best, why = sec, "its commits include "+sec
			}
		}
		if best != SecOther {
			return best, why
		}
	}
	return SecOther, "no feature or fix marker"
}

// Bump is how much a version rises.
type Bump string

const (
	BumpNone  Bump = ""
	BumpPatch Bump = "patch"
	BumpMinor Bump = "minor"
	BumpMajor Bump = "major"
)

func (b Bump) rank() int {
	switch b {
	case BumpMajor:
		return 3
	case BumpMinor:
		return 2
	case BumpPatch:
		return 1
	}
	return 0
}

// Reason is one change and what it asks of the version.
type Reason struct {
	Change Change `json:"change"`
	Bump   Bump   `json:"bump"`
	Why    string `json:"why"`
}

// Proposal is the next version proposed, with the reasoning. A person
// confirms or overrides it; nothing is released from it alone.
type Proposal struct {
	Scheme   string   `json:"scheme"`
	Last     string   `json:"last,omitempty"`
	Bump     Bump     `json:"bump"`
	Next     string   `json:"next"`
	Reasons  []Reason `json:"reasons"`
	Summary  string   `json:"summary"`
	Warnings []string `json:"warnings,omitempty"`
}

// Propose works out the next version from the last release and the changes
// since it. Only semantic versions are proposed; other schemes get the
// reasoning without a number (a calendar version is today's date, which the
// caller supplies).
func Propose(scheme, last string, changes []Change) Proposal {
	p := Proposal{Scheme: scheme, Last: last, Reasons: []Reason{}}
	top := BumpNone
	sv, err := ParseSemver(last)
	pre1 := err == nil && sv.Major == 0
	for _, c := range changes {
		sec, _ := Classify(c)
		b := BumpPatch
		why := "a change with no feature or fix marker is treated as a patch"
		switch sec {
		case SecBreaking:
			b, why = BumpMajor, "breaking change"
			if pre1 {
				b, why = BumpMinor, "breaking change; before 1.0.0 it raises the minor number"
			}
		case SecFeature:
			b, why = BumpMinor, "new feature"
		case SecFix:
			why = "fix"
		case SecSecurity:
			why = "security fix"
		case SecDeps:
			why = "dependency change"
		}
		p.Reasons = append(p.Reasons, Reason{Change: c, Bump: b, Why: why})
		if b.rank() > top.rank() {
			top = b
		}
	}
	sort.SliceStable(p.Reasons, func(i, j int) bool { return p.Reasons[i].Bump.rank() > p.Reasons[j].Bump.rank() })
	p.Bump = top
	if len(changes) == 0 {
		p.Summary = "Nothing has changed since " + nonEmpty(last, "the start") + "; there is nothing to release."
		p.Warnings = append(p.Warnings, "no changes since the last release")
		return p
	}
	if scheme != SchemeSemver && scheme != "" {
		p.Summary = fmt.Sprintf("%d changes since %s. This project's versions are %s; give the next one yourself.", len(changes), nonEmpty(last, "the start"), scheme)
		return p
	}
	if last == "" {
		p.Next = "0.1.0"
		if err == nil {
			p.Next = sv.String()
		}
		p.Summary = "This is the first release: 0.1.0 is proposed; use 1.0.0 if the first release is stable."
		return p
	}
	if err != nil {
		p.Warnings = append(p.Warnings, "the last version is not a semantic version: "+last)
		return p
	}
	core := Semver{Major: sv.Major, Minor: sv.Minor, Patch: sv.Patch}
	next := core
	if sv.IsPre() {
		// A candidate of 1.5.0 is released as 1.5.0 if the changes since still fit it; otherwise higher.
		p.Warnings = append(p.Warnings, fmt.Sprintf("the last release %s is a pre-release; its release would be %s", last, core))
	} else {
		switch top {
		case BumpMajor:
			next = Semver{Major: sv.Major + 1}
		case BumpMinor:
			next = Semver{Major: sv.Major, Minor: sv.Minor + 1}
		default:
			next = Semver{Major: sv.Major, Minor: sv.Minor, Patch: sv.Patch + 1}
		}
	}
	p.Next = next.String()
	p.Summary = fmt.Sprintf("%s: %d changes since %s; the highest is %s, so %s → %s.", p.Next, len(changes), last, top, last, p.Next)
	return p
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// NextPre proposes the next pre-release of a version: 1.5.0 with label rc and
// existing 1.5.0-rc.1, 1.5.0-rc.2 gives 1.5.0-rc.3.
func NextPre(core, label string, existing []string) string {
	if label == "" {
		label = "rc"
	}
	n := 0
	prefix := core + "-" + label + "."
	for _, e := range existing {
		if strings.HasPrefix(e, prefix) {
			var k int
			if _, err := fmt.Sscanf(strings.TrimPrefix(e, prefix), "%d", &k); err == nil && k > n {
				n = k
			}
		}
	}
	return fmt.Sprintf("%s%d", prefix, n+1)
}

// MayRelease checks that a new version is higher than the last unless the
// caller explicitly allows otherwise. last is the highest version of the
// project so far ("" for none).
func MayRelease(scheme, last, next string, allowLower bool) error {
	if last == "" || scheme == SchemeCreated {
		return nil
	}
	c := Compare(scheme, next, last)
	if c > 0 {
		return nil
	}
	if c == 0 {
		return fmt.Errorf("%w: %s is the same version as the release before it", ErrVersion, next)
	}
	if allowLower {
		return nil
	}
	return fmt.Errorf("%w: %s is lower than %s, the highest release so far; a release from a maintenance branch must say so (allow_lower)", ErrVersion, next, last)
}
