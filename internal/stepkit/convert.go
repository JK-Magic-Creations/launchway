package stepkit

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
)

// SARIF writes a step's findings as a SARIF 2.1.0 document, which the
// findings model reads like the output of any scanner. It returns nil when
// there are none. The tool is the step.
func SARIF(step string, findings []Result) []byte {
	if len(findings) == 0 {
		return nil
	}
	rules := map[string]bool{}
	var ruleList []map[string]any
	var results []map[string]any
	for _, f := range findings {
		if !rules[f.Rule] {
			rules[f.Rule] = true
			ruleList = append(ruleList, map[string]any{"id": f.Rule, "name": f.Rule,
				"properties": map[string]any{"security-severity": severityScore(f.Severity)}})
		}
		res := map[string]any{
			"ruleId":     f.Rule,
			"level":      sarifLevel(f.Severity),
			"message":    map[string]any{"text": f.Message},
			"properties": map[string]any{"security-severity": severityScore(f.Severity)},
		}
		if f.File != "" {
			region := map[string]any{}
			if f.Line > 0 {
				region["startLine"] = f.Line
				if f.EndLine >= f.Line {
					region["endLine"] = f.EndLine
				}
			}
			loc := map[string]any{"artifactLocation": map[string]any{"uri": f.File}}
			if len(region) > 0 {
				loc["region"] = region
			}
			res["locations"] = []any{map[string]any{"physicalLocation": loc}}
		}
		results = append(results, res)
	}
	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []any{map[string]any{
			"tool":    map[string]any{"driver": map[string]any{"name": step, "rules": ruleList}},
			"results": results,
		}},
	}
	b, _ := json.Marshal(doc)
	return b
}

func severityScore(s string) string {
	switch s {
	case "critical":
		return "9.5"
	case "high":
		return "8.0"
	case "medium":
		return "5.0"
	}
	return "2.0"
}

func sarifLevel(s string) string {
	switch s {
	case "critical", "high":
		return "error"
	case "medium":
		return "warning"
	}
	return "note"
}

// JUnit writes a step's test results as JUnit XML, which the test model reads
// like any test tool's. It returns nil when there are none.
func JUnit(step string, tests []Result) []byte {
	if len(tests) == 0 {
		return nil
	}
	var failures, skipped int
	for _, t := range tests {
		switch t.Status {
		case "failed":
			failures++
		case "skipped":
			skipped++
		}
	}
	var b bytes.Buffer
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, "<testsuites><testsuite name=%s tests=\"%d\" failures=\"%d\" skipped=\"%d\">\n", attr(step), len(tests), failures, skipped)
	for _, t := range tests {
		suite := t.Suite
		if suite == "" {
			suite = step
		}
		fmt.Fprintf(&b, "<testcase classname=%s name=%s time=\"%s\"", attr(suite), attr(t.Name), strconv.FormatFloat(float64(t.Millis)/1000, 'f', 3, 64))
		switch t.Status {
		case "failed":
			fmt.Fprintf(&b, "><failure message=%s/></testcase>\n", attr(firstLineOf(t.Message)))
		case "skipped":
			b.WriteString("><skipped/></testcase>\n")
		default:
			b.WriteString("/>\n")
		}
	}
	b.WriteString("</testsuite></testsuites>\n")
	return b.Bytes()
}

func attr(s string) string {
	var b bytes.Buffer
	b.WriteByte('"')
	_ = xml.EscapeText(&b, []byte(s))
	b.WriteByte('"')
	return b.String()
}

func firstLineOf(s string) string {
	for i, c := range s {
		if c == '\n' || c == '\r' {
			return s[:i]
		}
	}
	return s
}
