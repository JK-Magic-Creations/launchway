package standards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Adapters turn the report formats that are not SARIF into SARIF, so one
// reader (internal/findings) judges every file the same way, with the same
// limits and the same masking. The formats are:
//
//   - the JSON of shellcheck ("json1");
//   - the license report the license check writes: osv-scanner's JSON with
//     the license policy of the run around it;
//   - "launchway-findings/1", the JSON a tool without SARIF output can write
//     to plug in (see FormatNative and docs/reference/standing-orders.md).
//
// A file that is SARIF, or that no adapter knows, is returned unchanged.

// FormatNative is the value of "format" in the Launchway findings JSON.
const FormatNative = "launchway-findings/1"

// Adapt returns SARIF for a report in a format an adapter knows, and the
// name of the format, or data unchanged and "". Data that looks like an
// adapter's format but cannot be read gives an error, which the reader
// shows as the file's problem.
func Adapt(data []byte) ([]byte, string, error) {
	head := bytes.TrimLeft(data[:min(len(data), 512)], " \t\r\n\xef\xbb\xbf")
	if len(head) == 0 || head[0] != '{' {
		return data, "", nil
	}
	var top map[string]json.RawMessage
	// Only the top-level keys matter; a SARIF file's runs are never decoded
	// here, they are skipped by the decoder without being kept in a shape.
	if err := json.Unmarshal(data, &top); err != nil {
		return data, "", nil // not our business: the SARIF reader will say what is wrong
	}
	switch {
	case top["runs"] != nil:
		return data, "", nil // SARIF
	case top["launchway_licenses"] != nil:
		out, err := adaptLicenses(top)
		return out, "launchway-licenses", err
	case top["comments"] != nil:
		out, err := adaptShellcheck(top["comments"])
		return out, "shellcheck-json1", err
	case jsonString(top["format"]) == FormatNative:
		out, err := adaptNative(top)
		return out, FormatNative, err
	}
	return data, "", nil
}

func jsonString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// sarifOut builds the little SARIF the adapters write.
type sarifOut struct {
	Version string `json:"version"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name  string      `json:"name"`
				Rules []sarifRule `json:"rules,omitempty"`
			} `json:"driver"`
		} `json:"tool"`
		Results []sarifResultOut `json:"results"`
	} `json:"runs"`
}

type sarifRule struct {
	ID    string            `json:"id"`
	Short map[string]string `json:"shortDescription,omitempty"`
	Help  map[string]string `json:"help,omitempty"`
	URI   string            `json:"helpUri,omitempty"`
}

type sarifResultOut struct {
	RuleID     string            `json:"ruleId"`
	Level      string            `json:"level,omitempty"`
	Message    map[string]string `json:"message"`
	Locations  []map[string]any  `json:"locations,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
}

func newSARIF(tool string, rules []sarifRule, results []sarifResultOut) ([]byte, error) {
	var s sarifOut
	s.Version = "2.1.0"
	s.Runs = make([]struct {
		Tool struct {
			Driver struct {
				Name  string      `json:"name"`
				Rules []sarifRule `json:"rules,omitempty"`
			} `json:"driver"`
		} `json:"tool"`
		Results []sarifResultOut `json:"results"`
	}, 1)
	s.Runs[0].Tool.Driver.Name = tool
	s.Runs[0].Tool.Driver.Rules = rules
	s.Runs[0].Results = results
	if s.Runs[0].Results == nil {
		s.Runs[0].Results = []sarifResultOut{}
	}
	return json.Marshal(s)
}

func location(file string, line int) []map[string]any {
	if file == "" {
		return nil
	}
	region := map[string]any{}
	if line > 0 {
		region["startLine"] = line
	}
	return []map[string]any{{"physicalLocation": map[string]any{"artifactLocation": map[string]string{"uri": file}, "region": region}}}
}

// ---- shellcheck ----

func adaptShellcheck(raw json.RawMessage) ([]byte, error) {
	var comments []struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Level   string `json:"level"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &comments); err != nil {
		return nil, fmt.Errorf("the shellcheck report could not be read: %w", err)
	}
	ruleSeen := map[string]bool{}
	var rules []sarifRule
	var results []sarifResultOut
	for _, c := range comments {
		id := fmt.Sprintf("SC%d", c.Code)
		if !ruleSeen[id] {
			ruleSeen[id] = true
			rules = append(rules, sarifRule{ID: id, URI: "https://www.shellcheck.net/wiki/" + id, Short: map[string]string{"text": c.Message}})
		}
		level := "note"
		switch c.Level {
		case "error":
			level = "error"
		case "warning":
			level = "warning"
		}
		results = append(results, sarifResultOut{RuleID: id, Level: level, Message: map[string]string{"text": c.Message}, Locations: location(c.File, c.Line)})
	}
	return newSARIF("shellcheck", rules, results)
}

// ---- licenses ----

func adaptLicenses(top map[string]json.RawMessage) ([]byte, error) {
	var pol struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	if err := json.Unmarshal(top["launchway_licenses"], &pol); err != nil {
		return nil, fmt.Errorf("the license policy in the report could not be read: %w", err)
	}
	var osv struct {
		Results []struct {
			Source   struct{ Path string } `json:"source"`
			Packages []struct {
				Package struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"package"`
				Licenses          []string `json:"licenses"`
				LicenseViolations []string `json:"license_violations"`
			} `json:"packages"`
		} `json:"results"`
	}
	if err := json.Unmarshal(top["osv"], &osv); err != nil {
		return nil, fmt.Errorf("the license report could not be read: %w", err)
	}
	deny := map[string]bool{}
	for _, d := range pol.Deny {
		deny[strings.ToLower(d)] = true
	}
	allowed := map[string]bool{}
	for _, a := range pol.Allow {
		allowed[strings.ToLower(a)] = true
	}
	rules := []sarifRule{
		{ID: "license-denied", Short: map[string]string{"text": "A dependency is under a license on the denied list"},
			Help: map[string]string{"text": "Replace the dependency, or get written permission under a different license. If the use is allowed, remove the license from the denied list, or accept the risk with a reason."}},
		{ID: "license-not-allowed", Short: map[string]string{"text": "A dependency is under a license that is not on the allowed list"},
			Help: map[string]string{"text": "Check the license, then add it to the allowed list if it fits how you ship, or replace the dependency."}},
		{ID: "license-unknown", Short: map[string]string{"text": "A dependency has no license that could be identified"},
			Help: map[string]string{"text": "Look at the project's repository for its license. A dependency without a license gives you no right to use it."}},
	}
	type seenKey struct{ file, name, version, rule string }
	seen := map[seenKey]bool{}
	var results []sarifResultOut
	for _, res := range osv.Results {
		file := cleanPath(strings.TrimPrefix(res.Source.Path, "lockfile:"))
		for _, p := range res.Packages {
			viol := map[string]bool{}
			for _, v := range p.LicenseViolations {
				viol[strings.ToLower(v)] = true
			}
			add := func(rule, level, lic, why string) {
				k := seenKey{file, p.Package.Name, p.Package.Version, rule}
				if seen[k] {
					return
				}
				seen[k] = true
				results = append(results, sarifResultOut{RuleID: rule, Level: level, Locations: location(file, 0),
					Message:    map[string]string{"text": fmt.Sprintf("Package '%s@%s' is under the license %s, %s.", p.Package.Name, p.Package.Version, lic, why)},
					Properties: map[string]string{"severity": map[string]string{"error": "high", "warning": "medium", "note": "low"}[level]}})
			}
			if len(p.Licenses) == 0 {
				add("license-unknown", "note", "(none found)", "so no license could be identified")
			}
			for _, l := range p.Licenses {
				lc := strings.ToLower(l)
				switch {
				case deny[lc]:
					add("license-denied", "error", l, "which is on the denied list")
				case lc == "non-standard" || lc == "unknown" || lc == "" || strings.HasPrefix(lc, "licenseref"):
					add("license-unknown", "note", l, "which could not be matched to a known license")
				case len(allowed) > 0 && !allowed[lc] && viol[lc]:
					add("license-not-allowed", "warning", l, "which is not on the allowed list")
				}
			}
		}
	}
	return newSARIF("launchway-licenses", rules, results)
}

// ---- the native format ----

// native is "launchway-findings/1": a tool that cannot write SARIF writes
// this and is read like every other.
type native struct {
	Format   string `json:"format"`
	Tool     string `json:"tool"`
	Findings []struct {
		Rule     string `json:"rule"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
		File     string `json:"file"`
		Line     int    `json:"line"`
		Package  string `json:"package"`
		Version  string `json:"version"`
		FixedIn  string `json:"fixed_in"`
		Help     string `json:"help"`
	} `json:"findings"`
}

func adaptNative(top map[string]json.RawMessage) ([]byte, error) {
	b, _ := json.Marshal(top)
	var n native
	if err := json.Unmarshal(b, &n); err != nil {
		return nil, fmt.Errorf("the findings file could not be read: %w", err)
	}
	if strings.TrimSpace(n.Tool) == "" {
		return nil, fmt.Errorf("the findings file does not name its tool (\"tool\")")
	}
	ruleSeen := map[string]bool{}
	var rules []sarifRule
	var results []sarifResultOut
	for i, f := range n.Findings {
		if f.Rule == "" {
			return nil, fmt.Errorf("finding %d has no \"rule\"", i+1)
		}
		sev := strings.ToLower(f.Severity)
		switch sev {
		case "critical", "high", "medium", "low":
		case "":
			sev = "medium"
		default:
			return nil, fmt.Errorf("finding %d: severity %q is not one of critical, high, medium, low", i+1, f.Severity)
		}
		msg := f.Message
		if f.Package != "" {
			msg = strings.TrimSpace(fmt.Sprintf("Package '%s@%s' %s", f.Package, f.Version, f.Message))
		}
		help := f.Help
		if f.Package != "" && f.FixedIn != "" {
			help += fmt.Sprintf("\n\n### Fixed Versions\n\n| Vulnerability ID | Package Name | Fixed Version |\n| --- | --- | --- |\n| %s | %s | %s |\n", f.Rule, f.Package, f.FixedIn)
		}
		if !ruleSeen[f.Rule] {
			ruleSeen[f.Rule] = true
			rules = append(rules, sarifRule{ID: f.Rule, Short: map[string]string{"text": f.Message}, Help: map[string]string{"text": help}})
		}
		results = append(results, sarifResultOut{RuleID: f.Rule, Message: map[string]string{"text": msg}, Locations: location(f.File, f.Line),
			Properties: map[string]string{"severity": sev}})
	}
	return newSARIF(n.Tool, rules, results)
}
