package config

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The chart cannot warn about a values key it does not know from its schema (the schema is open, so
// a newer chart's values still render under an older chart), so the templates compare the values
// against known-values.json. That file is generated from values.schema.json: this test fails when
// it is stale, and UPDATE_KNOWN_VALUES=1 go test -run TestKnownValuesCurrent ./internal/config
// rewrites it.
const (
	schemaPath      = "../../deploy/helm/launchway/values.schema.json"
	knownValuesPath = "../../deploy/helm/launchway/known-values.json"
)

func resolveSchema(root, s map[string]any) map[string]any {
	for i := 0; i < 8; i++ {
		ref, ok := s["$ref"].(string)
		if !ok {
			break
		}
		name := ref[len("#/definitions/"):]
		s = root["definitions"].(map[string]any)[name].(map[string]any)
	}
	return s
}

// skeleton maps each known key of an object schema to its own skeleton, or true for a value that is
// not walked (a scalar, a list, a free-form map).
func skeleton(root, s map[string]any) any {
	s = resolveSchema(root, s)
	props, _ := s["properties"].(map[string]any)
	if len(props) == 0 {
		return true
	}
	out := map[string]any{}
	for k, v := range props {
		out[k] = skeleton(root, v.(map[string]any))
	}
	return out
}

func TestKnownValuesCurrent(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(mustRead(t, schemaPath), &schema); err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(skeleton(schema, schema), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_KNOWN_VALUES") == "1" {
		if err := os.WriteFile(knownValuesPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got := mustRead(t, knownValuesPath)
	var a, b any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal(want, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("deploy/helm/launchway/known-values.json is out of date with values.schema.json: run UPDATE_KNOWN_VALUES=1 go test -run TestKnownValuesCurrent ./internal/config")
	}
}
