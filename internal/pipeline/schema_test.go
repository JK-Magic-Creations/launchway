package pipeline

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const schemaFile = "../../docs/public/pipeline.schema.json"

// The schema published in docs/public is the one the parser's key lists make;
// LAUNCHWAY_UPDATE_DOCS=1 rewrites it.
func TestPublishedSchemaIsCurrent(t *testing.T) {
	want, err := json.MarshalIndent(Schema(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("LAUNCHWAY_UPDATE_DOCS") != "" {
		if err := os.WriteFile(schemaFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(schemaFile)
	if err != nil || string(got) != string(want) {
		t.Fatalf("docs/public/pipeline.schema.json is out of date: run LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/pipeline -run TestPublishedSchemaIsCurrent")
	}
}

// Every key the parser accepts in a step is in the schema, and a key it does
// not know is not.
func TestSchemaKnowsTheKeysOfSteps(t *testing.T) {
	s := Schema()
	steps := s["properties"].(obj)["steps"].(obj)["additionalProperties"].(obj)["properties"].(obj)
	for _, k := range append(append([]string{}, stepKeys...), imageKeys...) {
		if _, ok := steps[k]; !ok {
			t.Errorf("the schema lacks the step key %q", k)
		}
	}
	for _, k := range []string{"runs_on", "matrix", "runs-on"} {
		if _, ok := steps[k]; ok {
			t.Errorf("the schema accepts %q, which the parser does not", k)
		}
	}
	top := s["properties"].(obj)
	for _, k := range topKeys {
		if _, ok := top[k]; !ok || !strings.Contains(strings.Join(topKeys, " "), k) {
			t.Errorf("the schema lacks the top-level key %q", k)
		}
	}
}
