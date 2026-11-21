package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Build caches were one file of nearly 1,500 lines. The size ratchet of the
// quality gates refuses a new file over 1,000 lines and any growth of the
// big files the feature hooks into; this keeps the feature in files of a
// size a person can review, and the hooks to what they were.
func TestCacheSourceStaysInSmallFiles(t *testing.T) {
	files, err := filepath.Glob("cache*.go")
	if err != nil || len(files) < 6 {
		t.Fatalf("%v %v", files, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(src), "\n"); n > 700 {
			t.Errorf("%s has %d lines; split it by responsibility", f, n)
		}
	}
}
