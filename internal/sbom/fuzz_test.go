package sbom

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.json"))
	for _, name := range files {
		if b, err := os.ReadFile(name); err == nil {
			f.Add(b)
		}
	}
	for _, s := range []string{
		"",
		`{"bomFormat":"CycloneDX"}`,
		`{"bomFormat":"CycloneDX","components":[{"name":"a","group":"g","components":[{"name":"b","licenses":[{"expression":"MIT"}]}]}]}`,
		`{"bomFormat":"CycloneDX","components":[{"name":"a","name":"b"},[[[[]]]],{"name":"\u0000\u001b[1m"}]}`,
		`{"spdxVersion":"SPDX-2.3"}`,
	} {
		f.Add([]byte(s))
	}
	lim := Limits{MaxComponents: 20, MaxDepth: 20, MaxNameBytes: 40}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Parse(context.Background(), data, lim)
		if err != nil {
			if errors.Is(err, ErrNotSBOM) && !strings.HasPrefix(err.Error(), "not a CycloneDX SBOM: ") {
				t.Errorf("error = %q", err)
			}
			checkString(t, "error", err.Error(), 1000)
			return
		}
		checkReport(t, r, lim)
	})
}
