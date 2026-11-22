package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportPipelineCommand(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	old := "on: push\njobs:\n  deploy:\n    runs-on: ubuntu-latest\n    environment: prod\n    steps:\n      - run: kubectl apply -f k.yaml\n      - uses: some/vendor-action@v1\n"
	if err := os.WriteFile(filepath.Join(wf, "ci.yml"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := cli(t, dir, nil, "import", "pipeline", "--dry", "--report", "report.md")
	if code != 0 || !strings.Contains(out, "enable_delivery") || !strings.Contains(out, "needs a person") && !strings.Contains(out, "Needs a person") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".launchway")); err == nil {
		t.Error("printing wrote files")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "report.md")); err != nil || !strings.Contains(string(b), "third-party") {
		t.Errorf("report: %v %s", err, b)
	}
	out, errs, code := cli(t, dir, nil, "import", "pipeline", "--dry", "--write")
	if code != 0 {
		t.Fatalf("write: exit %d\n%s\n%s", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, ".launchway", "pipeline.yaml")); err != nil {
		t.Error("the pipeline was not written")
	}
	if b, _ := os.ReadFile(filepath.Join(wf, "ci.yml")); string(b) != old {
		t.Error("the old file was changed")
	}
	// A second write refuses to replace it.
	out, _, _ = cli(t, dir, nil, "import", "pipeline", "--write")
	if !strings.Contains(out, "already exists") {
		t.Errorf("replaced without --force:\n%s", out)
	}
	// Nothing to import is exit 1 with the known formats.
	out, _, code = cli(t, t.TempDir(), nil, "import", "pipeline")
	if code != 1 || !strings.Contains(out, "hosted-workflows") {
		t.Errorf("empty dir: exit %d\n%s", code, out)
	}
}
