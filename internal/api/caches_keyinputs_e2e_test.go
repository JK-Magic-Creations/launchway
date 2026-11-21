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

// A step that rewrites a file its key is made of (an install that updates a
// lock file) saves under the key of the files as it left them, never under
// the key from before it: a later run with the old file must not restore
// what was built for the new one.
func TestCacheIsSavedUnderTheKeyOfTheFilesTheStepLeft(t *testing.T) {
	ce := newCacheEnv(t)
	text := cachePipelineText("deps", "none", "success", "      echo 'pinned==2' >> requirements.txt\n")
	sha1 := ce.head(t)
	d, log := ce.runWith(t, "demo", text, pushMain(sha1))
	if d.Status != "succeeded" || !strings.Contains(log, "Cache deps: the step changed its key files (requirements.txt)") || !strings.Contains(log, "Cache deps: saved ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	saved := cachesOf(t, d)[0]
	// The same checkout again: its key is the key from before the step, and
	// there is no entry under it.
	d, log = ce.runWith(t, "demo", cachePipelineText("deps", "none", "success", ""), pushMain(sha1))
	if r := cachesOf(t, d)[0]; r.Result == "hit" || r.Restored == "hit" || r.Key == saved.Key || !strings.Contains(log, "found nothing") {
		t.Fatalf("the old lock file restored what was built for the new one: %+v\n%s", r, log)
	}
	// A checkout whose file is what the step left is the entry's key.
	sha2 := ce.commit(t, "requirements.txt", "requests==1\npinned==2\n")
	d, log = ce.runWith(t, "demo", cachePipelineText("deps", "none", "success", ""), pushMain(sha2))
	if r := cachesOf(t, d)[0]; r.Result != "hit" || r.Key != saved.Key || !strings.Contains(log, "found n=1") {
		t.Fatalf("%+v\n%s", r, log)
	}
}
