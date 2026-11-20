package pipeline

import (
	"strings"
	"testing"
)

// Every worked example is a valid pipeline, as the parser reads it.
func TestGuideExamplesAreValid(t *testing.T) {
	examples := Examples()
	if len(examples) < 4 {
		t.Fatalf("only %d examples", len(examples))
	}
	for _, ex := range examples {
		if ex.Summary == "" {
			t.Errorf("%s: no comment at the top saying what it shows", ex.Name)
		}
		if _, err := Parse([]byte(ex.Text)); err != nil {
			t.Errorf("%s: %v", ex.Name, err)
		}
	}
}

// The reference names every key the parser accepts, so it cannot fall
// behind the format without this test failing.
func TestGuideReferenceNamesEveryKey(t *testing.T) {
	ref := Reference()
	lists := map[string][]string{
		"top level":  {"pipeline", "on", "supersede", "libraries", "containers", "steps", "inputs"},
		"step":       stepKeys,
		"use":        useKeys,
		"container":  containerKeys,
		"secret":     secretKeys,
		"input":      inputKeys,
		"loop":       loopKeys,
		"schedule":   scheduleKeys,
		"template":   templateKeys,
		"image":      imageKeys,
		"deploy":     deployKeys,
		"rollback":   rollbackKeys,
		"rollout":    rolloutStepKeys,
		"argocd":     argocdKeys,
		"write_back": writeKeys,
		"change":     changeKeys,
		"sync":       syncKeys,
	}
	for what, keys := range lists {
		for _, k := range keys {
			if !strings.Contains(ref, "`"+k+"`") && !strings.Contains(ref, k+":") {
				t.Errorf("the reference does not mention the %s key %q", what, k)
			}
		}
	}
	// Every YAML block in the reference that is a whole pipeline parses.
	for i, block := range strings.Split(ref, "```yaml\n")[1:] {
		text, _, _ := strings.Cut(block, "```")
		if !strings.HasPrefix(text, "pipeline:") {
			continue
		}
		if _, err := Parse([]byte(text)); err != nil {
			t.Errorf("block %d of the reference: %v", i, err)
		}
	}
}
