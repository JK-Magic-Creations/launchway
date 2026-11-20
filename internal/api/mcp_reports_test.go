package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The MCP tools that read a run's results work with what the schemas after
// them brought: get_test_results reads the richer test model of schema 18,
// and list_artifacts and read_artifact_file read test attachments and an
// HTML report kept in the database store (schema 17), through the same
// routes and permission checks as the API.
func TestMCPReportsInTheDatabaseStore(t *testing.T) {
	e := mcpEnv(t, envOpts{artifacts: &engine.ArtifactOptions{Choice: engine.StoreDatabase}})
	if st, err := e.eng.StoreStatus(t.Context()); err != nil || st.Store != "database" {
		t.Fatalf("store %+v %v", st, err)
	}
	run, _ := runWithReports(t, e.eng, "pay-api")

	vic := mcpSession(t, e.mcpURL(), e.tokenFor(t, "vic", "reports"), nil, nil) // viewer of pay-*
	tests := mustCall(t, vic, "get_test_results", map[string]any{"run_id": run, "status": "all", "include_details": true})
	results, _ := tests["results"].([]any)
	if len(results) != 1 || !strings.Contains(fmt.Sprint(tests["summary"]), "1 failed") {
		t.Fatalf("get_test_results: %v", tests)
	}
	r := results[0].(map[string]any)
	u := r["untrusted"].(map[string]any)
	if r["status"] != "failed" || u["name"] != "n" || u["message"] != "m" || !strings.Contains(fmt.Sprint(u["trace"]), "trace") {
		t.Fatalf("the result: %v", r)
	}

	list := mustCall(t, vic, "list_artifacts", map[string]any{"run_id": run})
	ids := map[string]string{}
	paths := map[string][]string{}
	for _, a := range list["artifacts"].([]any) {
		a := a.(map[string]any)
		ids[a["name"].(string)] = a["id"].(string)
		for _, f := range a["files"].([]any) {
			paths[a["name"].(string)] = append(paths[a["name"].(string)], f.(map[string]any)["path"].(string))
		}
	}
	if ids["test-attachments"] == "" || ids["report-site"] == "" || len(paths["test-attachments"]) != 6 || len(paths["report-site"]) != 2 {
		t.Fatalf("list_artifacts: %v", list)
	}
	pathOf := func(artifact, suffix string) string {
		for _, p := range paths[artifact] {
			if strings.HasSuffix(p, suffix) {
				return p
			}
		}
		t.Fatalf("%s has no %s: %v", artifact, suffix, paths[artifact])
		return ""
	}
	for _, c := range []struct{ artifact, suffix, want string }{
		{"test-attachments", "notes.txt", "plain words\n"},
		{"report-site", "app.js", "console.log('report')"},
	} {
		out := mustCall(t, vic, "read_artifact_file", map[string]any{"run_id": run, "artifact_id": ids[c.artifact], "path": pathOf(c.artifact, c.suffix)})
		if out["untrusted"].(map[string]any)["text"] != c.want || out["more"] != false {
			t.Fatalf("read_artifact_file %s: %v", c.suffix, out)
		}
	}
	if out := mustCall(t, vic, "read_artifact_file", map[string]any{"run_id": run, "artifact_id": ids["test-attachments"],
		"path": pathOf("test-attachments", "shot.png")}); out["binary"] != true {
		t.Fatalf("a PNG read as text: %v", out)
	}

	// Someone who cannot see pay-api gets nothing, as from the API.
	dana := mcpSession(t, e.mcpURL(), e.tokenFor(t, "dana", "reports"), nil, nil) // operator of web only
	for tool, args := range map[string]map[string]any{
		"get_test_results":   {"run_id": run},
		"list_artifacts":     {"run_id": run},
		"read_artifact_file": {"run_id": run, "artifact_id": ids["test-attachments"], "path": pathOf("test-attachments", "notes.txt")},
	} {
		if _, errText := call(t, dana, tool, args); outcome(errText) != mNotFound {
			t.Errorf("dana: %s gave %q", tool, errText)
		}
	}
}
