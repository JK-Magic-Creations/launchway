package main

import (
	"strings"
	"testing"
)

// The help is the default words, filled in before it is printed: no placeholder is left, and flags and commands
// keep their plain names.
func TestHelpUsesTheProductWords(t *testing.T) {
	for name, text := range map[string]string{"usage": usage, "runners": runnersUsage, "deployments": deploymentsUsage} {
		if strings.ContainsAny(text, "{}") && name != "usage" {
			t.Errorf("%s help has an unfilled placeholder:\n%s", name, text)
		}
	}
	if !strings.Contains(runnersUsage, "Stations, their pools") || !strings.Contains(runnersUsage, "launchway runners list") {
		t.Errorf("runners help: %s", runnersUsage)
	}
	if !strings.Contains(deploymentsUsage, "a Return to base") || !strings.Contains(deploymentsUsage, "deployments rollback --env E") {
		t.Errorf("deployments help: %s", deploymentsUsage)
	}
}
