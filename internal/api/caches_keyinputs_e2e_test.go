package api

import (
	"strings"
	"testing"
)

// The key is made of more than the author's files: a version command's
// output is part of it, so that a cache made under one tool version is not
// restored under another, and the run record says what the key is made of.
func TestCacheKeyIncludesVersionInputsAndSaysSo(t *testing.T) {
	ce := newCacheEnv(t)
	withVersion := func(cmd string) string {
		return strings.Replace(cachePipelineText("deps", "none", "success", ""), "key: { files: [requirements.txt], text: v1 }",
			"key: { files: [requirements.txt], versions: ['"+cmd+"'] }", 1)
	}
	sha := ce.head(t)
	d, log := ce.runWith(t, "demo", withVersion("echo jdk-17"), pushMain(sha))
	if d.Status != "succeeded" || !strings.Contains(log, "Cache deps: saved ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	first := cachesOf(t, d)[0]
	var versionPart string
	for _, p := range first.KeyParts {
		if strings.HasPrefix(p.Label, "version command: echo jdk-17") {
			versionPart = p.Value
		}
	}
	if versionPart != "jdk-17" {
		t.Fatalf("the run record does not show the version input: %+v", first.KeyParts)
	}
	// Same inputs: a hit.
	d, log = ce.runWith(t, "demo", withVersion("echo jdk-17"), pushMain(sha))
	if r := cachesOf(t, d)[0]; r.Result != "hit" || r.Key != first.Key {
		t.Fatalf("%+v\n%s", r, log)
	}
	// The tool changed under the same lock file: not the same entry.
	d, log = ce.runWith(t, "demo", withVersion("echo jdk-21"), pushMain(sha))
	if r := cachesOf(t, d)[0]; r.Result == "hit" || r.Restored == "hit" || r.Key == first.Key || !strings.Contains(log, "found nothing") {
		t.Fatalf("a different tool version restored the old entry: %+v\n%s", r, log)
	}
}
