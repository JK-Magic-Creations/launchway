package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// runs show says what the server reports the caller may do, and for what
// they may not, why, what to do instead and what would help.
func TestPrintActionsSaysWhyNot(t *testing.T) {
	var b bytes.Buffer
	printActions(&b, []engine.Action{
		{Name: "rerun", Available: true, Who: "operator"},
		{Name: "resume", Reason: "deploy-sandbox made a deployment in this run", Instead: "rerun from deploy-dev", WouldHelp: "ask an administrator", Who: "operator"},
	})
	got := b.String()
	for _, want := range []string{"can rerun", "cannot resume: deploy-sandbox made a deployment", "instead: rerun from deploy-dev", "would help: ask an administrator"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}
