package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

func TestInitProposesAFilePipelineThatIsValid(t *testing.T) {
	files, read := map[string]string{"go.mod": "module x\n\ngo 1.26\n", "main.go": "package main\n", "Dockerfile": "FROM scratch\n"}, func(n string) string { return "module x\n\ngo 1.26\n" }
	d := starters.Detect([]string{"go.mod", "main.go", "Dockerfile"}, read)
	_ = files
	if d == nil || d.Flight != "go-service" {
		t.Fatalf("%+v", d)
	}
	text := proposal("My Service!", d)
	if !strings.Contains(text, "pipeline: my-service") || !strings.Contains(text, "uses: launchway/go-service") || !strings.Contains(text, `go_version: "1.26"`) {
		t.Fatalf("%s", text)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateOffline(file, nil, true, pipeline.ProjectFacts{}, nil); err != nil {
		t.Fatalf("the proposed file does not expand: %v", err)
	}
}
