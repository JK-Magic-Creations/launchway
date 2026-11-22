package lwstep

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, inputs map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	doc, _ := json.Marshal(map[string]any{"contract": 1, "inputs": inputs})
	os.WriteFile(filepath.Join(dir, "inputs.json"), doc, 0o644)
	os.WriteFile(filepath.Join(dir, "results.ndjson"), nil, 0o644)
	t.Setenv("LAUNCHWAY_STEP_INPUTS", filepath.Join(dir, "inputs.json"))
	t.Setenv("LAUNCHWAY_STEP_OUTPUTS", filepath.Join(dir, "outputs.json"))
	t.Setenv("LAUNCHWAY_STEP_RESULTS", filepath.Join(dir, "results.ndjson"))
	return dir
}

func TestInputsAreTypedAndOutputsAndResultsGoToTheContractsFiles(t *testing.T) {
	dir := setup(t, map[string]any{"who": "Ada", "n": 3, "on": true, "files": []string{"a", "b"}, "meta": map[string]any{"k": 1}, "token": map[string]string{"secret": "LW_TEST_TOKEN"}})
	t.Setenv("LW_TEST_TOKEN", "s3cr3t-value")
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if s.String("who") != "Ada" || s.Number("n") != 3 || !s.Bool("on") || len(s.List("files")) != 2 || s.Record("meta")["k"] != float64(1) || s.Secret("token") != "s3cr3t-value" || s.String("absent") != "" {
		t.Fatal("inputs")
	}
	s.Output("greeting", "hi")
	s.Output("count", 2)
	s.Summary("**done**")
	s.Annotate("warning", "a.txt", 3, "look")
	s.Finding("R1", "high", "a.txt", 3, "bad")
	s.Test("t1", "passed", 12, "")
	s.Mask("hunter2-value")
	if code := s.finish(nil); code != 0 {
		t.Fatal(code)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "outputs.json"))
	if string(out) != `{"count":2,"greeting":"hi"}` {
		t.Errorf("%s", out)
	}
	res, _ := os.ReadFile(filepath.Join(dir, "results.ndjson"))
	lines := strings.Split(strings.TrimSpace(string(res)), "\n")
	if len(lines) != 5 || !strings.Contains(lines[0], `"t":"summary"`) || !strings.Contains(lines[4], `"t":"mask"`) {
		t.Errorf("%q", lines)
	}
}

func TestExitCodesMeanWhatTheContractSays(t *testing.T) {
	setup(t, nil)
	s, _ := Open()
	if got := s.finish(errors.New("boom")); got != 1 {
		t.Errorf("failed: %d", got)
	}
	if got := s.finish(ErrAllowed); got != 64 {
		t.Errorf("allowed: %d", got)
	}
	if got := s.finish(NeedsPerson("approve?")); got != 65 {
		t.Errorf("needs a person: %d", got)
	}
	if got := s.finish(nil); got != 0 {
		t.Errorf("passed: %d", got)
	}
}

func TestEnvironLeavesOutWhatLetsAToolWriteResults(t *testing.T) {
	setup(t, nil)
	for _, e := range Environ() {
		if strings.HasPrefix(e, "LAUNCHWAY_STEP_RESULTS=") || strings.HasPrefix(e, "LAUNCHWAY_STEP_OUTPUTS=") {
			t.Errorf("%s", e)
		}
	}
}
