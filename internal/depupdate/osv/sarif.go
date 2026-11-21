package osv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// ToolName is the tool the findings are attributed to.
const ToolName = "launchway-dependencies"

// SARIF renders matches as a SARIF 2.1.0 document, so the findings go the
// way every scanner's do: a pipeline step publishes the file and the Security
// tab shows them, new and fixed counted by their fingerprints. A known
// vulnerable dependency whose fix is out is named with the fixed version.
func SARIF(matches []Match) ([]byte, error) {
	type loc struct {
		PhysicalLocation map[string]any `json:"physicalLocation"`
	}
	rules := []map[string]any{}
	seenRule := map[string]bool{}
	results := []map[string]any{}
	for _, m := range matches {
		if !seenRule[m.ID] {
			seenRule[m.ID] = true
			r := map[string]any{
				"id":               m.ID,
				"shortDescription": map[string]string{"text": m.Summary},
				"properties":       map[string]any{"security-severity": strconv.FormatFloat(m.Score, 'f', 1, 64), "tags": []string{"vulnerability", "dependency"}},
			}
			if m.URL != "" {
				r["helpUri"] = m.URL
			}
			rules = append(rules, r)
		}
		msg := fmt.Sprintf("%s %s is affected by %s", m.Dep.Name, m.Dep.Current, m.ID)
		if m.Summary != "" {
			msg += ": " + m.Summary
		}
		if m.FixedIn != "" {
			msg += ". Fixed in " + m.FixedIn + "."
		} else {
			msg += ". No fixed version is published yet."
		}
		line := m.Dep.Line
		if line < 1 {
			line = 1
		}
		sum := sha256.Sum256([]byte(m.Dep.Ecosystem + "|" + m.Dep.Name + "|" + m.ID + "|" + m.Dep.File))
		results = append(results, map[string]any{
			"ruleId":  m.ID,
			"level":   level(m.Severity),
			"message": map[string]string{"text": msg},
			"locations": []loc{{map[string]any{
				"artifactLocation": map[string]string{"uri": m.Dep.File},
				"region":           map[string]int{"startLine": line},
			}}},
			"partialFingerprints": map[string]string{"launchwayDependency/v1": hex.EncodeToString(sum[:16])},
			"properties":          map[string]any{"fixedIn": m.FixedIn, "package": m.Dep.Name, "version": m.Dep.Current, "ecosystem": m.Dep.Ecosystem},
		})
	}
	doc := map[string]any{
		"version": "2.1.0",
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"runs": []any{map[string]any{
			"tool":    map[string]any{"driver": map[string]any{"name": ToolName, "rules": rules}},
			"results": results,
		}},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func level(sev string) string {
	switch sev {
	case "critical", "high":
		return "error"
	case "low":
		return "note"
	}
	return "warning"
}
