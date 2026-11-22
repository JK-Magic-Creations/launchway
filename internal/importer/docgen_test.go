package importer

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var updateDocs = flag.Bool("update-docs", false, "rewrite the generated tables of the guide")

const guidePage = "../../docs/guide/move-your-pipelines.md"

// The guide's tables are made from the readers' tables; this fails when they differ.
func TestGuide(t *testing.T) {
	b, err := os.ReadFile(guidePage)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	i, j := strings.Index(text, GuideStart), strings.Index(text, GuideEnd)
	if i < 0 || j < i {
		t.Fatalf("%s has no generated section between the markers", guidePage)
	}
	want := text[:i] + GuideTables() + text[j+len(GuideEnd)+1:]
	if *updateDocs {
		if err := os.WriteFile(guidePage, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want != text {
		t.Errorf("%s is not what the readers' tables say: run go test ./internal/importer -run TestGuide -update-docs", guidePage)
	}
}
