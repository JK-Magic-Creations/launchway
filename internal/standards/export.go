package standards

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

// ExportSARIF writes findings as SARIF 2.1.0, the format other tools read:
// one run per tool, the fingerprint as a partial fingerprint so that another
// system tracks the same finding the same way. Secrets are not in it: their
// values were never kept, and the location and rule are all there is.
func ExportSARIF(w io.Writer, list []Finding) error {
	type result struct {
		RuleID              string            `json:"ruleId"`
		Level               string            `json:"level"`
		Message             map[string]string `json:"message"`
		Locations           []map[string]any  `json:"locations,omitempty"`
		PartialFingerprints map[string]string `json:"partialFingerprints"`
		Properties          map[string]any    `json:"properties,omitempty"`
	}
	type run struct {
		Tool struct {
			Driver struct {
				Name string `json:"name"`
			} `json:"driver"`
		} `json:"tool"`
		Results []result `json:"results"`
	}
	byTool := map[string]*run{}
	var order []string
	for _, f := range list {
		r := byTool[f.Tool]
		if r == nil {
			r = &run{}
			r.Tool.Driver.Name = f.Tool
			byTool[f.Tool] = r
			order = append(order, f.Tool)
		}
		level := map[findings.Severity]string{findings.Critical: "error", findings.High: "error", findings.Medium: "warning"}[f.Severity]
		if level == "" {
			level = "note"
		}
		props := map[string]any{"severity": string(f.Severity), "kind": string(f.Kind), "check": f.Check}
		for k, v := range map[string]string{"package": f.Package, "version": f.Version, "fixed_in": f.FixedIn, "reachable": f.Reachable} {
			if v != "" {
				props[k] = v
			}
		}
		if len(f.Advisories) > 0 {
			props["advisories"] = f.Advisories
		}
		res := result{RuleID: f.Rule, Level: level, Message: map[string]string{"text": f.Title}, PartialFingerprints: map[string]string{"launchway/v1": f.Fingerprint}, Properties: props}
		res.Locations = location(f.File, f.Line)
		r.Results = append(r.Results, res)
	}
	runs := make([]*run, 0, len(order))
	for _, t := range order {
		runs = append(runs, byTool[t])
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"version": "2.1.0", "$schema": "https://json.schemastore.org/sarif-2.1.0.json", "runs": runs})
}

// CSVHeader is the columns of ExportCSV.
var CSVHeader = []string{"fingerprint", "check", "tool", "rule", "severity", "kind", "file", "line", "package", "version", "fixed_in", "advisories", "reachable", "title"}

// ExportCSV writes findings as CSV. A cell that a spreadsheet would run as a
// formula (it starts with =, +, - or @) is written with a leading quote, so
// a finding's text cannot do anything when the file is opened.
func ExportCSV(w io.Writer, list []Finding) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	safe := func(s string) string {
		if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
			return "'" + s
		}
		return s
	}
	for _, f := range list {
		if err := cw.Write([]string{f.Fingerprint, f.Check, f.Tool, safe(f.Rule), string(f.Severity), string(f.Kind), safe(f.File), strconv.Itoa(f.Line), safe(f.Package), f.Version, f.FixedIn,
			strings.Join(f.Advisories, " "), f.Reachable, safe(f.Title)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
