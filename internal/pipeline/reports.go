package pipeline

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Limits on the reports a step declares.
const (
	MaxReportGlobs   = 20  // globs per kind of report
	MaxReportGlobLen = 256 // bytes in one glob
)

// parseReports reads reports: { tests: <globs>, findings: <globs>, sbom:
// <globs>, coverage: <globs>, html: <reports> }, each kind of file one glob
// or a list of globs. tests and coverage may also be mappings with paths:
// and their own settings. In a template, a glob may be { param: name } or
// use format:, like any value written in place.
func (ps *parser) parseReports(n *yaml.Node, at string) *Reports {
	r := &Reports{}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "reports is a mapping, for example: reports: { tests: reports/*.xml }")
		return r
	}
	ps.eachKey(n, at, []string{"tests", "findings", "sbom", "coverage", "html"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "tests":
			if v.Kind == yaml.MappingNode && !isParamNode(v) {
				ps.eachKey(v, at+".tests", []string{"paths", "attachments"}, func(k2, v2 *yaml.Node) {
					switch k2.Value {
					case "paths":
						r.Tests = ps.reportGlobs(v2, at+".tests.paths", "paths: reports/*.xml")
					case "attachments":
						dir, msg := CheckReportDir(ps.artifactText(v2, at+".tests.attachments"))
						if msg != "" {
							ps.add(v2, at+".tests.attachments", "%s", msg)
						} else if dir == "" {
							ps.add(v2, at+".tests.attachments", "name a directory below the workspace, such as test-results")
						}
						r.TestAttachments = dir
					}
				})
				if !hasKey(v, "paths") {
					ps.add(v, at+".tests", "say which files, for example: tests: { paths: test-results/results.json, attachments: test-results }")
				}
				return
			}
			r.Tests = ps.reportGlobs(v, at+".tests", "tests: reports/*.xml")
		case "findings":
			r.Findings = ps.reportGlobs(v, at+".findings", "findings: reports/*.sarif")
		case "sbom":
			r.SBOM = ps.reportGlobs(v, at+".sbom", "sbom: reports/sbom.cdx.json")
		case "coverage":
			if v.Kind == yaml.MappingNode && !isParamNode(v) {
				ps.eachKey(v, at+".coverage", []string{"paths", "fail_under"}, func(k2, v2 *yaml.Node) {
					switch k2.Value {
					case "paths":
						r.Coverage = ps.reportGlobs(v2, at+".coverage.paths", "paths: coverage/lcov.info")
					case "fail_under":
						r.CoverageFailUnder = ps.parseFailUnder(v2, at+".coverage.fail_under")
					}
				})
				if !hasKey(v, "paths") {
					ps.add(v, at+".coverage", "say which files, for example: coverage: { paths: coverage/cobertura.xml, fail_under: 80 }")
				}
				return
			}
			r.Coverage = ps.reportGlobs(v, at+".coverage", "coverage: coverage/lcov.info")
		case "html":
			r.HTML = ps.parseHTMLReports(v, at+".html")
		}
	})
	if len(n.Content) == 0 {
		ps.add(n, at, "say which files, for example: reports: { tests: reports/*.xml }")
	}
	return r
}

// isParamNode reports whether a mapping is a template value ({ param: … }
// and the like) rather than settings.
func isParamNode(n *yaml.Node) bool {
	return hasKey(n, "param") || hasKey(n, "input") || hasKey(n, "each")
}

// parseFailUnder reads a coverage threshold: a percentage of lines (80,
// 72.5, "80%"), or { lines: 80, branches: 60, functions: 70 }.
func (ps *parser) parseFailUnder(n *yaml.Node, at string) *CoverageThreshold {
	t := &CoverageThreshold{}
	read := func(v *yaml.Node, where string) float64 {
		s := strings.TrimSuffix(strings.TrimSpace(ps.scalar(v, where)), "%")
		f, err := strconv.ParseFloat(s, 64)
		if err == nil && f == 0 && ps.tmpl != nil && isParamNode(v) {
			// A template's parameter may be 0: no threshold, so that a
			// repository that has none leaves it out.
			return 0
		}
		if err != nil || f <= 0 || f > 100 || f != f {
			ps.add(v, where, "write a percentage above 0 and at most 100, such as 80")
			return 0
		}
		return f
	}
	if n.Kind == yaml.MappingNode && !isParamNode(n) {
		ps.eachKey(n, at, []string{"lines", "branches", "functions"}, func(k, v *yaml.Node) {
			switch k.Value {
			case "lines":
				t.Lines = read(v, at+".lines")
			case "branches":
				t.Branches = read(v, at+".branches")
			case "functions":
				t.Functions = read(v, at+".functions")
			}
		})
		if len(n.Content) == 0 {
			ps.add(n, at, "say which, for example: fail_under: { lines: 80, branches: 60 }")
		}
	} else {
		t.Lines = read(n, at)
	}
	if t.Lines == 0 && t.Branches == 0 && t.Functions == 0 {
		return nil
	}
	return t
}

// parseHTMLReports reads html: one { name, path, index } or a list of them.
func (ps *parser) parseHTMLReports(n *yaml.Node, at string) []HTMLReport {
	items := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		items = n.Content
	}
	if len(items) == 0 {
		ps.add(n, at, "list at least one report, for example: html: { name: playwright, path: playwright-report }")
		return nil
	}
	if len(items) > MaxHTMLReports {
		ps.add(n, at, "at most %d HTML reports per step", MaxHTMLReports)
		items = items[:MaxHTMLReports]
	}
	var out []HTMLReport
	seen := map[string]bool{}
	for _, item := range items {
		if item.Kind != yaml.MappingNode || isParamNode(item) {
			ps.add(item, at, "each HTML report is a mapping, for example: { name: playwright, path: playwright-report, index: index.html }")
			continue
		}
		h := HTMLReport{Line: item.Line}
		var pathNode *yaml.Node
		ps.eachKey(item, at, []string{"name", "path", "index"}, func(k, v *yaml.Node) {
			switch k.Value {
			case "name":
				h.Name = ps.artifactText(v, at+".name")
				if h.Name != "" && !ValidHTMLReportName(h.Name) {
					ps.add(v, at+".name", "%s is not a report name: lower-case letters, digits, '.', '-' and '_', starting with a letter or digit, at most %d characters",
						quoteText(h.Name), maxHTMLReportName)
				}
			case "path":
				pathNode = v
				dir, msg := CheckReportDir(ps.artifactText(v, at+".path"))
				switch {
				case msg != "":
					ps.add(v, at+".path", "%s", msg)
				case dir == "":
					ps.add(v, at+".path", "name the directory the tool wrote its report to, such as playwright-report; the workspace itself cannot be published")
				}
				h.Path = dir
			case "index":
				idx, msg := CheckHTMLIndex(ps.artifactText(v, at+".index"))
				if msg != "" {
					ps.add(v, at+".index", "%s", msg)
				}
				h.Index = idx
			}
		})
		if pathNode == nil {
			ps.add(item, at, "say which directory to publish, for example: { name: playwright, path: playwright-report }")
			continue
		}
		if h.Name == "" && h.Path != "" {
			h.Name = htmlNameFromPath(h.Path)
		}
		if h.Index == "" {
			h.Index = "index.html"
		}
		if h.Name != "" {
			if seen[h.Name] {
				ps.add(item, at, "HTML report %s is listed twice; give each a name of its own", h.Name)
			}
			seen[h.Name] = true
		}
		out = append(out, h)
	}
	return out
}

// htmlNameFromPath makes a report name from the last part of its directory:
// playwright-report for playwright-report, karate-reports for
// target/karate-reports.
func htmlNameFromPath(dir string) string {
	base := strings.ToLower(path.Base(dir))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.TrimLeft(b.String(), ".-_")
	if len(name) > maxHTMLReportName {
		name = name[:maxHTMLReportName]
	}
	if !ValidHTMLReportName(name) {
		return "report"
	}
	return name
}

// CheckReportDir checks a directory of reports (test attachments, an HTML
// report), relative to the workspace, and returns it tidied, or why it
// cannot be used. "" with no message means the workspace itself.
func CheckReportDir(dir string) (string, string) {
	d, msg := CheckInto(dir)
	if msg != "" {
		return "", strings.Replace(msg, "is not a plain directory name", "is not a plain directory name (no patterns here)", 1)
	}
	return d, ""
}

// CheckHTMLIndex checks the page an HTML report opens on, relative to its
// directory.
func CheckHTMLIndex(index string) (string, string) {
	i := strings.TrimSpace(index)
	for strings.HasPrefix(i, "./") {
		i = strings.TrimLeft(i[2:], "/")
	}
	quoted := quoteText(index)
	switch {
	case i == "":
		return "", "name the page to open, such as index.html"
	case len(i) > MaxReportGlobLen:
		return "", quoted + " is too long"
	case strings.HasPrefix(i, "/") || strings.Contains(i, "\\") || strings.ContainsAny(i, "*?[#") ||
		strings.ContainsFunc(i, func(r rune) bool { return r < 0x20 || r == 0x7f }) || !utf8.ValidString(i):
		return "", quoted + " is not a page inside the report's directory"
	}
	for _, part := range strings.Split(i, "/") {
		if part == "" || part == "." || part == ".." {
			return "", quoted + " is not a page inside the report's directory"
		}
	}
	return i, ""
}

// Limits on HTML reports.
const (
	MaxHTMLReports    = 5
	maxHTMLReportName = 48
)

var htmlReportNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`)

// ValidHTMLReportName reports whether name can name an HTML report.
func ValidHTMLReportName(name string) bool { return htmlReportNameRe.MatchString(name) }

// The artifacts that hold what reports keep (schema 18). They are named so
// they cannot be mistaken for each other; a step's own artifacts may not use
// these names.
const (
	// TestAttachmentsArtifact keeps a step's reports.tests.attachments
	// directory.
	TestAttachmentsArtifact = "test-attachments"
	// TestEmbeddedArtifact keeps attachments carried inside report files
	// (Cucumber embeddings, Playwright bodies), which the server stores.
	TestEmbeddedArtifact = "test-embedded"
	// HTMLReportPrefix starts the name of the artifact that keeps an HTML
	// report: report-<name>.
	HTMLReportPrefix = "report-"
)

// SchemaReports is the server schema that brought test attachments,
// coverage and HTML reports.
const SchemaReports = 18

// UsesSchema18 reports whether the pipeline uses what servers older than
// schema 18 do not know: test attachments, coverage and HTML reports.
func (p *Pipeline) UsesSchema18() bool {
	for _, s := range p.Steps {
		if s.Reports.UsesSchema18() {
			return true
		}
	}
	return false
}

// UsesSchema18 reports whether the reports use what schema 18 brought.
func (r *Reports) UsesSchema18() bool {
	return r != nil && (r.TestAttachments != "" || len(r.Coverage) > 0 || len(r.HTML) > 0)
}

// reservedArtifact says why a step's own artifact may not have a name, or
// "" if it may.
func reservedArtifact(s *Step, name string) string {
	switch {
	case s.Reports == nil:
	case name == TestAttachmentsArtifact && s.Reports.TestAttachments != "",
		name == TestEmbeddedArtifact && len(s.Reports.Tests) > 0:
		return "the artifact name " + name + " is taken by the test reports of this step; choose another"
	case strings.HasPrefix(name, HTMLReportPrefix):
		for _, h := range s.Reports.HTML {
			if HTMLReportPrefix+h.Name == name {
				return "the artifact name " + name + " is taken by the HTML report " + h.Name + " in this step; choose another"
			}
		}
	}
	return ""
}

// reportGlobs reads one kind of report: a glob or a list of globs, each
// checked where it is written, so a problem points at its own line.
func (ps *parser) reportGlobs(v *yaml.Node, where, example string) []string {
	var out []string
	items := []*yaml.Node{v}
	if v.Kind == yaml.SequenceNode {
		items = v.Content
	}
	before := len(ps.problems)
	switch {
	case len(items) == 0:
		ps.add(v, where, "list at least one file or glob, for example: %s", example)
	case len(items) > MaxReportGlobs:
		ps.add(v, where, "at most %d globs", MaxReportGlobs)
		items = items[:MaxReportGlobs]
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.Kind == yaml.SequenceNode {
			ps.add(item, where, "expected a glob or a list of globs")
			continue
		}
		list := ps.valueList(item, where)
		if len(list) == 0 && item.Kind == yaml.ScalarNode {
			list = []string{""}
		}
		for _, g := range list {
			clean, msg := CheckReportGlob(g)
			if msg != "" {
				ps.add(item, where, "%s", msg)
				continue
			}
			if !seen[clean] {
				seen[clean] = true
				out = append(out, clean)
			}
		}
	}
	if len(out) == 0 && len(ps.problems) == before {
		ps.add(v, where, "no glob is left: list at least one file or glob, for example: %s", example)
	}
	return out
}

// CheckReportGlob checks a glob naming report files, relative to the
// workspace, and returns it tidied (without a leading ./), or why it cannot
// be used. A path part ** matches any number of directories; the others
// are patterns as in on.push.branches (*, ?, [a-z]).
func CheckReportGlob(g string) (string, string) { return checkGlob(g, "report files") }

// checkGlob checks a glob naming files of the workspace; what names them in
// messages, such as "report files".
func checkGlob(g, what string) (string, string) {
	quoted := quoteText(g)
	switch {
	case strings.TrimSpace(g) == "":
		return "", "a glob is empty; write a path such as reports/*.xml"
	case len(g) > MaxReportGlobLen:
		return "", quoted + " is longer than " + itoa(MaxReportGlobLen) + " bytes"
	case !utf8.ValidString(g) || strings.ContainsFunc(g, unicode.IsControl):
		return "", quoted + " is not a path: it has characters that are not text"
	case strings.HasPrefix(g, "/") || strings.HasPrefix(g, "~"):
		return "", quoted + " is not relative to the workspace: write it without a leading / or ~, such as reports/*.xml"
	case strings.Contains(g, "\\"):
		return "", quoted + ": separate directories with /, not \\"
	}
	for strings.HasPrefix(g, "./") {
		g = strings.TrimLeft(g[2:], "/")
	}
	parts := strings.Split(g, "/")
	for i, part := range parts {
		switch {
		case part == "..":
			return "", quoted + " leaves the workspace (..); " + what + " must be inside it"
		case part == "" && i == len(parts)-1:
			return "", quoted + " ends with /: name the files, such as reports/*.xml"
		case part == "" || part == ".":
			return "", quoted + " has an empty path part (// or /./)"
		case part == "**":
			if i == len(parts)-1 {
				return "", quoted + " ends with **: name the files after it, such as **/*.xml"
			}
		case strings.Contains(part, "**"):
			return "", quoted + ": ** must be a whole path part, as in reports/**/*.xml"
		default:
			if _, err := path.Match(part, ""); err != nil {
				return "", quoted + " is not a valid pattern"
			}
		}
	}
	return g, ""
}
