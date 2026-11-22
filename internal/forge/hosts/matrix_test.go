package hosts_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
)

var updateMatrix = flag.Bool("update", false, "rewrite docs/guide/outside-host-matrix.md from the declarations")

const matrixPage = "../../../docs/guide/outside-host-matrix.md"

func TestMatrixCoversEveryKindAndFeature(t *testing.T) {
	m := hosts.Matrix()
	if len(m.Features) != len(forge.AllCapabilities) {
		t.Fatalf("features %d, want %d", len(m.Features), len(forge.AllCapabilities))
	}
	for _, f := range m.Features {
		if f.Title == string(f.Key) {
			t.Errorf("capability %s has no plain title", f.Key)
		}
	}
	have := map[forge.Kind]bool{}
	for _, k := range m.Kinds {
		have[k.Kind] = true
	}
	for _, k := range hosts.Kinds() {
		// The launchway kind is a test double of the fallback tests, with no declaration.
		if !have[k] && k != forge.KindLaunchway {
			t.Errorf("kind %s has no Caps in its entry", k)
		}
	}
	for _, k := range m.Kinds {
		if k.Label == "" {
			t.Errorf("%s has no label", k.Kind)
		}
		if err := forge.Capabilities(k.Capabilities).Validate(); err != nil {
			t.Errorf("%s: %v", k.Kind, err)
		}
	}
}

// The page is generated: it fails when it drifts from the declarations.
func TestMatrixPageMatchesDeclarations(t *testing.T) {
	want := hosts.Matrix().Markdown()
	if *updateMatrix {
		if err := os.WriteFile(matrixPage, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(matrixPage)
	if err != nil {
		t.Fatalf("%v: generate it with `make docs-matrix`", err)
	}
	if string(got) != want {
		t.Fatalf("%s is out of date with the providers' declarations; regenerate it with `make docs-matrix` (go test ./internal/forge/hosts -run TestMatrixPage -update) and commit it", filepath.Clean(matrixPage))
	}
}
