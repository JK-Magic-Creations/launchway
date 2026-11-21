package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/parity"
)

// Every command line the parity table names exists: its first word is a
// command of main, and the rest appears in that command's usage text.
func TestParityCLICommandsExist(t *testing.T) {
	usages := usage + configUsage + clientUsage + verifyUsage + policyUsage + runnersUsage + blobsUsage + stepResourcesUsage + variablesUsage + keysUsage + reposUsage + hooksUsage + reposKeysUsage + deploymentsUsage + prUsage + encryptionUsage + depsUsage
	for _, r := range parity.Rows() {
		for _, c := range r.Commands() {
			words := strings.Fields(c)
			if !regexp.MustCompile(`launchway [a-z|-]*\b` + regexp.QuoteMeta(words[0]) + `\b`).MatchString(usages) {
				t.Errorf("%s: the command %q does not exist (no \"launchway %s\" in the usage)", r.Route, c, words[0])
				continue
			}
			for _, w := range words[1:] {
				if !strings.Contains(usages, w) {
					t.Errorf("%s: the command %q: %q is in no usage text", r.Route, c, w)
				}
			}
		}
	}
}
