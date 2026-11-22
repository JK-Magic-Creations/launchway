package steptest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/executor"
)

// Each kit is tested against the conformance checker: a step scaffolded in the
// language is run as a Station runs it (here by the host executor, so the
// language's tools must be on the machine; a language whose tool is missing is
// skipped) and must keep to the contract, with the outputs, summary and note
// the scaffold writes.
func TestScaffoldedStepsConformInEveryLanguage(t *testing.T) {
	tools := map[string]string{"go": "go", "python": "python3", "ts": "node", "sh": "sh"}
	for _, language := range Languages {
		t.Run(language, func(t *testing.T) {
			if _, err := exec.LookPath(tools[language]); err != nil {
				t.Skipf("%s is not installed", tools[language])
			}
			if language == "ts" {
				out, err := exec.Command("node", "-e", "process.exit(Number(process.versions.node.split('.')[0]) >= 22 ? 0 : 1)").CombinedOutput()
				if err != nil {
					t.Skipf("node is older than 22: %s", out)
				}
			}
			root := t.TempDir()
			dir, err := Scaffold(root, "hello", language)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := Run(context.Background(), Options{Dir: dir, With: map[string]string{"who": "Ada Lovelace"}, Executor: &executor.Host{AllowUnsandboxed: true}})
			if err != nil {
				t.Fatal(err)
			}
			if !rep.OK() {
				t.Fatalf("problems %v, code %d (%v), stdout:\n%s", rep.Problems, rep.ExitCode, rep.Result.Why, rep.Stdout)
			}
			if rep.Result.Values["greeting"] != "Hello, Ada Lovelace!" || rep.Result.Values["length"] != "20" {
				t.Errorf("outputs %v", rep.Result.Values)
			}
			if len(rep.Result.Notes) != 1 || !strings.Contains(rep.Result.Notes[0].Text, "Greeted **Ada Lovelace**") {
				t.Errorf("notes %+v", rep.Result.Notes)
			}
			// The step's own test runs without Launchway.
			own := map[string][]string{"go": {"go", "test", "./..."}, "python": {"python3", "-B", "-m", "unittest"}, "ts": {"node", "--test"}, "sh": {"sh", "test.sh"}}[language]
			cmd := exec.Command(own[0], own[1:]...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("the scaffolded test fails: %v\n%s", err, out)
			}
			// A value that does not fit the manifest is refused before anything runs.
			if _, err := Run(context.Background(), Options{Dir: dir, With: map[string]string{"who": "R2-D2"}, Executor: &executor.Host{AllowUnsandboxed: true}}); err == nil || !strings.Contains(err.Error(), "must match") {
				t.Errorf("a bad value: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".launchway", "step-io", "test")); err == nil {
				t.Error("the step's files were left behind")
			}
		})
	}
}
