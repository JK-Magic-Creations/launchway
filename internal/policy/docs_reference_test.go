package policy

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// docsPolicyRules is the reference page that lists the rule catalogue.
const docsPolicyRules = "../../docs/reference/policy-rules.md"

// docsPointText is how the page says when a rule is judged.
var docsPointText = map[string]string{
	PointRun:    "run start",
	PointDeploy: "before deploying",
	PointImage:  "before an image push",
}

// TestDocsReferencePolicyRules fails when the rule catalogue and the
// catalogue table in docs/reference/policy-rules.md disagree: a rule
// missing from the page, a row naming a rule that does not exist, or a
// rule documented at the wrong point. The convention is described in an
// HTML comment at the top of the page.
func TestDocsReferencePolicyRules(t *testing.T) {
	data, err := os.ReadFile(docsPolicyRules)
	if err != nil {
		t.Fatalf("reading the reference page: %v", err)
	}
	text := string(data)
	start := strings.Index(text, "<!-- rule-catalogue:start -->")
	end := strings.Index(text, "<!-- rule-catalogue:end -->")
	if start < 0 || end < start {
		t.Fatalf("%s has no rule-catalogue:start / rule-catalogue:end markers around the catalogue table", docsPolicyRules)
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|([^|]*)\\|([^|]*)\\|")
	documented := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(text[start:end], -1) {
		if _, dup := documented[m[1]]; dup {
			t.Errorf("%s lists rule %s twice", docsPolicyRules, m[1])
		}
		documented[m[1]] = strings.TrimSpace(m[3])
	}
	for _, r := range Catalogue {
		point, ok := documented[r.Name]
		if !ok {
			t.Errorf("rule %s is in the catalogue but not in the table of %s; add a row \"| `%s` | ... | %s | ... |\"",
				r.Name, docsPolicyRules, r.Name, docsPointText[r.Point])
			continue
		}
		if want := docsPointText[r.Point]; point != want {
			t.Errorf("%s says rule %s is judged %q; it is judged %q", docsPolicyRules, r.Name, point, want)
		}
		delete(documented, r.Name)
	}
	for name := range documented {
		t.Errorf("%s lists rule %s, which the catalogue does not have", docsPolicyRules, name)
	}
}
