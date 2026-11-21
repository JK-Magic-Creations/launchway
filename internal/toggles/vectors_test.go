package toggles

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

const vectorsFile = "../../sdk/vectors/evaluation.json"

type vectorFile struct {
	Hashes []struct {
		Salt, Value string
		Bucket      int
	} `json:"hashes"`
	Cases []struct {
		Name    string  `json:"name"`
		Ruleset Ruleset `json:"ruleset"`
		Toggle  string  `json:"toggle"`
		Context Context `json:"context"`
		Expect  Result  `json:"expect"`
	} `json:"cases"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	b, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The server's evaluator gives every shared test vector's answer. The Go and
// TypeScript SDKs run the same file.
func TestSharedEvaluationVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.Cases) < 60 || len(v.Hashes) == 0 {
		t.Fatalf("the vector file has %d cases and %d hashes", len(v.Cases), len(v.Hashes))
	}
	for _, h := range v.Hashes {
		if got := Bucket(h.Salt, h.Value); got != h.Bucket {
			t.Errorf("Bucket(%q, %q) = %d, the vectors say %d", h.Salt, h.Value, got, h.Bucket)
		}
	}
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := Evaluate(&c.Ruleset, c.Toggle, c.Context)
			if !reflect.DeepEqual(got, c.Expect) {
				t.Fatalf("got %+v, want %+v", got, c.Expect)
			}
		})
	}
}

// Every toggle in the vectors is a definition the server would accept, except
// the cases that are about a malformed ruleset on purpose.
func TestVectorToggleDefinitionsAreValid(t *testing.T) {
	skip := map[string]bool{"a loop of prerequisites is an error and serves the off value": true}
	for _, c := range loadVectors(t).Cases {
		if skip[c.Name] {
			continue
		}
		for k, f := range c.Ruleset.Toggles {
			st := f.State
			if err := CheckState(f.Type, &st); err != nil {
				t.Errorf("%s: toggle %s: %v", c.Name, k, err)
			}
		}
		for n, s := range c.Ruleset.Segments {
			if err := CheckSegment(n, s); err != nil {
				t.Errorf("%s: segment %s: %v", c.Name, n, err)
			}
		}
	}
}
