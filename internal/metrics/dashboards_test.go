package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var metricName = regexp.MustCompile(`launchway_[a-z0-9_]+`)

// The dashboards and the alert rules under deploy/dashboards name only metrics the registry declares
// (a histogram by its _bucket, _sum or _count), so they cannot drift from the code.
func TestDashboardsAndRulesUseOnlyDeclaredMetrics(t *testing.T) {
	declared := map[string]bool{}
	for _, d := range Default.Descs() {
		declared[d.Name] = true
		if d.Kind == Histogram {
			for _, s := range []string{"_bucket", "_sum", "_count"} {
				declared[d.Name+s] = true
			}
		}
	}
	files, _ := filepath.Glob("../../deploy/dashboards/*.json")
	rules, _ := filepath.Glob("../../deploy/dashboards/alerts/*.yaml")
	if len(files) != 3 || len(rules) != 3 {
		t.Fatalf("expected three dashboards and three rule files, found %d and %d", len(files), len(rules))
	}
	for _, f := range append(files, rules...) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(f, ".json") {
			var doc struct {
				UID    string           `json:"uid"`
				Title  string           `json:"title"`
				Panels []map[string]any `json:"panels"`
			}
			if err := json.Unmarshal(b, &doc); err != nil || doc.UID == "" || doc.Title == "" || len(doc.Panels) == 0 {
				t.Errorf("%s is not a dashboard: %v", f, err)
			}
		} else {
			var doc struct {
				Groups []struct {
					Rules []struct{ Alert, Expr string }
				}
			}
			if err := yaml.Unmarshal(b, &doc); err != nil || len(doc.Groups) == 0 || len(doc.Groups[0].Rules) == 0 {
				t.Errorf("%s is not a rule file: %v", f, err)
			}
			for _, g := range doc.Groups {
				for _, r := range g.Rules {
					if r.Alert == "" || r.Expr == "" {
						t.Errorf("%s: a rule without a name or an expression", f)
					}
				}
			}
		}
		for _, name := range metricName.FindAllString(string(b), -1) {
			if !declared[name] {
				t.Errorf("%s names the metric %s, which the registry does not declare", f, name)
			}
		}
	}
}
