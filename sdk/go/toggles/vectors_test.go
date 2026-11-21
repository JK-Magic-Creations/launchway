package toggles

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

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

// The SDK's evaluator gives every shared test vector's answer, the same file
// the server and the TypeScript SDK run.
func TestSharedEvaluationVectors(t *testing.T) {
	b, err := os.ReadFile("../../vectors/evaluation.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Cases) < 60 {
		t.Fatalf("%d cases", len(v.Cases))
	}
	for _, h := range v.Hashes {
		if got := Bucket(h.Salt, h.Value); got != h.Bucket {
			t.Errorf("Bucket(%q, %q) = %d, the vectors say %d", h.Salt, h.Value, got, h.Bucket)
		}
	}
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := Evaluate(&c.Ruleset, c.Toggle, c.Context); !reflect.DeepEqual(got, c.Expect) {
				t.Fatalf("got %+v, want %+v", got, c.Expect)
			}
		})
	}
}
