package standards

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

// Finding is one problem, whichever tool found it, in the one shape the
// product shows, tracks and gates on. The tool's own report is read by
// internal/findings; Normalize adds what the product needs.
type Finding struct {
	Check    string            `json:"check,omitempty"` // the check that ran the tool; empty for a step the team wrote
	Tool     string            `json:"tool"`
	Rule     string            `json:"rule"`
	Severity findings.Severity `json:"severity"`
	Kind     Kind              `json:"kind"`
	// Title is what is wrong, in a sentence.
	Title string `json:"title"`
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
	// Package, Version and FixedIn are filled for dependency findings:
	// what is affected, the version in use and the first version that is
	// not affected.
	Package string `json:"package,omitempty"`
	Version string `json:"version,omitempty"`
	FixedIn string `json:"fixed_in,omitempty"`
	// Advisories are the other names the problem is known by (CVE, GHSA).
	Advisories []string `json:"advisories,omitempty"`
	// Reachable is "yes" or "no" where a tool analyses calls, else "".
	Reachable string `json:"reachable,omitempty"`
	// Fix is what the tool suggests, in words, when it does.
	Fix string `json:"fix,omitempty"`
	// Fingerprint identifies the finding across runs: the same problem has
	// the same fingerprint when the code around it moves.
	Fingerprint string `json:"fingerprint"`
}

func (f Finding) hasAlias(id string) bool {
	for _, a := range f.Advisories {
		if strings.EqualFold(a, id) {
			return true
		}
	}
	return false
}

var (
	pkgRe      = regexp.MustCompile(`[Pp]ackage '([^']+?)@([^']+)'(?: is vulnerable to '([^']+)'(?: \(also known as ([^)]*)\))?)?`)
	aliasRe    = regexp.MustCompile(`'([^']+)'`)
	parenRe    = regexp.MustCompile(`\((?:[^()]*\bmodule|[^()]*\bpackage)? ?([a-z0-9][A-Za-z0-9._~/-]+)\)`)
	fixedRowRe = regexp.MustCompile(`(?m)^\|\s*[^|]+\|\s*([^|\s]+)\s*\|\s*([^|]+?)\s*\|\s*$`)
	digitsRe   = regexp.MustCompile(`[0-9]+`)
	aliasID    = regexp.MustCompile(`^(CVE-\d{4}-\d{4,}|GHSA-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{4}|GO-\d{4}-\d{4,}|PYSEC-\d{4}-\d+|OSV-\d{4}-\d+)$`)
)

// workspacePrefixes are where a tool in a step container sees the
// repository; they are removed from the paths it reports.
var workspacePrefixes = []string{"/workspace/", "/src/", "/github/workspace/"}

func cleanPath(p string) string {
	for _, pre := range workspacePrefixes {
		if rest, ok := strings.CutPrefix(p, pre); ok {
			return rest
		}
	}
	return strings.TrimPrefix(p, "./")
}

// Normalize turns what internal/findings read from one file into
// findings of the product's model. check is the Inspection check whose step
// wrote the file ("" for a step a team wrote). Findings keep the order of
// the report, and a finding that repeats gets its own fingerprint (the
// order among the identical ones), so none is lost and each is tracked.
func Normalize(check string, rep *findings.Report) []Finding {
	rules := map[string]findings.Rule{}
	for _, r := range rep.Rules {
		rules[r.Tool+"\x00"+r.ID] = r
	}
	out := make([]Finding, 0, len(rep.Findings))
	seen := map[string]int{}
	for _, f := range rep.Findings {
		out = append(out, normalizeOne(check, f, rules[f.Tool+"\x00"+f.RuleID], seen))
	}
	return out
}

// NormalizeOne is Normalize for a single finding, for callers that read
// findings one at a time. seen counts the findings already keyed in the
// same file, and is changed.
func NormalizeOne(check string, f findings.Finding, r findings.Rule, seen map[string]int) Finding {
	return normalizeOne(check, f, r, seen)
}

func normalizeOne(check string, f findings.Finding, r findings.Rule, seen map[string]int) Finding {
	n := Finding{Check: check, Tool: f.Tool, Rule: f.RuleID, Severity: f.Severity, Title: f.Message, File: cleanPath(f.File), Line: f.StartLine,
		Fix: f.Fix, Kind: kindOf(check, f)}
	tool := strings.ToLower(f.Tool)
	switch {
	case strings.Contains(tool, "osv"):
		osvDetails(&n, f, r)
	case strings.Contains(tool, "govulncheck"):
		goVulnDetails(&n, f, r)
	case strings.Contains(tool, "trivy"):
		trivyDetails(&n, f)
	}
	if n.Package == "" && (n.Kind == KindVulnerability || n.Kind == KindLicense) {
		if m := pkgRe.FindStringSubmatch(f.Message); m != nil {
			n.Package, n.Version = m[1], m[2]
		}
	}
	if n.Kind == KindStyle && (n.Severity == findings.Critical || n.Severity == findings.High || n.Severity == findings.Medium) {
		n.Severity = findings.Low // advice is never alarming
	}
	if n.FixedIn == "" && n.Package != "" {
		n.FixedIn = fixedIn(r.Help, n.Package)
	}
	n.Fingerprint = key(n, seen)
	if n.Fix == "" {
		n.Fix = fixAdvice(n, r)
	}
	return n
}

func kindOf(check string, f findings.Finding) Kind {
	if c, ok := CheckByID(check); ok {
		if f.Secret {
			return KindSecret
		}
		return c.Kind
	}
	if f.Secret {
		return KindSecret
	}
	t := strings.ToLower(f.Tool)
	switch {
	case strings.Contains(t, "osv"), strings.Contains(t, "govulncheck"), strings.Contains(t, "grype"), strings.Contains(t, "audit"):
		return KindVulnerability
	case strings.Contains(t, "hadolint"), strings.Contains(t, "kube"), strings.Contains(t, "checkov"), strings.Contains(t, "tfsec"):
		return KindConfig
	case strings.Contains(t, "trivy"):
		if strings.HasPrefix(f.RuleID, "CVE-") || strings.HasPrefix(f.RuleID, "GHSA-") {
			return KindVulnerability
		}
		return KindConfig
	case strings.Contains(t, "ruff"), strings.Contains(t, "eslint"), strings.Contains(t, "shellcheck"), strings.Contains(t, "lint"):
		return KindStyle
	}
	return KindCode
}

func osvDetails(n *Finding, f findings.Finding, r findings.Rule) {
	if m := pkgRe.FindStringSubmatch(f.Message); m != nil {
		n.Package, n.Version = m[1], m[2]
		if m[4] != "" {
			for _, a := range aliasRe.FindAllStringSubmatch(m[4], -1) {
				n.Advisories = append(n.Advisories, a[1])
			}
		}
	}
	for _, t := range r.Tags {
		if aliasID.MatchString(t) {
			n.Advisories = appendOnce(n.Advisories, t)
		}
	}
	n.Advisories = appendOnce(n.Advisories, f.RuleID)
	n.Advisories = withoutSelf(n.Advisories, f.RuleID)
}

func goVulnDetails(n *Finding, f findings.Finding, r findings.Rule) {
	msg := f.Message
	switch {
	case strings.Contains(msg, "calls vulnerable functions"):
		n.Reachable = "yes"
	case strings.Contains(msg, "doesn't appear to call"), strings.Contains(msg, "does not appear to call"):
		n.Reachable = "no"
	case strings.Contains(msg, "imports"):
		n.Reachable = "no"
	}
	if m := parenRe.FindStringSubmatch(msg); m != nil {
		n.Package = m[1]
	}
	for _, t := range r.Tags {
		if aliasID.MatchString(t) {
			n.Advisories = appendOnce(n.Advisories, t)
		}
	}
	if n.Reachable == "yes" {
		n.Title = "Vulnerable code that you call: " + strings.TrimSpace(strings.TrimPrefix(r.Short, "["+f.RuleID+"]"))
	} else if r.Short != "" {
		n.Title = "Vulnerable dependency that your code does not call: " + strings.TrimSpace(strings.TrimPrefix(r.Short, "["+f.RuleID+"]"))
	}
	n.Title = strings.TrimRight(n.Title, ": ")
	// The Go database names the module's own fixed version in the full
	// description of some rules; nothing is guessed when it is not there.
}

var trivyMsgRe = regexp.MustCompile(`(?m)^Message: (.+)$`)

func trivyDetails(n *Finding, f findings.Finding) {
	if m := trivyMsgRe.FindStringSubmatch(f.Message); m != nil {
		n.Title = m[1]
	}
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func withoutSelf(list []string, self string) []string {
	out := list[:0]
	for _, x := range list {
		if x != self {
			out = append(out, x)
		}
	}
	return out
}

// fixedIn finds, in the "Fixed Versions" table that the advisory text of a
// dependency scanner carries, the first version that fixes pkg. When
// several rows name the package, the newest version wins.
func fixedIn(help, pkg string) string {
	i := strings.Index(help, "Fixed Version")
	if i < 0 {
		return ""
	}
	best := ""
	for _, m := range fixedRowRe.FindAllStringSubmatch(help[i:], -1) {
		if m[1] != pkg {
			continue
		}
		v := strings.TrimSpace(strings.SplitN(m[2], ",", 2)[0])
		if v == "" || strings.EqualFold(v, "Fixed Version") {
			continue
		}
		if best == "" || compareVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}

// compareVersions compares dotted numbers, ignoring a leading v and
// anything after a dash; it is for choosing among fixed versions, nothing
// more.
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-")
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// key is the fingerprint: stable when the code around a finding moves.
func key(n Finding, seen map[string]int) string {
	var base string
	if (n.Kind == KindVulnerability || n.Kind == KindLicense) && n.Package != "" {
		base = strings.Join([]string{n.Tool, n.Rule, n.File, n.Package}, "\x00")
	} else {
		msg := digitsRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(n.Title)), "#")
		base = strings.Join([]string{n.Tool, n.Rule, n.File, msg}, "\x00")
	}
	seen[base]++
	if seen[base] > 1 {
		base += "\x00#" + strconv.Itoa(seen[base])
	}
	sum := sha256.Sum256([]byte(base))
	return hex.EncodeToString(sum[:16])
}

// fixAdvice is what to do, in a sentence, when the tool gave no suggestion
// of its own.
func fixAdvice(n Finding, r findings.Rule) string {
	switch {
	case n.Kind == KindVulnerability && n.FixedIn != "" && n.Package != "":
		return "Update " + n.Package + " to " + n.FixedIn + " or later."
	case n.Kind == KindVulnerability && n.Reachable == "yes":
		return "Update the module named in the finding to a version the advisory lists as fixed, then run the check again."
	case n.Kind == KindVulnerability:
		return "No fixed version is known yet. Look at the advisory; if you cannot update, accept the risk with a reason and an end date."
	case n.Kind == KindSecret:
		return "Revoke the secret and issue a new one, then remove it from the code. Removing it from the file alone does not help: it stays in the history."
	case n.Kind == KindLicense:
		return "Replace the dependency, get a license that allows your use, or accept the risk with a reason."
	}
	if h := strings.TrimSpace(r.Help); h != "" && len(h) < 300 && !strings.Contains(h, "\n") {
		return h
	}
	return ""
}

// GlobMatch matches a slash-separated path against a pattern in which *
// matches within one path element and ** any number of elements.
func GlobMatch(pattern, p string) bool {
	return globParts(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func globParts(pat, parts []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(parts); i++ {
				if globParts(pat[1:], parts[i:]) {
					return true
				}
			}
			return false
		}
		if len(parts) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], parts[0]); !ok {
			return false
		}
		pat, parts = pat[1:], parts[1:]
	}
	return len(parts) == 0
}
