package standards

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Flight plans of the starter library name their secret scan and their Go vulnerability check
// inspect-<check>, so that Standing orders keep them (docs/reference/standing-orders.md). For the
// Inspection's verdict to be able to read what they find, they must be the server's own step: the same
// script, the same image and the same report path. The scripts are copied from here into the library;
// UPDATE_STARTER_SCRIPTS=1 go test ./internal/standards -run TestStarterStepsAre... rewrites them.
func TestStarterStepsAreTheServersOwn(t *testing.T) {
	lib := filepath.Join("..", "starters", "library")
	for _, id := range []string{"secrets", "vuln-reach-go"} {
		c, ok := CheckByID(id)
		if !ok {
			t.Fatal(id)
		}
		path := filepath.Join(lib, "scripts", "inspect-"+id+".sh")
		want := "#!/bin/sh\n# The script of the check " + id + " of Standing orders, byte for byte (generated; see internal/standards/starters_wiring_test.go).\n" + c.Script
		if os.Getenv("UPDATE_STARTER_SCRIPTS") != "" {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s is not the script of the check %s: run UPDATE_STARTER_SCRIPTS=1 go test ./internal/standards -run TestStarterStepsAreTheServersOwn (%v)", path, id, err)
		}
	}
	tool, _ := ToolNamed("gitleaks")
	secrets, _ := CheckByID("secrets")
	reach, _ := CheckByID("vuln-reach-go")
	templates, _ := filepath.Glob(filepath.Join(lib, "templates", "*.yaml"))
	seenSecrets, seenReach := 0, 0
	for _, f := range templates {
		b, _ := os.ReadFile(f)
		text := string(b)
		if strings.Contains(text, "  inspect-secrets:") {
			seenSecrets++
			for _, want := range []string{"image: " + tool.Image, "reports: { findings: [" + secrets.Outputs[0] + "] }", "scripts/inspect-secrets.sh", "on_failure: continue"} {
				if !strings.Contains(text, want) {
					t.Errorf("%s: the step inspect-secrets lacks %q", f, want)
				}
			}
		}
		if strings.Contains(text, "  inspect-vuln-reach-go:") {
			seenReach++
			for _, want := range []string{`reports: { findings: ["` + reach.Outputs[0] + `"] }`, "scripts/inspect-vuln-reach-go.sh", "on_failure: continue"} {
				if !strings.Contains(text, want) {
					t.Errorf("%s: the step inspect-vuln-reach-go lacks %q", f, want)
				}
			}
		}
		if strings.Contains(text, "scripts/secrets.sh") && !strings.Contains(f, "secret-scan") {
			t.Errorf("%s still runs the old secret scan beside the Inspection's", f)
		}
	}
	if seenSecrets < 10 || seenReach != 2 {
		t.Errorf("the Flight plans with an inspect-secrets step: %d, with inspect-vuln-reach-go: %d", seenSecrets, seenReach)
	}
}
