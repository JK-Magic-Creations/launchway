package findings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.sarif"))
	for _, name := range files {
		if b, err := os.ReadFile(name); err == nil && len(b) < 100<<10 {
			f.Add(b)
		}
	}
	for _, s := range []string{
		"",
		`{"runs":[]}`,
		`{"version":"2.1.0","runs":[{"results":[{"ruleIndex":0,"message":{"text":"a\u0000"}}],"tool":{"driver":{"name":"gitleaks","rules":[{"id":"x"}]}}}]}`,
		`{"runs":[{"tool":{"extensions":[{"rules":[{"id":"e"}]}]},"results":[{"rule":{"index":0,"toolComponent":{"index":0}},"partialFingerprints":{"a":"b"},"suppressions":[{}]}]}]}`,
		`{"runs":[{"results":[{"ruleId":"r","ruleId":"s"},[[[[[[]]]]]],{"locations":[{"logicalLocations":[{"name":"n"}]}]}]}]}`,
		`{"runs":[{"originalUriBaseIds":{"S":{"uri":"file:///w/"}},"results":[{"locations":[{"physicalLocation":{"artifactLocation":{"uri":"file:///w/%2e%2e/x"}}}]}]}]}`,
		"{\"runs\":[{\"results\":[{\"message\":{\"text\":\"password=hunter2 AKIA2E0A8F3B244C9986 \xff\"}}]}]}",
	} {
		f.Add([]byte(s))
	}
	lim := Limits{MaxFindings: 20, MaxRules: 20, MaxMessageBytes: 200, MaxHelpBytes: 300, MaxPathBytes: 50, MaxNameBytes: 40, MaxDepth: 20}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Parse(context.Background(), data, lim)
		if err != nil {
			if errors.Is(err, ErrNotSARIF) && !strings.HasPrefix(err.Error(), "not a SARIF report: ") {
				t.Errorf("error = %q", err)
			}
			checkString(t, "error", err.Error(), 1000, false)
			return
		}
		checkReport(t, r, lim)
	})
}
