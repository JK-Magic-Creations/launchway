package pipeline

import (
	"os"
	"testing"
)

// Launchway's own pipeline: a push that changes only documentation builds the
// docs and runs nothing else (no image, no deploy approvals); a push that
// changes code runs everything as before.
func TestOwnPipelineSkipsEverythingButDocsForDocsOnlyChanges(t *testing.T) {
	data, err := os.ReadFile("../../.launchway/pipeline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx := func(files ...string) Context {
		return Context{Event: EventPush, Branch: "main", Changed: &ChangeSet{Known: true, Files: files}}
	}
	docsOnly := ctx("docs/guide/queues.md", "docs/roadmap/roadmap.json", "README.md")
	code := ctx("docs/guide/queues.md", "internal/engine/queues.go")
	for _, s := range p.Steps {
		runsDocs := s.When.Match(docsOnly)
		runsCode := s.When.Match(code)
		switch {
		case s.ID == "docs":
			if !runsDocs || !runsCode {
				t.Errorf("the docs step must always run")
			}
		case s.IsRollback():
			// A rollback runs only when its deployment fails.
		default:
			if runsDocs {
				t.Errorf("step %s runs for a docs-only push", s.ID)
			}
			if !runsCode && s.When != nil && len(s.When.Branch) == 0 {
				t.Errorf("step %s does not run for a code push", s.ID)
			}
		}
	}
	for _, id := range []string{"image", "approve-sandbox", "approve-dev", "deploy-dev"} {
		if p.Step(id).When.Match(docsOnly) {
			t.Errorf("%s would run for a docs-only push", id)
		}
		if !p.Step(id).When.Match(code) {
			t.Errorf("%s would not run for a code push to main", id)
		}
	}
}
