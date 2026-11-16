package pipeline

import (
	"path"
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
// <globs> }, each one glob or a list of globs. In a template, a glob may be
// { param: name } or use format:, like any value written in place.
func (ps *parser) parseReports(n *yaml.Node, at string) *Reports {
	r := &Reports{}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "reports is a mapping, for example: reports: { tests: reports/*.xml }")
		return r
	}
	ps.eachKey(n, at, []string{"tests", "findings", "sbom"}, func(k, v *yaml.Node) {
		switch k.Value {
		case "tests":
			r.Tests = ps.reportGlobs(v, at+".tests", "tests: reports/*.xml")
		case "findings":
			r.Findings = ps.reportGlobs(v, at+".findings", "findings: reports/*.sarif")
		case "sbom":
			r.SBOM = ps.reportGlobs(v, at+".sbom", "sbom: reports/sbom.cdx.json")
		}
	})
	if len(n.Content) == 0 {
		ps.add(n, at, "say which files, for example: reports: { tests: reports/*.xml }")
	}
	return r
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
	quoted := "\"" + clipText(g, 60) + "\""
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
